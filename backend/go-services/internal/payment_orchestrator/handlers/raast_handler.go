package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnigo/backend/internal/ledger"
	orderRepo "github.com/omnigo/backend/internal/order/repository"
	paymentRepo "github.com/omnigo/backend/internal/payment/repository"
	payment_orchestrator "github.com/omnigo/backend/internal/payment_orchestrator"
	paymentservice "github.com/omnigo/backend/internal/payment/service"
	"github.com/omnigo/backend/internal/shared/middleware"
)

// RaastHandler exposes the PayFast Raast P2M (instant bank transfer) flow.
// Raast is an instant 24/7 bank transfer via the SBP network.
// The Flutter app calls at /api/v1/payments/raast/…
type RaastHandler struct {
	orchestrator *paymentservice.Orchestrator
	txnRepo      *paymentRepo.Repository
	orders       *orderRepo.OrderRepository
	calculator   *payment_orchestrator.CommissionCalculator
	db           *pgxpool.Pool
}

func NewRaastHandler(
	orchestrator *paymentservice.Orchestrator,
	txnRepo *paymentRepo.Repository,
	orders *orderRepo.OrderRepository,
	calculator *payment_orchestrator.CommissionCalculator,
	db *pgxpool.Pool,
) *RaastHandler {
	return &RaastHandler{
		orchestrator: orchestrator,
		txnRepo:      txnRepo,
		orders:       orders,
		calculator:   calculator,
		db:           db,
	}
}

type raastInitiateRequest struct {
	OrderID   string  `json:"order_id" binding:"required"`
	Amount    float64 `json:"amount" binding:"required"`
	Currency  string  `json:"currency"`
	ReturnURL string  `json:"return_url"`
	CancelURL string  `json:"cancel_url"`
}

// Initiate handles POST /api/v1/payments/raast/initiate
func (h *RaastHandler) Initiate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.orchestrator == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "payment orchestrator unavailable"})
			return
		}
		var req raastInitiateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		callerID := middleware.GetTrackingID(c)
		if callerID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authenticated customer"})
			return
		}

		order, err := h.orders.GetOrderByTrackingID(c.Request.Context(), req.OrderID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
			return
		}
		if order.UserTrackID != callerID {
			c.JSON(http.StatusForbidden, gin.H{"error": "order does not belong to authenticated customer"})
			return
		}

		if order.Status == "paid" || order.PaymentStatus == "paid" {
			c.JSON(http.StatusConflict, gin.H{"error": "order has already been paid"})
			return
		}

		res, err := h.orchestrator.CreateCheckout(c.Request.Context(), "raast", paymentservice.CheckoutRequest{
			OrderID:   req.OrderID,
			Amount:    order.TotalAmount,
			Currency:  req.Currency,
			ReturnURL: req.ReturnURL,
			CancelURL: req.CancelURL,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

// Callback handles POST /api/v1/payments/raast/callback - the Raast webhook
func (h *RaastHandler) Callback() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.orchestrator == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "raast processing unavailable"})
			return
		}
		payload, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read callback body"})
			return
		}

		event, err := h.orchestrator.ProcessWebhook("raast", payload, c.GetHeader("X-Raast-Signature"))
		if err != nil {
			log.Printf("[raast] callback rejected: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid or forged callback"})
			return
		}
		if event.OrderID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "callback is missing order reference"})
			return
		}
		if event.Status != "SUCCESS" {
			c.JSON(http.StatusOK, gin.H{"status": "ignored", "payment_status": event.Status})
			return
		}

		if err := h.settleSuccess(c.Request.Context(), &event); err != nil {
			log.Printf("[raast] settlement enqueue failed for order %s: %v", event.OrderID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settlement enqueue failed"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "order_id": event.OrderID})
	}
}

// settleSuccess records the captured payment and hands off to SettlementWorker
func (h *RaastHandler) settleSuccess(ctx context.Context, event *paymentservice.WebhookEvent) error {
	order, err := h.orders.GetOrderByTrackingID(ctx, event.OrderID)
	if err != nil {
		return fmt.Errorf("order %s not found: %w", event.OrderID, err)
	}

	eventAmountPaisa := int64(event.Amount * 100)
	orderAmountPaisa := int64(order.TotalAmount * 100)
	if eventAmountPaisa != orderAmountPaisa {
		return fmt.Errorf("amount mismatch for order %s: raast %d paisa vs order %d paisa", event.OrderID, eventAmountPaisa, orderAmountPaisa)
	}

	idempotencyKey := fmt.Sprintf("settle:raast:%s", event.TransactionID)
	existing, getErr := h.txnRepo.GetByIDempotencyKey(ctx, idempotencyKey)
	if getErr == nil && existing != nil {
		log.Printf("[raast] settlement already enqueued for txn %s", event.TransactionID)
		return nil
	}

	txn, err := h.txnRepo.Create(ctx, &paymentRepo.PaymentTransaction{
		OrderID:        event.OrderID,
		Gateway:        "raast",
		GatewayTxnID:   event.TransactionID,
		Amount:         event.Amount,
		Currency:       event.Currency,
		Status:         paymentRepo.TxnSettlementPending,
		Kind:           paymentRepo.KindPayment,
		IdempotencyKey: idempotencyKey,
		Metadata: map[string]any{
			"gateway": "raast",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to record payment transaction: %w", err)
	}

	deliveryTrackingID := h.calculator.ResolveDeliveryTrackingID(ctx, event.OrderID)
	split, err := h.calculator.CalculateSplit(ctx, orderAmountPaisa, order.VendorStoreTrackID, deliveryTrackingID)
	if err != nil {
		return fmt.Errorf("split calculation failed for order %s: %w", event.OrderID, err)
	}

	transfers := []map[string]any{
		{
			"debit_account":  string(ledger.AccountGatewayClearing),
			"credit_account": string(ledger.AccountAdminRevenue),
			"amount_paisa":   split.AdminRevenue,
			"idempotency":    idempotencyKey + ":admin",
		},
		{
			"debit_account":  string(ledger.AccountGatewayClearing),
			"credit_account": string(ledger.AccountVendorLockedEscrow),
			"amount_paisa":   split.VendorEscrow,
			"idempotency":    idempotencyKey + ":vendor",
		},
	}
	if split.DeliveryEscrow > 0 {
		transfers = append(transfers, map[string]any{
			"debit_account":  string(ledger.AccountGatewayClearing),
			"credit_account": string(ledger.AccountCentralEscrow),
			"amount_paisa":   split.DeliveryEscrow,
			"idempotency":    idempotencyKey + ":delivery",
		})
	}

	outboxPayload, err := json.Marshal(map[string]any{
		"internal_txn_id":        txn.TransactionID,
		"order_id":               event.OrderID,
		"gateway_txn_id":         event.TransactionID,
		"store_id":               order.VendorStoreTrackID,
		"vendor_tracking_id":     order.VendorTrackID,
		"delivery_tracking_id":   deliveryTrackingID,
		"total_amount_paisa":     orderAmountPaisa,
		"currency":               event.Currency,
		"admin_revenue_paisa":    split.AdminRevenue,
		"vendor_escrow_paisa":    split.VendorEscrow,
		"delivery_escrow_paisa":  split.DeliveryEscrow,
		"idempotency_key":       idempotencyKey,
		"transfers":             transfers,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal outbox payload: %w", err)
	}

	if _, err := h.db.Exec(ctx,
		`INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at, updated_at)
		 VALUES ($1, 'payment_settlement', $2, 'PENDING', NOW(), NOW())`,
		event.OrderID, string(outboxPayload),
	); err != nil {
		return fmt.Errorf("failed to enqueue settlement outbox event: %w", err)
	}

	log.Printf("[raast] settlement enqueued for order %s (%d paisa): admin=%d vendor_escrow=%d delivery_escrow=%d",
		event.OrderID, orderAmountPaisa, split.AdminRevenue, split.VendorEscrow, split.DeliveryEscrow)
	return nil
}

// RegisterRoutes attaches the Raast endpoints
func (h *RaastHandler) RegisterRoutes(router *gin.Engine) {
	router.POST("/api/v1/payments/raast/initiate", middleware.JWTAuth(), h.Initiate())
	router.POST("/api/v1/payments/raast/callback", h.Callback())
	router.GET("/api/v1/payments/raast/callback", h.CallbackRedirect())
}

// CallbackRedirect handles GET /api/v1/payments/raast/callback - customer redirect
func (h *RaastHandler) CallbackRedirect() gin.HandlerFunc {
	return func(c *gin.Context) {
		txnRef := c.Query("TXN_REF")
		status := c.Query("RESPONSE_CODE")
		orderID := c.Query("ORDER_ID")

		log.Printf("[raast] GET callback: txn=%s status=%s order=%s", txnRef, status, orderID)

		targetOrigin := raastPostMessageTargetOrigin()
		successHTML := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Payment Successful</title></head>
<body style="font-family:sans-serif;text-align:center;padding:50px;">
    <h2 style="color:#2e7d32;">Payment Successful!</h2>
    <p>Your order is being processed via Raast instant transfer.</p>
    <script>
        if (window.opener) { window.opener.postMessage({status: 'success', order_id: '%s'}, '%s'); }
        setTimeout(function() { window.close(); }, 3000);
    </script>
</body>
</html>`, orderID, targetOrigin)
		failureHTML := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Payment Cancelled</title></head>
<body style="font-family:sans-serif;text-align:center;padding:50px;">
    <h2 style="color:#c62828;">Payment Cancelled</h2>
    <p>Your Raast payment was not completed.</p>
    <script>
        if (window.opener) { window.opener.postMessage({status: 'cancelled', order_id: '%s'}, '%s'); }
        setTimeout(function() { window.close(); }, 3000);
    </script>
</body>
</html>`, orderID, targetOrigin)

		if status == "00" || status == "000" || status == "0000" {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(200, successHTML)
			return
		}

		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(200, failureHTML)
	}
}

func raastPostMessageTargetOrigin() string {
	if o := strings.TrimSpace(os.Getenv("PAYFAST_WEB_ORIGIN")); o != "" {
		return o
	}
	if origins := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS")); origins != "" {
		first := strings.TrimSpace(strings.Split(origins, ",")[0])
		if first != "" && first != "*" {
			return first
		}
	}
	log.Println("WARNING: [raast] PAYFAST_WEB_ORIGIN / CORS_ALLOWED_ORIGINS unset — postMessage falling back to wildcard")
	return "*"
}
