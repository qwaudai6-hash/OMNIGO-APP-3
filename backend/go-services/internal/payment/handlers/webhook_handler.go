package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/omnigo/backend/internal/ledger"
	orderRepo "github.com/omnigo/backend/internal/order/repository"
	payment_orchestrator "github.com/omnigo/backend/internal/payment_orchestrator"
	paymentRepo "github.com/omnigo/backend/internal/payment/repository"
	"github.com/omnigo/backend/internal/payment/service"
)

type WebhookHandler struct {
	orchestrator *service.Orchestrator
	ledgerSvc    *ledger.Service
	txnRepo      *paymentRepo.Repository
	orders       *orderRepo.OrderRepository
	calculator   *payment_orchestrator.CommissionCalculator
	db           *pgxpool.Pool
	redis        redis.UniversalClient
}

func NewWebhookHandler(
	orchestrator *service.Orchestrator,
	ledgerSvc *ledger.Service,
	txnRepo *paymentRepo.Repository,
	orders *orderRepo.OrderRepository,
	calculator *payment_orchestrator.CommissionCalculator,
	db *pgxpool.Pool,
	rdb redis.UniversalClient,
) *WebhookHandler {
	return &WebhookHandler{
		orchestrator: orchestrator,
		ledgerSvc:    ledgerSvc,
		txnRepo:      txnRepo,
		orders:       orders,
		calculator:   calculator,
		db:           db,
		redis:        rdb,
	}
}

func (h *WebhookHandler) HandleWebhook(c *gin.Context) {
	gatewayName := c.Param("gateway")

	// Read payload
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf("[Webhook] Error reading body: %v", err)
		c.Status(http.StatusBadRequest)
		return
	}

	// Signature transport differs per gateway: Stripe uses a dedicated
	// header, JazzCash/EasyPaisa may use X-Payment-Signature or embed it in
	// the payload (their VerifyWebhook implementations handle that).
	signature := c.GetHeader("Stripe-Signature")
	if signature == "" {
		signature = c.GetHeader("X-Payment-Signature")
	}

	event, err := h.orchestrator.ProcessWebhook(gatewayName, payload, signature)
	if err != nil {
		log.Printf("[Webhook] Error processing webhook for %s: %v", gatewayName, err)
		c.Status(http.StatusBadRequest)
		return
	}

	if event.Status == "SUCCESS" {
		if err := h.settleSuccess(c.Request.Context(), gatewayName, &event); err != nil {
			log.Printf("[Webhook] Settlement enqueue failed for %s order %s: %v", gatewayName, event.OrderID, err)
			// Return 500 so the gateway retries until the settlement event is durably enqueued.
			c.Status(http.StatusInternalServerError)
			return
		}
	}

	c.Status(http.StatusOK)
}

// settleSuccess records the captured gateway payment and hands off completion
// to the SettlementWorker by enqueueing a 'payment_settlement' outbox event.
// The worker then atomically performs the three-way ledger split, the vendor
// escrow hold, and the final order -> paid transition — the exact same
// completion semantics as the PayFast flow (FIX-PAY-01: previously this path
// dumped the whole amount into central_escrow, marked the order paid inline,
// and never created an escrow hold, so vendors could never be paid out).
//
// Dedup contract: a Redis SetNX lock keyed on the gateway transaction ID makes
// this endpoint mutually exclusive with the split-aware Stripe webhook
// (/api/v1/webhooks/stripe), which uses the same key namespace. Even if both
// endpoints ever receive the same event (e.g. dual webhook configuration),
// the shared ledger idempotency keys ("settle:<gw>:<txn>:*") make the second
// execution an idempotent replay instead of a double money movement.
func (h *WebhookHandler) settleSuccess(ctx context.Context, gatewayName string, event *service.WebhookEvent) error {
	if h.txnRepo == nil || h.orders == nil || h.calculator == nil || h.db == nil {
		return fmt.Errorf("settlement dependencies not wired")
	}
	if event.OrderID == "" {
		return fmt.Errorf("webhook event without order_id cannot be settled")
	}

	// Cross-endpoint dedup (best-effort when Redis is absent).
	settleLock := fmt.Sprintf("lock:webhook:settle:%s:%s", gatewayName, event.TransactionID)
	if h.redis != nil {
		acquired, err := h.redis.SetNX(ctx, settleLock, "1", 24*time.Hour).Result()
		if err != nil {
			// Fail-open but log loudly: single-endpoint deployments are still
			// protected by the payment_transactions idempotency_key.
			log.Printf("[Webhook] Redis settle-lock unavailable (%v) — proceeding for %s %s", err, gatewayName, event.TransactionID)
		} else if !acquired {
			log.Printf("[Webhook] Duplicate settlement suppressed for %s txn %s", gatewayName, event.TransactionID)
			return nil
		}
	}

	// Authoritative order lookup: never trust webhook amounts or derive the
	// store from untrusted metadata.
	order, err := h.orders.GetOrderByTrackingID(ctx, event.OrderID)
	if err != nil {
		return fmt.Errorf("order %s not found: %w", event.OrderID, err)
	}
	// Convert amounts to paisa for comparison
	eventAmountPaisa := int64(event.Amount * 100)
	orderAmountPaisa := int64(order.TotalAmount * 100)
	if eventAmountPaisa != orderAmountPaisa {
		return fmt.Errorf("amount mismatch for order %s: gateway %d paisa vs order %d paisa", event.OrderID, eventAmountPaisa, orderAmountPaisa)
	}
	settleAmountPaisa := orderAmountPaisa

	// Idempotent local record of the capture. The row starts at
	// settlement_pending and the SettlementWorker flips it to 'captured'
	// atomically with the order -> paid update (mirrors the PayFast flow).
	idempotencyKey := fmt.Sprintf("settle:%s:%s", gatewayName, event.TransactionID)
	existing, getErr := h.txnRepo.GetByIDempotencyKey(ctx, idempotencyKey)
	if getErr == nil && existing != nil {
		// Already enqueued before (e.g. gateway retry after our 500) — do not
		// create a second settlement event for the same gateway transaction.
		log.Printf("[Webhook] Settlement already enqueued for %s txn %s (txn row %s)", gatewayName, event.TransactionID, existing.TransactionID)
		return nil
	}
	txn, err := h.txnRepo.Create(ctx, &paymentRepo.PaymentTransaction{
		OrderID:        event.OrderID,
		Gateway:        gatewayName,
		GatewayTxnID:   event.TransactionID,
		Amount:         float64(settleAmountPaisa) / 100.0, // Store as rupees for payment_transactions compat
		Currency:       event.Currency,
		Status:         paymentRepo.TxnSettlementPending,
		Kind:           paymentRepo.KindPayment,
		IdempotencyKey: idempotencyKey,
		Metadata: map[string]any{
			"customer_id":   event.CustomerID,
			"amount_paisa":  settleAmountPaisa,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to record payment transaction: %w", err)
	}

	// Three-way split against the store's commission rate and any linked
	// delivery gig's fee (paisa-rounded, overflow-guarded inside the
	// calculator — identical math to the PayFast split path).
	deliveryTrackingID := ""
	if h.calculator != nil {
		deliveryTrackingID = h.calculator.ResolveDeliveryTrackingID(ctx, event.OrderID)
	}
	split, err := h.calculator.CalculateSplit(ctx, settleAmountPaisa, order.VendorStoreTrackID, deliveryTrackingID)
	if err != nil {
		return fmt.Errorf("split calculation failed for order %s: %w", event.OrderID, err)
	}

	transfers := []map[string]any{
		{
			"debit_account":  string(ledger.AccountGatewayClearing),
			"credit_account": string(ledger.AccountAdminRevenue),
			"amount":         split.AdminRevenue,
			"idempotency":    idempotencyKey + ":admin",
		},
		{
			"debit_account":  string(ledger.AccountGatewayClearing),
			"credit_account": string(ledger.AccountVendorLockedEscrow),
			"amount":         split.VendorEscrow,
			"idempotency":    idempotencyKey + ":vendor",
		},
	}
	if split.DeliveryEscrow > 0 {
		transfers = append(transfers, map[string]any{
			"debit_account":  string(ledger.AccountGatewayClearing),
			"credit_account": string(ledger.AccountCentralEscrow),
			"amount":         split.DeliveryEscrow,
			"idempotency":    idempotencyKey + ":delivery",
		})
	}

	outboxPayload, err := json.Marshal(map[string]any{
		// Field names match workers.SettlementPayload JSON tags.
		"internal_txn_id":      txn.TransactionID,
		"order_id":             event.OrderID,
		"gateway_txn_id":       event.TransactionID,
		"store_id":             order.VendorStoreTrackID,
		"vendor_tracking_id":   order.VendorTrackID,
		"delivery_tracking_id": deliveryTrackingID,
		"total_amount_paisa":   settleAmountPaisa,
		"currency":             event.Currency,
		"admin_revenue_paisa":  split.AdminRevenue,
		"vendor_escrow_paisa":  split.VendorEscrow,
		"delivery_escrow_paisa": split.DeliveryEscrow,
		"idempotency_key":      idempotencyKey,
		"transfers":            transfers,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal settlement payload: %w", err)
	}

	_, err = h.db.Exec(ctx,
		`INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at, updated_at)
		 VALUES ($1, 'payment_settlement', $2, 'PENDING', NOW(), NOW())`,
		event.OrderID, string(outboxPayload),
	)
	if err != nil {
		return fmt.Errorf("failed to enqueue settlement outbox event: %w", err)
	}

	log.Printf("[Webhook] Settlement enqueued for order %s (txn %s, %d paisa %s via %s): admin=%d vendor_escrow=%d delivery_escrow=%d",
		event.OrderID, txn.TransactionID, settleAmountPaisa, event.Currency, gatewayName,
		split.AdminRevenue, split.VendorEscrow, split.DeliveryEscrow)
	return nil
}
