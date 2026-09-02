package workers

import (
	"os"
	"strings"
	"testing"
)

func readPayoutWorkerSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("payout_worker.go")
	if err != nil {
		t.Fatalf("failed to read payout_worker.go: %v", err)
	}
	return string(data)
}

// TestPayoutWorker_VW1_SelectSpecificHolds validates VW-1 fix: the
// PayoutWorker must track which specific holds are being swept and only
// mark THOSE holds as 'paid_out', not ALL released holds.
//
// Before the fix: `UPDATE escrow_holds SET status = 'paid_out' WHERE
// vendor_tracking_id = $1 AND status = 'released'` would mark ALL
// released holds as paid_out, even when sweepAmount was less than the
// total released amount (because the vendor had already manually
// withdrawn part of the balance). This created an audit mismatch
// between vendor_wallet.total_payouts and the sum of paid_out holds.
//
// After the fix: the worker iterates over holds, accumulates them up
// to sweepAmount, and marks only those specific holds as paid_out.
func TestPayoutWorker_VW1_SelectSpecificHolds(t *testing.T) {
	src := readPayoutWorkerSource(t)

	// The buggy pattern: blanket update of ALL released holds.
	buggyPattern := `UPDATE escrow_holds SET status = 'paid_out' WHERE vendor_tracking_id = $1 AND status = 'released'`
	if strings.Contains(src, buggyPattern) {
		t.Error("VW-1 still present: PayoutWorker marks ALL released holds as paid_out (should mark only swept holds)")
	}

	// The fix: uses WHERE id = ANY($1) to update only the swept hold IDs.
	if !strings.Contains(src, "WHERE id = ANY($1)") {
		t.Error("VW-1 fix missing: PayoutWorker should use `WHERE id = ANY($1)` to mark only swept holds")
	}

	// The fix: iterates over released holds and accumulates up to sweepAmount.
	if !strings.Contains(src, "sweptHolds") {
		t.Error("VW-1 fix missing: PayoutWorker should track sweptHolds list")
	}
	if !strings.Contains(src, "finalSweepAmount") {
		t.Error("VW-1 fix missing: PayoutWorker should use finalSweepAmount (sum of actual swept holds)")
	}
}

// TestPayoutWorker_VW2_BalanceGuard validates VW-2 fix: the vendor_wallet
// debit UPDATE should have `AND balance >= $2` guard to prevent negative
// balance if a refactor changes the transaction boundaries.
func TestPayoutWorker_VW2_BalanceGuard(t *testing.T) {
	src := readPayoutWorkerSource(t)

	// Locate the wallet debit UPDATE in the payout flow.
	idx := strings.Index(src, "total_payouts = total_payouts + $2")
	if idx == -1 {
		t.Fatal("vendor_wallet debit UPDATE not found in payout_worker.go")
	}
	// Check the next 200 chars after the debit for the balance guard.
	section := src[idx : idx+300]
	if !strings.Contains(section, "AND balance >= $2") {
		t.Error("VW-2 fix missing: PayoutWorker wallet debit should have `AND balance >= $2` guard")
	}
}
