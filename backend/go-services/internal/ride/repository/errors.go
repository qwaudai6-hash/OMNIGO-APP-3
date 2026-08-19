package repository

import "errors"

// Sentinel errors used across the ride repository and service.
var (
	// ErrRideAlreadyAccepted is returned when a rider tries to accept
	// a ride that has already been claimed by someone else.
	ErrRideAlreadyAccepted = errors.New("ride already accepted by another rider")

	// ErrInvalidRideTransition is returned when the requested status
	// change is not allowed by the ride state machine.
	ErrInvalidRideTransition = errors.New("invalid ride status transition")
)
