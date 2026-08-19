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
	merchantID   string
	securedKey   string
	merchantName string
	baseURL      string
	successURL   string
	failureURL   string
	httpClient   *http.Client
	tokens       *TokenManager
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

	c := &Client{
		merchantID:   merchantID,
		securedKey:   securedKey,
		merchantName: merchantName,
		baseURL:      strings.TrimRight(baseURL, "/"),
		successURL:   os.Getenv("PAYFAST_SUCCESS_URL"),
		failureURL:   os.Getenv("PAYFAST_FAILURE_URL"),
		httpClient:   httpClient,
		tokens:       NewTokenManager(httpClient, baseURL, merchantID, securedKey),
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
