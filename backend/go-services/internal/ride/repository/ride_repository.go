package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/ride/models"
	"github.com/omnigo/backend/internal/shared/database"
)

type RideRepository struct {
	writer *pgxpool.Pool
	reader *pgxpool.Pool
}

func NewRideRepository(writer, reader *pgxpool.Pool) *RideRepository {
	return &RideRepository{
		writer: writer,
		reader: reader,
	}
}

// CreateRide inserts a new ride request into PostgreSQL
func (r *RideRepository) CreateRide(ctx context.Context, ride *models.Ride) error {
	query := `
		INSERT INTO rides (tracking_id, customer_tracking_id, status, admin_commission, fare_amount, vehicle_type, pickup_lat, pickup_lng, dropoff_lat, dropoff_lng)
		VALUES ($1, $2, 'requested', $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at
	`
	err := r.writer.QueryRow(ctx, query,
		ride.TrackingID,
		ride.CustomerTrackID,
		ride.AdminCommission,
		ride.FareAmount,
		ride.VehicleType,
		ride.PickupLat,
		ride.PickupLng,
		ride.DropoffLat,
		ride.DropoffLng,
	).Scan(&ride.ID, &ride.CreatedAt, &ride.UpdatedAt)

	return err
}

// GetRideByTrackingID retrieves a ride by its UTID
func (r *RideRepository) GetRideByTrackingID(ctx context.Context, trackingID string) (*models.Ride, error) {
	query := `
		SELECT id, tracking_id, customer_tracking_id, rider_tracking_id, status, admin_commission, fare_amount, vehicle_type, pickup_lat, pickup_lng, dropoff_lat, dropoff_lng, created_at, updated_at
		FROM rides
		WHERE tracking_id = $1
	`
	var ride models.Ride
	var riderID *string

	err := r.reader.QueryRow(ctx, query, trackingID).Scan(
		&ride.ID, &ride.TrackingID, &ride.CustomerTrackID, &riderID,
		&ride.Status, &ride.AdminCommission, &ride.FareAmount, &ride.VehicleType,
		&ride.PickupLat, &ride.PickupLng, &ride.DropoffLat, &ride.DropoffLng,
		&ride.CreatedAt, &ride.UpdatedAt,
	)

	if riderID != nil {
		ride.RiderTrackID = *riderID
	}

	if err != nil {
		return nil, err
	}
	return &ride, nil
}

// AssignRider sets the rider on a ride and transitions it to "accepted".
// Uses FOR UPDATE row lock so two riders cannot claim the same ride.
func (r *RideRepository) AssignRider(ctx context.Context, trackingID, riderTrackID string) error {
	tx, err := r.writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Check rider verification status and order count limit (max 10 orders for unverified riders)
	var isVerified bool
	userQuery := `SELECT COALESCE(is_verified, false) FROM users WHERE tracking_id = $1`
	err = tx.QueryRow(ctx, userQuery, riderTrackID).Scan(&isVerified)
	if err != nil {
		isVerified = false
	}

	if !isVerified {
		return fmt.Errorf("conflict: rider KYC verification required. Please submit CNIC and Driving License documents for admin approval")
	}

	query := `
		UPDATE rides
		SET rider_tracking_id = $1, status = 'accepted', updated_at = NOW()
		WHERE tracking_id = $2
		  AND status = 'requested'
		  AND rider_tracking_id IS NULL
	`
	tag, err := tx.Exec(ctx, query, riderTrackID, trackingID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRideAlreadyAccepted
	}
	return tx.Commit(ctx)
}

// UpdateRideStatus moves the ride to a new state, validating the
// state machine. Returns ErrInvalidRideTransition if the move is not allowed.
func (r *RideRepository) UpdateRideStatus(ctx context.Context, trackingID, riderTrackID, newStatus string) error {
	// State machine: accepted → in_progress | cancelled,
	//                in_progress → completed | cancelled.
	// The transition check is expressed inline because pgx cannot encode a
	// map for ANY($n); each allowed (current → next) pair is listed below.
	query := `
		UPDATE rides
		SET status = $1, updated_at = NOW()
		WHERE tracking_id = $2
		  AND (
		        (status = 'requested' AND $4::text = 'cancelled' AND rider_tracking_id IS NULL)
		     OR (rider_tracking_id = $3 AND (
		          (status = 'accepted'    AND $4::text IN ('in_progress', 'cancelled'))
		       OR (status = 'in_progress' AND $4::text IN ('completed',  'cancelled'))
		     ))
		  )
	`
	tag, err := r.writer.Exec(ctx, query, newStatus, trackingID, riderTrackID, newStatus)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidRideTransition
	}
	return nil
}

// CompleteRide marks the ride as completed and persists the final fare along
// with the rider-reported distance/duration (HIGH-07) so analytics and
// per-km earnings audits are possible. The caller must already have
// validated that the rider is authorized and the fare is the agreed one.
func (r *RideRepository) CompleteRide(ctx context.Context, trackingID, riderTrackID string, finalFare, distanceMeters, durationSeconds float64) error {
	query := `
		UPDATE rides
		SET status = 'completed',
		    fare_amount = $1,
		    actual_distance_meters = $4,
		    actual_duration_seconds = $5,
		    updated_at = NOW()
		WHERE tracking_id = $2
		  AND rider_tracking_id = $3
		  AND status = 'in_progress'
	`
	tag, err := r.writer.Exec(ctx, query, finalFare, trackingID, riderTrackID, distanceMeters, durationSeconds)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidRideTransition
	}
	return nil
}

// SaveBid inserts a new ride bid from a rider
func (r *RideRepository) SaveBid(ctx context.Context, bid *models.RideBid) error {
	checks := []struct {
		id    string
		label string
		query string
	}{
		{bid.RideTrackID, "ride", "SELECT 1 FROM rides WHERE tracking_id = $1"},
		{bid.RiderTrackID, "user", "SELECT 1 FROM users WHERE tracking_id = $1"},
	}
	for _, c := range checks {
		ok, err := database.Exists(ctx, r.writer, c.query, c.id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s %s does not exist", c.label, c.id)
		}
	}

	query := `
		INSERT INTO ride_bids (ride_tracking_id, rider_tracking_id, bid_amount, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id, status, created_at, updated_at
	`
	err := r.writer.QueryRow(ctx, query,
		bid.RideTrackID,
		bid.RiderTrackID,
		bid.BidAmount,
	).Scan(&bid.ID, &bid.Status, &bid.CreatedAt, &bid.UpdatedAt)

	return err
}

// GetBidsForRide returns all active bids for a ride
func (r *RideRepository) GetBidsForRide(ctx context.Context, rideTrackID string) ([]models.RideBid, error) {
	query := `
		SELECT id, ride_tracking_id, rider_tracking_id, bid_amount, status, created_at, updated_at
		FROM ride_bids
		WHERE ride_tracking_id = $1 AND status = 'pending'
		ORDER BY bid_amount ASC
	`
	rows, err := r.reader.Query(ctx, query, rideTrackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bids []models.RideBid
	for rows.Next() {
		var b models.RideBid
		if err := rows.Scan(&b.ID, &b.RideTrackID, &b.RiderTrackID, &b.BidAmount, &b.Status, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		bids = append(bids, b)
	}
	return bids, nil
}

// AcceptBid marks a bid as accepted, rejects others, and assigns the rider and fare to the ride
func (r *RideRepository) AcceptBid(ctx context.Context, rideTrackID string, bidID int) (*models.RideBid, error) {
	tx, err := r.writer.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Update the chosen bid
	var bid models.RideBid
	acceptBidQuery := `
		UPDATE ride_bids
		SET status = 'accepted', updated_at = NOW()
		WHERE id = $1 AND ride_tracking_id = $2 AND status = 'pending'
		RETURNING id, ride_tracking_id, rider_tracking_id, bid_amount, status, created_at, updated_at
	`
	err = tx.QueryRow(ctx, acceptBidQuery, bidID, rideTrackID).Scan(
		&bid.ID, &bid.RideTrackID, &bid.RiderTrackID, &bid.BidAmount, &bid.Status, &bid.CreatedAt, &bid.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to accept bid or bid not found: %w", err)
	}

	// Reject all other bids for this ride
	rejectBidsQuery := `
		UPDATE ride_bids
		SET status = 'rejected', updated_at = NOW()
		WHERE ride_tracking_id = $1 AND id != $2 AND status = 'pending'
	`
	_, _ = tx.Exec(ctx, rejectBidsQuery, rideTrackID, bidID)

	// Update the ride itself to set rider and fare
	updateRideQuery := `
		UPDATE rides
		SET rider_tracking_id = $1, fare_amount = $2, status = 'accepted', updated_at = NOW()
		WHERE tracking_id = $3 AND status = 'requested' AND rider_tracking_id IS NULL
	`
	tag, err := tx.Exec(ctx, updateRideQuery, bid.RiderTrackID, bid.BidAmount, rideTrackID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrRideAlreadyAccepted
	}

	err = tx.Commit(ctx)
	if err != nil {
		return nil, err
	}

	return &bid, nil
}
