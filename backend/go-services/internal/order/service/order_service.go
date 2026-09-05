package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnigo/backend/internal/order/models"
	"github.com/omnigo/backend/internal/order/repository"
	"github.com/omnigo/backend/internal/product/pb"
	"github.com/omnigo/backend/internal/shared/cache"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/security"
	"github.com/omnigo/backend/internal/shared/tracking"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Custom domain errors
var (
	ErrDuplicateRequest = errors.New("CONFLICT_DUPLICATE_TRANSACTION: request already processed or in progress")
	ErrDatabaseFailure  = errors.New("INTERNAL_DB_ERROR: database transaction failed")
)

type OrderService struct {
	repo              *repository.OrderRepository
	kafka             *messaging.KafkaClient
	redis             *cache.RedisClient
	productGRPCClient pb.ProductInventoryServiceClient
	productServiceURL string
	// internalSigner signs all internal HTTP calls to product-service.
	// nil means signing is disabled (dev mode); production must set it.
	internalSigner *security.InternalSigner
	codService     CODRecorder
	escrowService  EscrowHolder
}

// CODRecorder is the small surface the order service needs from the COD service.
type CODRecorder interface {
	OnOrderCreated(ctx context.Context, orderID string, amountPaisa int64, currency string) error
}

// EscrowHolder creates escrow holds when orders are paid, and cancels them on cancel/return.
type EscrowHolder interface {
	CreateHold(ctx context.Context, orderID, vendorID string, amount int64) error
	CreateHoldTx(ctx context.Context, tx pgx.Tx, orderID, vendorID string, amount int64) error
	CancelForOrder(ctx context.Context, orderTrackingID string) error
}

func NewOrderService(
	repo *repository.OrderRepository,
	kafka *messaging.KafkaClient,
	redis *cache.RedisClient,
	productGRPCClient pb.ProductInventoryServiceClient,
	productServiceURL string,
	internalSigner *security.InternalSigner,
) *OrderService {
	return &OrderService{
		repo:              repo,
		kafka:             kafka,
		redis:             redis,
		productGRPCClient: productGRPCClient,
		productServiceURL: productServiceURL,
		internalSigner:    internalSigner,
	}
}

// WithCODService attaches the COD recorder so COD orders create a pending payment transaction.
func (s *OrderService) WithCODService(cs CODRecorder) *OrderService {
	s.codService = cs
	return s
}

// WithEscrowService attaches the escrow holder so paid orders create an escrow hold.
func (s *OrderService) WithEscrowService(es EscrowHolder) *OrderService {
	s.escrowService = es
	return s
}

func generateUTID() string {
	return tracking.Generate("ORD")
}

// CreateOrder handles order creation with Postgres-backed fail-closed idempotency.
// Stock reservations are created locally in the same transaction; a background
// worker confirms them via gRPC to the product service.
// This eliminates the saga race where gRPC succeeded but DB failed (orphaned stock).
func (s *OrderService) CreateOrder(ctx context.Context, req *models.CreateOrderRequest) (*models.Order, error) {

	// Best-effort early duplicate filter. If Redis is down, the Postgres unique
	// constraint still prevents duplicates, so we continue rather than fail.
	if s.redis != nil && req.DeviceSessionNonce != "" {
		idempotencyKey := fmt.Sprintf("idempotency:%s:%s", req.UserTrackID, req.DeviceSessionNonce)
		success, err := s.redis.Client.SetNX(ctx, idempotencyKey, "1", 120*time.Second).Result()
		if err != nil {
			fmt.Printf("System Log: Redis idempotency check failed (%v), falling back to Postgres unique constraint\n", err)
		} else if !success {
			return nil, ErrDuplicateRequest
		}
	}

	// Build order first (we need the tracking ID for local reservations)
	utid := generateUTID()
	_ = 0 // calculatedTotal reserved for future use

	// Support both payment_gateway and payment_method from frontend
	paymentGW := req.PaymentGateway
	if paymentGW == "" {
		paymentGW = req.PaymentMethod
	}

	// Validate: if payment_method is explicitly set to "cod", payment_gateway must not indicate prepaid
	if req.PaymentMethod != "" && strings.EqualFold(req.PaymentMethod, "cod") {
		if req.PaymentGateway != "" && !strings.EqualFold(req.PaymentGateway, "cod") {
			return nil, fmt.Errorf("CONFLICT_CONFLICTING_PAYMENT_METHOD: payment_method=cod but payment_gateway=%s is a prepaid gateway; cannot treat order as cash-on-delivery", req.PaymentGateway)
		}
	}

	// NOTE: We don't have product prices yet (no gRPC call).
	// The frontend sends TotalAmount which we trust for now.
	// The background worker will reconcile prices via product service.
	// H4: Uber-style — customer pays product total + delivery fee.
	productTotalPaisa := int64(req.TotalAmount * 100)
	order := &models.Order{
		UserTrackID:           req.UserTrackID,
		VendorStoreTrackID:    req.VendorStoreTrackID, // frontend must provide this
		VendorTrackID:         req.VendorStoreTrackID, // placeholder; worker will resolve
		Currency:              req.Currency,
		PaymentGateway:        paymentGW,
		Status:                "pending",
		CustomerLat:           req.DropoffLat,
		CustomerLng:           req.DropoffLng,
		DeviceSessionNonce:    req.DeviceSessionNonce,
		TrackingID:            utid,
		TotalAmount:           req.TotalAmount,
		TotalAmountPaisa:      productTotalPaisa,
		BaseProductAmountPaisa: productTotalPaisa,       // H4: product portion in paisa
		DeliveryFeeAmountPaisa: req.DeliveryFeePaisa,   // H4: delivery fee in paisa
		TotalBilledAmountPaisa: productTotalPaisa + req.DeliveryFeePaisa, // H4: customer pays this
		RoutingStatus:         req.RoutingStatus,        // H3: DYNAMIC_CALCULATED | FALLBACK_HAVERSINE | FAILED_CALCULATION
	}

	// Build order items from request (prices will be reconciled by worker)
	for _, item := range req.Items {
		order.Items = append(order.Items, models.OrderItem{
			ProductTrackingID: item.ProductTrackingID,
			Quantity:          item.Quantity,
			PriceAtCheckout:   0, // will be filled by stock confirmation worker
		})
	}

	// H4: Build items summary string for rider display
	var itemsSummary string
	for i, item := range order.Items {
		if i > 0 {
			itemsSummary += ", "
		}
		itemsSummary += fmt.Sprintf("%dx %s", item.Quantity, item.ProductTrackingID)
		if item.ProductName != "" {
			itemsSummary += fmt.Sprintf(" (%s)", item.ProductName)
		}
	}

	// H4: Fetch customer name and address for rider delivery verification
	customerName, customerAddress, _ := s.repo.GetUserInfo(ctx, order.UserTrackID)

	isCOD := order.PaymentGateway == "" || strings.EqualFold(order.PaymentGateway, "cod")
	event := models.OrderEvent{
		OrderID:            order.TrackingID,
		UserTrackID:        order.UserTrackID,
		VendorStoreTrackID: order.VendorStoreTrackID,
		Items:              order.Items,
		ItemsSummary:       itemsSummary,
		TotalAmountPaisa:   int64(order.TotalAmount * 100),
		TotalAmountRupees:  order.TotalAmount,
		IsCOD:              isCOD,
		CustomerPhone:      order.CustomerPhone,
		CustomerName:       customerName,
		CustomerAddress:    customerAddress,
		DropoffLat:         order.CustomerLat,
		DropoffLng:         order.CustomerLng,
		Timestamp:          time.Now().UnixMilli(),
	}

	eventBytes, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("failed serializing outbox event: %w", err)
	}

	// 3. Save to DB atomically: order + items + outbox + LOCAL stock reservations
	// This is the critical fix: reservations are created in the SAME transaction
	// as the order, so we never have "reserved stock but no order" or vice versa.
	err = s.repo.CreateOrderWithReservations(ctx, order, eventBytes)
	if err != nil {
		log.Printf("[OrderService] CreateOrder DB error: %v", err)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDuplicateRequest
		}
		return nil, fmt.Errorf("INTERNAL_DB_ERROR: %w", err)
	}

	// 4. Record COD pending transaction for cash-on-delivery orders.
	if isCOD && s.codService != nil {
		totalAmountPaisa := int64(order.TotalAmount * 100)
		if err := s.codService.OnOrderCreated(ctx, order.TrackingID, totalAmountPaisa, order.Currency); err != nil {
			fmt.Printf("[OrderService] Warning: failed to record COD pending transaction for order %s: %v\n", order.TrackingID, err)
		}
	}

	return order, nil
}

// releaseStockCompensating runs asynchronously to release stock if order creation fails
func (s *OrderService) releaseStockCompensating(items []models.OrderItem) {
	ctx := context.Background()

	var pbItems []*pb.OrderItem
	for _, item := range items {
		pbItems = append(pbItems, &pb.OrderItem{
			ProductTrackingId: item.ProductTrackingID,
			Quantity:          int32(item.Quantity),
		})
	}

	grpcReq := &pb.ReleaseRequest{
		Items:           pbItems,
		OrderTrackingId: "",
	}

	res, err := s.productGRPCClient.ReleaseProduct(ctx, grpcReq)
	if err != nil {
		fmt.Printf("SAGA FAILURE: could not release stock via gRPC: %v\n", err)
		return
	}

	if !res.Success {
		fmt.Printf("SAGA FAILURE: failed to release stock via gRPC: %s\n", res.Message)
	}
}

func (s *OrderService) GetOrder(ctx context.Context, trackingID string) (*models.Order, error) {
	return s.repo.GetOrderByTrackingID(ctx, trackingID)
}

func (s *OrderService) GetOrdersByCustomer(ctx context.Context, customerID string, limit int, status string) ([]*models.Order, error) {
	return s.repo.GetOrdersByCustomerID(ctx, customerID, limit, status)
}

func (s *OrderService) GetOrdersByVendor(ctx context.Context, vendorID string, status string, limit, offset int) ([]*models.Order, error) {
	return s.repo.GetOrdersByVendorID(ctx, vendorID, status, limit, offset)
}

// IsOrderSettled checks if the order's payment has been settled (funds disbursed
// to vendor/delivery via outbox). Returns true if the settlement outbox event
// was processed. Used by refund handler to determine if vendor clawback is needed.
func (s *OrderService) IsOrderSettled(ctx context.Context, orderTrackingID string) (bool, error) {
	var count int
	err := s.repo.DB().QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_events
		 WHERE aggregate_id = $1 AND topic = 'payment_settlement' AND status = 'PROCESSED'`,
		orderTrackingID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ReleaseStockForOrder releases reserved stock via product-service gRPC. It is
// safe to call from cancellation/refund handlers even if the order has no items.
func (s *OrderService) ReleaseStockForOrder(ctx context.Context, order *models.Order) error {
	if order == nil || len(order.Items) == 0 {
		return nil
	}
	var pbItems []*pb.OrderItem
	for _, item := range order.Items {
		pbItems = append(pbItems, &pb.OrderItem{
			ProductTrackingId: item.ProductTrackingID,
			Quantity:          int32(item.Quantity),
		})
	}
	grpcReq := &pb.ReleaseRequest{
		Items:           pbItems,
		OrderTrackingId: order.TrackingID,
	}
	res, err := s.productGRPCClient.ReleaseProduct(ctx, grpcReq)
	if err != nil {
		return fmt.Errorf("product service gRPC unavailable during stock release: %w", err)
	}
	if !res.Success {
		return fmt.Errorf("failed to release stock: %s", res.Message)
	}
	return nil
}

var validOrderTransitions = map[string][]string{
	"pending":    {"paid", "accepted", "cancelled", "failed"},
	"paid":       {"accepted", "cancelled", "refunded"},
	"accepted":   {"shipped", "cancelled"},
	"shipped":    {"in_transit", "delivered", "failed"},
	"in_transit": {"delivered", "failed"},
	"delivered":  {"completed", "refunded", "returned"},
	"completed":  {"refunded", "returned"},
	"cancelled":  {},
	"failed":     {},
	"refunded":   {},
	"returned":   {},
}

func isValidOrderTransition(from, to string) bool {
	from = strings.ToLower(from)
	to = strings.ToLower(to)
	if from == to {
		return false
	}
	allowed, ok := validOrderTransitions[from]
	if !ok {
		return false
	}
	for _, target := range allowed {
		if target == to {
			return true
		}
	}
	return false
}

func (s *OrderService) UpdateOrderStatus(ctx context.Context, trackingID string, status string) error {
	status = strings.ToLower(status)
	order, err := s.repo.GetOrderByTrackingID(ctx, trackingID)
	if err != nil {
		return fmt.Errorf("order not found: %w", err)
	}

	if order.Status == status {
		return nil // Already in requested state, no-op
	}

	if !isValidOrderTransition(order.Status, status) {
		return fmt.Errorf("invalid order status transition from '%s' to '%s'", order.Status, status)
	}

	// H-1 FIX: Use transaction for status="paid" to ensure escrow creation is atomic.
	// If status update succeeds but escrow creation fails, we'd have paid order without escrow.
	// We wrap the critical "paid" status change in a transaction.
	if status == "paid" && s.escrowService != nil {
		return s.updateOrderStatusPaidAtomic(ctx, trackingID, order)
	}

	// For non-paid statuses, use the original flow (no critical side effects)
	err = s.repo.UpdateOrderStatus(ctx, trackingID, status)
	if err != nil {
		return err
	}

	// Side effects — only execute after confirmed status change
	if status == "cancelled" || status == "failed" {
		if len(order.Items) > 0 {
			_ = s.ReleaseStockForOrder(ctx, order)
		}
		if s.escrowService != nil {
			if err := s.escrowService.CancelForOrder(ctx, trackingID); err != nil {
				log.Printf("[ORDER-%s] Warning: failed to cancel escrow on %s: %v", trackingID, status, err)
			}
		}
		if s.repo != nil {
			if err := s.repo.CancelCODDebtsForOrder(ctx, trackingID); err != nil {
				log.Printf("[ORDER-%s] Warning: failed to cancel COD debts: %v", trackingID, err)
			}
		}
	}

	if status == "returned" && s.escrowService != nil {
		if err := s.escrowService.CancelForOrder(ctx, trackingID); err != nil {
			log.Printf("[ORDER-%s] Warning: failed to cancel escrow on return: %v", trackingID, err)
		}
	}

	// COD settlement is handled entirely by delivery_service.UpdateGigStatus() when
	// the rider completes the delivery. The delivery service has access to the gig's
	// actual delivery_fee, admin_commission, and rider_earning — so it performs the
	// correct split. We must NOT duplicate settlement here to avoid double vendor credit.
	//
	// Legacy: This block previously called codService.OnOrderDelivered() with hardcoded
	// 2% commission and zero rider earning, which caused double vendor wallet credit
	// when combined with delivery_service's settlement. Removed.

	// Outbox handles orders.created, but we keep direct produce for orders.updated for simplicity in Phase 6
	if s.kafka != nil {
		event := map[string]interface{}{
			"order_id":             trackingID,
			"status":               status,
			"customer_tracking_id": order.UserTrackID,
			"vendor_tracking_id":   order.VendorTrackID,
			"timestamp":            time.Now().UnixMilli(),
		}
		eventBytes, mErr := json.Marshal(event)
		if mErr != nil {
			log.Printf("[MEDIUM-03] failed to marshal orders.updated event for %s: %v — skipping produce", trackingID, mErr)
		} else {
			record := &kgo.Record{
				Topic: "orders.updated",
				Key:   []byte(trackingID),
				Value: eventBytes,
			}
			s.kafka.Client.Produce(context.Background(), record, func(_ *kgo.Record, err error) {
				if err != nil {
					fmt.Printf("Warning: Failed to produce orders.updated event: %v\n", err)
				}
			})
		}
	}

	return nil
}

// updateOrderStatusPaidAtomic wraps status="paid" update + escrow creation in a transaction.
// This prevents the TOCTOU race where order is marked paid but escrow hold creation fails.
func (s *OrderService) updateOrderStatusPaidAtomic(ctx context.Context, trackingID string, order *models.Order) error {
	// Get the pool from repo to begin transaction
	var pool *pgxpool.Pool = s.repo.DB()
	if pool == nil {
		return fmt.Errorf("cannot get database pool for transaction")
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Update status within transaction
	err = s.repo.UpdateOrderStatusTx(ctx, tx, trackingID, "paid")
	if err != nil {
		return err
	}

	// Create escrow hold within the same transaction
	holdAmountPaisa := int64(order.VendorEscrow * 100)
	if holdAmountPaisa <= 0 {
		holdAmountPaisa = int64((order.TotalAmount - order.AdminCommission - order.DeliveryEscrow) * 100)
		if holdAmountPaisa < 0 {
			holdAmountPaisa = 0
		}
	}

	if holdAmountPaisa > 0 {
		if err := s.escrowService.CreateHoldTx(ctx, tx, trackingID, order.VendorTrackID, holdAmountPaisa); err != nil {
			return fmt.Errorf("failed to create escrow hold: %w", err)
		}
	}

	// Commit transaction only if both status update and escrow creation succeed
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Produce Kafka event after successful commit (best-effort)
	if s.kafka != nil {
		event := map[string]interface{}{
			"order_id":             trackingID,
			"status":               "paid",
			"customer_tracking_id": order.UserTrackID,
			"vendor_tracking_id":   order.VendorTrackID,
			"timestamp":            time.Now().UnixMilli(),
		}
		eventBytes, mErr := json.Marshal(event)
		if mErr != nil {
			log.Printf("[H-1] failed to marshal orders.updated event for %s: %v", trackingID, mErr)
		} else {
			record := &kgo.Record{
				Topic: "orders.updated",
				Key:   []byte(trackingID),
				Value: eventBytes,
			}
			s.kafka.Client.Produce(context.Background(), record, func(_ *kgo.Record, err error) {
				if err != nil {
					fmt.Printf("Warning: Failed to produce orders.updated event: %v\n", err)
				}
			})
		}
	}

	return nil
}

// MarkOrderDelivered is the canonical "delivery completed" action.
// BUG-1 FIX: Previously this double-wrote status (repo + service), causing
// the Kafka orders.updated event to be silently dropped. Now it delegates
// entirely to UpdateOrderStatus, which stamps delivered_at atomically and
// emits the Kafka event. If the order is already delivered, this is a
// safe no-op (returns nil) to avoid breaking callers like ConfirmOrder
// and the delivery status consumer.
func (s *OrderService) MarkOrderDelivered(ctx context.Context, trackingID string) error {
	err := s.UpdateOrderStatus(ctx, trackingID, "delivered")
	if err != nil && errors.Is(err, repository.ErrNoStatusChange) {
		return nil // already delivered — safe no-op
	}
	return err
}

// OrderLineage contains the unified lineage for an order across all actors.
type OrderLineage struct {
	OrderTrackingID    string  `json:"order_tracking_id"`
	CustomerTrackingID string  `json:"customer_tracking_id"`
	VendorTrackingID   string  `json:"vendor_tracking_id"`
	StoreTrackingID    string  `json:"store_tracking_id"`
	DeliveryTrackingID string  `json:"delivery_tracking_id,omitempty"`
	RiderTrackingID    string  `json:"rider_tracking_id,omitempty"`
	Status             string  `json:"status"`
	TotalAmount        float64 `json:"total_amount"`
	Currency           string  `json:"currency"`
	PaymentGateway     string  `json:"payment_gateway"`
}

// GetOrderLineage retrieves the full UTID lineage for an order.
func (s *OrderService) GetOrderLineage(ctx context.Context, trackingID string) (*OrderLineage, error) {
	order, err := s.repo.GetOrderByTrackingID(ctx, trackingID)
	if err != nil {
		return nil, fmt.Errorf("order not found: %w", err)
	}

	lineage := &OrderLineage{
		OrderTrackingID:    order.TrackingID,
		CustomerTrackingID: order.UserTrackID,
		VendorTrackingID:   order.VendorTrackID,
		StoreTrackingID:    order.VendorStoreTrackID,
		Status:             order.Status,
		TotalAmount:        order.TotalAmount,
		Currency:           order.Currency,
		PaymentGateway:     order.PaymentGateway,
	}

	// Enrich with delivery tracking if available
	delivery, err := s.repo.GetDeliveryByOrderTrackingID(ctx, trackingID)
	if err == nil && delivery != nil {
		lineage.DeliveryTrackingID = delivery.TrackingID
		lineage.RiderTrackingID = delivery.RiderTrackID
	}

	return lineage, nil
}

// produceOrderEvent marshals an event payload and dispatches it to the given topic.
func (s *OrderService) produceOrderEvent(ctx context.Context, topic, trackingID, reason string, amount float64, currency string) {
	if s.kafka == nil {
		return
	}
	payload := map[string]any{
		"order_tracking_id": trackingID,
		"reason":            reason,
		"amount":            amount,
		"currency":          currency,
		"timestamp":         time.Now().UnixMilli(),
	}
	eventBytes, mErr := json.Marshal(payload)
	if mErr != nil {
		log.Printf("[MEDIUM-03] failed to marshal %s event for %s: %v — skipping produce", topic, trackingID, mErr)
		return
	}

	record := &kgo.Record{
		Topic: topic,
		Key:   []byte(trackingID),
		Value: eventBytes,
	}
	s.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
		if err != nil {
			fmt.Printf("Warning: Failed to produce %s event: %v\n", topic, err)
		}
	})
}

// EmitRefundEvent is a public helper used by payment handlers to announce a refund.
func (s *OrderService) EmitRefundEvent(ctx context.Context, trackingID, reason string, amount float64, currency string) {
	s.produceOrderEvent(ctx, "orders.refunded", trackingID, reason, amount, currency)
}

// EmitCancelEvent is a public helper used by payment handlers to announce a cancellation.
func (s *OrderService) EmitCancelEvent(ctx context.Context, trackingID, reason string) {
	s.produceOrderEvent(ctx, "orders.cancelled", trackingID, reason, 0, "")
}

// InsertOutboxEvent writes a refund intent to the outbox_events table atomically.
// The RefundOutboxWorker picks it up and publishes to Kafka.
// This is the C3 FIX: transactional outbox pattern.
func (s *OrderService) InsertOutboxEvent(ctx context.Context, aggregateID, topic, payloadJSON string) (int64, error) {
	var id int64
	err := s.repo.DB().QueryRow(ctx,
		`INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at, updated_at)
		 VALUES ($1, $2, $3::jsonb, 'PENDING', NOW(), NOW())
		 RETURNING id`,
		aggregateID, topic, payloadJSON).Scan(&id)
	return id, err
}

// StartOutboxPoller begins a background routine that atomically polls, claims, and publishes outbox events
func (s *OrderService) StartOutboxPoller(ctx context.Context) {
	if s.kafka == nil {
		fmt.Println("Warning: Kafka client is nil, outbox poller will not start")
		return
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			events, err := s.repo.ClaimPendingOutboxEvents(ctx, 50)
			if err != nil {
				fmt.Printf("Error claiming outbox events: %v\n", err)
				continue
			}

			for _, event := range events {
				ev := event
				record := &kgo.Record{
					Topic: ev.Topic,
					Key:   []byte(ev.AggregateID),
					Value: ev.Payload,
				}
				s.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
					if err != nil {
						fmt.Printf("Failed to publish outbox event %d: %v\n", ev.ID, err)
						_ = s.repo.MarkOutboxEventFailed(context.Background(), ev.ID)
					} else {
						// Mark processed
						_ = s.repo.MarkOutboxEventProcessed(context.Background(), ev.ID)
					}
				})
			}
		}
	}
}
