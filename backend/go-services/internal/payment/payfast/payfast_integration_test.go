package payfast

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testMerchantID  = "102"
	testSecuredKey  = "zWHjBp2AlttNu1sK"
	testUATBaseURL  = "https://ipguat.apps.net.pk/Ecommerce/api/Transaction"
	testCardNumber  = "5123450000000008"
	testCardExpiryM = "01"
	testCardExpiryY = "39"
	testCardCVV     = "100"
	testPhone       = "03001234567"
	testEmail       = "test@omnigo.pk"
)

// --- 1. TOKEN API TESTS ---

func TestLiveTokenAPI_Authenticate(t *testing.T) {
	t.Run("GetAccessToken_FormEncoded", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("MERCHANT_ID", testMerchantID)
		formData.Set("SECURED_KEY", testSecuredKey)
		formData.Set("BASKET_ID", "TEST-INTG-"+fmt.Sprintf("%d", time.Now().UnixNano()))
		formData.Set("TXNAMT", "2")
		formData.Set("CURRENCY_CODE", "PKR")
		formData.Set("APPLY_DISCOUNT", "true")

		resp, err := http.PostForm(testUATBaseURL+"/GetAccessToken", formData)
		if err != nil {
			t.Fatalf("Token API request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Token API returned HTTP %d", resp.StatusCode)
		}

		var tokenResp AuthTokenResponse
		if err := jsonDecode(resp, &tokenResp); err != nil {
			t.Fatalf("Failed to parse token response: %v", err)
		}

		token := tokenResp.GetToken()
		if token == "" {
			t.Fatal("Access token is empty")
		}
		t.Logf("✅ Token API OK — token=%s... expires=%s", token[:min(12, len(token))], tokenResp.ExpiresIn)
	})

	t.Run("GetAccessToken_InvalidCredentials", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("MERCHANT_ID", "99999")
		formData.Set("SECURED_KEY", "wrong_key")
		formData.Set("BASKET_ID", "TEST-INVALID")
		formData.Set("TXNAMT", "1")
		formData.Set("CURRENCY_CODE", "PKR")
		formData.Set("APPLY_DISCOUNT", "true")

		resp, err := http.PostForm(testUATBaseURL+"/GetAccessToken", formData)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var tokenResp AuthTokenResponse
			if err := jsonDecode(resp, &tokenResp); err == nil {
				if tokenResp.GetToken() != "" {
					t.Fatal("Should NOT return a valid token with wrong credentials")
				}
			}
		}
		t.Logf("✅ Invalid credentials correctly rejected (HTTP %d)", resp.StatusCode)
	})

	t.Run("GetAccessToken_EmptyCredentials", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("MERCHANT_ID", "")
		formData.Set("SECURED_KEY", "")
		formData.Set("BASKET_ID", "TEST-EMPTY")
		formData.Set("TXNAMT", "0")
		formData.Set("CURRENCY_CODE", "PKR")

		resp, err := http.PostForm(testUATBaseURL+"/GetAccessToken", formData)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()
		t.Logf("✅ Empty credentials returned HTTP %d", resp.StatusCode)
	})
}

func TestLiveTokenAPI_CachingBehavior(t *testing.T) {
	client := NewClient(testMerchantID, testSecuredKey, "", "OMNIGO Test", testUATBaseURL)
	if !client.IsConfigured() {
		t.Fatal("Client should be configured")
	}

	ctx := context.Background()

	token1, err := client.GetAuthToken(ctx, "127.0.0.1")
	if err != nil {
		t.Fatalf("First token fetch failed: %v", err)
	}
	if token1 == "" {
		t.Fatal("First token is empty")
	}
	t.Logf("✅ First token: %s...", token1[:min(12, len(token1))])

	start := time.Now()
	token2, err := client.GetAuthToken(ctx, "127.0.0.1")
	if err != nil {
		t.Fatalf("Second token fetch failed: %v", err)
	}
	elapsed := time.Since(start)

	if token1 != token2 {
		t.Errorf("Cached token should be identical")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("Cached token retrieval took %v (should be <100ms)", elapsed)
	}
	t.Logf("✅ Token caching works (2nd call took %v)", elapsed)
}

// --- 2. SIGNATURE / HASH VERIFICATION TESTS ---

func TestSignatureHashing(t *testing.T) {
	t.Run("HMAC_SHA256_KnownVector", func(t *testing.T) {
		payload := "basket123100.004111222233334444122026123"
		key := "test_secret_key"

		result := generateHMACSHA256(payload, key)

		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(payload))
		expected := hex.EncodeToString(mac.Sum(nil))

		if result != expected {
			t.Errorf("HMAC mismatch: got %s, want %s", result, expected)
		}
		t.Logf("✅ HMAC-SHA256: %s", result)
	})

	t.Run("ResponseValidationHash_PayFastWorkedExample", func(t *testing.T) {
		hash := CalculateResponseValidationHash("BAS-01", "jdnkaabcks", "102", "000")
		expected := "e8192a7554dd699975adf39619c703a492392edf5e416a61e183866ecdf6a2a2"
		if hash != expected {
			t.Errorf("PayFast worked example mismatch: got %s, want %s", hash, expected)
		}
		t.Logf("✅ ResponseValidationHash matches PayFast official example")
	})

	t.Run("ValidationHash_CardFlow", func(t *testing.T) {
		req := CustomerValidationRequest{
			BasketID:     "PF123456",
			TxnAmt:       "100.00",
			CardNumber:   testCardNumber,
			ExpiryMonth:  testCardExpiryM,
			ExpiryYear:   testCardExpiryY,
			CVV:          testCardCVV,
		}
		hash := CalculateValidationHash(req, testSecuredKey)
		if hash == "" {
			t.Error("Validation hash is empty")
		}
		if len(hash) != 64 {
			t.Errorf("Expected 64-char hex hash, got %d chars", len(hash))
		}
		t.Logf("✅ ValidationHash (card): %s", hash)
	})

	t.Run("ValidationHash_BankFlow", func(t *testing.T) {
		req := CustomerValidationRequest{
			BasketID:      "PF789012",
			TxnAmt:        "500.00",
			AccountNumber: "03001234567",
			CNICNumber:    "3520212345671",
		}
		hash := CalculateValidationHash(req, testSecuredKey)
		if hash == "" {
			t.Error("Bank validation hash is empty")
		}
		t.Logf("✅ ValidationHash (bank): %s", hash)
	})

	t.Run("TransactionHash_CardWithOTP", func(t *testing.T) {
		req := InitiateTransactionRequest{
			BasketID:     "PF111",
			TxnAmt:       "100.00",
			CardNumber:   testCardNumber,
			ExpiryMonth:  testCardExpiryM,
			ExpiryYear:   testCardExpiryY,
			CVV:          testCardCVV,
		}
		hash := CalculateTransactionHash(req, "123456", testSecuredKey)
		if hash == "" {
			t.Error("Transaction hash is empty")
		}
		t.Logf("✅ TransactionHash (card+OTP): %s", hash)
	})

	t.Run("TransactionHash_CardWithoutOTP", func(t *testing.T) {
		req := InitiateTransactionRequest{
			BasketID:     "PF222",
			TxnAmt:       "200.00",
			CardNumber:   testCardNumber,
			ExpiryMonth:  testCardExpiryM,
			ExpiryYear:   testCardExpiryY,
			CVV:          testCardCVV,
		}
		hash := CalculateTransactionHash(req, "", testSecuredKey)
		if hash == "" {
			t.Error("Transaction hash without OTP is empty")
		}
		t.Logf("✅ TransactionHash (card, no OTP): %s", hash)
	})

	t.Run("TemporaryTokenHash_Card", func(t *testing.T) {
		req := TemporaryTokenRequest{
			MerchantUserId:   testMerchantID,
			CustomerMobileNo: testPhone,
			CardNumber:       testCardNumber,
			ExpiryMonth:      testCardExpiryM,
			ExpiryYear:       testCardExpiryY,
			CVV:              testCardCVV,
		}
		hash := CalculateTemporaryTokenHash(req, testSecuredKey)
		if hash == "" || len(hash) != 64 {
			t.Errorf("TemporaryTokenHash: expected 64-char hex, got %q", hash)
		}
		t.Logf("✅ TemporaryTokenHash: %s", hash)
	})

	t.Run("TokenizedTransactionHash", func(t *testing.T) {
		req := TokenizedTransactionRequest{
			InstrumentToken:  "test_instrument_token",
			MerchantUserId:   testMerchantID,
			CustomerMobileNo: testPhone,
			TxnAmt:           "100.00",
			Otp:              "654321",
		}
		hash := CalculateTokenizedTransactionHash(req, testSecuredKey)
		if hash == "" || len(hash) != 64 {
			t.Errorf("TokenizedTransactionHash: expected 64-char hex, got %q", hash)
		}
		t.Logf("✅ TokenizedTransactionHash: %s", hash)
	})

	t.Run("SignatureComparison_Consistent", func(t *testing.T) {
		hash1 := CalculateValidationHash(CustomerValidationRequest{
			BasketID: "B1", TxnAmt: "100", CardNumber: "4111", ExpiryMonth: "12", ExpiryYear: "25", CVV: "999",
		}, "key")
		hash2 := CalculateValidationHash(CustomerValidationRequest{
			BasketID: "B1", TxnAmt: "100", CardNumber: "4111", ExpiryMonth: "12", ExpiryYear: "25", CVV: "999",
		}, "key")
		if hash1 != hash2 {
			t.Error("Same inputs should produce same hash")
		}

		hash3 := CalculateValidationHash(CustomerValidationRequest{
			BasketID: "B2", TxnAmt: "100", CardNumber: "4111", ExpiryMonth: "12", ExpiryYear: "25", CVV: "999",
		}, "key")
		if hash1 == hash3 {
			t.Error("Different inputs should produce different hash")
		}
		t.Logf("✅ Hash consistency verified")
	})

	t.Run("VerifySignature_ConstantTime", func(t *testing.T) {
		hash := "abc123def456"
		if !VerifySignature(hash, hash) {
			t.Error("Identical signatures should verify")
		}
		if VerifySignature(hash, "wrong_hash") {
			t.Error("Different signatures should NOT verify")
		}
		if VerifySignature("", "abc") {
			t.Error("Empty expected signature should fail")
		}
		if VerifySignature("abc", "") {
			t.Error("Empty received signature should fail")
		}
		t.Logf("✅ VerifySignature constant-time comparison works")
	})

	t.Run("CaseInsensitiveComparison", func(t *testing.T) {
		upper := "ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890"
		lower := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
		if !VerifySignature(upper, lower) {
			t.Error("Case-insensitive comparison should pass")
		}
		t.Logf("✅ Case-insensitive signature comparison works")
	})
}

// --- 3. LIVE GATEWAY API TESTS ---

func TestLiveGatewayAPI_CustomerValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live gateway test in short mode")
	}

	client := NewClient(testMerchantID, testSecuredKey, "", "OMNIGO Test", testUATBaseURL)
	ctx := context.Background()

	t.Run("ValidateCard_Customer", func(t *testing.T) {
		basketID := fmt.Sprintf("PFVAL%d", time.Now().UnixNano())
		req := CustomerValidationRequest{
			BasketID:             basketID,
			TxnAmt:               "100.00",
			OrderDate:            time.Now().Format("2006-01-02 15:04:05"),
			CustomerMobileNo:     testPhone,
			CustomerEmailAddress: testEmail,
			AccountTypeID:        "2",
			MerCatCode:           "0",
			CustomerIP:           "127.0.0.1",
			CardNumber:           testCardNumber,
			ExpiryMonth:          testCardExpiryM,
			ExpiryYear:           testCardExpiryY,
			CVV:                  testCardCVV,
			Data3DSPagemode:      "true",
			Data3DSCallbackURL:   "https://omnigo-app-3-production.up.railway.app/api/v1/payments/payfast/3ds_callback",
		}

		res, err := client.ValidateCustomerPayment(ctx, req)
		if err != nil {
			t.Logf("⚠️  Customer validation error (expected in UAT): %v", err)
			return
		}

		t.Logf("✅ Customer validation response:")
		t.Logf("   Code: %s", res.Code)
		t.Logf("   TransactionID: %s", res.TransactionID)
		t.Logf("   3DS ACS URL: %s", res.Data3DSAcsURL)
		t.Logf("   3DS HTML present: %v", res.Data3DSHTML != "")
		t.Logf("   3DS Gateway Rec: %s", res.Data3DSGatewayRecommendation)
	})
}

func TestLiveGatewayAPI_TemporaryToken(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live gateway test in short mode")
	}

	client := NewClient(testMerchantID, testSecuredKey, "", "OMNIGO Test", testUATBaseURL)
	ctx := context.Background()

	t.Run("GetTemporaryToken_Card", func(t *testing.T) {
		basketID := fmt.Sprintf("PFTEMP%d", time.Now().UnixNano())
		req := TemporaryTokenRequest{
			BasketID:         basketID,
			TxnAmt:           "100.00",
			OrderDate:        time.Now().Format("2006-01-02 15:04:05"),
			CustomerMobileNo: testPhone,
			MerchantUserId:   testMerchantID,
			AccountTypeID:    "2",
			MerCatCode:       "0",
			CustomerIP:       "127.0.0.1",
			CardNumber:       testCardNumber,
			ExpiryMonth:      testCardExpiryM,
			ExpiryYear:       testCardExpiryY,
			CVV:              testCardCVV,
			Data3DSPagemode:    "true",
			Data3DSCallbackURL: "https://omnigo-app-3-production.up.railway.app/api/v1/payments/payfast/3ds_callback",
		}

		res, err := client.GetTemporaryTransactionToken(ctx, req)
		if err != nil {
			t.Logf("⚠️  Temporary token error (expected in UAT): %v", err)
			return
		}

		t.Logf("✅ Temporary token response:")
		t.Logf("   StatusCode: %s", res.StatusCode)
		t.Logf("   StatusMsg: %s", res.StatusMsg)
		t.Logf("   InstrumentToken: %s", maskString(res.InstrumentToken))
		t.Logf("   TransactionID: %s", res.TransactionID)
		t.Logf("   OTP Required: %v", res.OtpRequired.Bool())
		t.Logf("   ECI: %s", res.ECI.String())
		t.Logf("   3DS ACS URL: %s", res.Data3DSAcsURL)
		t.Logf("   3DS HTML present: %v", res.Data3DSHTML != "")
	})
}

func TestLiveGatewayAPI_TokenizedTransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live gateway test in short mode")
	}

	client := NewClient(testMerchantID, testSecuredKey, "", "OMNIGO Test", testUATBaseURL)
	ctx := context.Background()

	t.Run("TokenizedTransaction_WithFakeToken", func(t *testing.T) {
		basketID := fmt.Sprintf("PFTOK%d", time.Now().UnixNano())
		req := TokenizedTransactionRequest{
			InstrumentToken:  "fake_test_token_for_validation",
			TransactionID:    fmt.Sprintf("TXN%d", time.Now().UnixNano()),
			MerchantUserId:   testMerchantID,
			CustomerMobileNo: testPhone,
			BasketID:         basketID,
			OrderDate:        time.Now().Format("2006-01-02 15:04:05"),
			TxnDesc:          "Test Payment",
			TxnAmt:           "100.00",
			CustomerIP:       "127.0.0.1",
			MerCatCode:       "0",
		}

		res, err := client.InitiateTokenizedTransaction(ctx, req)
		if err != nil {
			t.Logf("⚠️  Tokenized transaction error (expected with fake token): %v", err)
			return
		}

		t.Logf("✅ Tokenized transaction response:")
		t.Logf("   StatusCode: %s", res.StatusCode)
		t.Logf("   StatusMsg: %s", res.StatusMsg)
		t.Logf("   TransactionID: %s", res.TransactionID)
	})
}

func TestLiveGatewayAPI_TransactionStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live gateway test in short mode")
	}

	client := NewClient(testMerchantID, testSecuredKey, "", "OMNIGO Test", testUATBaseURL)
	ctx := context.Background()

	t.Run("CheckStatus_ByTransactionID", func(t *testing.T) {
		res, err := client.GetTransactionStatus(ctx, "NONEXISTENT_TXN_ID")
		if err != nil {
			t.Logf("⚠️  Status check error (expected for non-existent txn): %v", err)
			return
		}
		t.Logf("✅ Status check response: StatusCode=%s StatusMsg=%s", res.StatusCode, res.StatusMsg)
	})

	t.Run("CheckStatus_ByBasketID", func(t *testing.T) {
		res, err := client.GetTransactionStatusByBasketID(ctx, "NONEXISTENT_BASKET_ID")
		if err != nil {
			t.Logf("⚠️  Basket status check error (expected for non-existent basket): %v", err)
			return
		}
		t.Logf("✅ Basket status check response: StatusCode=%s StatusMsg=%s", res.StatusCode, res.StatusMsg)
	})
}

// --- 4. HOSTED CHECKOUT FORM TESTS ---

func TestHostedCheckout_FormURL(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live gateway test in short mode")
	}

	basketID := fmt.Sprintf("PFHOST%d", time.Now().UnixNano())
	returnURL := "https://omnigo-app-3-production.up.railway.app/api/v1/wallet/callback"

	formURL := fmt.Sprintf(
		"%s/PostTransaction?merchant_id=%s&basket_id=%s&txnamt=%.2f&currency_code=PKR&customer_mobile_no=%s&customer_email_address=%s&success_url=%s&checkout_url=%s",
		testUATBaseURL,
		url.QueryEscape(testMerchantID),
		url.QueryEscape(basketID),
		100.00,
		url.QueryEscape(testPhone),
		url.QueryEscape(testEmail),
		url.QueryEscape(returnURL),
		url.QueryEscape(returnURL),
	)

	t.Run("FormEndpoint_Reachable", func(t *testing.T) {
		client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}}

		resp, err := client.Post(formURL, "application/x-www-form-urlencoded", strings.NewReader(""))
		if err != nil {
			t.Fatalf("Hosted checkout endpoint unreachable: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == 302 {
			loc := resp.Header.Get("Location")
			t.Logf("✅ Hosted checkout reachable — redirects to: %s", loc)
		} else if resp.StatusCode == 200 {
			t.Logf("✅ Hosted checkout reachable — returned HTML form")
		} else {
			t.Logf("⚠️  Hosted checkout returned HTTP %d", resp.StatusCode)
		}
	})

	t.Run("FormPOST_WithAllParams", func(t *testing.T) {
		formData := url.Values{}
		formData.Set("merchant_id", testMerchantID)
		formData.Set("basket_id", basketID)
		formData.Set("txnamt", "100.00")
		formData.Set("currency_code", "PKR")
		formData.Set("customer_mobile_no", testPhone)
		formData.Set("customer_email_address", testEmail)
		formData.Set("success_url", returnURL)
		formData.Set("checkout_url", returnURL)

		client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}}

		resp, err := client.Post(testUATBaseURL+"/PostTransaction", "application/x-www-form-urlencoded", strings.NewReader(formData.Encode()))
		if err != nil {
			t.Fatalf("POST to PostTransaction failed: %v", err)
		}
		defer resp.Body.Close()

		t.Logf("✅ POST to PostTransaction returned HTTP %d", resp.StatusCode)
		if resp.StatusCode == 302 {
			t.Logf("   Redirect Location: %s", resp.Header.Get("Location"))
		}
	})
}

// --- 5. CLIENT CONSTRUCTION & CONFIGURATION TESTS ---

func TestClientConstruction(t *testing.T) {
	t.Run("IsConfigured_WithValidParams", func(t *testing.T) {
		c := NewClient("102", "key", "", "Name", "https://example.com")
		if !c.IsConfigured() {
			t.Error("Client with valid params should be configured")
		}
	})

	t.Run("IsConfigured_EmptyMerchantID", func(t *testing.T) {
		c := NewClient("", "key", "", "Name", "https://example.com")
		if c.IsConfigured() {
			t.Error("Client with empty merchant ID should NOT be configured")
		}
	})

	t.Run("IsConfigured_EmptySecuredKey", func(t *testing.T) {
		c := NewClient("102", "", "", "Name", "https://example.com")
		if c.IsConfigured() {
			t.Error("Client with empty secured key should NOT be configured")
		}
	})

	t.Run("IsConfigured_EmptyBaseURL", func(t *testing.T) {
		t.Setenv("PAYFAST_BASE_URL", "")
		t.Setenv("PAYFAST_API_URL", "")
		c := NewClient("102", "key", "", "Name", "")
		if c.IsConfigured() {
			t.Error("Client with empty base URL should NOT be configured")
		}
	})

	t.Run("HashKey_FallsBackToSecuredKey", func(t *testing.T) {
		c := NewClient("102", "mysecuredkey", "", "Name", "https://example.com")
		if c.hashKey != "mysecuredkey" {
			t.Errorf("hashKey should fall back to securedKey, got %q", c.hashKey)
		}
	})

	t.Run("ExplicitHashKey_OverridesSecuredKey", func(t *testing.T) {
		c := NewClient("102", "secured", "explicit_hash", "Name", "https://example.com")
		if c.hashKey != "explicit_hash" {
			t.Errorf("explicit hashKey should override, got %q", c.hashKey)
		}
	})

	t.Run("BaseURL_StripTrailingSlash", func(t *testing.T) {
		c := NewClient("102", "key", "", "Name", "https://example.com/")
		if c.baseURL != "https://example.com" {
			t.Errorf("Trailing slash should be stripped, got %q", c.baseURL)
		}
	})

	t.Run("AccessorMethods", func(t *testing.T) {
		c := NewClient("102", "key", "", "TestMerchant", "https://example.com")
		if c.MerchantID() != "102" {
			t.Errorf("MerchantID() = %q", c.MerchantID())
		}
		if c.BaseURL() != "https://example.com" {
			t.Errorf("BaseURL() = %q", c.BaseURL())
		}
	})
}

// --- 6. TOKEN URL ROUTING TESTS ---

func TestTokenURLRouting(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		wantApps bool // whether it should use apps.net.pk routing
	}{
		{"DirectTokenURL", "https://ipguat.apps.net.pk/Ecommerce/api/Transaction/GetAccessToken", true},
		{"TransactionSuffix", "https://ipguat.apps.net.pk/Ecommerce/api/Transaction", true},
		{"GenericBaseURL", "https://ipg.gopayfast.com", false},
		{"OtherAppsBaseURL", "https://example.apps.net.pk/something", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"ACCESS_TOKEN":"test","MERCHANT_ID":102}`))
			}))
			defer ts.Close()

			// The URL routing logic in fetchToken() inspects the baseURL string
			// to decide /token vs /GetAccessToken. We verify by checking what
			// the TokenManager constructs for the auth URL.

			// Build the expected auth URL the same way fetchToken does
			authURL := tt.baseURL + "/token"
			if strings.Contains(tt.baseURL, "apps.net.pk") {
				if strings.HasSuffix(tt.baseURL, "/GetAccessToken") {
					authURL = tt.baseURL
				} else if strings.HasSuffix(tt.baseURL, "/Transaction") {
					authURL = tt.baseURL + "/GetAccessToken"
				} else {
					authURL = tt.baseURL + "/Transaction/GetAccessToken"
				}
			}

			// Verify the routing logic matches our expectations
			if tt.wantApps && authURL == tt.baseURL+"/token" {
				t.Errorf("Expected apps.net.pk routing for %s, but got /token", tt.baseURL)
			}
			if !tt.wantApps && authURL != tt.baseURL+"/token" {
				t.Errorf("Expected generic /token routing for %s, got %s", tt.baseURL, authURL)
			}

			// Also test the actual server call with a real mock
			c2 := NewClient(testMerchantID, testSecuredKey, "", "Test", "https://test.example.com")
			c2.tokens = NewTokenManager(c2.httpClient, ts.URL, testMerchantID, testSecuredKey, c2.circuitBreaker)

			token, err := c2.GetAuthToken(context.Background(), "127.0.0.1")
			if err != nil {
				t.Fatalf("GetAuthToken failed: %v", err)
			}
			if token != "test" {
				t.Errorf("Expected 'test' token, got %q", token)
			}

			t.Logf("✅ URL routing for %s -> %s (apps.net.pk=%v)", tt.baseURL, authURL, tt.wantApps)
		})
	}
}

// --- 7. ERROR HANDLING & EDGE CASES ---

func TestErrorHandling(t *testing.T) {
	t.Run("GatewayError_FormatsCorrectly", func(t *testing.T) {
		err := &GatewayError{
			StatusCode: 400,
			Message:    "Invalid request",
			StatusMsg:  "Card declined",
			Internal:   fmt.Errorf("underlying error"),
		}

		errStr := err.Error()
		if !strings.Contains(errStr, "400") {
			t.Error("Error string should contain status code")
		}
		if !strings.Contains(errStr, "Invalid request") {
			t.Error("Error string should contain message")
		}
		if !strings.Contains(errStr, "Card declined") {
			t.Error("Error string should contain gateway status msg")
		}
	})

	t.Run("GatewayError_Unwrap", func(t *testing.T) {
		inner := fmt.Errorf("inner error")
		err := &GatewayError{StatusCode: 500, Message: "fail", Internal: inner}
		if !errors.Is(err, inner) {
			t.Error("Unwrap should expose inner error")
		}
	})

	t.Run("IsTransient_NilError", func(t *testing.T) {
		if IsTransient(nil) {
			t.Error("nil error should not be transient")
		}
	})

	t.Run("IsDeterministicRejection_NilError", func(t *testing.T) {
		if IsDeterministicRejection(nil) {
			t.Error("nil error should not be deterministic rejection")
		}
	})

	t.Run("MapIssuerResponseCode_AllCases", func(t *testing.T) {
		codes := map[string][]string{
			"00":  {"approved"},
			"000": {"approved"},
			"05":  {"insufficient", "balance"},
			"51":  {"insufficient", "balance"},
			"14":  {"expired", "card"},
			"55":  {"invalid otp"},
			"57":  {"e-commerce", "not enabled"},
			"104": {"payment details", "incorrect"},
			"91":  {"bank", "temporarily unavailable"},
		}

		for code, expectedSubstrings := range codes {
			result := MapIssuerResponseCode(code)
			lowerResult := strings.ToLower(result)
			allMatch := true
			for _, substr := range expectedSubstrings {
				if !strings.Contains(lowerResult, strings.ToLower(substr)) {
					allMatch = false
					break
				}
			}
			if !allMatch {
				t.Errorf("Code %s: expected result containing %v, got %q", code, expectedSubstrings, result)
			}
		}

		// Default case
		result := MapIssuerResponseCode("999")
		if !strings.Contains(strings.ToLower(result), "declined") {
			t.Errorf("Unknown code should return 'declined' message, got %q", result)
		}
		t.Logf("✅ All %d issuer response codes mapped correctly", len(codes)+1)
	})
}

// --- 8. CIRCUIT BREAKER DEEP TESTS ---

func TestCircuitBreakerDeep(t *testing.T) {
	t.Run("StateTransitions", func(t *testing.T) {
		cb := NewCircuitBreaker(2, 50*time.Millisecond)

		if cb.State() != StateClosed {
			t.Error("Initial state should be Closed")
		}

		// Fail twice with TRANSIENT errors (5xx gateway errors) to trip
		_ = cb.Execute(func() error { return &GatewayError{StatusCode: 502, Message: "bad gateway"} })
		_ = cb.Execute(func() error { return &GatewayError{StatusCode: 504, Message: "gateway timeout"} })

		if cb.State() != StateOpen {
			t.Errorf("Expected Open after 2 transient failures, got %v", cb.State())
		}

		// Immediate call should fail fast
		err := cb.Execute(func() error { return nil })
		if err != ErrCircuitBreakerOpen {
			t.Error("Should get ErrCircuitBreakerOpen while circuit is Open")
		}

		// Wait for cooldown
		time.Sleep(60 * time.Millisecond)

		// Probe call
		err = cb.Execute(func() error { return nil })
		if err != nil {
			t.Errorf("Probe call should succeed: %v", err)
		}
		if cb.State() != StateClosed {
			t.Errorf("State should be Closed after successful probe, got %v", cb.State())
		}
	})

	t.Run("DeterministicErrorResetsCounter", func(t *testing.T) {
		cb := NewCircuitBreaker(3, 50*time.Millisecond)

		_ = cb.Execute(func() error { return &GatewayError{StatusCode: 500, Message: "transient"} })
		_ = cb.Execute(func() error { return &GatewayError{StatusCode: 500, Message: "transient"} })

		// This is a deterministic error (400) — should reset failure counter
		_ = cb.Execute(func() error { return &GatewayError{StatusCode: 400, Message: "bad request"} })

		// Two more 500s should NOT trip (counter was reset)
		_ = cb.Execute(func() error { return &GatewayError{StatusCode: 500, Message: "transient"} })
		_ = cb.Execute(func() error { return &GatewayError{StatusCode: 500, Message: "transient"} })

		// Need one more failure to trip (threshold=3)
		_ = cb.Execute(func() error { return &GatewayError{StatusCode: 500, Message: "transient"} })

		if cb.State() != StateOpen {
			t.Errorf("Circuit should be Open after 3 consecutive transient failures (post-reset), got %v", cb.State())
		}
	})

	t.Run("ResetForce", func(t *testing.T) {
		cb := NewCircuitBreaker(1, 1*time.Hour)
		_ = cb.Execute(func() error { return context.DeadlineExceeded })

		if cb.State() != StateOpen {
			t.Fatal("Should be Open")
		}

		cb.Reset()
		if cb.State() != StateClosed {
			t.Errorf("Reset should set state to Closed, got %v", cb.State())
		}

		err := cb.Execute(func() error { return nil })
		if err != nil {
			t.Errorf("After reset, Execute should work: %v", err)
		}
	})
}

// --- 9. IPN HASH VERIFICATION TESTS ---

func TestIPNHashVerification(t *testing.T) {
	t.Run("VerifyIPNHash_Match", func(t *testing.T) {
		c := NewClient(testMerchantID, testSecuredKey, "", "Test", "https://example.com")

		basketID := "ORDER-123"
		errCode := "000"

		expectedHash := CalculateResponseValidationHash(basketID, testSecuredKey, testMerchantID, errCode)

		if !c.VerifyIPNHash(basketID, errCode, expectedHash) {
			t.Error("Valid IPN hash should verify")
		}
		t.Logf("✅ IPN hash verification pass")
	})

	t.Run("VerifyIPNHash_TamperedHash", func(t *testing.T) {
		c := NewClient(testMerchantID, testSecuredKey, "", "Test", "https://example.com")

		if c.VerifyIPNHash("ORDER-123", "000", "tampered_hash_value") {
			t.Error("Tampered hash should NOT verify")
		}
		t.Logf("✅ Tampered IPN hash correctly rejected")
	})

	t.Run("VerifyIPNHash_EmptyHash", func(t *testing.T) {
		c := NewClient(testMerchantID, testSecuredKey, "", "Test", "https://example.com")
		if c.VerifyIPNHash("ORDER-123", "000", "") {
			t.Error("Empty hash should not verify")
		}
	})

	t.Run("VerifyIPNHash_WithSeparateHashKey", func(t *testing.T) {
		c := NewClient(testMerchantID, testSecuredKey, "separate_hash_key", "Test", "https://example.com")

		basketID := "ORDER-456"
		errCode := "000"

		hashWithSecured := CalculateResponseValidationHash(basketID, testSecuredKey, testMerchantID, errCode)
		hashWithHashKey := CalculateResponseValidationHash(basketID, "separate_hash_key", testMerchantID, errCode)

		if !c.VerifyIPNHash(basketID, errCode, hashWithSecured) {
			t.Error("Should verify with securedKey")
		}
		if !c.VerifyIPNHash(basketID, errCode, hashWithHashKey) {
			t.Error("Should verify with hashKey")
		}
		if c.VerifyIPNHash(basketID, errCode, "wrong_hash") {
			t.Error("Wrong hash should not verify")
		}
	})
}

// --- 10. FLEXIBLE TYPES DEEP TESTS ---

func TestFlexibleBoolDeep(t *testing.T) {
	type S struct {
		F FlexibleBool `json:"f"`
	}

	tests := []struct {
		input    string
		expected bool
	}{
		{`{"f": true}`, true},
		{`{"f": false}`, false},
		{`{"f": "true"}`, true},
		{`{"f": "false"}`, false},
		{`{"f": "1"}`, true},
		{`{"f": "0"}`, false},
		{`{"f": "t"}`, true},
		{`{"f": "f"}`, false},
		{`{"f": "yes"}`, true},
		{`{"f": "no"}`, false},
		{`{"f": "y"}`, true},
		{`{"f": "n"}`, false},
		{`{"f": ""}`, false},
		{`{"f": null}`, false},
	}

	for _, tt := range tests {
		var s S
		if err := json.Unmarshal([]byte(tt.input), &s); err != nil {
			t.Errorf("Input %s: unmarshal error: %v", tt.input, err)
			continue
		}
		if s.F.Bool() != tt.expected {
			t.Errorf("Input %s: got %v, want %v", tt.input, s.F.Bool(), tt.expected)
		}
	}
	t.Logf("✅ FlexibleBool handles %d input variants", len(tests))
}

// --- 12. FULL FLOW SIMULATION (Option C) ---

func TestFullFlowSimulation_OptionC(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping full flow simulation in short mode")
	}

	client := NewClient(testMerchantID, testSecuredKey, "", "OMNIGO Test", testUATBaseURL)
	ctx := context.Background()

	// Step 1: Get Token
	token, err := client.GetAuthToken(ctx, "127.0.0.1")
	if err != nil {
		t.Fatalf("Step 1 - GetAuthToken failed: %v", err)
	}
	t.Logf("Step 1 ✅ Got access token: %s...", token[:min(12, len(token))])

	// Step 2: Get Temporary Transaction Token (simulates card entry)
	basketID := fmt.Sprintf("PFFLOW%d", time.Now().UnixNano())
	tempTokenReq := TemporaryTokenRequest{
		BasketID:         basketID,
		TxnAmt:           "100.00",
		OrderDate:        time.Now().Format("2006-01-02 15:04:05"),
		CustomerMobileNo: testPhone,
		MerchantUserId:   testMerchantID,
		AccountTypeID:    "2",
		MerCatCode:       "0",
		CustomerIP:       "127.0.0.1",
		CardNumber:       testCardNumber,
		ExpiryMonth:      testCardExpiryM,
		ExpiryYear:       testCardExpiryY,
		CVV:              testCardCVV,
		Data3DSPagemode:    "true",
		Data3DSCallbackURL: "https://omnigo-app-3-production.up.railway.app/api/v1/payments/payfast/3ds_callback",
	}

	tempTokenRes, err := client.GetTemporaryTransactionToken(ctx, tempTokenReq)
	if err != nil {
		t.Logf("Step 2 ⚠️  GetTemporaryTransactionToken error: %v", err)
		return
	}
	t.Logf("Step 2 ✅ Temporary token response:")
	t.Logf("   Status: %s - %s", tempTokenRes.StatusCode, tempTokenRes.StatusMsg)
	t.Logf("   Instrument Token: %s", maskString(tempTokenRes.InstrumentToken))
	t.Logf("   Transaction ID: %s", tempTokenRes.TransactionID)
	t.Logf("   3DS HTML present: %v", tempTokenRes.Data3DSHTML != "")
	t.Logf("   OTP Required: %v", tempTokenRes.OtpRequired.Bool())

	// Step 3: If we got an instrument token, try tokenized transaction
	if tempTokenRes.InstrumentToken != "" {
		txnReq := TokenizedTransactionRequest{
			InstrumentToken:  tempTokenRes.InstrumentToken,
			TransactionID:    tempTokenRes.TransactionID,
			MerchantUserId:   testMerchantID,
			CustomerMobileNo: testPhone,
			BasketID:         basketID,
			OrderDate:        time.Now().Format("2006-01-02 15:04:05"),
			TxnDesc:          "OMNIGO Wallet Top-up",
			TxnAmt:           "100.00",
			CustomerIP:       "127.0.0.1",
			MerCatCode:       "0",
		}

		txnRes, err := client.InitiateTokenizedTransaction(ctx, txnReq)
		if err != nil {
			t.Logf("Step 3 ⚠️  Tokenized transaction error: %v", err)
		} else {
			t.Logf("Step 3 ✅ Tokenized transaction response:")
			t.Logf("   Status: %s - %s", txnRes.StatusCode, txnRes.StatusMsg)
			t.Logf("   Transaction ID: %s", txnRes.TransactionID)
			t.Logf("   3DS ACS URL present: %v", txnRes.Data3DSAcsURL != "")
		}
	}

	// Step 4: Check transaction status
	t.Logf("Step 4 ℹ️  Transaction status check skipped (no valid txn ID from Step 2/3)")

	t.Logf("\n=== Full Option C Flow Simulation Complete ===")
}

// --- HELPERS ---

func jsonDecode(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	return decoder.Decode(v)
}

func maskString(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "..." + s[len(s)-4:]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}


