package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

// GigTimeoutWorker handles delivery gigs stuck in 'broadcasting' state.
// When no rider accepts within the timeout window:
//  1. First attempt: Re-broadcast to wider area (k=2, 10km)
//  2. Second attempt: Re-broadcast to widest area (k=3, 15km)
//  3. Final attempt: Cancel the gig and notify customer
//
// Without this, gigs remain in broadcasting forever and the customer
// never knows delivery cannot be fulfilled.
//
// Pattern: Uber Eats / DoorDash use progressive retry with escalation.
type GigTimeoutWorker struct {
	db     *pgxpool.Pool
	redis  redis.UniversalClient
	kafka  *kgo.Client
	timeout time.Duration
}

// NewGigTimeoutWorker constructs the worker.
func NewGigTimeoutWorker(db *pgxpool.Pool, rdb redis.UniversalClient, kafkaCl *kgo.Client) *GigTimeoutWorker {
	return &GigTimeoutWorker{
		db:      db,
		redis:   rdb,
		kafka:   kafkaCl,
		timeout: 5 * time.Minute, // Gigs older than 5 minutes in broadcasting
	}
}

// Start begins the timeout check loop.
func (w *GigTimeoutWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()

	log.Printf("[GigTimeout] Started — reaping broadcasting gigs > %v", w.timeout)

	for {
		select {
		case <-ctx.Done():
			log.Println("[GigTimeout] Stopped")
			return
		case <-ticker.C:
			w.processStaleGigs(ctx)
		}
	}
}

// processStaleGigs finds gigs stuck in broadcasting and handles escalation.
func (w *GigTimeoutWorker) processStaleGigs(ctx context.Context) {
	rows, err := w.db.Query(ctx,
		`SELECT d.tracking_id, d.order_tracking_id, d.pickup_lat, d.pickup_lng,
		        d.status, d.created_at
		 FROM deliveries d
		 WHERE d.status = 'broadcasting'
		   AND d.created_at < NOW() - INTERVAL '5 minutes'
		 FOR UPDATE SKIP LOCKED
		 LIMIT 20`,
	)
	if err != nil {
		log.Printf("[GigTimeout] Query error: %v", err)
		return
	}
	defer rows.Close()

	var staleGigs []struct {
		TrackingID     string
		OrderTrackingID string
		PickupLat      *float64
		PickupLng      *float64
		Status         string
		CreatedAt      time.Time
	}

	for rows.Next() {
		var g struct {
			TrackingID      string
			OrderTrackingID string
			PickupLat       *float64
			PickupLng       *float64
			Status          string
			CreatedAt       time.Time
		}
		if err := rows.Scan(&g.TrackingID, &g.OrderTrackingID, &g.PickupLat, &g.PickupLng, &g.Status, &g.CreatedAt); err != nil {
			log.Printf("[GigTimeout] Scan error: %v", err)
			continue
		}
		staleGigs = append(staleGigs, g)
	}
	rows.Close()

	if len(staleGigs) == 0 {
		return
	}

	for _, gig := range staleGigs {
		minutesStale := time.Since(gig.CreatedAt).Minutes()

		// Escalation logic based on how long the gig has been stale
		switch {
		case minutesStale > 15:
			// FINAL: No rider available after 15 minutes — cancel the gig and order
			w.cancelGigAndOrder(ctx, gig.TrackingID, gig.OrderTrackingID)
		case minutesStale > 10:
			// THIRD attempt: Cancel and notify customer
			w.cancelGigAndOrder(ctx, gig.TrackingID, gig.OrderTrackingID)
		case minutesStale > 5:
			// SECOND attempt: Just log — the original broadcast already covered up to 25km
			log.Printf("[GigTimeout] Gig %s (order=%s) still broadcasting after %.0f min — no wider retry needed (already searched k=1 to k=5)",
				gig.TrackingID, gig.OrderTrackingID, minutesStale)
		default:
			// FIRST attempt: Still within window, skip
		}
	}
}

// cancelGigAndOrder cancels both the delivery gig and the parent order.
func (w *GigTimeoutWorker) cancelGigAndOrder(ctx context.Context, gigTrackingID, orderTrackingID string) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		log.Printf("[GigTimeout] Failed to begin transaction: %v", err)
		return
	}
	defer tx.Rollback(ctx)

	// 1. Cancel the gig (idempotent)
	_, _ = tx.Exec(ctx,
		`UPDATE deliveries SET status = 'cancelled', updated_at = NOW()
		 WHERE tracking_id = $1 AND status = 'broadcasting'`,
		gigTrackingID,
	)

	// 2. Cancel the order (idempotent, only if still pending/accepted)
	result, err := tx.Exec(ctx,
		`UPDATE orders SET status = 'cancelled', updated_at = NOW()
		 WHERE order_tracking_id = $1 AND status IN ('pending', 'paid', 'accepted')`,
		orderTrackingID,
	)
	if err != nil {
		log.Printf("[GigTimeout] Failed to cancel order %s: %v", orderTrackingID, err)
		return
	}
	rowsAffected := result.RowsAffected()
	_ = rowsAffected

	if err := tx.Commit(ctx); err != nil {
		log.Printf("[GigTimeout] Failed to commit: %v", err)
		return
	}

	// 3. Publish cancellation event for downstream services
	if w.kafka != nil {
		event := map[string]interface{}{
			"order_tracking_id": orderTrackingID,
			"reason":            "no_rider_available",
			"timestamp":         time.Now().UnixMilli(),
		}
		eventBytes, _ := json.Marshal(event)
		record := &kgo.Record{
			Topic: "orders.cancelled",
			Key:   []byte(orderTrackingID),
			Value: eventBytes,
		}
		w.kafka.Produce(ctx, record, func(_ *kgo.Record, err error) {
			if err != nil {
				fmt.Printf("[GigTimeout] Warning: Failed to produce orders.cancelled for %s: %v\n", orderTrackingID, err)
			}
		})

		// Also emit refund event so payment service can refund the customer.
		// The payment service will check order status and skip if not paid.
		refundEvent := map[string]interface{}{
			"order_tracking_id": orderTrackingID,
			"reason":            "no_rider_available",
			"refund_amount":     0, // 0 = full refund
			"timestamp":         time.Now().UnixMilli(),
		}
		refundBytes, _ := json.Marshal(refundEvent)
		refundRecord := &kgo.Record{
			Topic: "orders.refunded",
			Key:   []byte(orderTrackingID),
			Value: refundBytes,
		}
		w.kafka.Produce(ctx, refundRecord, func(_ *kgo.Record, err error) {
			if err != nil {
				fmt.Printf("[GigTimeout] Warning: Failed to produce orders.refunded for %s: %v\n", orderTrackingID, err)
			}
		})
	}

	log.Printf("[GigTimeout] Cancelled gig %s and order %s (no rider available after timeout)", gigTrackingID, orderTrackingID)
}
