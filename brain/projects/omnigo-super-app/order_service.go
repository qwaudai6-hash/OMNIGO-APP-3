package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnigo/backend/internal/order/models"
	"github.com/omnigo/backend/internal/order/repository"
	"github.com/omnigo/backend/internal/shared/cache"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Custom domain errors
var (
	ErrDuplicateRequest = errors.New("CONFLICT_DUPLICATE_TRANSACTION: request already processed or in progress")
	ErrDatabaseFailure  = errors.New("INTERNAL_DB_ERROR: database transaction failed")
)

type OrderService struct {
	repo  *repository.OrderRepository
	kafka *messaging.KafkaClient
	redis *cache.RedisClient
}

func NewOrderService(repo *repository.OrderRepository, kafka *messaging.KafkaClient, redis *cache.RedisClient) *OrderService {
	return &OrderService{
		repo:  repo,
		kafka: kafka,
		redis: redis,
	}
}

func generateUTID() string {
	id := uuid.New().String()
	parts := strings.Split(id, "-")
	return fmt.Sprintf("ORD-%s%s", parts[0], parts[1])
}

// CreateOrder handles order checking with Fail-Open Idempotency guard
func (s *OrderService) CreateOrder(ctx context.Context, req *models.CreateOrderRequest) (*models.Order, error) {
	
	// Idempotency check with FAIL-OPEN pattern (If Redis fails, log it and proceed to Primary DB)
	if s.redis != nil && req.DeviceSessionNonce != "" {
		idempotencyKey := fmt.Sprintf("idempotency:%s:%s:%s", req.UserTrackID, req.VendorStoreTrackID, req.DeviceSessionNonce)
		success, err := s.redis.Client.SetNX(ctx, idempotencyKey, "1", 120*time.Second).Result()
		if err != nil {
			// Fail-Open: Log error but do not block checkout transaction
			fmt.Printf("System Log (Fail-Open): Idempotency check Redis error: %v. Continuing transaction.\n", err)
		} else if !success {
			// Lock exists, request is duplicate
			return nil, ErrDuplicateRequest
		}
	}

	utid := generateUTID()

	order := &models.Order{
		TrackingID:         utid,
		UserTrackID:        req.UserTrackID,
		VendorStoreTrackID: req.VendorStoreTrackID,
		TotalAmount:        req.TotalAmount,
		Currency:           req.Currency,
		Status:             "pending",
	}

	// Save to DB using the request-scoped context
	err := s.repo.CreateOrder(ctx, order)
	if err != nil {
		return nil, ErrDatabaseFailure
	}

	// Publish Event to Kafka
	event := models.OrderEvent{
		OrderID:            order.TrackingID,
		UserTrackID:        order.UserTrackID,
		VendorStoreTrackID: order.VendorStoreTrackID,
		TotalAmount:        order.TotalAmount,
		Timestamp:          time.Now().UnixMilli(),
	}

	eventBytes, _ := json.Marshal(event)

	if s.kafka != nil {
		record := &kgo.Record{
			Topic: "orders.created",
			Key:   []byte(order.TrackingID),
			Value: eventBytes,
		}

		s.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
			if err != nil {
				fmt.Printf("Warning: Failed to produce orders.created event: %v\n", err)
			}
		})
	}

	return order, nil
}

func (s *OrderService) GetOrder(ctx context.Context, trackingID string) (*models.Order, error) {
	return s.repo.GetOrderByTrackingID(ctx, trackingID)
}
