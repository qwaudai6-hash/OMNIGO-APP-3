package models

import "time"

// Product represents an item listed by a vendor store
type Product struct {
	ID                int       `json:"id"`
	ProductTrackingID string    `json:"product_tracking_id"`
	VendorTrackingID  string    `json:"vendor_tracking_id"`
	StoreTrackingID   string    `json:"store_tracking_id"`
	SKU               string    `json:"sku"`
	Name              string    `json:"name"`
	Description       string    `json:"description,omitempty"`
	BasePrice         float64   `json:"base_price"`
	Stock             int       `json:"stock"`
	IsFeatured        bool      `json:"is_featured"`
	ImageURL          string    `json:"image_url"`
	Category          string    `json:"category"`
	IsActive          bool      `json:"is_active"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// CreateProductRequest is the payload for creating a new product
type CreateProductRequest struct {
	VendorTrackingID string  `json:"vendor_tracking_id" binding:"required"`
	StoreTrackingID  string  `json:"store_tracking_id" binding:"required"`
	SKU              string  `json:"sku"`
	Name             string  `json:"name" binding:"required"`
	Description      string  `json:"description"`
	BasePrice        float64 `json:"base_price" binding:"required"`
	Stock            int     `json:"stock" binding:"required,min=0"`
	ImageURL         string  `json:"image_url"`
	Category         string  `json:"category"`
}

// OrderItem represents a line item for stock reservation
type OrderItem struct {
	ProductTrackingID string  `json:"product_tracking_id"`
	Quantity          int     `json:"quantity"`
	PriceAtCheckout   float64 `json:"price_at_checkout"`  // Returned by reservation
	StoreTrackingID   string  `json:"store_tracking_id"`  // Returned by reservation
	VendorTrackingID  string  `json:"vendor_tracking_id"` // Returned by reservation
}

// VendorCreateProductRequest is the vendor-authenticated variant of
// CreateProductRequest: vendor_tracking_id is pulled from the
// Authorization header (not the JSON body) so a vendor cannot spoof
// another merchant's catalog.
type VendorCreateProductRequest struct {
	StoreTrackingID string  `json:"store_tracking_id" binding:"required"`
	SKU             string  `json:"sku"`
	Name            string  `json:"name" binding:"required"`
	Description     string  `json:"description"`
	BasePrice       float64 `json:"base_price" binding:"required"`
	Stock           int     `json:"stock" binding:"required"`
	ImageURL        string  `json:"image_url"`
	Category        string  `json:"category"`
}

// UpdateProductRequest is the partial-update payload. All fields are
// pointers so callers can send only the subset they want changed.
type UpdateProductRequest struct {
	Name        *string  `json:"name"`
	Description *string  `json:"description"`
	BasePrice   *float64 `json:"base_price"`
	Stock       *int     `json:"stock"`
	ImageURL    *string  `json:"image_url"`
	Category    *string  `json:"category"`
	IsFeatured  *bool    `json:"is_featured"`
	IsActive    *bool    `json:"is_active"`
}
