package handlers

import (
	"os"
	"strings"
	"testing"
)

func readVendorHandlerSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("vendor_handler.go")
	if err != nil {
		t.Fatalf("failed to read vendor_handler.go: %v", err)
	}
	return string(data)
}

// TestVendorHandler_VW2_BalanceGuard validates VW-2 fix: the manual
// withdraw UPDATE must have `AND balance >= $1` guard.
func TestVendorHandler_VW2_BalanceGuard(t *testing.T) {
	src := readVendorHandlerSource(t)

	// Locate the withdraw handler function
	idx := strings.Index(src, "func (h *VendorHandler) RequestWithdraw")
	if idx == -1 {
		t.Fatal("RequestWithdraw function not found")
	}
	end := idx + 3000
	if end > len(src) {
		end = len(src)
	}
	body := src[idx:end]

	// The fix: the UPDATE inside the withdrawal handler has the balance guard.
	if !strings.Contains(body, "AND balance >= $1") {
		t.Error("VW-2 fix missing: vendor_handler withdraw UPDATE should have `AND balance >= $1` guard")
	}
	// The fix: checks RowsAffected() to surface concurrent wallet changes.
	if !strings.Contains(body, "RowsAffected()") {
		t.Error("VW-2 fix missing: vendor_handler should check RowsAffected() to detect concurrent wallet changes")
	}
}
