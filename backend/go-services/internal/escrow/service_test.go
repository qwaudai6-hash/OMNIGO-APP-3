package escrow

import (
	"os"
	"strings"
	"testing"
)

// readSourceFile reads the service.go file for static pattern checks.
func readSourceFile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("failed to read service.go: %v", err)
	}
	return string(data)
}

// TestReleaseExpiredHolds_SQLPatternContainsAtomicClaim validates that the
// ReleaseExpiredHolds function uses an atomic claim pattern (UPDATE ...
// RETURNING) INSIDE a transaction to prevent the BUG-04/11 double-payout
// race. This is a static check — the function needs a real DB to
// integration-test the race, but we can verify the source contains the
// required concurrency primitives.
func TestReleaseExpiredHolds_SQLPatternContainsAtomicClaim(t *testing.T) {
	src := readSourceFile(t)

	// Find the ReleaseExpiredHolds function body
	idx := strings.Index(src, "func (s *Service) ReleaseExpiredHolds")
	if idx == -1 {
		t.Fatal("ReleaseExpiredHolds function not found in service.go")
	}
	// Take a 6KB window after the function declaration — enough to cover the body.
	end := idx + 6000
	if end > len(src) {
		end = len(src)
	}
	fnBody := src[idx:end]

	checks := []struct {
		desc   string
		substr string
	}{
		{"FOR UPDATE SKIP LOCKED is used", "FOR UPDATE SKIP LOCKED"},
		{"UPDATE ... RETURNING pattern is used for atomic claim", "RETURNING id, order_tracking_id, vendor_tracking_id, amount"},
		{"alreadyReleased check has error handling (no _ = prefix)", "if err != nil"},
		{"status transition held -> releasing is used", "SET status = 'releasing'"},
		{"orders.escrow_released update has WHERE guard", "AND escrow_released = FALSE"},
	}
	for _, c := range checks {
		if !strings.Contains(fnBody, c.substr) {
			t.Errorf("BUG #1 fix missing: %s — expected substring %q in ReleaseExpiredHolds", c.desc, c.substr)
		}
	}
}

// TestReleaseExpiredHolds_FailClosedOnSafetyCheck validates BUG #8 fix:
// the alreadyReleased check must NOT swallow errors with `_ =`.
// It must check the error and roll back on failure (fail-closed).
func TestReleaseExpiredHolds_FailClosedOnSafetyCheck(t *testing.T) {
	src := readSourceFile(t)
	idx := strings.Index(src, "func (s *Service) ReleaseExpiredHolds")
	if idx == -1 {
		t.Fatal("ReleaseExpiredHolds function not found")
	}
	end := idx + 6000
	if end > len(src) {
		end = len(src)
	}
	fnBody := src[idx:end]

	// The buggy fail-open pattern was: `_ = s.db.QueryRow(...)` for
	// the alreadyReleased check. That must be gone.
	if strings.Contains(fnBody, "_ = s.db.QueryRow(ctx,\n\t\t\t`SELECT COALESCE(escrow_released") {
		t.Error("BUG #8 still present: alreadyReleased check uses `_ =` (fail-open)")
	}
	// The fix uses `err = tx.QueryRow(...)` with an `if err != nil` check.
	if !strings.Contains(fnBody, "Failed to check alreadyReleased") {
		t.Error("BUG #8 fix missing: no error handling for alreadyReleased check")
	}
}

// TestEscrowStatusConstants ensures the status enum values are stable.
func TestEscrowStatusConstants(t *testing.T) {
	if StatusHeld != "held" {
		t.Errorf("StatusHeld = %q, want %q", StatusHeld, "held")
	}
	if StatusReleased != "released" {
		t.Errorf("StatusReleased = %q, want %q", StatusReleased, "released")
	}
	if StatusDisputed != "disputed" {
		t.Errorf("StatusDisputed = %q, want %q", StatusDisputed, "disputed")
	}
	if StatusRefunded != "refunded" {
		t.Errorf("StatusRefunded = %q, want %q", StatusRefunded, "refunded")
	}
	if StatusPaidOut != "paid_out" {
		t.Errorf("StatusPaidOut = %q, want %q", StatusPaidOut, "paid_out")
	}
}
