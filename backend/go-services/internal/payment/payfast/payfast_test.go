package payfast

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

	t.Run("Official PayFast Email Worked Example - Validation Hash", func(t *testing.T) {
		// Input string sequence from official email: BAS-01|jdnkaabcks|102|000
		basketID := "BAS-01"
		secretKey := "jdnkaabcks"
		merchantID := "102"
		errCode := "000"

		calculatedHash := CalculateResponseValidationHash(basketID, secretKey, merchantID, errCode)
		expectedHash := "e8192a7554dd699975adf39619c703a492392edf5e416a61e183866ecdf6a2a2"

		if calculatedHash != expectedHash {
			t.Errorf("Official PayFast worked example mismatch! Expected %s, got %s", expectedHash, calculatedHash)
		}

		if !VerifyResponseHash(expectedHash, calculatedHash) {
			t.Errorf("VerifyResponseHash failed to verify exact hash")
		}
	})
}

func TestPayFastAuthAndTokenCache(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.WriteHeader(http.StatusOK)
			// Emulate APPS UAT response format with uppercase ACCESS_TOKEN
			w.Write([]byte(`{"MERCHANT_ID":"102","ACCESS_TOKEN":"uat_access_token_123","GENERATED_DATE_TIME":"2026-08-21 22:00:00"}`))
			return
		}
	}))
	defer ts.Close()

	client := NewClient("102", "ROTATED_ME_REMOVE_THIS_PAYFAST_UAT_KEY", "", "Test Merchant", ts.URL)

	ctx := context.Background()
	token, err := client.GetAuthToken(ctx, "127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if token != "uat_access_token_123" {
		t.Errorf("expected uat_access_token_123, got %s", token)
	}

	time.Sleep(10 * time.Millisecond)

	token2, err := client.GetAuthToken(ctx, "127.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if token2 != "uat_access_token_123" {
		t.Errorf("expected uat_access_token_123 from cache, got %s", token2)
	}
}

func TestIsSuccessCode(t *testing.T) {
	tests := []struct {
		code     string
		expected bool
	}{
		{"00", true},
		{"000", true},
		{" 00 ", true},
		{" 000 ", true},
		{"05", false},
		{"97", false},
		{"104", false},
		{"002", false},
		{"", false},
	}

	for _, tt := range tests {
		got := IsSuccessCode(tt.code)
		if got != tt.expected {
			t.Errorf("IsSuccessCode(%q) = %v; expected %v", tt.code, got, tt.expected)
		}
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

	client := NewClient("merchantID", "secretKey", "hashKey", "Test Merchant", ts.URL)

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

	client := NewClient("merchantID", "secretKey", "hashKey", "Test Merchant", ts.URL)

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
				json.NewEncoder(w).Encode(TemporaryTokenResponse{
					StatusCode:      "00",
					InstrumentToken: "inst_3ds",
					TransactionID:   "txn_3ds",
					Data3DSHTML:     "<html>Please authenticate 3DS</html>",
				})
			} else if r.FormValue("card_number") == "4111111111111111" {
				json.NewEncoder(w).Encode(TemporaryTokenResponse{
					StatusCode:      "00",
					InstrumentToken: "inst_no3ds",
					TransactionID:   "txn_no3ds",
					Data3DSHTML:     "",
				})
			} else {
				json.NewEncoder(w).Encode(TemporaryTokenResponse{
					StatusCode: "99",
					StatusMsg:  "Unknown Gateway Error",
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
					StatusMsg:     "Approved",
				})
			} else if r.FormValue("instrument_token") == "inst_no3ds" {
				json.NewEncoder(w).Encode(TokenizedTransactionResponse{
					StatusCode:    "00",
					TransactionID: "txn_no3ds",
					StatusMsg:     "Approved",
				})
			} else {
				json.NewEncoder(w).Encode(TokenizedTransactionResponse{
					StatusCode: "111",
					StatusMsg:  "Transaction already captured or duplicate",
				})
			}
			return
		}
	}))
	defer ts.Close()

	client := NewClient("merchantID", "secretKey", "hashKey", "Test Merchant", ts.URL)
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

		bClient := NewClient("merchantID", "secretKey", "hashKey", "Test Merchant", basketServer.URL)
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

		var t1 TestStruct
		if err := json.Unmarshal([]byte(`{"flag": true}`), &t1); err != nil || !t1.Flag.Bool() {
			t.Errorf("expected true, got %v (err: %v)", t1.Flag.Bool(), err)
		}

		var t2 TestStruct
		if err := json.Unmarshal([]byte(`{"flag": "true"}`), &t2); err != nil || !t2.Flag.Bool() {
			t.Errorf("expected true, got %v (err: %v)", t2.Flag.Bool(), err)
		}

		var t3 TestStruct
		if err := json.Unmarshal([]byte(`{"flag": false}`), &t3); err != nil || t3.Flag.Bool() {
			t.Errorf("expected false, got %v (err: %v)", t3.Flag.Bool(), err)
		}

		var t4 TestStruct
		if err := json.Unmarshal([]byte(`{"flag": "false"}`), &t4); err != nil || t4.Flag.Bool() {
			t.Errorf("expected false, got %v (err: %v)", t4.Flag.Bool(), err)
		}
	})

	t.Run("FlexibleString unmarshaling", func(t *testing.T) {
		type TestStruct struct {
			Val FlexibleString `json:"val"`
		}

		var t1 TestStruct
		if err := json.Unmarshal([]byte(`{"val": "05"}`), &t1); err != nil || t1.Val.String() != "05" {
			t.Errorf("expected '05', got %v (err: %v)", t1.Val.String(), err)
		}

		var t2 TestStruct
		if err := json.Unmarshal([]byte(`{"val": true}`), &t2); err != nil || t2.Val.String() != "true" {
			t.Errorf("expected 'true', got %v (err: %v)", t2.Val.String(), err)
		}

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

	for i := 0; i < 3; i++ {
		_ = cb.Execute(func() error {
			return mockErr
		})
	}

	if cb.State() != StateOpen {
		t.Errorf("expected circuit to be Open after 3 failures, got %v", cb.State())
	}

	err := cb.Execute(func() error {
		return nil
	})
	if err != ErrCircuitBreakerOpen {
		t.Errorf("expected ErrCircuitBreakerOpen, got %v", err)
	}

	time.Sleep(60 * time.Millisecond)

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

func TestCircuitBreakerPanicRecovery(t *testing.T) {
	cb := NewCircuitBreaker(1, 50*time.Millisecond)

	// Cause state to transition to Open
	_ = cb.Execute(func() error {
		return context.DeadlineExceeded
	})

	if cb.State() != StateOpen {
		t.Fatalf("expected circuit to be Open, got %v", cb.State())
	}

	time.Sleep(60 * time.Millisecond)

	// Now in HalfOpen probe, simulate panic in gateway handler
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("expected panic to bubble up")
			}
		}()

		_ = cb.Execute(func() error {
			panic("unexpected gateway nil pointer panic")
		})
	}()

	// Verify breaker is back to StateOpen and not deadlocked
	if cb.State() != StateOpen {
		t.Errorf("expected circuit to transition to Open after panic, got %v", cb.State())
	}
}

// A missing/unset PayFast config must NEVER panic at construction: services build
// this client unconditionally at startup and also serve non-PayFast traffic.
func TestNewClientGracefulDegradation(t *testing.T) {
	t.Run("Empty config does not panic and reports unconfigured", func(t *testing.T) {
		var client *Client
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("NewClient panicked on empty config: %v", r)
				}
			}()
			t.Setenv("PAYFAST_BASE_URL", "")
			t.Setenv("PAYFAST_API_URL", "")
			client = NewClient("", "", "", "", "")
		}()
		if client == nil {
			t.Fatal("expected non-nil client")
		}
		if client.IsConfigured() {
			t.Errorf("empty credentials must report IsConfigured()==false")
		}
		if _, err := client.GetAuthToken(context.Background(), "1.2.3.4"); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("expected ErrNotConfigured, got %v", err)
		}
	})

	t.Run("Credentials without base URL are unconfigured (no boot crash)", func(t *testing.T) {
		t.Setenv("PAYFAST_BASE_URL", "")
		t.Setenv("PAYFAST_API_URL", "")
		client := NewClient("mid", "skey", "", "Merchant", "")
		if client.IsConfigured() {
			t.Errorf("credentials without gateway URL must NOT report configured")
		}
	})

	t.Run("PAYFAST_API_URL alias satisfies configuration", func(t *testing.T) {
		t.Setenv("PAYFAST_MERCHANT_ID", "mid")
		t.Setenv("PAYFAST_SECURED_KEY", "skey")
		t.Setenv("PAYFAST_HASH_KEY", "hkey")
		t.Setenv("PAYFAST_MERCHANT_NAME", "Merchant")
		t.Setenv("PAYFAST_BASE_URL", "")
		t.Setenv("PAYFAST_API_URL", "https://ipg.gopayfast.com/")
		client := NewClientFromEnv()
		if !client.IsConfigured() {
			t.Errorf("PAYFAST_API_URL alias must satisfy IsConfigured()")
		}
	})

	t.Run("Explicit baseURL wins over env", func(t *testing.T) {
		t.Setenv("PAYFAST_BASE_URL", "https://from-env.example")
		client := NewClient("mid", "skey", "", "M", "https://explicit.example")
		if client.baseURL != "https://explicit.example" {
			t.Errorf("explicit arg should take precedence, got %q", client.baseURL)
		}
	})
}

func TestIsTransientGatewayStatuses(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		want       bool
	}{
		{"500 internal server error IS transient", 500, true},
		{"501 not implemented IS transient", 501, true},
		{"502 bad gateway IS transient", 502, true},
		{"503 service unavailable IS transient", 503, true},
		{"504 gateway timeout IS transient", 504, true},
		{"408 request timeout IS transient", 408, true},
		{"400 bad request is NOT transient", 400, false},
		{"401 unauthorized is NOT transient", 401, false},
		{"402 payment required is NOT transient", 402, false},
		{"422 unprocessable is NOT transient", 422, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &GatewayError{StatusCode: tc.statusCode, Message: "test"}
			if got := IsTransient(err); got != tc.want {
				t.Errorf("IsTransient(HTTP %d) = %v, want %v", tc.statusCode, got, tc.want)
			}
		})
	}
}

// Sensitive credentials (PAN/CVV/CNIC/OTP/instrument tokens) must never appear in any
// json.Marshal output of request structs — a single debug log of a marshaled request
// would otherwise leak cardholder data into logs.
func TestSensitiveRequestFieldsNotSerializable(t *testing.T) {
	t.Run("TemporaryTokenRequest", func(t *testing.T) {
		req := TemporaryTokenRequest{
			BasketID:      "ord_1",
			TxnAmt:        "100.00",
			CardNumber:    "4111222233334444",
			ExpiryMonth:   "12",
			ExpiryYear:    "2028",
			CVV:           "123",
			AccountNumber: "03001234567",
			CNICNumber:    "3520212345671",
		}
		b, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		out := string(b)
		for _, secret := range []string{"4111222233334444", "03001234567", "3520212345671"} {
			if strings.Contains(out, secret) {
				t.Errorf("sensitive value %q leaked in JSON output: %s", secret, out)
			}
		}
		for _, key := range []string{"card_number", "cvv", "expiry_month", "expiry_year", "account_number", "cnic_number"} {
			if strings.Contains(out, key) {
				t.Errorf("sensitive field key %q present in JSON output: %s", key, out)
			}
		}
	})

	t.Run("TokenizedTransactionRequest", func(t *testing.T) {
		req := TokenizedTransactionRequest{
			InstrumentToken: "instr_secret_token_abc",
			Otp:             "998877",
			BasketID:        "ord_1",
			TxnAmt:          "100.00",
		}
		b, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}
		out := string(b)
		if strings.Contains(out, "instr_secret_token_abc") {
			t.Errorf("instrument token leaked in JSON output: %s", out)
		}
		if strings.Contains(out, "998877") {
			t.Errorf("OTP leaked in JSON output: %s", out)
		}
	})
}
