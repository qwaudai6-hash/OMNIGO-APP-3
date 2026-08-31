package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omnigo/backend/internal/order/models"
	"github.com/omnigo/backend/internal/order/repository"
	"github.com/omnigo/backend/internal/shared/security"
)

type CartService struct {
	repo              *repository.CartRepository
	productServiceURL string
	internalSigner    *security.InternalSigner
}

func NewCartService(repo *repository.CartRepository, productServiceURL string, internalSigner *security.InternalSigner) *CartService {
	return &CartService{
		repo:              repo,
		productServiceURL: productServiceURL,
		internalSigner:    internalSigner,
	}
}

// GetCart retrieves the user's cart
func (s *CartService) GetCart(ctx context.Context, userID string) (*models.Cart, error) {
	cart, err := s.repo.GetCart(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Return empty cart instead of error if not found
			return &models.Cart{UserID: userID, Items: []models.CartItem{}}, nil
		}
		return nil, err
	}
	if cart.Items == nil {
		cart.Items = []models.CartItem{}
	}
	return cart, nil
}

// fetchProductPrice calls the Product Service to get the real price
func (s *CartService) fetchProductPrice(ctx context.Context, productID int64) (float64, error) {
	if s.productServiceURL == "" {
		return 0, errors.New("product service URL not configured")
	}

	var url string
	var req *http.Request
	var err error

	if s.internalSigner != nil {
		// Use internal HMAC-authenticated route to bypass JWT
		url = fmt.Sprintf("%s/api/v1/internal/products/%d", s.productServiceURL, productID)
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return 0, err
		}
		s.internalSigner.SignRequest(req, nil)
	} else {
		url = fmt.Sprintf("%s/api/v1/products/%d", s.productServiceURL, productID)
		req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return 0, err
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to call product service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("product service returned status %d", resp.StatusCode)
	}

	var data struct {
		Price    float64 `json:"price"`
		BasePrice float64 `json:"base_price"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, fmt.Errorf("failed to decode product response: %w", err)
	}

	if data.BasePrice > 0 {
		return data.BasePrice, nil
	}
	return data.Price, nil
}

// AddItem adds an item to the cart. It enforces the single-store constraint.
func (s *CartService) AddItem(ctx context.Context, userID string, req models.AddToCartRequest) error {
	cart, err := s.repo.GetCart(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Create new cart
			cart, err = s.repo.CreateCart(ctx, userID, req.StoreID)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// Enforce single-store constraint
	if cart.StoreID != req.StoreID {
		return errors.New("cart contains items from a different store. Please clear cart first.")
	}

	// SP-GO-20: sanity-bound quantity at the cart layer. Stock is still
	// authoritatively checked at order creation (atomic reserve), but an
	// unbounded cart quantity enables absurd payloads and noisy downstream
	// failures. 0/negative and >999 are rejected outright.
	if req.Quantity <= 0 || req.Quantity > 999 {
		return fmt.Errorf("invalid quantity %d: must be between 1 and 999", req.Quantity)
	}

	// Fetch real price from Product Service
	realPrice, err := s.fetchProductPrice(ctx, req.ProductID)
	if err != nil {
		return fmt.Errorf("failed to verify product price: %w", err)
	}

	return s.repo.AddItem(ctx, cart.ID, req.ProductID, req.Quantity, realPrice)
}

// UpdateItemQuantity updates the quantity of a specific item
func (s *CartService) UpdateItemQuantity(ctx context.Context, userID string, productID int64, quantity int) error {
	// SP-GO-20: same bound as AddItem.
	if quantity <= 0 || quantity > 999 {
		return fmt.Errorf("invalid quantity %d: must be between 1 and 999", quantity)
	}
	cart, err := s.repo.GetCart(ctx, userID)
	if err != nil {
		return errors.New("cart not found")
	}

	return s.repo.UpdateItemQuantity(ctx, cart.ID, productID, quantity)
}

// RemoveItem removes a specific item from the cart
func (s *CartService) RemoveItem(ctx context.Context, userID string, productID int64) error {
	cart, err := s.repo.GetCart(ctx, userID)
	if err != nil {
		return errors.New("cart not found")
	}

	return s.repo.RemoveItem(ctx, cart.ID, productID)
}

// ClearCart clears the entire cart
func (s *CartService) ClearCart(ctx context.Context, userID string) error {
	return s.repo.ClearCart(ctx, userID)
}
