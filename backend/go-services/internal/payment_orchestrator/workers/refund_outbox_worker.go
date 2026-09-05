package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
)

// RefundOutboxWorker processes pending refund events from the outbox_events table.
// This replaces the fragile `go h.triggerRefund()` goroutine pattern (C3 FIX).
// The outbox guarantees at-least-once delivery with automatic retries.
type RefundOutboxWorker struct {
	db      *pgxpool.Pool
	kafka   *kgo.Client
	interval time.Duration
}

func NewRefundOutboxWorker(db *pgxpool.Pool, kafka *kgo.Client, interval time.Duration) *RefundOutboxWorker {
	return &RefundOutboxWorker{db: db, kafka: kafka, interval: interval}
}

// Start begins the outbox polling loop.
func (w *RefundOutboxWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	log.Printf("[RefundOutbox] Worker started — polling every %v", w.interval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[RefundOutbox] Worker stopping")
			return
		case <-ticker.C:
			if err := w.processPending(ctx); err != nil {
				log.Printf("[RefundOutbox] Error processing pending refunds: %v", err)
			}
		}
	}
}

type refundPayload struct {
	OrderID        string  `json:"order_id"`
	Reason         string  `json:"reason"`
	RefundAmount   float64 `json:"refund_amount"`
	Currency       string  `json:"currency"`
	RefundTo       string  `json:"refund_to"`
}

func (w *RefundOutboxWorker) processPending(ctx context.Context) error {
	rows, err := w.db.Query(ctx,
		`SELECT id, aggregate_id, payload FROM outbox_events
		 WHERE topic = 'payment_refund' AND status = 'PENDING'
		 ORDER BY created_at ASC LIMIT 50`)
	if err != nil {
		return fmt.Errorf("query pending refunds: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var aggregateID string
		var payloadBytes []byte

		if err := rows.Scan(&id, &aggregateID, &payloadBytes); err != nil {
			log.Printf("[RefundOutbox] Failed to scan row: %v", err)
			continue
		}

		var payload refundPayload
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			log.Printf("[RefundOutbox] Invalid payload for outbox id=%d: %v", id, err)
			w.markFailed(ctx, id, "invalid_payload: "+err.Error())
			continue
		}

		// Emit to Kafka refund topic
		refundEvent := map[string]interface{}{
			"order_tracking_id": payload.OrderID,
			"reason":           payload.Reason,
			"refund_amount":    payload.RefundAmount,
			"currency":         payload.Currency,
			"refund_to":        payload.RefundTo,
			"timestamp":        time.Now().UnixMilli(),
		}
		eventBytes, _ := json.Marshal(refundEvent)

		record := &kgo.Record{
			Topic: "orders.refunded",
			Key:   []byte(payload.OrderID),
			Value: eventBytes,
		}

		w.kafka.Produce(ctx, record, func(r *kgo.Record, err error) {
			if err != nil {
				log.Printf("[RefundOutbox] Kafka publish failed for order %s: %v", payload.OrderID, err)
				w.markFailed(ctx, id, err.Error())
			} else {
				w.markProcessed(ctx, id)
			}
		})
	}

	return nil
}

func (w *RefundOutboxWorker) markProcessed(ctx context.Context, id int64) {
	_, _ = w.db.Exec(ctx,
		`UPDATE outbox_events SET status = 'PROCESSED', processed_at = NOW(), updated_at = NOW() WHERE id = $1`, id)
}

func (w *RefundOutboxWorker) markFailed(ctx context.Context, id int64, errMsg string) {
	_, _ = w.db.Exec(ctx,
		`UPDATE outbox_events SET status = 'FAILED', updated_at = NOW() WHERE id = $1`, id)
	log.Printf("[RefundOutbox] Outbox id=%d marked FAILED: %s", id, errMsg)
}
