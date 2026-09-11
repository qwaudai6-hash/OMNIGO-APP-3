package escrow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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
func (s *Service) CreateHold(ctx context.Context, orderID, vendorID string, amount int64) error {
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

// CreateHoldTx creates a new escrow hold inside an existing transaction.
// Used when the caller needs atomic DB operations with the hold creation.
func (s *Service) CreateHoldTx(ctx context.Context, tx pgx.Tx, orderID, vendorID string, amount int64) error {
	exists, err := s.repo.HoldExistsForOrderTx(ctx, tx, orderID)
	if err != nil {
		return fmt.Errorf("failed to check existing hold: %w", err)
	}
	if exists {
		return nil
	}

	hold := &EscrowHold{
		ID:                uuid.New(),
		OrderTrackingID:   orderID,
		VendorTrackingID:  vendorID,
		Amount:            amount,
		Status:            StatusHeld,
		HoldUntil:         time.Now().Add(getHoldDuration()),
	}

	if err := s.repo.CreateHoldInTx(ctx, tx, hold); err != nil {
		return fmt.Errorf("escrow hold creation failed: %w", err)
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
			var amount int64
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
				RETURNING id, order_tracking_id, vendor_tracking_id, amount_paisa
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
	var amount int64
	err = tx.QueryRow(ctx, `
		UPDATE escrow_holds
		SET status = 'releasing', updated_at = NOW()
		WHERE id = $1 AND status = 'held' AND hold_until <= NOW()
		RETURNING order_tracking_id, vendor_tracking_id, amount_paisa
	`, holdID).Scan(&orderID, &vendorID, &amount)
	if err != nil {
		return err
	}

	return s.processHoldTx(ctx, tx, holdID, orderID, vendorID, amount)
}

// processHoldTx handles the common hold processing logic (checks + transfer + commit).
func (s *Service) processHoldTx(ctx context.Context, tx pgx.Tx, holdID uuid.UUID, orderID, vendorID string, amount int64) error {
	// Fail-closed check: re-verify escrow_released flag and order payment state
	// FINANCIAL-AUDIT FIX #6: Also read orders.status to guard against releasing
	// escrow for orders that were never delivered.
	var alreadyReleased bool
	var paymentGateway, paymentStatus, disputeStatus, orderStatus string
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(escrow_released, FALSE),
		        COALESCE(payment_gateway, ''),
		        COALESCE(payment_status, ''),
		        COALESCE(dispute_status, 'NONE'),
		        COALESCE(status, '')
		 FROM orders WHERE order_tracking_id = $1`,
		orderID,
	).Scan(&alreadyReleased, &paymentGateway, &paymentStatus, &disputeStatus, &orderStatus)
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

	// 1. Check for open disputes in disputes table or on order row
	var hasDispute bool
	err = tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM disputes WHERE order_tracking_id = $1 AND status IN ('open', 'investigating', 'pending', 'under_review'))`,
		orderID,
	).Scan(&hasDispute)
	if err != nil {
		// FIX [ES-5]: If dispute check fails, we should NOT proceed with release.
		// Rolling back and returning error is the correct fail-closed behavior.
		_ = tx.Rollback(ctx)
		return fmt.Errorf("failed to check disputes for order %s: %w", orderID, err)
	}
	if hasDispute || (disputeStatus != "NONE" && disputeStatus != "" && !strings.EqualFold(disputeStatus, "resolved")) {
		_, err := tx.Exec(ctx, `UPDATE escrow_holds SET status = 'held', updated_at = NOW() WHERE id = $1`, holdID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return nil
		}
		if err := tx.Commit(ctx); err != nil {
			return nil
		}
		fmt.Printf("[Escrow] Skipping release for order %s — open dispute exists (dispute_status=%s)\n", orderID, disputeStatus)
		return nil
	}

	// FINANCIAL-AUDIT FIX #6: Guard — only release escrow for delivered/completed orders.
	// Prevents auto-release for cancelled, returned, or stuck-in-paid orders.
	if !strings.EqualFold(orderStatus, "delivered") && !strings.EqualFold(orderStatus, "completed") {
		_, err := tx.Exec(ctx, `UPDATE escrow_holds SET status = 'held', updated_at = NOW() WHERE id = $1`, holdID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return nil
		}
		if err := tx.Commit(ctx); err != nil {
			return nil
		}
		fmt.Printf("[Escrow] Skipping release for order %s — order status is '%s' (must be 'delivered' or 'completed')\n", orderID, orderStatus)
		return nil
	}

	// 2. SP-GO-14: COD Protection — verify that rider has settled the COD debt
	if strings.EqualFold(paymentGateway, "cod") {
		var codDebtStatus string
		err = tx.QueryRow(ctx,
			`SELECT COALESCE(status, '') FROM cod_debts WHERE order_tracking_id = $1 ORDER BY created_at DESC LIMIT 1`,
			orderID,
		).Scan(&codDebtStatus)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Check if hold is stale (> 72h) — force release
				var holdCreatedAt time.Time
				holdErr := tx.QueryRow(ctx, `SELECT created_at FROM escrow_holds WHERE id = $1`, holdID).Scan(&holdCreatedAt)
				if holdErr == nil && time.Since(holdCreatedAt) > 72*time.Hour {
					fmt.Printf("[Escrow] WARNING: COD hold %s for order %s is stale (%.0fh) — force releasing\n", holdID, orderID, time.Since(holdCreatedAt).Hours())
					// Fall through to release logic below
				} else {
					if _, revertErr := tx.Exec(ctx, `UPDATE escrow_holds SET status = 'held', updated_at = NOW() WHERE id = $1`, holdID); revertErr != nil {
						_ = tx.Rollback(ctx)
						return fmt.Errorf("failed to revert hold for COD order %s: %w", orderID, revertErr)
					}
					_ = tx.Commit(ctx)
					fmt.Printf("[Escrow] Skipping release for COD order %s — no cod_debts record found\n", orderID)
					return nil
				}
			}
			_ = tx.Rollback(ctx)
			return fmt.Errorf("failed to check cod_debts for order %s: %w", orderID, err)
		}
		if !strings.EqualFold(codDebtStatus, "settled") {
			if _, revertErr := tx.Exec(ctx, `UPDATE escrow_holds SET status = 'held', updated_at = NOW() WHERE id = $1`, holdID); revertErr != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("failed to revert hold for unsettled COD order %s: %w", orderID, revertErr)
			}
			_ = tx.Commit(ctx)
			fmt.Printf("[Escrow] Skipping release for COD order %s — COD debt is not settled (status=%s)\n", orderID, codDebtStatus)
			return nil
		}
	} else if paymentStatus != "" && !strings.EqualFold(paymentStatus, "paid") {
		// Non-COD order: must have payment_status == 'paid'
		if _, revertErr := tx.Exec(ctx, `UPDATE escrow_holds SET status = 'held', updated_at = NOW() WHERE id = $1`, holdID); revertErr != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("failed to revert hold for unpaid order %s: %w", orderID, revertErr)
		}
		_ = tx.Commit(ctx)
		fmt.Printf("[Escrow] Skipping release for order %s — payment_status is not paid (%s)\n", orderID, paymentStatus)
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

	// Credit vendor wallet (using paisa columns after H4 migration)
	_, err = tx.Exec(ctx, `
		INSERT INTO vendor_wallet (vendor_tracking_id, balance_paisa, lifetime_earnings_paisa, updated_at)
		VALUES ($1, $2, $2, NOW())
		ON CONFLICT (vendor_tracking_id)
		DO UPDATE SET
			balance_paisa = vendor_wallet.balance_paisa + $2,
			lifetime_earnings_paisa = vendor_wallet.lifetime_earnings_paisa + $2,
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

	fmt.Printf("[Escrow] Released %d paisa for vendor %s (order %s)\n", amount, vendorID, orderID)
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
// FIX [ES-1]: Ledger transfer is done FIRST (idempotent, safe). All DB operations are wrapped in a single
// transaction to ensure atomicity. If any DB operation fails, the entire transaction rolls back.
func (s *Service) RefundDispute(ctx context.Context, disputeID uuid.UUID) error {
	// Fetch hold details first (before any modifications)
	var hold EscrowHold
	err := s.db.QueryRow(ctx,
		`SELECT id, order_tracking_id, vendor_tracking_id, amount, status, hold_until, created_at
		 FROM escrow_holds WHERE dispute_id = $1 AND status = 'disputed'`,
		disputeID,
	).Scan(&hold.ID, &hold.OrderTrackingID, &hold.VendorTrackingID, &hold.Amount, &hold.Status, &hold.HoldUntil, &hold.CreatedAt)
	if err != nil {
		return fmt.Errorf("escrow hold not found or not in disputed status: %w", err)
	}

	// Fetch customer tracking ID from orders
	var customerTrackingID string
	err = s.db.QueryRow(ctx,
		`SELECT customer_tracking_id FROM orders WHERE order_tracking_id = $1`,
		hold.OrderTrackingID,
	).Scan(&customerTrackingID)
	if err != nil {
		return fmt.Errorf("failed to fetch order customer: %w", err)
	}

	// STEP 1: Execute ledger transfer FIRST (idempotent, safe to retry)
	// If this fails, hold stays 'disputed' - customer doesn't get money (correct behavior)
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

	// STEP 2: All DB operations in a single transaction for atomicity
	tx, txErr := s.db.Begin(ctx)
	if txErr != nil {
		return fmt.Errorf("failed to begin transaction for DB updates: %w", txErr)
	}
	defer tx.Rollback(ctx)

	// Mark hold as refunded in DB
	_, err = tx.Exec(ctx,
		`UPDATE escrow_holds SET status = 'refunded', released_at = NOW() WHERE id = $1`,
		hold.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update escrow_holds status: %w", err)
	}

	// Update customer wallet balance in customer_wallet table
	upsertQuery := `
		INSERT INTO customer_wallet (customer_tracking_id, balance_paisa, lifetime_spent_paisa, updated_at)
		VALUES ($1, $2, 0, NOW())
		ON CONFLICT (customer_tracking_id)
		DO UPDATE SET
			balance_paisa = customer_wallet.balance_paisa + $2,
			updated_at = NOW()
	`
	_, err = tx.Exec(ctx, upsertQuery, customerTrackingID, hold.Amount)
	if err != nil {
		return fmt.Errorf("failed to update customer_wallet: %w", err)
	}

	// Update order payment status to refunded
	_, err = tx.Exec(ctx,
		`UPDATE orders SET payment_status = 'refunded', updated_at = NOW() WHERE order_tracking_id = $1`,
		hold.OrderTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to update order payment_status: %w", err)
	}

	// Commit transaction - all DB updates succeed or all fail together
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit DB transaction: %w", err)
	}

	// Remove from Redis index only after successful DB commit
	_ = s.index.Remove(ctx, hold.ID.String())

	return nil
}

// GetHoldsByVendor returns escrow hold history for a vendor.
func (s *Service) GetHoldsByVendor(ctx context.Context, vendorTrackingID string) ([]EscrowHold, error) {
	return s.repo.GetHoldsByVendor(ctx, vendorTrackingID)
}

// CreateReturnHold creates a new escrow hold for return verification.
// The hold prevents auto-release until the return is verified or disputed.
func (s *Service) CreateReturnHold(ctx context.Context, orderID, vendorID string, amount int64, holdUntil time.Time) error {
	exists, err := s.repo.HoldExistsForOrder(ctx, orderID)
	if err != nil {
		return fmt.Errorf("failed to check existing hold: %w", err)
	}
	if exists {
		// Update existing hold to extend hold_until for return verification
		return s.repo.ExtendHoldUntil(ctx, orderID, holdUntil)
	}

	hold := &EscrowHold{
		ID:               uuid.New(),
		OrderTrackingID:  orderID,
		VendorTrackingID: vendorID,
		Amount:           amount,
		Status:           StatusHeld,
		HoldUntil:        holdUntil,
	}

	if err := s.repo.CreateHold(ctx, hold); err != nil {
		return fmt.Errorf("return escrow hold creation failed: %w", err)
	}

	// Index in Redis sorted set for O(log N) expiry lookup
	if idxErr := s.index.Add(ctx, hold.ID.String(), hold.HoldUntil); idxErr != nil {
		fmt.Printf("[Escrow] Warning: failed to index return hold %s in Redis: %v\n", hold.ID, idxErr)
	}

	return nil
}

// RefundForReturn executes a double-entry ledger refund to the customer for a verified return.
// Similar to RefundDispute but for return-verified orders.
// For COD orders where rider hasn't settled (no escrow hold), uses refundCODReturn fallback.
// For orders where escrow was already released to vendor, uses ClawbackFromVendor (Shopify model).
func (s *Service) RefundForReturn(ctx context.Context, orderTrackingID string, amount int64) error {
	// Check if this is a COD order and if escrow was released
	// FIX [H-1]: Use FOR UPDATE to prevent race condition on concurrent returns
	var paymentGateway string
	var escrowReleased bool
	var vendorID string
	err := s.db.QueryRow(ctx,
		`SELECT payment_gateway, COALESCE(escrow_released, FALSE), vendor_tracking_id
		 FROM orders WHERE order_tracking_id = $1 FOR UPDATE`, orderTrackingID,
	).Scan(&paymentGateway, &escrowReleased, &vendorID)
	if err != nil {
		return fmt.Errorf("failed to fetch order details: %w", err)
	}
	isCOD := strings.EqualFold(paymentGateway, "cod")

	// Fetch hold details
	var hold EscrowHold
	err = s.db.QueryRow(ctx,
		`SELECT id, order_tracking_id, vendor_tracking_id, amount, status, hold_until, created_at
		 FROM escrow_holds WHERE order_tracking_id = $1 AND status IN ('held', 'disputed')`,
		orderTrackingID,
	).Scan(&hold.ID, &hold.OrderTrackingID, &hold.VendorTrackingID, &hold.Amount, &hold.Status, &hold.HoldUntil, &hold.CreatedAt)
	if err != nil {
		if escrowReleased {
			// Escrow was released to vendor — clawback from vendor wallet (Shopify model)
			refundAmount := amount
			if refundAmount == 0 {
				// Get amount from order
				var orderAmount int64
				_ = s.db.QueryRow(ctx, `SELECT COALESCE(total_amount_paisa, 0) FROM orders WHERE order_tracking_id = $1`, orderTrackingID).Scan(&orderAmount)
				refundAmount = orderAmount
			}
			return s.ClawbackFromVendor(ctx, orderTrackingID, vendorID, refundAmount)
		}
		if isCOD {
			// COD fallback: no escrow hold exists (rider hasn't settled)
			return s.refundCODReturn(ctx, orderTrackingID, amount)
		}
		return fmt.Errorf("escrow hold not found for order %s: %w", orderTrackingID, err)
	}

	// Fetch customer tracking ID
	var customerTrackingID string
	err = s.db.QueryRow(ctx,
		`SELECT customer_tracking_id FROM orders WHERE order_tracking_id = $1`,
		orderTrackingID,
	).Scan(&customerTrackingID)
	if err != nil {
		return fmt.Errorf("failed to fetch order customer: %w", err)
	}

	// Use the actual hold amount if amount is 0
	refundAmount := amount
	if refundAmount == 0 {
		refundAmount = hold.Amount
	}

	// STEP 1: Execute ledger transfer FIRST (idempotent)
	idempotencyKey := fmt.Sprintf("return:refund:%s", orderTrackingID)
	_, err = s.ledger.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   ledger.AccountVendorLockedEscrow,
		CreditAccount:  ledger.AccountCustomerWallet,
		Amount:         refundAmount,
		Currency:       "PKR",
		ReferenceType:  "return_refund",
		ReferenceID:    orderTrackingID,
		Description:    fmt.Sprintf("Return refund for order %s to customer %s", orderTrackingID, customerTrackingID),
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("ledger return refund transfer failed: %w", err)
	}

	// STEP 2: All DB operations in a single transaction
	tx, txErr := s.db.Begin(ctx)
	if txErr != nil {
		return fmt.Errorf("failed to begin transaction: %w", txErr)
	}
	defer tx.Rollback(ctx)

	// Mark hold as refunded
	_, err = tx.Exec(ctx,
		`UPDATE escrow_holds SET status = 'refunded', released_at = NOW() WHERE id = $1`,
		hold.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update escrow_holds status: %w", err)
	}

	// Update customer wallet balance
	upsertQuery := `
		INSERT INTO customer_wallet (customer_tracking_id, balance_paisa, lifetime_spent_paisa, updated_at)
		VALUES ($1, $2, 0, NOW())
		ON CONFLICT (customer_tracking_id)
		DO UPDATE SET
			balance_paisa = customer_wallet.balance_paisa + $2,
			updated_at = NOW()
	`
	_, err = tx.Exec(ctx, upsertQuery, customerTrackingID, refundAmount)
	if err != nil {
		return fmt.Errorf("failed to update customer_wallet: %w", err)
	}

	// Update order payment status
	_, err = tx.Exec(ctx,
		`UPDATE orders SET payment_status = 'refunded', updated_at = NOW() WHERE order_tracking_id = $1`,
		orderTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to update order payment_status: %w", err)
	}

	// Cancel any pending COD debts for this order
	_, err = tx.Exec(ctx,
		`UPDATE cod_debts SET status = 'cancelled', settled_at = NOW() WHERE order_tracking_id = $1 AND status != 'cancelled'`,
		orderTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to cancel COD debts: %w", err)
	}

	// Commit
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Remove from Redis index
	_ = s.index.Remove(ctx, hold.ID.String())

	return nil
}


// refundCODReturn handles COD orders where no escrow hold exists (rider hasn't settled).
// Credits customer wallet directly, cancels COD debt, decrements rider's cash_in_hand.
func (s *Service) refundCODReturn(ctx context.Context, orderTrackingID string, amount int64) error {
	// 1. Get order details
	var customerID, vendorID string
	var paymentGateway string
	var holdAmount int64
	err := s.db.QueryRow(ctx,
		`SELECT customer_tracking_id, vendor_tracking_id, payment_gateway, COALESCE(total_amount_paisa, 0)
		 FROM orders WHERE order_tracking_id = $1`, orderTrackingID,
	).Scan(&customerID, &vendorID, &paymentGateway, &holdAmount)
	if err != nil {
		return fmt.Errorf("failed to fetch order: %w", err)
	}

	// Use order total if amount is 0
	refundAmount := amount
	if refundAmount == 0 {
		refundAmount = holdAmount
	}

	// 3. Get rider_tracking_id from cod_debts
	var riderID string
	if riderErr := s.db.QueryRow(ctx,
		`SELECT rider_tracking_id FROM cod_debts WHERE order_tracking_id = $1 AND status != 'cancelled' LIMIT 1`,
		orderTrackingID,
	).Scan(&riderID); riderErr != nil {
		fmt.Printf("[COD-RETURN] Warning: could not find rider_tracking_id for order %s: %v\n", orderTrackingID, riderErr)
	}

	// 4. DB Transaction
	tx, txErr := s.db.Begin(ctx)
	if txErr != nil {
		return fmt.Errorf("failed to begin transaction: %w", txErr)
	}
	defer tx.Rollback(ctx)

	// 3. Ledger transfer: rider_cod_debt -> cash_receivable (reverse the debt)
	idempotencyKey := fmt.Sprintf("cod:return:%s", orderTrackingID)
	_, err = s.ledger.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   ledger.AccountRiderCODDebt,
		CreditAccount:  ledger.AccountCashReceivable,
		Amount:         refundAmount,
		Currency:       "PKR",
		ReferenceType:  "cod_return_reversal",
		ReferenceID:    orderTrackingID,
		Description:    fmt.Sprintf("COD return reversal for order %s", orderTrackingID),
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("COD return ledger transfer failed: %w", err)
	}

	// a. Credit customer wallet
	_, err = tx.Exec(ctx,
		`INSERT INTO customer_wallet (customer_tracking_id, balance_paisa, lifetime_spent_paisa, updated_at)
		 VALUES ($1, $2, 0, NOW())
		 ON CONFLICT (customer_tracking_id)
		 DO UPDATE SET balance_paisa = customer_wallet.balance_paisa + $2, updated_at = NOW()`,
		customerID, refundAmount,
	)
	if err != nil {
		return fmt.Errorf("failed to credit customer wallet: %w", err)
	}

	// b. Cancel COD debts
	_, err = tx.Exec(ctx,
		`UPDATE cod_debts SET status = 'cancelled', settled_at = NOW()
		 WHERE order_tracking_id = $1 AND status != 'cancelled'`,
		orderTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to cancel COD debts: %w", err)
	}

	// c. Decrement rider's cash_in_hand
	if riderID != "" {
		tag, err := tx.Exec(ctx,
			`UPDATE rider_wallet SET cash_in_hand_paisa = GREATEST(0, cash_in_hand_paisa - $1), updated_at = NOW()
			 WHERE rider_tracking_id = $2`,
			refundAmount, riderID,
		)
		if err != nil {
			return fmt.Errorf("failed to decrement rider cash_in_hand: %w", err)
		}
		if tag.RowsAffected() == 0 {
			fmt.Printf("[COD-RETURN] ERROR: rider_wallet not found for rider %s on order %s - customer still refunded but cash_in_hand not corrected\n", riderID, orderTrackingID)
		}
	}

	// d. Update order payment status
	_, err = tx.Exec(ctx,
		`UPDATE orders SET payment_status = 'refunded', updated_at = NOW() WHERE order_tracking_id = $1`,
		orderTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to update order payment_status: %w", err)
	}

	// 5. Commit
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// ClawbackFromVendor debits vendor_clawback_paisa when a return is verified
// after escrow has already been released to the vendor. The clawback amount
// is deducted from the vendor's next payout (Shopify model).
func (s *Service) ClawbackFromVendor(ctx context.Context, orderTrackingID string, vendorID string, amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("clawback amount must be positive, got %d", amount)
	}

	// 1. Ledger transfer: vendor_clawback → customer_wallet
	idempotencyKey := fmt.Sprintf("clawback:%s", orderTrackingID)
	_, err := s.ledger.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   ledger.AccountVendorClawback,
		CreditAccount:  ledger.AccountCustomerWallet,
		Amount:         amount,
		Currency:       "PKR",
		ReferenceType:  "vendor_clawback",
		ReferenceID:    orderTrackingID,
		Description:    fmt.Sprintf("Vendor clawback for return order %s", orderTrackingID),
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("vendor clawback ledger transfer failed: %w", err)
	}

	// 2. Debit vendor_clawback_paisa (deducted from next payout)
	tag, err := s.db.Exec(ctx,
		`UPDATE vendor_wallet SET vendor_clawback_paisa = vendor_clawback_paisa + $1, updated_at = NOW()
		 WHERE vendor_tracking_id = $2`,
		amount, vendorID,
	)
	if err != nil {
		return fmt.Errorf("failed to update vendor clawback: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("vendor_wallet not found for vendor %s", vendorID)
	}

	fmt.Printf("[CLAWBACK] Vendor %s: clawback %d paisa for order %s (deducted from next payout)\n",
		vendorID, amount, orderTrackingID)

	return nil
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
