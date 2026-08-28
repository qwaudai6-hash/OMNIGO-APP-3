package payfast

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Client handles all interaction with PayFast Pakistan gateway.
//
// Per PayFast's official clarification: "Use the PayFast Secured Key for secured_hash.
// No separate HASH_KEY is required." The same SECURED_KEY is used for both OAuth
// token fetch AND HMAC-SHA256 signing of secured_hash on all API requests.
// PAYFAST_HASH_KEY is optional — if not set, SECURED_KEY is used for everything.
type Client struct {
	merchantID     string
	securedKey     string
	hashKey        string   // optional override; defaults to securedKey if empty
	merchantName   string
	baseURL        string
	successURL     string
	failureURL     string
	httpClient     *http.Client
	timeout        time.Duration
	tokens         *TokenManager
	circuitBreaker *CircuitBreaker
}

// NewClient constructs a production-ready PayFast client from environment variables or custom config.
//
// Construction NEVER panics: this client is built unconditionally at startup by services
// that also serve non-PayFast traffic (payment orchestrator hosts Stripe/COD/JazzCash
// routes too). A missing PayFast config must degrade to IsConfigured()==false — which
// keeps those other payment methods alive — not take the whole process down.
func NewClient(merchantID, securedKey, hashKey, merchantName, baseURL string) *Client {
	if baseURL == "" {
		baseURL = os.Getenv("PAYFAST_BASE_URL")
	}
	if baseURL == "" {
		// PAYFAST_API_URL is the name used across this repo's .env templates and older
		// integrations; accept it as an alias so template-based deployments work.
		baseURL = os.Getenv("PAYFAST_API_URL")
	}

	if merchantID != "" && securedKey != "" && baseURL == "" {
		// Opted INTO PayFast but no endpoint: loud, unmissable startup error. The service
		// still boots (other gateways keep working) and PayFast calls fail with a clear
		// ErrNotConfigured instead of crashing at init.
		log.Printf("ERROR: [payfast] PAYFAST_MERCHANT_ID/PAYFAST_SECURED_KEY are set but no gateway URL found " +
			"(checked PAYFAST_BASE_URL and PAYFAST_API_URL). PayFast payments are DISABLED until this is fixed. " +
			"Example: PAYFAST_BASE_URL=https://ipg.gopayfast.com (sandbox)")
	}

	// Per-call gateway timeout: operationally tuned via PAYFAST_GATEWAY_TIMEOUT_SECONDS
	// (must cover auth + API round-trips on slow issuer networks). No magic constant.
	gatewayTimeout := DefaultGatewayTimeout()
	httpClient := &http.Client{
		Timeout: gatewayTimeout,
	}

	cb := NewCircuitBreaker(5, 10*time.Second)
	tokenCb := NewCircuitBreaker(5, 10*time.Second)
	if hashKey == "" && securedKey != "" && baseURL != "" {
		// PayFast: "Use the PayFast Secured Key for secured_hash. No separate HASH_KEY is required."
		// The same SECURED_KEY is used for both OAuth token and HMAC signing.
		log.Printf("INFO: [payfast] PAYFAST_HASH_KEY not set — using PAYFAST_SECURED_KEY for HMAC signing (per PayFast spec, no separate hash key needed)")
		hashKey = securedKey
	}

	c := &Client{
		merchantID:     merchantID,
		securedKey:     securedKey,
		hashKey:        hashKey,
		merchantName:   merchantName,
		baseURL:        strings.TrimRight(baseURL, "/"),
		successURL:     os.Getenv("PAYFAST_SUCCESS_URL"),
		failureURL:     os.Getenv("PAYFAST_FAILURE_URL"),
		httpClient:     httpClient,
		timeout:        gatewayTimeout,
		tokens:         NewTokenManager(httpClient, baseURL, merchantID, securedKey, tokenCb),
		circuitBreaker: cb,
	}

	return c
}

// DefaultGatewayTimeout resolves the configured per-call gateway timeout from
// PAYFAST_GATEWAY_TIMEOUT_SECONDS (positive integer seconds), defaulting to 20s.
func DefaultGatewayTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("PAYFAST_GATEWAY_TIMEOUT_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 20 * time.Second
}

// Timeout returns this client's configured per-call HTTP timeout so callers can
// wrap their own contexts with a coherent (slightly larger) deadline.
func (c *Client) Timeout() time.Duration {
	return c.timeout
}

// NewClientFromEnv initializes client using environment variables.
func NewClientFromEnv() *Client {
	return NewClient(
		os.Getenv("PAYFAST_MERCHANT_ID"),
		os.Getenv("PAYFAST_SECURED_KEY"),
		os.Getenv("PAYFAST_HASH_KEY"),
		os.Getenv("PAYFAST_MERCHANT_NAME"),
		os.Getenv("PAYFAST_BASE_URL"),
	)
}

// CircuitBreaker returns the underlying circuit breaker instance.
func (c *Client) CircuitBreaker() *CircuitBreaker {
	return c.circuitBreaker
}

// IsConfigured returns true only when the client can actually reach and sign PayFast
// API calls: merchant credentials AND a gateway base URL must all be present.
// (Merchant ID + Secured Key alone are useless without an endpoint to call.)
func (c *Client) IsConfigured() bool {
	return c.merchantID != "" && c.securedKey != "" && c.baseURL != ""
}

// BaseURL returns the configured gateway base URL so callers can detect the
// gateway variant (e.g. apps.net.pk vs gopayfast.com) and choose the right flow.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// MerchantID returns the configured merchant ID for building hosted checkout URLs.
func (c *Client) MerchantID() string {
	return c.merchantID
}

// VerifyIPNHash verifies the integrity hash PayFast attaches to Instant Payment Notification (IPN)
// callbacks sent to the merchant's registered checkout_url. Per PayFast's documented spec, this is
// a PLAIN SHA256 (not HMAC) of "basket_id|merchant_secured_key|merchant_id|payfast_err_code".
// It verifies against securedKey (official spec: "Merchant Secured Key"), with graceful fallback to hashKey.
func (c *Client) VerifyIPNHash(basketID, payfastErrCode, receivedHash string) bool {
	if receivedHash == "" {
		return false
	}
	expectedSecured := CalculateResponseValidationHash(basketID, c.securedKey, c.merchantID, payfastErrCode)
	if VerifyResponseHash(expectedSecured, receivedHash) {
		return true
	}
	if c.hashKey != "" && c.hashKey != c.securedKey {
		expectedHashKey := CalculateResponseValidationHash(basketID, c.hashKey, c.merchantID, payfastErrCode)
		if VerifyResponseHash(expectedHashKey, receivedHash) {
			return true
		}
	}
	return false
}

// GetAuthToken returns a cached or freshly-fetched auth token via the internal TokenManager.
func (c *Client) GetAuthToken(ctx context.Context, customerIP string) (string, error) {
	if !c.IsConfigured() {
		return "", ErrNotConfigured
	}
	return c.tokens.GetToken(ctx, customerIP)
}
