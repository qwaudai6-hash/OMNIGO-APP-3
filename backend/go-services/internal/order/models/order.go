package models

import "time"

// Order represents an e-commerce order.
// Money fields are int64 paisa (1 PKR = 100 paisa). Float64 *_rupees fields
// are computed at JSON serialization time for display only. Never use *_rupees
// for calculations — use *_paisa for precision. (H6 migration 0032)
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
	TotalAmountPaisa      int64       `json:"total_amount_paisa"`
	TotalAmountRupees     float64     `json:"total_amount_rupees"`
	AdminCommissionPaisa  int64       `json:"admin_commission_paisa"`
	AdminCommissionRupees float64     `json:"admin_commission_rupees"`
	VendorEscrowPaisa     int64       `json:"vendor_escrow_paisa"`
	VendorEscrowRupees    float64     `json:"vendor_escrow_rupees"`
	DeliveryEscrowPaisa   int64       `json:"delivery_escrow_paisa"`
	DeliveryEscrowRupees  float64     `json:"delivery_escrow_rupees"`
	// H4: Uber-style explicit billing columns (customer pays everything)
	BaseProductAmountPaisa int64  `json:"base_product_amount_paisa"`
	DeliveryFeeAmountPaisa int64  `json:"delivery_fee_amount_paisa"`
	TotalBilledAmountPaisa int64  `json:"total_billed_amount_paisa"`
	RoutingStatus          string `json:"routing_status"` // DYNAMIC_CALCULATED | FALLBACK_HAVERSINE | FAILED_CALCULATION
	// Legacy fields (kept for backward compat — prefer *_paisa versions)
	TotalAmount         float64    `json:"total_amount,omitempty"`
	AdminCommission     float64    `json:"admin_commission,omitempty"`
	VendorEscrow        float64    `json:"vendor_escrow,omitempty"`
	DeliveryEscrow      float64    `json:"delivery_escrow,omitempty"`
	Currency            string     `json:"currency"`
	PaymentStatus       string     `json:"payment_status"`
	CustomerLat         float64    `json:"customer_lat"`
	CustomerLng         float64    `json:"customer_lng"`
	OTPCode             string     `json:"otp_code"`
	DeviceSessionNonce  string     `json:"device_session_nonce"`
	EscrowReleased      bool       `json:"escrow_released"`
	DisputeStatus       string     `json:"dispute_status"`
	DeliveredAt         *time.Time `json:"delivered_at,omitempty"`
	HandoverPhotoURL    string     `json:"handover_photo_url,omitempty"`
	HandoverAt          *time.Time `json:"handover_at,omitempty"`
	HandoverNotes       string     `json:"handover_notes,omitempty"`
	HandedByTrackingID  string     `json:"handed_over_by_tracking_id,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	CustomerName        string     `json:"customer_name,omitempty"`
	CustomerPhone       string     `json:"customer_phone,omitempty"`
	RiderName           string     `json:"rider_name,omitempty"`
	RiderPhone          string     `json:"rider_phone,omitempty"`
}

// CreateOrderRequest is the payload for creating a new order.
// TotalAmount is accepted in rupees (float64) for backward compat with
// existing frontend code, but is converted to paisa internally.
// DeliveryFeePaisa is the Uber-style delivery fee in paisa charged to customer.
type CreateOrderRequest struct {
	UserTrackID        string               `json:"user_tracking_id"`
	VendorStoreTrackID string               `json:"store_tracking_id" binding:"required"`
	Items              []CreateOrderItemReq `json:"items" binding:"required,dive"`
	TotalAmount        float64              `json:"total_amount" binding:"required"` // rupees; converted to paisa internally
	DeliveryFeePaisa   int64                `json:"delivery_fee_paisa"`             // H4: Uber-style delivery fee in paisa (customer pays)
	RoutingStatus      string               `json:"routing_status"`                 // H3: DYNAMIC_CALCULATED | FALLBACK_HAVERSINE | FAILED_CALCULATION
	Currency           string               `json:"currency" binding:"required"`
	PaymentGateway     string               `json:"payment_gateway"`
	PaymentMethod      string               `json:"payment_method"` // alias: frontend sends payment_method
	DeviceSessionNonce string               `json:"device_session_nonce" binding:"required"`
	DropoffLat         float64              `json:"dropoff_lat" binding:"required"`
	DropoffLng         float64              `json:"dropoff_lng" binding:"required"`
}

// OrderEvent represents the payload sent to Kafka when an order is created.
// Money fields are paisa (int64).
type OrderEvent struct {
	OrderID            string      `json:"order_id"`
	UserTrackID        string      `json:"user_tracking_id"`
	VendorStoreTrackID string      `json:"store_tracking_id"`
	Items              []OrderItem `json:"items"`
	ItemsSummary       string      `json:"items_summary"` // H4: human-readable item summary for rider
	TotalAmountPaisa   int64       `json:"total_amount_paisa"`
	TotalAmountRupees  float64     `json:"total_amount_rupees"`
	IsCOD              bool        `json:"is_cod"`
	CustomerPhone      string      `json:"customer_phone"`
	CustomerName       string      `json:"customer_name"`  // H4: customer name for rider
	CustomerAddress    string      `json:"customer_address"` // H4: customer address for rider
	Tips               float64     `json:"tips"`
	PetrolAllowance    float64     `json:"petrol_allowance"`
	DropoffLat         float64     `json:"dropoff_lat"`
	DropoffLng         float64     `json:"dropoff_lng"`
	Timestamp          int64       `json:"timestamp"`
}

// OrderItem captures the frozen snapshot of a product at checkout.
// PriceAtCheckoutPaisa is source of truth. PriceAtCheckoutRupees for display.
type OrderItem struct {
	ProductTrackingID     string  `json:"product_tracking_id"`
	ProductName          string  `json:"product_name"` // H4: product name for rider display
	Quantity             int     `json:"quantity"`
	PriceAtCheckoutPaisa int64   `json:"price_at_checkout_paisa"`
	PriceAtCheckoutRupees float64 `json:"price_at_checkout_rupees"`
	// Legacy
	PriceAtCheckout float64 `json:"price_at_checkout,omitempty"`
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

// StockReservationStatus represents the state of a local stock reservation.
type StockReservationStatus string

const (
	StockReservationPending   StockReservationStatus = "pending"
	StockReservationConfirmed StockReservationStatus = "confirmed"
	StockReservationFailed    StockReservationStatus = "failed"
	StockReservationReleased  StockReservationStatus = "released"
)

// StockReservation represents a local stock reservation record.
// Created atomically with order to guarantee compensation.
type StockReservation struct {
	ID                int64                  `json:"id"`
	OrderTrackingID   string                 `json:"order_tracking_id"`
	ProductTrackingID string                 `json:"product_tracking_id"`
	Quantity          int                    `json:"quantity"`
	Status            StockReservationStatus `json:"status"`
	GrpcRequestID     string                 `json:"grpc_request_id"`
	ErrorMessage      string                 `json:"error_message"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
	ConfirmedAt       *time.Time             `json:"confirmed_at,omitempty"`
	ReleasedAt        *time.Time             `json:"released_at,omitempty"`
}
