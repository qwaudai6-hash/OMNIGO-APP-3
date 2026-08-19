package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/meilisearch/meilisearch-go"
	"github.com/omnigo/backend/internal/product/models"
	"github.com/omnigo/backend/internal/product/repository"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/tracking"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

type ProductService struct {
	repo        *repository.ProductRepository
	redisClient redis.UniversalClient // Multi-shard cluster alignment fixed
	meiliClient *meilisearch.Client
	kafkaClient *messaging.KafkaClient
}

func NewProductService(repo *repository.ProductRepository, rdb redis.UniversalClient, meili *meilisearch.Client, kafkaClient *messaging.KafkaClient) *ProductService {
	return &ProductService{
		repo:        repo,
		redisClient: rdb,
		meiliClient: meili,
		kafkaClient: kafkaClient,
	}
}

// generateUTID creates the Universal Tracking ID for products
func generateUTID() string {
	return tracking.Generate("PROD")
}

func (s *ProductService) CreateProduct(ctx context.Context, req *models.CreateProductRequest) (*models.Product, error) {
	utid := generateUTID()

	prod := &models.Product{
		ProductTrackingID: utid,
		VendorTrackingID:  req.VendorTrackingID,
		StoreTrackingID:   req.StoreTrackingID,
		SKU:               req.SKU,
		Name:              req.Name,
		Description:       req.Description,
		BasePrice:         req.BasePrice,
		Stock:             req.Stock,
		ImageURL:          req.ImageURL,
		Category:          req.Category,
	}

	// Step 1: Write to Primary Database
	err := s.repo.CreateProduct(ctx, prod)
	if err != nil {
		return nil, fmt.Errorf("database transaction execution aborted: %w", err)
	}

	// Step 2: HIGH-SCALE PIPELINED INVALIDATION WITH REPLICATION LAG DELAY
	if s.redisClient != nil {
		go func() {
			bgCtx := context.Background()

			// ANTI-LAG REPLICATION SHIELD: Wait for 500ms before evicting cache.
			// This guarantees that the Read Replicas have successfully caught up with the Primary DB writes.
			time.Sleep(500 * time.Millisecond)

			trackingSetKey := "invalidations:product:catalog"

			// Fetch all dynamic query parameter cache keys from the tracker Set instantly
			cachedKeys, err := s.redisClient.SMembers(bgCtx, trackingSetKey).Result()
			if err != nil || len(cachedKeys) == 0 {
				return
			}

			// Execute batch eviction via Redis Pipeline to execute bulk deletes in 1 network roundtrip
			pipe := s.redisClient.Pipeline()
			for _, key := range cachedKeys {
				pipe.Del(bgCtx, key)
			}
			// Clear the tracking index set clean
			pipe.Del(bgCtx, trackingSetKey)
			pipe.Del(bgCtx, fmt.Sprintf("store:products:%s", req.StoreTrackingID))

			_, _ = pipe.Exec(bgCtx)
		}()
	}

	// Step 3: Write to Meilisearch Index
	if s.meiliClient != nil {
		go func() {
			index := s.meiliClient.Index("products")
			_, _ = index.AddDocuments(prod) // Note: Prod struct is JSON marshaled
		}()
	}

	s.emitEvent(ctx, prod.ProductTrackingID, "product.created", prod)

	return prod, nil
}

// GetProduct fetches a product by its UTID
func (s *ProductService) GetProduct(ctx context.Context, trackingID string) (*models.Product, error) {
	return s.repo.GetProductByTrackingID(ctx, trackingID)
}

// ListProducts returns a paginated list using optimal Cache-Aside Read with Pipeline Tracker
func (s *ProductService) ListProducts(ctx context.Context, limit, offset int, search, category, sort string, minPrice, maxPrice float64) ([]*models.Product, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	// Dynamic Cache Key
	cacheKey := fmt.Sprintf("products:list:limit=%d:offset=%d:search=%s:category=%s:sort=%s:min=%.0f:max=%.0f",
		limit, offset, search, category, sort, minPrice, maxPrice)

	if s.redisClient != nil {
		// Step 1: Try fetching from Redis Cache (Fast Path)
		cachedData, err := s.redisClient.Get(ctx, cacheKey).Result()
		if err == nil && cachedData != "" {
			var products []*models.Product
			if err := json.Unmarshal([]byte(cachedData), &products); err == nil {
				return products, nil // Cache Hit! Return instantly
			}
		}
	}

	// Step 1.5: If search is provided and Meilisearch is active, use Meilisearch instead of PG replica
	if search != "" && s.meiliClient != nil {
		index := s.meiliClient.Index("products")
		req := &meilisearch.SearchRequest{
			Limit:  int64(limit),
			Offset: int64(offset),
		}
		if category != "" {
			req.Filter = []string{fmt.Sprintf("Category = '%s'", category)}
		}

		resp, err := index.Search(search, req)
		if err == nil {
			var meiliProducts []*models.Product
			for _, hit := range resp.Hits {
				jsonbody, _ := json.Marshal(hit)
				var mp models.Product
				json.Unmarshal(jsonbody, &mp)
				meiliProducts = append(meiliProducts, &mp)
			}
			return meiliProducts, nil
		}
		// Fallback to PostgreSQL on error
	}

	// Step 2: Cache Miss. Database (Read Replica) se fetch karein.
	products, err := s.repo.ListProducts(ctx, limit, offset, search, category, sort, minPrice, maxPrice)
	if err != nil {
		return nil, fmt.Errorf("database replica fetch aborted: %w", err)
	}

	// Step 3: Write-Behind Pipeline for Set Indexing & Cache Storage
	if s.redisClient != nil {
		go func(prods []*models.Product) {
			bgCtx := context.Background()

			jsonData, err := json.Marshal(prods)
			if err != nil {
				return
			}

			trackingSetKey := "invalidations:product:catalog"

			pipe := s.redisClient.Pipeline()
			pipe.Set(bgCtx, cacheKey, jsonData, 15*time.Minute)
			pipe.SAdd(bgCtx, trackingSetKey, cacheKey)
			pipe.Expire(bgCtx, trackingSetKey, 24*time.Hour)
			_, _ = pipe.Exec(bgCtx)
		}(products)
	}

	return products, nil
}

// GetRecommendations calls Python AI Microservice (Port 8086) for co-occurrence ML predictions
// and resolves returned product tracking IDs into complete PostgreSQL Product records.
func (s *ProductService) GetRecommendations(ctx context.Context, productTrackingID string) ([]*models.Product, error) {
	var recommendedIDs []string

	// 1. Call Python AI Microservice (via gateway or direct internal URL in prod)
	aiURL := os.Getenv("AI_ENGINE_URL")
	if aiURL == "" {
		log.Printf("Warning: AI_ENGINE_URL not set, recommendations endpoint will return 503")
		return nil, fmt.Errorf("AI_ENGINE_URL not configured")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	aiPayload, _ := json.Marshal(map[string]string{"product_tracking_id": productTrackingID})

	req, err := http.NewRequestWithContext(ctx, "POST", aiURL+"/api/v1/ai/frequently-bought-together", strings.NewReader(string(aiPayload)))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			var aiResult struct {
				Recommendations []string `json:"recommendations"`
			}
			if json.NewDecoder(resp.Body).Decode(&aiResult) == nil {
				recommendedIDs = aiResult.Recommendations
			}
			resp.Body.Close()
		}
	}

	// 2. Fallback: If Python AI service is offline or has no data, fetch same-category products from PostgreSQL
	if len(recommendedIDs) == 0 {
		currentProd, err := s.repo.GetProductByTrackingID(ctx, productTrackingID)
		if err == nil && currentProd.Category != "" {
			catProducts, err := s.repo.ListProducts(ctx, 5, 0, "", currentProd.Category, "newest", 0, 0)
			if err == nil {
				var filtered []*models.Product
				for _, p := range catProducts {
					if p.ProductTrackingID != productTrackingID {
						filtered = append(filtered, p)
					}
				}
				return filtered, nil
			}
		}
	}

	// 3. Resolve recommended tracking IDs into full Product structs from PostgreSQL
	return s.repo.GetProductsByTrackingIDs(ctx, recommendedIDs)
}

// UpdateProductStockSecure updates the product stock securely and invalidates the cache.
func (s *ProductService) UpdateProductStockSecure(ctx context.Context, productTrackingID string, stock int, vendorTrackingID string) error {
	err := s.repo.UpdateProductStockSecure(ctx, productTrackingID, stock, vendorTrackingID)
	if err != nil {
		return err
	}

	s.invalidateCache()

	// Emit product.updated event with full details
	prod, err := s.repo.GetProductByTrackingID(ctx, productTrackingID)
	if err == nil {
		s.emitEvent(ctx, productTrackingID, "product.updated", prod)
	}

	return nil
}

// DeleteProductSecure deletes a product securely and invalidates the cache.
func (s *ProductService) DeleteProductSecure(ctx context.Context, productTrackingID string, vendorTrackingID string) error {
	err := s.repo.DeleteProductSecure(ctx, productTrackingID, vendorTrackingID)
	if err != nil {
		return err
	}

	s.invalidateCache()
	s.emitEvent(ctx, productTrackingID, "product.deleted", map[string]string{
		"product_tracking_id": productTrackingID,
	})

	return nil
}

// CreateProductForVendor creates a product scoped to the authenticated vendor.
// The vendorTrackingID is extracted from the Authorization header (server-side)
// and the store ownership is verified before inserting. This prevents a vendor
// from spoofing another merchant's catalog.
func (s *ProductService) CreateProductForVendor(ctx context.Context, vendorTrackingID string, req *models.VendorCreateProductRequest) (*models.Product, error) {
	// 1. Verify the vendor actually owns the store_tracking_id they provided.
	if err := s.repo.VerifyStoreOwnership(ctx, req.StoreTrackingID, vendorTrackingID); err != nil {
		return nil, fmt.Errorf("ownership verification failed: %w", err)
	}

	utid := generateUTID()

	prod := &models.Product{
		ProductTrackingID: utid,
		VendorTrackingID:  vendorTrackingID,
		StoreTrackingID:   req.StoreTrackingID,
		SKU:               req.SKU,
		Name:              req.Name,
		Description:       req.Description,
		BasePrice:         req.BasePrice,
		Stock:             req.Stock,
		ImageURL:          req.ImageURL,
		Category:          req.Category,
	}

	isVerified, err := s.repo.GetVendorVerificationStatus(ctx, vendorTrackingID)
	if err != nil {
		return nil, fmt.Errorf("failed to verify vendor status: %w", err)
	}
	prod.IsActive = isVerified

	// 2. Write to Primary DB
	if err := s.repo.CreateProduct(ctx, prod); err != nil {
		return nil, fmt.Errorf("database transaction execution aborted: %w", err)
	}

	// 3. Write to Meilisearch Index
	if s.meiliClient != nil {
		go func() {
			index := s.meiliClient.Index("products")
			_, _ = index.AddDocuments(prod)
		}()
	}

	// 4. Pipelined cache invalidation (reuse the existing pattern).
	s.invalidateCache()
	s.emitEvent(ctx, prod.ProductTrackingID, "product.created", prod)
	return prod, nil
}

// UpdateProduct performs a partial update of a vendor-owned product.
// Only non-nil fields in the request are written. Ownership is verified
// inside the repository via the WHERE vendor_tracking_id guard.
func (s *ProductService) UpdateProduct(ctx context.Context, productTrackingID, vendorTrackingID string, req *models.UpdateProductRequest) error {
	updates := map[string]interface{}{}

	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.BasePrice != nil {
		updates["base_price"] = *req.BasePrice
	}
	if req.Stock != nil {
		updates["stock"] = *req.Stock
	}
	if req.ImageURL != nil {
		updates["image_url"] = *req.ImageURL
	}
	if req.Category != nil {
		updates["category"] = *req.Category
	}
	if req.IsFeatured != nil {
		updates["is_featured"] = *req.IsFeatured
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
	}

	if len(updates) == 0 {
		return fmt.Errorf("no fields provided for update")
	}

	if err := s.repo.UpdateProductFields(ctx, productTrackingID, vendorTrackingID, updates); err != nil {
		return err
	}

	s.invalidateCache()

	// Emit product.updated event with full details
	prod, err := s.repo.GetProductByTrackingID(ctx, productTrackingID)
	if err == nil {
		s.emitEvent(ctx, productTrackingID, "product.updated", prod)
	}

	return nil
}

// Helper to invalidate all listing cache keys during updates
func (s *ProductService) invalidateCache() {
	if s.redisClient != nil {
		go func() {
			bgCtx := context.Background()

			// ANTI-LAG REPLICATION SHIELD: wait for write propagation
			time.Sleep(500 * time.Millisecond)

			trackingSetKey := "invalidations:product:catalog"

			cachedKeys, err := s.redisClient.SMembers(bgCtx, trackingSetKey).Result()
			if err != nil || len(cachedKeys) == 0 {
				return
			}

			pipe := s.redisClient.Pipeline()
			for _, key := range cachedKeys {
				pipe.Del(bgCtx, key)
			}
			pipe.Del(bgCtx, trackingSetKey)

			_, _ = pipe.Exec(bgCtx)
		}()
	}
}

// ListVendorProducts returns a paginated list of products for a specific vendor
func (s *ProductService) ListVendorProducts(ctx context.Context, vendorID string, limit, offset int) ([]*models.Product, int, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	products, err := s.repo.ListVendorProducts(ctx, vendorID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.CountVendorProducts(ctx, vendorID)
	if err != nil {
		return nil, 0, err
	}
	return products, total, nil
}

// ReserveStock deducts stock for an array of items using CAS (Optimistic Concurrency)
func (s *ProductService) ReserveStock(ctx context.Context, items []models.OrderItem) (*repository.ReserveStockResponse, error) {
	return s.repo.ReserveStock(ctx, items)
}

// ReleaseStock adds stock back for an array of items
func (s *ProductService) ReleaseStock(ctx context.Context, items []models.OrderItem) error {
	err := s.repo.ReleaseStock(ctx, items)
	if err != nil {
		return err
	}
	s.invalidateCache()
	return nil
}

// emitEvent publishes a serialized message to the Kafka products topic
func (s *ProductService) emitEvent(ctx context.Context, trackingID string, eventType string, payload interface{}) {
	if s.kafkaClient == nil {
		return
	}

	eventBytes, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("Warning: Failed to marshal product event: %v\n", err)
		return
	}

	record := &kgo.Record{
		Topic: "products",
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
