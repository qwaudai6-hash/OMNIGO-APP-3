package service

import (
	"context"
	"fmt"
	"os"
)

// PaymentGateway interface that all gateway implementations must satisfy.
type PaymentGateway interface {
	CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (CheckoutResponse, error)
	Refund(ctx context.Context, transactionID string, amount float64) error
	VerifyWebhook(payload []byte, signature string) (WebhookEvent, error)
	IsConfigured() bool
}

type CheckoutRequest struct {
	OrderID       string
	CustomerID    string
	Amount        float64
	Currency      string
	ReturnURL     string
	CancelURL     string
	CustomerPhone string
	CustomerEmail string
}

type CheckoutResponse struct {
	Gateway      string `json:"gateway"`
	SessionID    string `json:"session_id"`
	RedirectURL  string `json:"redirect_url,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
}

type WebhookEvent struct {
	OrderID       string  `json:"order_id"`
	CustomerID    string  `json:"customer_id,omitempty"`
	TransactionID string  `json:"transaction_id"`
	Status        string  `json:"status"` // SUCCESS, FAILED, REFUNDED, PENDING
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency,omitempty"`
	Gateway       string  `json:"gateway,omitempty"`
}

// Orchestrator routes payments to the correct gateway.
type Orchestrator struct {
	gateways map[string]PaymentGateway
}

func NewOrchestrator() *Orchestrator {
	gateways := make(map[string]PaymentGateway)

	if stripeKey := os.Getenv("STRIPE_SECRET_KEY"); stripeKey != "" {
		gateways["stripe"] = NewStripeService(stripeKey, os.Getenv("STRIPE_WEBHOOK_SECRET"))
	}

	jazzSalt := os.Getenv("JAZZCASH_SALT")
	if jazzSalt == "" {
		jazzSalt = os.Getenv("JAZZCASH_INTEGRITY_SALT") // deprecated alias, kept for compatibility
	}
	if jazzSalt != "" {
		gateways["jazzcash"] = NewJazzCashService(
			os.Getenv("JAZZCASH_MERCHANT_ID"),
			os.Getenv("JAZZCASH_PASSWORD"),
			jazzSalt,
			os.Getenv("JAZZCASH_API_URL"),
		)
	}
	if epKey := os.Getenv("EASYPAISA_HASH_KEY"); epKey != "" {
		gateways["easypaisa"] = NewEasyPaisaService(
			os.Getenv("EASYPAISA_STORE_ID"),
			epKey,
			os.Getenv("EASYPAISA_API_URL"),
		)
	}

	return &Orchestrator{
		gateways: gateways,
	}
}

// NewOrchestratorFromEnv is a convenience constructor for handlers.
func NewOrchestratorFromEnv() *Orchestrator {
	return NewOrchestrator()
}

func (o *Orchestrator) CreateCheckout(ctx context.Context, gatewayName string, req CheckoutRequest) (CheckoutResponse, error) {
	gateway, ok := o.gateways[gatewayName]
	if !ok {
		return CheckoutResponse{}, fmt.Errorf("unsupported or unconfigured payment gateway: %s", gatewayName)
	}
	if !gateway.IsConfigured() {
		return CheckoutResponse{}, fmt.Errorf("gateway %s is not configured", gatewayName)
	}
	return gateway.CreateCheckoutSession(ctx, req)
}

func (o *Orchestrator) ProcessWebhook(gatewayName string, payload []byte, signature string) (WebhookEvent, error) {
	gateway, ok := o.gateways[gatewayName]
	if !ok {
		return WebhookEvent{}, fmt.Errorf("unsupported or unconfigured payment gateway: %s", gatewayName)
	}
	if !gateway.IsConfigured() {
		return WebhookEvent{}, fmt.Errorf("gateway %s is not configured", gatewayName)
	}
	return gateway.VerifyWebhook(payload, signature)
}

func (o *Orchestrator) Refund(ctx context.Context, gatewayName string, transactionID string, amount float64) error {
	gateway, ok := o.gateways[gatewayName]
	if !ok {
		return fmt.Errorf("unsupported or unconfigured payment gateway: %s", gatewayName)
	}
	if !gateway.IsConfigured() {
		return fmt.Errorf("gateway %s is not configured", gatewayName)
	}
	return gateway.Refund(ctx, transactionID, amount)
}

// AvailableGateways returns a list of configured gateway names for the frontend.
func (o *Orchestrator) AvailableGateways() []string {
	out := make([]string, 0, len(o.gateways))
	for name, gw := range o.gateways {
		if gw.IsConfigured() {
			out = append(out, name)
		}
	}
	return out
}
