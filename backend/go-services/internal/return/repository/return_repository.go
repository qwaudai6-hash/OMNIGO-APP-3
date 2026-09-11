package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
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

// GetCustomerReturnCount returns the total number of return requests by a customer.
func (r *ReturnRepository) GetCustomerReturnCount(ctx context.Context, customerTrackingID string) (int, error) {
	var count int
	err := r.reader.QueryRow(ctx,
		`SELECT COUNT(*) FROM return_requests WHERE customer_tracking_id = $1`,
		customerTrackingID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to get customer return count: %w", err)
	}
	return count, nil
}

// GetCustomerOrderCount returns the total number of orders by a customer.
func (r *ReturnRepository) GetCustomerOrderCount(ctx context.Context, customerTrackingID string) (int, error) {
	var count int
	err := r.reader.QueryRow(ctx,
		`SELECT COUNT(*) FROM orders WHERE customer_tracking_id = $1 AND status IN ('delivered', 'completed')`,
		customerTrackingID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to get customer order count: %w", err)
	}
	return count, nil
}

// GetCustomerName returns the customer's name from the users table.
func (r *ReturnRepository) GetCustomerName(ctx context.Context, customerTrackingID string) (string, error) {
	var name string
	err := r.reader.QueryRow(ctx,
		`SELECT COALESCE(first_name || ' ' || last_name, name, 'Customer') FROM users WHERE tracking_id = $1`,
		customerTrackingID,
	).Scan(&name)
	if err != nil {
		return "Customer", nil
	}
	return name, nil
}

// GetVendorName returns the vendor's name from the users table.
func (r *ReturnRepository) GetVendorName(ctx context.Context, vendorTrackingID string) (string, error) {
	var name string
	err := r.reader.QueryRow(ctx,
		`SELECT COALESCE(first_name || ' ' || last_name, name, 'Vendor') FROM users WHERE tracking_id = $1`,
		vendorTrackingID,
	).Scan(&name)
	if err != nil {
		return "Vendor", nil
	}
	return name, nil
}

// ListReturns returns paginated return requests with optional filters.
func (r *ReturnRepository) ListReturns(ctx context.Context, status string, limit, offset int) ([]map[string]interface{}, int, error) {
	countQuery := `SELECT COUNT(*) FROM return_requests WHERE 1=1`
	dataQuery := `
		SELECT id, order_tracking_id, customer_tracking_id, vendor_tracking_id,
		       store_tracking_id, reason, status, requested_at, pickup_deadline,
		       verified_at, completed_at, return_delivery_fee_paisa, payment_status,
		       created_at, updated_at
		FROM return_requests WHERE 1=1`

	args := []interface{}{}
	argIdx := 1

	if status != "" {
		countQuery += fmt.Sprintf(" AND status = $%d", argIdx)
		dataQuery += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, status)
		argIdx++
	}

	var total int
	err := r.reader.QueryRow(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to count returns: %w", err)
	}

	dataQuery += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := r.reader.Query(ctx, dataQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list returns: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var id, orderID, customerID, vendorID, storeID, reason, retStatus, payStatus string
		var requestedAt, createdAt, updatedAt time.Time
		var pickupDeadline, verifiedAt, completedAt *time.Time
		var returnFee int64

		err := rows.Scan(&id, &orderID, &customerID, &vendorID, &storeID,
			&reason, &retStatus, &requestedAt, &pickupDeadline,
			&verifiedAt, &completedAt, &returnFee, &payStatus,
			&createdAt, &updatedAt)
		if err != nil {
			continue
		}

		results = append(results, map[string]interface{}{
			"id":                       id,
			"order_tracking_id":        orderID,
			"customer_tracking_id":     customerID,
			"vendor_tracking_id":       vendorID,
			"store_tracking_id":        storeID,
			"reason":                   reason,
			"status":                   retStatus,
			"requested_at":             requestedAt,
			"pickup_deadline":          pickupDeadline,
			"verified_at":              verifiedAt,
			"completed_at":             completedAt,
			"return_delivery_fee_paisa": returnFee,
			"payment_status":           payStatus,
			"created_at":               createdAt,
			"updated_at":               updatedAt,
		})
	}

	return results, total, nil
}

// GetReturnStats returns aggregated return statistics.
func (r *ReturnRepository) GetReturnStats(ctx context.Context) (map[string]interface{}, error) {
	stats := map[string]interface{}{}

	// Total returns
	var total int
	err := r.reader.QueryRow(ctx, `SELECT COUNT(*) FROM return_requests`).Scan(&total)
	if err != nil {
		return nil, err
	}
	stats["total_returns"] = total

	// Returns by status
	rows, err := r.reader.Query(ctx,
		`SELECT status, COUNT(*) FROM return_requests GROUP BY status ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byStatus := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if rows.Scan(&status, &count) == nil {
			byStatus[status] = count
		}
	}
	stats["by_status"] = byStatus

	// Total refund amount
	var totalRefund int64
	err = r.reader.QueryRow(ctx,
		`SELECT COALESCE(SUM(return_delivery_fee_paisa), 0) FROM return_requests WHERE payment_status = 'completed'`).Scan(&totalRefund)
	if err == nil {
		stats["total_refund_paisa"] = totalRefund
	}

	// Returns in last 7 days
	var last7Days int
	err = r.reader.QueryRow(ctx,
		`SELECT COUNT(*) FROM return_requests WHERE created_at > NOW() - INTERVAL '7 days'`).Scan(&last7Days)
	if err == nil {
		stats["returns_last_7_days"] = last7Days
	}

	// Returns in last 30 days
	var last30Days int
	err = r.reader.QueryRow(ctx,
		`SELECT COUNT(*) FROM return_requests WHERE created_at > NOW() - INTERVAL '30 days'`).Scan(&last30Days)
	if err == nil {
		stats["returns_last_30_days"] = last30Days
	}

	return stats, nil
}

// CreateReturnDispute creates a dispute record in the disputes table for a return.
func (r *ReturnRepository) CreateReturnDispute(ctx context.Context, disputeID uuid.UUID, orderTrackingID, vendorTrackingID, reason string) error {
	_, err := r.writer.Exec(ctx,
		`INSERT INTO disputes (id, order_tracking_id, filed_by, reason, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, 'open', NOW(), NOW())
		 ON CONFLICT (id) DO NOTHING`,
		disputeID, orderTrackingID, vendorTrackingID, reason,
	)
	if err != nil {
		return fmt.Errorf("failed to create dispute: %w", err)
	}
	return nil
}

// UpdateReturnDisputeDetails updates the return request with dispute information.
func (r *ReturnRepository) UpdateReturnDisputeDetails(ctx context.Context, returnID, disputeReason, disputeID string) error {
	_, err := r.writer.Exec(ctx,
		`UPDATE return_requests 
		 SET dispute_reason = $1, escrow_hold_id = $2, updated_at = NOW()
		 WHERE id = $3`,
		disputeReason, disputeID, returnID,
	)
	if err != nil {
		return fmt.Errorf("failed to update return dispute details: %w", err)
	}
	return nil
}

// DeactivateStore temporarily deactivates a vendor's store and products.
func (r *ReturnRepository) DeactivateStore(ctx context.Context, vendorTrackingID string, duration time.Duration) error {
	// Deactivate store
	_, err := r.writer.Exec(ctx,
		`UPDATE stores SET is_active = false WHERE vendor_tracking_id = $1`,
		vendorTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to deactivate store: %w", err)
	}

	// Deactivate all products
	_, err = r.writer.Exec(ctx,
		`UPDATE products SET is_active = false WHERE vendor_tracking_id = $1`,
		vendorTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to deactivate products: %w", err)
	}

	return nil
}

// ReactivateStore reactivates a vendor's store and products.
func (r *ReturnRepository) ReactivateStore(ctx context.Context, vendorTrackingID string) error {
	// Reactivate store
	_, err := r.writer.Exec(ctx,
		`UPDATE stores SET is_active = true WHERE vendor_tracking_id = $1`,
		vendorTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to reactivate store: %w", err)
	}

	// Reactivate all products
	_, err = r.writer.Exec(ctx,
		`UPDATE products SET is_active = true WHERE vendor_tracking_id = $1`,
		vendorTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to reactivate products: %w", err)
	}

	return nil
}
