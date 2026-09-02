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
		// or pay out the same money twice.
		//
		// VW-1 FIX: We must track which specific holds are actually being
		// swept, so we only mark THOSE holds as 'paid_out'. The previous
		// implementation marked ALL released holds as paid_out regardless
		// of how much was actually paid — creating an audit mismatch where
		// vendor_wallet.total_payouts disagreed with the sum of paid_out holds.
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

		// Identify the specific holds that fit within sweepAmount, oldest
		// first. These are the holds that will be marked paid_out.
		// Unused holds revert to 'held' status for a future tick.
		type holdRow struct {
			ID     uuid.UUID
			Amount float64
		}
		var sweptHolds []holdRow
		var sweptTotal float64
		if sweepAmount > 0 {
			rows, err := tx.Query(ctx, `
				SELECT id, amount
				FROM escrow_holds
				WHERE vendor_tracking_id = $1 AND status = 'released'
				ORDER BY hold_until ASC, created_at ASC, id ASC
				FOR UPDATE
			`, vendorID)
			if err != nil {
				tx.Rollback(ctx)
				fmt.Printf("[PayoutWorker] Error listing released holds for %s: %v\n", vendorID, err)
				continue
			}
			for rows.Next() {
				var h holdRow
				if err := rows.Scan(&h.ID, &h.Amount); err != nil {
					rows.Close()
					tx.Rollback(ctx)
					fmt.Printf("[PayoutWorker] Error scanning hold row for %s: %v\n", vendorID, err)
					continue
				}
				if sweptTotal+h.Amount <= sweepAmount+0.0001 {
					sweptHolds = append(sweptHolds, h)
					sweptTotal += h.Amount
				} else {
					// Remaining holds don't fit in this sweep; leave them.
					// They are still locked (FOR UPDATE) but we won't mark
					// them paid_out. The release on rows.Close() at the end
					// of the loop will unlock them.
					break
				}
			}
			rows.Close()
		}
		// Use the actual sum of swept holds as the final sweep amount, to
		// ensure vendor_wallet.total_payouts and the ledger transfer match
		// the sum of paid_out holds exactly (no floating-point drift).
		finalSweepAmount := sweptTotal
		if finalSweepAmount > sweepAmount {
			finalSweepAmount = sweepAmount
		}

		payoutID := uuid.New()
		if finalSweepAmount > 0 {
			_, err = tx.Exec(ctx,
				`INSERT INTO vendor_payouts (id, vendor_tracking_id, amount, status, batch_id)
				 VALUES ($1, $2, $3, 'pending_disbursement', $4)`,
				payoutID, vendorID, finalSweepAmount, batchID,
			)
			if err != nil {
				tx.Rollback(ctx)
				fmt.Printf("[PayoutWorker] Error creating payout for %s: %v\n", vendorID, err)
				continue
			}
		} else {
			payoutID = uuid.Nil // fully withdrawn manually — reconciliation-only tick
		}

		// Mark ONLY the swept holds as 'paid_out'. Unused holds remain
		// 'released' and will be re-evaluated on the next tick (if the
		// vendor's wallet balance is sufficient then).
		if len(sweptHolds) > 0 {
			holdIDs := make([]uuid.UUID, len(sweptHolds))
			for i, h := range sweptHolds {
				holdIDs[i] = h.ID
			}
			_, err = tx.Exec(ctx,
				`UPDATE escrow_holds SET status = 'paid_out' WHERE id = ANY($1)`,
				holdIDs,
			)
			if err != nil {
				tx.Rollback(ctx)
				fmt.Printf("[PayoutWorker] Error marking swept holds as paid_out for %s: %v\n", vendorID, err)
				continue
			}
		}

		// 4. Update vendor_wallet in the same transaction (only the swept amount).
		// VW-2 FIX: AND balance >= $2 prevents negative balance if a refactor
		// changes the transaction boundaries.
		if finalSweepAmount > 0 {
			_, err = tx.Exec(ctx,
				`UPDATE vendor_wallet
				 SET total_payouts = total_payouts + $2, balance = balance - $2, updated_at = NOW()
				 WHERE vendor_tracking_id = $1 AND balance >= $2`,
				vendorID, finalSweepAmount,
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

		if finalSweepAmount == 0 {
			fmt.Printf("[PayoutWorker] Vendor %s: %.2f released escrow reconciled (already paid out via manual withdrawal)\n",
				vendorID, totalReleasedInTx)
			continue
		}

		// 5. Execute ledger transfer: vendor_withdrawable → vendor's external account
		idempotencyKey := fmt.Sprintf("payout:%s:%s", payoutID, vendorID)
		_, err = w.ledger.Transfer(ctx, ledger.TransferRequest{
			DebitAccount:   ledger.AccountVendorWithdrawable,
			CreditAccount:  ledger.AccountVendorBankPayout,
			Amount:         finalSweepAmount,
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

		fmt.Printf("[PayoutWorker] Paid out %.2f %s to vendor %s (batch %s, swept %d holds)\n",
			finalSweepAmount, currency, vendorID, batchID, len(sweptHolds))
	}

	if payoutsCreated > 0 {
		fmt.Printf("[PayoutWorker] Processed %d vendor payouts in batch %s\n", payoutsCreated, batchID)
	}
}
