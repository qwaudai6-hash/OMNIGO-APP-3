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

type QRService struct {
	merchantID    string
	securedKey    string
	apiURL        string
	httpClient    *http.Client
}

func NewQRService(merchantID, securedKey, apiURL string) *QRService {
	if apiURL == "" {
		apiURL = "https://ipg1.apps.net.pk/Ecommerce/api/Transaction/QR"
	}
	return &QRService{
		merchantID: merchantID,
		securedKey: securedKey,
		apiURL:     apiURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (s *QRService) IsConfigured() bool {
	return s.merchantID != "" && s.securedKey != ""
}

type QRGenerateRequest struct {
	OrderID    string
	Amount     float64
	Currency   string
	ReturnURL  string
	CancelURL  string
	QRType     string // STATIC or DYNAMIC
}

type QRGenerateResponse struct {
	Gateway     string `json:"gateway"`
	QRCode      string `json:"qr_code"`      // Base64 encoded QR image
	QRPayload   string `json:"qr_payload"`   // Raw QR string (for manual scanning)
	SessionID   string `json:"session_id"`
	ExpiresAt   string `json:"expires_at"`
	RedirectURL string `json:"redirect_url,omitempty"`
}

func (s *QRService) CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (CheckoutResponse, error) {
	if !s.IsConfigured() {
		return CheckoutResponse{}, errors.New("qr payment is not configured")
	}

	if req.Amount <= 0 {
		return CheckoutResponse{}, errors.New("qr: amount must be greater than zero")
	}

	txnRef := "QR" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if len(txnRef) > 20 {
		txnRef = txnRef[:20]
	}

	amountStr := fmt.Sprintf("%.2f", req.Amount)

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
	payload.Set("QR_TYPE", "DYNAMIC")

	signature := s.createSignature(payload)
	payload.Set("SIGNATURE", signature)

	redirectURL := s.apiURL + "?" + payload.Encode()

	return CheckoutResponse{
		Gateway:     "qr",
		SessionID:   txnRef,
		RedirectURL: redirectURL,
	}, nil
}

func (s *QRService) GenerateQR(ctx context.Context, req QRGenerateRequest) (QRGenerateResponse, error) {
	if !s.IsConfigured() {
		return QRGenerateResponse{}, errors.New("qr payment is not configured")
	}

	txnRef := "QR" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if len(txnRef) > 20 {
		txnRef = txnRef[:20]
	}

	amountStr := fmt.Sprintf("%.2f", req.Amount)

	payload := url.Values{}
	payload.Set("MERCHANT_ID", s.merchantID)
	payload.Set("TXN_REF", txnRef)
	payload.Set("TXN_AMOUNT", amountStr)
	payload.Set("CURRENCY_CODE", currencyOrDefault(req.Currency, "PKR"))
	payload.Set("ORDER_ID", req.OrderID)
	payload.Set("MERP_ID", s.merchantID)
	payload.Set("VERSION", "1.0")
	payload.Set("SUCCESS_URL", req.ReturnURL)
	payload.Set("FAILURE_URL", req.CancelURL)
	payload.Set("QR_TYPE", req.QRType)

	signature := s.createSignature(payload)
	payload.Set("SIGNATURE", signature)

	resp, err := s.httpClient.PostForm(s.apiURL+"/generate", payload)
	if err != nil {
		return QRGenerateResponse{}, fmt.Errorf("qr generation request failed: %w", err)
	}
	defer resp.Body.Close()

	var qrResp QRGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&qrResp); err != nil {
		return QRGenerateResponse{}, fmt.Errorf("failed to parse qr response: %w", err)
	}

	qrResp.SessionID = txnRef
	qrResp.Gateway = "qr"

	return qrResp, nil
}

func (s *QRService) createSignature(payload url.Values) string {
	var keys []string
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		if k == "SIGNATURE" {
			continue
		}
		parts = append(parts, k+"="+payload.Get(k))
	}
	baseString := strings.Join(parts, "&")

	mac := hmac.New(sha256.New, []byte(s.securedKey))
	mac.Write([]byte(baseString))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *QRService) VerifyWebhook(payload []byte, signature string) (WebhookEvent, error) {
	var callback QRCallback
	if err := json.Unmarshal(payload, &callback); err != nil {
		return WebhookEvent{}, fmt.Errorf("failed to parse qr callback: %w", err)
	}

	if signature == "" {
		return WebhookEvent{}, errors.New("qr callback missing signature header")
	}
	if !s.verifyCallbackSignature(callback, signature) {
		return WebhookEvent{}, errors.New("qr callback signature mismatch")
	}

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
		TransactionID: callback.TxnRef,
		Status:        status,
		Amount:        callback.TxnAmount,
		Currency:      callback.CurrencyCode,
		Gateway:       "qr",
	}, nil
}

func (s *QRService) verifyCallbackSignature(callback QRCallback, signature string) bool {
	expected := s.createCallbackSignature(callback)
	return hmac.Equal([]byte(expected), []byte(signature))
}

func (s *QRService) createCallbackSignature(callback QRCallback) string {
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

func (s *QRService) Refund(ctx context.Context, transactionID string, amount float64) error {
	return errors.New("qr refunds must be processed manually via PayFast merchant portal")
}

type QRCallback struct {
	MerchantID     string  `json:"MERCHANT_ID"`
	TxnRef        string  `json:"TXN_REF"`
	OrderID       string  `json:"ORDER_ID"`
	TxnAmount     float64 `json:"TXN_AMOUNT"`
	CurrencyCode  string  `json:"CURRENCY_CODE"`
	ResponseCode  string  `json:"RESPONSE_CODE"`
	ResponseMessage string `json:"RESPONSE_MESSAGE"`
	TxnDateTime   string  `json:"TXN_DATETIME"`
	QRType        string  `json:"QR_TYPE,omitempty"`
}
