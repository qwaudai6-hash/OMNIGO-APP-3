package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
}

// CODRecorder is the small surface the order service needs from the COD service.
type CODRecorder interface {
	OnOrderCreated(ctx context.Context, orderID string, amount float64, currency string) error
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

	order := &models.Order{
		UserTrackID:        req.UserTrackID,
		VendorStoreTrackID: res.StoreTrackingId,
		VendorTrackID:      res.VendorTrackingId,
		Currency:           req.Currency,
		PaymentGateway:     req.PaymentGateway,
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

	event := models.OrderEvent{
		OrderID:            order.TrackingID,
		UserTrackID:        order.UserTrackID,
		VendorStoreTrackID: order.VendorStoreTrackID,
		Items:              order.Items,
		TotalAmount:        order.TotalAmount,
		DropoffLat:         order.CustomerLat,
		DropoffLng:         order.CustomerLng,
		Timestamp:          time.Now().UnixMilli(),
	}

	eventBytes, _ := json.Marshal(event)

	// 3. Save to DB and outbox atomically using the request-scoped context
	err = s.repo.CreateOrder(ctx, order, eventBytes)
	if err != nil {
		// Compensating Transaction: Release Stock
		go s.releaseStockCompensating(order.Items)

		// Postgres unique violation on idempotency constraint -> duplicate request.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrDuplicateRequest
		}
		return nil, ErrDatabaseFailure
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

func (s *OrderService) UpdateOrderStatus(ctx context.Context, trackingID string, status string) error {
	err := s.repo.UpdateOrderStatus(ctx, trackingID, status)
	if err != nil {
		return err
	}

	// Fetch order to get customer and vendor ids for broadcasting
	order, _ := s.repo.GetOrderByTrackingID(ctx, trackingID)

	// Outbox handles orders.created, but we keep direct produce for orders.updated for simplicity in Phase 6
	if s.kafka != nil && order != nil {
		event := map[string]interface{}{
			"order_id":             trackingID,
			"status":               status,
			"customer_tracking_id": order.UserTrackID,
			"vendor_tracking_id":   order.VendorTrackID,
			"timestamp":            time.Now().UnixMilli(),
		}
		eventBytes, _ := json.Marshal(event)
		record := &kgo.Record{
			Topic: "orders.updated",
			Key:   []byte(trackingID),
			Value: eventBytes,
		}
		s.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
			if err != nil {
				fmt.Printf("Warning: Failed to produce orders.updated event: %v\n", err)
			}
		})
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

	// Emit the orders.updated event so downstream consumers (admin
	// dashboards, email-service, analytics-worker) can react.
	if s.kafka != nil {
		order, _ := s.repo.GetOrderByTrackingID(ctx, trackingID)
		if order != nil {
			event := map[string]interface{}{
				"order_id":             trackingID,
				"status":               "delivered",
				"customer_tracking_id": order.UserTrackID,
				"vendor_tracking_id":   order.VendorTrackID,
				"delivered_at":         time.Now().UTC().Format(time.RFC3339),
				"timestamp":            time.Now().UnixMilli(),
			}
			eventBytes, _ := json.Marshal(event)
			record := &kgo.Record{
				Topic: "orders.updated",
				Key:   []byte(trackingID),
				Value: eventBytes,
			}
			s.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
				if err != nil {
					fmt.Printf("Warning: Failed to produce orders.updated (delivered) event: %v\n", err)
				}
			})
		}
	}

	return nil
}

// produceOrderEvent emits a domain-specific Kafka topic for lifecycle events such as
// orders.refunded or orders.cancelled. It is best-effort: callers should not fail
// their primary transaction if Kafka is unavailable.
func (s *OrderService) produceOrderEvent(ctx context.Context, topic, trackingID, reason string, amount float64, currency string) {
	if s.kafka == nil {
		return
	}

	order, _ := s.repo.GetOrderByTrackingID(ctx, trackingID)
	payload := map[string]interface{}{
		"order_id":  trackingID,
		"reason":    reason,
		"amount":    amount,
		"currency":  currency,
		"timestamp": time.Now().UnixMilli(),
	}
	if order != nil {
		payload["customer_tracking_id"] = order.UserTrackID
		payload["vendor_tracking_id"] = order.VendorTrackID
	}
	eventBytes, _ := json.Marshal(payload)

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

// StartOutboxPoller begins a background routine that polls and publishes outbox events
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
			events, err := s.repo.FetchPendingOutboxEvents(ctx, 50)
			if err != nil {
				fmt.Printf("Error fetching outbox events: %v\n", err)
				continue
			}

			for _, event := range events {
				record := &kgo.Record{
					Topic: event.Topic,
					Key:   []byte(event.AggregateID),
					Value: event.Payload,
				}
				s.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
					if err != nil {
						fmt.Printf("Failed to publish outbox event %d: %v\n", event.ID, err)
					} else {
						// Mark processed
						_ = s.repo.MarkOutboxEventProcessed(ctx, event.ID)
					}
				})
			}
		}
	}
}
