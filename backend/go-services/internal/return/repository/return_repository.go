package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/return/models"
	"github.com/omnigo/backend/internal/shared/tracking"
)

type ReturnRepository struct {
	writer *pgxpool.Pool
	reader *pgxpool.Pool
}

func NewReturnRepository(writer, reader *pgxpool.Pool) *ReturnRepository {
	return &ReturnRepository{
		writer: writer,
		reader: reader,
	}
}

// CreateReturnRequest inserts a new return request into the database.
func (r *ReturnRepository) CreateReturnRequest(ctx context.Context, req *models.ReturnRequest) error {
	if req.ID == "" {
		req.ID = tracking.Generate("RET")
	}

	query := `
		INSERT INTO return_requests (
			id, order_tracking_id, customer_tracking_id, vendor_tracking_id,
			store_tracking_id, reason, return_items, status, requested_at,
			pickup_deadline, return_delivery_fee_paisa, payment_status,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW(), NOW())
	`

	_, err := r.writer.Exec(ctx, query,
		req.ID,
		req.OrderTrackingID,
		req.CustomerTrackingID,
		req.VendorTrackingID,
		req.StoreTrackingID,
		req.Reason,
		req.ReturnItems,
		req.Status,
		req.RequestedAt,
		req.PickupDeadline,
		req.ReturnDeliveryFeePaisa,
		req.PaymentStatus,
	)
	if err != nil {
		return fmt.Errorf("failed to create return request: %w", err)
	}

	return nil
}

// GetReturnRequestByID retrieves a return request by its tracking ID.
func (r *ReturnRepository) GetReturnRequestByID(ctx context.Context, id string) (*models.ReturnRequest, error) {
	query := `
		SELECT id, order_tracking_id, customer_tracking_id, vendor_tracking_id,
			store_tracking_id, rider_tracking_id, gig_tracking_id,
			reason, return_items, status, requested_at, pickup_deadline,
			verified_at, completed_at, pickup_photo_url, delivery_photo_url,
			vendor_verification_photo, return_delivery_fee_paisa, payment_method,
			payment_status, escrow_hold_id, dispute_reason, dispute_resolved_at,
			created_at, updated_at
		FROM return_requests
		WHERE id = $1
	`

	var req models.ReturnRequest
	err := r.reader.QueryRow(ctx, query, id).Scan(
		&req.ID, &req.OrderTrackingID, &req.CustomerTrackingID, &req.VendorTrackingID,
		&req.StoreTrackingID, &req.RiderTrackingID, &req.GigTrackingID,
		&req.Reason, &req.ReturnItems, &req.Status, &req.RequestedAt, &req.PickupDeadline,
		&req.VerifiedAt, &req.CompletedAt, &req.PickupPhotoURL, &req.DeliveryPhotoURL,
		&req.VendorVerificationPhoto, &req.ReturnDeliveryFeePaisa, &req.PaymentMethod,
		&req.PaymentStatus, &req.EscrowHoldID, &req.DisputeReason, &req.DisputeResolvedAt,
		&req.CreatedAt, &req.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("return request not found: %w", err)
	}

	return &req, nil
}

// GetReturnRequestByOrderID retrieves the active return request for an order.
func (r *ReturnRepository) GetReturnRequestByOrderID(ctx context.Context, orderTrackingID string) (*models.ReturnRequest, error) {
	query := `
		SELECT id, order_tracking_id, customer_tracking_id, vendor_tracking_id,
			store_tracking_id, rider_tracking_id, gig_tracking_id,
			reason, return_items, status, requested_at, pickup_deadline,
			verified_at, completed_at, pickup_photo_url, delivery_photo_url,
			vendor_verification_photo, return_delivery_fee_paisa, payment_method,
			payment_status, escrow_hold_id, dispute_reason, dispute_resolved_at,
			created_at, updated_at
		FROM return_requests
		WHERE order_tracking_id = $1 AND status NOT IN ('return_completed', 'return_cancelled')
		ORDER BY created_at DESC
		LIMIT 1
	`

	var req models.ReturnRequest
	err := r.reader.QueryRow(ctx, query, orderTrackingID).Scan(
		&req.ID, &req.OrderTrackingID, &req.CustomerTrackingID, &req.VendorTrackingID,
		&req.StoreTrackingID, &req.RiderTrackingID, &req.GigTrackingID,
		&req.Reason, &req.ReturnItems, &req.Status, &req.RequestedAt, &req.PickupDeadline,
		&req.VerifiedAt, &req.CompletedAt, &req.PickupPhotoURL, &req.DeliveryPhotoURL,
		&req.VendorVerificationPhoto, &req.ReturnDeliveryFeePaisa, &req.PaymentMethod,
		&req.PaymentStatus, &req.EscrowHoldID, &req.DisputeReason, &req.DisputeResolvedAt,
		&req.CreatedAt, &req.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("no active return request for order: %w", err)
	}

	return &req, nil
}

// UpdateReturnStatus updates the status of a return request.
func (r *ReturnRepository) UpdateReturnStatus(ctx context.Context, id, status string) error {
	query := `UPDATE return_requests SET status = $1, updated_at = NOW() WHERE id = $2`
	_, err := r.writer.Exec(ctx, query, status, id)
	if err != nil {
		return fmt.Errorf("failed to update return status: %w", err)
	}
	return nil
}

// AssignRider assigns a rider and gig to a return request.
func (r *ReturnRepository) AssignRider(ctx context.Context, id, riderTrackingID, gigTrackingID string) error {
	query := `
		UPDATE return_requests 
		SET rider_tracking_id = $1, gig_tracking_id = $2, status = 'rider_assigned', updated_at = NOW()
		WHERE id = $3
	`
	_, err := r.writer.Exec(ctx, query, riderTrackingID, gigTrackingID, id)
	if err != nil {
		return fmt.Errorf("failed to assign rider: %w", err)
	}
	return nil
}

// RecordPickupPhoto records the rider's pickup photo and OTP verification.
func (r *ReturnRepository) RecordPickupPhoto(ctx context.Context, id, photoURL string) error {
	query := `
		UPDATE return_requests 
		SET pickup_photo_url = $1, status = 'return_pickup_completed', updated_at = NOW()
		WHERE id = $2
	`
	_, err := r.writer.Exec(ctx, query, photoURL, id)
	if err != nil {
		return fmt.Errorf("failed to record pickup photo: %w", err)
	}
	return nil
}

// RecordDeliveryPhoto records the rider's delivery photo at vendor store.
func (r *ReturnRepository) RecordDeliveryPhoto(ctx context.Context, id, photoURL string) error {
	query := `
		UPDATE return_requests 
		SET delivery_photo_url = $1, status = 'return_delivered', updated_at = NOW()
		WHERE id = $2
	`
	_, err := r.writer.Exec(ctx, query, photoURL, id)
	if err != nil {
		return fmt.Errorf("failed to record delivery photo: %w", err)
	}
	return nil
}

// VerifyByVendor records the vendor's verification result.
func (r *ReturnRepository) VerifyByVendor(ctx context.Context, id string, verified bool, photoURL, notes string) error {
	var status string
	var disputeReason *string

	if verified {
		status = models.ReturnStatusVerified
	} else {
		status = models.ReturnStatusDisputed
		if notes != "" {
			disputeReason = &notes
		}
	}

	query := `
		UPDATE return_requests 
		SET status = $1, vendor_verification_photo = $2, dispute_reason = $3,
			verified_at = NOW(), updated_at = NOW()
		WHERE id = $4
	`
	_, err := r.writer.Exec(ctx, query, status, photoURL, disputeReason, id)
	if err != nil {
		return fmt.Errorf("failed to verify return: %w", err)
	}
	return nil
}

// CompleteReturn marks a return as completed (refund processed).
func (r *ReturnRepository) CompleteReturn(ctx context.Context, id string) error {
	query := `
		UPDATE return_requests 
		SET status = 'return_completed', completed_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`
	_, err := r.writer.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to complete return: %w", err)
	}
	return nil
}

// CancelReturn cancels a return request.
func (r *ReturnRepository) CancelReturn(ctx context.Context, id string) error {
	query := `
		UPDATE return_requests 
		SET status = 'return_cancelled', updated_at = NOW()
		WHERE id = $1 AND status NOT IN ('return_completed', 'return_cancelled')
	`
	_, err := r.writer.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to cancel return: %w", err)
	}
	return nil
}

// HasActiveReturn checks if an order already has an active return request.
func (r *ReturnRepository) HasActiveReturn(ctx context.Context, orderTrackingID string) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1 FROM return_requests 
			WHERE order_tracking_id = $1 
			AND status NOT IN ('return_completed', 'return_cancelled')
		)
	`
	var exists bool
	err := r.reader.QueryRow(ctx, query, orderTrackingID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// GetReturnsByStatus returns all return requests with a given status.
func (r *ReturnRepository) GetReturnsByStatus(ctx context.Context, status string, limit int) ([]*models.ReturnRequest, error) {
	query := `
		SELECT id, order_tracking_id, customer_tracking_id, vendor_tracking_id,
			store_tracking_id, rider_tracking_id, gig_tracking_id,
			reason, return_items, status, requested_at, pickup_deadline,
			verified_at, completed_at, pickup_photo_url, delivery_photo_url,
			vendor_verification_photo, return_delivery_fee_paisa, payment_method,
			payment_status, escrow_hold_id, dispute_reason, dispute_resolved_at,
			created_at, updated_at
		FROM return_requests
		WHERE status = $1
		ORDER BY created_at DESC
		LIMIT $2
	`

	rows, err := r.reader.Query(ctx, query, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*models.ReturnRequest
	for rows.Next() {
		var req models.ReturnRequest
		if err := rows.Scan(
			&req.ID, &req.OrderTrackingID, &req.CustomerTrackingID, &req.VendorTrackingID,
			&req.StoreTrackingID, &req.RiderTrackingID, &req.GigTrackingID,
			&req.Reason, &req.ReturnItems, &req.Status, &req.RequestedAt, &req.PickupDeadline,
			&req.VerifiedAt, &req.CompletedAt, &req.PickupPhotoURL, &req.DeliveryPhotoURL,
			&req.VendorVerificationPhoto, &req.ReturnDeliveryFeePaisa, &req.PaymentMethod,
			&req.PaymentStatus, &req.EscrowHoldID, &req.DisputeReason, &req.DisputeResolvedAt,
			&req.CreatedAt, &req.UpdatedAt,
		); err != nil {
			return nil, err
		}
		results = append(results, &req)
	}

	return results, nil
}

// SetReturnDeadline sets the 36-hour return deadline on an order.
func (r *ReturnRepository) SetReturnDeadline(ctx context.Context, orderTrackingID string, deadline time.Time) error {
	query := `UPDATE orders SET return_deadline = $1 WHERE order_tracking_id = $2`
	_, err := r.writer.Exec(ctx, query, deadline, orderTrackingID)
	if err != nil {
		return fmt.Errorf("failed to set return deadline: %w", err)
	}
	return nil
}

// IsReturnWindowOpen checks if the return window is still open for an order.
func (r *ReturnRepository) IsReturnWindowOpen(ctx context.Context, orderTrackingID string) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1 FROM orders 
			WHERE order_tracking_id = $1 
			AND return_deadline IS NOT NULL
			AND return_deadline > NOW()
		)
	`
	var open bool
	err := r.reader.QueryRow(ctx, query, orderTrackingID).Scan(&open)
	if err != nil {
		return false, err
	}
	return open, nil
}

// GetOrderForReturn fetches order details needed for return processing.
func (r *ReturnRepository) GetOrderForReturn(ctx context.Context, orderTrackingID string) (map[string]interface{}, error) {
	query := `
		SELECT order_tracking_id, customer_tracking_id, vendor_tracking_id,
			store_tracking_id, customer_lat, customer_lng, total_amount,
			payment_gateway, payment_status, status
		FROM orders
		WHERE order_tracking_id = $1
	`
	row := r.reader.QueryRow(ctx, query, orderTrackingID)

	var orderTracking, customerID, vendorID, storeID string
	var customerLat, customerLng float64
	var totalAmount int64
	var paymentGateway, paymentStatus, status string

	err := row.Scan(
		&orderTracking, &customerID, &vendorID, &storeID,
		&customerLat, &customerLng, &totalAmount,
		&paymentGateway, &paymentStatus, &status,
	)
	if err != nil {
		return nil, err
	}

	result := map[string]interface{}{
		"order_tracking_id":  orderTracking,
		"customer_tracking_id": customerID,
		"vendor_tracking_id": vendorID,
		"store_tracking_id":  storeID,
		"customer_lat":       customerLat,
		"customer_lng":       customerLng,
		"total_amount":       totalAmount,
		"payment_gateway":    paymentGateway,
		"payment_status":     paymentStatus,
		"status":             status,
	}

	return result, nil
}
