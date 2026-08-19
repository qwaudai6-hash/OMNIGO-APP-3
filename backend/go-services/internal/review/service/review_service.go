package service

import (
	"context"
	"fmt"

	"github.com/omnigo/backend/internal/review/models"
	"github.com/omnigo/backend/internal/review/repository"
)

type ReviewService struct {
	repo *repository.ReviewRepository
}

func NewReviewService(repo *repository.ReviewRepository) *ReviewService {
	return &ReviewService{repo: repo}
}

// CreateReview creates or updates a review (upsert — one per customer per product).
func (s *ReviewService) CreateReview(ctx context.Context, req *models.CreateReviewRequest) (*models.Review, error) {
	if req.Rating < 1 || req.Rating > 5 {
		return nil, fmt.Errorf("rating must be between 1 and 5")
	}
	rev := &models.Review{
		ProductTrackingID:  req.ProductTrackingID,
		CustomerTrackingID: req.CustomerTrackingID,
		Rating:             req.Rating,
		Comment:            req.Comment,
	}
	if err := s.repo.CreateReview(ctx, rev); err != nil {
		return nil, fmt.Errorf("failed to save review: %w", err)
	}
	return rev, nil
}

// ListReviewsByProduct returns reviews for a product.
func (s *ReviewService) ListReviewsByProduct(ctx context.Context, productTrackingID string, limit int) ([]*models.Review, error) {
	return s.repo.ListReviewsByProduct(ctx, productTrackingID, limit)
}

// GetReviewSummary returns average rating + count for a product.
func (s *ReviewService) GetReviewSummary(ctx context.Context, productTrackingID string) (*models.ReviewSummary, error) {
	return s.repo.GetReviewSummary(ctx, productTrackingID)
}
