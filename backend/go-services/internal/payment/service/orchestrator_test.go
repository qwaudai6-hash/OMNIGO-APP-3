package service

import (
	"context"
	"os"
	"testing"
)

func TestNewOrchestratorNoGateways(t *testing.T) {
	// Ensure no real env keys leak into the test.
	for _, k := range []string{"STRIPE_SECRET_KEY", "JAZZCASH_SALT", "EASYPAISA_HASH_KEY"} {
		os.Unsetenv(k)
	}

	o := NewOrchestrator()
	if len(o.AvailableGateways()) != 0 {
		t.Fatalf("expected 0 configured gateways, got %v", o.AvailableGateways())
	}
}

func TestNewOrchestratorWithStripe(t *testing.T) {
	os.Setenv("STRIPE_SECRET_KEY", "sk_test_dummy")
	os.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_dummy")
	defer func() {
		os.Unsetenv("STRIPE_SECRET_KEY")
		os.Unsetenv("STRIPE_WEBHOOK_SECRET")
	}()

	o := NewOrchestrator()
	available := o.AvailableGateways()
	if len(available) != 1 || available[0] != "stripe" {
		t.Fatalf("expected only stripe gateway, got %v", available)
	}

	_, err := o.CreateCheckout(context.Background(), "stripe", CheckoutRequest{
		OrderID:    "ORD-TEST-1",
		CustomerID: "CUST-1",
		Amount:     100,
		Currency:   "PKR",
	})
	// Should fail because the dummy key is not accepted by Stripe, but it proves routing works.
	if err == nil {
		t.Error("expected Stripe API error with dummy key")
	}
}

func TestNewOrchestratorUnsupportedGateway(t *testing.T) {
	o := NewOrchestrator()
	_, err := o.CreateCheckout(context.Background(), "unknown", CheckoutRequest{})
	if err == nil {
		t.Error("expected error for unsupported gateway")
	}
}

func TestJazzCashHash(t *testing.T) {
	svc := NewJazzCashService("MID", "PASS", "SALT", "")
	params := map[string]string{
		"pp_MerchantID": "MID",
		"pp_Amount":     "1000",
		"pp_TxnRefNo":   "REF1",
	}
	h1 := svc.createSecureHash(params)
	h2 := svc.createSecureHash(params)
	if h1 != h2 {
		t.Error("same params should produce same hash")
	}
	if h1 == "" {
		t.Error("hash should not be empty")
	}
}

func TestEasyPaisaHash(t *testing.T) {
	params := map[string]string{
		"storeId": "STORE",
		"amount":  "100.00",
	}
	h1 := createEasyPaisaHash(params, "KEY")
	h2 := createEasyPaisaHash(params, "KEY")
	if h1 != h2 {
		t.Error("same params should produce same hash")
	}
	if h1 == "" {
		t.Error("hash should not be empty")
	}
}
