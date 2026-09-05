package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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
	acquired, err := s.redis.SetNX(ctx, lockKey, riderTrackID, 60*time.Second).Result()
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

// SendCustomerOTPNotification sends an FCM push notification to the customer
// with their delivery OTP when a rider is assigned to their order.
func (s *DeliveryService) SendCustomerOTPNotification(ctx context.Context, gig *models.DeliveryGig) error {
	// Get customer's FCM token from device_tokens table
	fcmToken, err := s.repo.GetUserFCMToken(ctx, gig.CustomerTrackID)
	if err != nil || fcmToken == "" {
		log.Printf("SendCustomerOTPNotification: no FCM token for customer %s: %v", gig.CustomerTrackID, err)
		return fmt.Errorf("no FCM token for customer %s: %w", gig.CustomerTrackID, err)
	}

	// Read FCM server key from environment
	fcmServerKey := getEnv("FCM_SERVER_KEY", "")
	if fcmServerKey == "" {
		log.Printf("SendCustomerOTPNotification: FCM_SERVER_KEY not configured, skipping push")
		return fmt.Errorf("FCM_SERVER_KEY not configured")
	}

	// Build FCM HTTP v1 payload
	payload := map[string]interface{}{
		"token": fcmToken,
		"notification": map[string]string{
			"title": "Your Delivery OTP",
			"body":  fmt.Sprintf("Share this code with the rider when they arrive: %s", gig.OTPCode),
		},
		"data": map[string]string{
			"type":           "delivery_otp",
			"order_id":        gig.OrderTrackingID,
			"gig_id":          gig.TrackingID,
			"otp_code":        gig.OTPCode,
		},
		"android": map[string]interface{}{
			"priority": "high",
			"notification": map[string]string{
				"channel_id": "delivery_updates",
				"sound":      "default",
			},
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("SendCustomerOTPNotification: failed to marshal payload: %v", err)
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://fcm.googleapis.com/fcm/send", bytes.NewReader(payloadBytes))
	if err != nil {
		log.Printf("SendCustomerOTPNotification: failed to create request: %v", err)
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "key="+fcmServerKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		log.Printf("SendCustomerOTPNotification: FCM request failed: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("SendCustomerOTPNotification: FCM returned status %d", resp.StatusCode)
		return fmt.Errorf("FCM returned status %d", resp.StatusCode)
	}

	log.Printf("SendCustomerOTPNotification: OTP %s sent to customer %s for order %s",
		gig.OTPCode, gig.CustomerTrackID, gig.OrderTrackingID)
	return nil
}

// getEnv is a simple env helper to avoid importing utils in this file.
func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
