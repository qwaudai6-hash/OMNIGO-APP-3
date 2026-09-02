package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/tracking"
	"github.com/omnigo/backend/internal/vendorstore/models"
	"github.com/omnigo/backend/internal/vendorstore/repository"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

type VendorService struct {
	repo        *repository.VendorRepository
	rdb         redis.UniversalClient
	kafkaClient *messaging.KafkaClient
}

func NewVendorService(repo *repository.VendorRepository, rdb redis.UniversalClient, kafkaClient *messaging.KafkaClient) *VendorService {
	return &VendorService{repo: repo, rdb: rdb, kafkaClient: kafkaClient}
}

// generateUTID creates the Universal Tracking ID for stores
func generateUTID() string {
	return tracking.Generate("STOR")
}

// CreateStore handles the business logic for creating a vendor store mapping
func (s *VendorService) CreateStore(ctx context.Context, req *models.CreateStoreRequest) (*models.VendorStore, error) {
	utid := generateUTID()

	store := &models.VendorStore{
		VendorTrackingID: req.VendorTrackingID,
		StoreTrackingID:  utid,
		StoreName:        req.StoreName,
		LogoURL:          req.LogoURL,
		BannerURL:        req.BannerURL,
		Latitude:         req.Latitude,
		Longitude:        req.Longitude,
	}

	err := s.repo.CreateStore(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("failed to create store: %w", err)
	}

	s.emitEvent(ctx, store.StoreTrackingID, "store.created", store)

	return store, nil
}

// emitEvent publishes a serialized message to the Kafka stores topic
func (s *VendorService) emitEvent(ctx context.Context, trackingID string, eventType string, payload interface{}) {
	if s.kafkaClient == nil {
		return
	}

	eventBytes, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("Warning: Failed to marshal store event: %v\n", err)
		return
	}

	record := &kgo.Record{
		Topic: "stores",
		Key:   []byte(trackingID),
		Headers: []kgo.RecordHeader{
			{Key: "event_type", Value: []byte(eventType)},
		},
		Value: eventBytes,
	}

	s.kafkaClient.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
		if err != nil {
			fmt.Printf("Warning: Failed to produce %s event to Kafka: %v\n", eventType, err)
		}
	})
}

// GetStore fetches a store by its tracking ID
func (s *VendorService) GetStore(ctx context.Context, trackingID string) (*models.VendorStore, error) {
	return s.repo.GetStoreByTrackingID(ctx, trackingID)
}

// GetStoreByVendorID fetches the store belonging to a vendor.
func (s *VendorService) GetStoreByVendorID(ctx context.Context, vendorTrackingID string) (*models.VendorStore, error) {
	return s.repo.GetStoreByVendorID(ctx, vendorTrackingID)
}

// ListStores returns a paginated list of stores for public browsing.
func (s *VendorService) ListStores(ctx context.Context, limit, offset int) ([]models.VendorStore, error) {
	return s.repo.ListStores(ctx, limit, offset)
}

// UpdateMyStore updates the vendor's own store.
func (s *VendorService) UpdateMyStore(ctx context.Context, vendorTrackingID string, req *models.UpdateStoreRequest) (*models.VendorStore, error) {
	return s.repo.UpdateStore(ctx, vendorTrackingID, req)
}

// GetVendorMetrics pulls caching-first analytics and handles division-by-zero math guards
func (s *VendorService) GetVendorMetrics(ctx context.Context, vendorTrackingID string) (*models.VendorMetricsResponse, error) {
	cacheKey := fmt.Sprintf("vendor:metrics:%s", vendorTrackingID)

	// 1. Check Redis cache first
	if s.rdb != nil {
		val, err := s.rdb.Get(ctx, cacheKey).Result()
		if err == nil {
			var cachedResp models.VendorMetricsResponse
			if err := json.Unmarshal([]byte(val), &cachedResp); err == nil {
				return &cachedResp, nil
			}
		}
	}

	// 2. Fetch from Postgres pooling on cache miss
	totalRevenue, completed, pending, cancelled, currentWeekRev, prevWeekRev, err := s.repo.GetVendorMetricsSecure(ctx, vendorTrackingID)
	if err != nil {
		return nil, fmt.Errorf("failed to load sales metrics: %w", err)
	}

	trends, err := s.repo.GetVendorDailyTrends(ctx, vendorTrackingID)
	if err != nil {
		return nil, fmt.Errorf("failed to load daily trends: %w", err)
	}

	totalProducts, activeProducts, err := s.repo.GetVendorProductStats(ctx, vendorTrackingID)
	if err != nil {
		return nil, fmt.Errorf("failed to load product catalog metrics: %w", err)
	}

	// 3. Mathematical Division-by-Zero Guard
	var wowGrowth float64
	if prevWeekRev == 0.00 {
		if currentWeekRev == 0.00 {
			wowGrowth = 0.00
		} else {
			wowGrowth = 100.00 // Standard growth indicator if starting from 0 revenue
		}
	} else {
		wowGrowth = ((currentWeekRev - prevWeekRev) / prevWeekRev) * 100.00
	}

	resp := &models.VendorMetricsResponse{
		TotalRevenue:        totalRevenue,
		CompletedOrders:     completed,
		PendingOrders:       pending,
		CancelledOrders:     cancelled,
		TotalProducts:       totalProducts,
		ActiveProducts:      activeProducts,
		CurrentWeekRevenue:  currentWeekRev,
		PreviousWeekRevenue: prevWeekRev,
		WowGrowthPercentage: wowGrowth,
		DailyTrends:         trends,
	}

	// 4. Save to Redis Cache (5-Minute TTL)
	if s.rdb != nil {
		jsonData, err := json.Marshal(resp)
		if err == nil {
			_ = s.rdb.Set(ctx, cacheKey, jsonData, 5*time.Minute).Err()
		}
	}

	return resp, nil
}
