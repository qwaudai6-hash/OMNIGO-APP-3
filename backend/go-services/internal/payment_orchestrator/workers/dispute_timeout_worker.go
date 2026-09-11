package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

// DisputeTimeoutWorker auto-resolves disputes after 48 hours (customer wins).
type DisputeTimeoutWorker struct {
	db      *pgxpool.Pool
	escrow  *escrow.Service
	kafka   *messaging.KafkaClient
	redis   redis.UniversalClient
}

func NewDisputeTimeoutWorker(
	db *pgxpool.Pool,
	escrowSvc *escrow.Service,
	kafkaClient *messaging.KafkaClient,
	rdb redis.UniversalClient,
) *DisputeTimeoutWorker {
	return &DisputeTimeoutWorker{
		db:     db,
		escrow: escrowSvc,
		kafka:  kafkaClient,
		redis:  rdb,
	}
}

func (w *DisputeTimeoutWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	log.Println("[DISPUTE-TIMEOUT] Worker started, checking every 5 minutes")

	for {
		select {
		case <-ctx.Done():
			log.Println("[DISPUTE-TIMEOUT] Worker stopped")
			return
		case <-ticker.C:
			w.processExpiredDisputes(ctx)
		}
	}
}

func (w *DisputeTimeoutWorker) processExpiredDisputes(ctx context.Context) {
	// Find disputes that are open and older than 48 hours
	rows, err := w.db.Query(ctx,
		`SELECT d.id, d.order_tracking_id, d.filed_by, r.customer_tracking_id, e.amount
		 FROM disputes d
		 JOIN return_requests r ON r.order_tracking_id = d.order_tracking_id
		 JOIN escrow_holds e ON e.order_tracking_id = d.order_tracking_id
		 WHERE d.status = 'open'
		   AND d.created_at < NOW() - INTERVAL '48 hours'
		   AND e.status = 'disputed'
		 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		log.Printf("[DISPUTE-TIMEOUT] Error querying expired disputes: %v", err)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var disputeID uuid.UUID
		var orderID, vendorID, customerID string
		var amount int64

		if err := rows.Scan(&disputeID, &orderID, &vendorID, &customerID, &amount); err != nil {
			log.Printf("[DISPUTE-TIMEOUT] Error scanning dispute: %v", err)
			continue
		}

		if err := w.resolveDispute(ctx, disputeID, orderID, customerID, amount); err != nil {
			log.Printf("[DISPUTE-TIMEOUT] Error resolving dispute %s: %v", disputeID, err)
			continue
		}

		count++
	}

	if count > 0 {
		log.Printf("[DISPUTE-TIMEOUT] Auto-resolved %d expired disputes", count)
	}
}

func (w *DisputeTimeoutWorker) resolveDispute(
	ctx context.Context,
	disputeID uuid.UUID,
	orderID, customerID string,
	amount int64,
) error {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Refund to customer wallet via escrow
	if err := w.escrow.RefundForReturn(ctx, orderID, amount); err != nil {
		return fmt.Errorf("failed to refund: %w", err)
	}

	// 2. Update dispute resolution
	_, err = tx.Exec(ctx,
		`UPDATE disputes 
		 SET status = 'resolved', 
		     resolution = 'auto_timeout_customer_wins',
		     resolved_at = NOW(),
		     updated_at = NOW()
		 WHERE id = $1`,
		disputeID,
	)
	if err != nil {
		return fmt.Errorf("failed to update dispute: %w", err)
	}

	// 3. Update return request status
	_, err = tx.Exec(ctx,
		`UPDATE return_requests 
		 SET status = 'return_completed',
		     dispute_resolved_at = NOW(),
		     updated_at = NOW()
		 WHERE order_tracking_id = $1 AND status = 'return_disputed'`,
		orderID,
	)
	if err != nil {
		return fmt.Errorf("failed to update return request: %w", err)
	}

	// 4. Update order status
	_, err = tx.Exec(ctx,
		`UPDATE orders 
		 SET status = 'refunded',
		     payment_status = 'refunded',
		     dispute_status = 'resolved',
		     updated_at = NOW()
		 WHERE order_tracking_id = $1`,
		orderID,
	)
	if err != nil {
		return fmt.Errorf("failed to update order: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// 5. Emit events (after commit)
	w.emitEvent(ctx, "returns.customer_notification", customerID, map[string]interface{}{
		"action":    "RETURN_AUTO_APPROVED",
		"order_id":  orderID,
		"message":   "Your return has been auto-approved after dispute timeout. Refund processed.",
		"timestamp": time.Now().UnixMilli(),
	})

	w.emitEvent(ctx, "admin.dispute_auto_resolved", orderID, map[string]interface{}{
		"action":     "DISPUTE_AUTO_RESOLVED",
		"dispute_id": disputeID.String(),
		"order_id":   orderID,
		"resolution": "auto_timeout_customer_wins",
		"timestamp":  time.Now().UnixMilli(),
	})

	return nil
}

func (w *DisputeTimeoutWorker) emitEvent(ctx context.Context, topic, key string, payload interface{}) {
	if w.kafka == nil || w.kafka.Client == nil {
		return
	}
	eventBytes, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[DISPUTE-TIMEOUT] Failed to marshal event: %v", err)
		return
	}
	record := &kgo.Record{
		Topic: topic,
		Key:   []byte(key),
		Value: eventBytes,
	}
	w.kafka.Client.Produce(ctx, record, func(_ *kgo.Record, err error) {
		if err != nil {
			log.Printf("[DISPUTE-TIMEOUT] Failed to produce event to %s: %v", topic, err)
		}
	})
}
