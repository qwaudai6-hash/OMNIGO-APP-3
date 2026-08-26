package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// JazzCashService implements the live JazzCash hosted checkout / mobile wallet
// flow. The frontend loads the redirect URL in a WebView or redirects the
// browser. Callback is verified via HMAC using the integrity salt.
type JazzCashService struct {
	merchantID    string
	password      string
	integritySalt string
	apiURL        string
	returnURL     string
	httpClient    *http.Client
}

func NewJazzCashService(merchantID, password, integritySalt, apiURL string) *JazzCashService {
	if apiURL == "" {
		apiURL = "https://payments.jazzcash.com.pk/CustomerPortal/transactionmanagement/merchantform"
	}
	return &JazzCashService{
		merchantID:    merchantID,
		password:      password,
		integritySalt: integritySalt,
		apiURL:        apiURL,
		httpClient:    &http.Client{Timeout: 20 * time.Second},
	}
}

func (s *JazzCashService) IsConfigured() bool {
	return s.merchantID != "" && s.integritySalt != "" && s.password != ""
}

// CreateCheckoutSession builds the JazzCash form payload and returns the
// redirect URL. The frontend must POST these fields or we redirect via GET.
func (s *JazzCashService) CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (CheckoutResponse, error) {
	if !s.IsConfigured() {
		return CheckoutResponse{}, errors.New("jazzcash is not configured")
	}

	txnRefNo := "JC" + strconv.FormatInt(time.Now().Unix(), 10)
	// JazzCash txn refs are limited in length; append only the last 4 chars
	// of the order ID and never panic on short IDs.
	if len(req.OrderID) > 4 {
		txnRefNo += req.OrderID[len(req.OrderID)-4:]
	}
	amountPaisa := fmt.Sprintf("%.0f", req.Amount*100)
	txnDateTime := time.Now().UTC().Format("20060102150405")

	payload := map[string]string{
		"pp_Version":        "1.1",
		"pp_TxnType":        "MWALLET",
		"pp_Language":       "EN",
		"pp_MerchantID":     s.merchantID,
		"pp_Password":       s.password,
		"pp_TxnRefNo":       txnRefNo,
		"pp_Amount":         amountPaisa,
		"pp_TxnCurrency":    currencyOrDefault(req.Currency, "PKR"),
		"pp_TxnDateTime":    txnDateTime,
		"pp_BillReference":  req.OrderID,
		"pp_Description":    "OMNIGO Order " + req.OrderID,
		"pp_CustomerEmail":  req.CustomerEmail,
		"pp_CustomerMobile": req.CustomerPhone,
		"pp_ReturnURL":      req.ReturnURL,
		"ppmpf_1":           req.CustomerID,
		"ppmpf_2":           req.OrderID,
	}
	if payload["pp_CustomerEmail"] == "" {
		return CheckoutResponse{}, fmt.Errorf("jazzcash checkout requires customer email address")
	}
	if payload["pp_CustomerMobile"] == "" {
		return CheckoutResponse{}, fmt.Errorf("jazzcash checkout requires customer mobile number")
	}

	secureHash := s.createSecureHash(payload)
	payload["pp_SecureHash"] = secureHash

	formData := url.Values{}
	for k, v := range payload {
		formData.Set(k, v)
	}

	return CheckoutResponse{
		Gateway:     "jazzcash",
		SessionID:   txnRefNo,
		RedirectURL: s.apiURL + "?" + formData.Encode(),
	}, nil
}

// createSecureHash builds the JazzCash secure hash.
// Per docs: sorted alphabetically, concatenated with ampersands, then HMAC-SHA256
// with integritySalt as the key.
func (s *JazzCashService) createSecureHash(payload map[string]string) string {
	var keys []string
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+payload[k])
	}
	baseString := strings.Join(parts, "&")

	mac := hmac.New(sha256.New, []byte(s.integritySalt))
	mac.Write([]byte(baseString))
	return hex.EncodeToString(mac.Sum(nil))
}

// Refund is not available over the standard hosted checkout API and must be
// done through JazzCash merchant portal / API access if granted.
func (s *JazzCashService) Refund(ctx context.Context, transactionID string, amount float64) error {
	return errors.New("jazzcash refund must be processed via merchant portal; API refund not enabled")
}

// VerifyWebhook validates the JazzCash IPN callback.
func (s *JazzCashService) VerifyWebhook(payload []byte, signature string) (WebhookEvent, error) {
	if !s.IsConfigured() {
		return WebhookEvent{}, errors.New("jazzcash is not configured")
	}

	var data map[string]string
	if err := json.Unmarshal(payload, &data); err != nil {
		values, parseErr := url.ParseQuery(string(payload))
		if parseErr != nil {
			return WebhookEvent{}, fmt.Errorf("failed to parse jazzcash callback: %w", err)
		}
		data = map[string]string{}
		for k, v := range values {
			if len(v) > 0 {
				data[k] = v[0]
			}
		}
	}

	receivedHash := data["pp_SecureHash"]
	delete(data, "pp_SecureHash")

	expected := s.createSecureHash(data)
	if !hmac.Equal([]byte(receivedHash), []byte(expected)) {
		return WebhookEvent{}, errors.New("jazzcash signature mismatch")
	}

	status := "FAILED"
	if data["pp_ResponseCode"] == "000" {
		status = "SUCCESS"
	}

	amount := 0.0
	if a := data["pp_Amount"]; a != "" {
		var paisa int64
		fmt.Sscanf(a, "%d", &paisa)
		amount = float64(paisa) / 100.0
	}

	return WebhookEvent{
		OrderID:       data["pp_BillReference"],
		CustomerID:    data["ppmpf_1"],
		TransactionID: data["pp_TxnRefNo"],
		Status:        status,
		Amount:        amount,
		Currency:      data["pp_TxnCurrency"],
		Gateway:       "jazzcash",
	}, nil
}

func currencyOrDefault(c, def string) string {
	if c == "" {
		return def
	}
	return c
}

// nolint
var _ = io.ReadAll
