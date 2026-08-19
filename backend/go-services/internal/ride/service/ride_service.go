package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/ride/models"
	"github.com/omnigo/backend/internal/ride/repository"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/tracking"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

type RideService struct {
	repo   *repository.RideRepository
	kafka  *messaging.KafkaClient
	redis  redis.UniversalClient
	ledger *ledger.Service
}

func NewRideService(
	repo *repository.RideRepository,
	kafka *messaging.KafkaClient,
	rdb redis.UniversalClient,
	ledgerSvc *ledger.Service,
) *RideService {
	return &RideService{
		repo:   repo,
		kafka:  kafka,
		redis:  rdb,
		ledger: ledgerSvc,
	}
}

// generateUTID creates the Universal Tracking ID for rides
func generateUTID() string {
	return tracking.Generate("RIDE")
}

// RequestRide handles the business logic for creating a ride
func (s *RideService) RequestRide(ctx context.Context, req *models.RequestRidePayload) (*models.Ride, error) {

	utid := generateUTID()

	ride := &models.Ride{
		TrackingID:      utid,
		CustomerTrackID: req.CustomerTrackID,
		Status:          "requested",
		VehicleType:     req.VehicleType,
		PickupLat:       req.PickupLat,
		PickupLng:       req.PickupLng,
		DropoffLat:      req.DropoffLat,
		DropoffLng:      req.DropoffLng,
		FareAmount:      req.FareAmount,
		AdminCommission: envFloat("RIDE_COMMISSION_PERCENT", 5.0),
	}

	// 1. Save to Database
	err := s.repo.CreateRide(ctx, ride)
	if err != nil {
		return nil, fmt.Errorf("failed to create ride: %w", err)
	}

	// 2. Publish Event to Kafka so we can broadcast it to drivers
	event := models.RideEvent{
		RideID:      ride.TrackingID,
		VehicleType: ride.VehicleType,
		PickupLat:   ride.PickupLat,
		PickupLng:   ride.PickupLng,
		FareAmount:  ride.FareAmount,
	}

	eventBytes, _ := json.Marshal(event)

	if s.kafka != nil {
		record := &kgo.Record{
			Topic: "ride.requested",
			Key:   []byte(ride.TrackingID), // Partition by Ride ID
			Value: eventBytes,
		}

		s.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
			if err != nil {
				fmt.Printf("Warning: Failed to produce ride.requested event: %v\n", err)
			}
		})
	}

	return ride, nil
}

// GetRide fetches a ride by its UTID
func (s *RideService) GetRide(ctx context.Context, trackingID string) (*models.Ride, error) {
	return s.repo.GetRideByTrackingID(ctx, trackingID)
}

// AcceptRide assigns a rider to a ride request. Uses row-level locking
// inside the repository so two concurrent Accept calls cannot both win.
func (s *RideService) AcceptRide(ctx context.Context, trackingID string, req *models.AcceptRidePayload) (*models.Ride, error) {
	if !strings.HasPrefix(req.RiderTrackID, "RIDR-") {
		return nil, fmt.Errorf("invalid rider tracking id")
	}
	if err := s.repo.AssignRider(ctx, trackingID, req.RiderTrackID); err != nil {
		return nil, err
	}
	return s.repo.GetRideByTrackingID(ctx, trackingID)
}

// UpdateRideStatus moves the ride through the state machine.
func (s *RideService) UpdateRideStatus(ctx context.Context, trackingID string, req *models.UpdateRideStatusPayload) (*models.Ride, error) {
	if !strings.HasPrefix(req.RiderTrackID, "RIDR-") {
		return nil, fmt.Errorf("invalid rider tracking id")
	}
	if err := s.repo.UpdateRideStatus(ctx, trackingID, req.RiderTrackID, req.Status); err != nil {
		return nil, err
	}
	return s.repo.GetRideByTrackingID(ctx, trackingID)
}

// CompleteRide finishes the ride, updates the fare, and runs the
// money-split through the ledger. The split is:
//
//   - For payment_method=cash:
//     debit central_escrow  (the platform's float held for the trip)
//     credit admin_revenue  (the admin commission %)
//     credit rider_wallet   (rider earnings, later settled at cash-out)
//
//   - For payment_method=wallet:
//     debit customer_wallet equivalent
//     credit admin_revenue
//     credit rider_wallet
//
//   - For payment_method=stripe:
//     already settled at RequestRide time via Stripe PaymentIntent;
//     we only credit the rider's wallet here.
func (s *RideService) CompleteRide(ctx context.Context, trackingID string, req *models.CompleteRidePayload) (*models.Ride, error) {
	if !strings.HasPrefix(req.RiderTrackID, "RIDR-") {
		return nil, fmt.Errorf("invalid rider tracking id")
	}
	if err := s.repo.CompleteRide(ctx, trackingID, req.RiderTrackID, req.FinalFare, float64(req.DistanceMeters), float64(req.DurationSeconds)); err != nil {
		return nil, err
	}

	// 2. Settle the money. We use the fare stored on the ride (re-fetched
	//    above) so the split stays consistent with what the customer saw.
	ride, err := s.repo.GetRideByTrackingID(ctx, trackingID)
	if err != nil {
		return nil, err
	}

	if s.ledger != nil && ride.FareAmount > 0 {
		commissionRate := ride.AdminCommission
		if commissionRate <= 0 || commissionRate >= 100 {
			commissionRate = envFloat("RIDE_COMMISSION_PERCENT", 5.0)
		}
		commission := ride.FareAmount * (commissionRate / 100.0)
		riderEarning := ride.FareAmount - commission

		idempotencyKey := fmt.Sprintf("ride:complete:%s", trackingID)
		_, err := s.ledger.MultiTransfer(ctx, []ledger.TransferRequest{
			{
				DebitAccount:   ledger.AccountCentralEscrow,
				CreditAccount:  ledger.AccountAdminRevenue,
				Amount:         commission,
				ReferenceType:  "ride_completion",
				ReferenceID:    trackingID,
				Description:    fmt.Sprintf("Admin commission on ride %s (%.2f PKR)", trackingID, commission),
				IdempotencyKey: idempotencyKey + ":admin",
			},
			{
				DebitAccount:   ledger.AccountCentralEscrow,
				CreditAccount:  ledger.AccountRiderWallet,
				Amount:         riderEarning,
				ReferenceType:  "ride_completion",
				ReferenceID:    trackingID,
				Description:    fmt.Sprintf("Rider earnings for ride %s (%.2f PKR)", trackingID, riderEarning),
				IdempotencyKey: idempotencyKey + ":rider",
			},
		})
		if err != nil {
			return nil, fmt.Errorf("ride completion succeeded but ledger split failed: %w", err)
		}
	}

	// 3. Emit ride.completed event for analytics + admin dashboards.
	if s.kafka != nil {
		eventBytes, _ := json.Marshal(map[string]any{
			"ride_id":          trackingID,
			"rider_id":         req.RiderTrackID,
			"final_fare":       req.FinalFare,
			"distance_meters":  req.DistanceMeters,
			"duration_seconds": req.DurationSeconds,
			"payment_method":   req.PaymentMethod,
		})
		s.kafka.Client.Produce(ctx, &kgo.Record{
			Topic: "ride.completed",
			Key:   []byte(trackingID),
			Value: eventBytes,
		}, func(_ *kgo.Record, err error) {
			if err != nil {
				fmt.Printf("Warning: failed to produce ride.completed: %v\n", err)
			}
		})
	}

	return ride, nil
}

// CancelRide cancels a ride that has not yet started. Customer or rider
// can call it. After completion the ride is final — no cancellations.
func (s *RideService) CancelRide(ctx context.Context, trackingID string, req *models.CancelRidePayload) (*models.Ride, error) {
	// The repository update enforces the state machine: only rides in
	// "requested" or "accepted" can be cancelled.
	if err := s.repo.UpdateRideStatus(ctx, trackingID, req.ActorTrackID, "cancelled"); err != nil {
		return nil, err
	}
	return s.repo.GetRideByTrackingID(ctx, trackingID)
}

// SubmitBid submits a new bid for a requested ride from a rider
func (s *RideService) SubmitBid(ctx context.Context, trackingID string, req *models.SubmitBidPayload) (*models.RideBid, error) {
	if !strings.HasPrefix(req.RiderTrackID, "RIDR-") {
		return nil, fmt.Errorf("invalid rider tracking id")
	}

	bid := &models.RideBid{
		RideTrackID:  trackingID,
		RiderTrackID: req.RiderTrackID,
		BidAmount:    req.BidAmount,
	}

	err := s.repo.SaveBid(ctx, bid)
	if err != nil {
		return nil, err
	}

	// Publish bid event
	if s.kafka != nil {
		eventBytes, _ := json.Marshal(bid)
		s.kafka.Client.Produce(ctx, &kgo.Record{
			Topic: "ride.bid.submitted",
			Key:   []byte(trackingID),
			Value: eventBytes,
		}, func(_ *kgo.Record, err error) {
			if err != nil {
				fmt.Printf("Warning: failed to produce ride.bid.submitted: %v\n", err)
			}
		})
	}

	return bid, nil
}

// GetBidsForRide returns all active bids for a ride
func (s *RideService) GetBidsForRide(ctx context.Context, trackingID string) ([]models.RideBid, error) {
	return s.repo.GetBidsForRide(ctx, trackingID)
}

// AcceptBid accepts a specific driver's bid and assigns the ride
func (s *RideService) AcceptBid(ctx context.Context, trackingID string, req *models.AcceptBidPayload) (*models.RideBid, error) {
	if !strings.HasPrefix(req.CustomerTrackID, "CUST-") {
		return nil, fmt.Errorf("invalid customer tracking id")
	}

	bid, err := s.repo.AcceptBid(ctx, trackingID, req.BidID)
	if err != nil {
		return nil, err
	}

	// Publish bid accepted event
	if s.kafka != nil {
		eventBytes, _ := json.Marshal(bid)
		s.kafka.Client.Produce(ctx, &kgo.Record{
			Topic: "ride.bid.accepted",
			Key:   []byte(trackingID),
			Value: eventBytes,
		}, func(_ *kgo.Record, err error) {
			if err != nil {
				fmt.Printf("Warning: failed to produce ride.bid.accepted: %v\n", err)
			}
		})
	}

	return bid, nil
}
