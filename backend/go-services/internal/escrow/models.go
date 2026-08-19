package escrow

import (
	"time"

	"github.com/google/uuid"
)

// EscrowStatus represents the lifecycle of an escrow hold.
type EscrowStatus string

const (
	StatusHeld     EscrowStatus = "held"
	StatusReleased EscrowStatus = "released"
	StatusDisputed EscrowStatus = "disputed"
	StatusRefunded EscrowStatus = "refunded"
)

// EscrowHold represents a vendor fund hold after delivery completion.
type EscrowHold struct {
	ID               uuid.UUID    `json:"id"`
	OrderTrackingID  string       `json:"order_tracking_id"`
	VendorTrackingID string       `json:"vendor_tracking_id"`
	Amount           float64      `json:"amount"`
	Status           EscrowStatus `json:"status"`
	HoldUntil        time.Time    `json:"hold_until"`
	ReleasedAt       *time.Time   `json:"released_at,omitempty"`
	DisputeID        *uuid.UUID   `json:"dispute_id,omitempty"`
	CreatedAt        time.Time    `json:"created_at"`
}
