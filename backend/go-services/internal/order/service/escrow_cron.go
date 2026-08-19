package service

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/ledger"
)

// EscrowCronService handles the background job for automatically releasing 48-hour holds.
type EscrowCronService struct {
	db        *pgxpool.Pool
	ledgerSvc *ledger.Service
	// Configurable commission rate (e.g., 0.05 for 5%)
	commissionRate float64
}

// NewEscrowCronService initializes the service.
func NewEscrowCronService(db *pgxpool.Pool, ledgerSvc *ledger.Service, commissionRate float64) *EscrowCronService {
	return &EscrowCronService{
		db:             db,
		ledgerSvc:      ledgerSvc,
		commissionRate: commissionRate,
	}
}

// Start begins the cron loop. It should be run in a goroutine.
func (s *EscrowCronService) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[EscrowCron] Shutting down...")
			return
		case <-ticker.C:
			s.ProcessPendingEscrows(ctx)
		}
	}
}

// ProcessPendingEscrows scans the DB for eligible orders and releases their escrow holds.
func (s *EscrowCronService) ProcessPendingEscrows(ctx context.Context) {
	// Find orders that were delivered more than 48 hours ago, have no
	// disputes, and haven't been released. Status is lowercase to match
	// the canonical enum installed by migration 0015.
	//
	// dispute_status is normalized to 'none' (matches schema default),
	// schema default for new orders: NONE — older rows may have 'NONE'
	// before the migration, so we cover both with LOWER().
	query := `
		SELECT order_tracking_id, total_amount, vendor_tracking_id
		FROM orders
		WHERE status = 'delivered'
		  AND LOWER(COALESCE(dispute_status, 'none')) = 'none'
		  AND escrow_released = FALSE
		  AND delivered_at IS NOT NULL
		  AND delivered_at < NOW() - INTERVAL '48 hours'
		FOR UPDATE SKIP LOCKED
	`

	rows, err := s.db.Query(ctx, query)
	if err != nil {
		log.Printf("[EscrowCron] Failed to query pending escrows: %v", err)
		return
	}
	defer rows.Close()

	var toRelease []struct {
		OrderTrackingID  string
		TotalAmount      float64
		VendorTrackingID string
	}

	for rows.Next() {
		var r struct {
			OrderTrackingID  string
			TotalAmount      float64
			VendorTrackingID string
		}
		if err := rows.Scan(&r.OrderTrackingID, &r.TotalAmount, &r.VendorTrackingID); err != nil {
			log.Printf("[EscrowCron] Failed to scan row: %v", err)
			continue
		}
		toRelease = append(toRelease, r)
	}
	rows.Close() // close early so we can do updates

	for _, r := range toRelease {
		// 1. Release in Ledger
		// (admin commission vs vendor payout)
		_, err := s.ledgerSvc.ReleaseEscrowToVendor(ctx, r.OrderTrackingID, r.TotalAmount, s.commissionRate)
		if err != nil {
			log.Printf("[EscrowCron] Failed to release escrow in ledger for order %s: %v", r.OrderTrackingID, err)
			continue
		}

		// 2. Mark as released in DB
		updateQ := `UPDATE orders SET escrow_released = TRUE WHERE order_tracking_id = $1`
		_, err = s.db.Exec(ctx, updateQ, r.OrderTrackingID)
		if err != nil {
			log.Printf("[EscrowCron] Failed to update escrow_released for order %s: %v", r.OrderTrackingID, err)
			// Note: The ledger enforces idempotency, so if this cron runs again, it won't duplicate the ledger transfer.
		}

		log.Printf("[EscrowCron] Successfully released %.2f for order %s (Vendor %s)", r.TotalAmount, r.OrderTrackingID, r.VendorTrackingID)
	}
}
