package models

import "time"

// Review represents a customer's rating + comment on a product.
// NOTE: DB column is `user_tracking_id`, not `customer_tracking_id`.
// The repository maps user_tracking_id → CustomerTrackingID.
type Review struct {
	ID                 int64     `json:"id"`
	ProductTrackingID  string    `json:"product_tracking_id"`
	CustomerTrackingID string    `json:"customer_tracking_id"`
	Rating             int       `json:"rating"`
	Comment            string    `json:"comment"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// CreateReviewRequest is the payload for submitting a review.
type CreateReviewRequest struct {
	ProductTrackingID  string `json:"product_tracking_id" binding:"required"`
	CustomerTrackingID string `json:"customer_tracking_id" binding:"required"`
	Rating             int    `json:"rating" binding:"required,min=1,max=5"`
	Comment            string `json:"comment"`
}

// ReviewSummary holds the aggregate rating stats for a product.
type ReviewSummary struct {
	ProductTrackingID string  `json:"product_tracking_id"`
	AverageRating     float64 `json:"average_rating"`
	TotalReviews      int64   `json:"total_reviews"`
}
