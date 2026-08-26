package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/paymentintent"
	"github.com/stripe/stripe-go/v76/refund"
	"github.com/stripe/stripe-go/v76/webhook"
)

// StripeService implements production-grade Stripe checkout, capture, refund,
// and webhook verification. Card data never touches our servers — Stripe SDK
// tokenization keeps PCI scope minimal.
type StripeService struct {
	apiKey        string
	webhookSecret string
}

func NewStripeService(apiKey, webhookSecret string) *StripeService {
	if apiKey != "" {
		stripe.Key = apiKey
	}
	return &StripeService{
		apiKey:        apiKey,
		webhookSecret: webhookSecret,
	}
}

func (s *StripeService) IsConfigured() bool {
	return s.apiKey != ""
}

// CreateCheckoutSession creates a PaymentIntent. The frontend will use the
// returned client_secret with Stripe SDK (PaymentSheet) to tokenize the card
// and authenticate 3-D Secure when required.
func (s *StripeService) CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (CheckoutResponse, error) {
	if !s.IsConfigured() {
		return CheckoutResponse{}, errors.New("stripe is not configured")
	}

	amountCents := int64(req.Amount * 100)

	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(amountCents),
		Currency: stripe.String(string(stripe.Currency(req.Currency))),
		PaymentMethodTypes: []*string{
			stripe.String("card"),
		},
		ConfirmationMethod: stripe.String("manual"),
		CaptureMethod:      stripe.String("manual"), // Authorize now, capture on dispatch
		Metadata: map[string]string{
			"order_id":    req.OrderID,
			"customer_id": req.CustomerID,
			// STOR- tracking ID. Required by the split webhook to attribute the
			// vendor escrow hold to the correct store; empty means the split
			// falls back to the default commission rate and skips the hold.
			"store_id": req.StoreID,
			"gateway":  "stripe",
		},
	}

	pi, err := paymentintent.New(params)
	if err != nil {
		return CheckoutResponse{}, fmt.Errorf("stripe paymentintent failed: %w", err)
	}

	return CheckoutResponse{
		Gateway:      "stripe",
		SessionID:    pi.ID,
		ClientSecret: pi.ClientSecret,
		RedirectURL:  "", // SDK flow, no redirect
	}, nil
}

// Capture captures an authorized PaymentIntent. Call this when the order is
// accepted / dispatched.
func (s *StripeService) Capture(ctx context.Context, paymentIntentID string, amount float64) error {
	if !s.IsConfigured() {
		return errors.New("stripe is not configured")
	}
	params := &stripe.PaymentIntentCaptureParams{
		AmountToCapture: stripe.Int64(int64(amount * 100)),
	}
	_, err := paymentintent.Capture(paymentIntentID, params)
	return err
}

// Refund issues a full or partial refund against a PaymentIntent.
func (s *StripeService) Refund(ctx context.Context, paymentIntentID string, amount float64) error {
	if !s.IsConfigured() {
		return errors.New("stripe is not configured")
	}
	params := &stripe.RefundParams{
		PaymentIntent: stripe.String(paymentIntentID),
	}
	if amount > 0 {
		params.Amount = stripe.Int64(int64(amount * 100))
	}
	_, err := refund.New(params)
	return err
}

// VerifyWebhook cryptographically verifies Stripe webhook signatures and
// returns a normalized event. Never process unsigned Stripe webhooks in prod.
func (s *StripeService) VerifyWebhook(payload []byte, signature string) (WebhookEvent, error) {
	if !s.IsConfigured() {
		return WebhookEvent{}, errors.New("stripe is not configured")
	}
	if s.webhookSecret == "" {
		return WebhookEvent{}, errors.New("stripe webhook secret is not configured")
	}

	event, err := webhook.ConstructEvent(payload, signature, s.webhookSecret)
	if err != nil {
		return WebhookEvent{}, fmt.Errorf("stripe signature verification failed: %w", err)
	}

	switch event.Type {
	case "payment_intent.amount_capturable_updated",
		"payment_intent.created":
		// No-op; just acknowledge.
		return WebhookEvent{}, nil

	case "payment_intent.succeeded":
		var pi stripe.PaymentIntent
		if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
			return WebhookEvent{}, err
		}
		return WebhookEvent{
			OrderID:       pi.Metadata["order_id"],
			CustomerID:    pi.Metadata["customer_id"],
			TransactionID: pi.ID,
			Status:        "SUCCESS",
			Amount:        float64(pi.Amount) / 100.0,
			Currency:      string(pi.Currency),
			Gateway:       "stripe",
		}, nil

	case "payment_intent.requires_action":
		// 3-D Secure is pending — the payment is NOT confirmed yet. It must
		// never be treated as SUCCESS or escrow would be credited for money
		// the customer has not actually paid.
		var pi stripe.PaymentIntent
		if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
			return WebhookEvent{}, err
		}
		return WebhookEvent{
			OrderID:       pi.Metadata["order_id"],
			CustomerID:    pi.Metadata["customer_id"],
			TransactionID: pi.ID,
			Status:        "PENDING",
			Amount:        float64(pi.Amount) / 100.0,
			Currency:      string(pi.Currency),
			Gateway:       "stripe",
		}, nil

	case "payment_intent.payment_failed":
		var pi stripe.PaymentIntent
		if err := json.Unmarshal(event.Data.Raw, &pi); err != nil {
			return WebhookEvent{}, err
		}
		return WebhookEvent{
			OrderID:       pi.Metadata["order_id"],
			CustomerID:    pi.Metadata["customer_id"],
			TransactionID: pi.ID,
			Status:        "FAILED",
			Amount:        float64(pi.Amount) / 100.0,
			Currency:      string(pi.Currency),
			Gateway:       "stripe",
		}, nil

	case "charge.refunded":
		var charge stripe.Charge
		if err := json.Unmarshal(event.Data.Raw, &charge); err != nil {
			return WebhookEvent{}, err
		}
		refundedAmount := float64(0)
		if charge.Refunds != nil {
			for _, r := range charge.Refunds.Data {
				refundedAmount += float64(r.Amount) / 100.0
			}
		}
		return WebhookEvent{
			OrderID:       charge.Metadata["order_id"],
			CustomerID:    charge.Metadata["customer_id"],
			TransactionID: charge.PaymentIntent.ID,
			Status:        "REFUNDED",
			Amount:        refundedAmount,
			Currency:      string(charge.Currency),
			Gateway:       "stripe",
		}, nil
	}

	return WebhookEvent{}, fmt.Errorf("unhandled stripe event type: %s", event.Type)
}

func parseStripeAmount(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v / 100.0
}

func envStripeWebhookSecret() string {
	return os.Getenv("STRIPE_WEBHOOK_SECRET")
}
