package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/twmb/franz-go/pkg/kgo"
)

// PickupDeadlineWorker auto-cancels returns whose pickup_deadline has expired.
// When a rider doesn't pick up within the deadline, the return is cancelled
// and the escrow hold is released.
type PickupDeadlineWorker struct {
	db     *pgxpool.Pool
	escrow *escrow.Service
	kafka  *messaging.KafkaClient
}

func NewPickupDeadlineWorker(
	db *pgxpool.Pool,
	escrowSvc *escrow.Service,
	kafkaClient *messaging.KafkaClient,
) *PickupDeadlineWorker {
	return &PickupDeadlineWorker{
		db:     db,
		escrow: escrowSvc,
		kafka:  kafkaClient,
	}
}

func (w *PickupDeadlineWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	log.Println("[PICKUP-DEADLINE] Worker started, checking every 5 minutes")

	for {
		select {
		case <-ctx.Done():
			log.Println("[PICKUP-DEADLINE] Worker stopped")
			return
		case <-ticker.C:
			w.processExpiredPickups(ctx)
		}
	}
}

func (w *PickupDeadlineWorker) processExpiredPickups(ctx context.Context) {
	rows, err := w.db.Query(ctx,
		`SELECT r.id, r.order_tracking_id, r.customer_tracking_id, r.vendor_tracking_id
		 FROM return_requests r
		 WHERE r.pickup_deadline < NOW()
		   AND r.status IN ('return_requested', 'rider_assigned')
		 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		log.Printf("[PICKUP-DEADLINE] Error querying expired pickups: %v", err)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id, orderID, customerID, vendorID string

		if err := rows.Scan(&id, &orderID, &customerID, &vendorID); err != nil {
			log.Printf("[PICKUP-DEADLINE] Error scanning return: %v", err)
			continue
		}

		if err := w.cancelExpiredReturn(ctx, id, orderID, customerID, vendorID); err != nil {
			log.Printf("[PICKUP-DEADLINE] Error cancelling return %s: %v", id, err)
			continue
		}

		count++
	}

	if count > 0 {
		log.Printf("[PICKUP-DEADLINE] Auto-cancelled %d expired pickup returns", count)
	}
}

func (w *PickupDeadlineWorker) cancelExpiredReturn(
	ctx context.Context,
	returnID, orderID, customerID, vendorID string,
) error {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`UPDATE return_requests
		 SET status = 'return_cancelled', updated_at = NOW()
		 WHERE id = $1 AND status IN ('return_requested', 'rider_assigned')`,
		returnID,
	)
	if err != nil {
		return fmt.Errorf("failed to update return status: %w", err)
	}

	if w.escrow != nil {
		if err := w.escrow.CancelForOrder(ctx, orderID); err != nil {
			log.Printf("[PICKUP-DEADLINE] Warning: failed to release escrow for order %s: %v", orderID, err)
		}
	}

	_, _ = tx.Exec(ctx,
		`UPDATE deliveries SET status = 'cancelled', updated_at = NOW()
		 WHERE order_tracking_id = $1 AND status IN ('broadcasting', 'accepted')`,
		orderID,
	)

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	w.emitEvent(ctx, "return.cancelled", orderID, map[string]interface{}{
		"return_request_id": returnID,
		"order_tracking_id": orderID,
		"reason":            "pickup_deadline_expired",
		"message":           "Return cancelled: rider did not pick up within the deadline.",
		"timestamp":         time.Now().UnixMilli(),
	})

	w.emitEvent(ctx, "returns.customer_notification", customerID, map[string]interface{}{
		"action":    "RETURN_CANCELLED_PICKUP_DEADLINE",
		"order_id":  orderID,
		"message":   "Your return request was cancelled because no rider picked up within the deadline. Please contact support to reschedule.",
		"timestamp": time.Now().UnixMilli(),
	})

	return nil
}

func (w *PickupDeadlineWorker) emitEvent(ctx context.Context, topic, key string, payload interface{}) {
	if w.kafka == nil || w.kafka.Client == nil {
		return
	}
	eventBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[PICKUP-DEADLINE] Failed to marshal event: %v", err)
		return
	}
	record := &kgo.Record{
		Topic: topic,
		Key:   []byte(key),
		Value: eventBytes,
	}
	w.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
		if err != nil {
			log.Printf("[PICKUP-DEADLINE] Failed to produce event to %s: %v", topic, err)
		}
	})
}
