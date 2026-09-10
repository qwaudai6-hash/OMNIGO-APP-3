package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RefundProcessorWorker processes pending refund events from the outbox_events table
// and directly credits the customer wallet. This replaces the broken Kafka-only path
// where nobody consumed the "orders.refunded" topic (FINANCIAL-AUDIT Fix #1).
type RefundProcessorWorker struct {
	db       *pgxpool.Pool
	interval time.Duration
}

func NewRefundProcessorWorker(db *pgxpool.Pool, interval time.Duration) *RefundProcessorWorker {
	return &RefundProcessorWorker{db: db, interval: interval}
}

// Start begins the outbox polling loop.
func (w *RefundProcessorWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	log.Printf("[RefundProcessor] Worker started — polling every %v", w.interval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[RefundProcessor] Worker stopping")
			return
		case <-ticker.C:
			if err := w.processPending(ctx); err != nil {
				log.Printf("[RefundProcessor] Error processing pending refunds: %v", err)
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

func (w *RefundProcessorWorker) processPending(ctx context.Context) error {
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
			log.Printf("[RefundProcessor] Failed to scan row: %v", err)
			continue
		}

		var payload refundPayload
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			log.Printf("[RefundProcessor] Invalid payload for outbox id=%d: %v", id, err)
			w.markFailed(ctx, id, "invalid_payload: "+err.Error())
			continue
		}

		if err := w.processSingleRefund(ctx, id, &payload); err != nil {
			log.Printf("[RefundProcessor] Failed to process refund for order %s: %v", payload.OrderID, err)
			w.markFailed(ctx, id, err.Error())
		}
	}

	return nil
}

func (w *RefundProcessorWorker) processSingleRefund(ctx context.Context, outboxID int64, payload *refundPayload) error {
	if payload.OrderID == "" {
		return fmt.Errorf("empty order_id in refund payload")
	}

	// 1. Look up the customer tracking ID and refund amount from the order
	var customerID string
	var orderTotalPaisa int64
	var paymentGateway string
	err := w.db.QueryRow(ctx,
		`SELECT COALESCE(customer_tracking_id, ''), COALESCE(total_amount_paisa, 0),
		        COALESCE(payment_gateway, '')
		 FROM orders WHERE order_tracking_id = $1`,
		payload.OrderID,
	).Scan(&customerID, &orderTotalPaisa, &paymentGateway)
	if err != nil {
		return fmt.Errorf("order lookup failed for %s: %w", payload.OrderID, err)
	}

	if customerID == "" {
		return fmt.Errorf("order %s has no customer_tracking_id — cannot credit wallet", payload.OrderID)
	}

	// 2. Calculate refund amount in paisa
	// payload.RefundAmount is in rupees (float64) — convert to paisa
	var refundPaisa int64
	if payload.RefundAmount > 0 {
		refundPaisa = int64(math.Round(payload.RefundAmount * 100))
	} else {
		// Default: full order refund
		refundPaisa = orderTotalPaisa
	}

	if refundPaisa <= 0 {
		return fmt.Errorf("refund amount is zero or negative for order %s", payload.OrderID)
	}

	// 3. Skip COD orders — no online payment was captured
	if paymentGateway == "cod" || paymentGateway == "" {
		log.Printf("[RefundProcessor] Skipping COD/no-gateway order %s — no payment captured", payload.OrderID)
		w.markProcessed(ctx, outboxID)
		return nil
	}

	// 4. Credit customer wallet atomically
	creditQuery := `
		INSERT INTO customer_wallet (customer_tracking_id, balance_paisa, lifetime_spent_paisa, updated_at)
		VALUES ($1, $2, 0, NOW())
		ON CONFLICT (customer_tracking_id)
		DO UPDATE SET
			balance_paisa = customer_wallet.balance_paisa + $2,
			updated_at = NOW()
	`
	tag, err := w.db.Exec(ctx, creditQuery, customerID, refundPaisa)
	if err != nil {
		return fmt.Errorf("wallet credit failed for customer %s: %w", customerID, err)
	}

	// 5. Update order payment status to 'refunded'
	_, err = w.db.Exec(ctx,
		`UPDATE orders SET payment_status = 'refunded', updated_at = NOW()
		 WHERE order_tracking_id = $1 AND payment_status != 'refunded'`,
		payload.OrderID,
	)
	if err != nil {
		log.Printf("[RefundProcessor] Warning: failed to update order payment_status for %s: %v", payload.OrderID, err)
	}

	// 6. Mark outbox event as processed
	w.markProcessed(ctx, outboxID)

	log.Printf("[RefundProcessor] Refund processed: %d paisa credited to customer %s for order %s (reason: %s, rows affected: %d)",
		refundPaisa, customerID, payload.OrderID, payload.Reason, tag.RowsAffected())
	return nil
}

func (w *RefundProcessorWorker) markProcessed(ctx context.Context, id int64) {
	_, _ = w.db.Exec(ctx,
		`UPDATE outbox_events SET status = 'PROCESSED', processed_at = NOW(), updated_at = NOW() WHERE id = $1`, id)
}

func (w *RefundProcessorWorker) markFailed(ctx context.Context, id int64, errMsg string) {
	_, _ = w.db.Exec(ctx,
		`UPDATE outbox_events SET status = 'FAILED', error_message = $2, updated_at = NOW() WHERE id = $1`, id, errMsg)
	log.Printf("[RefundProcessor] Outbox id=%d marked FAILED: %s", id, errMsg)
}
