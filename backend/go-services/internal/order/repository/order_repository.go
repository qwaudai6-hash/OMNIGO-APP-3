package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/order/models"
	"github.com/omnigo/backend/internal/shared/database"
)

type OrderRepository struct {
	writer *pgxpool.Pool
	reader *pgxpool.Pool
}

func NewOrderRepository(writer, reader *pgxpool.Pool) *OrderRepository {
	return &OrderRepository{
		writer: writer,
		reader: reader,
	}
}

// CreateOrder inserts a new order, its items, and an outbox event into the database within a transaction
func (r *OrderRepository) CreateOrder(ctx context.Context, order *models.Order, outboxPayload []byte) error {
	tx, err := r.writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	query := `
		INSERT INTO orders (order_tracking_id, customer_tracking_id, store_tracking_id, vendor_tracking_id, status, total_amount, currency, payment_gateway, customer_lat, customer_lng, device_session_nonce, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'pending', $5, $6, $7, $8, $9, $10, NOW(), NOW())
		RETURNING id, created_at, updated_at
	`
	err = tx.QueryRow(ctx, query,
		order.TrackingID,
		order.UserTrackID,
		order.VendorStoreTrackID,
		order.VendorTrackID,
		order.TotalAmount,
		order.Currency,
		order.PaymentGateway,
		order.CustomerLat,
		order.CustomerLng,
		order.DeviceSessionNonce,
	).Scan(&order.ID, &order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		return err
	}

	// Validate parent references before inserting order items
	if order.VendorStoreTrackID != "" {
		ok, err := database.Exists(ctx, tx, "SELECT 1 FROM stores WHERE store_tracking_id = $1", order.VendorStoreTrackID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("store %s does not exist", order.VendorStoreTrackID)
		}
	}
	if order.VendorTrackID != "" {
		ok, err := database.Exists(ctx, tx, "SELECT 1 FROM users WHERE tracking_id = $1", order.VendorTrackID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("user %s does not exist", order.VendorTrackID)
		}
	}

	// Insert order items
	if len(order.Items) > 0 {
		var batch pgx.Batch
		for _, item := range order.Items {
			ok, err := database.Exists(ctx, tx, "SELECT 1 FROM products WHERE product_tracking_id = $1", item.ProductTrackingID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("product %s does not exist", item.ProductTrackingID)
			}

			batch.Queue(`
				INSERT INTO order_items (order_tracking_id, product_tracking_id, quantity, price_at_checkout, created_at)
				VALUES ($1, $2, $3, $4, NOW())
			`, order.TrackingID, item.ProductTrackingID, item.Quantity, item.PriceAtCheckout)
		}

		br := tx.SendBatch(ctx, &batch)
		_, err = br.Exec()
		br.Close()
		if err != nil {
			return err
		}
	}

	// Insert outbox event with uppercase status so the outbox poller can pick it up.
	if len(outboxPayload) > 0 {
		outboxQuery := `
			INSERT INTO outbox_events (aggregate_id, topic, payload, status)
			VALUES ($1, 'orders.created', $2, 'PENDING')
		`
		_, err = tx.Exec(ctx, outboxQuery, order.TrackingID, outboxPayload)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// fetchOrderItems retrieves items for a given order
func (r *OrderRepository) fetchOrderItems(ctx context.Context, orderID string) ([]models.OrderItem, error) {
	query := `SELECT product_tracking_id, quantity, price_at_checkout FROM order_items WHERE order_tracking_id = $1`
	rows, err := r.reader.Query(ctx, query, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []models.OrderItem
	for rows.Next() {
		var item models.OrderItem
		if err := rows.Scan(&item.ProductTrackingID, &item.Quantity, &item.PriceAtCheckout); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// GetOrderByTrackingID retrieves an order by its UTID
func (r *OrderRepository) GetOrderByTrackingID(ctx context.Context, trackingID string) (*models.Order, error) {
	query := `
		SELECT id, order_tracking_id, customer_tracking_id, store_tracking_id, vendor_tracking_id, rider_tracking_id, status, delivery_type, payment_gateway, total_amount, currency, otp_code, customer_lat, customer_lng, created_at, updated_at
		FROM orders
		WHERE order_tracking_id = $1
	`
	var order models.Order
	var riderID *string
	var deliveryType *string
	var paymentGateway *string
	var otpCode *string
	var customerLat *float64
	var customerLng *float64
	var createdAt *time.Time
	var updatedAt *time.Time

	err := r.reader.QueryRow(ctx, query, trackingID).Scan(
		&order.ID, &order.TrackingID, &order.UserTrackID, &order.VendorStoreTrackID,
		&order.VendorTrackID, &riderID,
		&order.Status, &deliveryType, &paymentGateway,
		&order.TotalAmount, &order.Currency, &otpCode,
		&customerLat, &customerLng,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	if riderID != nil {
		order.RiderTrackID = *riderID
	}
	if deliveryType != nil {
		order.DeliveryType = *deliveryType
	}
	if paymentGateway != nil {
		order.PaymentGateway = *paymentGateway
	}
	if otpCode != nil {
		order.OTPCode = *otpCode
	}
	if customerLat != nil {
		order.CustomerLat = *customerLat
	}
	if customerLng != nil {
		order.CustomerLng = *customerLng
	}
	if createdAt != nil {
		order.CreatedAt = *createdAt
	}
	if updatedAt != nil {
		order.UpdatedAt = *updatedAt
	}

	items, err := r.fetchOrderItems(ctx, order.TrackingID)
	if err != nil {
		return nil, err
	}
	order.Items = items
	if order.Items == nil {
		order.Items = []models.OrderItem{}
	}

	return &order, nil
}

// GetOrdersByCustomerID retrieves all orders placed by a customer.
// Pass limit=0 to fetch all (back-compat); status="" for no filter.
func (r *OrderRepository) GetOrdersByCustomerID(ctx context.Context, customerID string, limit int, status string) ([]*models.Order, error) {
	args := []interface{}{customerID}
	where := "WHERE customer_tracking_id = $1"
	if status != "" {
		args = append(args, status)
		where += " AND status = $2"
	}
	argsLen := len(args)
	limitClause := ""
	if limit > 0 {
		args = append(args, limit)
		limitClause = fmt.Sprintf(" LIMIT $%d", argsLen+1)
	}
	query := `
		SELECT id, order_tracking_id, customer_tracking_id, store_tracking_id, vendor_tracking_id, rider_tracking_id, status, delivery_type, payment_gateway, total_amount, currency, otp_code, customer_lat, customer_lng, created_at, updated_at
		FROM orders
		` + where + `
		ORDER BY created_at DESC
		` + limitClause
	rows, err := r.reader.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []*models.Order
	for rows.Next() {
		var order models.Order
		var riderID *string
		var deliveryType *string
		var paymentGateway *string
		var otpCode *string
		var customerLat *float64
		var customerLng *float64
		var createdAt *time.Time
		var updatedAt *time.Time

		err := rows.Scan(
			&order.ID, &order.TrackingID, &order.UserTrackID, &order.VendorStoreTrackID,
			&order.VendorTrackID, &riderID,
			&order.Status, &deliveryType, &paymentGateway,
			&order.TotalAmount, &order.Currency, &otpCode,
			&customerLat, &customerLng,
			&createdAt, &updatedAt,
		)
		if err != nil {
			return nil, err
		}

		if riderID != nil {
			order.RiderTrackID = *riderID
		}
		if deliveryType != nil {
			order.DeliveryType = *deliveryType
		}
		if paymentGateway != nil {
			order.PaymentGateway = *paymentGateway
		}
		if otpCode != nil {
			order.OTPCode = *otpCode
		}
		if customerLat != nil {
			order.CustomerLat = *customerLat
		}
		if customerLng != nil {
			order.CustomerLng = *customerLng
		}
		if createdAt != nil {
			order.CreatedAt = *createdAt
		}
		if updatedAt != nil {
			order.UpdatedAt = *updatedAt
		}

		orders = append(orders, &order)
	}

	// N+1 query issue here natively, but acceptable for this prototype scale.
	// In production, use an IN clause or JOIN.
	for _, o := range orders {
		items, _ := r.fetchOrderItems(ctx, o.TrackingID)
		o.Items = items
		if o.Items == nil {
			o.Items = []models.OrderItem{}
		}
	}

	return orders, nil
}

// GetOrdersByVendorID retrieves all orders for stores owned by a vendor, joining user details
func (r *OrderRepository) GetOrdersByVendorID(ctx context.Context, vendorID string) ([]*models.Order, error) {
	query := `
		SELECT 
			o.id, o.order_tracking_id, o.customer_tracking_id, o.store_tracking_id, o.vendor_tracking_id, o.rider_tracking_id, 
			o.status, o.delivery_type, o.payment_gateway, o.total_amount, o.currency, o.otp_code, o.customer_lat, o.customer_lng, 
			o.created_at, o.updated_at,
			COALESCE(c.full_name, 'Unknown Customer'),
			COALESCE(c.phone, ''),
			COALESCE(rd.full_name, 'Unassigned Rider'),
			COALESCE(rd.phone, '')
		FROM orders o
		LEFT JOIN users c ON o.customer_tracking_id = c.tracking_id
		LEFT JOIN users rd ON o.rider_tracking_id = rd.tracking_id
		WHERE o.vendor_tracking_id = $1 OR o.store_tracking_id IN (SELECT store_tracking_id FROM stores WHERE vendor_tracking_id = $1)
		ORDER BY o.created_at DESC
	`
	rows, err := r.reader.Query(ctx, query, vendorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []*models.Order
	for rows.Next() {
		var order models.Order
		var riderID *string
		var deliveryType *string
		var paymentGateway *string
		var otpCode *string
		var customerLat *float64
		var customerLng *float64
		var createdAt *time.Time
		var updatedAt *time.Time
		var custName string
		var custPhone string
		var riderName string
		var riderPhone string

		err := rows.Scan(
			&order.ID, &order.TrackingID, &order.UserTrackID, &order.VendorStoreTrackID,
			&order.VendorTrackID, &riderID,
			&order.Status, &deliveryType, &paymentGateway,
			&order.TotalAmount, &order.Currency, &otpCode,
			&customerLat, &customerLng,
			&createdAt, &updatedAt,
			&custName, &custPhone, &riderName, &riderPhone,
		)
		if err != nil {
			return nil, err
		}

		if riderID != nil {
			order.RiderTrackID = *riderID
		}
		if deliveryType != nil {
			order.DeliveryType = *deliveryType
		}
		if paymentGateway != nil {
			order.PaymentGateway = *paymentGateway
		}
		if otpCode != nil {
			order.OTPCode = *otpCode
		}
		if customerLat != nil {
			order.CustomerLat = *customerLat
		}
		if customerLng != nil {
			order.CustomerLng = *customerLng
		}
		if createdAt != nil {
			order.CreatedAt = *createdAt
		}
		if updatedAt != nil {
			order.UpdatedAt = *updatedAt
		}
		order.CustomerName = custName
		order.CustomerPhone = custPhone
		order.RiderName = riderName
		order.RiderPhone = riderPhone

		orders = append(orders, &order)
	}

	for _, o := range orders {
		items, _ := r.fetchOrderItems(ctx, o.TrackingID)
		o.Items = items
		if o.Items == nil {
			o.Items = []models.OrderItem{}
		}
	}

	return orders, nil
}

// UpdateOrderStatus mutates order status and sets updated_at
func (r *OrderRepository) UpdateOrderStatus(ctx context.Context, trackingID string, status string) error {
	query := `
		UPDATE orders
		SET status = $1, updated_at = NOW()
		WHERE order_tracking_id = $2
	`
	res, err := r.writer.Exec(ctx, query, status, trackingID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return errors.New("order not found")
	}
	return nil
}

// MarkOrderDelivered sets status to 'delivered' AND stamps delivered_at
// in a single transaction. The escrow release cron
// (`escrow_cron.go`) requires both columns to settle funds after the
// 48-hour dispute window.
func (r *OrderRepository) MarkOrderDelivered(ctx context.Context, trackingID string) error {
	query := `
		UPDATE orders
		SET status = 'delivered',
		    delivered_at = NOW(),
		    updated_at = NOW()
		WHERE order_tracking_id = $1
	`
	res, err := r.writer.Exec(ctx, query, trackingID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return errors.New("order not found")
	}
	return nil
}

// RecordVendorHandover persists the handover audit trail. The vendor
// captured a photo of the package being handed to the rider, plus
// optional notes. Returns an error if the order is not in a state
// where a handover makes sense (must be 'accepted' or 'shipped').
func (r *OrderRepository) RecordVendorHandover(ctx context.Context, orderTrackingID, photoURL, notes string) error {
	query := `
		UPDATE orders
		SET handover_photo_url = $1,
		    handover_at = NOW(),
		    handover_notes = NULLIF($2, ''),
		    updated_at = NOW()
		WHERE order_tracking_id = $3
		  AND status IN ('accepted', 'shipped', 'in_transit')
	`
	res, err := r.writer.Exec(ctx, query, photoURL, notes, orderTrackingID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return errors.New("order not found or not in a handover-eligible state")
	}
	return nil
}

// FetchPendingOutboxEvents retrieves a batch of pending events for publishing
func (r *OrderRepository) FetchPendingOutboxEvents(ctx context.Context, limit int) ([]models.OutboxEvent, error) {
	query := `
		SELECT id, aggregate_id, topic, payload, status
		FROM outbox_events
		WHERE status = 'PENDING'
		ORDER BY id ASC
		LIMIT $1
	`
	rows, err := r.reader.Query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []models.OutboxEvent
	for rows.Next() {
		var event models.OutboxEvent
		if err := rows.Scan(&event.ID, &event.AggregateID, &event.Topic, &event.Payload, &event.Status); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

// MarkOutboxEventProcessed marks an event as processed
func (r *OrderRepository) MarkOutboxEventProcessed(ctx context.Context, id int64) error {
	query := `
		UPDATE outbox_events
		SET status = 'PROCESSED', processed_at = NOW()
		WHERE id = $1
	`
	_, err := r.writer.Exec(ctx, query, id)
	return err
}
