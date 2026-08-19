package models

import "time"

// Favorite represents a customer's wishlisted product.
type Favorite struct {
	ID                 int64     `json:"id"`
	CustomerTrackingID string    `json:"customer_tracking_id"`
	ProductTrackingID  string    `json:"product_tracking_id"`
	CreatedAt          time.Time `json:"created_at"`
}
