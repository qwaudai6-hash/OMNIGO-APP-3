package models

import (
	"encoding/json"
	"time"
)

// ReturnRequest represents a product return request from a customer.
type ReturnRequest struct {
	ID                  string          `json:"return_request_id"`
	OrderTrackingID     string          `json:"order_tracking_id"`
	CustomerTrackingID  string          `json:"customer_tracking_id"`
	VendorTrackingID    string          `json:"vendor_tracking_id"`
	StoreTrackingID     string          `json:"store_tracking_id"`
	RiderTrackingID     string          `json:"rider_tracking_id,omitempty"`
	GigTrackingID       string          `json:"gig_tracking_id,omitempty"`
	Reason              string          `json:"reason"`
	ReturnItems         json.RawMessage `json:"return_items"`
	Status              string          `json:"status"`
	RequestedAt         time.Time       `json:"requested_at"`
	PickupDeadline      *time.Time      `json:"pickup_deadline,omitempty"`
	VerifiedAt          *time.Time      `json:"verified_at,omitempty"`
	CompletedAt         *time.Time      `json:"completed_at,omitempty"`
	PickupPhotoURL      string          `json:"pickup_photo_url,omitempty"`
	DeliveryPhotoURL    string          `json:"delivery_photo_url,omitempty"`
	VendorVerificationPhoto string     `json:"vendor_verification_photo,omitempty"`
	ReturnDeliveryFeePaisa int64       `json:"return_delivery_fee_paisa"`
	PaymentMethod       string          `json:"payment_method,omitempty"`
	PaymentStatus       string          `json:"payment_status"`
	EscrowHoldID        string          `json:"escrow_hold_id,omitempty"`
	DisputeReason       string          `json:"dispute_reason,omitempty"`
	DisputeResolvedAt   *time.Time      `json:"dispute_resolved_at,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// ReturnItem represents a single item in a return request.
type ReturnItem struct {
	ProductTrackingID string `json:"product_tracking_id"`
	ProductName       string `json:"product_name"`
	Quantity          int    `json:"quantity"`
	Reason            string `json:"reason,omitempty"`
}

// RequestReturnRequest is the payload for POST /orders/:id/return-request.
type RequestReturnRequest struct {
	Reason      string       `json:"reason" binding:"required"`
	ReturnItems []ReturnItem `json:"return_items" binding:"required,dive"`
}

// VerifyReturnRequest is the payload for POST /returns/:id/vendor-verify.
type VerifyReturnRequest struct {
	Verified        bool   `json:"verified" binding:"required"`
	PhotoURL        string `json:"photo_url" binding:"required"`
	Notes           string `json:"notes,omitempty"`
}

// DisputeReturnRequest is the payload for POST /returns/:id/vendor-dispute.
type DisputeReturnRequest struct {
	Reason   string `json:"reason" binding:"required"`
	PhotoURL string `json:"photo_url" binding:"required"`
}

// ReturnStatus constants
const (
	ReturnStatusRequested        = "return_requested"
	ReturnStatusRiderAssigned    = "rider_assigned"
	ReturnStatusPickupCompleted  = "return_pickup_completed"
	ReturnStatusInTransit        = "return_in_transit"
	ReturnStatusDelivered        = "return_delivered"
	ReturnStatusVerified         = "return_verified"
	ReturnStatusDisputed         = "return_disputed"
	ReturnStatusCompleted        = "return_completed"
	ReturnStatusCancelled        = "return_cancelled"
)

// ValidReturnTransitions defines allowed status transitions.
var ValidReturnTransitions = map[string][]string{
	ReturnStatusRequested:       {ReturnStatusRiderAssigned, ReturnStatusCancelled},
	ReturnStatusRiderAssigned:   {ReturnStatusPickupCompleted, ReturnStatusCancelled},
	ReturnStatusPickupCompleted: {ReturnStatusInTransit, ReturnStatusDelivered}, // direct delivery allowed
	ReturnStatusInTransit:       {ReturnStatusDelivered},
	ReturnStatusDelivered:       {ReturnStatusVerified, ReturnStatusDisputed},
	ReturnStatusVerified:        {ReturnStatusCompleted},
	ReturnStatusDisputed:        {ReturnStatusCompleted},
	ReturnStatusCompleted:       {},
	ReturnStatusCancelled:       {},
}

// IsValidReturnTransition checks if a status transition is allowed.
func IsValidReturnTransition(from, to string) bool {
	allowed, ok := ValidReturnTransitions[from]
	if !ok {
		return false
	}
	for _, target := range allowed {
		if target == to {
			return true
		}
	}
	return false
}
