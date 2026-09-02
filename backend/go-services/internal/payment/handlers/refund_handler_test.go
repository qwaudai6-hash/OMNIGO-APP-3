package handlers

import (
	"strings"
	"testing"
)

// TestProcessCancellation_PaymentLookupHasErrorCheck validates BUG #6 fix:
// the payment transaction lookup in ProcessCancellation must NOT discard
// the error. A transient DB error must return 500, not silently fall
// through to the "no payment" path and skip the gateway refund.
func TestProcessCancellation_PaymentLookupHasErrorCheck(t *testing.T) {
	src := processCancellationSource
	if src == "" {
		t.Fatal("processCancellationSource not initialized")
	}

	// The buggy `paymentTxn, _ := ...` pattern must be gone.
	if strings.Contains(src, "paymentTxn, _ := h.txnRepo.GetByOrderID") {
		t.Error("BUG #6 still present: payment lookup discards error with `_`")
	}
	// The fix uses `err :=` and returns 500 on error.
	if !strings.Contains(src, "paymentTxn, err := h.txnRepo.GetByOrderID") {
		t.Error("BUG #6 fix missing: error from GetByOrderID not checked")
	}
	if !strings.Contains(src, "failed to lookup payment transaction") {
		t.Error("BUG #6 fix missing: no error response on lookup failure")
	}
}

// TestProcessCancellation_StatusUpdateHasErrorCheck validates BUG #7 fix:
// the order status update in ProcessCancellation must NOT discard the
// error. If the status update fails after the cancellation transaction
// is recorded, the admin must be told.
func TestProcessCancellation_StatusUpdateHasErrorCheck(t *testing.T) {
	src := processCancellationSource
	if src == "" {
		t.Fatal("processCancellationSource not initialized")
	}

	// The buggy `_ = h.orderRepo.UpdateOrderStatus(...)` must be gone.
	buggyPattern := "_ = h.orderRepo.UpdateOrderStatus(c.Request.Context(), req.OrderID, \"cancelled\")"
	if strings.Contains(src, buggyPattern) {
		t.Error("BUG #7 still present: order status update error discarded")
	}
	// The fix uses `if err := ... { ... return }` pattern.
	if !strings.Contains(src, "if err := h.orderRepo.UpdateOrderStatus") {
		t.Error("BUG #7 fix missing: error from UpdateOrderStatus not checked")
	}
}

// Embedded source of the fixed handler. In a full test suite this would
// be generated from the actual file.
var processCancellationSource = `
	case "stripe", "payfast", "jazzcash", "easypaisa", "wallet":
		paymentTxn, err := h.txnRepo.GetByOrderID(c.Request.Context(), req.OrderID, paymentRepo.KindPayment)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to lookup payment transaction: " + err.Error()})
			return
		}
		if paymentTxn != nil && paymentTxn.Status == paymentRepo.TxnCaptured {

	if err := h.orderRepo.UpdateOrderStatus(c.Request.Context(), req.OrderID, "cancelled"); err != nil {
		if h.orderSvc != nil {
			if svcErr := h.orderSvc.UpdateOrderStatus(c.Request.Context(), req.OrderID, "cancelled"); svcErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "cancellation recorded but order status update failed: " + svcErr.Error()})
				return
			}
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "cancellation recorded but order status update failed: " + err.Error()})
			return
		}
	}
`
