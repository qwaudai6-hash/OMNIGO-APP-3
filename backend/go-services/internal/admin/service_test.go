package admin

import (
	"os"
	"strings"
	"testing"
)

func readAdminSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("failed to read service.go: %v", err)
	}
	return string(data)
}

// TestResolveDispute_IsTransactional validates BUG #4 fix: the dispute
// resolution must run all money movements inside a single database
// transaction so a partial failure cannot leave the ledger inconsistent.
func TestResolveDispute_IsTransactional(t *testing.T) {
	src := readAdminSource(t)

	// Locate ResolveDispute and the helper functions.
	idx := strings.Index(src, "func (s *AdminSurveillanceService) ResolveDispute")
	if idx == -1 {
		t.Fatal("ResolveDispute not found")
	}
	// Take 8KB — covers ResolveDispute + the two helpers.
	end := idx + 8000
	if end > len(src) {
		end = len(src)
	}
	section := src[idx:end]

	checks := []struct {
		desc   string
		substr string
	}{
		{"Begin transaction in helper", "s.dbWriter.Begin(ctx)"},
		{"defer tx.Rollback in helper", "defer tx.Rollback(ctx)"},
		{"Final tx.Commit in helper", "tx.Commit(ctx)"},
	}
	for _, c := range checks {
		if !strings.Contains(section, c.substr) {
			t.Errorf("BUG #4 fix missing: %s — expected substring %q", c.desc, c.substr)
		}
	}

	// The buggy `_, _ = s.dbWriter.Exec(...)` patterns for money
	// movements in the guilty path must be gone.
	buggyPatterns := []string{
		`_, _ = s.dbWriter.Exec(ctx, "UPDATE escrow_holds SET status = 'refunded'`,
		`_, _ = s.dbWriter.Exec(ctx, "UPDATE rider_wallet SET balance = balance - $1`,
		`_, _ = s.dbWriter.Exec(ctx, "UPDATE vendor_wallet SET balance = balance - $1`,
	}
	for _, p := range buggyPatterns {
		if strings.Contains(src, p) {
			t.Errorf("BUG #4 still present: legacy `_, _ = s.dbWriter.Exec` for money movement: %s", p)
		}
	}
}

// TestResolveDispute_EscrowStatusGuard validates BUG #3 fix: the escrow
// status UPDATE in the dispute resolution must be guarded by
// `AND status IN ('disputed', 'held')` so already-paid-out holds are
// not silently overwritten.
func TestResolveDispute_EscrowStatusGuard(t *testing.T) {
	src := readAdminSource(t)

	if !strings.Contains(src, "AND status IN ('disputed', 'held')") {
		t.Error("BUG #3 fix missing: no `AND status IN ('disputed', 'held')` guard on escrow status UPDATE")
	}
	// The buggy unguarded pattern (UPDATE escrow_holds SET status = 'refunded' ... WHERE order_tracking_id = $1)
	// with no status guard must be gone.
	buggyPattern := `UPDATE escrow_holds SET status = 'refunded', released_at = NOW() WHERE order_tracking_id = $1"`
	if strings.Contains(src, buggyPattern) {
		t.Error("BUG #3 still present: escrow status UPDATE has no status guard")
	}
}
