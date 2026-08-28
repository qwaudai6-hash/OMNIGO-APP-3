package models

import "time"

// Ride represents a ride-hailing request (Uber/Careem style)
type Ride struct {
	ID                   int       `json:"id"`
	TrackingID           string    `json:"tracking_id"` // e.g. RIDE-1234abcd
	CustomerTrackID      string    `json:"customer_tracking_id"`
	RiderTrackID         string    `json:"rider_tracking_id,omitempty"`
	VehicleType          string    `json:"vehicle_type"` // e.g. bike, rickshaw, car
	PickupLat            float64   `json:"pickup_lat"`
	PickupLng            float64   `json:"pickup_lng"`
	DropoffLat           float64   `json:"dropoff_lat"`
	DropoffLng           float64   `json:"dropoff_lng"`
	Status               string    `json:"status"` // requested, accepted, in_progress, completed
	AdminCommission      float64   `json:"admin_commission"`
	FareAmount           float64   `json:"fare_amount"`
	ActualDistanceMeters float64   `json:"actual_distance_meters,omitempty"`
	ActualDurationSecs  float64   `json:"actual_duration_seconds,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// RequestRidePayload is used when a customer requests a ride
type RequestRidePayload struct {
	CustomerTrackID string  `json:"customer_tracking_id" binding:"required"`
	VehicleType     string  `json:"vehicle_type" binding:"required"`
	PickupLat       float64 `json:"pickup_lat" binding:"required"`
	PickupLng       float64 `json:"pickup_lng" binding:"required"`
	DropoffLat      float64 `json:"dropoff_lat" binding:"required"`
	DropoffLng      float64 `json:"dropoff_lng" binding:"required"`
	FareAmount      float64 `json:"fare_amount" binding:"required"`
}

// AcceptRidePayload is sent by the rider when they accept a ride offer.
type AcceptRidePayload struct {
	RiderTrackID string `json:"rider_tracking_id" binding:"required"`
}

// UpdateRideStatusPayload is sent by the rider to transition the ride state.
type UpdateRideStatusPayload struct {
	RiderTrackID string `json:"rider_tracking_id" binding:"required"`
	Status       string `json:"status" binding:"required,oneof=in_progress cancelled"`
}

// CompleteRidePayload is sent by the rider at the end of the ride.
// The full fare is split server-side: admin commission → admin_revenue,
// remainder → vendor-style escrow (rider earnings). The payment method
// determines the ledger path:
//   - "cash"     : rider collects cash, owes platform (cash_receivable debit)
//   - "wallet"   : direct transfer from customer wallet
//   - "stripe"   : assume pre-paid at request time (no further action)
type CompleteRidePayload struct {
	RiderTrackID    string  `json:"rider_tracking_id" binding:"required"`
	FinalFare       float64 `json:"final_fare" binding:"required,gt=0"`
	DistanceMeters  float64 `json:"distance_meters"`
	DurationSeconds int     `json:"duration_seconds"`
	PaymentMethod   string  `json:"payment_method" binding:"required,oneof=cash wallet stripe"`
}

// CancelRidePayload is sent by the customer (or the rider) to cancel
// a ride that has not yet started.
type CancelRidePayload struct {
	ActorTrackID string `json:"actor_tracking_id" binding:"required"`
	ActorRole    string `json:"actor_role" binding:"required,oneof=customer rider"`
	Reason       string `json:"reason"`
}

// RideEvent represents the payload published to Kafka for ride broadcasting
type RideEvent struct {
	RideID      string  `json:"ride_id"`
	VehicleType string  `json:"vehicle_type"`
	PickupLat   float64 `json:"pickup_lat"`
	PickupLng   float64 `json:"pickup_lng"`
	FareAmount  float64 `json:"fare_amount"`
}

// VehicleEstimate bundles details for a single vehicle option
type VehicleEstimate struct {
	VehicleType     string  `json:"vehicle_type"`
	BaseFare        float64 `json:"base_fare"`
	DistanceFare    float64 `json:"distance_fare"`
	DurationFare    float64 `json:"duration_fare"`
	SurgeMultiplier float64 `json:"surge_multiplier"`
	TotalFare       float64 `json:"total_fare"`
	EtaMinutes      float64 `json:"eta_minutes"`
}

// PricingEstimateResponse represents the complete route and multi-vehicle quote
type PricingEstimateResponse struct {
	PickupLat       float64           `json:"pickup_lat"`
	PickupLng       float64           `json:"pickup_lng"`
	DropoffLat      float64           `json:"dropoff_lat"`
	DropoffLng      float64           `json:"dropoff_lng"`
	DistanceMeters  float64           `json:"distance_meters"`
	DurationSeconds float64           `json:"duration_seconds"`
	Polyline        [][]float64       `json:"polyline"`
	Estimates       []VehicleEstimate `json:"estimates"`
}

// RideBid represents a counter-offer or acceptance of the fare by a driver
type RideBid struct {
	ID           int       `json:"id"`
	RideTrackID  string    `json:"ride_tracking_id"`
	RiderTrackID string    `json:"rider_tracking_id"`
	BidAmount    float64   `json:"bid_amount"`
	Status       string    `json:"status"` // pending, accepted, rejected
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// SubmitBidPayload is sent by a rider to bid on a ride
type SubmitBidPayload struct {
	RiderTrackID string  `json:"rider_tracking_id" binding:"required"`
	BidAmount    float64 `json:"bid_amount" binding:"required"`
}

// AcceptBidPayload is sent by the customer to accept a specific driver's bid
type AcceptBidPayload struct {
	CustomerTrackID string `json:"customer_tracking_id" binding:"required"`
	BidID           int    `json:"bid_id" binding:"required"`
}
