package service

import (
	"bytes"
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

// EasyPaisaService implements the live EasyPaisa hosted checkout / OTC / MA
// flow. Hash is generated with the configured hash key. Refund API is available
// only with direct Easypaisa integration and is stubbed accordingly.
type EasyPaisaService struct {
	storeID    string
	hashKey    string
	apiURL     string
	returnURL  string
	httpClient *http.Client
}

func NewEasyPaisaService(storeID, hashKey, apiURL string) *EasyPaisaService {
	if apiURL == "" {
		apiURL = "https://easypay.easypaisa.com.pk/easypay/Index.jsf"
	}
	return &EasyPaisaService{
		storeID:    storeID,
		hashKey:    hashKey,
		apiURL:     apiURL,
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

func (s *EasyPaisaService) IsConfigured() bool {
	return s.storeID != "" && s.hashKey != ""
}

// CreateCheckoutSession returns the EasyPaisa hosted checkout redirect URL.
func (s *EasyPaisaService) CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (CheckoutResponse, error) {
	if !s.IsConfigured() {
		return CheckoutResponse{}, errors.New("easypaisa is not configured")
	}

	txnRef := "EP" + strconv.FormatInt(time.Now().Unix(), 10)
	amountStr := fmt.Sprintf("%.2f", req.Amount)

	payload := map[string]string{
		"storeId":           s.storeID,
		"amount":            amountStr,
		"postBackURL":       req.ReturnURL,
		"orderRefNum":       req.OrderID,
		"merchantHashedReq": "",
		"paymentMethod":     "MA_PAYTYPE",
	}

	// Sort alphabetically for hash.
	var keys []string
	for k := range payload {
		keys = append(keys, k)
	}
	hash := createEasyPaisaHash(payload, s.hashKey)
	payload["merchantHashedReq"] = hash

	formData := url.Values{}
	for k, v := range payload {
		formData.Set(k, v)
	}

	return CheckoutResponse{
		Gateway:     "easypaisa",
		SessionID:   txnRef,
		RedirectURL: s.apiURL + "?" + formData.Encode(),
	}, nil
}

func createEasyPaisaHash(payload map[string]string, hashKey string) string {
	var keys []string
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+payload[k])
	}
	baseString := strings.Join(parts, "|")

	mac := hmac.New(sha256.New, []byte(hashKey))
	mac.Write([]byte(baseString))
	return hex.EncodeToString(mac.Sum(nil))
}

// Refund calls the live EasyPaisa merchant API to process a refund securely.
func (s *EasyPaisaService) Refund(ctx context.Context, transactionID string, amount float64) error {
	if !s.IsConfigured() {
		return errors.New("easypaisa is not configured")
	}

	amountStr := fmt.Sprintf("%.2f", amount)
	payload := map[string]string{
		"storeId":       s.storeID,
		"transactionId": transactionID,
		"refundAmount":  amountStr,
	}

	hash := createEasyPaisaHash(payload, s.hashKey)
	payload["merchantHashedReq"] = hash

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal refund request: %w", err)
	}

	refundURL := strings.Replace(s.apiURL, "Index.jsf", "api/refund", 1)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refundURL, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create refund request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("refund request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refund api returned status %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode refund response: %w", err)
	}

	if responseCode, ok := result["responseCode"].(string); !ok || responseCode != "0000" {
		return fmt.Errorf("refund failed with response code: %v", result["responseCode"])
	}

	return nil
}

// VerifyWebhook validates EasyPaisa callback by verifying the HMAC-SHA256
// signature against the merchant hash key. This prevents forged payment callbacks.
func (s *EasyPaisaService) VerifyWebhook(payload []byte, signature string) (WebhookEvent, error) {
	if !s.IsConfigured() {
		return WebhookEvent{}, errors.New("easypaisa is not configured")
	}

	var data map[string]string
	if err := json.Unmarshal(payload, &data); err != nil {
		values, parseErr := url.ParseQuery(string(payload))
		if parseErr != nil {
			return WebhookEvent{}, fmt.Errorf("failed to parse easypaisa callback: %w", err)
		}
		data = map[string]string{}
		for k, v := range values {
			if len(v) > 0 {
				data[k] = v[0]
			}
		}
	}

	// The hash never covers itself — drop it before recomputing.
	inBodySignature := data["merchantHashedReq"]
	delete(data, "merchantHashedReq")

	// Verify HMAC-SHA256 to prevent forged callbacks. The signature may
	// arrive via header or embedded in the payload as merchantHashedReq.
	expectedHash := createEasyPaisaHash(data, s.hashKey)
	if signature == "" && inBodySignature != "" {
		signature = inBodySignature
	}
	if signature == "" {
		return WebhookEvent{}, errors.New("missing easypaisa webhook signature")
	}
	if !hmac.Equal([]byte(expectedHash), []byte(signature)) {
		return WebhookEvent{}, errors.New("easypaisa webhook signature mismatch")
	}

	status := "FAILED"
	if data["transaction_status"] == "PAID" || data["responseCode"] == "0000" {
		status = "SUCCESS"
	}

	amount := 0.0
	if a := data["amount"]; a != "" {
		fmt.Sscanf(a, "%f", &amount)
	}

	return WebhookEvent{
		OrderID:       data["orderRefNum"],
		TransactionID: data["transactionId"],
		Status:        status,
		Amount:        amount,
		Currency:      "PKR",
		Gateway:       "easypaisa",
	}, nil
}

// nolint
var _ = io.ReadAll
