package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RaastService implements PayFast Raast P2M (instant bank transfer) via
// the SBP (1LINK) network. Raast enables 24/7 instant bank transfers with
// low MDR (~1%). The frontend loads the redirect URL in a WebView for
// bank selection and authentication.
type RaastService struct {
	merchantID    string
	securedKey    string
	apiURL        string
	returnURL     string
	httpClient    *http.Client
}

func NewRaastService(merchantID, securedKey, apiURL string) *RaastService {
	if apiURL == "" {
		// Default to PayFast Raast endpoint
		apiURL = "https://ipg1.apps.net.pk/Ecommerce/api/Transaction/Raast"
	}
	return &RaastService{
		merchantID: merchantID,
		securedKey: securedKey,
		apiURL:     apiURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (s *RaastService) IsConfigured() bool {
	return s.merchantID != "" && s.securedKey != ""
}

// CreateCheckoutSession initiates a Raast P2M transaction and returns
// the redirect URL for the hosted checkout flow.
func (s *RaastService) CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (CheckoutResponse, error) {
	if !s.IsConfigured() {
		return CheckoutResponse{}, errors.New("raast is not configured")
	}

	if req.Amount <= 0 {
		return CheckoutResponse{}, errors.New("raast: amount must be greater than zero")
	}

	txnRef := "RAST" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if len(txnRef) > 20 {
		txnRef = txnRef[:20]
	}

	amountStr := fmt.Sprintf("%.2f", req.Amount)

	// Build the Raast request payload
	payload := url.Values{}
	payload.Set("MERCHANT_ID", s.merchantID)
	payload.Set("TXN_REF", txnRef)
	payload.Set("TXN_AMOUNT", amountStr)
	payload.Set("CURRENCY_CODE", currencyOrDefault(req.Currency, "PKR"))
	payload.Set("ORDER_ID", req.OrderID)
	payload.Set("MERP_ID", s.merchantID)
	payload.Set("TOKEN", "NONE")
	payload.Set("VERSION", "1.0")
	payload.Set("SUCCESS_URL", req.ReturnURL)
	payload.Set("FAILURE_URL", req.CancelURL)
	payload.Set("CHECKOUT_URL", req.ReturnURL)

	// Generate signature
	signature := s.createSignature(payload)
	payload.Set("SIGNATURE", signature)

	// Build redirect URL
	redirectURL := s.apiURL + "?" + payload.Encode()

	return CheckoutResponse{
		Gateway:     "raast",
		SessionID:   txnRef,
		RedirectURL: redirectURL,
	}, nil
}

// createSignature generates HMAC-SHA256 signature for Raast requests
func (s *RaastService) createSignature(payload url.Values) string {
	// Sort keys alphabetically
	var keys []string
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Build canonical string: key=value&key=value...
	var parts []string
	for _, k := range keys {
		if k == "SIGNATURE" {
			continue
		}
		parts = append(parts, k+"="+payload.Get(k))
	}
	baseString := strings.Join(parts, "&")

	// HMAC-SHA256
	mac := hmac.New(sha256.New, []byte(s.securedKey))
	mac.Write([]byte(baseString))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhook verifies Raast callback signature and extracts event data
func (s *RaastService) VerifyWebhook(payload []byte, signature string) (WebhookEvent, error) {
	var callback RaastCallback
	if err := json.Unmarshal(payload, &callback); err != nil {
		return WebhookEvent{}, fmt.Errorf("failed to parse raast callback: %w", err)
	}

	if signature == "" {
		return WebhookEvent{}, errors.New("raast callback missing signature header")
	}
	if !s.verifyCallbackSignature(callback, signature) {
		return WebhookEvent{}, errors.New("raast callback signature mismatch")
	}

	// Map Raast status to our status
	status := "PENDING"
	switch callback.ResponseCode {
	case "00", "000", "0000":
		status = "SUCCESS"
	case "01", "02", "05", "51", "91":
		status = "FAILED"
	default:
		if strings.Contains(strings.ToLower(callback.ResponseMessage), "cancelled") {
			status = "FAILED"
		} else if strings.Contains(strings.ToLower(callback.ResponseMessage), "success") {
			status = "SUCCESS"
		}
	}

	return WebhookEvent{
		OrderID:       callback.OrderID,
		TransactionID:  callback.TxnRef,
		Status:         status,
		Amount:         callback.TxnAmount,
		Currency:       callback.CurrencyCode,
		Gateway:        "raast",
	}, nil
}

// verifyCallbackSignature verifies the HMAC signature from Raast callback
func (s *RaastService) verifyCallbackSignature(callback RaastCallback, signature string) bool {
	expected := s.createCallbackSignature(callback)
	return hmac.Equal([]byte(expected), []byte(signature))
}

// createCallbackSignature creates signature for callback verification
func (s *RaastService) createCallbackSignature(callback RaastCallback) string {
	baseString := fmt.Sprintf("%s|%s|%s|%.2f|%s",
		callback.MerchantID,
		callback.TxnRef,
		callback.OrderID,
		callback.TxnAmount,
		callback.ResponseCode,
	)
	mac := hmac.New(sha256.New, []byte(s.securedKey))
	mac.Write([]byte(baseString))
	return hex.EncodeToString(mac.Sum(nil))
}

// Refund processes a Raast refund. Note: Raast refunds are typically
// not supported as it's an instant transfer. This returns an error.
func (s *RaastService) Refund(ctx context.Context, transactionID string, amount float64) error {
	return errors.New("raast refunds must be processed manually via PayFast merchant portal")
}

// RaastCallback represents the callback payload from PayFast Raast
type RaastCallback struct {
	MerchantID    string  `json:"MERCHANT_ID"`
	TxnRef       string  `json:"TXN_REF"`
	OrderID      string  `json:"ORDER_ID"`
	TxnAmount    float64 `json:"TXN_AMOUNT"`
	CurrencyCode string  `json:"CURRENCY_CODE"`
	ResponseCode string  `json:"RESPONSE_CODE"`
	ResponseMessage string `json:"RESPONSE_MESSAGE"`
	TxnDateTime  string  `json:"TXN_DATETIME"`
	BankName     string  `json:"BANK_NAME,omitempty"`
	RaastID      string  `json:"RAAST_ID,omitempty"`
}
