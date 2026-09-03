package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	stripeSDK "github.com/stripe/stripe-go/v76"

	stripeClient "github.com/omnigo/backend/internal/payment/stripe"
)

// StripeReplayWorker periodically reprocesses unprocessed Stripe events.
// This handles cases where:
//   - The webhook handler crashed after insert but before marking processed
//   - Processing failed due to transient errors (DB timeout, Stripe API down)
//   - Events arrived out of order and the canonical state wasn't available yet
//
// Pattern from: theroadtoenterprise.com production webhook architecture.
type StripeReplayWorker struct {
	db     *pgxpool.Pool
	stripe *stripeClient.Client
}

// NewStripeReplayWorker constructs the worker.
func NewStripeReplayWorker(db *pgxpool.Pool, stripeCl *stripeClient.Client) *StripeReplayWorker {
	return &StripeReplayWorker{
		db:     db,
		stripe: stripeCl,
	}
}

// Start begins the replay loop. Call cancel() to stop.
func (w *StripeReplayWorker) Start(ctx context.Context) {
	if w.stripe == nil || !w.stripe.IsConfigured() {
		log.Println("[StripeReplayWorker] Disabled — Stripe not configured")
		return
	}

	ticker := time.NewTicker(60 * time.Second) // Check every 60 seconds
	defer ticker.Stop()

	log.Println("[StripeReplayWorker] Started — checking for unprocessed events every 60s")

	for {
		select {
		case <-ctx.Done():
			log.Println("[StripeReplayWorker] Stopped")
			return
		case <-ticker.C:
			w.processUnprocessedEvents(ctx)
		}
	}
}

// processUnprocessedEvents finds and reprocesses events where processed_at IS NULL
// and received_at is older than 5 seconds (give the primary handler a chance to finish).
func (w *StripeReplayWorker) processUnprocessedEvents(ctx context.Context) {
	rows, err := w.db.Query(ctx,
		`SELECT id, stripe_event_id, event_type, payload, order_id, payment_intent_id
		 FROM stripe_events
		 WHERE processed_at IS NULL
		   AND process_error IS NULL
		   AND received_at < NOW() - INTERVAL '5 seconds'
		 ORDER BY received_at ASC
		 LIMIT 10`,
	)
	if err != nil {
		log.Printf("[StripeReplayWorker] Query error: %v", err)
		return
	}
	defer rows.Close()

	processed := 0
	for rows.Next() {
		var id, stripeEventID, eventType string
		var payload []byte
		var orderID, paymentIntentID *string

		if err := rows.Scan(&id, &stripeEventID, &eventType, &payload, &orderID, &paymentIntentID); err != nil {
			log.Printf("[StripeReplayWorker] Scan error: %v", err)
			continue
		}

		// Parse the event
		var event stripeSDK.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Printf("[StripeReplayWorker] Failed to parse event %s: %v", stripeEventID, err)
			w.markError(ctx, id, fmt.Sprintf("payload parse error: %v", err))
			continue
		}

		// Attempt reprocessing
		processErr := w.reprocessEvent(ctx, event, id, orEmpty(orderID), orEmpty(paymentIntentID))
		if processErr != nil {
			log.Printf("[StripeReplayWorker] Replay failed for %s: %v", stripeEventID, processErr)
			w.markError(ctx, id, processErr.Error())
			continue
		}

		// Mark as processed
		_, _ = w.db.Exec(ctx,
			`UPDATE stripe_events SET processed_at = NOW() WHERE id = $1`,
			id,
		)
		processed++
	}

	if processed > 0 {
		log.Printf("[StripeReplayWorker] Reprocessed %d unprocessed events", processed)
	}
}

// reprocessEvent handles a single event replay.
func (w *StripeReplayWorker) reprocessEvent(ctx context.Context, event stripeSDK.Event, eventRowID, orderID, paymentIntentID string) error {
	switch event.Type {
	case "payment_intent.succeeded":
		return w.replayPaymentSucceeded(ctx, event, eventRowID, orderID, paymentIntentID)
	case "payment_intent.payment_failed":
		return w.replayPaymentFailed(ctx, event, orderID, paymentIntentID)
	case "charge.refunded":
		return w.replayChargeRefunded(ctx, event, orderID, paymentIntentID)
	default:
		return nil
	}
}

func (w *StripeReplayWorker) replayPaymentSucceeded(ctx context.Context, event stripeSDK.Event, eventRowID, orderID, paymentIntentID string) error {
	// Refetch canonical state from Stripe
	pi, err := stripeClient.ParsePaymentIntent(event)
	if err != nil {
		return err
	}
	if orderID == "" {
		orderID = pi.Metadata["order_id"]
	}
	if orderID == "" {
		return nil
	}

	// Mark order as paid (idempotent)
	result, err := w.db.Exec(ctx,
		`UPDATE orders SET status = 'paid', payment_status = 'paid', updated_at = NOW()
		 WHERE order_tracking_id = $1 AND status != 'paid'`,
		orderID,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return nil // Already paid
	}

	// FIX H10: Create payment transaction + outbox event for SettlementWorker
	// so the 3-way ledger split + escrow hold is created. Previously, the
	// replay worker skipped this step, leaving vendors unpaid on replayed events.
	amountPKR := float64(pi.Amount) / 100.0 // Stripe amounts are in paisa/cents
	_, _ = w.db.Exec(ctx, `
		INSERT INTO payment_transactions (id, order_tracking_id, gateway, gateway_txn_id, amount, currency, status, kind, idempotency_key, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'stripe', $2, $3, 'PKR', 'settlement_pending', 'payment', $4, NOW(), NOW())
		ON CONFLICT (idempotency_key) DO NOTHING
	`, orderID, pi.ID, amountPKR, fmt.Sprintf("stripe_replay:%s", pi.ID))

	eventPayload := fmt.Sprintf(`{"order_id":"%s","gateway":"stripe","gateway_txn_id":"%s","amount":%.2f}`, orderID, pi.ID, amountPKR)
	_, _ = w.db.Exec(ctx, `
		INSERT INTO outbox_events (id, topic, key, payload, status, created_at, updated_at)
		VALUES (gen_random_uuid(), 'payment_settlement', $1, $2, 'PENDING', NOW(), NOW())
		ON CONFLICT (idempotency_key) DO NOTHING
	`, orderID, eventPayload, fmt.Sprintf("stripe_settle:%s", orderID))

	// Update payment_transactions
	_, _ = w.db.Exec(ctx,
		`UPDATE payment_transactions SET status = 'completed', updated_at = NOW() WHERE gateway_txn_id = $1`,
		pi.ID,
	)

	log.Printf("[StripeReplayWorker] payment_intent.succeeded replayed: order=%s pi=%s", orderID, pi.ID)
	return nil
}

func (w *StripeReplayWorker) replayPaymentFailed(ctx context.Context, event stripeSDK.Event, orderID, paymentIntentID string) error {
	pi, err := stripeClient.ParsePaymentIntent(event)
	if err != nil {
		return err
	}
	if orderID == "" {
		orderID = pi.Metadata["order_id"]
	}
	if orderID == "" {
		return nil
	}

	_, _ = w.db.Exec(ctx,
		`UPDATE payment_transactions SET status = 'failed', updated_at = NOW() WHERE gateway_txn_id = $1`,
		pi.ID,
	)
	_, _ = w.db.Exec(ctx,
		`UPDATE orders SET payment_status = 'failed', updated_at = NOW() WHERE order_tracking_id = $1`,
		orderID,
	)

	log.Printf("[StripeReplayWorker] payment_intent.payment_failed replayed: order=%s pi=%s", orderID, pi.ID)
	return nil
}

func (w *StripeReplayWorker) replayChargeRefunded(ctx context.Context, event stripeSDK.Event, orderID, paymentIntentID string) error {
	charge, err := stripeClient.ParseCharge(event)
	if err != nil {
		return err
	}
	if orderID == "" {
		orderID = charge.Metadata["order_id"]
	}
	if orderID == "" {
		return nil
	}

	_, _ = w.db.Exec(ctx,
		`UPDATE orders SET status = 'refunded', payment_status = 'refunded', updated_at = NOW()
		 WHERE order_tracking_id = $1 AND status != 'refunded'`,
		orderID,
	)

	log.Printf("[StripeReplayWorker] charge.refunded replayed: order=%s", orderID)
	return nil
}

func (w *StripeReplayWorker) markError(ctx context.Context, eventRowID, errMsg string) {
	_, _ = w.db.Exec(ctx,
		`UPDATE stripe_events SET process_error = $1, processed_at = NOW() WHERE id = $2`,
		errMsg, eventRowID,
	)
}

func orEmpty(s *string) string {
	if s != nil {
		return *s
	}
	return ""
}
