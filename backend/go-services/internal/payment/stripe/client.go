package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	stripeSDK "github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/paymentintent"
	"github.com/stripe/stripe-go/v76/refund"
	"github.com/stripe/stripe-go/v76/webhook"
)

var (
	ErrNotConfigured    = errors.New("stripe: API key not configured")
	ErrWebhookSecret    = errors.New("stripe: webhook signing secret not configured")
	ErrCircuitOpen      = errors.New("stripe: circuit breaker is open — gateway temporarily unreachable")
	ErrAmountMismatch   = errors.New("stripe: payment amount does not match local order amount")
	ErrOrderAlreadyPaid = errors.New("stripe: order is already paid (idempotent rejection)")
)

// Client wraps the Stripe SDK with circuit breaking, retry, and idempotency support.
type Client struct {
	apiKey        string
	webhookSecret string
	circuit       *circuitBreaker
	mu            sync.RWMutex
}

// NewClient constructs a Stripe client from explicit config.
func NewClient(apiKey, webhookSecret string) *Client {
	if apiKey != "" {
		stripeSDK.Key = apiKey
	}
	return &Client{
		apiKey:        apiKey,
		webhookSecret: webhookSecret,
		circuit:       newCircuitBreaker(5, 30*time.Second),
	}
}

// NewClientFromEnv initializes client from environment variables.
func NewClientFromEnv() *Client {
	return NewClient(
		os.Getenv("STRIPE_SECRET_KEY"),
		os.Getenv("STRIPE_WEBHOOK_SECRET"),
	)
}

// IsConfigured returns true when the client has valid credentials.
func (c *Client) IsConfigured() bool {
	return c.apiKey != ""
}

// WebhookSecret returns the configured webhook signing secret.
func (c *Client) WebhookSecret() string {
	return c.webhookSecret
}

// CreatePaymentIntent creates a new PaymentIntent with idempotency key.
// The frontend uses the returned ClientSecret with Stripe.js PaymentSheet.
func (c *Client) CreatePaymentIntent(ctx context.Context, amountCents int64, currency string, metadata map[string]string, idempotencyKey string) (*stripeSDK.PaymentIntent, error) {
	if !c.IsConfigured() {
		return nil, ErrNotConfigured
	}
	if err := c.circuit.Execute(); err != nil {
		return nil, err
	}

	params := &stripeSDK.PaymentIntentParams{
		Amount:   stripeSDK.Int64(amountCents),
		Currency: stripeSDK.String(currency),
		PaymentMethodTypes: []*string{
			stripeSDK.String("card"),
		},
		ConfirmationMethod: stripeSDK.String("manual"),
		CaptureMethod:      stripeSDK.String("manual"),
		Metadata:           metadata,
	}
	params.Context = ctx

	var pi *stripeSDK.PaymentIntent
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
		params.Context = ctx
		if idempotencyKey != "" {
			params.SetIdempotencyKey(idempotencyKey)
		}
		pi, lastErr = paymentintent.New(params)
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		c.circuit.RecordFailure()
		return nil, fmt.Errorf("stripe paymentintent create failed: %w", lastErr)
	}
	c.circuit.RecordSuccess()
	return pi, nil
}

// CapturePaymentIntent captures an authorized PaymentIntent (full or partial).
func (c *Client) CapturePaymentIntent(ctx context.Context, paymentIntentID string, amountCents int64) (*stripeSDK.PaymentIntent, error) {
	if !c.IsConfigured() {
		return nil, ErrNotConfigured
	}
	if err := c.circuit.Execute(); err != nil {
		return nil, err
	}

	params := &stripeSDK.PaymentIntentCaptureParams{}
	if amountCents > 0 {
		params.AmountToCapture = stripeSDK.Int64(amountCents)
	}
	params.Context = ctx

	pi, err := paymentintent.Capture(paymentIntentID, params)
	if err != nil {
		c.circuit.RecordFailure()
		return nil, fmt.Errorf("stripe capture failed: %w", err)
	}
	c.circuit.RecordSuccess()
	return pi, nil
}

// RefundPaymentIntent issues a full or partial refund.
func (c *Client) RefundPaymentIntent(ctx context.Context, paymentIntentID string, amountCents int64, idempotencyKey string) (*stripeSDK.Refund, error) {
	if !c.IsConfigured() {
		return nil, ErrNotConfigured
	}
	if err := c.circuit.Execute(); err != nil {
		return nil, err
	}

	params := &stripeSDK.RefundParams{
		PaymentIntent: stripeSDK.String(paymentIntentID),
	}
	if amountCents > 0 {
		params.Amount = stripeSDK.Int64(amountCents)
	}
	params.Context = ctx

	var r *stripeSDK.Refund
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
		params.Context = ctx
		if idempotencyKey != "" {
			params.SetIdempotencyKey(idempotencyKey)
		}
		r, lastErr = refund.New(params)
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		c.circuit.RecordFailure()
		return nil, fmt.Errorf("stripe refund failed: %w", lastErr)
	}
	c.circuit.RecordSuccess()
	return r, nil
}

// VerifyWebhook validates Stripe webhook signature and returns the parsed event.
func (c *Client) VerifyWebhook(payload []byte, signatureHeader string) (stripeSDK.Event, error) {
	if !c.IsConfigured() {
		return stripeSDK.Event{}, ErrNotConfigured
	}
	if c.webhookSecret == "" {
		return stripeSDK.Event{}, ErrWebhookSecret
	}
	event, err := webhook.ConstructEvent(payload, signatureHeader, c.webhookSecret)
	if err != nil {
		return stripeSDK.Event{}, fmt.Errorf("stripe webhook signature verification failed: %w", err)
	}
	return event, nil
}

// ParsePaymentIntent extracts the PaymentIntent from a webhook event.
func ParsePaymentIntent(event stripeSDK.Event) (*stripeSDK.PaymentIntent, error) {
	var pi stripeSDK.PaymentIntent
	if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
		return nil, fmt.Errorf("failed to unmarshal payment_intent: %w", err)
	}
	return &pi, nil
}

// ParseCharge extracts a Charge from a webhook event.
func ParseCharge(event stripeSDK.Event) (*stripeSDK.Charge, error) {
	var charge stripeSDK.Charge
	if err := json.Unmarshal(event.Data.Raw, &charge); err != nil {
		return nil, fmt.Errorf("failed to unmarshal charge: %w", err)
	}
	return &charge, nil
}

// MapStripeErrorClassifies Stripe API errors into actionable categories.
func MapStripeError(err error) (code string, message string) {
	if err == nil {
		return "", ""
	}
	var stripeErr *stripeSDK.Error
	if !errors.As(err, &stripeErr) {
		return "unknown", err.Error()
	}
	switch stripeErr.Type {
	case "card_error":
		return "card_error", mapDeclineCode(string(stripeErr.DeclineCode))
	case "rate_limit_error":
		return "rate_limit", "Too many requests. Please retry shortly."
	case "invalid_request_error":
		return "invalid_request", "Payment configuration error."
	case "authentication_error":
		return "auth_error", "Payment service authentication failed."
	case "api_error":
		return "api_error", "Payment gateway temporarily unavailable."
	default:
		return "unknown", "An unexpected payment error occurred."
	}
}

func mapDeclineCode(code string) string {
	switch strings.TrimSpace(code) {
	case "insufficient_funds":
		return "Insufficient funds. Please try a different payment method."
	case "expired_card":
		return "Your card has expired. Please use a different card."
	case "incorrect_cvc":
		return "Incorrect CVC. Please check and retry."
	case "card_declined":
		return "Card was declined. Please try a different card."
	case "processing_error":
		return "Processing error. Please retry."
	case "incorrect_number":
		return "Invalid card number. Please check and retry."
	default:
		return "Payment was declined. Please try a different payment method."
	}
}

// --- Circuit Breaker (Stripe-specific, mirrors payfast pattern) ---

type cbState int

const (
	cbClosed cbState = iota
	cbOpen
	cbHalfOpen
)

type circuitBreaker struct {
	mu                  sync.Mutex
	state               cbState
	consecutiveFailures int
	failureThreshold    int
	cooldownDuration    time.Duration
	lastStateChange     time.Time
}

func newCircuitBreaker(threshold int, cooldown time.Duration) *circuitBreaker {
	return &circuitBreaker{
		state:            cbClosed,
		failureThreshold: threshold,
		cooldownDuration: cooldown,
		lastStateChange:  time.Now(),
	}
}

func (cb *circuitBreaker) Execute() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	if cb.state == cbOpen && now.Sub(cb.lastStateChange) >= cb.cooldownDuration {
		cb.state = cbHalfOpen
		cb.lastStateChange = now
		log.Printf("[stripe-circuit] OPEN → HALF_OPEN (cooldown elapsed)")
	}
	if cb.state == cbOpen {
		return ErrCircuitOpen
	}
	return nil
}

func (cb *circuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFailures = 0
	if cb.state == cbHalfOpen {
		log.Printf("[stripe-circuit] HALF_OPEN → CLOSED")
		cb.state = cbClosed
		cb.lastStateChange = time.Now()
	}
}

func (cb *circuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFailures++
	if cb.consecutiveFailures >= cb.failureThreshold && cb.state != cbOpen {
		log.Printf("[stripe-circuit] → OPEN (failures: %d)", cb.consecutiveFailures)
		cb.state = cbOpen
		cb.lastStateChange = time.Now()
	}
}
