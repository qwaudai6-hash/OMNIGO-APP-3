package repository

import (
	"context"
	"encoding/json"
	"fmt"
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

// CreateGig inserts a new delivery gig into PostgreSQL
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

	query := `
		INSERT INTO deliveries (tracking_id, order_tracking_id, vendor_store_tracking_id, customer_tracking_id, status, delivery_fee, admin_commission, rider_earning, tips, petrol_allowance, pickup_lat, pickup_lng, dropoff_lat, dropoff_lng, otp_code, is_cod, order_total, customer_phone)
		VALUES ($1, $2, $3, $4, 'broadcasting', $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING id, created_at, updated_at
	`
	err := r.writer.QueryRow(ctx, query,
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
		return err
	}

	// Mirror OTP code into orders table so customer app can display it to customer
	if gig.OrderTrackingID != "" && gig.OTPCode != "" {
		_, _ = r.writer.Exec(ctx,
			`UPDATE orders SET otp_code = $1, updated_at = NOW() WHERE order_tracking_id = $2`,
			gig.OTPCode, gig.OrderTrackingID,
		)
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
			// Remove from old hexagon set
			r.redis.SRem(ctx, oldHexKey, riderTrackID)
		}
	}

	// Add to new hexagon set
	err = r.redis.SAdd(ctx, newHexKey, riderTrackID).Err()
	if err != nil {
		return err
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
		// Publish telemetry coordinates to Redis Pub/Sub channel for sub-millisecond streaming to client
		r.redis.Publish(ctx, "rider:telemetry:pubsub", coordsJSON)
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
		isVerified = false
	}

	if !isVerified {
		return fmt.Errorf("conflict: rider KYC verification required. Please submit CNIC and Driving License documents for admin approval")
	}

	// Block COD gig if rider has >= 5000 cash in hand
	if isCod {
		var cashInHand float64
		walletQuery := `SELECT COALESCE(cash_in_hand, 0) FROM rider_wallet WHERE rider_tracking_id = $1`
		_ = tx.QueryRow(ctx, walletQuery, riderID).Scan(&cashInHand)
		if cashInHand >= 5000.0 {
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
	_, _ = tx.Exec(ctx, `UPDATE orders SET rider_tracking_id = $1 WHERE order_tracking_id = $2`, riderID, orderTrackingID)

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
	_, _ = tx.Exec(ctx, `UPDATE orders SET rider_tracking_id = $1 WHERE order_tracking_id = $2`, riderID, orderTrackingID)

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
func (r *DeliveryRepository) CreateOrderDispute(ctx context.Context, orderTrackingID, reason, photoURL string) error {
	query := `
		INSERT INTO disputes (order_tracking_id, reason, status, created_at, updated_at)
		VALUES ($1, $2, 'open', NOW(), NOW())
		ON CONFLICT (order_tracking_id) DO UPDATE SET reason = $2, updated_at = NOW()
	`
	_, err := r.writer.Exec(ctx, query, orderTrackingID, reason)
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
func (r *DeliveryRepository) UpdateGigStatus(ctx context.Context, trackingID string, status string, pickupPhoto string, deliveryPhoto string) (prevStatus, assignedRider string, err error) {
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
	_, err := r.writer.Exec(ctx, query, photoURL, trackingID)
	return err
}

// ResolveDispute updates the dispute status to resolved
func (r *DeliveryRepository) ResolveDispute(ctx context.Context, trackingID string, guiltyParty string) error {
	query := `
		UPDATE deliveries 
		SET dispute_status = $1, updated_at = NOW()
		WHERE tracking_id = $2
	`
	_, err := r.writer.Exec(ctx, query, guiltyParty, trackingID)
	return err
}

// RecordCODDebt inserts a pending debt for the rider for the COD amount.
func (r *DeliveryRepository) RecordCODDebt(ctx context.Context, orderTrackingID, riderTrackingID string, amount float64) error {
	query := `
		INSERT INTO cod_debts (id, order_tracking_id, rider_tracking_id, amount_owed, status)
		SELECT gen_random_uuid(), $1, $2, $3, 'pending'
		WHERE NOT EXISTS (
			SELECT 1 FROM cod_debts d
			WHERE d.order_tracking_id = $1 AND d.status IN ('pending', 'settled')
		)
	`
	_, err := r.writer.Exec(ctx, query, orderTrackingID, riderTrackingID, amount)
	return err
}

// CancelDeliveryForOrder cancels any pending or broadcasting delivery gig for a given order
func (r *DeliveryRepository) CancelDeliveryForOrder(ctx context.Context, orderTrackingID string, cancelReason string) error {
	if cancelReason != "" {
		query := `
			UPDATE deliveries 
			SET status = 'cancelled', cancel_reason = $2, updated_at = NOW()
			WHERE order_tracking_id = $1 AND status IN ('broadcasting', 'accepted')
		`
		_, err := r.writer.Exec(ctx, query, orderTrackingID, cancelReason)
		return err
	}
	query := `
		UPDATE deliveries 
		SET status = 'cancelled', updated_at = NOW()
		WHERE order_tracking_id = $1 AND status IN ('broadcasting', 'accepted')
	`
	_, err := r.writer.Exec(ctx, query, orderTrackingID)
	return err
}

// SettleCODVendorAndDebt closes the COD loop at delivery completion:
//
//	CLAIM-2 FIX: credits the VENDOR wallet (escrow_holds row written as
//	'paid_out' for audit) — previously COD vendor money lived only in the
//	TigerBeetle ledger and never reached vendor_wallet / payouts.
//	CLAIM-3 FIX: auto-inserts the rider's cod_debts row so the Pay Now
//	flow has something to settle (previously only the manual confirm
//	endpoint created debts, which no client ever called).
//
// Idempotent per order: wallet uses upsert-add; escrow/debt inserts are
// guarded by NOT EXISTS.
func (r *DeliveryRepository) SettleCODVendorAndDebt(ctx context.Context, orderTrackingID, storeID, riderTrackingID string, orderTotal, adminCommission, riderEarning float64) error {
	tx, err := r.writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var vendorID string
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(vendor_tracking_id, '') FROM orders WHERE order_tracking_id = $1`,
		orderTrackingID).Scan(&vendorID)
	if err != nil {
		return fmt.Errorf("order lookup failed: %w", err)
	}
	if vendorID == "" {
		vendorID = storeID // legacy fallback
	}

	vendorAmount := orderTotal - adminCommission - riderEarning
	if vendorAmount < 0 {
		vendorAmount = 0
	}

	if vendorAmount > 0 {
		// CLAIM-2: credit the vendor's withdrawable wallet.
		if _, err := tx.Exec(ctx, `
			INSERT INTO vendor_wallet (vendor_tracking_id, balance, lifetime_earnings, updated_at)
			VALUES ($1, $2, $2, NOW())
			ON CONFLICT (vendor_tracking_id)
			DO UPDATE SET balance = vendor_wallet.balance + $2,
			              lifetime_earnings = vendor_wallet.lifetime_earnings + $2,
			              updated_at = NOW()
		`, vendorID, vendorAmount); err != nil {
			return fmt.Errorf("vendor wallet credit failed: %w", err)
		}

		// Audit trail: an already-settled escrow hold (excluded from the
		// PayoutWorker sweep, which only scans status='released').
		if _, err := tx.Exec(ctx, `
			INSERT INTO escrow_holds (id, order_tracking_id, vendor_tracking_id, amount, status, released_at)
			SELECT gen_random_uuid(), $1, $2, $3, 'paid_out', NOW()
			WHERE NOT EXISTS (
				SELECT 1 FROM escrow_holds e WHERE e.order_tracking_id = $1 AND e.status = 'paid_out'
			)
		`, orderTrackingID, vendorID, vendorAmount); err != nil {
			return fmt.Errorf("cod escrow audit row failed: %w", err)
		}
	}

	// CLAIM-3: rider now holds this cash — record the debt once.
	if _, err := tx.Exec(ctx, `
		INSERT INTO cod_debts (id, order_tracking_id, rider_tracking_id, amount, amount_owed, status)
		SELECT gen_random_uuid(), $1, $2, $3, $3, 'pending'
		WHERE NOT EXISTS (
			SELECT 1 FROM cod_debts d
			WHERE d.order_tracking_id = $1 AND d.status IN ('pending', 'settled')
		)
	`, orderTrackingID, riderTrackingID, orderTotal); err != nil {
		return fmt.Errorf("cod debt insert failed: %w", err)
	}

	return tx.Commit(ctx)
}
