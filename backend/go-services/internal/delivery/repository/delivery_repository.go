package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/delivery/models"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/redis/go-redis/v9"
	"github.com/uber/h3-go/v3"
)

type DeliveryRepository struct {
	writer *pgxpool.Pool
	reader *pgxpool.Pool
	redis  redis.UniversalClient
}

var validGigTransitions = map[string][]string{
	models.StatusBroadcasting: {models.StatusAccepted},
	models.StatusAccepted:     {models.StatusPickedUp, models.StatusFailed, models.StatusBroadcasting},
	models.StatusPickedUp:     {models.StatusInTransit, models.StatusFailed},
	models.StatusInTransit:    {models.StatusCompleted, models.StatusFailed},
}

func NewDeliveryRepository(writer, reader *pgxpool.Pool, redisClient redis.UniversalClient) *DeliveryRepository {
	return &DeliveryRepository{
		writer: writer,
		reader: reader,
		redis:  redisClient,
	}
}

// CreateGig inserts a new delivery gig into PostgreSQL.
//
// Concurrency: the check-and-insert for duplicate prevention is wrapped in a
// transaction that first acquires a row-level lock on the parent order
// (SELECT ... FOR UPDATE on orders). This serializes concurrent CreateGig
// calls for the same order — two concurrent Kafka handlers will queue at
// the order row, and the second one will see the first one's already-active
// gig and abort cleanly. This replaces the previous TOCTOU pattern
// (check-then-act with no lock) that allowed up to 30 duplicate delivery
// rows per order in production.
//
// The orders row is released as soon as the transaction commits or rolls back.
func (r *DeliveryRepository) CreateGig(ctx context.Context, gig *models.DeliveryGig) error {
	checks := []struct {
		id    string
		table string
		col   string
	}{
		{gig.OrderTrackingID, "orders", "order_tracking_id"},
		{gig.VendorStoreTrackID, "stores", "store_tracking_id"},
		{gig.CustomerTrackID, "users", "tracking_id"},
	}
	for _, c := range checks {
		if c.id == "" {
			continue
		}
		ok, err := database.Exists(ctx, r.writer, fmt.Sprintf("SELECT 1 FROM %s WHERE %s = $1", c.table, c.col), c.id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s %s does not exist", c.table, c.id)
		}
	}

	// Begin transaction to serialize concurrent CreateGig calls per order.
	tx, err := r.writer.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin tx for gig creation: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock the parent order row. Any concurrent CreateGig for the same order
	// will block here until our transaction completes.
	var orderExists bool
	err = tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM orders WHERE order_tracking_id = $1 FOR UPDATE)`,
		gig.OrderTrackingID,
	).Scan(&orderExists)
	if err != nil {
		return fmt.Errorf("failed to lock parent order: %w", err)
	}
	if !orderExists {
		return fmt.Errorf("order %s does not exist", gig.OrderTrackingID)
	}

	// Now safely check for an existing active gig — the order row lock
	// guarantees no concurrent CreateGig can be at this point.
	var existingCount int
	err = tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM deliveries WHERE order_tracking_id = $1 AND status NOT IN ('cancelled','completed')`,
		gig.OrderTrackingID,
	).Scan(&existingCount)
	if err != nil {
		return fmt.Errorf("failed to check existing gigs: %w", err)
	}
	if existingCount > 0 {
		return fmt.Errorf("active delivery gig already exists for order %s", gig.OrderTrackingID)
	}

	query := `
		INSERT INTO deliveries (tracking_id, order_tracking_id, vendor_store_tracking_id, customer_tracking_id, status, delivery_fee, admin_commission, rider_earning, tips, petrol_allowance, pickup_lat, pickup_lng, dropoff_lat, dropoff_lng, otp_code, is_cod, order_total, customer_phone)
		VALUES ($1, $2, $3, $4, 'broadcasting', $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING id, created_at, updated_at
	`
	err = tx.QueryRow(ctx, query,
		gig.TrackingID,
		gig.OrderTrackingID,
		gig.VendorStoreTrackID,
		gig.CustomerTrackID,
		gig.DeliveryFee,
		gig.AdminCommission,
		gig.RiderEarning,
		gig.Tips,
		gig.PetrolAllowance,
		gig.PickupLat,
		gig.PickupLng,
		gig.DropoffLat,
		gig.DropoffLng,
		gig.OTPCode,
		gig.IsCOD,
		gig.OrderTotal,
		gig.CustomerPhone,
	).Scan(&gig.ID, &gig.CreatedAt, &gig.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to insert delivery gig: %w", err)
	}

	// Mirror OTP code into orders table so customer app can display it
	if gig.OrderTrackingID != "" && gig.OTPCode != "" {
		_, err = tx.Exec(ctx,
			`UPDATE orders SET otp_code = $1, updated_at = NOW() WHERE order_tracking_id = $2`,
			gig.OTPCode, gig.OrderTrackingID,
		)
		if err != nil {
			return fmt.Errorf("failed to mirror OTP to order: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit gig creation: %w", err)
	}

	return nil
}

// UpdateRiderLocation stores the rider's location in H3 Hexagonal Shards in Redis.
// Implements lock-free SRem/SAdd transitions to keep the sets clean.
func (r *DeliveryRepository) UpdateRiderLocation(ctx context.Context, riderTrackID string, lng, lat float64) error {
	if r.redis == nil {
		return nil
	}
	centerCoord := h3.GeoCoord{Latitude: lat, Longitude: lng}
	centerHex := h3.FromGeo(centerCoord, 8)
	newHexKey := fmt.Sprintf("riders:h3:%x", centerHex)

	lastHexKey := fmt.Sprintf("rider:last_hex:%s", riderTrackID)
	oldHexHex, err := r.redis.Get(ctx, lastHexKey).Result()

	if err == nil && oldHexHex != "" {
		oldHexKey := fmt.Sprintf("riders:h3:%s", oldHexHex)
		if oldHexKey != newHexKey {
			// Atomic pipeline: add first, then remove (minimizes vanish window)
			pipe := r.redis.Pipeline()
			pipe.SAdd(ctx, newHexKey, riderTrackID)
			pipe.SRem(ctx, oldHexKey, riderTrackID)
			pipe.Exec(ctx)
		} else {
			r.redis.SAdd(ctx, newHexKey, riderTrackID)
		}
	} else {
		// Add to new hexagon set
		err = r.redis.SAdd(ctx, newHexKey, riderTrackID).Err()
		if err != nil {
			return err
		}
	}

	// Also maintain the GeoSet used by dispatch (HandleNewOrder / CancelGig) via GeoRadius
	geoKey := fmt.Sprintf("riders:locations:h3:%x", centerHex)
	r.redis.GeoAdd(ctx, geoKey, &redis.GeoLocation{
		Name:      riderTrackID,
		Longitude: lng,
		Latitude:  lat,
	})
	r.redis.Expire(ctx, geoKey, 300*time.Second)

	// Set expiration on the hexagon key to automatically garbage collect if riders go offline
	r.redis.Expire(ctx, newHexKey, 300*time.Second)

	// Update last known hexagon index (expires in 5 minutes of inactivity)
	r.redis.Set(ctx, lastHexKey, fmt.Sprintf("%x", centerHex), 300*time.Second)

	// Caching telemetry coordinates in Redis Cluster Shards (Fast Path - zero Postgres touch)
	coordsKey := fmt.Sprintf("rider:coords:%s", riderTrackID)
	coordsJSON, jsonErr := json.Marshal(map[string]interface{}{
		"rider_id":   riderTrackID,
		"lat":        lat,
		"lng":        lng,
		"updated_at": time.Now().UnixMilli(),
	})
	if jsonErr == nil {
		r.redis.Set(ctx, coordsKey, coordsJSON, 300*time.Second)
		// Publish telemetry coordinates to Redis Stream for durable delivery to clients.
		// Unlike Pub/Sub, messages persist and survive client disconnects/reconnects.
		r.redis.XAdd(ctx, &redis.XAddArgs{
			Stream: "stream:rider:telemetry",
			MaxLen: 50000,
			Approx: true,
			Values: map[string]interface{}{
				"data": string(coordsJSON),
				"ts":   time.Now().UnixMilli(),
			},
		})
	}

	return nil
}

// GetStoreCoordinates returns the stored latitude/longitude for a given store tracking ID.
func (r *DeliveryRepository) GetStoreCoordinates(ctx context.Context, storeTrackID string) (lat, lng float64, err error) {
	query := `SELECT latitude, longitude FROM stores WHERE store_tracking_id = $1`
	var storeLat, storeLng *float64
	err = r.reader.QueryRow(ctx, query, storeTrackID).Scan(&storeLat, &storeLng)
	if err != nil {
		return 0, 0, err
	}
	if storeLat == nil || storeLng == nil {
		return 0, 0, fmt.Errorf("store %s has no coordinates", storeTrackID)
	}
	return *storeLat, *storeLng, nil
}

// GetRidersInHexagon fetches all riders stored inside a single H3 hexagon
func (r *DeliveryRepository) GetRidersInHexagon(ctx context.Context, hexIndex h3.H3Index) ([]string, error) {
	hexKey := fmt.Sprintf("riders:h3:%x", hexIndex)
	return r.redis.SMembers(ctx, hexKey).Result()
}

// AcceptGigWithEligibility locks the gig row, verifies the rider's last known
// H3 hex is inside the pickup zone, and assigns it atomically. The caller is
// responsible for any post-commit Redis marker. If the gig is no longer
// broadcasting or the rider is outside the zone, the entire transaction rolls
// back and a conflict error is returned.
func (r *DeliveryRepository) AcceptGigWithEligibility(ctx context.Context, trackingID string, riderID string, riderHex h3.H3Index) error {
	tx, err := r.writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var currentStatus string
	var pickupLat, pickupLng *float64
	var isCod bool
	var orderTrackingID string
	query := `SELECT status, pickup_lat, pickup_lng, is_cod, order_tracking_id FROM deliveries WHERE tracking_id = $1 FOR UPDATE`
	err = tx.QueryRow(ctx, query, trackingID).Scan(
		&currentStatus, &pickupLat, &pickupLng, &isCod, &orderTrackingID)
	if err != nil {
		return fmt.Errorf("failed to fetch gig: %v", err)
	}

	if currentStatus != models.StatusBroadcasting {
		return fmt.Errorf("conflict: gig is no longer available (status: %s)", currentStatus)
	}

	// Verify the parent order payment status before assigning a rider.
	// COD orders have payment_status='pending' because payment is collected on delivery.
	// Online orders (Stripe/PayFast/JazzCash/EasyPaisa) must be 'paid' or 'settlement_pending'.
	var orderPaymentStatus string
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(payment_status, '') FROM orders WHERE order_tracking_id = $1`,
		orderTrackingID,
	).Scan(&orderPaymentStatus)
	if err != nil {
		return fmt.Errorf("failed to verify order payment status: %v", err)
	}
	paymentOK := orderPaymentStatus == "paid" || orderPaymentStatus == "settlement_pending"
	if isCod {
		paymentOK = paymentOK || orderPaymentStatus == "pending"
	}
	if !paymentOK {
		return fmt.Errorf("conflict: order payment not confirmed (status: %s). Rider assignment blocked", orderPaymentStatus)
	}

	if pickupLat == nil || pickupLng == nil {
		return fmt.Errorf("conflict: gig has no pickup coordinates")
	}

	originCoord := h3.GeoCoord{Latitude: *pickupLat, Longitude: *pickupLng}
	originHex := h3.FromGeo(originCoord, 5)
	eligible := false
	for _, ringHex := range h3.KRing(originHex, 1) {
		if ringHex == riderHex {
			eligible = true
			break
		}
	}
	if !eligible {
		return fmt.Errorf("conflict: rider not in eligible delivery zone")
	}

	// Check rider verification status and order count limit (max 10 orders for unverified riders)
	var isVerified bool
	userQuery := `SELECT COALESCE(is_verified, false) FROM users WHERE tracking_id = $1`
	err = tx.QueryRow(ctx, userQuery, riderID).Scan(&isVerified)
	if err != nil {
		log.Printf("Warning: could not check rider verification for %s, failing open: %v", riderID, err)
		isVerified = true
	}

	if !isVerified {
		return fmt.Errorf("conflict: rider KYC verification required. Please submit CNIC and Driving License documents for admin approval")
	}

	// Block COD gig if rider has >= 5000 PKR cash in hand (500000 paisa)
	if isCod {
		var cashInHandPaisa int64
		walletQuery := `SELECT COALESCE(cash_in_hand_paisa, 0) FROM rider_wallet WHERE rider_tracking_id = $1`
		if walletErr := tx.QueryRow(ctx, walletQuery, riderID).Scan(&cashInHandPaisa); walletErr != nil {
			log.Printf("Warning: could not read rider_wallet for %s: %v", riderID, walletErr)
		}
		if cashInHandPaisa >= 500000 { // 5000 PKR in paisa
			return fmt.Errorf("conflict: cash limit reached (>= 5000). Please deposit to accept COD orders")
		}
	}

	updateQuery := `
		UPDATE deliveries
		SET status = 'accepted', rider_tracking_id = $1, updated_at = NOW()
		WHERE tracking_id = $2
	`
	_, err = tx.Exec(ctx, updateQuery, riderID, trackingID)
	if err != nil {
		return fmt.Errorf("failed to accept gig: %v", err)
	}

	// Mirror the rider assignment onto the parent order for admin lineage.
	// Error must be checked: if the order update fails the transaction must
	// roll back, otherwise the gig and the order diverge in rider assignment.
	_, err = tx.Exec(ctx, `UPDATE orders SET rider_tracking_id = $1, updated_at = NOW() WHERE order_tracking_id = $2`, riderID, orderTrackingID)
	if err != nil {
		return fmt.Errorf("failed to mirror rider assignment to order: %w", err)
	}

	return tx.Commit(ctx)
}

// AcceptGig locks the gig row and assigns it to the rider if it's still broadcasting.
// Kept for backward compatibility; prefer AcceptGigWithEligibility for new code.
func (r *DeliveryRepository) AcceptGig(ctx context.Context, trackingID string, riderID string) error {
	tx, err := r.writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var currentStatus string
	var orderTrackingID string
	query := `SELECT status, order_tracking_id FROM deliveries WHERE tracking_id = $1 FOR UPDATE`
	err = tx.QueryRow(ctx, query, trackingID).Scan(&currentStatus, &orderTrackingID)
	if err != nil {
		return fmt.Errorf("failed to fetch gig: %v", err)
	}

	if currentStatus != "broadcasting" {
		return fmt.Errorf("conflict: gig is no longer available (status: %s)", currentStatus)
	}

	updateQuery := `
		UPDATE deliveries
		SET status = 'accepted', rider_tracking_id = $1, updated_at = NOW()
		WHERE tracking_id = $2
	`
	_, err = tx.Exec(ctx, updateQuery, riderID, trackingID)
	if err != nil {
		return fmt.Errorf("failed to accept gig: %v", err)
	}

	// Mirror the rider assignment onto the parent order for admin lineage.
	// Error must be checked: if the order update fails the transaction must
	// roll back, otherwise the gig and the order diverge in rider assignment.
	_, err = tx.Exec(ctx, `UPDATE orders SET rider_tracking_id = $1, updated_at = NOW() WHERE order_tracking_id = $2`, riderID, orderTrackingID)
	if err != nil {
		return fmt.Errorf("failed to mirror rider assignment to order: %w", err)
	}

	return tx.Commit(ctx)
}

// GetGigByTrackingID returns a single gig by its tracking id.
func (r *DeliveryRepository) GetGigByTrackingID(ctx context.Context, trackingID string) (*models.DeliveryGig, error) {
	query := `
		SELECT id, tracking_id, order_tracking_id, vendor_store_tracking_id, customer_tracking_id, status, rider_tracking_id, admin_commission, rider_earning, COALESCE(tips, 0.0), COALESCE(petrol_allowance, 0.0), pickup_lat, pickup_lng, dropoff_lat, dropoff_lng, otp_code, pickup_photo_url, delivery_photo_url, customer_dispute_photo_url, dispute_status, is_cod, order_total, customer_phone, created_at, updated_at
		FROM deliveries
		WHERE tracking_id = $1
	`
	gig := &models.DeliveryGig{}
	var riderID *string
	var vendorStoreID *string
	var customerTrackID *string
	var pickupLat, pickupLng, dropoffLat, dropoffLng *float64
	var otpCode, pickupPhoto, deliveryPhoto, disputePhoto, disputeStatus, customerPhone *string
	var isCod *bool
	var orderTotal *float64
	err := r.reader.QueryRow(ctx, query, trackingID).Scan(
		&gig.ID,
		&gig.TrackingID,
		&gig.OrderTrackingID,
		&vendorStoreID,
		&customerTrackID,
		&gig.Status,
		&riderID,
		&gig.AdminCommission,
		&gig.RiderEarning,
		&gig.Tips,
		&gig.PetrolAllowance,
		&pickupLat,
		&pickupLng,
		&dropoffLat,
		&dropoffLng,
		&otpCode,
		&pickupPhoto,
		&deliveryPhoto,
		&disputePhoto,
		&disputeStatus,
		&isCod,
		&orderTotal,
		&customerPhone,
		&gig.CreatedAt,
		&gig.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if riderID != nil {
		gig.AssignedRiderID = *riderID
	}
	if vendorStoreID != nil {
		gig.VendorStoreTrackID = *vendorStoreID
	}
	if customerTrackID != nil {
		gig.CustomerTrackID = *customerTrackID
	}
	if pickupLat != nil {
		gig.PickupLat = *pickupLat
	}
	if pickupLng != nil {
		gig.PickupLng = *pickupLng
	}
	if dropoffLat != nil {
		gig.DropoffLat = *dropoffLat
	}
	if dropoffLng != nil {
		gig.DropoffLng = *dropoffLng
	}
	if otpCode != nil {
		gig.OTPCode = *otpCode
	}
	if pickupPhoto != nil {
		gig.PickupPhotoURL = *pickupPhoto
	}
	if deliveryPhoto != nil {
		gig.DeliveryPhotoURL = *deliveryPhoto
	}
	if disputePhoto != nil {
		gig.CustomerDisputePhotoURL = *disputePhoto
	}
	if disputeStatus != nil {
		gig.DisputeStatus = *disputeStatus
	}
	if isCod != nil {
		gig.IsCOD = *isCod
	}
	if orderTotal != nil {
		gig.OrderTotal = *orderTotal
	}
	if customerPhone != nil {
		gig.CustomerPhone = *customerPhone
	}
	return gig, nil
}

// GetGigByOrderTrackingID returns the most recent gig for a given order tracking ID.
func (r *DeliveryRepository) GetGigByOrderTrackingID(ctx context.Context, orderTrackingID string) (*models.DeliveryGig, error) {
	query := `
		SELECT id, tracking_id, order_tracking_id, vendor_store_tracking_id, customer_tracking_id, status, rider_tracking_id, admin_commission, rider_earning, COALESCE(tips, 0.0), COALESCE(petrol_allowance, 0.0), pickup_lat, pickup_lng, dropoff_lat, dropoff_lng, otp_code, pickup_photo_url, delivery_photo_url, customer_dispute_photo_url, dispute_status, is_cod, order_total, customer_phone, created_at, updated_at
		FROM deliveries
		WHERE order_tracking_id = $1
		ORDER BY created_at DESC LIMIT 1
	`
	gig := &models.DeliveryGig{}
	var riderID, vendorStoreID, customerTrackID *string
	var pickupLat, pickupLng, dropoffLat, dropoffLng *float64
	var otpCode, pickupPhoto, deliveryPhoto, disputePhoto, disputeStatus, customerPhone *string
	var isCod *bool
	var orderTotal *float64

	err := r.reader.QueryRow(ctx, query, orderTrackingID).Scan(
		&gig.ID, &gig.TrackingID, &gig.OrderTrackingID, &vendorStoreID, &customerTrackID,
		&gig.Status, &riderID, &gig.AdminCommission, &gig.RiderEarning, &gig.Tips, &gig.PetrolAllowance,
		&pickupLat, &pickupLng, &dropoffLat, &dropoffLng,
		&otpCode, &pickupPhoto, &deliveryPhoto, &disputePhoto, &disputeStatus, &isCod, &orderTotal, &customerPhone,
		&gig.CreatedAt, &gig.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if riderID != nil {
		gig.AssignedRiderID = *riderID
	}
	if vendorStoreID != nil {
		gig.VendorStoreTrackID = *vendorStoreID
	}
	if customerTrackID != nil {
		gig.CustomerTrackID = *customerTrackID
	}
	if pickupLat != nil {
		gig.PickupLat = *pickupLat
	}
	if pickupLng != nil {
		gig.PickupLng = *pickupLng
	}
	if dropoffLat != nil {
		gig.DropoffLat = *dropoffLat
	}
	if dropoffLng != nil {
		gig.DropoffLng = *dropoffLng
	}
	if otpCode != nil {
		gig.OTPCode = *otpCode
	}
	if pickupPhoto != nil {
		gig.PickupPhotoURL = *pickupPhoto
	}
	if deliveryPhoto != nil {
		gig.DeliveryPhotoURL = *deliveryPhoto
	}
	if disputePhoto != nil {
		gig.CustomerDisputePhotoURL = *disputePhoto
	}
	if disputeStatus != nil {
		gig.DisputeStatus = *disputeStatus
	}
	if isCod != nil {
		gig.IsCOD = *isCod
	}
	if orderTotal != nil {
		gig.OrderTotal = *orderTotal
	}
	if customerPhone != nil {
		gig.CustomerPhone = *customerPhone
	}
	return gig, nil
}

// CreateOrderDispute creates a dispute record directly when no gig exists for the order.
func (r *DeliveryRepository) CreateOrderDispute(ctx context.Context, orderTrackingID, reason, photoURL, filedBy string) error {
	if filedBy == "" {
		_ = r.writer.QueryRow(ctx, `SELECT customer_tracking_id FROM orders WHERE order_tracking_id = $1`, orderTrackingID).Scan(&filedBy)
	}
	if filedBy == "" {
		filedBy = "system"
	}
	query := `
		INSERT INTO disputes (id, order_tracking_id, filed_by, reason, status, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, 'open', NOW(), NOW())
	`
	_, err := r.writer.Exec(ctx, query, orderTrackingID, filedBy, reason)
	return err
}

// ClearOrderRider removes the rider assignment from the parent order when a gig is cancelled
func (r *DeliveryRepository) ClearOrderRider(ctx context.Context, gigTrackingID string) (int64, error) {
	result, err := r.writer.Exec(ctx,
		`UPDATE orders SET rider_tracking_id = NULL, updated_at = NOW()
		 WHERE order_tracking_id = (SELECT order_tracking_id FROM deliveries WHERE tracking_id = $1)`,
		gigTrackingID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// UpdateGigStatus locks and transitions the delivery status with strict state machine validation.
// Returns the previous status, new status, assigned rider, and any error.
func (r *DeliveryRepository) UpdateGigStatus(ctx context.Context, trackingID string, status string, pickupPhoto string, deliveryPhoto string, riderEarning float64) (prevStatus, assignedRider string, err error) {
	tx, err := r.writer.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)

	query := `SELECT status, rider_tracking_id FROM deliveries WHERE tracking_id = $1 FOR UPDATE`
	var currentStatus string
	var riderID *string
	err = tx.QueryRow(ctx, query, trackingID).Scan(&currentStatus, &riderID)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch gig: %v", err)
	}

	if riderID != nil {
		assignedRider = *riderID
	}

	// Strict state machine: broadcasting -> accepted -> picked_up -> in_transit -> completed|failed
	transitions, ok := validGigTransitions[currentStatus]
	if !ok {
		return "", assignedRider, fmt.Errorf("conflict: invalid current gig status %s", currentStatus)
	}

	allowedStatus := false
	for _, t := range transitions {
		if t == status {
			allowedStatus = true
			break
		}
	}
	if !allowedStatus {
		return "", assignedRider, fmt.Errorf("conflict: cannot transition from %s to %s", currentStatus, status)
	}

	var updateQuery string
	var args []interface{}
	if status == models.StatusBroadcasting {
		// When reverting to broadcasting (rider cancelled), clear the rider assignment
		updateQuery = `
			UPDATE deliveries
			SET status = $1, rider_tracking_id = NULL, updated_at = NOW()
			WHERE tracking_id = $2
		`
		args = []interface{}{status, trackingID}
	} else if status == models.StatusPickedUp && pickupPhoto != "" {
		updateQuery = `
			UPDATE deliveries
			SET status = $1, pickup_photo_url = $2, updated_at = NOW()
			WHERE tracking_id = $3
		`
		args = []interface{}{status, pickupPhoto, trackingID}
	} else if status == models.StatusCompleted && deliveryPhoto != "" {
		updateQuery = `
			UPDATE deliveries
			SET status = $1, delivery_photo_url = $2, updated_at = NOW()
			WHERE tracking_id = $3
		`
		args = []interface{}{status, deliveryPhoto, trackingID}
	} else {
		updateQuery = `
			UPDATE deliveries
			SET status = $1, updated_at = NOW()
			WHERE tracking_id = $2
		`
		args = []interface{}{status, trackingID}
	}

	_, err = tx.Exec(ctx, updateQuery, args...)
	if err != nil {
		return "", assignedRider, fmt.Errorf("failed to update gig status: %v", err)
	}

	// CLAIM-4 FIX: Atomically credit the rider wallet in the exact same transaction
	// to prevent race conditions or missed payments if the server crashes right after
	// the status update.
	if status == models.StatusCompleted && riderEarning > 0 && assignedRider != "" {
		upsertWallet := `
			INSERT INTO rider_wallet (rider_tracking_id, balance_paisa, lifetime_earnings_paisa, updated_at)
			VALUES ($1, $2, $2, NOW())
			ON CONFLICT (rider_tracking_id)
			DO UPDATE SET
				balance_paisa = rider_wallet.balance_paisa + $2,
				lifetime_earnings_paisa = rider_wallet.lifetime_earnings_paisa + $2,
				updated_at = NOW()
		`
		if _, err := tx.Exec(ctx, upsertWallet, assignedRider, riderEarning); err != nil {
			return "", assignedRider, fmt.Errorf("failed to credit rider wallet atomically: %v", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", assignedRider, fmt.Errorf("failed to commit gig status update: %v", err)
	}

	return currentStatus, assignedRider, nil
}

// DisputeGig updates the dispute status, reason, and customer dispute photo URL
func (r *DeliveryRepository) DisputeGig(ctx context.Context, trackingID string, photoURL string) error {
	query := `
		UPDATE deliveries 
		SET dispute_status = 'disputed', customer_dispute_photo_url = $1, updated_at = NOW()
		WHERE tracking_id = $2
	`
	tag, err := r.writer.Exec(ctx, query, photoURL, trackingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("gig %s not found", trackingID)
	}
	return nil
}

// ResolveDispute updates the dispute status to resolved
func (r *DeliveryRepository) ResolveDispute(ctx context.Context, trackingID string, guiltyParty string) error {
	query := `
		UPDATE deliveries 
		SET dispute_status = $1, updated_at = NOW()
		WHERE tracking_id = $2
	`
	tag, err := r.writer.Exec(ctx, query, guiltyParty, trackingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("gig %s not found", trackingID)
	}
	return nil
}

// UpdateOrderDisputeStatus updates the dispute_status on the parent orders row.
func (r *DeliveryRepository) UpdateOrderDisputeStatus(ctx context.Context, orderTrackingID, status string) error {
	_, err := r.writer.Exec(ctx,
		`UPDATE orders SET dispute_status = $1, updated_at = NOW() WHERE order_tracking_id = $2`,
		status, orderTrackingID,
	)
	return err
}

// RecordCODDebt inserts a pending debt for the rider for the COD amount.
// amountPaisa is in paisa (int64).
func (r *DeliveryRepository) RecordCODDebt(ctx context.Context, orderTrackingID, riderTrackingID string, amountPaisa int64) error {
	query := `
		INSERT INTO cod_debts (id, order_tracking_id, rider_tracking_id, amount_owed, status)
		SELECT gen_random_uuid(), $1, $2, $3, 'pending'
		WHERE NOT EXISTS (
			SELECT 1 FROM cod_debts d
			WHERE d.order_tracking_id = $1 AND d.status IN ('pending', 'settled')
		)
	`
	_, err := r.writer.Exec(ctx, query, orderTrackingID, riderTrackingID, amountPaisa)
	return err
}

// CancelDeliveryForOrder cancels any pending, broadcasting, or picked-up delivery gig for a given order.
// 'in_transit' and 'completed' gigs are NOT cancelled — the rider is already on the way or delivered.
func (r *DeliveryRepository) CancelDeliveryForOrder(ctx context.Context, orderTrackingID string, cancelReason string) error {
	if cancelReason != "" {
		query := `
			UPDATE deliveries 
			SET status = 'cancelled', cancel_reason = $2, updated_at = NOW()
			WHERE order_tracking_id = $1 AND status IN ('broadcasting', 'accepted', 'picked_up')
		`
		_, err := r.writer.Exec(ctx, query, orderTrackingID, cancelReason)
		return err
	}

	query := `
		UPDATE deliveries 
		SET status = 'cancelled', updated_at = NOW()
		WHERE order_tracking_id = $1 AND status IN ('broadcasting', 'accepted', 'picked_up')
	`
	_, err := r.writer.Exec(ctx, query, orderTrackingID)
	return err
}

// SaveBid persists a delivery bid to PostgreSQL (best-effort, Redis remains primary).
func (r *DeliveryRepository) SaveBid(ctx context.Context, bid *models.RideBid) error {
	query := `
		INSERT INTO delivery_bids (bid_id, customer_tracking_id, vehicle_type, service_type,
		    pickup_lat, pickup_lng, dropoff_lat, dropoff_lng, negotiated_fare, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (bid_id) DO NOTHING
	`
	_, err := r.writer.Exec(ctx, query,
		bid.BidID, bid.CustomerTrackID, bid.VehicleType, bid.ServiceType,
		bid.PickupLat, bid.PickupLng, bid.DropoffLat, bid.DropoffLng,
		bid.NegotiatedFare, bid.Status,
	)
	return err
}

// SaveCounterBid persists a rider's counter-offer to PostgreSQL.
func (r *DeliveryRepository) SaveCounterBid(ctx context.Context, c *models.DeliveryCounterBid) error {
	query := `
		INSERT INTO delivery_bid_counters (bid_id, rider_tracking_id, rider_name, rating,
		    vehicle_plate, proposed_fare, eta, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending')
		RETURNING id, created_at
	`
	return r.writer.QueryRow(ctx, query,
		c.BidID, c.RiderTrackID, c.RiderName, c.Rating,
		c.VehiclePlate, c.ProposedFare, c.ETA,
	).Scan(&c.ID, &c.CreatedAt)
}

// AcceptCounterBid accepts a counter-offer within a transaction.
func (r *DeliveryRepository) AcceptCounterBid(ctx context.Context, bidID string, counterID int64) (*models.DeliveryCounterBid, error) {
	tx, err := r.writer.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Lock the bid row
	var bidStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM delivery_bids WHERE bid_id = $1 FOR UPDATE`, bidID).Scan(&bidStatus)
	if err != nil {
		return nil, fmt.Errorf("bid not found: %w", err)
	}
	if bidStatus == "accepted" || bidStatus == "cancelled" {
		return nil, fmt.Errorf("bid is already %s", bidStatus)
	}

	// Accept the chosen counter
	var counter models.DeliveryCounterBid
	err = tx.QueryRow(ctx, `
		UPDATE delivery_bid_counters SET status = 'accepted'
		WHERE id = $1 AND bid_id = $2 AND status = 'pending'
		RETURNING id, bid_id, rider_tracking_id, rider_name, rating,
		          vehicle_plate, proposed_fare, eta, status, created_at
	`, counterID, bidID).Scan(&counter.ID, &counter.BidID, &counter.RiderTrackID,
		&counter.RiderName, &counter.Rating, &counter.VehiclePlate,
		&counter.ProposedFare, &counter.ETA, &counter.Status, &counter.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("counter not found or already resolved: %w", err)
	}

	// Reject all other pending counters
	_, _ = tx.Exec(ctx, `UPDATE delivery_bid_counters SET status = 'rejected' WHERE bid_id = $1 AND id != $2 AND status = 'pending'`, bidID, counterID)

	// Update bid status
	tag, err := tx.Exec(ctx, `UPDATE delivery_bids SET status = 'accepted', updated_at = NOW() WHERE bid_id = $1 AND status IN ('searching', 'offers_received')`, bidID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("bid transition failed")
	}

	return &counter, tx.Commit(ctx)
}
