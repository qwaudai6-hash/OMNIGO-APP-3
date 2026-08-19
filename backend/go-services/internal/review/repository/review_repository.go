package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/review/models"
	"github.com/omnigo/backend/internal/shared/database"
)

type ReviewRepository struct {
	writer *pgxpool.Pool
	reader *pgxpool.Pool
}

func NewReviewRepository(writer, reader *pgxpool.Pool) *ReviewRepository {
	return &ReviewRepository{writer: writer, reader: reader}
}

// CreateReview inserts a review. Enforces one-review-per-customer-per-product
// via the UNIQUE constraint in the DB. If a review already exists, it's
// updated (upsert pattern).
func (r *ReviewRepository) CreateReview(ctx context.Context, rev *models.Review) error {
	checks := []struct {
		id    string
		label string
		query string
	}{
		{rev.ProductTrackingID, "product", "SELECT 1 FROM products WHERE product_tracking_id = $1"},
		{rev.CustomerTrackingID, "user", "SELECT 1 FROM users WHERE tracking_id = $1"},
	}
	for _, c := range checks {
		ok, err := database.Exists(ctx, r.writer, c.query, c.id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s %s does not exist", c.label, c.id)
		}
	}

	// The reviews table uses `user_tracking_id` (per migration 0001_init.sql),
	// not `customer_tracking_id`. The Go code historically used the wrong
	// column name, so all review writes silently no-op'd.
	query := `
		INSERT INTO reviews (product_tracking_id, user_tracking_id, rating, comment)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (product_tracking_id, user_tracking_id)
		DO UPDATE SET rating = $3, comment = $4, updated_at = NOW()
		RETURNING id, created_at, updated_at
	`
	err := r.writer.QueryRow(ctx, query,
		rev.ProductTrackingID, rev.CustomerTrackingID, rev.Rating, rev.Comment,
	).Scan(&rev.ID, &rev.CreatedAt, &rev.UpdatedAt)
	return err
}

// ListReviewsByProduct returns all reviews for a product, newest first.
func (r *ReviewRepository) ListReviewsByProduct(ctx context.Context, productTrackingID string, limit int) ([]*models.Review, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	query := `
		SELECT id, product_tracking_id, user_tracking_id, rating, comment, created_at, updated_at
		FROM reviews
		WHERE product_tracking_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`
	rows, err := r.reader.Query(ctx, query, productTrackingID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var reviews []*models.Review
	for rows.Next() {
		var rev models.Review
		if err := rows.Scan(&rev.ID, &rev.ProductTrackingID, &rev.CustomerTrackingID, &rev.Rating, &rev.Comment, &rev.CreatedAt, &rev.UpdatedAt); err != nil {
			return nil, err
		}
		reviews = append(reviews, &rev)
	}
	return reviews, rows.Err()
}

// GetReviewSummary returns the average rating and total review count for a product.
func (r *ReviewRepository) GetReviewSummary(ctx context.Context, productTrackingID string) (*models.ReviewSummary, error) {
	query := `
		SELECT COALESCE(AVG(rating), 0.0), COALESCE(COUNT(id), 0)
		FROM reviews
		WHERE product_tracking_id = $1
	`
	var summary models.ReviewSummary
	summary.ProductTrackingID = productTrackingID
	var avg float64
	var count int64
	err := r.reader.QueryRow(ctx, query, productTrackingID).Scan(&avg, &count)
	if err != nil {
		return nil, err
	}
	summary.AverageRating = avg
	summary.TotalReviews = count
	if summary.AverageRating == 0.0 && summary.TotalReviews == 0 {
		return &summary, errors.New("no reviews found")
	}
	return &summary, nil
}
