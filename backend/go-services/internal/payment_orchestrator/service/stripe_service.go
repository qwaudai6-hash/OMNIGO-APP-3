package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	stripeSDK "github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/paymentintent"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	stripeClient "github.com/omnigo/backend/internal/payment/stripe"
	"github.com/omnigo/backend/internal/payment_orchestrator"
	"github.com/omnigo/backend/internal/payment_orchestrator/fraud"
	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/ledger"
)

// StripeService orchestrates the full Stripe payment lifecycle:
// checkout → PaymentIntent creation → webhook settlement → ledger split.
//
// Production webhook pattern (insert-first, process-second):
//  1. Verify signature
//  2. Insert event into stripe_events (UNIQUE constraint = dedup)
//  3. Refetch canonical state from Stripe API (out-of-order safety)
//  4. Process side effects (mark order paid, ledger split)
//  5. Mark event as processed
//  6. Return 200
//
// Source: theroadtoenterprise.com + monstar-lab.com + stripe.dev production patterns.
type StripeService struct {
	db              *pgxpool.Pool
	ledger          *ledger.Service
	escrow          *escrow.Service
	calculator      *payment_orchestrator.CommissionCalculator
	stripe          *stripeClient.Client
	fraud           *fraud.Detector
	callbackBaseURL string
}

// NewStripeService constructs the service.
func NewStripeService(
	db *pgxpool.Pool,
	ledgerSvc *ledger.Service,
	escrowSvc *escrow.Service,
	calc *payment_orchestrator.CommissionCalculator,
	stripeCl *stripeClient.Client,
) *StripeService {
	publicBase := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")
	if publicBase == "" {
		publicBase = "https://omnigo-app-3-production.up.railway.app"
	}
	return &StripeService{
		db:              db,
		ledger:          ledgerSvc,
		escrow:          escrowSvc,
		calculator:      calc,
		stripe:          stripeCl,
		callbackBaseURL: publicBase,
	}
}

// SetFraudDetector optionally attaches a fraud detector.
func (s *StripeService) SetFraudDetector(fd *fraud.Detector) {
	s.fraud = fd
}

// ─── Checkout ────────────────────────────────────────────────────────────────

// StripeCheckoutRequest is the inbound checkout payload.
type StripeCheckoutRequest struct {
	OrderID    string  `json:"order_id"`
	CustomerID string  `json:"customer_id"`
	StoreID    string  `json:"store_id"`
	Amount     float64 `json:"amount"`
	Currency   string  `json:"currency"`
}

// StripeCheckoutResponse is returned to the frontend.
type StripeCheckoutResponse struct {
	Status       string `json:"status"` // "requires_action" | "processing" | "failed"
	ClientSecret string `json:"client_secret,omitempty"`
	PaymentIntID string `json:"payment_intent_id,omitempty"`
	OrderID      string `json:"order_id"`
	Message      string `json:"message,omitempty"`
}

// ProcessCheckout creates a PaymentIntent and records the pending transaction.
// The frontend uses the returned client_secret to confirm via PaymentSheet.
func (s *StripeService) ProcessCheckout(ctx context.Context, merchantUserID, clientIP string, req *StripeCheckoutRequest) (*StripeCheckoutResponse, error) {
	if !s.stripe.IsConfigured() {
		return nil, stripeClient.ErrNotConfigured
	}

	// 1. Validate order exists and belongs to this customer
	var dbAmount float64
	var storeID, orderStatus string
	err := s.db.QueryRow(ctx,
		`SELECT total_amount, store_tracking_id, status FROM orders WHERE order_tracking_id = $1 AND user_tracking_id = $2`,
		req.OrderID, req.CustomerID,
	).Scan(&dbAmount, &storeID, &orderStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("order not found or does not belong to customer")
		}
		return nil, fmt.Errorf("order lookup failed: %w", err)
	}
	if orderStatus == "paid" || orderStatus == "cancelled" || orderStatus == "refunded" {
		return nil, stripeClient.ErrOrderAlreadyPaid
	}

	// 2. Amount validation (server-authoritative)
	const amountEpsilon = 0.01
	if diff := req.Amount - dbAmount; diff > amountEpsilon || diff < -amountEpsilon {
		return nil, stripeClient.ErrAmountMismatch
	}
	req.Amount = dbAmount
	if req.StoreID == "" {
		req.StoreID = storeID
	}
	if req.Currency == "" {
		req.Currency = "PKR"
	}

	// 3. Fraud check (non-blocking, best-effort)
	if s.fraud != nil {
		s.fraud.RecordAttempt(ctx, merchantUserID, clientIP, false)
	}

	// 4. Generate idempotency key
	idempotencyKey := stripeClient.GenerateIdempotencyKey("checkout", req.OrderID)

	// 5. Create PaymentIntent via Stripe
	amountCents := int64(req.Amount * 100)
	metadata := map[string]string{
		"order_id":    req.OrderID,
		"customer_id": req.CustomerID,
		"store_id":    req.StoreID,
		"gateway":     "stripe",
	}

	pi, err := s.stripe.CreatePaymentIntent(ctx, amountCents, req.Currency, metadata, idempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create PaymentIntent: %w", err)
	}

	// 6. Record in payment_transactions
	internalTxnID := fmt.Sprintf("stripe_%s_%d", req.OrderID, time.Now().UnixMilli())
	_, err = s.db.Exec(ctx,
		`INSERT INTO payment_transactions
		 (transaction_id, order_id, gateway, gateway_txn_id, amount, currency, status, customer_tracking_id, ip_address, metadata, created_at, updated_at)
		 VALUES ($1, $2, 'stripe', $3, $4, $5, 'pending', $6, $7, $8, NOW(), NOW())
		 ON CONFLICT (transaction_id) DO NOTHING`,
		internalTxnID, req.OrderID, pi.ID, req.Amount, req.Currency,
		merchantUserID, clientIP, `{"payment_intent_id":"`+pi.ID+`"}`,
	)
	if err != nil {
		log.Printf("[StripeService] WARNING: failed to record payment transaction for order %s: %v", req.OrderID, err)
	}

	// 7. Update order payment status
	_, err = s.db.Exec(ctx,
		`UPDATE orders SET payment_status = 'pending', updated_at = NOW() WHERE order_tracking_id = $1`,
		req.OrderID,
	)
	if err != nil {
		log.Printf("[StripeService] WARNING: failed to update order payment_status for %s: %v", req.OrderID, err)
	}

	s.logEvent("checkout", req.OrderID, pi.ID, fmt.Sprintf("PaymentIntent created: amount=%.2f %s", req.Amount, req.Currency))

	return &StripeCheckoutResponse{
		Status:       "requires_action",
		ClientSecret: pi.ClientSecret,
		PaymentIntID: pi.ID,
		OrderID:      req.OrderID,
		Message:      "PaymentIntent created — client must confirm via PaymentSheet",
	}, nil
}

// ─── Webhook (Production Pattern: Insert-First, Process-Second) ──────────────

// HandleWebhook processes a Stripe webhook event with the production pattern:
//  1. Verify signature
//  2. Insert event into stripe_events (dedup via UNIQUE constraint)
//  3. Refetch canonical state from Stripe API
//  4. Process side effects
//  5. Mark event as processed
//  6. Return 200
//
// This ensures replay-safety, out-of-order safety, and audit trail.
func (s *StripeService) HandleWebhook(ctx context.Context, payload []byte, signatureHeader string) error {
	// 1. Verify signature
	event, err := s.stripe.VerifyWebhook(payload, signatureHeader)
	if err != nil {
		return err
	}

	// 2. Extract metadata before inserting (for fast lookups)
	var orderID, paymentIntentID string
	switch event.Type {
	case "payment_intent.succeeded", "payment_intent.payment_failed":
		pi, parseErr := stripeClient.ParsePaymentIntent(event)
		if parseErr == nil {
			orderID = pi.Metadata["order_id"]
			paymentIntentID = pi.ID
		}
	case "charge.refunded":
		charge, parseErr := stripeClient.ParseCharge(event)
		if parseErr == nil {
			orderID = charge.Metadata["order_id"]
			if charge.PaymentIntent != nil {
				paymentIntentID = charge.PaymentIntent.ID
			}
		}
	}

	// 3. Insert event into stripe_events (UNIQUE constraint = dedup)
	//    ON CONFLICT DO NOTHING — duplicate event is silently skipped
	var eventRowID string
	err = s.db.QueryRow(ctx,
		`INSERT INTO stripe_events (stripe_event_id, event_type, payload, order_id, payment_intent_id)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (stripe_event_id) DO NOTHING
		 RETURNING id`,
		event.ID, event.Type, string(payload), orderID, paymentIntentID,
	).Scan(&eventRowID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Duplicate event — already processed or being processed
			s.logEvent("dedup", orderID, paymentIntentID, fmt.Sprintf("Duplicate event %s skipped", event.ID))
			return nil
		}
		return fmt.Errorf("failed to insert stripe event: %w", err)
	}

	// 4. Process the event (side effects)
	processErr := s.processWebhookEvent(ctx, event, eventRowID, orderID, paymentIntentID)

	// 5. Mark event as processed (or record error)
	if processErr != nil {
		_, _ = s.db.Exec(ctx,
			`UPDATE stripe_events SET process_error = $1, processed_at = NOW() WHERE id = $2`,
			processErr.Error(), eventRowID,
		)
		s.logEvent("error", orderID, paymentIntentID, fmt.Sprintf("Event %s processing failed: %v", event.ID, processErr))
		return processErr
	}

	_, _ = s.db.Exec(ctx,
		`UPDATE stripe_events SET processed_at = NOW() WHERE id = $1`,
		eventRowID,
	)

	s.logEvent("processed", orderID, paymentIntentID, fmt.Sprintf("Event %s processed successfully", event.ID))
	return nil
}

// processWebhookEvent dispatches to the appropriate handler based on event type.
// It REFETCHES canonical state from Stripe API (not trusting the event payload).
func (s *StripeService) processWebhookEvent(ctx context.Context, event stripeSDK.Event, eventRowID, orderID, paymentIntentID string) error {
	switch event.Type {
	case "payment_intent.succeeded":
		return s.handlePaymentSucceeded(ctx, event, eventRowID, orderID, paymentIntentID)
	case "payment_intent.payment_failed":
		return s.handlePaymentFailed(ctx, event, orderID, paymentIntentID)
	case "charge.refunded":
		return s.handleChargeRefunded(ctx, event, orderID, paymentIntentID)
	default:
		return nil // Acknowledge other events without processing
	}
}

// handlePaymentSucceeded uses the REFETCH pattern:
// Never trust event.data.object — always call paymentintent.Get() for canonical state.
// This prevents out-of-order events from writing stale state.
func (s *StripeService) handlePaymentSucceeded(ctx context.Context, event stripeSDK.Event, eventRowID, orderID, paymentIntentID string) error {
	// REFETCH: Get canonical state from Stripe (not from event payload)
	pi, err := paymentintent.Get(paymentIntentID, nil)
	if err != nil {
		return fmt.Errorf("failed to refetch PaymentIntent %s: %w", paymentIntentID, err)
	}

	// Use refetched state, not event payload
	canonicalAmount := float64(pi.Amount) / 100.0
	if orderID == "" {
		orderID = pi.Metadata["order_id"]
	}

	if orderID == "" {
		log.Printf("[StripeWebhook] CRITICAL: payment_intent.succeeded WITHOUT order_id — pi=%s event=%s", pi.ID, event.ID)
		return nil // Acknowledge — can't route without order_id
	}

	// Mark order as paid (idempotent: only if not already paid)
	result, err := s.db.Exec(ctx,
		`UPDATE orders SET status = 'paid', payment_status = 'paid', updated_at = NOW()
		 WHERE order_tracking_id = $1 AND status != 'paid'`,
		orderID,
	)
	if err != nil {
		return fmt.Errorf("failed to mark order %s as paid: %w", orderID, err)
	}
	if result.RowsAffected() == 0 {
		// Already paid — idempotent rejection
		s.logEvent("idempotent", orderID, pi.ID, "Order already paid, skipping split")
		return nil
	}

	// Update payment_transactions
	_, _ = s.db.Exec(ctx,
		`UPDATE payment_transactions SET status = 'completed', updated_at = NOW() WHERE gateway_txn_id = $1`,
		pi.ID,
	)

	// Execute ledger split
	storeID := pi.Metadata["store_id"]
	if err := s.ExecuteSplit(ctx, orderID, storeID, canonicalAmount, pi.ID); err != nil {
		log.Printf("[StripeWebhook] ERROR: split execution failed for order %s: %v", orderID, err)
		return err
	}

	log.Printf("[StripeWebhook] payment_intent.succeeded: order=%s pi=%s amount=%.2f (refetched)", orderID, pi.ID, canonicalAmount)
	return nil
}

func (s *StripeService) handlePaymentFailed(ctx context.Context, event stripeSDK.Event, orderID, paymentIntentID string) error {
	// REFETCH canonical state
	pi, err := paymentintent.Get(paymentIntentID, nil)
	if err != nil {
		return fmt.Errorf("failed to refetch PaymentIntent %s: %w", paymentIntentID, err)
	}

	if orderID == "" {
		orderID = pi.Metadata["order_id"]
	}
	if orderID == "" {
		return nil
	}

	// Update payment_transactions
	_, _ = s.db.Exec(ctx,
		`UPDATE payment_transactions SET status = 'failed', updated_at = NOW() WHERE gateway_txn_id = $1`,
		pi.ID,
	)

	// CRITICAL: Cancel the order AND delivery gig on payment failure.
	// Without this, unpaid orders remain in 'pending' state forever
	// and delivery gigs stay active (zombie gig problem).
	var orderStatus string
	err = s.db.QueryRow(ctx,
		`SELECT status FROM orders WHERE order_tracking_id = $1`, orderID,
	).Scan(&orderStatus)
	if err == nil && orderStatus != "cancelled" && orderStatus != "refunded" {
		// Cancel the order
		_, _ = s.db.Exec(ctx,
			`UPDATE orders SET status = 'cancelled', payment_status = 'failed', updated_at = NOW()
			 WHERE order_tracking_id = $1 AND status NOT IN ('cancelled', 'refunded', 'completed')`,
			orderID,
		)

		// Cancel any active delivery gig for this order
		_, _ = s.db.Exec(ctx,
			`UPDATE deliveries SET status = 'cancelled', updated_at = NOW()
			 WHERE order_tracking_id = $1 AND status NOT IN ('completed', 'cancelled')`,
			orderID,
		)

		log.Printf("[StripeWebhook] CRITICAL: Order %s cancelled due to payment failure. Delivery gig also cancelled.", orderID)
	} else {
		// Still update payment_status even if order is already in terminal state
		_, _ = s.db.Exec(ctx,
			`UPDATE orders SET payment_status = 'failed', updated_at = NOW() WHERE order_tracking_id = $1`,
			orderID,
		)
	}

	declineCode := ""
	if pi.LastPaymentError != nil {
		declineCode = string(pi.LastPaymentError.Code)
	}
	log.Printf("[StripeWebhook] payment_intent.payment_failed: order=%s pi=%s decline=%s", orderID, pi.ID, declineCode)
	return nil
}

func (s *StripeService) handleChargeRefunded(ctx context.Context, event stripeSDK.Event, orderID, paymentIntentID string) error {
	// REFETCH canonical state
	pi, err := paymentintent.Get(paymentIntentID, nil)
	if err != nil {
		return fmt.Errorf("failed to refetch PaymentIntent %s: %w", paymentIntentID, err)
	}

	if orderID == "" {
		orderID = pi.Metadata["order_id"]
	}
	if orderID == "" {
		return nil
	}

	// Update order status (idempotent: only if not already refunded)
	_, _ = s.db.Exec(ctx,
		`UPDATE orders SET status = 'refunded', payment_status = 'refunded', updated_at = NOW()
		 WHERE order_tracking_id = $1 AND status != 'refunded'`,
		orderID,
	)

	// Update payment_transactions
	_, _ = s.db.Exec(ctx,
		`UPDATE payment_transactions SET status = 'refunded', updated_at = NOW() WHERE gateway_txn_id = $1`,
		pi.ID,
	)

	log.Printf("[StripeWebhook] charge.refunded: order=%s pi=%s", orderID, pi.ID)
	return nil
}

// ─── Ledger Split ────────────────────────────────────────────────────────────

// ExecuteSplit performs the three-way ledger split: admin commission + vendor escrow + delivery escrow.
func (s *StripeService) ExecuteSplit(ctx context.Context, orderID, storeID string, amount float64, gatewayTxnID string) error {
	ctx = context.WithoutCancel(ctx)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Lock order
	var dbAmount float64
	var orderStatus, paymentStatus string
	err = tx.QueryRow(ctx,
		`SELECT total_amount, status, COALESCE(payment_status, '') FROM orders WHERE order_tracking_id = $1 FOR UPDATE`,
		orderID,
	).Scan(&dbAmount, &orderStatus, &paymentStatus)
	if err != nil {
		return fmt.Errorf("order not found: %w", err)
	}
	if paymentStatus == "paid" || paymentStatus == "settlement_pending" {
		return stripeClient.ErrOrderAlreadyPaid
	}

	var deliveryTrackingID string
	_ = tx.QueryRow(ctx,
		`SELECT COALESCE(tracking_id, '') FROM deliveries WHERE order_tracking_id = $1`, orderID,
	).Scan(&deliveryTrackingID)

	split, err := s.calculator.CalculateSplit(ctx, dbAmount, storeID, deliveryTrackingID)
	if err != nil {
		return fmt.Errorf("split calculation failed: %w", err)
	}

	idempotencyKey := fmt.Sprintf("stripe:split:%s", gatewayTxnID)

	// 2. Update payment_transactions
	_, _ = tx.Exec(ctx,
		`UPDATE payment_transactions SET status = 'settlement_pending', updated_at = NOW() WHERE gateway_txn_id = $1`,
		gatewayTxnID,
	)

	// 3. Update order split columns
	_, _ = tx.Exec(ctx,
		`UPDATE orders SET admin_commission = $1, vendor_escrow = $2, delivery_escrow = $3,
		 payment_status = 'settlement_pending', updated_at = NOW() WHERE order_tracking_id = $4`,
		split.AdminRevenue, split.VendorEscrow, split.DeliveryEscrow, orderID,
	)

	// 4. Build outbox event for atomic settlement
	transfers := []map[string]interface{}{
		{
			"debit_account":  string(ledger.AccountStripeHolding),
			"credit_account": string(ledger.AccountAdminRevenue),
			"amount":         split.AdminRevenue,
			"idempotency":    idempotencyKey + ":admin",
		},
		{
			"debit_account":  string(ledger.AccountStripeHolding),
			"credit_account": string(ledger.AccountVendorLockedEscrow),
			"amount":         split.VendorEscrow,
			"idempotency":    idempotencyKey + ":vendor",
		},
	}
	if split.DeliveryEscrow > 0 {
		transfers = append(transfers, map[string]interface{}{
			"debit_account":  string(ledger.AccountStripeHolding),
			"credit_account": string(ledger.AccountCentralEscrow),
			"amount":         split.DeliveryEscrow,
			"idempotency":    idempotencyKey + ":delivery",
		})
	}

	outboxPayload, err := json.Marshal(map[string]interface{}{
		"order_id":       orderID,
		"gateway_txn_id": gatewayTxnID,
		"amount":         amount,
		"split": map[string]float64{
			"admin_commission": split.AdminRevenue,
			"vendor_escrow":    split.VendorEscrow,
			"delivery_escrow":  split.DeliveryEscrow,
		},
		"transfers": transfers,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal outbox payload: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (topic, payload, status, created_at)
		 VALUES ('payment_settlement', $1, 'PENDING', NOW())`,
		outboxPayload,
	)
	if err != nil {
		return fmt.Errorf("failed to insert outbox event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit split transaction: %w", err)
	}

	log.Printf("[StripeService] ExecuteSplit: order=%s admin=%.2f vendor=%.2f delivery=%.2f",
		orderID, split.AdminRevenue, split.VendorEscrow, split.DeliveryEscrow)
	return nil
}

// ─── Refund ──────────────────────────────────────────────────────────────────

// ProcessRefund issues a refund via Stripe and updates local records.
func (s *StripeService) ProcessRefund(ctx context.Context, orderID string, amount float64, reason string) error {
	if !s.stripe.IsConfigured() {
		return stripeClient.ErrNotConfigured
	}

	// 1. Find the payment transaction
	var gatewayTxnID string
	var dbAmount float64
	err := s.db.QueryRow(ctx,
		`SELECT gateway_txn_id, amount FROM payment_transactions WHERE order_id = $1 AND status = 'completed' ORDER BY created_at DESC LIMIT 1`,
		orderID,
	).Scan(&gatewayTxnID, &dbAmount)
	if err != nil {
		return fmt.Errorf("no completed payment found for order %s: %w", orderID, err)
	}

	// 2. Validate refund amount
	if amount <= 0 {
		amount = dbAmount // Full refund
	}
	if amount > dbAmount {
		return fmt.Errorf("refund amount %.2f exceeds payment amount %.2f", amount, dbAmount)
	}

	// 3. Call Stripe refund API
	idempotencyKey := stripeClient.GenerateRefundKey(orderID, 1)
	amountCents := int64(amount * 100)
	_, err = s.stripe.RefundPaymentIntent(ctx, gatewayTxnID, amountCents, idempotencyKey)
	if err != nil {
		return fmt.Errorf("stripe refund failed: %w", err)
	}

	// 4. Update local records
	_, _ = s.db.Exec(ctx,
		`UPDATE orders SET status = 'refunded', payment_status = 'refunded', updated_at = NOW() WHERE order_tracking_id = $1`,
		orderID,
	)
	_, _ = s.db.Exec(ctx,
		`UPDATE payment_transactions SET status = 'refunded', updated_at = NOW() WHERE gateway_txn_id = $1`,
		gatewayTxnID,
	)

	s.logEvent("refund", orderID, gatewayTxnID, fmt.Sprintf("Refund processed: amount=%.2f reason=%s", amount, reason))
	return nil
}

// ─── Structured Logging ──────────────────────────────────────────────────────

// logEvent writes structured payment event logs for observability.
func (s *StripeService) logEvent(action, orderID, paymentIntentID, message string) {
	log.Printf("[StripePayment] action=%s order=%s pi=%s ts=%s %s",
		action, orderID, paymentIntentID, time.Now().UTC().Format(time.RFC3339), message)
}
