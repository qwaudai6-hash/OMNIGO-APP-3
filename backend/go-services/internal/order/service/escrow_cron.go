package service

import (
	"context"
	"fmt"
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
		SELECT order_tracking_id, total_amount, vendor_tracking_id,
		       COALESCE(vendor_escrow, 0) AS vendor_escrow,
		       COALESCE(admin_commission, 0) AS admin_commission
		FROM orders
		WHERE status = 'delivered'
		  AND LOWER(COALESCE(dispute_status, 'none')) = 'none'
		  AND escrow_released = FALSE
		  AND delivered_at IS NOT NULL
		  AND delivered_at < NOW() - INTERVAL '48 hours'
		  AND LOWER(COALESCE(payment_gateway, '')) != 'cod'
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
		VendorEscrow     float64
		AdminCommission  float64
	}

	for rows.Next() {
		var r struct {
			OrderTrackingID  string
			TotalAmount      float64
			VendorTrackingID string
			VendorEscrow     float64
			AdminCommission  float64
		}
		if err := rows.Scan(&r.OrderTrackingID, &r.TotalAmount, &r.VendorTrackingID, &r.VendorEscrow, &r.AdminCommission); err != nil {
			log.Printf("[EscrowCron] Failed to scan row: %v", err)
			continue
		}
		toRelease = append(toRelease, r)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[EscrowCron] LOW-05: row iteration error before release phase: %v", err)
	}
	rows.Close() // close early so we can do updates

	for _, r := range toRelease {
		// FIX C3+M8: Use pre-computed vendor_escrow + admin_commission instead of
		// total_amount. The SettlementWorker already computed the correct split
		// at payment time. Using total_amount would re-apply the commission rate
		// and over-credit the vendor with the delivery fee portion.
		//
		// Also fix M8: We no longer call ReleaseEscrowToVendor which applies
		// commissionRate again (double-dip). Instead, we directly transfer the
		// pre-computed amounts to their respective accounts.
		vendorAmount := r.VendorEscrow
		adminAmount := r.AdminCommission
		if vendorAmount+adminAmount <= 0 {
			// Fallback for legacy orders without pre-computed split
			vendorAmount = r.TotalAmount * (1 - s.commissionRate)
			adminAmount = r.TotalAmount * s.commissionRate
		}

		// 1. Release pre-computed splits directly in Ledger
		if adminAmount > 0 {
			_, err := s.ledgerSvc.Transfer(ctx, ledger.TransferRequest{
				DebitAccount:   ledger.AccountVendorPendingEscrow,
				CreditAccount:  ledger.AccountAdminRevenue,
				Amount:         adminAmount,
				Currency:       "PKR",
				ReferenceType:  "admin_commission",
				ReferenceID:    r.OrderTrackingID,
				Description:    fmt.Sprintf("Admin commission for order %s", r.OrderTrackingID),
				IdempotencyKey: fmt.Sprintf("commission_%s", r.OrderTrackingID),
			})
			if err != nil {
				log.Printf("[EscrowCron] Failed to transfer admin commission for order %s: %v", r.OrderTrackingID, err)
				continue
			}
		}
		if vendorAmount > 0 {
			_, err := s.ledgerSvc.Transfer(ctx, ledger.TransferRequest{
				DebitAccount:   ledger.AccountVendorPendingEscrow,
				CreditAccount:  ledger.AccountVendorWallet,
				Amount:         vendorAmount,
				Currency:       "PKR",
				ReferenceType:  "vendor_payout",
				ReferenceID:    r.OrderTrackingID,
				Description:    fmt.Sprintf("Vendor payout for order %s", r.OrderTrackingID),
				IdempotencyKey: fmt.Sprintf("vendor_payout_%s", r.OrderTrackingID),
			})
			if err != nil {
				log.Printf("[EscrowCron] Failed to transfer vendor payout for order %s: %v", r.OrderTrackingID, err)
				continue
			}
		}

		// 2. Mark as released in DB
		updateQ := `UPDATE orders SET escrow_released = TRUE WHERE order_tracking_id = $1`
		_, err = s.db.Exec(ctx, updateQ, r.OrderTrackingID)
		if err != nil {
			log.Printf("[EscrowCron] Failed to update escrow_released for order %s: %v", r.OrderTrackingID, err)
			// Note: The ledger enforces idempotency, so if this cron runs again, it won't duplicate the ledger transfer.
		}

		log.Printf("[EscrowCron] Successfully released %.2f for order %s (Vendor %s, Admin %.2f, Vendor %.2f)", vendorAmount+adminAmount, r.OrderTrackingID, r.VendorTrackingID, adminAmount, vendorAmount)
	}
}
