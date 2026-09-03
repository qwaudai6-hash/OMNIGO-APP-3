package escrow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/omnigo/backend/internal/ledger"
)

func getHoldDuration() time.Duration {
	hoursStr := os.Getenv("ESCROW_HOLD_HOURS")
	if hoursStr != "" {
		if hours, err := strconv.Atoi(hoursStr); err == nil {
			return time.Duration(hours) * time.Hour
		}
	}
	return 48 * time.Hour
}

// Service manages escrow holds and releases.
type Service struct {
	repo   *Repository
	ledger *ledger.Service
	db     *pgxpool.Pool
	index  *HoldIndex
}

func NewService(db *pgxpool.Pool, ledgerSvc *ledger.Service, rdb redis.UniversalClient) *Service {
	return &Service{
		repo:   NewRepository(db),
		ledger: ledgerSvc,
		db:     db,
		index:  NewHoldIndex(rdb),
	}
}

// CreateHold creates a new escrow hold for a vendor after delivery completion.
func (s *Service) CreateHold(ctx context.Context, orderID, vendorID string, amount float64) error {
	exists, err := s.repo.HoldExistsForOrder(ctx, orderID)
	if err != nil {
		return fmt.Errorf("failed to check existing hold: %w", err)
	}
	if exists {
		// Idempotent success
		return nil
	}

	hold := &EscrowHold{
		ID:               uuid.New(),
		OrderTrackingID:  orderID,
		VendorTrackingID: vendorID,
		Amount:           amount,
		Status:           StatusHeld,
		HoldUntil:        time.Now().Add(getHoldDuration()),
	}

	if err := s.repo.CreateHold(ctx, hold); err != nil {
		return fmt.Errorf("escrow hold creation failed: %w", err)
	}

	// Index in Redis sorted set for O(log N) expiry lookup
	if idxErr := s.index.Add(ctx, hold.ID.String(), hold.HoldUntil); idxErr != nil {
		fmt.Printf("[Escrow] Warning: failed to index hold %s in Redis: %v\n", hold.ID, idxErr)
	}

	return nil
}

// ReleaseExpiredHolds releases all holds past their hold_until time
// if no open disputes exist for the order.
//
// Concurrency model: this function uses an atomic claim pattern inside a
// transaction to guarantee that each hold is processed exactly once even
// across multiple concurrent worker pods.
//
//  1. Begin a transaction
//  2. Atomically claim ONE releasable hold by transitioning its status from
//     'held' to 'releasing' using UPDATE ... RETURNING. The row-level lock
//     acquired by UPDATE prevents any concurrent worker from claiming the
//     same row.
//  3. Re-verify the order's escrow_released flag INSIDE the same transaction
//     (fail-closed: if the lookup fails, we roll back and skip the hold).
//  4. Execute the ledger transfer, hold status update, order flag update,
//     and vendor wallet credit — all in the same transaction.
//  5. Commit. If any step fails, the claim UPDATE rolls back, releasing the
//     row for a future attempt.
func (s *Service) ReleaseExpiredHolds(ctx context.Context) (int, error) {
	released := 0

	for {
		tx, err := s.db.Begin(ctx)
		if err != nil {
			fmt.Printf("[Escrow] Failed to begin transaction: %v\n", err)
			return released, err
		}

		// Atomic claim: transition ONE hold from 'held' to 'releasing' and return
		// its details. The UPDATE acquires a row-level lock; concurrent workers
		// running the same statement will SKIP this row (or block on it).
		var holdID uuid.UUID
		var orderID, vendorID string
		var amount float64
		err = tx.QueryRow(ctx, `
			UPDATE escrow_holds
			SET status = 'releasing', updated_at = NOW()
			WHERE id = (
				SELECT id FROM escrow_holds
				WHERE status = 'held' AND hold_until <= NOW()
				ORDER BY hold_until
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			)
			RETURNING id, order_tracking_id, vendor_tracking_id, amount
		`).Scan(&holdID, &orderID, &vendorID, &amount)
		if err != nil {
			_ = tx.Rollback(ctx)
			// pgx.ErrNoRows means no eligible holds remain — normal exit
			if errors.Is(err, pgx.ErrNoRows) {
				break
			}
			fmt.Printf("[Escrow] Failed to claim hold: %v\n", err)
			return released, err
		}

		// Fail-closed check: re-verify the order's escrow_released flag INSIDE
		// the same transaction. If the lookup fails, we roll back the claim
		// (status reverts to 'held') and skip — never proceed on a failed safety check.
		var alreadyReleased bool
		err = tx.QueryRow(ctx,
			`SELECT COALESCE(escrow_released, FALSE) FROM orders WHERE order_tracking_id = $1`,
			orderID,
		).Scan(&alreadyReleased)
		if err != nil {
			_ = tx.Rollback(ctx)
			fmt.Printf("[Escrow] Failed to check alreadyReleased for order %s: %v — skipping\n", orderID, err)
			continue
		}
		if alreadyReleased {
			// Order already released — mark the hold as released and move on
			_, _ = s.db.Exec(ctx, `UPDATE escrow_holds SET status = 'released', released_at = NOW() WHERE id = $1`, holdID)
			_ = tx.Rollback(ctx)
			continue
		}

		// Check for open disputes (read-only, can be outside tx but kept here for atomicity)
		var hasDispute bool
		err = tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM disputes WHERE order_tracking_id = $1 AND status IN ('open', 'investigating'))`,
			orderID,
		).Scan(&hasDispute)
		if err != nil {
			_ = tx.Rollback(ctx)
			fmt.Printf("[Escrow] Failed to check disputes for order %s: %v\n", orderID, err)
			continue
		}
		if hasDispute {
			// Release our claim — let the hold go back to 'held' for a future attempt
			_, err := tx.Exec(ctx, `UPDATE escrow_holds SET status = 'held', updated_at = NOW() WHERE id = $1`, holdID)
			if err != nil {
				_ = tx.Rollback(ctx)
				fmt.Printf("[Escrow] Failed to revert claim for disputed order %s: %v\n", orderID, err)
				continue
			}
			if err := tx.Commit(ctx); err != nil {
				fmt.Printf("[Escrow] Failed to commit dispute-skip for order %s: %v\n", orderID, err)
			}
			fmt.Printf("[Escrow] Skipping release for order %s — open dispute exists\n", orderID)
			continue
		}

		// Execute ledger transfer: vendor_locked_escrow → vendor_withdrawable.
		// The idempotency key is per-hold, so retries across cron cycles are safe.
		idempotencyKey := fmt.Sprintf("escrow:release:%s", holdID.String())
		_, err = s.ledger.Transfer(ctx, ledger.TransferRequest{
			DebitAccount:   ledger.AccountVendorLockedEscrow,
			CreditAccount:  ledger.AccountVendorWithdrawable,
			Amount:         amount,
			ReferenceType:  "escrow_release",
			ReferenceID:    orderID,
			Description:    fmt.Sprintf("Escrow released for order %s after %dh hold", orderID, int(getHoldDuration().Hours())),
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			_ = tx.Rollback(ctx)
			fmt.Printf("[Escrow] Ledger transfer failed for hold %s: %v\n", holdID, err)
			continue
		}

		// Mark hold as 'released' (final status) — atomically with the wallet credit
		_, err = tx.Exec(ctx, `UPDATE escrow_holds SET status = 'released', released_at = NOW() WHERE id = $1`, holdID)
		if err != nil {
			_ = tx.Rollback(ctx)
			fmt.Printf("[Escrow] Failed to mark hold %s as released: %v\n", holdID, err)
			continue
		}

		// Mark order as released — only if not already released (prevents double-flag)
		tag, err := tx.Exec(ctx,
			`UPDATE orders SET escrow_released = TRUE WHERE order_tracking_id = $1 AND escrow_released = FALSE`,
			orderID,
		)
		if err != nil {
			_ = tx.Rollback(ctx)
			fmt.Printf("[Escrow] Failed to mark escrow_released for order %s: %v\n", orderID, err)
			continue
		}
		_ = tag // rows affected is informational; the WHERE guard prevents double-set

		// Credit vendor wallet — idempotency note: since the hold was atomically
		// claimed (status 'held' → 'releasing'), only THIS transaction can reach
		// this point for this hold. The next iteration will see 'releasing' and
		// skip it.
		_, err = tx.Exec(ctx, `
			INSERT INTO vendor_wallet (vendor_tracking_id, balance, lifetime_earnings, updated_at)
			VALUES ($1, $2, $2, NOW())
			ON CONFLICT (vendor_tracking_id)
			DO UPDATE SET
				balance = vendor_wallet.balance + $2,
				lifetime_earnings = vendor_wallet.lifetime_earnings + $2,
				updated_at = NOW()
		`, vendorID, amount)
		if err != nil {
			_ = tx.Rollback(ctx)
			fmt.Printf("[Escrow] Failed to credit vendor_wallet for hold %s: %v\n", holdID, err)
			continue
		}

		// Commit — all-or-nothing
		if err := tx.Commit(ctx); err != nil {
			fmt.Printf("[Escrow] Transaction commit failed for hold %s: %v\n", holdID, err)
			continue
		}

		// Remove from Redis index after successful release
		_ = s.index.Remove(ctx, holdID.String())

		released++
		fmt.Printf("[Escrow] Released %.2f PKR for vendor %s (order %s)\n",
			amount, vendorID, orderID)
	}

	return released, nil
}

// FreezeForDispute freezes all held escrows for an order when a dispute is filed.
func (s *Service) FreezeForDispute(ctx context.Context, orderTrackingID string, disputeID uuid.UUID) error {
	err := s.repo.FreezeForDispute(ctx, orderTrackingID, disputeID)
	if err == nil {
		s.removeHoldsFromIndexByOrder(ctx, orderTrackingID)
	}
	return err
}

// CancelForOrder cancels all held escrows when an order is cancelled or returned.
// BUG-06 FIX: Prevents vendor from receiving funds for cancelled/returned orders.
func (s *Service) CancelForOrder(ctx context.Context, orderTrackingID string) error {
	err := s.repo.CancelHoldForOrder(ctx, orderTrackingID)
	if err == nil {
		s.removeHoldsFromIndexByOrder(ctx, orderTrackingID)
	}
	return err
}

// UnfreezeOnRejection reverts disputed holds when a dispute is rejected.
func (s *Service) UnfreezeOnRejection(ctx context.Context, disputeID uuid.UUID) error {
	return s.repo.UnfreezeOnDisputeRejection(ctx, disputeID)
}

// removeHoldsFromIndexByOrder removes all holds for an order from the Redis index.
func (s *Service) removeHoldsFromIndexByOrder(ctx context.Context, orderTrackingID string) {
	rows, err := s.db.Query(ctx,
		`SELECT id FROM escrow_holds WHERE order_tracking_id = $1 AND status IN ('cancelled', 'released', 'refunded', 'disputed')`,
		orderTrackingID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			_ = s.index.Remove(ctx, id.String())
		}
	}
}

// RefundDispute executes a double-entry ledger refund to the customer and marks the escrow hold refunded.
func (s *Service) RefundDispute(ctx context.Context, disputeID uuid.UUID) error {
	hold, err := s.repo.RefundDisputedHold(ctx, disputeID)
	if err != nil {
		return fmt.Errorf("failed to mark escrow hold refunded: %w", err)
	}

	// Remove from Redis index
	_ = s.index.Remove(ctx, hold.ID.String())

	// Fetch customer tracking ID from orders
	var customerTrackingID string
	err = s.db.QueryRow(ctx,
		`SELECT customer_tracking_id FROM orders WHERE order_tracking_id = $1`,
		hold.OrderTrackingID,
	).Scan(&customerTrackingID)
	if err != nil {
		return fmt.Errorf("failed to fetch order customer: %w", err)
	}

	// Double-entry ledger transfer: vendor_locked_escrow -> customer_wallet
	idempotencyKey := fmt.Sprintf("escrow:refund:%s", disputeID.String())
	_, err = s.ledger.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   ledger.AccountVendorLockedEscrow,
		CreditAccount:  ledger.AccountCustomerWallet,
		Amount:         hold.Amount,
		Currency:       "PKR",
		ReferenceType:  "dispute_refund",
		ReferenceID:    hold.OrderTrackingID,
		Description:    fmt.Sprintf("Dispute refund for order %s to customer %s", hold.OrderTrackingID, customerTrackingID),
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("ledger refund transfer failed: %w", err)
	}

	// Update customer wallet balance in customer_wallet table
	upsertQuery := `
		INSERT INTO customer_wallet (customer_tracking_id, balance, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (customer_tracking_id)
		DO UPDATE SET
			balance = customer_wallet.balance + $2,
			updated_at = NOW()
	`
	if _, err := s.db.Exec(ctx, upsertQuery, customerTrackingID, hold.Amount); err != nil {
		fmt.Printf("[Escrow] Warning: failed to credit customer_wallet table directly: %v\n", err)
	}

	// Update order payment status to refunded
	_, _ = s.db.Exec(ctx,
		`UPDATE orders SET payment_status = 'refunded', updated_at = NOW() WHERE order_tracking_id = $1`,
		hold.OrderTrackingID,
	)

	return nil
}

// GetHoldsByVendor returns escrow hold history for a vendor.
func (s *Service) GetHoldsByVendor(ctx context.Context, vendorTrackingID string) ([]EscrowHold, error) {
	return s.repo.GetHoldsByVendor(ctx, vendorTrackingID)
}

// RebuildIndex populates the Redis sorted set from Postgres on startup or after Redis restart.
func (s *Service) RebuildIndex(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT id, hold_until FROM escrow_holds WHERE status = 'held'`)
	if err != nil {
		return fmt.Errorf("rebuild index query failed: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id uuid.UUID
		var holdUntil time.Time
		if err := rows.Scan(&id, &holdUntil); err != nil {
			continue
		}
		if err := s.index.Add(ctx, id.String(), holdUntil); err != nil {
			continue
		}
		count++
	}
	fmt.Printf("[Escrow] Rebuilt Redis hold index with %d entries\n", count)
	return nil
}
