package service

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OrderTimeoutWorker periodically cancels orders stuck in 'pending' state
// for more than 30 minutes (abandoned orders). Without this, customers who
// close the app before completing payment leave zombie orders that:
//   - Keep stock reserved indefinitely
//   - Have delivery gigs broadcasting to riders
//   - Corrupt order analytics
//
// Pattern: Production e-commerce systems always have abandoned cart/order cleanup.
// Reference: hopstack.io + commercetools.com order lifecycle best practices.
type OrderTimeoutWorker struct {
	db         *pgxpool.Pool
	timeout    time.Duration
	escrowSvc  EscrowHolder
}

// NewOrderTimeoutWorker constructs the worker with a 30-minute default timeout.
func NewOrderTimeoutWorker(db *pgxpool.Pool) *OrderTimeoutWorker {
	return &OrderTimeoutWorker{
		db:      db,
		timeout: 30 * time.Minute,
	}
}

// SetEscrowService injects the optional escrow service for cancel-time cleanup.
func (w *OrderTimeoutWorker) SetEscrowService(svc EscrowHolder) {
	w.escrowSvc = svc
}

// Start begins the cleanup loop. Call cancel() to stop.
func (w *OrderTimeoutWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second) // Check every 60 seconds
	defer ticker.Stop()

	log.Printf("[OrderTimeout] Started — cancelling orders pending > %v", w.timeout)

	for {
		select {
		case <-ctx.Done():
			log.Println("[OrderTimeout] Stopped")
			return
		case <-ticker.C:
			w.cancelStaleOrders(ctx)
		}
	}
}

// cancelStaleOrders finds and cancels orders stuck in pending state.
func (w *OrderTimeoutWorker) cancelStaleOrders(ctx context.Context) {
	// Find orders stuck in pending for more than 30 minutes
	rows, err := w.db.Query(ctx,
		`SELECT order_tracking_id, COALESCE(payment_gateway, ''), total_amount
		 FROM orders
		 WHERE status = 'pending'
		   AND created_at < NOW() - INTERVAL '30 minutes'
		 FOR UPDATE SKIP LOCKED
		 LIMIT 50`,
	)
	if err != nil {
		log.Printf("[OrderTimeout] Query error: %v", err)
		return
	}
	defer rows.Close()

	var staleOrders []struct {
		OrderID    string
		Gateway    string
		TotalAmount float64
	}

	for rows.Next() {
		var o struct {
			OrderID     string
			Gateway     string
			TotalAmount float64
		}
		if err := rows.Scan(&o.OrderID, &o.Gateway, &o.TotalAmount); err != nil {
			log.Printf("[OrderTimeout] Scan error: %v", err)
			continue
		}
		staleOrders = append(staleOrders, o)
	}
	rows.Close()

	if len(staleOrders) == 0 {
		return
	}

	cancelled := 0
	for _, order := range staleOrders {
		if err := w.cancelOrder(ctx, order.OrderID); err != nil {
			log.Printf("[OrderTimeout] Failed to cancel order %s: %v", order.OrderID, err)
			continue
		}
		cancelled++
		log.Printf("[OrderTimeout] Cancelled stale order %s (gateway=%s, amount=%.2f, pending > %v)",
			order.OrderID, order.Gateway, order.TotalAmount, w.timeout)
	}

	if cancelled > 0 {
		log.Printf("[OrderTimeout] Cancelled %d stale pending orders", cancelled)
	}
}

// cancelOrder performs the full cancellation: order + delivery gig + stock release + escrow + COD debts.
func (w *OrderTimeoutWorker) cancelOrder(ctx context.Context, orderID string) error {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 1. Cancel the order (idempotent: only if still pending)
	result, err := tx.Exec(ctx,
		`UPDATE orders SET status = 'cancelled', payment_status = 'failed', updated_at = NOW()
		 WHERE order_tracking_id = $1 AND status = 'pending'`,
		orderID,
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return nil // Already cancelled or moved to another state
	}

	// 2. Cancel any active delivery gig
	_, _ = tx.Exec(ctx,
		`UPDATE deliveries SET status = 'cancelled', updated_at = NOW()
		 WHERE order_tracking_id = $1 AND status NOT IN ('completed', 'cancelled')`,
		orderID,
	)

	// 3. Cancel any COD debts for this order
	_, _ = tx.Exec(ctx,
		`UPDATE cod_debts SET status = 'cancelled', updated_at = NOW()
		 WHERE order_tracking_id = $1 AND status != 'cancelled'`,
		orderID,
	)

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// 4. Cancel escrow hold (outside DB tx — escrow service uses its own connection)
	if w.escrowSvc != nil {
		if err := w.escrowSvc.CancelForOrder(ctx, orderID); err != nil {
			log.Printf("[OrderTimeout] WARNING: escrow cancel failed for order %s: %v (order was cancelled in DB)", orderID, err)
		}
	}

	return nil
}

// StartOrderTimeoutWorker is a convenience method on OrderService that
// creates and starts the timeout worker using the service's own DB pool.
func (s *OrderService) StartOrderTimeoutWorker(ctx context.Context) {
	w := NewOrderTimeoutWorker(s.repo.DB())
	w.SetEscrowService(s.escrowService)
	w.Start(ctx)
}
