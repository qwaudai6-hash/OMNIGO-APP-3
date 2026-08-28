package models

import "time"

// Delivery statuses
const (
	StatusBroadcasting = "broadcasting"
	StatusAccepted     = "accepted"
	StatusPickedUp     = "picked_up"
	StatusInTransit    = "in_transit"
	StatusCompleted    = "completed"
	StatusFailed       = "failed"
	StatusCancelled    = "cancelled"
	StatusAssigned     = "assigned"
	StatusDisputed     = "disputed"
)

// DeliveryGig represents a delivery task broadcasted to riders
type DeliveryGig struct {
	ID                      int       `json:"id"`
	TrackingID              string    `json:"tracking_id"` // e.g. GIG-1234abcd
	OrderTrackingID         string    `json:"order_tracking_id"`
	VendorStoreTrackID      string    `json:"vendor_store_tracking_id"`
	AssignedRiderID         string    `json:"assigned_rider_id,omitempty"`
	CustomerTrackID         string    `json:"customer_tracking_id"`
	EligibleRiders          []string  `json:"eligible_riders,omitempty"` // populated during broadcast
	IsCOD                   bool      `json:"is_cod"`
	OrderTotal              float64   `json:"order_total"`
	CustomerPhone           string    `json:"customer_phone"`
	Status                  string    `json:"status"` // broadcasting, accepted, picked_up, in_transit, completed, failed
	AdminCommission         float64   `json:"admin_commission"`
	RiderEarning            float64   `json:"rider_earning"`
	Tips                    float64   `json:"tips"`
	PetrolAllowance         float64   `json:"petrol_allowance"`
	PickupLat               float64   `json:"pickup_lat"`
	PickupLng               float64   `json:"pickup_lng"`
	DropoffLat              float64   `json:"dropoff_lat"`
	DropoffLng              float64   `json:"dropoff_lng"`
	OTPCode                 string    `json:"otp_code,omitempty"`
	PickupPhotoURL          string    `json:"pickup_photo_url,omitempty"`
	DeliveryPhotoURL        string    `json:"delivery_photo_url,omitempty"`
	CustomerDisputePhotoURL string    `json:"customer_dispute_photo_url,omitempty"`
	DisputeStatus           string    `json:"dispute_status,omitempty"` // none, disputed, resolved_rider_guilty, resolved_vendor_guilty
	ProofOfDeliveryURL      string    `json:"proof_of_delivery_url,omitempty"`
	ProofOfDeliveryType     string    `json:"proof_of_delivery_type,omitempty"` // photo, signature, pin
	CancelReason            string    `json:"cancel_reason,omitempty"`
	DeliveryFee             float64   `json:"delivery_fee"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

// UpdateLocationRequest used by riders to update their live location
type UpdateLocationRequest struct {
	RiderTrackID string  `json:"rider_tracking_id" binding:"required"`
	Longitude    float64 `json:"longitude" binding:"required"`
	Latitude     float64 `json:"latitude" binding:"required"`
}

// OrderEvent represents the payload consumed from Kafka
type OrderEvent struct {
	OrderID            string  `json:"order_id"`
	UserTrackID        string  `json:"user_tracking_id"`
	VendorStoreTrackID string  `json:"vendor_store_tracking_id"`
	TotalAmount        float64 `json:"total_amount"`
	IsCOD              bool    `json:"is_cod"`
	CustomerPhone      string  `json:"customer_phone"`
	Tips               float64 `json:"tips"`
	PetrolAllowance    float64 `json:"petrol_allowance"`
	DropoffLat         float64 `json:"dropoff_lat"`
	DropoffLng         float64 `json:"dropoff_lng"`
	Timestamp          int64   `json:"timestamp"`
}

// AcceptGigRequest used by riders to accept a broadcasted gig
type AcceptGigRequest struct {
	TrackingID   string `json:"tracking_id" binding:"required"` // Gig Tracking ID
	RiderTrackID string `json:"rider_tracking_id" binding:"required"`
}

// UpdateGigStatusRequest used by riders to update delivery status
type UpdateGigStatusRequest struct {
	Status   string `json:"status" binding:"required"` // picked_up, in_transit, completed, failed
	OTPCode  string `json:"otp_code,omitempty"`
	PhotoURL string `json:"photo_url,omitempty"` // Pickup or Delivery photo URL
}

// DisputeOrderRequest used by customers to report disputes
type DisputeOrderRequest struct {
	TrackingID string `json:"tracking_id" binding:"required"`
	PhotoURL   string `json:"photo_url" binding:"required"`
	Reason     string `json:"reason" binding:"required"`
}

// CancelGigRequest used when a rider cancels an active order mid-delivery
type CancelGigRequest struct {
	TrackingID   string `json:"tracking_id" binding:"required"`
	RiderTrackID string `json:"rider_tracking_id" binding:"required"`
	Reason       string `json:"reason"`
}

// RouteResponse is the normalized OSRM route payload returned to clients.
type RouteResponse struct {
	DistanceMeters  float64     `json:"distance_meters"`
	DurationSeconds float64     `json:"duration_seconds"`
	Coordinates     [][]float64 `json:"coordinates"`
	Source          string      `json:"source"`
}

// RideEstimateRequest is the payload for pricing a trip.
type RideEstimateRequest struct {
	PickupLat   float64 `json:"pickup_lat" binding:"required"`
	PickupLng   float64 `json:"pickup_lng" binding:"required"`
	DropoffLat  float64 `json:"dropoff_lat" binding:"required"`
	DropoffLng  float64 `json:"dropoff_lng" binding:"required"`
	VehicleType string  `json:"vehicle_type"` // bike, rickshaw, car; empty returns all
}

// FareBreakdown contains per-vehicle pricing details.
type FareBreakdown struct {
	VehicleType     string  `json:"vehicle_type"`
	BaseFare        float64 `json:"base_fare"`
	PerKmRate       float64 `json:"per_km_rate"`
	EstimatedKm     float64 `json:"estimated_km"`
	SurgeMultiplier float64 `json:"surge_multiplier"`
	TotalFare       float64 `json:"total_fare"`
	Currency        string  `json:"currency"`
	EtaSeconds      float64 `json:"eta_seconds"`
}

// RideEstimateResponse returns unified pricing and route geometry.
type RideEstimateResponse struct {
	Estimates   []FareBreakdown `json:"estimates"`
	Geometry    [][]float64     `json:"geometry"`     // Preview polyline coordinates
	RouteSource string          `json:"route_source"` // "osrm" = real roads, "estimated" = approx straight line
}

// SurgeHex represents a single H3 hexagon and its surge multiplier.
type SurgeHex struct {
	HexID           string      `json:"hex_id"`
	SurgeMultiplier float64     `json:"surge_multiplier"`
	Boundary        [][]float64 `json:"boundary"` // [ [lat, lng], [lat, lng], ... ]
}

// CreateBidRequest used by customer to send a custom fare bid to riders
type CreateBidRequest struct {
	CustomerTrackID string  `json:"customer_tracking_id" binding:"required"`
	VehicleType     string  `json:"vehicle_type" binding:"required"`
	ServiceType     string  `json:"service_type"` // passenger or courier
	PickupLat       float64 `json:"pickup_lat" binding:"required"`
	PickupLng       float64 `json:"pickup_lng" binding:"required"`
	DropoffLat      float64 `json:"dropoff_lat" binding:"required"`
	DropoffLng      float64 `json:"dropoff_lng" binding:"required"`
	NegotiatedFare  float64 `json:"negotiated_fare" binding:"required"`
}

// CounterBidRequest used by rider to offer a counter-price
type CounterBidRequest struct {
	BidID        string  `json:"bid_id" binding:"required"`
	RiderTrackID string  `json:"rider_tracking_id" binding:"required"`
	RiderName    string  `json:"rider_name" binding:"required"`
	Rating       string  `json:"rating"`
	VehiclePlate string  `json:"vehicle_plate"`
	ProposedFare float64 `json:"proposed_fare" binding:"required"`
	ETA          string  `json:"eta"`
}

// RideBid represents an active negotiation session
type RideBid struct {
	BidID           string    `json:"bid_id"`
	CustomerTrackID string    `json:"customer_tracking_id"`
	VehicleType     string    `json:"vehicle_type"`
	ServiceType     string    `json:"service_type"`
	PickupLat       float64   `json:"pickup_lat"`
	PickupLng       float64   `json:"pickup_lng"`
	DropoffLat      float64   `json:"dropoff_lat"`
	DropoffLng      float64   `json:"dropoff_lng"`
	NegotiatedFare  float64   `json:"negotiated_fare"`
	Status          string    `json:"status"` // searching, offers_received, accepted, cancelled
	CreatedAt       time.Time `json:"created_at"`
}
