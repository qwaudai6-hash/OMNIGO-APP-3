package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

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
	OnOrderCreated(ctx context.Context, orderID string, amount float64, currency string) error
	OnOrderDelivered(ctx context.Context, orderID, vendorID, riderID string, orderTotal, commission, riderEarning float64) error
}

// EscrowHolder creates escrow holds when orders are paid, and cancels them on cancel/return.
type EscrowHolder interface {
	CreateHold(ctx context.Context, orderID, vendorID string, amount float64) error
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
// Redis is used only as a best-effort early duplicate filter; the unique constraint
// on (customer_tracking_id, device_session_nonce) is the final source of truth.
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

	// 1. Reserve Stock via Product Service (gRPC call)
	var pbItems []*pb.OrderItem
	for _, item := range req.Items {
		pbItems = append(pbItems, &pb.OrderItem{
			ProductTrackingId: item.ProductTrackingID,
			Quantity:          int32(item.Quantity),
		})
	}

	grpcReq := &pb.ReserveRequest{
		Items:           pbItems,
		OrderTrackingId: "",
	}

	res, err := s.productGRPCClient.ReserveProduct(ctx, grpcReq)
	if err != nil {
		return nil, fmt.Errorf("product service gRPC unavailable: %w", err)
	}

	if !res.Success {
		return nil, fmt.Errorf("failed to reserve stock: %s", res.Message)
	}

	if res.VendorTrackingId == "" {
		return nil, fmt.Errorf("product service gRPC did not return vendor tracking id")
	}

	// 2. Build Order and calculate accurate total
	utid := generateUTID()
	var calculatedTotal float64

	// Support both payment_gateway and payment_method from frontend
	paymentGW := req.PaymentGateway
	if paymentGW == "" {
		paymentGW = req.PaymentMethod
	}

	order := &models.Order{
		UserTrackID:        req.UserTrackID,
		VendorStoreTrackID: res.StoreTrackingId,
		VendorTrackID:      res.VendorTrackingId,
		Currency:           req.Currency,
		PaymentGateway:     paymentGW,
		Status:             "pending",
		CustomerLat:        req.DropoffLat,
		CustomerLng:        req.DropoffLng,
		DeviceSessionNonce: req.DeviceSessionNonce,
	}

	for _, reservedItem := range res.Items {
		order.Items = append(order.Items, models.OrderItem{
			ProductTrackingID: reservedItem.ProductTrackingId,
			Quantity:          int(reservedItem.Quantity),
			PriceAtCheckout:   reservedItem.PriceAtCheckout,
		})
		calculatedTotal += reservedItem.PriceAtCheckout * float64(reservedItem.Quantity)
	}

	order.TrackingID = utid
	order.TotalAmount = calculatedTotal

	isCOD := order.PaymentGateway == "" || strings.EqualFold(order.PaymentGateway, "cod")
	event := models.OrderEvent{
		OrderID:            order.TrackingID,
		UserTrackID:        order.UserTrackID,
		VendorStoreTrackID: order.VendorStoreTrackID,
		Items:              order.Items,
		TotalAmount:        order.TotalAmount,
		IsCOD:              isCOD,
		DropoffLat:         order.CustomerLat,
		DropoffLng:         order.CustomerLng,
		Timestamp:          time.Now().UnixMilli(),
	}

	eventBytes, err := json.Marshal(event)
	if err != nil {
		s.releaseStockCompensating(order.Items)
		return nil, fmt.Errorf("failed serializing outbox event: %w", err)
	}

	// 3. Save to DB and outbox atomically using the request-scoped context
	err = s.repo.CreateOrder(ctx, order, eventBytes)
	if err != nil {
		// Compensating Transaction: Release Stock
		go s.releaseStockCompensating(order.Items)

		log.Printf("[OrderService] CreateOrder DB error: %v", err)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDuplicateRequest
		}
		return nil, fmt.Errorf("INTERNAL_DB_ERROR: %w", err)
	}

	// 4. Record COD pending transaction for cash-on-delivery orders.
	if (order.PaymentGateway == "" || order.PaymentGateway == "cod") && s.codService != nil {
		if err := s.codService.OnOrderCreated(ctx, order.TrackingID, order.TotalAmount, order.Currency); err != nil {
			// Log only; order already created, COD transaction can be reconciled later.
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

func (s *OrderService) GetOrdersByVendor(ctx context.Context, vendorID string) ([]*models.Order, error) {
	return s.repo.GetOrdersByVendorID(ctx, vendorID)
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

	// If order is being cancelled or failed, compensate reserved inventory stock
	if status == "cancelled" || status == "failed" {
		if len(order.Items) > 0 {
			_ = s.ReleaseStockForOrder(ctx, order)
		}
		// BUG-06 FIX: Cancel escrow hold so vendor doesn't receive funds for cancelled order
		if s.escrowService != nil {
			if err := s.escrowService.CancelForOrder(ctx, trackingID); err != nil {
				log.Printf("[ORDER-%s] Warning: failed to cancel escrow on %s: %v", trackingID, status, err)
			}
		}
		// BUG-09 FIX: Cancel any pending COD debts for this order so riders don't owe for cancelled orders
		if s.repo != nil {
			if err := s.repo.CancelCODDebtsForOrder(ctx, trackingID); err != nil {
				log.Printf("[ORDER-%s] Warning: failed to cancel COD debts: %v", trackingID, err)
			}
		}
	}

	// BUG-06 FIX: Cancel escrow on return as well
	if status == "returned" && s.escrowService != nil {
		if err := s.escrowService.CancelForOrder(ctx, trackingID); err != nil {
			log.Printf("[ORDER-%s] Warning: failed to cancel escrow on return: %v", trackingID, err)
		}
	}

	err = s.repo.UpdateOrderStatus(ctx, trackingID, status)
	if err != nil {
		return err
	}

	// When an order is marked paid (online payment via PayFast/Stripe/JazzCash/EasyPaisa),
	// create an escrow hold so the funds are locked for 48 hours before vendor payout.
	// This covers both the modern Option C flow AND the deprecated hosted checkout path.
	if status == "paid" && s.escrowService != nil {
		holdAmount := order.VendorEscrow
		if holdAmount <= 0 {
			// Fallback for legacy flows that didn't populate vendor_escrow
			holdAmount = order.TotalAmount - order.AdminCommission - order.DeliveryEscrow
			if holdAmount < 0 {
				holdAmount = 0
			}
		}
		
		if holdAmount > 0 {
			if err := s.escrowService.CreateHold(ctx, trackingID, order.VendorTrackID, holdAmount); err != nil {
				log.Printf("[ORDER-%s] Warning: failed to create escrow hold on payment: %v", trackingID, err)
			}
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

// MarkOrderDelivered is the canonical "delivery completed" action. It
// atomically stamps the status as `delivered` AND sets `delivered_at`,
// which is the precondition the escrow release cron
// (`escrow_cron.go`) checks before settling funds.
func (s *OrderService) MarkOrderDelivered(ctx context.Context, trackingID string) error {
	if err := s.repo.MarkOrderDelivered(ctx, trackingID); err != nil {
		return err
	}
	// Deliveries satisfy the precondition for 24h escrow window. Broadcast
	// update event so downstream telemetry/notifications pick it up.
	return s.UpdateOrderStatus(ctx, trackingID, "delivered")
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
