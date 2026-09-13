package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PayFastPKService implements the PaymentGateway interface for PayFast Pakistan
// (apps.net.pk hosted checkout). It acquires an OAuth access token via
// GetAccessToken, then redirects the customer to PostTransaction.
type PayFastPKService struct {
	merchantID   string
	securedKey   string
	merchantName string
	baseURL      string
	httpClient   *http.Client
}

// NewPayFastPKService constructs a PayFastPKService.
func NewPayFastPKService(merchantID, securedKey, merchantName, baseURL string) *PayFastPKService {
	baseURL = strings.TrimRight(baseURL, "/")
	return &PayFastPKService{
		merchantID:   merchantID,
		securedKey:   securedKey,
		merchantName: merchantName,
		baseURL:      baseURL,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *PayFastPKService) IsConfigured() bool {
	return s.merchantID != "" && s.securedKey != "" && s.baseURL != ""
}

type tokenResponse struct {
	AccessToken string `json:"ACCESS_TOKEN"`
	GeneratedDT string `json:"GENERATED_DATE_TIME"`
}

// fetchAccessToken calls PayFast GetAccessToken with form-encoded body per official PHP sample.
// Required fields: MERCHANT_ID, SECURED_KEY, BASKET_ID, TXNAMT, CURRENCY_CODE, APPLY_DISCOUNT.
func (s *PayFastPKService) fetchAccessToken(ctx context.Context, basketID string, amount float64) (string, error) {
	tokenURL := s.baseURL + "/Transaction/GetAccessToken"

	formData := url.Values{}
	formData.Set("MERCHANT_ID", s.merchantID)
	formData.Set("SECURED_KEY", s.securedKey)
	formData.Set("BASKET_ID", basketID)
	formData.Set("TXNAMT", fmt.Sprintf("%.2f", amount))
	formData.Set("CURRENCY_CODE", "PKR")
	formData.Set("APPLY_DISCOUNT", "true")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("payfast token request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("payfast token http: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("payfast token status %d: %s", resp.StatusCode, raw)
	}
	var tr tokenResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", fmt.Errorf("payfast token decode: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("payfast returned empty access token")
	}
	return tr.AccessToken, nil
}

func (s *PayFastPKService) CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (CheckoutResponse, error) {
	if !s.IsConfigured() {
		return CheckoutResponse{}, fmt.Errorf("payfast: not configured")
	}
	token, err := s.fetchAccessToken(ctx, req.OrderID, req.Amount)
	if err != nil {
		return CheckoutResponse{}, fmt.Errorf("payfast: token: %w", err)
	}

	orderDate := time.Now().Format("20060102")
	signature := s.computeSignature(req.OrderID, token)

	redirectURL := fmt.Sprintf(
		"%s/Transaction/PostTransaction?BASKET_ID=%s&TXNAMT=%.2f&CURRENCY_CODE=%s&SUCCESS_URL=%s&FAILURE_URL=%s&ACCESS_TOKEN=%s&MERCHANT_ID=%s&ORDER_DATE=%s&SIGNATURE=%s",
		s.baseURL,
		url.QueryEscape(req.OrderID),
		req.Amount,
		url.QueryEscape(req.Currency),
		url.QueryEscape(req.ReturnURL),
		url.QueryEscape(req.CancelURL),
		url.QueryEscape(token),
		url.QueryEscape(s.merchantID),
		orderDate,
		url.QueryEscape(signature),
	)
	return CheckoutResponse{
		Gateway:     "payfast",
		SessionID:   token,
		RedirectURL: redirectURL,
	}, nil
}

// PayFastGatewayError is a sentinel error type for PayFast refunds that
// require manual processing. The refund handler uses this to distinguish
// "gateway cannot refund automatically" from "actual gateway failure".
type PayFastGatewayError struct {
	TransactionID string
	Amount        float64
}

func (e *PayFastGatewayError) Error() string {
	return fmt.Sprintf("payfast: automated refunds not supported for transaction %s (amount %.2f); requires manual processing via merchant portal", e.TransactionID, e.Amount)
}

func (s *PayFastPKService) Refund(_ context.Context, transactionID string, amount float64) error {
	return &PayFastGatewayError{TransactionID: transactionID, Amount: amount}
}

// VerifyWebhook validates a PayFast IPN using the official SHA-256 formula:
// basket_id|merchant_secured_key|merchant_id|payfast_err_code
func (s *PayFastPKService) VerifyWebhook(payload []byte, signature string) (WebhookEvent, error) {
	var params map[string]string
	if err := json.Unmarshal(payload, &params); err != nil {
		return WebhookEvent{}, fmt.Errorf("payfast ipn: decode: %w", err)
	}
	basketID := params["BASKET_ID"]
	errCode := params["PAYFAST_ERR"]
	orderID := params["ORDER_ID"]
	txnStatus := params["TXN_STATUS"]
	txnID := params["TXN_ID"]
	amountStr := params["TXNAMT"]

	raw := basketID + "|" + s.securedKey + "|" + s.merchantID + "|" + errCode
	h := sha256.Sum256([]byte(raw))
	expected := strings.ToUpper(hex.EncodeToString(h[:]))
	received := strings.ToUpper(signature)
	if expected != received {
		return WebhookEvent{}, fmt.Errorf("payfast ipn: signature mismatch")
	}

	status := "FAILED"
	if txnStatus == "0000" || txnStatus == "00" {
		status = "SUCCESS"
	}

	var amount float64
	if _, err := fmt.Sscanf(amountStr, "%f", &amount); err != nil {
		return WebhookEvent{}, fmt.Errorf("payfast ipn: invalid amount %q: %w", amountStr, err)
	}
	amount = math.Round(amount*100) / 100

	return WebhookEvent{
		OrderID:       orderID,
		TransactionID: txnID,
		Status:        status,
		Amount:        amount,
		Currency:      "PKR",
		Gateway:       "payfast",
	}, nil
}

func (s *PayFastPKService) computeSignature(basketID, token string) string {
	raw := basketID + "|" + s.securedKey + "|" + s.merchantID + "|" + token
	h := sha256.Sum256([]byte(raw))
	return strings.ToUpper(hex.EncodeToString(h[:]))
}
