package models

import "time"

// Order represents an e-commerce order
type Order struct {
	ID                    int64       `json:"id"`
	TrackingID            string      `json:"order_tracking_id"`
	UserTrackID           string      `json:"customer_tracking_id"`
	VendorStoreTrackID    string      `json:"store_tracking_id"`
	VendorTrackID         string      `json:"vendor_tracking_id"`
	RiderTrackID          string      `json:"rider_tracking_id"`
	Items                 []OrderItem `json:"items"`
	Status                string      `json:"status"`
	DeliveryType          string      `json:"delivery_type"`
	PaymentGateway        string      `json:"payment_gateway"`
	TotalAmount           float64     `json:"total_amount"`
	AdminCommission       float64     `json:"admin_commission"`
	VendorEscrow          float64     `json:"vendor_escrow"`
	DeliveryEscrow        float64     `json:"delivery_escrow"`
	Currency              string      `json:"currency"`
	PaymentStatus         string      `json:"payment_status"`
	CustomerLat           float64     `json:"customer_lat"`
	CustomerLng           float64     `json:"customer_lng"`
	OTPCode               string      `json:"otp_code"`
	DeviceSessionNonce    string      `json:"device_session_nonce"`
	EscrowReleased        bool        `json:"escrow_released"`
	DisputeStatus         string      `json:"dispute_status"`
	DeliveredAt           *time.Time  `json:"delivered_at,omitempty"`
	HandoverPhotoURL      string      `json:"handover_photo_url,omitempty"`
	HandoverAt            *time.Time  `json:"handover_at,omitempty"`
	HandoverNotes         string      `json:"handover_notes,omitempty"`
	HandedByTrackingID    string      `json:"handed_over_by_tracking_id,omitempty"`
	CreatedAt             time.Time   `json:"created_at"`
	UpdatedAt             time.Time   `json:"updated_at"`
	CustomerName          string      `json:"customer_name,omitempty"`
	CustomerPhone         string      `json:"customer_phone,omitempty"`
	RiderName             string      `json:"rider_name,omitempty"`
	RiderPhone            string      `json:"rider_phone,omitempty"`
}

// CreateOrderRequest is the payload for creating a new order
type CreateOrderRequest struct {
	UserTrackID        string               `json:"user_tracking_id"`
	VendorStoreTrackID string               `json:"vendor_store_tracking_id" binding:"required"`
	Items              []CreateOrderItemReq `json:"items" binding:"required,dive"`
	TotalAmount        float64              `json:"total_amount" binding:"required"`
	Currency           string               `json:"currency" binding:"required"`
	PaymentGateway     string               `json:"payment_gateway"`
	DeviceSessionNonce string               `json:"device_session_nonce" binding:"required"`
	DropoffLat         float64              `json:"dropoff_lat" binding:"required"`
	DropoffLng         float64              `json:"dropoff_lng" binding:"required"`
}

// OrderEvent represents the payload sent to Kafka when an order is created
type OrderEvent struct {
	OrderID            string      `json:"order_id"`
	UserTrackID        string      `json:"user_tracking_id"`
	VendorStoreTrackID string      `json:"vendor_store_tracking_id"`
	Items              []OrderItem `json:"items"`
	TotalAmount        float64     `json:"total_amount"`
	IsCOD              bool        `json:"is_cod"`
	CustomerPhone      string      `json:"customer_phone"`
	Tips               float64     `json:"tips"`
	PetrolAllowance    float64     `json:"petrol_allowance"`
	DropoffLat         float64     `json:"dropoff_lat"`
	DropoffLng         float64     `json:"dropoff_lng"`
	Timestamp          int64       `json:"timestamp"`
}

// OrderItem captures the frozen snapshot of a product at checkout
type OrderItem struct {
	ProductTrackingID string  `json:"product_tracking_id"`
	Quantity          int     `json:"quantity"`
	PriceAtCheckout   float64 `json:"price_at_checkout"`
}

// CreateOrderItemReq is the payload item for creating an order
type CreateOrderItemReq struct {
	ProductTrackingID string `json:"product_tracking_id" binding:"required"`
	Quantity          int    `json:"quantity" binding:"required,min=1"`
}

// OutboxEvent represents an event to be published asynchronously
type OutboxEvent struct {
	ID           int64      `json:"id"`
	AggregateID  string     `json:"aggregate_id"`
	Topic        string     `json:"topic"`
	Payload      []byte     `json:"payload"`
	Status       string     `json:"status"`
	RetryCount   int        `json:"retry_count"`
	ErrorMessage string     `json:"error_message"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ProcessedAt  *time.Time `json:"processed_at,omitempty"`
}
