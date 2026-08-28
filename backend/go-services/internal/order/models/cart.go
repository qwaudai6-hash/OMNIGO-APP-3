package models

import "time"

type CartItem struct {
	ID        int64     `json:"id"`
	CartID    int64     `json:"cart_id"`
	ProductID int64     `json:"product_id"`
	Quantity  int       `json:"quantity"`
	Price     float64   `json:"price"` // Price at the time of adding to cart
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Cart struct {
	ID          int64      `json:"id"`
	UserID      string     `json:"user_id"`  // user_tracking_id
	StoreID     string     `json:"store_id"` // store_tracking_id (for single-store cart constraint)
	TotalAmount float64    `json:"total_amount"`
	Items       []CartItem `json:"items,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type AddToCartRequest struct {
	ProductID int64  `json:"product_id" binding:"required"`
	StoreID   string `json:"store_id" binding:"required"`
	Quantity  int    `json:"quantity" binding:"required,min=1"`
}

type UpdateCartItemRequest struct {
	Quantity int `json:"quantity" binding:"required,min=1"`
}
