package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/webhook"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/payment_orchestrator"
)

// StripeSplitHandler wraps the existing Stripe webhook with automatic payment splitting.
type StripeSplitHandler struct {
	redis         redis.UniversalClient
	kafka         *kgo.Client
	db            *pgxpool.Pool
	ledger        *ledger.Service
	escrow        *escrow.Service
	calculator    *payment_orchestrator.CommissionCalculator
	webhookSecret string
}

func NewStripeSplitHandler(
	rdb redis.UniversalClient,
	kafkaClient *kgo.Client,
	db *pgxpool.Pool,
	ledgerSvc *ledger.Service,
	escrowSvc *escrow.Service,
	calc *payment_orchestrator.CommissionCalculator,
	secret string,
) *StripeSplitHandler {
	return &StripeSplitHandler{
		redis:         rdb,
		kafka:         kafkaClient,
		db:            db,
		ledger:        ledgerSvc,
		escrow:        escrowSvc,
		calculator:    calc,
		webhookSecret: secret,
	}
}

// ServeHTTP handles Stripe webhooks and executes payment splits automatically.
func (h *StripeSplitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 1. Read body with size limit
	const MaxBodyBytes = int64(65536)
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// 2. Verify Stripe signature
	sigHeader := r.Header.Get("Stripe-Signature")
	event, err := webhook.ConstructEvent(payload, sigHeader, h.webhookSecret)
	if err != nil {
		fmt.Printf("[StripeSplit] Signature verification failed: %v\n", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// 3. Idempotency check
	if h.redis != nil {
		webhookLockKey := fmt.Sprintf("lock:webhook:split:%s", event.ID)
		success, err := h.redis.SetNX(ctx, webhookLockKey, "1", 24*time.Hour).Result()
		if err != nil || !success {
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// 4. Process payment_intent.succeeded
	if event.Type == "payment_intent.succeeded" {
		var paymentIntent stripe.PaymentIntent
		if err := json.Unmarshal(event.Data.Raw, &paymentIntent); err != nil {
			fmt.Printf("[StripeSplit] Failed to unmarshal payment intent: %v\n", err)
			w.WriteHeader(http.StatusOK)
			return
		}

		orderID := paymentIntent.Metadata["nonce"]
		storeID := paymentIntent.Metadata["store_id"]

		if orderID == "" {
			fmt.Printf("[StripeSplit] No order ID in payment intent metadata\n")
			w.WriteHeader(http.StatusOK)
			return
		}

		// 5. Execute payment split
		if err := h.executeSplit(ctx, orderID, storeID, float64(paymentIntent.Amount)/100.0, event.ID); err != nil {
			fmt.Printf("[StripeSplit] Split execution failed for order %s: %v\n", orderID, err)
			// Still return 200 to prevent Stripe retries — the split can be reconciled later
		} else {
			fmt.Printf("[StripeSplit] Split completed for order %s: %.2f PKR\n", orderID, float64(paymentIntent.Amount)/100.0)
		}

		// 6. Emit Kafka event
		if h.kafka != nil {
			eventPayload := map[string]interface{}{
				"event_id":  event.ID,
				"order_id":  orderID,
				"store_id":  storeID,
				"amount":    paymentIntent.Amount,
				"currency":  paymentIntent.Currency,
				"status":    "PAYMENT_SPLIT_COMPLETED",
				"timestamp": time.Now().UnixMilli(),
			}
			eventBytes, _ := json.Marshal(eventPayload)
			h.kafka.Produce(ctx, &kgo.Record{
				Topic: "payment.split_completed",
				Key:   []byte(orderID),
				Value: eventBytes,
			}, nil)
		}
	}

	// 7. Fast ACK
	w.WriteHeader(http.StatusOK)
}

// executeSplit performs the three-way ledger split for an online payment.
func (h *StripeSplitHandler) executeSplit(ctx context.Context, orderID, storeID string, amount float64, eventID string) error {
	// 1. Read order details to get delivery tracking ID
	var deliveryTrackingID string
	err := h.db.QueryRow(ctx,
		`SELECT COALESCE(d.tracking_id, '') FROM orders o
		 LEFT JOIN deliveries d ON o.order_tracking_id = d.order_tracking_id
		 WHERE o.order_tracking_id = $1`,
		orderID,
	).Scan(&deliveryTrackingID)
	if err != nil {
		// Order might not have a delivery yet — proceed without delivery fee
		deliveryTrackingID = ""
	}

	// 2. Calculate split
	split, err := h.calculator.CalculateSplit(ctx, amount, storeID, deliveryTrackingID)
	if err != nil {
		return fmt.Errorf("split calculation failed: %w", err)
	}

	// 3. Execute ledger transfers atomically
	idempotencyKey := fmt.Sprintf("stripe:split:%s", eventID)

	// Transfer 1: Stripe holding → Admin revenue (2%)
	_, err = h.ledger.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   ledger.AccountStripeHolding,
		CreditAccount:  ledger.AccountAdminRevenue,
		Amount:         split.AdminRevenue,
		Currency:       "PKR",
		ReferenceType:  "order_payment",
		ReferenceID:    orderID,
		Description:    fmt.Sprintf("Stripe payment split — admin commission (%.1f%%) for order %s", split.CommissionRate, orderID),
		IdempotencyKey: idempotencyKey + ":admin",
	})
	if err != nil {
		return fmt.Errorf("admin revenue transfer failed: %w", err)
	}

	// Transfer 2: Stripe holding → Vendor locked escrow (98% - delivery_fee)
	_, err = h.ledger.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   ledger.AccountStripeHolding,
		CreditAccount:  ledger.AccountVendorLockedEscrow,
		Amount:         split.VendorEscrow,
		Currency:       "PKR",
		ReferenceType:  "order_payment",
		ReferenceID:    orderID,
		Description:    fmt.Sprintf("Stripe payment split — vendor escrow (98%%) for order %s", orderID),
		IdempotencyKey: idempotencyKey + ":vendor",
	})
	if err != nil {
		return fmt.Errorf("vendor escrow transfer failed: %w", err)
	}

	// Transfer 3: Stripe holding → Central escrow (delivery fee, for rider payout)
	if split.DeliveryEscrow > 0 {
		_, err = h.ledger.Transfer(ctx, ledger.TransferRequest{
			DebitAccount:   ledger.AccountStripeHolding,
			CreditAccount:  ledger.AccountCentralEscrow,
			Amount:         split.DeliveryEscrow,
			Currency:       "PKR",
			ReferenceType:  "order_payment",
			ReferenceID:    orderID,
			Description:    fmt.Sprintf("Stripe payment split — delivery escrow for order %s", orderID),
			IdempotencyKey: idempotencyKey + ":delivery",
		})
		if err != nil {
			return fmt.Errorf("delivery escrow transfer failed: %w", err)
		}
	}

	// 4. Create escrow hold for vendor portion (48h hold)
	if err := h.escrow.CreateHold(ctx, orderID, storeID, split.VendorEscrow); err != nil {
		fmt.Printf("[StripeSplit] Warning: failed to create escrow hold for order %s: %v\n", orderID, err)
		// Non-fatal — the ledger entries are already committed
	}

	// 5. Update order admin_commission
	_, err = h.db.Exec(ctx,
		`UPDATE orders SET admin_commission = $1 WHERE order_tracking_id = $2`,
		split.AdminRevenue, orderID,
	)
	if err != nil {
		fmt.Printf("[StripeSplit] Warning: failed to update order commission for %s: %v\n", orderID, err)
	}

	return nil
}
