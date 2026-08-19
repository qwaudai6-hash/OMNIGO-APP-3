package service

import (
	"context"
	"encoding/json"
	"log"

	"github.com/omnigo/backend/internal/ride/models"
	"github.com/omnigo/backend/internal/shared/h3search"
	"github.com/twmb/franz-go/pkg/kgo"
)

type RideBroadcast struct {
	TrackingID      string   `json:"tracking_id"`
	VehicleType     string   `json:"vehicle_type"`
	PickupLat       float64  `json:"pickup_lat"`
	PickupLng       float64  `json:"pickup_lng"`
	DropoffLat      float64  `json:"dropoff_lat"`
	DropoffLng      float64  `json:"dropoff_lng"`
	FareAmount      float64  `json:"fare_amount"`
	EligibleDrivers []string `json:"eligible_drivers"`
}

func (s *RideService) StartConsumer(ctx context.Context) {
	if s.kafka == nil {
		log.Println("Ride Consumer: Kafka not configured, skipping")
		return
	}

	s.kafka.Client.AddConsumeTopics("ride.requested")
	log.Println("Ride Consumer: listening on ride.requested")

	for {
		fetches := s.kafka.Client.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return
		}
		iter := fetches.RecordIter()
		for !iter.Done() {
			record := iter.Next()
			var event models.RideEvent
			if err := json.Unmarshal(record.Value, &event); err != nil {
				log.Printf("Ride Consumer: failed to unmarshal ride.requested: %v", err)
				continue
			}
			s.handleRideRequested(ctx, &event)
		}
	}
}

func (s *RideService) handleRideRequested(ctx context.Context, event *models.RideEvent) {
	if s.redis == nil {
		log.Printf("Ride Consumer: Redis not configured, cannot search nearby drivers for ride %s", event.RideID)
		return
	}

	log.Printf("Ride Consumer: Processing ride.requested for ride %s (vehicle: %s, fare: %.2f)", event.RideID, event.VehicleType, event.FareAmount)

	eligibleDrivers := h3search.FindNearbyRiders(ctx, s.redis, event.PickupLat, event.PickupLng, 5, 5)
	if len(eligibleDrivers) == 0 {
		log.Printf("Ride Consumer: No eligible drivers found for ride %s", event.RideID)
		return
	}

	broadcast := RideBroadcast{
		TrackingID:      event.RideID,
		VehicleType:     event.VehicleType,
		PickupLat:       event.PickupLat,
		PickupLng:       event.PickupLng,
		FareAmount:      event.FareAmount,
		EligibleDrivers: eligibleDrivers,
	}

	payload, _ := json.Marshal(broadcast)
	record := &kgo.Record{
		Topic: "ride.broadcasted",
		Key:   []byte(event.RideID),
		Value: payload,
	}
	s.kafka.Client.Produce(ctx, record, func(r *kgo.Record, err error) {
		if err != nil {
			log.Printf("Ride Consumer: Failed to produce ride.broadcasted: %v", err)
		}
	})
	log.Printf("Ride Consumer: Broadcasted ride %s to %d drivers", event.RideID, len(eligibleDrivers))
}
