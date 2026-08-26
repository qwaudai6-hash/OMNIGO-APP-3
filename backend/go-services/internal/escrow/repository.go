package escrow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/shared/database"
)

// Repository handles escrow persistence.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
}

func validateHoldParents(ctx context.Context, q rowQuerier, hold *EscrowHold) error {
	checks := []struct {
		id    string
		label string
		query string
	}{
		{hold.OrderTrackingID, "order", "SELECT 1 FROM orders WHERE order_tracking_id = $1"},
		{hold.VendorTrackingID, "vendor/store", "SELECT 1 FROM users WHERE tracking_id = $1 UNION SELECT 1 FROM stores WHERE store_tracking_id = $1 OR vendor_tracking_id = $1"},
	}
	for _, c := range checks {
		ok, err := database.Exists(ctx, q, c.query, c.id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s %s does not exist", c.label, c.id)
		}
	}
	return nil
}

// CreateHold inserts a new escrow hold. It is idempotent across worker retries.
func (r *Repository) CreateHold(ctx context.Context, hold *EscrowHold) error {
	if err := validateHoldParents(ctx, r.db, hold); err != nil {
		return err
	}

	// Idempotency guard: If an active/released hold already exists for this order, do not duplicate
	var existingID uuid.UUID
	err := r.db.QueryRow(ctx,
		`SELECT id FROM escrow_holds WHERE order_tracking_id = $1 AND status IN ('held', 'disputed', 'released', 'paid_out') LIMIT 1`,
		hold.OrderTrackingID,
	).Scan(&existingID)
	if err == nil {
		// Hold already created in a previous attempt/retry
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("failed to check existing escrow hold: %w", err)
	}

	query := `
		INSERT INTO escrow_holds (id, order_tracking_id, vendor_tracking_id, amount, status, hold_until)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (order_tracking_id, vendor_tracking_id) DO NOTHING
	`
	_, err = r.db.Exec(ctx, query,
		hold.ID, hold.OrderTrackingID, hold.VendorTrackingID,
		hold.Amount, hold.Status, hold.HoldUntil,
	)
	if err != nil {
		return fmt.Errorf("failed to create escrow hold: %w", err)
	}
	return nil
}

// HoldExistsForOrder checks if a hold for the given order already exists.
func (r *Repository) HoldExistsForOrder(ctx context.Context, orderTrackingID string) (bool, error) {
	var count int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM escrow_holds WHERE order_tracking_id = $1`,
		orderTrackingID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// CreateHoldInTx inserts a new escrow hold inside an existing transaction.
func (r *Repository) CreateHoldInTx(ctx context.Context, tx pgx.Tx, hold *EscrowHold) error {
	if err := validateHoldParents(ctx, tx, hold); err != nil {
		return err
	}

	query := `
		INSERT INTO escrow_holds (id, order_tracking_id, vendor_tracking_id, amount, status, hold_until)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (order_tracking_id, vendor_tracking_id) DO NOTHING
	`
	_, err := tx.Exec(ctx, query,
		hold.ID, hold.OrderTrackingID, hold.VendorTrackingID,
		hold.Amount, hold.Status, hold.HoldUntil,
	)
	return err
}

// ReleaseHold marks an escrow hold as released.
func (r *Repository) ReleaseHold(ctx context.Context, holdID uuid.UUID) error {
	_, err := r.db.Exec(ctx,
		`UPDATE escrow_holds SET status = 'released', released_at = NOW() WHERE id = $1 AND status = 'held'`,
		holdID,
	)
	return err
}

// FreezeForDispute marks an escrow hold as disputed.
func (r *Repository) FreezeForDispute(ctx context.Context, orderTrackingID string, disputeID uuid.UUID) error {
	_, err := r.db.Exec(ctx,
		`UPDATE escrow_holds SET status = 'disputed', dispute_id = $1 WHERE order_tracking_id = $2 AND status = 'held'`,
		disputeID, orderTrackingID,
	)
	return err
}

// UnfreezeOnDisputeRejection reverts disputed holds back to held.
func (r *Repository) UnfreezeOnDisputeRejection(ctx context.Context, disputeID uuid.UUID) error {
	_, err := r.db.Exec(ctx,
		`UPDATE escrow_holds SET status = 'held', dispute_id = NULL WHERE dispute_id = $1 AND status = 'disputed'`,
		disputeID,
	)
	return err
}

// RefundDisputedHold marks a disputed hold as refunded and returns the hold details.
func (r *Repository) RefundDisputedHold(ctx context.Context, disputeID uuid.UUID) (*EscrowHold, error) {
	var hold EscrowHold
	err := r.db.QueryRow(ctx,
		`UPDATE escrow_holds 
		 SET status = 'refunded', released_at = NOW() 
		 WHERE dispute_id = $1 AND status = 'disputed'
		 RETURNING id, order_tracking_id, vendor_tracking_id, amount, status, hold_until, created_at`,
		disputeID,
	).Scan(&hold.ID, &hold.OrderTrackingID, &hold.VendorTrackingID, &hold.Amount, &hold.Status, &hold.HoldUntil, &hold.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &hold, nil
}

// GetReleasableHolds returns all held escrows past their hold_until time.
func (r *Repository) GetReleasableHolds(ctx context.Context) ([]EscrowHold, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, order_tracking_id, vendor_tracking_id, amount, status, hold_until, created_at
		 FROM escrow_holds WHERE status = 'held' AND hold_until < NOW() ORDER BY hold_until`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var holds []EscrowHold
	for rows.Next() {
		var h EscrowHold
		if err := rows.Scan(&h.ID, &h.OrderTrackingID, &h.VendorTrackingID,
			&h.Amount, &h.Status, &h.HoldUntil, &h.CreatedAt); err != nil {
			return nil, err
		}
		holds = append(holds, h)
	}
	return holds, rows.Err()
}

// HasOpenDisputes checks if an order has any open disputes.
func (r *Repository) HasOpenDisputes(ctx context.Context, orderTrackingID string) (bool, error) {
	var count int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM disputes WHERE order_tracking_id = $1 AND status IN ('open', 'investigating')`,
		orderTrackingID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetHoldsByVendor returns all escrow holds for a vendor.
func (r *Repository) GetHoldsByVendor(ctx context.Context, vendorTrackingID string) ([]EscrowHold, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, order_tracking_id, vendor_tracking_id, amount, status, hold_until, COALESCE(released_at, '0001-01-01'), created_at
		 FROM escrow_holds WHERE vendor_tracking_id = $1 ORDER BY created_at DESC LIMIT 50`,
		vendorTrackingID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var holds []EscrowHold
	for rows.Next() {
		var h EscrowHold
		var releasedAt time.Time
		if err := rows.Scan(&h.ID, &h.OrderTrackingID, &h.VendorTrackingID,
			&h.Amount, &h.Status, &h.HoldUntil, &releasedAt, &h.CreatedAt); err != nil {
			return nil, err
		}
		if releasedAt.Year() > 1 {
			h.ReleasedAt = &releasedAt
		}
		holds = append(holds, h)
	}
	return holds, rows.Err()
}
