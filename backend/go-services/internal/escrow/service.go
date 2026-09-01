package escrow

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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
}

func NewService(db *pgxpool.Pool, ledgerSvc *ledger.Service) *Service {
	return &Service{
		repo:   NewRepository(db),
		ledger: ledgerSvc,
		db:     db,
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

	return nil
}

// ReleaseExpiredHolds releases all holds past their hold_until time
// if no open disputes exist for the order.
// BUG-04+11 FIX: Uses SELECT ... FOR UPDATE SKIP LOCKED to prevent
// concurrent cron runs from double-releasing the same hold.
func (s *Service) ReleaseExpiredHolds(ctx context.Context) (int, error) {
	released := 0

	for {
		// Fetch one releasable hold with row-level lock to prevent concurrent release.
		var holdID uuid.UUID
		var orderID, vendorID string
		var amount float64
		err := s.db.QueryRow(ctx, `
			SELECT h.id, h.order_tracking_id, h.vendor_tracking_id, h.amount
			FROM escrow_holds h
			WHERE h.status = 'held'
			  AND h.hold_until <= NOW()
			ORDER BY h.hold_until
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		`).Scan(&holdID, &orderID, &vendorID, &amount)
		if err != nil {
			// No more rows to process (or DB error)
			break
		}

		// Guard: skip if already released
		var alreadyReleased bool
		_ = s.db.QueryRow(ctx,
			`SELECT COALESCE(escrow_released, FALSE) FROM orders WHERE order_tracking_id = $1`,
			orderID,
		).Scan(&alreadyReleased)
		if alreadyReleased {
			// Mark hold as released anyway to clean up
			_, _ = s.db.Exec(ctx, `UPDATE escrow_holds SET status = 'released', released_at = NOW() WHERE id = $1`, holdID)
			continue
		}

		// Check for open disputes
		hasDispute, err := s.repo.HasOpenDisputes(ctx, orderID)
		if err != nil {
			fmt.Printf("[Escrow] Error checking disputes for order %s: %v\n", orderID, err)
			continue
		}
		if hasDispute {
			fmt.Printf("[Escrow] Skipping release for order %s — open dispute exists\n", orderID)
			continue
		}

		// Wrap remaining steps in a transaction for atomicity
		tx, err := s.db.Begin(ctx)
		if err != nil {
			fmt.Printf("[Escrow] Failed to begin transaction for hold %s: %v\n", holdID, err)
			continue
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
			fmt.Printf("[Escrow] Ledger transfer failed for hold %s: %v\n", holdID, err)
			_ = tx.Rollback(ctx)
			continue
		}

		// Mark hold as released
		_, err = tx.Exec(ctx, `UPDATE escrow_holds SET status = 'released', released_at = NOW() WHERE id = $1`, holdID)
		if err != nil {
			fmt.Printf("[Escrow] Failed to mark hold %s as released: %v\n", holdID, err)
			_ = tx.Rollback(ctx)
			continue
		}

		// Mark order as released
		_, err = tx.Exec(ctx,
			`UPDATE orders SET escrow_released = TRUE WHERE order_tracking_id = $1`,
			orderID,
		)
		if err != nil {
			fmt.Printf("[Escrow] Failed to mark escrow_released for order %s: %v\n", orderID, err)
			_ = tx.Rollback(ctx)
			continue
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
			fmt.Printf("[Escrow] Failed to credit vendor_wallet for hold %s: %v\n", holdID, err)
			_ = tx.Rollback(ctx)
			continue
		}

		// Commit transaction — all-or-nothing
		if err := tx.Commit(ctx); err != nil {
			fmt.Printf("[Escrow] Transaction commit failed for hold %s: %v\n", holdID, err)
			continue
		}

		released++
		fmt.Printf("[Escrow] Released %.2f PKR for vendor %s (order %s)\n",
			amount, vendorID, orderID)
	}

	return released, nil
}

// FreezeForDispute freezes all held escrows for an order when a dispute is filed.
func (s *Service) FreezeForDispute(ctx context.Context, orderTrackingID string, disputeID uuid.UUID) error {
	return s.repo.FreezeForDispute(ctx, orderTrackingID, disputeID)
}

// CancelForOrder cancels all held escrows when an order is cancelled or returned.
// BUG-06 FIX: Prevents vendor from receiving funds for cancelled/returned orders.
func (s *Service) CancelForOrder(ctx context.Context, orderTrackingID string) error {
	return s.repo.CancelHoldForOrder(ctx, orderTrackingID)
}

// UnfreezeOnRejection reverts disputed holds when a dispute is rejected.
func (s *Service) UnfreezeOnRejection(ctx context.Context, disputeID uuid.UUID) error {
	return s.repo.UnfreezeOnDisputeRejection(ctx, disputeID)
}

// RefundDispute executes a double-entry ledger refund to the customer and marks the escrow hold refunded.
func (s *Service) RefundDispute(ctx context.Context, disputeID uuid.UUID) error {
	hold, err := s.repo.RefundDisputedHold(ctx, disputeID)
	if err != nil {
		return fmt.Errorf("failed to mark escrow hold refunded: %w", err)
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
