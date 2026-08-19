package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// CartItem represents an item inside the Redis-managed cart
type CartItem struct {
	ProductID   string  `json:"product_id"`
	ProductName string  `json:"product_name"`
	Price       float64 `json:"price"`
	Quantity    int     `json:"quantity"`
}

type CartService struct {
	redis *redis.ClusterClient
}

func NewCartService(redisClient *redis.ClusterClient) *CartService {
	return &CartService{
		redis: redisClient,
	}
}

func getCartKey(customerID string) string {
	return fmt.Sprintf("cart:%s", customerID)
}

// AddToCart reads, updates, and writes the entire cart payload as a single JSON array key to avoid CPU unmarshalling loop bottlenecks.
func (s *CartService) AddToCart(ctx context.Context, customerID string, newItem CartItem) error {
	if s.redis == nil {
		return fmt.Errorf("redis cache client not initialized")
	}

	cartKey := getCartKey(customerID)
	
	// Fetch existing cart array
	rawCart, err := s.redis.Get(ctx, cartKey).Result()
	var cart []CartItem
	if err == nil && rawCart != "" {
		if err := json.Unmarshal([]byte(rawCart), &cart); err != nil {
			return fmt.Errorf("failed to unmarshal cart: %w", err)
		}
	} else if err != nil && err != redis.Nil {
		return fmt.Errorf("failed to fetch cart state from Redis: %w", err)
	}

	// Update quantity if item already exists
	found := false
	for i, item := range cart {
		if item.ProductID == newItem.ProductID {
			cart[i].Quantity += newItem.Quantity
			found = true
			break
		}
	}

	if !found {
		cart = append(cart, newItem)
	}

	// Marshal back to JSON array once
	cartBytes, err := json.Marshal(cart)
	if err != nil {
		return fmt.Errorf("failed to marshal optimized cart payload: %w", err)
	}

	// Set value with 7 days TTL
	err = s.redis.Set(ctx, cartKey, string(cartBytes), 7*24*time.Hour).Err()
	if err != nil {
		return fmt.Errorf("failed to write optimized cart state to Redis: %w", err)
	}

	return nil
}

// RemoveFromCart filters and updates the cart JSON array key in a single operation.
func (s *CartService) RemoveFromCart(ctx context.Context, customerID string, productID string) error {
	if s.redis == nil {
		return fmt.Errorf("redis cache client not initialized")
	}

	cartKey := getCartKey(customerID)
	rawCart, err := s.redis.Get(ctx, cartKey).Result()
	if err == redis.Nil {
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to fetch cart state from Redis: %w", err)
	}

	var cart []CartItem
	if err := json.Unmarshal([]byte(rawCart), &cart); err != nil {
		return fmt.Errorf("failed to unmarshal cart payload: %w", err)
	}

	// Filter out the item
	updatedCart := make([]CartItem, 0, len(cart))
	for _, item := range cart {
		if item.ProductID != productID {
			updatedCart = append(updatedCart, item)
		}
	}

	// Marshal and update
	cartBytes, err := json.Marshal(updatedCart)
	if err != nil {
		return fmt.Errorf("failed to marshal updated cart payload: %w", err)
	}

	err = s.redis.Set(ctx, cartKey, string(cartBytes), 7*24*time.Hour).Err()
	if err != nil {
		return fmt.Errorf("failed to update filtered cart state to Redis: %w", err)
	}

	return nil
}

// GetCart retrieves and unmarshals the entire cart array in a single operation to optimize CPU parser load.
func (s *CartService) GetCart(ctx context.Context, customerID string) ([]CartItem, error) {
	if s.redis == nil {
		return nil, fmt.Errorf("redis cache client not initialized")
	}

	cartKey := getCartKey(customerID)
	rawCart, err := s.redis.Get(ctx, cartKey).Result()
	if err == redis.Nil {
		return []CartItem{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to fetch cart state from Redis: %w", err)
	}

	var cart []CartItem
	if err := json.Unmarshal([]byte(rawCart), &cart); err != nil {
		return nil, fmt.Errorf("failed to parse cart JSON array: %w", err)
	}

	return cart, nil
}
