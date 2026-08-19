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
	mu         sync.RWMutex
	cache      TokenCache
}

// NewTokenManager initializes a new TokenManager securely.
func NewTokenManager(client *http.Client, baseURL, merchantID, securedKey string) *TokenManager {
	return &TokenManager{
		client:     client,
		baseURL:    strings.TrimRight(baseURL, "/"),
		merchantID: merchantID,
		securedKey: securedKey,
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

	token, expiresInStr, err := tm.fetchToken(ctx)
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
			Internal:   fmt.Errorf("status code %d", resp.StatusCode),
		}
	}

	var res AuthTokenResponse
	if err := json.Unmarshal(body, &res); err == nil && res.Token != "" {
		return res.Token, res.ExpiresIn, nil
	}

	return "", "", ErrAuthFailed
}
