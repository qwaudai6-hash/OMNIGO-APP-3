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
	client         *http.Client
	baseURL        string
	merchantID     string
	securedKey     string
	circuitBreaker *CircuitBreaker
	mu             sync.RWMutex
	cache          TokenCache
}

// TokenContext holds optional basket context for GetAccessToken.
// Per PayFast PHP sample, GetAccessToken requires BASKET_ID, TXNAMT, CURRENCY_CODE.
type TokenContext struct {
	BasketID     string
	TxnAmt       string
	CurrencyCode string
	ApplyDiscount string
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
// Pass TokenContext with basket details for PayFast APPS UAT endpoints.
func (tm *TokenManager) GetToken(ctx context.Context, customerIP string, tokenCtx ...*TokenContext) (string, error) {
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

	var tc *TokenContext
	if len(tokenCtx) > 0 {
		tc = tokenCtx[0]
	}

	var token, expiresInStr string
	var err error
	if tm.circuitBreaker != nil {
		err = tm.circuitBreaker.Execute(func() error {
			t, exp, fErr := tm.fetchToken(ctx, tc)
			if fErr != nil {
				return fErr
			}
			token = t
			expiresInStr = exp
			return nil
		})
	} else {
		token, expiresInStr, err = tm.fetchToken(ctx, tc)
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

func (tm *TokenManager) fetchToken(ctx context.Context, tc *TokenContext) (string, string, error) {
	if tm.merchantID == "" || tm.securedKey == "" {
		return "", "", ErrNotConfigured
	}

	// Official API endpoint: /token or /Transaction/GetAccessToken (APPS net.pk IPG)
	authURL := tm.baseURL + "/token"
	if strings.Contains(tm.baseURL, "apps.net.pk") {
		if strings.HasSuffix(tm.baseURL, "/GetAccessToken") {
			authURL = tm.baseURL
		} else if strings.HasSuffix(tm.baseURL, "/Transaction") {
			authURL = tm.baseURL + "/GetAccessToken"
		} else {
			authURL = tm.baseURL + "/Transaction/GetAccessToken"
		}
	}

	// Per PayFast PHP sample: GetAccessToken requires MERCHANT_ID, SECURED_KEY,
	// BASKET_ID, TXNAMT, CURRENCY_CODE, APPLY_DISCOUNT
	formData := url.Values{}
	formData.Set("MERCHANT_ID", tm.merchantID)
	formData.Set("SECURED_KEY", tm.securedKey)
	if tc != nil {
		if tc.BasketID != "" {
			formData.Set("BASKET_ID", tc.BasketID)
		}
		if tc.TxnAmt != "" {
			formData.Set("TXNAMT", tc.TxnAmt)
		}
		if tc.CurrencyCode != "" {
			formData.Set("CURRENCY_CODE", tc.CurrencyCode)
		} else {
			formData.Set("CURRENCY_CODE", "PKR")
		}
		if tc.ApplyDiscount != "" {
			formData.Set("APPLY_DISCOUNT", tc.ApplyDiscount)
		} else {
			formData.Set("APPLY_DISCOUNT", "true")
		}
	} else {
		formData.Set("BASKET_ID", "PF"+strconv.FormatInt(time.Now().UnixNano(), 10))
		formData.Set("TXNAMT", "1")
		formData.Set("CURRENCY_CODE", "PKR")
		formData.Set("APPLY_DISCOUNT", "true")
	}

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
	if err := json.Unmarshal(body, &res); err == nil && res.GetToken() != "" {
		if res.Code != "" && !IsSuccessCode(res.Code) {
			return "", "", &GatewayError{
				StatusCode: resp.StatusCode,
				Message:    "Authentication rejected by gateway",
				StatusMsg:  fmt.Sprintf("code=%s msg=%s", res.Code, res.Message),
			}
		}
		return res.GetToken(), res.ExpiresIn, nil
	}

	return "", "", ErrAuthFailed
}
