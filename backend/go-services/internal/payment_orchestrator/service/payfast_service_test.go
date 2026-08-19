package service

import (
	"testing"
)

func TestPaymentRequestValidation(t *testing.T) {
	t.Run("Valid Card Request", func(t *testing.T) {
		req := PaymentRequest{
			OrderID:     "ord_12345",
			CardNumber:  "4111222233334444",
			ExpiryMonth: "12",
			ExpiryYear:  "2028",
			CVV:         "123",
		}
		if err := req.Validate(); err != nil {
			t.Errorf("expected valid card to pass, got err: %v", err)
		}
	})

	t.Run("Missing Order ID", func(t *testing.T) {
		req := PaymentRequest{
			OrderID:     "",
			CardNumber:  "4111222233334444",
			ExpiryMonth: "12",
			ExpiryYear:  "2028",
			CVV:         "123",
		}
		if err := req.Validate(); err == nil {
			t.Errorf("expected error on empty order ID")
		}
	})

	t.Run("Invalid CVV Length", func(t *testing.T) {
		req := PaymentRequest{
			OrderID:     "ord_12345",
			CardNumber:  "4111222233334444",
			ExpiryMonth: "12",
			ExpiryYear:  "2028",
			CVV:         "12", // too short
		}
		if err := req.Validate(); err == nil {
			t.Errorf("expected error on 2-digit CVV")
		}
	})

	t.Run("Invalid Expiry Month", func(t *testing.T) {
		req := PaymentRequest{
			OrderID:     "ord_12345",
			CardNumber:  "4111222233334444",
			ExpiryMonth: "13", // invalid month
			ExpiryYear:  "2028",
			CVV:         "123",
		}
		if err := req.Validate(); err == nil {
			t.Errorf("expected error on month 13")
		}
	})

	t.Run("Expired Card", func(t *testing.T) {
		req := PaymentRequest{
			OrderID:     "ord_12345",
			CardNumber:  "4111222233334444",
			ExpiryMonth: "01",
			ExpiryYear:  "2020", // in the past
			CVV:         "123",
		}
		if err := req.Validate(); err == nil {
			t.Errorf("expected error on expired card")
		}
	})

	t.Run("Valid Bank Account", func(t *testing.T) {
		req := PaymentRequest{
			OrderID:       "ord_12345",
			AccountNumber: "03001234567",
			BankCode:      "EP",
		}
		if err := req.Validate(); err != nil {
			t.Errorf("expected valid bank payment to pass, got: %v", err)
		}
	})
}

func TestMDSignatureVerification(t *testing.T) {
	svc := &PayFastService{
		callbackSecret: "secure-test-secret-key-32-bytes-long",
	}

	internalTxnID := "pf_550e8400-e29b-41d4-a716-446655440000"
	signedMD := svc.SignMD(internalTxnID)

	t.Run("Valid Signature Verification", func(t *testing.T) {
		extractedID, err := svc.VerifyMD(signedMD)
		if err != nil {
			t.Fatalf("expected valid signature to verify, got: %v", err)
		}
		if extractedID != internalTxnID {
			t.Errorf("expected %s, got %s", internalTxnID, extractedID)
		}
	})

	t.Run("Tampered Transaction ID Rejected", func(t *testing.T) {
		tamperedMD := "pf_tampered-id." + svc.generateHMACSHA256(internalTxnID)
		_, err := svc.VerifyMD(tamperedMD)
		if err == nil {
			t.Errorf("expected tampered MD to fail verification")
		}
	})

	t.Run("Tampered Signature Rejected", func(t *testing.T) {
		tamperedMD := internalTxnID + ".deadbeefcafebabe"
		_, err := svc.VerifyMD(tamperedMD)
		if err == nil {
			t.Errorf("expected tampered signature to fail verification")
		}
	})

	t.Run("Malformed MD Rejected", func(t *testing.T) {
		_, err := svc.VerifyMD("no-dot-separator")
		if err == nil {
			t.Errorf("expected malformed MD to fail verification")
		}
	})
}
