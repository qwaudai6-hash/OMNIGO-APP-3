package workers

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/redis/go-redis/v9"
)

// PayoutWorker runs periodically to release vendor payouts from released escrow.
type PayoutWorker struct {
	db     *pgxpool.Pool
	ledger *ledger.Service
	redis  redis.UniversalClient
}

func NewPayoutWorker(db *pgxpool.Pool, ledgerSvc *ledger.Service, rdb redis.UniversalClient) *PayoutWorker {
	return &PayoutWorker{db: db, ledger: ledgerSvc, redis: rdb}
}

// Start begins the hourly payout loop.
func (w *PayoutWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	fmt.Println("[PayoutWorker] Started — checking every hour for releasable vendor funds")

	// Run immediately on start
	w.processPayouts(ctx)

	for {
		select {
		case <-ctx.Done():
			fmt.Println("[PayoutWorker] Shutting down")
			return
		case <-ticker.C:
			w.processPayouts(ctx)
		}
	}
}

func (w *PayoutWorker) processPayouts(ctx context.Context) {
	if w.redis != nil {
		lockKey := "lock:workers:payout-worker"
		success, err := w.redis.SetNX(ctx, lockKey, "1", 30*time.Minute).Result()
		if err != nil {
			fmt.Printf("[PayoutWorker] Redis lock error: %v\n", err)
			return
		}
		if !success {
			return
		}
		defer w.redis.Del(ctx, lockKey)
	}
	// 1. Find all vendors with released escrow holds
	rows, err := w.db.Query(ctx,
		`SELECT vendor_tracking_id, COALESCE(SUM(amount), 0)::float8 as total_released
		 FROM escrow_holds
		 WHERE status = 'released'
		 GROUP BY vendor_tracking_id`,
	)
	if err != nil {
		fmt.Printf("[PayoutWorker] Error querying released escrows: %v\n", err)
		return
	}
	defer rows.Close()

	batchID := uuid.New()
	payoutsCreated := 0

	for rows.Next() {
		var vendorID string
		var totalReleased float64
		if err := rows.Scan(&vendorID, &totalReleased); err != nil {
			fmt.Printf("[PayoutWorker] Error scanning vendor row: %v\n", err)
			continue
		}

		if totalReleased <= 0 {
			continue
		}

		// Verify vendor exists before creating payout/wallet records
		ok, err := database.Exists(ctx, w.db, "SELECT 1 FROM users WHERE tracking_id = $1", vendorID)
		if err != nil {
			fmt.Printf("[PayoutWorker] Error verifying vendor %s: %v\n", vendorID, err)
			continue
		}
		if !ok {
			fmt.Printf("[PayoutWorker] Vendor %s does not exist, skipping payout\n", vendorID)
			continue
		}

		// 2. Check for open disputes on this vendor's orders
		var openDisputes int
		err = w.db.QueryRow(ctx,
			`SELECT COUNT(*) FROM disputes d
			 JOIN escrow_holds e ON d.order_tracking_id = e.order_tracking_id
			 WHERE e.vendor_tracking_id = $1 AND e.status = 'released'
			 AND d.status IN ('open', 'investigating')`,
			vendorID,
		).Scan(&openDisputes)
		if err != nil {
			fmt.Printf("[PayoutWorker] Error checking disputes for %s: %v\n", vendorID, err)
			continue
		}
		if openDisputes > 0 {
			fmt.Printf("[PayoutWorker] Skipping %s — %d open disputes\n", vendorID, openDisputes)
			continue
		}

		// 3. Process payout inside an atomic database transaction
		tx, err := w.db.Begin(ctx)
		if err != nil {
			fmt.Printf("[PayoutWorker] Error starting transaction for %s: %v\n", vendorID, err)
			continue
		}

		// Calculate the exact amount to be paid out inside transaction and lock the escrow holds
		var totalReleasedInTx float64
		err = tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(amount), 0)::float8
			 FROM escrow_holds
			 WHERE vendor_tracking_id = $1 AND status = 'released'
			 FOR UPDATE`,
			vendorID,
		).Scan(&totalReleasedInTx)
		if err != nil || totalReleasedInTx <= 0 {
			tx.Rollback(ctx)
			continue
		}

		// DOUBLE-SPEND GUARD: the vendor may have already withdrawn part or
		// all of this balance via POST /payments/vendor/withdraw, which
		// debits vendor_wallet.balance immediately. Only sweep what is still
		// actually present in the wallet — never drive the balance negative
		// or pay out the same money twice. Holds always progress to
		// 'paid_out' so they are not re-swept on the next tick.
		var walletBalance float64
		err = tx.QueryRow(ctx,
			`SELECT COALESCE(balance, 0)::float8 FROM vendor_wallet WHERE vendor_tracking_id = $1 FOR UPDATE`,
			vendorID,
		).Scan(&walletBalance)
		if err != nil {
			walletBalance = 0 // no wallet row = nothing withdrawable
		}
		sweepAmount := totalReleasedInTx
		if sweepAmount > walletBalance {
			sweepAmount = walletBalance
			if sweepAmount < 0 {
				sweepAmount = 0
			}
			fmt.Printf("[PayoutWorker] Vendor %s: released %.2f but wallet only has %.2f (manual withdrawal already debited) — sweeping %.2f\n",
				vendorID, totalReleasedInTx, walletBalance, sweepAmount)
		}

		payoutID := uuid.New()
		if sweepAmount > 0 {
			_, err = tx.Exec(ctx,
				`INSERT INTO vendor_payouts (id, vendor_tracking_id, amount, status, batch_id)
				 VALUES ($1, $2, $3, 'pending_disbursement', $4)`,
				payoutID, vendorID, sweepAmount, batchID,
			)
			if err != nil {
				tx.Rollback(ctx)
				fmt.Printf("[PayoutWorker] Error creating payout for %s: %v\n", vendorID, err)
				continue
			}
		} else {
			payoutID = uuid.Nil // fully withdrawn manually — reconciliation-only tick
		}

		// Mark the processed escrow holds as 'paid_out' to prevent duplicate payouts in future hourly ticks
		_, err = tx.Exec(ctx,
			`UPDATE escrow_holds SET status = 'paid_out' WHERE vendor_tracking_id = $1 AND status = 'released'`,
			vendorID,
		)
		if err != nil {
			tx.Rollback(ctx)
			fmt.Printf("[PayoutWorker] Error marking escrow holds as paid_out for %s: %v\n", vendorID, err)
			continue
		}

		// 4. Update vendor_wallet in the same transaction (only the swept amount)
		if sweepAmount > 0 {
			_, err = tx.Exec(ctx,
				`UPDATE vendor_wallet
				 SET total_payouts = total_payouts + $2, balance = balance - $2, updated_at = NOW()
				 WHERE vendor_tracking_id = $1`,
				vendorID, sweepAmount,
			)
			if err != nil {
				tx.Rollback(ctx)
				fmt.Printf("[PayoutWorker] Error updating vendor wallet for %s: %v\n", vendorID, err)
				continue
			}
		}

		if err := tx.Commit(ctx); err != nil {
			fmt.Printf("[PayoutWorker] Error committing payout transaction for %s: %v\n", vendorID, err)
			continue
		}

		if sweepAmount == 0 {
			fmt.Printf("[PayoutWorker] Vendor %s: %.2f released escrow reconciled (already paid out via manual withdrawal)\n",
				vendorID, totalReleasedInTx)
			continue
		}

		// 5. Execute ledger transfer: vendor_withdrawable → vendor's external account
		idempotencyKey := fmt.Sprintf("payout:%s:%s", payoutID, vendorID)
		_, err = w.ledger.Transfer(ctx, ledger.TransferRequest{
			DebitAccount:   ledger.AccountVendorWithdrawable,
			CreditAccount:  ledger.AccountVendorBankPayout,
			Amount:         sweepAmount,
			ReferenceType:  "vendor_payout",
			ReferenceID:    vendorID,
			Description:    fmt.Sprintf("Vendor payout for %s — batch %s", vendorID, batchID),
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			// DB says paid but the double-entry ledger does not — surface it
			// on the payout row so finance can reconcile instead of silently
			// diverging from TigerBeetle.
			fmt.Printf("[PayoutWorker] Warning: Ledger transfer failed for %s: %v\n", vendorID, err)
			if _, uerr := w.db.Exec(ctx,
				`UPDATE vendor_payouts SET status = 'ledger_failed', updated_at = NOW() WHERE id = $1`,
				payoutID,
			); uerr != nil {
				fmt.Printf("[PayoutWorker] CRITICAL: failed to flag payout %s as ledger_failed: %v\n", payoutID, uerr)
			}
			continue
		}

		payoutsCreated++

		currency := os.Getenv("DEFAULT_CURRENCY")
		if currency == "" {
			currency = "PKR"
		}

		fmt.Printf("[PayoutWorker] Paid out %.2f %s to vendor %s (batch %s)\n",
			sweepAmount, currency, vendorID, batchID)
	}

	if payoutsCreated > 0 {
		fmt.Printf("[PayoutWorker] Processed %d vendor payouts in batch %s\n", payoutsCreated, batchID)
	}
}
