package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/omnigo/backend/internal/delivery/models"
	"github.com/twmb/franz-go/pkg/kgo"
)

// BroadcastGigAlert broadcasts the gig to multiple riders using Kafka
// and sets up the 30-second Redis timeout key.
func (s *DeliveryService) BroadcastGigAlert(ctx context.Context, gig *models.DeliveryGig, topRiders []string) {
	if len(topRiders) == 0 {
		return
	}

	gig.EligibleRiders = topRiders

	// Sanitize broadcast payload — OTP must never be leaked to candidate riders
	broadcastGig := *gig
	broadcastGig.OTPCode = ""

	// Publish Gig to Kafka Event Bus for WebSocket Gateway consumption
	eventBytes, _ := json.Marshal(broadcastGig)
	if s.kafka != nil {
		record := &kgo.Record{
			Topic: "deliveries.broadcasted",
			Key:   []byte(gig.TrackingID),
			Value: eventBytes,
		}
		s.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
			if err != nil {
				log.Printf("Warning: Failed to produce deliveries.broadcasted event: %v", err)
			}
		})
	}

	// Set a 30-second TTL on this gig in Redis.
	// If nobody accepts it, the 'expired' keyspace notification can handle fallback/re-broadcasting.
	offerKey := fmt.Sprintf("gig:offer:%s", gig.TrackingID)
	if s.redis != nil {
		s.redis.Set(ctx, offerKey, "pending", 30*time.Second)
	}

	log.Printf("Broadcasting Gig %s to matching top riders: %v", gig.TrackingID, topRiders)
}

// AttemptToLockGig is kept for backward compatibility with older clients that
// may still pre-lock via Redis before calling AcceptGig. It no longer provides
// the primary race guarantee (Postgres FOR UPDATE in AcceptGigWithEligibility
// does that), but it still clears the 30-second offer timer so the gig does
// not expire while a rider is in the process of accepting.
func (s *DeliveryService) AttemptToLockGig(ctx context.Context, trackingID string, riderTrackID string) (bool, error) {
	if s.redis == nil {
		// ponytail: degraded Redis shouldn't block the accept path. The caller
		// falls back to Postgres-only accept.
		return true, nil
	}

	lockKey := fmt.Sprintf("gig:lock:%s", trackingID)

	// SETNX: Set only if it does not exist. Atomic operation.
	acquired, err := s.redis.SetNX(ctx, lockKey, riderTrackID, 12*time.Hour).Result()
	if err != nil {
		return false, err
	}

	if acquired {
		// Clear the 30-second offer timer so it doesn't expire
		offerKey := fmt.Sprintf("gig:offer:%s", trackingID)
		s.redis.Del(ctx, offerKey)
	}

	return acquired, nil
}
