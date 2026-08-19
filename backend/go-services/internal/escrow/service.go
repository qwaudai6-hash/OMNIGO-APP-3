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
func (s *Service) ReleaseExpiredHolds(ctx context.Context) (int, error) {
	holds, err := s.repo.GetReleasableHolds(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch releasable holds: %w", err)
	}

	released := 0
	for _, hold := range holds {
		// Check for open disputes
		hasDispute, err := s.repo.HasOpenDisputes(ctx, hold.OrderTrackingID)
		if err != nil {
			fmt.Printf("[Escrow] Error checking disputes for order %s: %v\n", hold.OrderTrackingID, err)
			continue
		}
		if hasDispute {
			fmt.Printf("[Escrow] Skipping release for order %s — open dispute exists\n", hold.OrderTrackingID)
			continue
		}

		// Execute ledger transfer: vendor_locked_escrow → vendor_withdrawable
		idempotencyKey := fmt.Sprintf("escrow:release:%s", hold.ID.String())
		_, err = s.ledger.Transfer(ctx, ledger.TransferRequest{
			DebitAccount:   ledger.AccountVendorLockedEscrow,
			CreditAccount:  ledger.AccountVendorWithdrawable,
			Amount:         hold.Amount,
			ReferenceType:  "escrow_release",
			ReferenceID:    hold.OrderTrackingID,
			Description:    fmt.Sprintf("Escrow released for order %s after %dh hold", hold.OrderTrackingID, int(getHoldDuration().Hours())),
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			fmt.Printf("[Escrow] Ledger transfer failed for hold %s: %v\n", hold.ID, err)
			continue
		}

		// Mark hold as released
		if err := s.repo.ReleaseHold(ctx, hold.ID); err != nil {
			fmt.Printf("[Escrow] Failed to mark hold %s as released: %v\n", hold.ID, err)
			continue
		}

		released++
		fmt.Printf("[Escrow] Released %.2f PKR for vendor %s (order %s)\n",
			hold.Amount, hold.VendorTrackingID, hold.OrderTrackingID)
	}

	return released, nil
}

// FreezeForDispute freezes all held escrows for an order when a dispute is filed.
func (s *Service) FreezeForDispute(ctx context.Context, orderTrackingID string, disputeID uuid.UUID) error {
	return s.repo.FreezeForDispute(ctx, orderTrackingID, disputeID)
}

// UnfreezeOnRejection reverts disputed holds when a dispute is rejected.
func (s *Service) UnfreezeOnRejection(ctx context.Context, disputeID uuid.UUID) error {
	return s.repo.UnfreezeOnDisputeRejection(ctx, disputeID)
}

// GetHoldsByVendor returns escrow hold history for a vendor.
func (s *Service) GetHoldsByVendor(ctx context.Context, vendorTrackingID string) ([]EscrowHold, error) {
	return s.repo.GetHoldsByVendor(ctx, vendorTrackingID)
}
