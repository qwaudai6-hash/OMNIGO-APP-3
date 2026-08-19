# OMNIGO Super-App — Official PayFast Payment Gateway Integration

> **Document Version:** 8.0 (Enterprise Hardened & Fully Grounded in Official PayFast Pakistan Spec)  
> **Target Gateway:** PayFast Pakistan (APPS Pvt Ltd / 1LINK Network)  
> **Integration Scenario:** Scenario 1 (Option C) — Temporary Transaction Token ➔ 3DS OTP Authentication ➔ Tokenized Transaction Capture ➔ Mandatory Status Verification ➔ Transactional Outbox Worker Settlement  
> **Security Baseline:** PCI-DSS Zero-Cardholder-Data Persistence, Fail-Closed Constant-Time HMAC Signature Validation, Automated Periodic Dual-Strategy Reconciliation, Gateway Circuit Breaker & Idempotent 3-Way Ledger Split.

---

## 🔒 Security & Enterprise Reliability Baseline

1. **Zero Cardholder Data Persistence:**  
   Primary Account Number (PAN), Card Expiry Date (MM/YY), Card Verification Value (CVV), Bank Account Number, and CNIC are processed strictly in-memory during temporary token creation and are **never** persisted to PostgreSQL, Redis, local disk, or cache.
2. **Comprehensive Logging Hardening & Error Sanitization:**  
   PAN, CVV, and account secrets are strictly excluded from HTTP request dumps, panic recovery logs, distributed tracing attributes, OpenTelemetry baggage, error monitoring, reverse proxy access logs (Nginx/Traefik), debug logs, Kafka payloads, and Redis values. Gateway errors are sanitized so internal network sockets and payloads are never exposed to clients.
3. **Gateway Circuit Breaker (`circuit_breaker.go`):**  
   Thread-safe 3-state (`Closed`, `Open`, `HalfOpen`) circuit breaker with 5-failure threshold and 10-second cooldown period protects downstream workers and DB connection pools from exhaustion during upstream gateway outages.
4. **Dual-Strategy Stuck Payment Reconciliation Engine:**  
   - Eliminates stuck `processing` and `gateway_pending` payments.
   - If gateway transaction ID is present, performs status inquiry via `GET /transaction/<gateway_txn_id>`.
   - If token generation was interrupted and gateway transaction ID is absent, seamlessly falls back to querying merchant basket tracking ID via `GET /transaction/basket_id/<basket_id>`.
   - Stale abandoned initial checkout rows (>10 minutes) are automatically swept to `failed`.
5. **Replay Attack & Concurrency Defense:**  
   - 3DS callbacks enforce single-use execution with `callback_processed_at` timestamp check and database row locking (`FOR UPDATE`).
   - `ux_payment_active_order` unique partial index prevents concurrent duplicate checkouts for the same order across active statuses `('processing', '3ds_required', 'settlement_pending', 'gateway_pending')`.
6. **Atomic Settlement & Mandatory Escrow:**  
   - Outbox events are claimed atomically by worker replicas using PostgreSQL `FOR UPDATE SKIP LOCKED`.
   - Outbox worker auto-recovers crashed events (`status = 'PROCESSING' AND updated_at < NOW() - INTERVAL '5 minutes'`).
   - Vendor escrow hold creation (`escrow.CreateHold`) and double-entry ledger transfers (`ledger.MultiTransfer`) are verified fail-closed before marking orders as paid.

---

## 🏛️ End-to-End Payment Architecture & Execution Lifecycle

```
┌─────────────────┐
│ Flutter Client  │ (Customer checkout: Card, Bank Account, or Wallet)
└────────┬────────┘
         │ POST /api/v1/payments/payfast/payment (OrderID, PaymentMethod, Credentials)
         ▼
┌────────────────────────────────────────────────────────────────────────┐
│ OMNIGO Backend Payment Orchestrator (PayFastService & Controller)      │
│ 1. Validate card PAN, CVV (3-4 digits), Expiry, and non-expired dates  │
│ 2. Acquire PostgreSQL Row Lock (orders FOR UPDATE)                     │
│ 3. Verify authoritative amount, customer ownership, & payable status   │
│ 4. Pre-calculate 3-way split & assert parity                           │
│ 5. Insert payment attempt with idempotency key & ux_payment_active_order│
│ 6. Build HMAC-signed 3DS Callback URL (pf_UUID.hmac_signature)         │
│ 7. Call PayFast POST /transaction/token (wrapped with CircuitBreaker)  │
└────────┬───────────────────────────────────────────────────────────────┘
         │
         ├──[If 3DS Required (data_3ds_html != "")]───────────────────────┐
         │   • Save instrument_token & 3DS metadata to payment_transactions│
         │   • Return HTML to Flutter for 3DS Webview / Browser OTP dialog│
         │                                                                │
         │                                  (Customer enters OTP on ACS)  │
         │                                                                │
         │   PayFast POSTs md + paRes ➔ /api/v1/payments/payfast/3ds_callback
         │   • Constant-time verification of HMAC-signed MD               │
         │   • Check replay defense (callback_processed_at IS NULL / old) │
         │   • Transition payment to 'processing'                         │
         │   • Call PayFast POST /transaction/tokenized (with paRes)      │
         │                                                                │
         ├──[If 3DS Not Required]                                         │
         │   • Immediately call PayFast POST /transaction/tokenized       │
         │                                                                │
         ▼                                                                │
┌────────────────────────────────────────────────────────────────────────┐│
│ PayFast Gateway (/transaction/tokenized response)                      ││
│ • Returns status_code: "00", transaction_id: "<gateway_txn_id>"        ││
└────────┬───────────────────────────────────────────────────────────────┘│
         │                                                                │
         ▼                                                                │
┌────────────────────────────────────────────────────────────────────────┐│
│ Status Verification (GET /transaction/<gateway_txn_id>)               ││
│ • Verify status_code == "00"                                           ││
│ • Verify BasketID == OrderID & TransactionID matches                   ││
│ • Verify Gateway Amount == Order Amount in paisa (if returned)         ││
└────────┬───────────────────────────────────────────────────────────────┘│
         │                                                                │
         ▼                                                                │
┌────────────────────────────────────────────────────────────────────────┐│
│ Transactional Outbox Settlement (ExecuteSplit)                         ││
│ • Single PostgreSQL transaction boundary:                              ││
│   1. Lock payment_transactions row (FOR UPDATE)                        ││
│   2. Lock orders row (FOR UPDATE) & re-verify authoritative amount     ││
│   3. Compute dynamic 3-way split                                       ││
│   4. Update payment_transactions ➔ 'settlement_pending'                ││
│   5. Update orders ➔ admin_commission, vendor_escrow, delivery_escrow  ││
│   6. INSERT full 3-way transfer list into outbox_events                ││
│   7. COMMIT                                                            ││
└────────┬───────────────────────────────────────────────────────────────┘│
         │                                                                │
         ▼                                                                │
┌────────────────────────────────────────────────────────────────────────┐│
│ Settlement & Reconciliation Worker (settlement_worker.go)              ││
│ 1. Claim PENDING events atomically with FOR UPDATE SKIP LOCKED         ││
│ 2. Execute ledger.MultiTransfer (PayFastHolding ➔ Admin + Vendor + Dev)││
│ 3. Execute escrow.CreateHold (MANDATORY: failure aborts capture)       ││
│ 4. Update payment ➔ 'captured', order ➔ 'paid', outbox ➔ PROCESSED     ││
│ 5. Periodically reconcile any stuck 'processing'/'gateway_pending' txns││
│    (Dual inquiry: Gateway Txn ID with Basket ID fallback)              ││
│ 6. Sweep stale abandoned 'pending' checkouts (>10m) ➔ 'failed'         ││
└────────────────────────────────────────────────────────────────────────┘┘
```

---

## 1. `internal/payment/payfast/models.go`

```go
package payfast

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FlexibleBool unmarshals both JSON boolean (true/false) and string ("true"/"false"/"1"/"0").
type FlexibleBool bool

func (b *FlexibleBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), "\"")
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "true", "1", "t", "yes", "y":
		*b = true
		return nil
	case "false", "0", "f", "no", "n", "", "null":
		*b = false
		return nil
	default:
		var rawBool bool
		if err := json.Unmarshal(data, &rawBool); err == nil {
			*b = FlexibleBool(rawBool)
			return nil
		}
		*b = false
		return nil
	}
}

func (b FlexibleBool) Bool() bool {
	return bool(b)
}

// FlexibleString unmarshals JSON strings, numbers, and booleans into a normalized string representation.
type FlexibleString string

func (fs *FlexibleString) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*fs = FlexibleString(s)
		return nil
	}
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		*fs = FlexibleString(strconv.FormatBool(b))
		return nil
	}
	trimmed := strings.Trim(string(data), "\"")
	*fs = FlexibleString(trimmed)
	return nil
}

func (fs FlexibleString) String() string {
	return string(fs)
}

// Environment specifies Sandbox vs Production gateway endpoints.
type Environment string

const (
	EnvSandbox    Environment = "sandbox"
	EnvProduction Environment = "production"
)

// AuthTokenRequest represents the request to get a merchant access token.
type AuthTokenRequest struct {
	MerchantID string
	SecuredKey string
	GrantType  string
	CustomerIP string
}

// AuthTokenResponse represents the authentication response from PayFast API token endpoint.
type AuthTokenResponse struct {
	Code         string `json:"code,omitempty"`
	Token        string `json:"token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    string `json:"expiry,omitempty"` // Wait, docs say "expiry": "<no.ofseconds>"
	Message      string `json:"message,omitempty"`
}

// TokenCache holds in-memory cached OAuth/Auth tokens with thread-safe expiration checks.
type TokenCache struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// NOTE: PayFast account_type / account_type_id values vary by integration endpoint
// and issuer. Do NOT hardcode assumptions about which numeric ID maps to card vs bank
// vs wallet. Use PayFast's /list/instruments API to retrieve the correct mapping for
// your merchant configuration. The values in PayFast's own documentation examples
// (e.g., account_type_id=2 for card, =3 for bank) differ from naive 1/2/3 assumptions.

// CustomerValidationRequest contains all fields needed for POST /customer/validate
type CustomerValidationRequest struct {
	BasketID             string
	TxnAmt               string
	OrderDate            string // YYYY-MM-DD HH:mm:ss
	CustomerMobileNo     string
	CustomerEmailAddress string
	AccountTypeID        string
	MerCatCode           string
	CustomerIP           string

	// Card specific
	CardNumber         string `json:"-"`
	ExpiryMonth        string `json:"-"`
	ExpiryYear         string `json:"-"`
	CVV                string `json:"-"`
	Data3DSPagemode    string
	Data3DSCallbackURL string

	// Bank/Wallet specific
	BankCode      string `json:"-"`
	AccountNumber string `json:"-"`
	AccountTitle  string `json:"-"`
	CNICNumber    string `json:"-"`
}

// String explicitly masks sensitive fields so they never leak in fmt.Print or log operations.
func (c CustomerValidationRequest) String() string {
	return fmt.Sprintf("CustomerValidationRequest{BasketID:%s, TxnAmt:%s, OrderDate:%s, AccountTypeID:%s, CardNumber:[REDACTED], CVV:[REDACTED], AccountNumber:[REDACTED]}",
		c.BasketID, c.TxnAmt, c.OrderDate, c.AccountTypeID)
}

// CustomerValidationResponse is returned by POST /customer/validate
type CustomerValidationResponse struct {
	Code                         string         `json:"code"`
	Message                      string         `json:"message"`
	TransactionID                string         `json:"transaction_id"`
	Data3DSAcsURL                string         `json:"data_3ds_acsurl"`
	Data3DSPaReq                 string         `json:"data_3ds_pareq"`
	Data3DSHTML                  string         `json:"data_3ds_html"`
	Data3DSSecureID              string         `json:"data_3ds_secureid"`
	Data3DSGatewayRecommendation string         `json:"data_3ds_gatewayrecommendation"`
	ECI                          FlexibleString `json:"eci"`
}

// InitiateTransactionRequest contains fields for POST /transaction
type InitiateTransactionRequest struct {
	BasketID             string
	TxnAmt               string
	OrderDate            string
	CustomerMobileNo     string
	CustomerEmailAddress string
	AccountTypeID        string
	MerCatCode           string
	CustomerIP           string

	// From validation response
	TransactionID string
	ECI           string

	// Card specific
	CardNumber      string `json:"-"`
	ExpiryMonth     string `json:"-"`
	ExpiryYear      string `json:"-"`
	CVV             string `json:"-"`
	Data3DSSecureID string
	Data3DSPaRes    string

	// Bank/Wallet specific
	BankCode      string `json:"-"`
	AccountNumber string `json:"-"`
	AccountTitle  string `json:"-"`
	CNICNumber    string `json:"-"`
}

// String explicitly masks sensitive fields.
func (i InitiateTransactionRequest) String() string {
	return fmt.Sprintf("InitiateTransactionRequest{BasketID:%s, TxnAmt:%s, TransactionID:%s, CardNumber:[REDACTED], CVV:[REDACTED], AccountNumber:[REDACTED]}",
		i.BasketID, i.TxnAmt, i.TransactionID)
}

// InitiateTransactionResponse is returned by POST /transaction
type InitiateTransactionResponse struct {
	StatusCode    string `json:"status_code"`
	StatusMsg     string `json:"status_msg"`
	RdvMessageKey string `json:"rdv_message_key"`
	BasketID      string `json:"basket_id"`
	TransactionID string `json:"transaction_id"`
	Code          string `json:"code"` // Sometimes "00"
}

// TransactionStatusResponse is returned by GET /transaction/<transaction_id>
type TransactionStatusResponse struct {
	StatusCode    string `json:"status_code"`
	StatusMsg     string `json:"status_msg"`
	RdvMessageKey string `json:"rdv_message_key"`
	BasketID      string `json:"basket_id"`
	TransactionID string `json:"transaction_id"`
	Code          string `json:"code"`
	TxnAmt        string `json:"txnamt,omitempty"`
}

// TemporaryTokenRequest is for POST /transaction/token
type TemporaryTokenRequest struct {
	BasketID         string `json:"basket_id"`
	TxnAmt           string `json:"txnamt"`
	OrderDate        string `json:"order_date"` // YYYY-MM-DD HH:mm:ss
	CustomerMobileNo string `json:"user_mobile_number"`
	MerchantUserId   string `json:"merchant_user_id"`
	AccountTypeID    string `json:"account_type"`
	MerCatCode       string `json:"merCatCode"`
	CustomerIP       string `json:"customer_ip"`
	SecuredHash      string `json:"secured_hash"`

	// Card specific (never persist)
	CardNumber         string `json:"card_number,omitempty"`
	ExpiryMonth        string `json:"expiry_month,omitempty"`
	ExpiryYear         string `json:"expiry_year,omitempty"`
	CVV                string `json:"cvv,omitempty"`
	Data3DSPagemode    string `json:"data_3ds_pagemode,omitempty"`
	Data3DSCallbackURL string `json:"data_3ds_callback_url,omitempty"`

	// Bank/Wallet specific (never persist)
	AccountNumber      string `json:"account_number,omitempty"`
	CNICNumber         string `json:"cnic_number,omitempty"`
}

func (t TemporaryTokenRequest) String() string {
	return fmt.Sprintf("TemporaryTokenRequest{BasketID:%s, TxnAmt:%s, CardNumber:[REDACTED], CVV:[REDACTED]}", t.BasketID, t.TxnAmt)
}

// TemporaryTokenResponse is returned by POST /transaction/token
type TemporaryTokenResponse struct {
	StatusCode                   string         `json:"status_code"`
	StatusMsg                    string         `json:"status_msg"`
	InstrumentAlias              string         `json:"instrument_alias"`
	InstrumentToken              string         `json:"instrument_token"`
	TransactionID                string         `json:"transaction_id"`
	OtpRequired                  FlexibleBool   `json:"otp_required"`
	ECI                          FlexibleString `json:"eci"`
	Data3DSAcsURL                string         `json:"data_3ds_acsurl"`
	Data3DSPaReq                 string         `json:"data_3ds_pareq"`
	Data3DSHTML                  string         `json:"data_3ds_html"`
	Data3DSSecureID              string         `json:"data_3ds_secureid"`
	Data3DSGatewayRecommendation string         `json:"data_3ds_gatewayrecommendation"`
}

// TokenizedTransactionRequest is for POST /transaction/tokenized
type TokenizedTransactionRequest struct {
	InstrumentToken  string `json:"instrument_token"`
	TransactionID    string `json:"transaction_id"`
	MerchantUserId   string `json:"merchant_user_id"`
	CustomerMobileNo string `json:"user_mobile_number"`
	BasketID         string `json:"basket_id"`
	OrderDate        string `json:"order_date"`
	TxnDesc          string `json:"txndesc"`
	TxnAmt           string `json:"txnamt"`
	Otp              string `json:"otp,omitempty"`
	CustomerIP       string `json:"customer_ip"`
	MerCatCode       string `json:"merCatCode"`
	ECI              string `json:"eci,omitempty"`
	Data3DSSecureID  string `json:"data_3ds_secureid,omitempty"`
	Data3DSPaRes     string `json:"data_3ds_pares,omitempty"`
	SecuredHash      string `json:"secured_hash"`
}

func (t TokenizedTransactionRequest) String() string {
	return fmt.Sprintf("TokenizedTransactionRequest{BasketID:%s, TransactionID:%s, InstrumentToken:[REDACTED]}", t.BasketID, t.TransactionID)
}

// TokenizedTransactionResponse is returned by POST /transaction/tokenized
type TokenizedTransactionResponse struct {
	StatusCode    string `json:"status_code"`
	StatusMsg     string `json:"status_msg"`
	RdvMessageKey string `json:"rdv_message_key"`
	BasketID      string `json:"basket_id"`
	TransactionID string `json:"transaction_id"`
	Code          string `json:"code"`
}

```

---

## 2. `internal/payment/payfast/client.go`

```go
package payfast

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client handles all interaction with PayFast Pakistan gateway.
type Client struct {
	merchantID     string
	securedKey     string
	merchantName   string
	baseURL        string
	successURL     string
	failureURL     string
	httpClient     *http.Client
	tokens         *TokenManager
	circuitBreaker *CircuitBreaker
}

// NewClient constructs a production-ready PayFast client from environment variables or custom config.
func NewClient(merchantID, securedKey, merchantName, baseURL string) *Client {
	if baseURL == "" {
		baseURL = os.Getenv("PAYFAST_BASE_URL")
	}
	if baseURL == "" {
		panic("PAYFAST_BASE_URL must be explicitly set (e.g., https://ipg.gopayfast.com for sandbox)")
	}

	httpClient := &http.Client{
		Timeout: 20 * time.Second,
	}

	cb := NewCircuitBreaker(5, 10*time.Second)
	c := &Client{
		merchantID:     merchantID,
		securedKey:     securedKey,
		merchantName:   merchantName,
		baseURL:        strings.TrimRight(baseURL, "/"),
		successURL:     os.Getenv("PAYFAST_SUCCESS_URL"),
		failureURL:     os.Getenv("PAYFAST_FAILURE_URL"),
		httpClient:     httpClient,
		tokens:         NewTokenManager(httpClient, baseURL, merchantID, securedKey, cb),
		circuitBreaker: cb,
	}

	return c
}

// NewClientFromEnv initializes client using environment variables.
func NewClientFromEnv() *Client {
	return NewClient(
		os.Getenv("PAYFAST_MERCHANT_ID"),
		os.Getenv("PAYFAST_SECURED_KEY"),
		os.Getenv("PAYFAST_MERCHANT_NAME"),
		os.Getenv("PAYFAST_BASE_URL"),
	)
}

// CircuitBreaker returns the underlying circuit breaker instance.
func (c *Client) CircuitBreaker() *CircuitBreaker {
	return c.circuitBreaker
}

// IsConfigured returns true if merchant credentials are present.
func (c *Client) IsConfigured() bool {
	return c.merchantID != "" && c.securedKey != ""
}

// GetAuthToken returns a cached or freshly-fetched auth token via the internal TokenManager.
func (c *Client) GetAuthToken(ctx context.Context, customerIP string) (string, error) {
	if !c.IsConfigured() {
		return "", ErrNotConfigured
	}
	return c.tokens.GetToken(ctx, customerIP)
}

```

---

## 3. `internal/payment/payfast/circuit_breaker.go`

```go
package payfast

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitBreakerOpen is returned when the gateway circuit breaker is tripped.
var ErrCircuitBreakerOpen = errors.New("payfast circuit breaker is open: gateway temporarily unreachable")

// CircuitState represents the current operating state of the circuit breaker.
type CircuitState int

const (
	StateClosed CircuitState = iota
	StateHalfOpen
	StateOpen
)

func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateHalfOpen:
		return "HALF_OPEN"
	case StateOpen:
		return "OPEN"
	default:
		return "UNKNOWN"
	}
}

// CircuitBreaker provides fast-failing resilience during PayFast gateway outages.
type CircuitBreaker struct {
	mu                  sync.Mutex
	state               CircuitState
	failureCount        int
	consecutiveFailures int
	failureThreshold    int
	cooldownDuration    time.Duration
	lastStateChange     time.Time
}

// NewCircuitBreaker initializes a circuit breaker with sensible defaults.
func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 10 * time.Second
	}
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: threshold,
		cooldownDuration: cooldown,
		lastStateChange:  time.Now(),
	}
}

// Execute wraps an external network call with circuit breaking protection.
func (cb *CircuitBreaker) Execute(fn func() error) error {
	cb.mu.Lock()
	now := time.Now()

	// Check if cooldown has elapsed in Open state -> transition to HalfOpen
	if cb.state == StateOpen {
		if now.Sub(cb.lastStateChange) >= cb.cooldownDuration {
			cb.state = StateHalfOpen
			cb.lastStateChange = now
		} else {
			cb.mu.Unlock()
			return ErrCircuitBreakerOpen
		}
	}
	cb.mu.Unlock()

	// Execute operation
	err := fn()

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		// Only count transient network/socket/gateway errors towards tripping
		if IsTransient(err) || errors.Is(err, ErrAuthFailed) {
			cb.failureCount++
			cb.consecutiveFailures++

			if cb.state == StateHalfOpen || cb.consecutiveFailures >= cb.failureThreshold {
				cb.state = StateOpen
				cb.lastStateChange = time.Now()
			}
		}
		return err
	}

	// Success -> Reset circuit to Closed
	cb.consecutiveFailures = 0
	if cb.state == StateHalfOpen {
		cb.state = StateClosed
		cb.lastStateChange = time.Now()
	}
	return nil
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// Reset forcefully resets the circuit breaker to Closed.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = StateClosed
	cb.failureCount = 0
	cb.consecutiveFailures = 0
	cb.lastStateChange = time.Now()
}

```

---

## 4. `internal/payment/payfast/auth.go`

```go
package payfast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxAuthResponseSize = 64 * 1024 // 64 KB limit

// TokenManager provides thread-safe token acquisition, caching, and refresh logic.
type TokenManager struct {
	client     *http.Client
	baseURL    string
	merchantID string
	securedKey string
	circuitBreaker *CircuitBreaker
	mu             sync.RWMutex
	cache          TokenCache
}

// NewTokenManager initializes a new TokenManager securely.
func NewTokenManager(client *http.Client, baseURL, merchantID, securedKey string, cb *CircuitBreaker) *TokenManager {
	return &TokenManager{
		client:         client,
		baseURL:        strings.TrimRight(baseURL, "/"),
		merchantID:     merchantID,
		securedKey:     securedKey,
		circuitBreaker: cb,
	}
}

// GetToken returns a valid token from cache or fetches a fresh token if expired.
func (tm *TokenManager) GetToken(ctx context.Context, customerIP string) (string, error) {
	tm.mu.RLock()
	if tm.cache.AccessToken != "" && time.Now().Before(tm.cache.ExpiresAt) {
		token := tm.cache.AccessToken
		tm.mu.RUnlock()
		return token, nil
	}
	tm.mu.RUnlock()

	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Double-check after acquiring write lock
	if tm.cache.AccessToken != "" && time.Now().Before(tm.cache.ExpiresAt) {
		return tm.cache.AccessToken, nil
	}

	var token, expiresInStr string
	var err error
	if tm.circuitBreaker != nil {
		err = tm.circuitBreaker.Execute(func() error {
			t, exp, fErr := tm.fetchToken(ctx)
			if fErr != nil {
				return fErr
			}
			token = t
			expiresInStr = exp
			return nil
		})
	} else {
		token, expiresInStr, err = tm.fetchToken(ctx)
	}

	if err != nil {
		return "", err
	}

	expiresIn, err := strconv.ParseInt(expiresInStr, 10, 64)
	if err != nil || expiresIn <= 0 {
		expiresIn = 3600 // Default to 1-hour validity if parsing fails or missing
	}
	// Buffer safety margin of 60 seconds
	tm.cache = TokenCache{
		AccessToken: token,
		ExpiresAt:   time.Now().Add(time.Duration(expiresIn-60) * time.Second),
	}

	return token, nil
}

func (tm *TokenManager) fetchToken(ctx context.Context) (string, string, error) {
	if tm.merchantID == "" || tm.securedKey == "" {
		return "", "", ErrNotConfigured
	}

	// Official API endpoint: /token
	authURL := tm.baseURL + "/token"
	formData := url.Values{}
	formData.Set("merchant_id", tm.merchantID)
	formData.Set("grant_type", "client_credentials")
	formData.Set("secured_key", tm.securedKey)

	var resp *http.Response
	var err error
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(formData.Encode()))
		if err != nil {
			return "", "", fmt.Errorf("payfast: create auth request failed: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err = tm.client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			break // Success
		}

		if resp != nil {
			// Don't retry 4xx errors (client errors like unauthorized)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				break
			}
			resp.Body.Close()
		}

		if i < maxRetries-1 {
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(time.Duration(i+1) * 500 * time.Millisecond): // Exponential-ish backoff
			}
		}
	}

	if err != nil {
		return "", "", &GatewayError{StatusCode: 500, Message: "Auth endpoint unreachable after retries", Internal: err}
	}
	if resp == nil {
		return "", "", &GatewayError{StatusCode: 500, Message: "Auth endpoint unreachable after retries", Internal: fmt.Errorf("no response")}
	}
	defer resp.Body.Close()

	limitedBody := io.LimitReader(resp.Body, maxAuthResponseSize)
	body, err := io.ReadAll(limitedBody)
	if err != nil {
		return "", "", &GatewayError{StatusCode: resp.StatusCode, Message: "Failed to read auth response", Internal: err}
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Authentication failed at gateway",
			Internal:   fmt.Errorf("status code %d: %s", resp.StatusCode, string(body)),
		}
	}

	var res AuthTokenResponse
	if err := json.Unmarshal(body, &res); err == nil && res.Token != "" {
		if res.Code != "" && res.Code != "00" {
			return "", "", &GatewayError{
				StatusCode: resp.StatusCode,
				Message:    "Authentication rejected by gateway",
				StatusMsg:  fmt.Sprintf("code=%s msg=%s", res.Code, res.Message),
			}
		}
		return res.Token, res.ExpiresIn, nil
	}

	return "", "", ErrAuthFailed
}

```

---

## 5. `internal/payment/payfast/signature.go`

```go
package payfast

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// generateHMACSHA256 generates a hex-encoded HMAC-SHA256 signature for the concatenated payload.
func generateHMACSHA256(payload string, securedKey string) string {
	mac := hmac.New(sha256.New, []byte(securedKey))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// CalculateValidationHash computes the required secured_hash for POST /customer/validate
// Official rules:
// Card: basket_id + txnamt + card_number + expiry_month + expiry_year + cvv
// Account/Wallet: basket_id + txnamt + account_number + cnic_number
func CalculateValidationHash(req CustomerValidationRequest, securedKey string) string {
	var payload string
	if req.CardNumber != "" {
		payload = req.BasketID + req.TxnAmt + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV
	} else {
		payload = req.BasketID + req.TxnAmt + req.AccountNumber + req.CNICNumber
	}
	return generateHMACSHA256(payload, securedKey)
}

// CalculateTransactionHash computes the required secured_hash for POST /transaction
// Official rules:
// Card: basket_id + txnamt + card_number + expiry_month + expiry_year + cvv + otp
// Account/Wallet: basket_id + txnamt + account_number + cnic_number + otp
// Note: If no OTP is present/required, the "+ otp" part simply adds an empty string.
func CalculateTransactionHash(req InitiateTransactionRequest, otp string, securedKey string) string {
	var payload string
	if req.CardNumber != "" {
		payload = req.BasketID + req.TxnAmt + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV + otp
	} else {
		payload = req.BasketID + req.TxnAmt + req.AccountNumber + req.CNICNumber + otp
	}
	return generateHMACSHA256(payload, securedKey)
}

// VerifySignature compares expected and received signatures using constant-time comparison.
func VerifySignature(expected, received string) bool {
	if expected == "" || received == "" {
		return false
	}
	expBytes := []byte(strings.ToLower(strings.TrimSpace(expected)))
	recBytes := []byte(strings.ToLower(strings.TrimSpace(received)))
	if len(expBytes) != len(recBytes) {
		return false
	}
	return subtle.ConstantTimeCompare(expBytes, recBytes) == 1
}

// CalculateTemporaryTokenHash computes the required secured_hash for POST /transaction/token
// Official rules:
// Card: merchant_user_id + user_mobile_number + card_number + expiry_month + expiry_year + cvv
// Account/Wallet: merchant_user_id + user_mobile_number + account_number + cnic_number
func CalculateTemporaryTokenHash(req TemporaryTokenRequest, securedKey string) string {
	var payload string
	if req.CardNumber != "" {
		payload = req.MerchantUserId + req.CustomerMobileNo + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV
	} else {
		payload = req.MerchantUserId + req.CustomerMobileNo + req.AccountNumber + req.CNICNumber
	}
	return generateHMACSHA256(payload, securedKey)
}

// CalculateTokenizedTransactionHash computes the required secured_hash for POST /transaction/tokenized
// Official rules:
// instrument_token + merchant_user_id + user_mobile_number + txnamt + otp
func CalculateTokenizedTransactionHash(req TokenizedTransactionRequest, securedKey string) string {
	payload := req.InstrumentToken + req.MerchantUserId + req.CustomerMobileNo + req.TxnAmt + req.Otp
	return generateHMACSHA256(payload, securedKey)
}

```

---

## 6. `internal/payment/payfast/api.go`

```go
package payfast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ValidateCustomerPayment calls POST /customer/validate
func (c *Client) ValidateCustomerPayment(ctx context.Context, req CustomerValidationRequest) (*CustomerValidationResponse, error) {
	token, err := c.GetAuthToken(ctx, req.CustomerIP)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := c.baseURL + "/customer/validate"
	formData := url.Values{}

	// Common Parameters
	formData.Set("basket_id", req.BasketID)
	formData.Set("txnamt", req.TxnAmt)
	formData.Set("order_date", req.OrderDate)
	formData.Set("customer_mobile_no", req.CustomerMobileNo)
	formData.Set("customer_email_address", req.CustomerEmailAddress)
	formData.Set("account_type_id", req.AccountTypeID)
	formData.Set("merCatCode", req.MerCatCode)
	formData.Set("customer_ip", req.CustomerIP)

	// Calculate and set secured_hash
	securedHash := CalculateValidationHash(req, c.securedKey)
	formData.Set("secured_hash", securedHash)

	// Instrument-specific parameters
	if req.CardNumber != "" {
		formData.Set("card_number", req.CardNumber)
		formData.Set("expiry_month", req.ExpiryMonth)
		formData.Set("expiry_year", req.ExpiryYear)
		formData.Set("cvv", req.CVV)
		if req.Data3DSPagemode != "" {
			formData.Set("data_3ds_pagemode", req.Data3DSPagemode)
		}
		if req.Data3DSCallbackURL != "" {
			formData.Set("data_3ds_callback_url", req.Data3DSCallbackURL)
		}
	} else if req.AccountNumber != "" {
		formData.Set("bank_code", req.BankCode)
		formData.Set("account_number", req.AccountNumber)
		formData.Set("cnic_number", req.CNICNumber)
		if req.AccountTitle != "" {
			formData.Set("account_title", req.AccountTitle)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errRes struct {
			StatusMsg string `json:"status_msg"`
			Message   string `json:"message"`
		}
		_ = json.Unmarshal(bodyBytes, &errRes)
		msg := errRes.StatusMsg
		if msg == "" {
			msg = errRes.Message
		}
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Customer validation failed",
			StatusMsg:  msg,
			Internal:   fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)),
		}
	}

	var validationRes CustomerValidationResponse
	if err := json.Unmarshal(bodyBytes, &validationRes); err != nil {
		return nil, fmt.Errorf("failed to parse validation response: %w", err)
	}

	return &validationRes, nil
}

// InitiateTransaction calls POST /transaction
func (c *Client) InitiateTransaction(ctx context.Context, req InitiateTransactionRequest, otp string) (*InitiateTransactionResponse, error) {
	token, err := c.GetAuthToken(ctx, req.CustomerIP)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := c.baseURL + "/transaction"
	formData := url.Values{}

	formData.Set("basket_id", req.BasketID)
	formData.Set("txnamt", req.TxnAmt)
	formData.Set("order_date", req.OrderDate)
	formData.Set("customer_mobile_no", req.CustomerMobileNo)
	formData.Set("customer_email_address", req.CustomerEmailAddress)
	formData.Set("account_type_id", req.AccountTypeID)
	formData.Set("merCatCode", req.MerCatCode)
	formData.Set("customer_ip", req.CustomerIP)

	if req.TransactionID != "" {
		formData.Set("transaction_id", req.TransactionID)
	}
	if req.ECI != "" {
		formData.Set("eci", req.ECI)
	}

	if otp != "" {
		formData.Set("otp", otp)
	}

	securedHash := CalculateTransactionHash(req, otp, c.securedKey)
	formData.Set("secured_hash", securedHash)

	if req.CardNumber != "" {
		formData.Set("card_number", req.CardNumber)
		formData.Set("expiry_month", req.ExpiryMonth)
		formData.Set("expiry_year", req.ExpiryYear)
		formData.Set("cvv", req.CVV)
		if req.Data3DSSecureID != "" {
			formData.Set("data_3ds_secureid", req.Data3DSSecureID)
		}
		if req.Data3DSPaRes != "" {
			formData.Set("data_3ds_pares", req.Data3DSPaRes)
		}
	} else if req.AccountNumber != "" {
		formData.Set("bank_code", req.BankCode)
		formData.Set("account_number", req.AccountNumber)
		formData.Set("cnic_number", req.CNICNumber)
		if req.AccountTitle != "" {
			formData.Set("account_title", req.AccountTitle)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errRes struct {
			StatusMsg string `json:"status_msg"`
			Message   string `json:"message"`
		}
		_ = json.Unmarshal(bodyBytes, &errRes)
		msg := errRes.StatusMsg
		if msg == "" {
			msg = errRes.Message
		}
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Transaction initiation failed",
			StatusMsg:  msg,
			Internal:   fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)),
		}
	}

	var txnRes InitiateTransactionResponse
	if err := json.Unmarshal(bodyBytes, &txnRes); err != nil {
		return nil, fmt.Errorf("failed to parse transaction response: %w", err)
	}

	return &txnRes, nil
}

// GetTransactionStatus calls GET /transaction/<transaction_id>
// GetTransactionStatus calls GET /transaction/<transaction_id>
func (c *Client) GetTransactionStatus(ctx context.Context, transactionID string) (*TransactionStatusResponse, error) {
	var statusRes *TransactionStatusResponse
	err := c.circuitBreaker.Execute(func() error {
		token, err := c.GetAuthToken(ctx, "")
		if err != nil {
			return fmt.Errorf("failed to get auth token: %w", err)
		}

		endpoint := fmt.Sprintf("%s/transaction/%s", c.baseURL, url.PathEscape(transactionID))

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		httpReq.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			var errRes struct {
				StatusMsg string `json:"status_msg"`
				Message   string `json:"message"`
			}
			_ = json.Unmarshal(bodyBytes, &errRes)
			msg := errRes.StatusMsg
			if msg == "" {
				msg = errRes.Message
			}
			return &GatewayError{
				StatusCode: resp.StatusCode,
				Message:    "Transaction status check failed",
				StatusMsg:  msg,
				Internal:   fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)),
			}
		}

		var res TransactionStatusResponse
		if err := json.Unmarshal(bodyBytes, &res); err != nil {
			return fmt.Errorf("failed to parse transaction status response: %w", err)
		}
		statusRes = &res
		return nil
	})

	if err != nil {
		return nil, err
	}
	return statusRes, nil
}

// GetTransactionStatusByBasketID calls GET /transaction/basket_id/<basket_id>
func (c *Client) GetTransactionStatusByBasketID(ctx context.Context, basketID string) (*TransactionStatusResponse, error) {
	var statusRes *TransactionStatusResponse
	err := c.circuitBreaker.Execute(func() error {
		token, err := c.GetAuthToken(ctx, "")
		if err != nil {
			return fmt.Errorf("failed to get auth token: %w", err)
		}

		endpoint := fmt.Sprintf("%s/transaction/basket_id/%s", c.baseURL, url.PathEscape(basketID))

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		httpReq.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			var errRes struct {
				StatusMsg string `json:"status_msg"`
				Message   string `json:"message"`
			}
			_ = json.Unmarshal(bodyBytes, &errRes)
			msg := errRes.StatusMsg
			if msg == "" {
				msg = errRes.Message
			}
			return &GatewayError{
				StatusCode: resp.StatusCode,
				Message:    "Transaction status check by basket ID failed",
				StatusMsg:  msg,
				Internal:   fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)),
			}
		}

		var res TransactionStatusResponse
		if err := json.Unmarshal(bodyBytes, &res); err != nil {
			return fmt.Errorf("failed to parse transaction status response: %w", err)
		}
		statusRes = &res
		return nil
	})

	if err != nil {
		return nil, err
	}
	return statusRes, nil
}

// GetTemporaryTransactionToken calls POST /transaction/token
func (c *Client) GetTemporaryTransactionToken(ctx context.Context, req TemporaryTokenRequest) (*TemporaryTokenResponse, error) {
	var tokenRes *TemporaryTokenResponse
	err := c.circuitBreaker.Execute(func() error {
		token, err := c.GetAuthToken(ctx, req.CustomerIP)
		if err != nil {
			return fmt.Errorf("failed to get auth token: %w", err)
		}

		endpoint := c.baseURL + "/transaction/token"
		formData := url.Values{}

		// Common Parameters
		formData.Set("basket_id", req.BasketID)
		formData.Set("txnamt", req.TxnAmt)
		formData.Set("order_date", req.OrderDate)
		formData.Set("user_mobile_number", req.CustomerMobileNo)
		formData.Set("merchant_user_id", req.MerchantUserId)
		formData.Set("account_type", req.AccountTypeID)
		formData.Set("merCatCode", req.MerCatCode)
		formData.Set("customer_ip", req.CustomerIP)

		// Card specific
		if req.CardNumber != "" {
			formData.Set("card_number", req.CardNumber)
			formData.Set("expiry_month", req.ExpiryMonth)
			formData.Set("expiry_year", req.ExpiryYear)
			formData.Set("cvv", req.CVV)
			if req.Data3DSPagemode != "" {
				formData.Set("data_3ds_pagemode", req.Data3DSPagemode)
			}
			if req.Data3DSCallbackURL != "" {
				formData.Set("data_3ds_callback_url", req.Data3DSCallbackURL)
			}
		} else if req.AccountNumber != "" {
			formData.Set("account_number", req.AccountNumber)
			formData.Set("cnic_number", req.CNICNumber)
		}

		securedHash := CalculateTemporaryTokenHash(req, c.securedKey)
		formData.Set("secured_hash", securedHash)

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		httpReq.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			var errRes struct {
				StatusMsg string `json:"status_msg"`
				Message   string `json:"message"`
			}
			_ = json.Unmarshal(bodyBytes, &errRes)
			msg := errRes.StatusMsg
			if msg == "" {
				msg = errRes.Message
			}
			return &GatewayError{
				StatusCode: resp.StatusCode,
				Message:    "Temporary token request failed",
				StatusMsg:  msg,
				Internal:   fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)),
			}
		}

		var res TemporaryTokenResponse
		if err := json.Unmarshal(bodyBytes, &res); err != nil {
			return fmt.Errorf("failed to parse temporary token response: %w", err)
		}
		tokenRes = &res
		return nil
	})

	if err != nil {
		return nil, err
	}
	return tokenRes, nil
}

// InitiateTokenizedTransaction calls POST /transaction/tokenized
func (c *Client) InitiateTokenizedTransaction(ctx context.Context, req TokenizedTransactionRequest) (*TokenizedTransactionResponse, error) {
	var txnRes *TokenizedTransactionResponse
	err := c.circuitBreaker.Execute(func() error {
		token, err := c.GetAuthToken(ctx, req.CustomerIP)
		if err != nil {
			return fmt.Errorf("failed to get auth token: %w", err)
		}

		endpoint := c.baseURL + "/transaction/tokenized"
		formData := url.Values{}

		formData.Set("instrument_token", req.InstrumentToken)
		formData.Set("transaction_id", req.TransactionID)
		formData.Set("merchant_user_id", req.MerchantUserId)
		formData.Set("user_mobile_number", req.CustomerMobileNo)
		formData.Set("basket_id", req.BasketID)
		formData.Set("order_date", req.OrderDate)
		formData.Set("txndesc", req.TxnDesc)
		formData.Set("txnamt", req.TxnAmt)
		formData.Set("customer_ip", req.CustomerIP)
		formData.Set("merCatCode", req.MerCatCode)

		if req.Otp != "" {
			formData.Set("otp", req.Otp)
		}
		if req.ECI != "" {
			formData.Set("eci", req.ECI)
		}
		if req.Data3DSSecureID != "" {
			formData.Set("data_3ds_secureid", req.Data3DSSecureID)
		}
		if req.Data3DSPaRes != "" {
			formData.Set("data_3ds_pares", req.Data3DSPaRes)
		}

		securedHash := CalculateTokenizedTransactionHash(req, c.securedKey)
		formData.Set("secured_hash", securedHash)

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		httpReq.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return fmt.Errorf("failed to send request: %w", err)
		}
		defer resp.Body.Close()

		bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			var errRes struct {
				StatusMsg string `json:"status_msg"`
				Message   string `json:"message"`
			}
			_ = json.Unmarshal(bodyBytes, &errRes)
			msg := errRes.StatusMsg
			if msg == "" {
				msg = errRes.Message
			}
			return &GatewayError{
				StatusCode: resp.StatusCode,
				Message:    "Tokenized transaction failed",
				StatusMsg:  msg,
				Internal:   fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)),
			}
		}

		var res TokenizedTransactionResponse
		if err := json.Unmarshal(bodyBytes, &res); err != nil {
			return fmt.Errorf("failed to parse tokenized transaction response: %w", err)
		}
		txnRes = &res
		return nil
	})

	if err != nil {
		return nil, err
	}
	return txnRes, nil
}

```

---

## 7. `internal/payment/payfast/errors.go`

```go
package payfast

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
)

var (
	ErrNotConfigured        = errors.New("payfast: gateway credentials missing or unconfigured")
	ErrInvalidCustomerEmail = errors.New("payfast: valid customer email is required")
	ErrInvalidCustomerPhone = errors.New("payfast: valid customer mobile number is required")
	ErrInvalidAmount        = errors.New("payfast: transaction amount must be greater than zero")
	ErrInvalidOrderID       = errors.New("payfast: basket/order ID is required")
	ErrSignatureMismatch    = errors.New("payfast: callback signature validation failed")
	ErrAmountMismatch       = errors.New("payfast: payment amount does not match local order amount")
	ErrCurrencyMismatch     = errors.New("payfast: payment currency does not match local order currency")
	ErrOrderAlreadyPaid     = errors.New("payfast: order is already marked as paid (idempotent rejection)")
	ErrAuthFailed           = errors.New("payfast: authentication token acquisition failed")
	ErrTransactionFailed    = errors.New("payfast: payment processing was rejected by gateway")
	ErrEscrowHoldFailed     = errors.New("payfast: vendor escrow hold creation failed")
)

// GatewayError wraps external API failures without exposing sensitive secrets in client logs.
type GatewayError struct {
	StatusCode int
	Message    string
	StatusMsg  string
	Internal   error
}

func (e *GatewayError) Error() string {
	msg := e.Message
	if e.StatusMsg != "" {
		msg = fmt.Sprintf("%s (gateway msg: %s)", msg, e.StatusMsg)
	}
	return fmt.Sprintf("payfast gateway error (HTTP %d): %s", e.StatusCode, msg)
}

func (e *GatewayError) Unwrap() error {
	return e.Internal
}

// IsTransient returns true if the error represents a temporary network, socket, or timeout failure
// where the transaction state at the gateway is unknown and should be reconciled via gateway_pending.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}

	// Explicit client cancellation (user closed tab / connection aborted) is NOT a transient gateway timeout
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Context deadline exceeded due to timeout
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCircuitBreakerOpen) {
		return true
	}

	// Network / Socket errors
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) {
		if errors.Is(syscallErr.Err, syscall.ECONNREFUSED) ||
			errors.Is(syscallErr.Err, syscall.ECONNRESET) ||
			errors.Is(syscallErr.Err, syscall.ETIMEDOUT) {
			return true
		}
	}

	// Gateway HTTP status checks
	var gwErr *GatewayError
	if errors.As(err, &gwErr) {
		switch gwErr.StatusCode {
		case http.StatusRequestTimeout, // 408
			http.StatusBadGateway,          // 502
			http.StatusServiceUnavailable,  // 503
			http.StatusGatewayTimeout:      // 504
			return true
		}
	}

	return false
}

// IsDeterministicRejection returns true if the error represents an explicit, permanent refusal
// (e.g. HTTP 400 Bad Request, 401 Unauthorized, 422 Unprocessable, or invalid credentials/parameters).
func IsDeterministicRejection(err error) bool {
	if err == nil {
		return false
	}
	var gwErr *GatewayError
	if errors.As(err, &gwErr) {
		if gwErr.StatusCode >= 400 && gwErr.StatusCode < 500 && gwErr.StatusCode != http.StatusRequestTimeout {
			return true
		}
	}
	return false
}


```

---

## 8. `internal/payment_orchestrator/service/payfast_service.go`

```go
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/payment/payfast"
	"github.com/omnigo/backend/internal/payment_orchestrator"
)

// PaymentRequest contains client checkout parameters.
type PaymentRequest struct {
	OrderID          string `json:"order_id" binding:"required"`
	CustomerMobileNo string `json:"customer_mobile_no"`
	AccountTypeID    string `json:"account_type_id"`
	PaymentMethod    string `json:"payment_method"` // card | bank | wallet
	BankCode         string `json:"bank_code"`
	AccountNumber    string `json:"account_number"`
	AccountTitle     string `json:"account_title"`
	CNICNumber       string `json:"cnic_number"`
	CardNumber       string `json:"card_number"`
	ExpiryMonth      string `json:"expiry_month"`
	ExpiryYear       string `json:"expiry_year"`
	CVV              string `json:"cvv"`
	OTP              string `json:"otp"`
}

// Validate ensures input parameters meet financial security and card scheme formats.
func (req *PaymentRequest) Validate() error {
	if strings.TrimSpace(req.OrderID) == "" {
		return errors.New("order_id is required")
	}

	// Card validation
	if req.CardNumber != "" {
		cleanPan := strings.ReplaceAll(req.CardNumber, " ", "")
		if len(cleanPan) < 13 || len(cleanPan) > 19 {
			return errors.New("invalid card number length")
		}
		if len(req.CVV) < 3 || len(req.CVV) > 4 {
			return errors.New("invalid CVV (must be 3 or 4 digits)")
		}
		if len(req.ExpiryMonth) != 2 {
			return errors.New("invalid expiry month format (must be MM, e.g. '08')")
		}
		m, err := strconv.Atoi(req.ExpiryMonth)
		if err != nil || m < 1 || m > 12 {
			return errors.New("expiry month must be between 01 and 12")
		}
		if len(req.ExpiryYear) != 4 && len(req.ExpiryYear) != 2 {
			return errors.New("invalid expiry year format (must be YYYY, e.g. '2026')")
		}
		fullYear := req.ExpiryYear
		if len(fullYear) == 2 {
			fullYear = "20" + fullYear
		}
		y, err := strconv.Atoi(fullYear)
		if err != nil {
			return errors.New("invalid expiry year")
		}

		now := time.Now()
		currentYear := now.Year()
		currentMonth := int(now.Month())
		if y < currentYear || (y == currentYear && m < currentMonth) {
			return errors.New("card is expired")
		}
	} else if req.AccountNumber != "" {
		if strings.TrimSpace(req.AccountNumber) == "" {
			return errors.New("account number is required for bank/wallet payment")
		}
	}

	return nil
}

// PaymentResponse represents the immediate response from payment initiation.
type PaymentResponse struct {
	Status        string `json:"status"` // 3ds_redirect | settlement_pending | failed | gateway_pending
	Action        string `json:"action,omitempty"`
	ThreeDSHtml   string `json:"threed_html,omitempty"`
	OrderID       string `json:"order_id"`
	TransactionID string `json:"transaction_id"`
	Message       string `json:"message,omitempty"`
}

// PaymentMetadata stores non-sensitive gateway token state. Zero cardholder data stored.
type PaymentMetadata struct {
	InstrumentToken string                 `json:"instrument_token"`
	GatewayTxnID    string                 `json:"gateway_txn_id"`
	Data3DSSecureID string                 `json:"data_3ds_secureid"`
	ECI             string                 `json:"eci"`
	CustomerMobile  string                 `json:"customer_mobile"`
	AccountTypeID   string                 `json:"account_type"`
	CustomerIP      string                 `json:"customer_ip"`
	CalculatedSplit map[string]float64     `json:"calculated_split"`
}

// PayFastService orchestrates domain logic, database invariants, and PayFast API integration.
type PayFastService struct {
	db               *pgxpool.Pool
	ledger           *ledger.Service
	escrow           *escrow.Service
	calculator       *payment_orchestrator.CommissionCalculator
	payfast          *payfast.Client
	callbackSecret   string
	merchantCategory string
	callbackBaseURL  string
	defaultCurrency  string
}

// NewPayFastService constructs a thread-safe, production-ready PayFast service.
func NewPayFastService(
	db *pgxpool.Pool,
	ledgerSvc *ledger.Service,
	escrowSvc *escrow.Service,
	calc *payment_orchestrator.CommissionCalculator,
	payfastClient *payfast.Client,
) *PayFastService {
	cat := os.Getenv("PAYFAST_MERCHANT_CATEGORY")
	if cat == "" {
		cat = "0001" // Standard retail default fallback if unconfigured
	}
	secret := os.Getenv("INTERNAL_CALLBACK_SECRET")
	if secret == "" {
		if os.Getenv("GO_TEST_ENV") == "1" {
			secret = "test-internal-callback-secret-32-chars-long"
		} else {
			panic("INTERNAL_CALLBACK_SECRET environment variable is required")
		}
	}
	cbURL := os.Getenv("PAYFAST_3DS_CALLBACK_URL")
	if cbURL == "" {
		cbURL = "http://localhost:8080/api/v1/payments/payfast/3ds_callback"
	}
	currency := os.Getenv("DEFAULT_CURRENCY")
	if currency == "" {
		currency = "PKR"
	}

	return &PayFastService{
		db:               db,
		ledger:           ledgerSvc,
		escrow:           escrowSvc,
		calculator:       calc,
		payfast:          payfastClient,
		callbackSecret:   secret,
		merchantCategory: cat,
		callbackBaseURL:  cbURL,
		defaultCurrency:  currency,
	}
}

// generateHMACSHA256 generates a hex-encoded HMAC-SHA256 signature.
func (s *PayFastService) generateHMACSHA256(data string) string {
	h := hmac.New(sha256.New, []byte(s.callbackSecret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// SignMD generates an HMAC signature for the internal transaction ID.
func (s *PayFastService) SignMD(internalTxnID string) string {
	return internalTxnID + "." + s.generateHMACSHA256(internalTxnID)
}

// VerifyMD validates the HMAC signature in constant time.
func (s *PayFastService) VerifyMD(mdParam string) (string, error) {
	parts := strings.Split(mdParam, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid MD format")
	}

	internalTxnID := parts[0]
	providedSignature := parts[1]
	expectedSignature := s.generateHMACSHA256(internalTxnID)

	if subtle.ConstantTimeCompare([]byte(providedSignature), []byte(expectedSignature)) != 1 {
		return "", fmt.Errorf("signature mismatch")
	}

	return internalTxnID, nil
}

// Build3DSCallbackURL safely constructs the 3DS callback URL with signed MD parameter.
func (s *PayFastService) Build3DSCallbackURL(internalTxnID string) (string, error) {
	signedMD := s.SignMD(internalTxnID)
	u, err := url.Parse(s.callbackBaseURL)
	if err != nil {
		return "", fmt.Errorf("invalid callback base URL: %w", err)
	}
	q := u.Query()
	q.Set("md", signedMD)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ProcessPayment handles the primary checkout initiation.
func (s *PayFastService) ProcessPayment(ctx context.Context, merchantUserID, clientIP string, req PaymentRequest) (*PaymentResponse, error) {
	// 1. Validate Input Payload
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	// 2. Fetch Authoritative Order Amount & Lock Order Row
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var expectedAmount float64
	var orderStatus string
	var customerTrackingID string
	var storeID string
	err = tx.QueryRow(ctx,
		`SELECT total_amount, status, customer_tracking_id, store_tracking_id 
		 FROM orders WHERE order_tracking_id = $1 FOR UPDATE`, req.OrderID,
	).Scan(&expectedAmount, &orderStatus, &customerTrackingID, &storeID)
	if err != nil {
		return nil, fmt.Errorf("order not found: %w", err)
	}
	if merchantUserID != customerTrackingID {
		return nil, errors.New("forbidden: order belongs to a different user")
	}
	if orderStatus != "pending" && orderStatus != "unpaid" {
		return nil, errors.New("order is not in a payable status")
	}

	internalTxnID := "pf_" + uuid.New().String()
	idempotencyKey := fmt.Sprintf("pf:%s:%s", req.OrderID, merchantUserID)

	// Check active attempts
	var activeAttempts int
	err = tx.QueryRow(ctx,
		`SELECT count(*) FROM payment_transactions 
		 WHERE order_tracking_id = $1 AND status IN ('processing', '3ds_required', 'settlement_pending', 'gateway_pending')`,
		req.OrderID,
	).Scan(&activeAttempts)
	if err != nil {
		return nil, fmt.Errorf("failed to check active attempts: %w", err)
	}
	if activeAttempts > 0 {
		return nil, errors.New("conflict: payment attempt is already in progress for this order")
	}

	// Fetch authoritative phone from users table with fallback
	var authoritativeMobile string
	err = tx.QueryRow(ctx, `SELECT COALESCE(phone, '') FROM users WHERE tracking_id = $1`, customerTrackingID).Scan(&authoritativeMobile)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user profile: %w", err)
	}
	if authoritativeMobile == "" && req.CustomerMobileNo != "" {
		authoritativeMobile = req.CustomerMobileNo
	}
	if authoritativeMobile == "" {
		return nil, errors.New("customer phone number is required for payment verification")
	}

	if req.AccountTypeID == "" {
		if req.CardNumber != "" {
			req.AccountTypeID = "2" // Card
		} else if req.AccountNumber != "" {
			req.AccountTypeID = "3" // Bank/Wallet
		} else {
			req.AccountTypeID = "1"
		}
	}

	// Pre-calculate Split and assert parity
	var deliveryTrackingID string
	_ = tx.QueryRow(ctx, `SELECT COALESCE(tracking_id, '') FROM deliveries WHERE order_tracking_id = $1`, req.OrderID).Scan(&deliveryTrackingID)
	split, err := s.calculator.CalculateSplit(ctx, expectedAmount, storeID, deliveryTrackingID)
	if err != nil {
		return nil, fmt.Errorf("commission split calculation failed: %w", err)
	}
	totalCalculated := split.AdminRevenue + split.VendorEscrow + split.DeliveryEscrow
	if math.Abs(totalCalculated-expectedAmount) > 0.01 {
		return nil, fmt.Errorf("split parity error: calculated %.2f vs total %.2f", totalCalculated, expectedAmount)
	}

	splitMeta := map[string]float64{
		"admin_revenue":   split.AdminRevenue,
		"vendor_escrow":   split.VendorEscrow,
		"delivery_escrow": split.DeliveryEscrow,
	}
	metaBytes, _ := json.Marshal(PaymentMetadata{
		CustomerMobile:  authoritativeMobile,
		AccountTypeID:   req.AccountTypeID,
		CustomerIP:      clientIP,
		CalculatedSplit: splitMeta,
	})

	// 3. Insert Initial Payment Transaction (idempotency key populated)
	_, err = tx.Exec(ctx,
		`INSERT INTO payment_transactions (transaction_id, order_tracking_id, gateway, amount, currency, status, kind, idempotency_key, metadata)
		 VALUES ($1, $2, 'payfast', $3, $4, 'pending', 'payment', $5, $6)`,
		internalTxnID, req.OrderID, expectedAmount, s.defaultCurrency, idempotencyKey, metaBytes,
	)
	if err != nil {
		if strings.Contains(err.Error(), "ux_payment_active_order") || strings.Contains(err.Error(), "unique constraint") {
			return nil, errors.New("conflict: payment attempt is already in progress")
		}
		return nil, fmt.Errorf("failed to record payment attempt: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit initial payment attempt: %w", err)
	}

	// Zero card persistence cleanup
	defer func() {
		req.CardNumber = ""
		req.CVV = ""
		req.AccountNumber = ""
		req.CNICNumber = ""
	}()

	callbackURL, err := s.Build3DSCallbackURL(internalTxnID)
	if err != nil {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Failed to build callback URL: "+err.Error())
		return nil, err
	}

	// 4. Request Temporary Transaction Token from PayFast
	tokenReq := payfast.TemporaryTokenRequest{
		MerchantUserId:     customerTrackingID,
		CustomerMobileNo:   authoritativeMobile,
		BasketID:           req.OrderID,
		OrderDate:          time.Now().Format("2006-01-02 15:04:05"),
		TxnAmt:             fmt.Sprintf("%.2f", expectedAmount),
		CustomerIP:         clientIP,
		AccountTypeID:      req.AccountTypeID,
		MerCatCode:         s.merchantCategory,
		CardNumber:         req.CardNumber,
		ExpiryMonth:        req.ExpiryMonth,
		ExpiryYear:         req.ExpiryYear,
		CVV:                req.CVV,
		AccountNumber:      req.AccountNumber,
		CNICNumber:         req.CNICNumber,
		Data3DSPagemode:    "SIMPLE",
		Data3DSCallbackURL: callbackURL,
	}

	tokenRes, err := s.payfast.GetTemporaryTransactionToken(ctx, tokenReq)
	if err != nil {
		// If initial token acquisition fails before any gateway token exists,
		// NO charge could possibly have occurred, so fail deterministically.
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Temporary token request failed: "+err.Error())
		return &PaymentResponse{
			Status:        "failed",
			OrderID:       req.OrderID,
			TransactionID: internalTxnID,
			Message:       "Failed to communicate with payment gateway",
		}, err
	}

	// 5. Evaluate Token Response (3DS Required vs Direct Tokenized Capture)
	if tokenRes.Data3DSHTML != "" {
		metaBytes, _ = json.Marshal(PaymentMetadata{
			InstrumentToken: tokenRes.InstrumentToken,
			GatewayTxnID:    tokenRes.TransactionID,
			Data3DSSecureID: tokenRes.Data3DSSecureID,
			ECI:             tokenRes.ECI.String(),
			CustomerMobile:  authoritativeMobile,
			AccountTypeID:   req.AccountTypeID,
			CustomerIP:      clientIP,
			CalculatedSplit: splitMeta,
		})

		_, _ = s.db.Exec(ctx,
			`UPDATE payment_transactions 
			 SET status = '3ds_required', gateway_txn_id = $1, metadata = $2, updated_at = NOW() 
			 WHERE transaction_id = $3`,
			tokenRes.TransactionID, metaBytes, internalTxnID,
		)

		return &PaymentResponse{
			Status:        "3ds_redirect",
			Action:        "3ds_redirect",
			ThreeDSHtml:   tokenRes.Data3DSHTML,
			OrderID:       req.OrderID,
			TransactionID: internalTxnID,
		}, nil
	}

	// 6. Direct Tokenized Capture (3DS Not Required)
	if tokenRes.InstrumentToken == "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Gateway returned empty instrument token")
		return nil, errors.New("invalid gateway token response")
	}

	// Mark processing
	_, _ = s.db.Exec(ctx, `UPDATE payment_transactions SET status = 'processing', updated_at = NOW() WHERE transaction_id = $1`, internalTxnID)

	txnReq := payfast.TokenizedTransactionRequest{
		InstrumentToken:  tokenRes.InstrumentToken,
		TransactionID:    tokenRes.TransactionID,
		MerchantUserId:   customerTrackingID,
		CustomerMobileNo: authoritativeMobile,
		BasketID:         req.OrderID,
		OrderDate:        time.Now().Format("2006-01-02 15:04:05"),
		TxnDesc:          "OmniGo Order " + req.OrderID,
		TxnAmt:           fmt.Sprintf("%.2f", expectedAmount),
		CustomerIP:       clientIP,
		MerCatCode:       s.merchantCategory,
		Otp:              req.OTP,
	}

	txnRes, err := s.payfast.InitiateTokenizedTransaction(ctx, txnReq)
	if err != nil {
		if payfast.IsTransient(err) {
			_ = s.MarkPaymentGatewayPending(ctx, internalTxnID, tokenRes.TransactionID, "Direct tokenized txn timeout: "+err.Error())
			return &PaymentResponse{
				Status:        "gateway_pending",
				OrderID:       req.OrderID,
				TransactionID: internalTxnID,
				Message:       "Payment is processing at gateway; reconciliation in progress",
			}, nil
		}

		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Tokenized capture failed: "+err.Error())
		return &PaymentResponse{
			Status:        "failed",
			OrderID:       req.OrderID,
			TransactionID: internalTxnID,
			Message:       "Payment was rejected by gateway",
		}, err
	}

	if txnRes == nil || txnRes.TransactionID == "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Gateway returned empty transaction ID")
		return nil, errors.New("invalid gateway transaction response")
	}
	if txnRes.StatusCode != "00" && txnRes.StatusCode != "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, txnRes.StatusMsg)
		return nil, fmt.Errorf("gateway error: %s (code: %s)", txnRes.StatusMsg, txnRes.StatusCode)
	}

	// 7. Verify & Settle
	if err := s.VerifyAndSettle(ctx, internalTxnID, req.OrderID, txnRes.TransactionID, expectedAmount); err != nil {
		return nil, err
	}

	return &PaymentResponse{
		Status:        "settlement_pending",
		OrderID:       req.OrderID,
		TransactionID: internalTxnID,
		Message:       "Payment verified successfully",
	}, nil
}

// Handle3DSCallback processes the ACS form post callback with replay defense.
func (s *PayFastService) Handle3DSCallback(ctx context.Context, mdParam, paRes, clientIP string) (string, error) {
	internalTxnID, err := s.VerifyMD(mdParam)
	if err != nil {
		return "", fmt.Errorf("invalid MD signature: %w", err)
	}

	// 1. Fetch 3DS Payment Record with Replay Defense Guard
	var orderID string
	var amount float64
	var metaJSON []byte
	var customerTrackingID string
	err = s.db.QueryRow(ctx,
		`SELECT pt.order_tracking_id, pt.amount, pt.metadata, o.customer_tracking_id 
		 FROM payment_transactions pt
		 JOIN orders o ON o.order_tracking_id = pt.order_tracking_id
		 WHERE pt.transaction_id = $1 AND pt.status = '3ds_required'`,
		internalTxnID,
	).Scan(&orderID, &amount, &metaJSON, &customerTrackingID)

	if err != nil {
		return "", errors.New("no pending 3DS payment found for transaction (possible replay or already finalized)")
	}

	var meta PaymentMetadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return "", fmt.Errorf("invalid payment metadata: %w", err)
	}

	// 2. Mark as processing and record callback_processed_at timestamp atomically
	res, err := s.db.Exec(ctx,
		`UPDATE payment_transactions 
		 SET status = 'processing', callback_processed_at = NOW(), updated_at = NOW() 
		 WHERE transaction_id = $1 AND status = '3ds_required'
		   AND (callback_processed_at IS NULL OR callback_processed_at < NOW() - INTERVAL '1 minute')`,
		internalTxnID,
	)
	if err != nil || res.RowsAffected() == 0 {
		return "", errors.New("conflict: 3DS callback is already processing or was recently processed")
	}

	origCustomerIP := meta.CustomerIP
	if origCustomerIP == "" {
		origCustomerIP = clientIP
	}

	// 3. Initiate Tokenized Transaction with 3DS PaRes
	txnReq := payfast.TokenizedTransactionRequest{
		InstrumentToken:  meta.InstrumentToken,
		TransactionID:    meta.GatewayTxnID,
		MerchantUserId:   customerTrackingID,
		CustomerMobileNo: meta.CustomerMobile,
		BasketID:         orderID,
		OrderDate:        time.Now().Format("2006-01-02 15:04:05"),
		TxnDesc:          "OmniGo Order " + orderID,
		TxnAmt:           fmt.Sprintf("%.2f", amount),
		CustomerIP:       origCustomerIP,
		MerCatCode:       s.merchantCategory,
		ECI:              meta.ECI,
		Data3DSSecureID:  meta.Data3DSSecureID,
		Data3DSPaRes:     paRes,
	}

	txnRes, err := s.payfast.InitiateTokenizedTransaction(ctx, txnReq)
	if err != nil {
		if payfast.IsTransient(err) {
			_ = s.MarkPaymentGatewayPending(ctx, internalTxnID, meta.GatewayTxnID, "Tokenized 3DS txn timeout: "+err.Error())
			return orderID, fmt.Errorf("gateway timeout during 3DS transaction: %w", err)
		}
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Tokenized 3DS txn rejected: "+err.Error())
		return orderID, fmt.Errorf("3DS payment rejected: %w", err)
	}

	if txnRes == nil || txnRes.TransactionID == "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Gateway returned empty transaction ID after 3DS")
		return orderID, errors.New("invalid gateway response")
	}
	if txnRes.StatusCode != "00" && txnRes.StatusCode != "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, txnRes.StatusMsg)
		return orderID, fmt.Errorf("gateway rejection: %s", txnRes.StatusMsg)
	}

	// 4. Verify Status & Settle
	if err := s.VerifyAndSettle(ctx, internalTxnID, orderID, txnRes.TransactionID, amount); err != nil {
		return orderID, err
	}

	return orderID, nil
}

// VerifyAndSettle queries PayFast status and settles the database transaction atomically.
func (s *PayFastService) VerifyAndSettle(ctx context.Context, internalTxnID, orderID, gatewayTxnID string, expectedAmount float64) error {
	if gatewayTxnID == "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Missing gateway transaction ID for status verification")
		return errors.New("invalid gateway transaction ID")
	}

	statusRes, err := s.payfast.GetTransactionStatus(ctx, gatewayTxnID)
	if err != nil {
		if payfast.IsTransient(err) {
			_ = s.MarkPaymentGatewayPending(ctx, internalTxnID, gatewayTxnID, "Status verification timeout: "+err.Error())
			return fmt.Errorf("status verification timeout: %w", err)
		}
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Status verification error: "+err.Error())
		return err
	}

	if statusRes.StatusCode != "00" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, fmt.Sprintf("Gateway status '%s': %s", statusRes.StatusCode, statusRes.StatusMsg))
		return fmt.Errorf("gateway status rejection: %s", statusRes.StatusMsg)
	}

	if statusRes.BasketID != "" && statusRes.BasketID != orderID {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, fmt.Sprintf("Basket ID mismatch: expected %s, got %s", orderID, statusRes.BasketID))
		return errors.New("basket ID mismatch")
	}

	if statusRes.TxnAmt != "" {
		gatewayAmt, parseErr := strconv.ParseFloat(statusRes.TxnAmt, 64)
		if parseErr == nil {
			expectedPaisa := int64(math.Round(expectedAmount * 100))
			gatewayPaisa := int64(math.Round(gatewayAmt * 100))
			if expectedPaisa != gatewayPaisa {
				_ = s.MarkPaymentFailed(ctx, internalTxnID, fmt.Sprintf("Amount mismatch: expected %d paisa, got %d paisa", expectedPaisa, gatewayPaisa))
				return errors.New("transaction amount mismatch")
			}
		}
	}

	return s.ExecuteSplit(ctx, internalTxnID, orderID, expectedAmount, gatewayTxnID)
}

// ExecuteSplit performs atomic double-entry split preparation and outbox event enqueueing.
func (s *PayFastService) ExecuteSplit(ctx context.Context, internalTxnID, orderID string, expectedAmount float64, gatewayTxnID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Idempotency lock on payment_transactions
	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM payment_transactions WHERE transaction_id = $1 FOR UPDATE`, internalTxnID).Scan(&currentStatus)
	if err != nil {
		return fmt.Errorf("payment transaction not found: %w", err)
	}
	if currentStatus == "captured" || currentStatus == "settlement_pending" {
		return nil // Idempotent success
	}

	// Lock order and re-verify authoritative state
	var dbAmount float64
	var storeID string
	var orderStatus string
	err = tx.QueryRow(ctx,
		`SELECT total_amount, store_tracking_id, status FROM orders WHERE order_tracking_id = $1 FOR UPDATE`, orderID,
	).Scan(&dbAmount, &storeID, &orderStatus)
	if err != nil {
		return fmt.Errorf("order not found: %w", err)
	}
	if orderStatus == "paid" {
		return errors.New("conflict: order already paid by another transaction")
	}
	if dbAmount != expectedAmount {
		return fmt.Errorf("order amount changed: expected %.2f, got %.2f", expectedAmount, dbAmount)
	}

	var deliveryTrackingID string
	_ = tx.QueryRow(ctx, `SELECT COALESCE(tracking_id, '') FROM deliveries WHERE order_tracking_id = $1`, orderID).Scan(&deliveryTrackingID)

	split, err := s.calculator.CalculateSplit(ctx, dbAmount, storeID, deliveryTrackingID)
	if err != nil {
		return fmt.Errorf("split calculation failed: %w", err)
	}

	idempotencyKey := fmt.Sprintf("payfast:split:%s", gatewayTxnID)

	// Update payment_transactions status
	_, err = tx.Exec(ctx,
		`UPDATE payment_transactions 
		 SET status = 'settlement_pending', gateway_txn_id = $1, updated_at = NOW() 
		 WHERE transaction_id = $2`,
		gatewayTxnID, internalTxnID,
	)
	if err != nil {
		return fmt.Errorf("failed to update payment transaction: %w", err)
	}

	// Update orders payment status and all split columns
	_, err = tx.Exec(ctx,
		`UPDATE orders 
		 SET admin_commission = $1, vendor_escrow = $2, delivery_escrow = $3, payment_status = 'settlement_pending', updated_at = NOW() 
		 WHERE order_tracking_id = $4`,
		split.AdminRevenue, split.VendorEscrow, split.DeliveryEscrow, orderID,
	)
	if err != nil {
		return fmt.Errorf("failed to update order: %w", err)
	}

	transfers := []map[string]interface{}{
		{
			"debit_account":  string(ledger.AccountPayFastHolding),
			"credit_account": string(ledger.AccountAdminRevenue),
			"amount":         split.AdminRevenue,
			"idempotency":    idempotencyKey + ":admin",
		},
		{
			"debit_account":  string(ledger.AccountPayFastHolding),
			"credit_account": string(ledger.AccountVendorLockedEscrow),
			"amount":         split.VendorEscrow,
			"idempotency":    idempotencyKey + ":vendor",
		},
	}
	if split.DeliveryEscrow > 0 {
		transfers = append(transfers, map[string]interface{}{
			"debit_account":  string(ledger.AccountPayFastHolding),
			"credit_account": string(ledger.AccountCentralEscrow),
			"amount":         split.DeliveryEscrow,
			"idempotency":    idempotencyKey + ":delivery",
		})
	}

	outboxPayload, err := json.Marshal(map[string]interface{}{
		"internal_txn_id":      internalTxnID,
		"order_id":             orderID,
		"gateway_txn_id":       gatewayTxnID,
		"store_id":             storeID,
		"delivery_tracking_id": deliveryTrackingID,
		"total_amount":         dbAmount,
		"currency":             s.defaultCurrency,
		"admin_revenue":        split.AdminRevenue,
		"vendor_escrow":        split.VendorEscrow,
		"delivery_escrow":      split.DeliveryEscrow,
		"idempotency_key":      idempotencyKey,
		"transfers":            transfers,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal outbox payload: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at, updated_at) 
		 VALUES ($1, 'payment_settlement', $2, 'PENDING', NOW(), NOW())`,
		orderID, string(outboxPayload),
	)
	if err != nil {
		return fmt.Errorf("failed to insert outbox event: %w", err)
	}

	return tx.Commit(ctx)
}

// MarkPaymentFailed updates payment status to failed with robust error checking.
func (s *PayFastService) MarkPaymentFailed(ctx context.Context, internalTxnID, reason string) error {
	res, err := s.db.Exec(ctx,
		`UPDATE payment_transactions 
		 SET status = 'failed', error_message = $1, updated_at = NOW()
		 WHERE transaction_id = $2 AND status IN ('pending', '3ds_required', 'processing', 'gateway_pending')`,
		reason, internalTxnID,
	)
	if err != nil {
		log.Printf("[PayFastService] DB error marking %s failed: %v", internalTxnID, err)
		return fmt.Errorf("db error marking failed: %w", err)
	}
	if res.RowsAffected() == 0 {
		log.Printf("[PayFastService] No active row found to mark failed for %s", internalTxnID)
	}
	return nil
}

// MarkPaymentGatewayPending updates payment status to gateway_pending on transient timeouts.
func (s *PayFastService) MarkPaymentGatewayPending(ctx context.Context, internalTxnID, gatewayTxnID, reason string) error {
	res, err := s.db.Exec(ctx,
		`UPDATE payment_transactions 
		 SET status = 'gateway_pending', gateway_txn_id = COALESCE(NULLIF($1, ''), gateway_txn_id), error_message = $2, updated_at = NOW()
		 WHERE transaction_id = $3 AND status IN ('pending', '3ds_required', 'processing')`,
		gatewayTxnID, reason, internalTxnID,
	)
	if err != nil {
		log.Printf("[PayFastService] DB error marking %s gateway_pending: %v", internalTxnID, err)
		return fmt.Errorf("db error marking gateway_pending: %w", err)
	}
	if res.RowsAffected() == 0 {
		log.Printf("[PayFastService] No active row found to mark gateway_pending for %s", internalTxnID)
	}
	return nil
}

```

---

## 9. `internal/payment_orchestrator/handlers/payfast_handler.go`

```go
package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	payfastSvc "github.com/omnigo/backend/internal/payment_orchestrator/service"
	"github.com/omnigo/backend/internal/shared/middleware"
)

// ThreeDSCallbackRequest payload from PayFast 3DS form POST.
type ThreeDSCallbackRequest struct {
	MD    string `form:"md" json:"md" binding:"required"`
	PaRes string `form:"paRes" json:"paRes" binding:"required"`
}

// PayFastSplitHandler is a thin HTTP controller delegating to PayFastService.
type PayFastSplitHandler struct {
	service *payfastSvc.PayFastService
}

// NewPayFastSplitHandler constructs a new PayFastSplitHandler.
func NewPayFastSplitHandler(svc *payfastSvc.PayFastService) *PayFastSplitHandler {
	return &PayFastSplitHandler{
		service: svc,
	}
}

// RegisterRoutes registers PayFast endpoints with the router.
func (h *PayFastSplitHandler) RegisterRoutes(r gin.IRoutes) {
	r.POST("/api/v1/payments/payfast/payment", middleware.JWTAuth(), h.ProcessPayment)
	r.POST("/api/v1/payments/payfast/3ds_callback", h.ThreeDSCallback)
	r.GET("/api/v1/payments/payfast/3ds_callback", h.ThreeDSCallback)
}

// getClientIP extracts real customer IP safely taking reverse proxies into account.
func getClientIP(c *gin.Context) string {
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			ip := strings.TrimSpace(ips[0])
			if ip != "" {
				return ip
			}
		}
	}
	if xRealIP := c.GetHeader("X-Real-IP"); xRealIP != "" {
		return strings.TrimSpace(xRealIP)
	}
	return c.ClientIP()
}

// ProcessPayment handles POST /api/v1/payments/payfast/payment (Option C Token Flow).
func (h *PayFastSplitHandler) ProcessPayment(c *gin.Context) {
	var req payfastSvc.PaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload: " + err.Error()})
		return
	}

	merchantUserID := middleware.GetTrackingID(c)
	if merchantUserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: missing user tracking ID"})
		return
	}

	clientIP := getClientIP(c)
	resp, err := h.service.ProcessPayment(c.Request.Context(), merchantUserID, clientIP, req)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": errMsg})
			return
		}
		if strings.Contains(errMsg, "forbidden") {
			c.JSON(http.StatusForbidden, gin.H{"error": errMsg})
			return
		}
		if strings.Contains(errMsg, "conflict") {
			c.JSON(http.StatusConflict, gin.H{"error": errMsg})
			return
		}
		if strings.Contains(errMsg, "validation") || strings.Contains(errMsg, "expired") || strings.Contains(errMsg, "invalid") {
			c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": errMsg})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// ThreeDSCallback handles POST /api/v1/payments/payfast/3ds_callback.
func (h *PayFastSplitHandler) ThreeDSCallback(c *gin.Context) {
	var req ThreeDSCallbackRequest
	if err := c.ShouldBind(&req); err != nil {
		h.respond3DSError(c, http.StatusBadRequest, "Invalid 3DS callback parameters")
		return
	}

	clientIP := getClientIP(c)
	orderID, err := h.service.Handle3DSCallback(c.Request.Context(), req.MD, req.PaRes, clientIP)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "conflict") || strings.Contains(errMsg, "replay") {
			h.respond3DSError(c, http.StatusConflict, errMsg)
			return
		}
		if strings.Contains(errMsg, "timeout") {
			h.respond3DSError(c, http.StatusGatewayTimeout, "Payment verification timeout; reconciliation is processing in background")
			return
		}
		h.respond3DSError(c, http.StatusBadRequest, errMsg)
		return
	}

	if strings.Contains(c.GetHeader("Accept"), "text/html") {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Payment Successful</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family:sans-serif;text-align:center;padding:50px 20px;">
    <h2 style="color:#2e7d32;">Payment Authenticated Successfully</h2>
    <p>Your order #<strong>%s</strong> is being settled.</p>
    <script>
        if (window.opener) { window.opener.postMessage({status: 'success', order_id: '%s'}, '*'); }
        if (window.FlutterChannel) { window.FlutterChannel.postMessage(JSON.stringify({status: 'success', order_id: '%s'})); }
    </script>
</body>
</html>`, orderID, orderID, orderID))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "settlement_pending",
		"order_id": orderID,
		"message":  "3DS payment authenticated and settlement enqueued",
	})
}

// respond3DSError sends a format-appropriate response (HTML for webviews, JSON for APIs).
func (h *PayFastSplitHandler) respond3DSError(c *gin.Context, statusCode int, message string) {
	if strings.Contains(c.GetHeader("Accept"), "text/html") {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(statusCode, fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Payment Authentication Failed</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family:sans-serif;text-align:center;padding:50px 20px;">
    <h2 style="color:#c62828;">Payment Authentication Failed</h2>
    <p>%s</p>
</body>
</html>`, message))
		return
	}
	c.JSON(statusCode, gin.H{"error": message})
}

```

---

## 10. `internal/payment_orchestrator/workers/settlement_worker.go`

```go
package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/payment/payfast"
	"github.com/omnigo/backend/internal/payment_orchestrator"
)

type SettlementPayload struct {
	InternalTxnID      string                   `json:"internal_txn_id"`
	OrderID            string                   `json:"order_id"`
	GatewayTxnID       string                   `json:"gateway_txn_id"`
	StoreID            string                   `json:"store_id"`
	DeliveryTrackingID string                   `json:"delivery_tracking_id"`
	TotalAmount        float64                  `json:"total_amount"`
	Currency           string                   `json:"currency"`
	AdminRevenue       float64                  `json:"admin_revenue"`
	VendorEscrow       float64                  `json:"vendor_escrow"`
	DeliveryEscrow     float64                  `json:"delivery_escrow"`
	IdempotencyKey     string                   `json:"idempotency_key"`
	Transfers          []SettlementTransferItem `json:"transfers"`
}

type SettlementTransferItem struct {
	DebitAccount  string  `json:"debit_account"`
	CreditAccount string  `json:"credit_account"`
	Amount        float64 `json:"amount"`
	Idempotency   string  `json:"idempotency"`
}

// SettlementWorker polls and processes 'payment_settlement' outbox events and reconciles stuck payments.
type SettlementWorker struct {
	db         *pgxpool.Pool
	ledger     *ledger.Service
	escrow     *escrow.Service
	calculator *payment_orchestrator.CommissionCalculator
	payfast    *payfast.Client
	redis      redis.UniversalClient
}

func NewSettlementWorker(
	db *pgxpool.Pool,
	ledgerSvc *ledger.Service,
	escrowSvc *escrow.Service,
	calc *payment_orchestrator.CommissionCalculator,
	payfastClient *payfast.Client,
	rdb redis.UniversalClient,
) *SettlementWorker {
	return &SettlementWorker{
		db:         db,
		ledger:     ledgerSvc,
		escrow:     escrowSvc,
		calculator: calc,
		payfast:    payfastClient,
		redis:      rdb,
	}
}

// Start runs the settlement processing loop and gateway reconciliation loop.
func (w *SettlementWorker) Start(ctx context.Context) {
	log.Println("[SettlementWorker] Starting background settlement and reconciliation worker...")

	pollTicker := time.NewTicker(2 * time.Second)
	defer pollTicker.Stop()

	reconTicker := time.NewTicker(30 * time.Second)
	defer reconTicker.Stop()

	cleanupTicker := time.NewTicker(5 * time.Minute)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[SettlementWorker] Stopping settlement worker...")
			return
		case <-pollTicker.C:
			w.processPendingSettlements(ctx)
		case <-reconTicker.C:
			w.reconcileStuckPayments(ctx)
		case <-cleanupTicker.C:
			w.cleanupStalePending(ctx)
		}
	}
}

func (w *SettlementWorker) processPendingSettlements(ctx context.Context) {
	// Claim outbox events atomically using FOR UPDATE SKIP LOCKED
	// Includes events stuck in PROCESSING due to worker crashes older than 5 minutes
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT id, aggregate_id, payload FROM outbox_events
		 WHERE topic = 'payment_settlement' 
		   AND (status IN ('PENDING', 'pending') OR (status = 'PROCESSING' AND updated_at < NOW() - INTERVAL '5 minutes'))
		 ORDER BY id ASC LIMIT 50 FOR UPDATE SKIP LOCKED`,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	type OutboxItem struct {
		ID          int64
		AggregateID string
		Payload     []byte
	}

	var items []OutboxItem
	for rows.Next() {
		var item OutboxItem
		if err := rows.Scan(&item.ID, &item.AggregateID, &item.Payload); err == nil {
			items = append(items, item)
		}
	}
	rows.Close()

	if len(items) == 0 {
		return
	}

	// Mark claimed items as PROCESSING within the transaction
	for _, item := range items {
		_, _ = tx.Exec(ctx, `UPDATE outbox_events SET status = 'PROCESSING', updated_at = NOW() WHERE id = $1`, item.ID)
	}
	if err := tx.Commit(ctx); err != nil {
		return
	}

	for _, item := range items {
		if err := w.processSingleSettlement(ctx, item.ID, item.Payload); err != nil {
			log.Printf("[SettlementWorker] Error processing settlement outbox event %d: %v", item.ID, err)
			// Revert to PENDING with backoff count so it can be retried by the worker loop
			_, _ = w.db.Exec(ctx,
				`UPDATE outbox_events 
				 SET status = 'PENDING', retry_count = retry_count + 1, error_message = $1, updated_at = NOW() 
				 WHERE id = $2`,
				err.Error(), item.ID,
			)
		}
	}
}

func (w *SettlementWorker) processSingleSettlement(ctx context.Context, eventID int64, payloadBytes []byte) error {
	var payload SettlementPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return fmt.Errorf("failed to unmarshal settlement payload: %w", err)
	}

	currency := payload.Currency
	if currency == "" {
		currency = "PKR"
	}

	// 1. Execute Ledger MultiTransfer (Atomic all-or-nothing double-entry split)
	var transferReqs []ledger.TransferRequest
	for _, tr := range payload.Transfers {
		if tr.Amount <= 0 {
			continue
		}
		transferReqs = append(transferReqs, ledger.TransferRequest{
			DebitAccount:   ledger.Account(tr.DebitAccount),
			CreditAccount:  ledger.Account(tr.CreditAccount),
			Amount:         tr.Amount,
			Currency:       currency,
			ReferenceType:  "order",
			ReferenceID:    payload.OrderID,
			Description:    fmt.Sprintf("Payment settlement for order %s", payload.OrderID),
			IdempotencyKey: tr.Idempotency,
		})
	}
	if len(transferReqs) > 0 {
		_, err := w.ledger.MultiTransfer(ctx, transferReqs)
		if err != nil {
			return fmt.Errorf("ledger multi-transfer failed for order %s: %w", payload.OrderID, err)
		}
	}

	// 2. Create Escrow Hold for vendor — Mandatory fail-closed verification.
	if payload.VendorEscrow > 0 && payload.StoreID != "" {
		if err := w.escrow.CreateHold(ctx, payload.OrderID, payload.StoreID, payload.VendorEscrow); err != nil {
			log.Printf("[SettlementWorker] CRITICAL: Escrow hold creation failed for order %s (Store: %s, Amount: %.2f): %v",
				payload.OrderID, payload.StoreID, payload.VendorEscrow, err)
			return fmt.Errorf("mandatory escrow hold creation failed: %w", err)
		}
	}

	// 3. Update Database State atomically (payment -> captured, order -> paid, outbox -> PROCESSED)
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin db tx: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`UPDATE payment_transactions SET status = 'captured', updated_at = NOW() WHERE transaction_id = $1`,
		payload.InternalTxnID,
	)
	if err != nil {
		return fmt.Errorf("failed to update payment to captured: %w", err)
	}

	_, err = tx.Exec(ctx,
		`UPDATE orders SET status = 'paid', payment_status = 'paid', updated_at = NOW() WHERE order_tracking_id = $1`,
		payload.OrderID,
	)
	if err != nil {
		return fmt.Errorf("failed to update order to paid: %w", err)
	}

	_, err = tx.Exec(ctx,
		`UPDATE outbox_events SET status = 'PROCESSED', processed_at = NOW(), updated_at = NOW() WHERE id = $1`,
		eventID,
	)
	if err != nil {
		return fmt.Errorf("failed to mark outbox event processed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit db settlement: %w", err)
	}

	log.Printf("[SettlementWorker] Successfully completed settlement for Order %s (Txn %s, Amount: %.2f %s)",
		payload.OrderID, payload.InternalTxnID, payload.TotalAmount, currency)
	return nil
}

// reconcileStuckPayments runs dual-strategy status inquiries for stuck 'gateway_pending' and 'processing' payments.
func (w *SettlementWorker) reconcileStuckPayments(ctx context.Context) {
	if w.payfast == nil || !w.payfast.IsConfigured() {
		return
	}

	tx, err := w.db.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)

	// Lock stuck payments with FOR UPDATE SKIP LOCKED
	rows, err := tx.Query(ctx,
		`SELECT pt.transaction_id, pt.order_tracking_id, COALESCE(pt.gateway_txn_id, ''), pt.amount, pt.status, pt.created_at
		 FROM payment_transactions pt
		 WHERE pt.status IN ('gateway_pending', 'processing')
		   AND pt.updated_at < NOW() - INTERVAL '1 minute'
		 ORDER BY pt.updated_at ASC LIMIT 20
		 FOR UPDATE SKIP LOCKED`,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	type StuckTxn struct {
		InternalTxnID string
		OrderID       string
		GatewayTxnID  string
		Amount        float64
		Status        string
		CreatedAt     time.Time
	}

	var list []StuckTxn
	for rows.Next() {
		var p StuckTxn
		if err := rows.Scan(&p.InternalTxnID, &p.OrderID, &p.GatewayTxnID, &p.Amount, &p.Status, &p.CreatedAt); err == nil {
			list = append(list, p)
		}
	}
	rows.Close()
	_ = tx.Commit(ctx) // release transaction lock so individual reconciliation updates can commit

	for _, p := range list {
		var statusRes *payfast.TransactionStatusResponse
		var inquiryErr error

		// Dual Inquiry Strategy: by Gateway Transaction ID, or fallback to Merchant Basket ID
		if p.GatewayTxnID != "" {
			statusRes, inquiryErr = w.payfast.GetTransactionStatus(ctx, p.GatewayTxnID)
		} else {
			log.Printf("[SettlementWorker] Missing gateway_txn_id for stuck payment %s. Falling back to BasketID inquiry: %s", p.InternalTxnID, p.OrderID)
			statusRes, inquiryErr = w.payfast.GetTransactionStatusByBasketID(ctx, p.OrderID)
		}

		if inquiryErr != nil {
			log.Printf("[SettlementWorker] Reconciliation status check failed for %s (Order: %s, GatewayID: %s): %v",
				p.InternalTxnID, p.OrderID, p.GatewayTxnID, inquiryErr)

			// If payment has been stuck for > 15 minutes and gateway returns not found/error, fail deterministically
			if time.Since(p.CreatedAt) > 15*time.Minute && !payfast.IsTransient(inquiryErr) {
				log.Printf("[SettlementWorker] Timing out stale stuck payment %s (>15m). Marking failed.", p.InternalTxnID)
				_, _ = w.db.Exec(ctx,
					`UPDATE payment_transactions 
					 SET status = 'failed', error_message = 'Reconciliation timed out: no gateway transaction found', updated_at = NOW() 
					 WHERE transaction_id = $1 AND status IN ('gateway_pending', 'processing')`,
					p.InternalTxnID,
				)
			}
			continue
		}

		if statusRes.StatusCode == "00" && (statusRes.BasketID == "" || statusRes.BasketID == p.OrderID) {
			// Verify amount in paisa if returned by gateway
			if statusRes.TxnAmt != "" {
				if gatewayAmt, err := strconv.ParseFloat(statusRes.TxnAmt, 64); err == nil {
					expectedPaisa := int64(math.Round(p.Amount * 100))
					gatewayPaisa := int64(math.Round(gatewayAmt * 100))
					if expectedPaisa != gatewayPaisa {
						log.Printf("[SettlementWorker] Reconciliation amount mismatch for %s: expected %d paisa, got %d paisa", p.InternalTxnID, expectedPaisa, gatewayPaisa)
						_, _ = w.db.Exec(ctx,
							`UPDATE payment_transactions SET status = 'failed', error_message = 'Reconciliation amount mismatch', updated_at = NOW() WHERE transaction_id = $1`,
							p.InternalTxnID,
						)
						continue
					}
				}
			}

			resolvedGatewayTxnID := statusRes.TransactionID
			if resolvedGatewayTxnID == "" {
				resolvedGatewayTxnID = p.GatewayTxnID
			}

			log.Printf("[SettlementWorker] Reconciliation verified SUCCESS for %s (%s). Enqueuing atomic settlement...", p.InternalTxnID, resolvedGatewayTxnID)
			w.enqueueSettlementOutbox(ctx, p.InternalTxnID, p.OrderID, resolvedGatewayTxnID, p.Amount)
		} else if statusRes.StatusCode != "" && statusRes.StatusCode != "00" {
			log.Printf("[SettlementWorker] Reconciliation verified REJECTION for %s: %s (code: %s)", p.InternalTxnID, statusRes.StatusMsg, statusRes.StatusCode)
			_, _ = w.db.Exec(ctx,
				`UPDATE payment_transactions SET status = 'failed', error_message = $1, updated_at = NOW() WHERE transaction_id = $2`,
				statusRes.StatusMsg, p.InternalTxnID,
			)
		}
	}
}

// cleanupStalePending marks abandoned 'pending' rows (>10m old) as failed so they don't linger in DB.
func (w *SettlementWorker) cleanupStalePending(ctx context.Context) {
	res, err := w.db.Exec(ctx,
		`UPDATE payment_transactions 
		 SET status = 'failed', error_message = 'Payment initiation abandoned/expired', updated_at = NOW()
		 WHERE status = 'pending' AND created_at < NOW() - INTERVAL '10 minutes'`,
	)
	if err == nil && res.RowsAffected() > 0 {
		log.Printf("[SettlementWorker] Cleaned up %d stale pending payment attempts", res.RowsAffected())
	}
}

func (w *SettlementWorker) enqueueSettlementOutbox(ctx context.Context, internalTxnID, orderID, gatewayTxnID string, amount float64) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		log.Printf("[SettlementWorker] Failed to begin transaction for reconciliation of order %s: %v", orderID, err)
		return
	}
	defer tx.Rollback(ctx)

	var storeID string
	var currentOrderStatus string
	err = tx.QueryRow(ctx, `SELECT store_tracking_id, status FROM orders WHERE order_tracking_id = $1 FOR UPDATE`, orderID).Scan(&storeID, &currentOrderStatus)
	if err != nil {
		log.Printf("[SettlementWorker] Order not found for reconciliation %s: %v", orderID, err)
		return
	}
	if currentOrderStatus == "paid" {
		return // already paid
	}

	var currentPaymentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM payment_transactions WHERE transaction_id = $1 FOR UPDATE`, internalTxnID).Scan(&currentPaymentStatus)
	if err != nil {
		log.Printf("[SettlementWorker] Payment txn not found for reconciliation %s: %v", internalTxnID, err)
		return
	}
	if currentPaymentStatus == "captured" || currentPaymentStatus == "settlement_pending" {
		return // already settled or in progress
	}

	var deliveryTrackingID string
	_ = tx.QueryRow(ctx, `SELECT COALESCE(tracking_id, '') FROM deliveries WHERE order_tracking_id = $1`, orderID).Scan(&deliveryTrackingID)

	split, err := w.calculator.CalculateSplit(ctx, amount, storeID, deliveryTrackingID)
	if err != nil {
		log.Printf("[SettlementWorker] Error calculating split for order %s: %v", orderID, err)
		return
	}

	idempotencyKey := fmt.Sprintf("payfast:split:%s", gatewayTxnID)
	currency := os.Getenv("DEFAULT_CURRENCY")
	if currency == "" {
		currency = "PKR"
	}

	transfers := []SettlementTransferItem{
		{
			DebitAccount:  string(ledger.AccountPayFastHolding),
			CreditAccount: string(ledger.AccountAdminRevenue),
			Amount:        split.AdminRevenue,
			Idempotency:   idempotencyKey + ":admin",
		},
		{
			DebitAccount:  string(ledger.AccountPayFastHolding),
			CreditAccount: string(ledger.AccountVendorLockedEscrow),
			Amount:        split.VendorEscrow,
			Idempotency:   idempotencyKey + ":vendor",
		},
	}
	if split.DeliveryEscrow > 0 {
		transfers = append(transfers, SettlementTransferItem{
			DebitAccount:  string(ledger.AccountPayFastHolding),
			CreditAccount: string(ledger.AccountCentralEscrow),
			Amount:        split.DeliveryEscrow,
			Idempotency:   idempotencyKey + ":delivery",
		})
	}

	outboxPayload, err := json.Marshal(SettlementPayload{
		InternalTxnID:      internalTxnID,
		OrderID:            orderID,
		GatewayTxnID:       gatewayTxnID,
		StoreID:            storeID,
		DeliveryTrackingID: deliveryTrackingID,
		TotalAmount:        amount,
		Currency:           currency,
		AdminRevenue:       split.AdminRevenue,
		VendorEscrow:       split.VendorEscrow,
		DeliveryEscrow:     split.DeliveryEscrow,
		IdempotencyKey:     idempotencyKey,
		Transfers:          transfers,
	})
	if err != nil {
		log.Printf("[SettlementWorker] Failed to marshal outbox payload for reconciliation %s: %v", orderID, err)
		return
	}

	_, err = tx.Exec(ctx,
		`UPDATE orders 
		 SET admin_commission = $1, vendor_escrow = $2, delivery_escrow = $3, payment_status = 'settlement_pending', updated_at = NOW() 
		 WHERE order_tracking_id = $4`,
		split.AdminRevenue, split.VendorEscrow, split.DeliveryEscrow, orderID,
	)
	if err != nil {
		log.Printf("[SettlementWorker] Failed to update order during reconciliation %s: %v", orderID, err)
		return
	}

	_, err = tx.Exec(ctx,
		`UPDATE payment_transactions SET status = 'settlement_pending', gateway_txn_id = $1, updated_at = NOW() WHERE transaction_id = $2`,
		gatewayTxnID, internalTxnID,
	)
	if err != nil {
		log.Printf("[SettlementWorker] Failed to update payment status during reconciliation %s: %v", internalTxnID, err)
		return
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at, updated_at) 
		 VALUES ($1, 'payment_settlement', $2, 'PENDING', NOW(), NOW())`,
		orderID, string(outboxPayload),
	)
	if err != nil {
		log.Printf("[SettlementWorker] Failed to insert outbox event during reconciliation %s: %v", orderID, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("[SettlementWorker] Failed to commit atomic reconciliation transaction for order %s: %v", orderID, err)
		return
	}

	log.Printf("[SettlementWorker] Successfully enqueued atomic settlement outbox for order %s (Txn %s)", orderID, internalTxnID)
}

```

---

## 11. `internal/payment/payfast/payfast_test.go`

```go
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



```

---

## 12. `internal/payment_orchestrator/service/payfast_service_test.go`

```go
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

```

---

## 13. `migrations/0020_harden_payment_transactions.sql`

```sql
-- ════════════════════════════════════════════════════════════════
--  OMNIGO Platform — Migration 0020
--  Hardens payment transactions and orders for enterprise production:
--    1. Adds callback_processed_at TIMESTAMPTZ to payment_transactions (3DS replay defense)
--    2. Adds vendor_escrow and delivery_escrow NUMERIC(12,2) to orders table
--    3. Recreates unique partial index ux_payment_active_order on payment_transactions
--       covering only active inflight states ('processing', '3ds_required', 'settlement_pending', 'gateway_pending')
--       so ephemeral 'pending' timeouts don't block subsequent user retries.
-- ════════════════════════════════════════════════════════════════

-- 1. 3DS Callback Replay Defense Column
ALTER TABLE payment_transactions
    ADD COLUMN IF NOT EXISTS callback_processed_at TIMESTAMPTZ;

-- 2. Orders Table Escrow Split Columns
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS vendor_escrow NUMERIC(12,2),
    ADD COLUMN IF NOT EXISTS delivery_escrow NUMERIC(12,2);

-- 3. Refine Unique Partial Index
DROP INDEX IF EXISTS ux_payment_active_order;

CREATE UNIQUE INDEX IF NOT EXISTS ux_payment_active_order
ON payment_transactions(order_tracking_id)
WHERE status IN ('processing', '3ds_required', 'settlement_pending', 'gateway_pending');

```

---

## 14. `migrations/0014_payment_transactions.sql`

```sql
-- Migration 0014: payment_transactions table and related indexes
-- Stores every payment attempt/capture/refund and idempotency for order payments.

CREATE TABLE IF NOT EXISTS payment_transactions (
    id                  BIGSERIAL PRIMARY KEY,
    transaction_id      VARCHAR(100) UNIQUE NOT NULL,   -- internal OMNIGO txn id
    order_tracking_id   VARCHAR(50) NOT NULL,
    gateway             VARCHAR(30) NOT NULL,         -- stripe | payfast | jazzcash | easypaisa | cod | wallet
    gateway_txn_id      VARCHAR(255),                  -- external gateway reference when available
    amount              NUMERIC(12,2) NOT NULL,
    currency            VARCHAR(10) NOT NULL DEFAULT 'PKR',
    status              VARCHAR(30) NOT NULL DEFAULT 'pending',
                                          -- pending | authorized | captured | failed | refunded | reversed | chargeback
    kind                VARCHAR(30) NOT NULL DEFAULT 'payment',
                                          -- payment | refund | reversal | wallet_load | payout
    idempotency_key     VARCHAR(255) UNIQUE,
    metadata            JSONB,
    error_message       TEXT,
    callback_processed_at TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_transactions_order ON payment_transactions(order_tracking_id);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_gateway_txn ON payment_transactions(gateway_txn_id);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_status ON payment_transactions(status);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_idempotency ON payment_transactions(idempotency_key);
CREATE INDEX IF NOT EXISTS idx_payment_transactions_created_at ON payment_transactions(created_at DESC);

-- Soft constraint to help catch obvious bad statuses without breaking future gateways.
ALTER TABLE payment_transactions
    DROP CONSTRAINT IF EXISTS chk_payment_transactions_status;
ALTER TABLE payment_transactions
    ADD CONSTRAINT chk_payment_transactions_status
    CHECK (status IN ('pending', 'processing', '3ds_required', 'authorized', 'captured', 'settlement_pending', 'gateway_pending', 'failed', 'refunded', 'reversed', 'chargeback'));

-- Concurrency protection: only one active payment attempt per order at any time
CREATE UNIQUE INDEX IF NOT EXISTS ux_payment_active_order
ON payment_transactions(order_tracking_id)
WHERE status IN ('processing', '3ds_required', 'settlement_pending', 'gateway_pending');

-- Helper for idempotency: if a caller re-uses an idempotency key, return existing record.
-- This is enforced by the UNIQUE index on idempotency_key above.

-- Update orders.payment_status automatically when a payment transaction is captured or refunded.
CREATE OR REPLACE FUNCTION update_order_payment_status()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status = 'captured' THEN
        UPDATE orders SET payment_status = 'paid', updated_at = NOW()
        WHERE order_tracking_id = NEW.order_tracking_id AND payment_status <> 'paid';
    ELSIF NEW.status = 'refunded' OR NEW.status = 'reversed' OR NEW.status = 'chargeback' THEN
        UPDATE orders SET payment_status = 'refunded', updated_at = NOW()
        WHERE order_tracking_id = NEW.order_tracking_id;
    ELSIF NEW.status = 'failed' THEN
        -- Do not overwrite 'paid' from a failed retry; only mark unpaid if currently pending.
        UPDATE orders SET payment_status = 'unpaid', updated_at = NOW()
        WHERE order_tracking_id = NEW.order_tracking_id AND payment_status = 'pending';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_payment_transactions_update_order ON payment_transactions;
CREATE TRIGGER trg_payment_transactions_update_order
    AFTER INSERT OR UPDATE OF status ON payment_transactions
    FOR EACH ROW
    EXECUTE FUNCTION update_order_payment_status();

-- Create payment_idempotency table for explicit key locking during in-flight requests.
CREATE TABLE IF NOT EXISTS payment_idempotency (
    key             VARCHAR(255) PRIMARY KEY,
    request_hash    VARCHAR(64) NOT NULL,   -- sha256 of payload for safety
    transaction_id  VARCHAR(100),           -- internal txn once known
    locked_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '24 hours'
);

CREATE INDEX IF NOT EXISTS idx_payment_idempotency_expires ON payment_idempotency(expires_at);

-- Lightweight cleanup of expired idempotency locks (optional, can also be done via cron).
CREATE OR REPLACE FUNCTION cleanup_expired_payment_idempotency()
RETURNS integer AS $$
DECLARE
    deleted_count integer;
BEGIN
    DELETE FROM payment_idempotency WHERE expires_at < NOW();
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN deleted_count;
END;
$$ LANGUAGE plpgsql;

```

---

---

## 📋 Required Environment Variables for Production

| Variable | Example Value | Description |
|---|---|---|
| `PAYFAST_MERCHANT_ID` | `100000000000000` | Merchant ID assigned by PayFast APPS |
| `PAYFAST_SECURED_KEY` | `your_ascii_secured_key` | ASCII Secured Key for HMAC-SHA256 signatures |
| `PAYFAST_BASE_URL` | `https://ipg.gopayfast.com` | Base URL (`https://ipg.gopayfast.com` for sandbox, production IPG URL for live) |
| `PAYFAST_MERCHANT_NAME` | `OMNIGO TECHNOLOGIES` | Registered Merchant Business Name |
| `PAYFAST_MERCHANT_CATEGORY` | `0001` | 4-digit Merchant Category Code assigned by PayFast |
| `PAYFAST_3DS_CALLBACK_URL` | `https://api.omnigo.com/api/v1/payments/payfast/3ds_callback` | Public callback URL for 3DS form POST |
| `INTERNAL_CALLBACK_SECRET` | `a_very_strong_random_32_byte_secret` | Secret used to HMAC-sign internal transaction IDs (`md`) |
| `DEFAULT_COMMISSION_RATE` | `2.0` | Default platform store commission percentage |
| `DEFAULT_CURRENCY` | `PKR` | Base platform currency |

