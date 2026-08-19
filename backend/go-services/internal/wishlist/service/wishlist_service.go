package service

import (
	"context"
	"fmt"

	"github.com/omnigo/backend/internal/wishlist/repository"
)

type WishlistService struct {
	repo *repository.WishlistRepository
}

func NewWishlistService(repo *repository.WishlistRepository) *WishlistService {
	return &WishlistService{repo: repo}
}

// ToggleFavorite adds or removes a product from the customer's wishlist.
// Returns true if now favorited, false if removed.
func (s *WishlistService) ToggleFavorite(ctx context.Context, customerTrackingID, productTrackingID string) (bool, error) {
	if customerTrackingID == "" || productTrackingID == "" {
		return false, fmt.Errorf("customer_tracking_id and product_tracking_id are required")
	}
	return s.repo.ToggleFavorite(ctx, customerTrackingID, productTrackingID)
}

// ListFavorites returns the product tracking IDs favorited by a customer.
func (s *WishlistService) ListFavorites(ctx context.Context, customerTrackingID string) ([]string, error) {
	if customerTrackingID == "" {
		return nil, fmt.Errorf("customer_tracking_id is required")
	}
	return s.repo.ListFavorites(ctx, customerTrackingID)
}

// IsFavorite checks if a product is favorited by a customer.
func (s *WishlistService) IsFavorite(ctx context.Context, customerTrackingID, productTrackingID string) (bool, error) {
	return s.repo.IsFavorite(ctx, customerTrackingID, productTrackingID)
}

// RemoveFavorite deletes a favorite.
func (s *WishlistService) RemoveFavorite(ctx context.Context, customerTrackingID, productTrackingID string) error {
	return s.repo.RemoveFavorite(ctx, customerTrackingID, productTrackingID)
}
