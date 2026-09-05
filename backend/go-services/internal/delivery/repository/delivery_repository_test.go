package repository

import (
	"os"
	"strings"
	"testing"
)

func readDeliverySource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("delivery_repository.go")
	if err != nil {
		t.Fatalf("failed to read delivery_repository.go: %v", err)
	}
	return string(data)
}

// extractFunctionBody returns the body of the named function from source.
func extractFunctionBody(t *testing.T, src, funcSig string) string {
	t.Helper()
	idx := strings.Index(src, funcSig)
	if idx == -1 {
		t.Fatalf("function %q not found", funcSig)
	}
	// Take 4KB after the declaration — enough for these function bodies.
	end := idx + 4000
	if end > len(src) {
		end = len(src)
	}
	return src[idx:end]
}

// TestCreateGig_HasTransactionalLock validates BUG #2 fix: CreateGig
// must use a transaction with SELECT ... FOR UPDATE to prevent the
// TOCTOU race that allowed up to 30 duplicate delivery rows per order.
func TestCreateGig_HasTransactionalLock(t *testing.T) {
	src := readDeliverySource(t)
	body := extractFunctionBody(t, src, "func (r *DeliveryRepository) CreateGig")

	checks := []struct {
		desc   string
		substr string
	}{
		{"CreateGig begins a transaction", "r.writer.Begin(ctx)"},
		{"CreateGig uses SELECT ... FOR UPDATE on parent order", "SELECT 1 FROM orders WHERE order_tracking_id = $1 FOR UPDATE"},
		{"CreateGig checks for existing active gig inside the tx", "FROM deliveries WHERE order_tracking_id = $1 AND status NOT IN ('cancelled','completed')"},
		{"defer tx.Rollback is present", "defer tx.Rollback(ctx)"},
		{"CreateGig commits the transaction", "tx.Commit(ctx)"},
	}
	for _, c := range checks {
		if !strings.Contains(body, c.substr) {
			t.Errorf("BUG #2 fix missing: %s — expected substring %q in CreateGig", c.desc, c.substr)
		}
	}

	// The legacy check-then-act pattern (count check on r.writer
	// OUTSIDE a tx) must be gone.
	if strings.Contains(body, "r.writer.QueryRow(ctx,\n\t\t`SELECT COUNT(*) FROM deliveries") {
		t.Error("BUG #2 still present: CreateGig has the legacy check-then-act pattern using r.writer.QueryRow")
	}
}

// TestAcceptGig_RiderMirrorHasErrorCheck validates BUG #5 fix: the
// UPDATE orders SET rider_tracking_id mirror statement inside the
// AcceptGig transactions must check the error.
func TestAcceptGig_RiderMirrorHasErrorCheck(t *testing.T) {
	src := readDeliverySource(t)

	// Count occurrences of the buggy `_, _ = tx.Exec(...UPDATE orders SET rider_tracking_id...)` pattern.
	buggyCount := strings.Count(src, "_, _ = tx.Exec(ctx, `UPDATE orders SET rider_tracking_id")
	if buggyCount > 0 {
		t.Errorf("BUG #5 still present: found %d occurrence(s) of `_, _ = tx.Exec` with rider mirror update (should be 0)", buggyCount)
	}

	// The fix must check the error.
	if !strings.Contains(src, "failed to mirror rider assignment to order") {
		t.Error("BUG #5 fix missing: error handling for rider-to-order mirror update")
	}
}
