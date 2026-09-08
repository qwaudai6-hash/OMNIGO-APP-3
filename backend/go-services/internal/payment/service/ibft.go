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

type IBFTService struct {
	merchantID    string
	securedKey    string
	apiURL        string
	returnURL     string
	httpClient    *http.Client
}

func NewIBFTService(merchantID, securedKey, apiURL string) *IBFTService {
	if apiURL == "" {
		apiURL = "https://ipg1.apps.net.pk/Ecommerce/api/Transaction/IBFT"
	}
	return &IBFTService{
		merchantID: merchantID,
		securedKey: securedKey,
		apiURL:     apiURL,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func (s *IBFTService) IsConfigured() bool {
	return s.merchantID != "" && s.securedKey != ""
}

func (s *IBFTService) CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (CheckoutResponse, error) {
	if !s.IsConfigured() {
		return CheckoutResponse{}, errors.New("ibft is not configured")
	}

	if req.Amount <= 0 {
		return CheckoutResponse{}, errors.New("ibft: amount must be greater than zero")
	}

	txnRef := "IBFT" + strconv.FormatInt(time.Now().UnixNano(), 10)
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

	signature := s.createSignature(payload)
	payload.Set("SIGNATURE", signature)

	redirectURL := s.apiURL + "?" + payload.Encode()

	return CheckoutResponse{
		Gateway:     "ibft",
		SessionID:   txnRef,
		RedirectURL: redirectURL,
	}, nil
}

func (s *IBFTService) createSignature(payload url.Values) string {
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

func (s *IBFTService) VerifyWebhook(payload []byte, signature string) (WebhookEvent, error) {
	var callback IBFTCallback
	if err := json.Unmarshal(payload, &callback); err != nil {
		return WebhookEvent{}, fmt.Errorf("failed to parse ibft callback: %w", err)
	}

	if signature == "" {
		return WebhookEvent{}, errors.New("ibft callback missing signature header")
	}
	if !s.verifyCallbackSignature(callback, signature) {
		return WebhookEvent{}, errors.New("ibft callback signature mismatch")
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
		Gateway:       "ibft",
	}, nil
}

func (s *IBFTService) verifyCallbackSignature(callback IBFTCallback, signature string) bool {
	expected := s.createCallbackSignature(callback)
	return hmac.Equal([]byte(expected), []byte(signature))
}

func (s *IBFTService) createCallbackSignature(callback IBFTCallback) string {
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

func (s *IBFTService) Refund(ctx context.Context, transactionID string, amount float64) error {
	return errors.New("ibft refunds must be processed manually via PayFast merchant portal")
}

type IBFTCallback struct {
	MerchantID    string  `json:"MERCHANT_ID"`
	TxnRef       string  `json:"TXN_REF"`
	OrderID      string  `json:"ORDER_ID"`
	TxnAmount    float64 `json:"TXN_AMOUNT"`
	CurrencyCode string  `json:"CURRENCY_CODE"`
	ResponseCode string  `json:"RESPONSE_CODE"`
	ResponseMessage string `json:"RESPONSE_MESSAGE"`
	TxnDateTime  string  `json:"TXN_DATETIME"`
	BankName     string  `json:"BANK_NAME,omitempty"`
	AccountNumber string `json:"ACCOUNT_NUMBER,omitempty"`
	IBAN         string  `json:"IBAN,omitempty"`
}
