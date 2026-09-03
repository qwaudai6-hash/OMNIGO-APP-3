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
// Hybrid approach: Redis sorted set for fast candidate lookup (O(log N)),
// Postgres FOR UPDATE SKIP LOCKED for safe concurrent claim.
func (s *Service) ReleaseExpiredHolds(ctx context.Context) (int, error) {
	released := 0

	// Try Redis index first for fast candidate lookup
	var candidateIDs []string
	if s.index != nil {
		ids, err := s.index.ClaimExpired(ctx, 50)
		if err == nil && len(ids) > 0 {
			candidateIDs = ids
		}
	}

	// If Redis has candidates, use them; otherwise fall back to full PG scan
	if len(candidateIDs) > 0 {
		for _, idStr := range candidateIDs {
			holdID, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			if err := s.releaseOneHold(ctx, holdID); err != nil {
				fmt.Printf("[Escrow] Failed to release hold %s: %v\n", holdID, err)
				continue
			}
			released++
		}
	} else {
		// Fallback: direct Postgres scan (handles Redis down or empty index)
		for {
			tx, err := s.db.Begin(ctx)
			if err != nil {
				fmt.Printf("[Escrow] Failed to begin transaction: %v\n", err)
				return released, err
			}

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
				if errors.Is(err, pgx.ErrNoRows) {
					break
				}
				fmt.Printf("[Escrow] Failed to claim hold: %v\n", err)
				return released, err
			}

			if err := s.processHoldTx(ctx, tx, holdID, orderID, vendorID, amount); err != nil {
				fmt.Printf("[Escrow] Failed to process hold %s: %v\n", holdID, err)
				continue
			}
			released++
		}
	}

	return released, nil
}

// releaseOneHold releases a single hold by ID (used with Redis index candidates).
func (s *Service) releaseOneHold(ctx context.Context, holdID uuid.UUID) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var orderID, vendorID string
	var amount float64
	err = tx.QueryRow(ctx, `
		UPDATE escrow_holds
		SET status = 'releasing', updated_at = NOW()
		WHERE id = $1 AND status = 'held' AND hold_until <= NOW()
		RETURNING order_tracking_id, vendor_tracking_id, amount
	`, holdID).Scan(&orderID, &vendorID, &amount)
	if err != nil {
		return err
	}

	return s.processHoldTx(ctx, tx, holdID, orderID, vendorID, amount)
}

// processHoldTx handles the common hold processing logic (checks + transfer + commit).
func (s *Service) processHoldTx(ctx context.Context, tx pgx.Tx, holdID uuid.UUID, orderID, vendorID string, amount float64) error {
	// Fail-closed check: re-verify escrow_released flag
	var alreadyReleased bool
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(escrow_released, FALSE) FROM orders WHERE order_tracking_id = $1`,
		orderID,
	).Scan(&alreadyReleased)
	if err != nil {
		_ = tx.Rollback(ctx)
		fmt.Printf("[Escrow] Failed to check alreadyReleased for order %s: %v — skipping\n", orderID, err)
		return nil
	}
	if alreadyReleased {
		_, _ = s.db.Exec(ctx, `UPDATE escrow_holds SET status = 'released', released_at = NOW() WHERE id = $1`, holdID)
		_ = tx.Rollback(ctx)
		return nil
	}

	// Check for open disputes
	var hasDispute bool
	err = tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM disputes WHERE order_tracking_id = $1 AND status IN ('open', 'investigating'))`,
		orderID,
	).Scan(&hasDispute)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil
	}
	if hasDispute {
		_, err := tx.Exec(ctx, `UPDATE escrow_holds SET status = 'held', updated_at = NOW() WHERE id = $1`, holdID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return nil
		}
		if err := tx.Commit(ctx); err != nil {
			return nil
		}
		fmt.Printf("[Escrow] Skipping release for order %s — open dispute exists\n", orderID)
		return nil
	}

	// Execute ledger transfer: vendor_locked_escrow → vendor_withdrawable
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
		return fmt.Errorf("ledger transfer failed: %w", err)
	}

	// Mark hold as released
	_, err = tx.Exec(ctx, `UPDATE escrow_holds SET status = 'released', released_at = NOW() WHERE id = $1`, holdID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("failed to mark hold released: %w", err)
	}

	// Mark order as released
	_, err = tx.Exec(ctx,
		`UPDATE orders SET escrow_released = TRUE WHERE order_tracking_id = $1 AND escrow_released = FALSE`,
		orderID,
	)
	if err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("failed to mark escrow_released: %w", err)
	}

	// Credit vendor wallet
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
		return fmt.Errorf("failed to credit vendor_wallet: %w", err)
	}

	// Commit
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("transaction commit failed: %w", err)
	}

	// Remove from Redis index after successful release
	_ = s.index.Remove(ctx, holdID.String())

	fmt.Printf("[Escrow] Released %.2f PKR for vendor %s (order %s)\n", amount, vendorID, orderID)
	return nil
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
	err := s.repo.UnfreezeOnDisputeRejection(ctx, disputeID)
	if err == nil {
		s.reAddHoldsToIndexByDispute(ctx, disputeID)
	}
	return err
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

// reAddHoldsToIndexByDispute re-adds holds to the Redis index after dispute rejection.
func (s *Service) reAddHoldsToIndexByDispute(ctx context.Context, disputeID uuid.UUID) {
	rows, err := s.db.Query(ctx,
		`SELECT id, hold_until FROM escrow_holds WHERE dispute_id = $1 AND status = 'held'`,
		disputeID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var holdUntil time.Time
		if rows.Scan(&id, &holdUntil) == nil {
			_ = s.index.Add(ctx, id.String(), holdUntil)
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
