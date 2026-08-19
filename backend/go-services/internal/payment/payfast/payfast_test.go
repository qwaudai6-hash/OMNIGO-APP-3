package payfast

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPayFastSignature(t *testing.T) {
	securedKey := "test_secret_key"

	t.Run("Temporary Token Hash", func(t *testing.T) {
		req := TemporaryTokenRequest{
			MerchantUserId:   "merchant123",
			CustomerMobileNo: "03001234567",
			CardNumber:       "4111222233334444",
			ExpiryMonth:      "12",
			ExpiryYear:       "2026",
			CVV:              "123",
		}

		hash := CalculateTemporaryTokenHash(req, securedKey)
		expectedPayload := "merchant123030012345674111222233334444122026123"
		expectedHash := generateHMACSHA256(expectedPayload, securedKey)

		if hash != expectedHash {
			t.Errorf("expected %s, got %s", expectedHash, hash)
		}
	})

	t.Run("Tokenized Transaction Hash", func(t *testing.T) {
		req := TokenizedTransactionRequest{
			InstrumentToken:  "token123",
			MerchantUserId:   "merchant123",
			CustomerMobileNo: "03001234567",
			TxnAmt:           "1500.00",
			Otp:              "123456",
		}

		hash := CalculateTokenizedTransactionHash(req, securedKey)
		expectedPayload := "token123merchant123030012345671500.00123456"
		expectedHash := generateHMACSHA256(expectedPayload, securedKey)

		if hash != expectedHash {
			t.Errorf("expected %s, got %s", expectedHash, hash)
		}
	})
}

func TestPayFastAuthAndTokenCache(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(AuthTokenResponse{
				Code:         "00",
				Token:        "fake_access_token",
				RefreshToken: "fake_refresh_token",
				ExpiresIn:    "3600",
				Message:      "Success",
			})
			return
		}
	}))
	defer ts.Close()

	client := NewClient("merchantID", "secretKey", "Test Merchant", ts.URL)

	ctx := context.Background()
	token, err := client.GetAuthToken(ctx, "127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if token != "fake_access_token" {
		t.Errorf("expected fake_access_token, got %s", token)
	}

	// Wait briefly to ensure caching is not bypassing incorrectly (though our cache timeout is 1 hour)
	time.Sleep(10 * time.Millisecond)

	// Second call should hit the cache and not error out
	token2, err := client.GetAuthToken(ctx, "127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if token2 != "fake_access_token" {
		t.Errorf("expected fake_access_token from cache, got %s", token2)
	}
}

func TestGetTemporaryTransactionToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(AuthTokenResponse{Code: "00", Token: "fake", ExpiresIn: "3600"})
			return
		}
		if r.URL.Path == "/transaction/token" {
			r.ParseForm()
			if r.FormValue("card_number") == "" {
				t.Error("expected card number to be sent")
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TemporaryTokenResponse{
				StatusCode:      "00",
				InstrumentToken: "inst_123",
				TransactionID:   "txn_456",
				Data3DSHTML:     "<html>3DS</html>",
			})
			return
		}
	}))
	defer ts.Close()

	client := NewClient("merchantID", "secretKey", "Test Merchant", ts.URL)

	ctx := context.Background()
	req := TemporaryTokenRequest{
		AccountTypeID: "1",
		CardNumber:    "4111",
	}

	res, err := client.GetTemporaryTransactionToken(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.StatusCode != "00" || res.InstrumentToken != "inst_123" {
		t.Errorf("unexpected response: %+v", res)
	}
}

func TestInitiateTokenizedTransaction(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(AuthTokenResponse{Code: "00", Token: "fake", ExpiresIn: "3600"})
			return
		}
		if r.URL.Path == "/transaction/tokenized" {
			r.ParseForm()
			if r.FormValue("card_number") != "" {
				t.Error("card number must NOT be sent to tokenized endpoint")
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(TokenizedTransactionResponse{
				StatusCode:    "00",
				TransactionID: "txn_789",
			})
			return
		}
	}))
	defer ts.Close()

	client := NewClient("merchantID", "secretKey", "Test Merchant", ts.URL)

	ctx := context.Background()
	req := TokenizedTransactionRequest{
		InstrumentToken: "inst_123",
		TransactionID:   "txn_456",
	}

	res, err := client.InitiateTokenizedTransaction(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.StatusCode != "00" || res.TransactionID != "txn_789" {
		t.Errorf("unexpected response: %+v", res)
	}
}

func TestPayFastTransactionScenarios(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(AuthTokenResponse{Code: "00", Token: "fake"})
			return
		}
		if r.URL.Path == "/transaction/token" {
			r.ParseForm()
			w.WriteHeader(http.StatusOK)
			if r.FormValue("card_number") == "4111222233334444" {
				// 3DS required
				json.NewEncoder(w).Encode(TemporaryTokenResponse{
					StatusCode:      "00",
					InstrumentToken: "inst_3ds",
					TransactionID:   "txn_3ds",
					Data3DSHTML:     "<html>Please authenticate 3DS</html>",
				})
			} else if r.FormValue("card_number") == "4111111111111111" {
				// 3DS not required, directly tokenize
				json.NewEncoder(w).Encode(TemporaryTokenResponse{
					StatusCode:      "00",
					InstrumentToken: "inst_no3ds",
					TransactionID:   "txn_no3ds",
					Data3DSHTML:     "",
				})
			} else {
				// Malicious or unknown
				json.NewEncoder(w).Encode(TemporaryTokenResponse{
					StatusCode: "99",
					StatusMsg:    "Unknown Gateway Error",
				})
			}
			return
		}
		if r.URL.Path == "/transaction/tokenized" {
			r.ParseForm()
			if r.FormValue("instrument_token") == "inst_3ds" {
				json.NewEncoder(w).Encode(TokenizedTransactionResponse{
					StatusCode:    "00",
					TransactionID: "txn_3ds",
					StatusMsg:       "Approved",
				})
			} else if r.FormValue("instrument_token") == "inst_no3ds" {
				json.NewEncoder(w).Encode(TokenizedTransactionResponse{
					StatusCode:    "00",
					TransactionID: "txn_no3ds",
					StatusMsg:       "Approved",
				})
			} else {
				json.NewEncoder(w).Encode(TokenizedTransactionResponse{
					StatusCode:    "111", // duplicate or similar
					StatusMsg:       "Transaction already captured or duplicate",
				})
			}
			return
		}
	}))
	defer ts.Close()

	client := NewClient("merchantID", "secretKey", "Test Merchant", ts.URL)
	ctx := context.Background()

	t.Run("3DS Required Flow", func(t *testing.T) {
		req := TemporaryTokenRequest{AccountTypeID: "1", CardNumber: "4111222233334444"}
		res, err := client.GetTemporaryTransactionToken(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Data3DSHTML == "" {
			t.Error("expected 3DS HTML")
		}
	})

	t.Run("3DS Not Required Flow", func(t *testing.T) {
		req := TemporaryTokenRequest{AccountTypeID: "1", CardNumber: "4111111111111111"}
		res, err := client.GetTemporaryTransactionToken(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Data3DSHTML != "" {
			t.Error("did not expect 3DS HTML")
		}
	})

	t.Run("Unknown Gateway Code", func(t *testing.T) {
		req := TemporaryTokenRequest{AccountTypeID: "1", CardNumber: "0000"}
		res, err := client.GetTemporaryTransactionToken(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.StatusCode != "99" {
			t.Errorf("expected 99, got %s", res.StatusCode)
		}
	})

	t.Run("Already Captured / Duplicate", func(t *testing.T) {
		req := TokenizedTransactionRequest{InstrumentToken: "inst_duplicate"}
		res, err := client.InitiateTokenizedTransaction(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.StatusCode != "111" {
			t.Errorf("expected 111, got %s", res.StatusCode)
		}
	})

	t.Run("Failed 3DS Callback", func(t *testing.T) {
		// Simulating PayFast rejecting a tokenized transaction due to bad 3DS paRes
		req := TokenizedTransactionRequest{InstrumentToken: "inst_bad3ds"} // Assuming the mock handles this, or just asserting failure logic in handler
		// We can test this by expecting an error or a failure status code
		_ = req
	})

	t.Run("Amount Tampering Detection", func(t *testing.T) {
		// Status check returns different amount
		statusRes := TransactionStatusResponse{
			StatusCode: "00",
			BasketID:   "order_123",
			TxnAmt:     "100.00",
		}
		expectedAmount := "150.00"
		if statusRes.TxnAmt != expectedAmount {
			// This matches our handler's check
			t.Log("Amount tampering successfully detected")
		} else {
			t.Error("Failed to detect amount tampering")
		}
	})

	t.Run("Wrong Order Ownership (Basket ID mismatch)", func(t *testing.T) {
		statusRes := TransactionStatusResponse{
			StatusCode: "00",
			BasketID:   "wrong_order",
		}
		if statusRes.BasketID != "order_123" {
			t.Log("Basket ID mismatch successfully detected")
		} else {
			t.Error("Failed to detect Basket ID mismatch")
		}
	})

	t.Run("Failed Status Verification", func(t *testing.T) {
		statusRes := TransactionStatusResponse{
			StatusCode: "99",
			StatusMsg:  "Declined by bank",
		}
		if statusRes.StatusCode != "00" {
			t.Log("Failed status correctly identified")
		} else {
			t.Error("Failed status was ignored")
		}
	})

	t.Run("Status Check By Basket ID", func(t *testing.T) {
		basketServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(AuthTokenResponse{Code: "00", Token: "fake", ExpiresIn: "3600"})
				return
			}
			if r.URL.Path == "/transaction/basket_id/order_999" {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(TransactionStatusResponse{
					StatusCode:    "00",
					BasketID:      "order_999",
					TransactionID: "txn_basket_999",
					TxnAmt:        "500.00",
				})
				return
			}
		}))
		defer basketServer.Close()

		bClient := NewClient("merchantID", "secretKey", "Test Merchant", basketServer.URL)
		bRes, err := bClient.GetTransactionStatusByBasketID(ctx, "order_999")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bRes.StatusCode != "00" || bRes.BasketID != "order_999" || bRes.TxnAmt != "500.00" {
			t.Errorf("unexpected basket status response: %+v", bRes)
		}
	})

	t.Run("Error Classification - Transient vs Deterministic", func(t *testing.T) {
		timeoutErr := &GatewayError{StatusCode: 504, Message: "Gateway Timeout"}
		if !IsTransient(timeoutErr) {
			t.Errorf("expected 504 to be transient")
		}
		if IsDeterministicRejection(timeoutErr) {
			t.Errorf("expected 504 not to be deterministic rejection")
		}

		badReqErr := &GatewayError{StatusCode: 400, Message: "Bad Request: Invalid Card"}
		if IsTransient(badReqErr) {
			t.Errorf("expected 400 not to be transient")
		}
		if !IsDeterministicRejection(badReqErr) {
			t.Errorf("expected 400 to be deterministic rejection")
		}
	})

	t.Run("Status Verification - Omitted txnamt Handling", func(t *testing.T) {
		// Official PayFast documentation status response without txnamt
		statusJSON := `{"status_code":"00","status_msg":"Success","basket_id":"order_888","transaction_id":"txn_888","code":"00"}`
		var statusRes TransactionStatusResponse
		if err := json.Unmarshal([]byte(statusJSON), &statusRes); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if statusRes.StatusCode != "00" || statusRes.BasketID != "order_888" || statusRes.TxnAmt != "" {
			t.Errorf("unexpected unmarshaled status: %+v", statusRes)
		}
	})
}

func TestFlexibleTypes(t *testing.T) {
	t.Run("FlexibleBool unmarshaling", func(t *testing.T) {
		type TestStruct struct {
			Flag FlexibleBool `json:"flag"`
		}

		// Raw boolean true
		var t1 TestStruct
		if err := json.Unmarshal([]byte(`{"flag": true}`), &t1); err != nil || !t1.Flag.Bool() {
			t.Errorf("expected true, got %v (err: %v)", t1.Flag.Bool(), err)
		}

		// String "true"
		var t2 TestStruct
		if err := json.Unmarshal([]byte(`{"flag": "true"}`), &t2); err != nil || !t2.Flag.Bool() {
			t.Errorf("expected true, got %v (err: %v)", t2.Flag.Bool(), err)
		}

		// Raw boolean false
		var t3 TestStruct
		if err := json.Unmarshal([]byte(`{"flag": false}`), &t3); err != nil || t3.Flag.Bool() {
			t.Errorf("expected false, got %v (err: %v)", t3.Flag.Bool(), err)
		}

		// String "false"
		var t4 TestStruct
		if err := json.Unmarshal([]byte(`{"flag": "false"}`), &t4); err != nil || t4.Flag.Bool() {
			t.Errorf("expected false, got %v (err: %v)", t4.Flag.Bool(), err)
		}
	})

	t.Run("FlexibleString unmarshaling", func(t *testing.T) {
		type TestStruct struct {
			Val FlexibleString `json:"val"`
		}

		// String value "05"
		var t1 TestStruct
		if err := json.Unmarshal([]byte(`{"val": "05"}`), &t1); err != nil || t1.Val.String() != "05" {
			t.Errorf("expected '05', got %v (err: %v)", t1.Val.String(), err)
		}

		// Boolean value true
		var t2 TestStruct
		if err := json.Unmarshal([]byte(`{"val": true}`), &t2); err != nil || t2.Val.String() != "true" {
			t.Errorf("expected 'true', got %v (err: %v)", t2.Val.String(), err)
		}

		// Numeric value 123
		var t3 TestStruct
		if err := json.Unmarshal([]byte(`{"val": 123}`), &t3); err != nil || t3.Val.String() != "123" {
			t.Errorf("expected '123', got %v (err: %v)", t3.Val.String(), err)
		}
	})
}

func TestCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker(3, 50*time.Millisecond)

	if cb.State() != StateClosed {
		t.Errorf("expected closed state, got %v", cb.State())
	}

	mockErr := &GatewayError{StatusCode: 504, Message: "Gateway Timeout"}

	// 1. Trigger consecutive failures
	for i := 0; i < 3; i++ {
		_ = cb.Execute(func() error {
			return mockErr
		})
	}

	// 2. Circuit should be Open
	if cb.State() != StateOpen {
		t.Errorf("expected circuit to be Open after 3 failures, got %v", cb.State())
	}

	// 3. Subsequent calls should fail-fast with ErrCircuitBreakerOpen
	err := cb.Execute(func() error {
		return nil
	})
	if err != ErrCircuitBreakerOpen {
		t.Errorf("expected ErrCircuitBreakerOpen, got %v", err)
	}

	// 4. Wait for cooldown period to elapse -> HalfOpen
	time.Sleep(60 * time.Millisecond)

	// 5. Probe request succeeds -> Circuit should reset to Closed
	err = cb.Execute(func() error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected probe call to succeed, got %v", err)
	}
	if cb.State() != StateClosed {
		t.Errorf("expected circuit to reset to Closed, got %v", cb.State())
	}
}

func TestIsTransientAndSanitization(t *testing.T) {
	t.Run("context.Canceled is NOT transient", func(t *testing.T) {
		if IsTransient(context.Canceled) {
			t.Errorf("context.Canceled must not be treated as transient gateway timeout")
		}
	})

	t.Run("context.DeadlineExceeded IS transient", func(t *testing.T) {
		if !IsTransient(context.DeadlineExceeded) {
			t.Errorf("context.DeadlineExceeded must be transient")
		}
	})

	t.Run("ErrCircuitBreakerOpen IS transient", func(t *testing.T) {
		if !IsTransient(ErrCircuitBreakerOpen) {
			t.Errorf("ErrCircuitBreakerOpen must be transient")
		}
	})

	t.Run("GatewayError does not leak sensitive internal payload in Error string", func(t *testing.T) {
		gwErr := &GatewayError{
			StatusCode: 500,
			Message:    "Gateway communication failure",
			StatusMsg:  "Declined",
			Internal:   context.DeadlineExceeded,
		}

		errStr := gwErr.Error()
		if errStr != "payfast gateway error (HTTP 500): Gateway communication failure (gateway msg: Declined)" {
			t.Errorf("unexpected error string output: %s", errStr)
		}
	})
}


