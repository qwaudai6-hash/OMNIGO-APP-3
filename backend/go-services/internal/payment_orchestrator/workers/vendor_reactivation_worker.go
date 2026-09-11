package workers

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// VendorReactivationWorker auto-reactivates vendor stores that were suspended
// due to dispute penalties after the 24-hour suspension period expires.
type VendorReactivationWorker struct {
	db *pgxpool.Pool
}

func NewVendorReactivationWorker(db *pgxpool.Pool) *VendorReactivationWorker {
	return &VendorReactivationWorker{db: db}
}

func (w *VendorReactivationWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	log.Println("[VENDOR-REACTIVATE] Worker started, checking every 5 minutes")

	for {
		select {
		case <-ctx.Done():
			log.Println("[VENDOR-REACTIVATE] Worker stopped")
			return
		case <-ticker.C:
			w.reactivateExpiredVendors(ctx)
		}
	}
}

func (w *VendorReactivationWorker) reactivateExpiredVendors(ctx context.Context) {
	rows, err := w.db.Query(ctx,
		`SELECT vendor_tracking_id
		 FROM stores
		 WHERE is_active = false
		   AND updated_at < NOW() - INTERVAL '24 hours'
		 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		log.Printf("[VENDOR-REACTIVATE] Error querying suspended vendors: %v", err)
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var vendorID string
		if err := rows.Scan(&vendorID); err != nil {
			log.Printf("[VENDOR-REACTIVATE] Error scanning vendor: %v", err)
			continue
		}

		if err := w.reactivateVendor(ctx, vendorID); err != nil {
			log.Printf("[VENDOR-REACTIVATE] Error reactivating vendor %s: %v", vendorID, err)
			continue
		}

		count++
	}

	if count > 0 {
		log.Printf("[VENDOR-REACTIVATE] Reactivated %d expired vendor suspensions", count)
	}
}

func (w *VendorReactivationWorker) reactivateVendor(ctx context.Context, vendorID string) error {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Reactivate store
	_, err = tx.Exec(ctx,
		`UPDATE stores SET is_active = true, updated_at = NOW()
		 WHERE vendor_tracking_id = $1 AND is_active = false`,
		vendorID,
	)
	if err != nil {
		return fmt.Errorf("failed to reactivate store: %w", err)
	}

	// 2. Reactivate all products
	_, err = tx.Exec(ctx,
		`UPDATE products SET is_active = true, updated_at = NOW()
		 WHERE vendor_tracking_id = $1 AND is_active = false`,
		vendorID,
	)
	if err != nil {
		return fmt.Errorf("failed to reactivate products: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	log.Printf("[VENDOR-REACTIVATE] Vendor %s store and products reactivated", vendorID)
	return nil
}
