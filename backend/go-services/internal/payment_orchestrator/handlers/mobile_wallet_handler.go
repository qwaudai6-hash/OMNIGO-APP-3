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
	paymentRepo "github.com/omnigo/backend/internal/payment/repository"
	payment_orchestrator "github.com/omnigo/backend/internal/payment_orchestrator"
	paymentservice "github.com/omnigo/backend/internal/payment/service"
	"github.com/omnigo/backend/internal/shared/middleware"
)

// MobileWalletHandler exposes the JazzCash / EasyPaisa hosted-checkout flow
// the Flutter app calls at /api/v1/payments/{jazzcash|easypaisa}/…
// Initiation is JWT-protected and validates order ownership + amount against
// the database. Callbacks are public but cryptographically verified by the
// gateway implementation inside paymentservice.Orchestrator.
//
// Completion semantics mirror the order-service webhook (FIX-PAY-01): the
// callback enqueues a 'payment_settlement' outbox event so the
// SettlementWorker performs the three-way ledger split, creates the vendor
// escrow hold, and flips the order to paid atomically — dumping the whole
// amount into central_escrow inline would mean vendors are never credited
// for JazzCash/EasyPaisa orders.
type MobileWalletHandler struct {
	orchestrator *paymentservice.Orchestrator
	txnRepo      *paymentRepo.Repository
	orders       *orderRepo.OrderRepository
	calculator   *payment_orchestrator.CommissionCalculator
	db           *pgxpool.Pool
	redis        redis.UniversalClient
}

func NewMobileWalletHandler(
	orchestrator *paymentservice.Orchestrator,
	txnRepo *paymentRepo.Repository,
	orders *orderRepo.OrderRepository,
	calculator *payment_orchestrator.CommissionCalculator,
	db *pgxpool.Pool,
	rdb redis.UniversalClient,
) *MobileWalletHandler {
	return &MobileWalletHandler{
		orchestrator: orchestrator,
		txnRepo:      txnRepo,
		orders:       orders,
		calculator:   calculator,
		db:           db,
		redis:        rdb,
	}
}

type mobileWalletInitiateRequest struct {
	Gateway       string  `json:"-"`
	OrderID       string  `json:"order_id" binding:"required"`
	CustomerID    string  `json:"customer_id"`
	Amount        float64 `json:"amount" binding:"required"`
	Currency      string  `json:"currency"`
	ReturnURL     string  `json:"return_url"`
	CancelURL     string  `json:"cancel_url"`
	CustomerPhone string  `json:"customer_phone"`
	CustomerEmail string  `json:"customer_email"`
}

// Initiate handles POST /api/v1/payments/{gateway}/initiate.
func (h *MobileWalletHandler) Initiate(gateway string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.orchestrator == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "payment orchestrator unavailable"})
			return
		}
		var req mobileWalletInitiateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// SECURITY: identity comes from the JWT, never from the body.
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

		// Double-payment guard: reject if order is already paid
		if order.Status == "paid" || order.PaymentStatus == "paid" {
			c.JSON(http.StatusConflict, gin.H{"error": "order has already been paid"})
			return
		}

		const amountEpsilon = 0.01
		if diff := req.Amount - order.TotalAmount; diff > amountEpsilon || diff < -amountEpsilon {
			c.JSON(http.StatusBadRequest, gin.H{"error": "amount mismatch: provided amount does not match order total"})
			return
		}

		res, err := h.orchestrator.CreateCheckout(c.Request.Context(), gateway, paymentservice.CheckoutRequest{
			OrderID:       req.OrderID,
			CustomerID:    callerID,
			Amount:        order.TotalAmount,
			Currency:      req.Currency,
			ReturnURL:     req.ReturnURL,
			CancelURL:     req.CancelURL,
			CustomerPhone: req.CustomerPhone,
			CustomerEmail: req.CustomerEmail,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

// Callback handles POST /api/v1/payments/{gateway}/callback — the async
// gateway postback. Signature verification happens inside the gateway's
// VerifyWebhook; a forged callback is rejected before any state changes.
func (h *MobileWalletHandler) Callback(gateway string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.orchestrator == nil || h.txnRepo == nil || h.orders == nil || h.calculator == nil || h.db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "mobile wallet processing unavailable"})
			return
		}
		payload, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read callback body"})
			return
		}

		event, err := h.orchestrator.ProcessWebhook(gateway, payload, c.GetHeader("X-Payment-Signature"))
		if err != nil {
			log.Printf("[mobile-wallet] %s callback rejected: %v", gateway, err)
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

		if err := h.settleSuccess(c.Request.Context(), gateway, &event); err != nil {
			log.Printf("[mobile-wallet] settlement enqueue failed for %s order %s: %v", gateway, event.OrderID, err)
			// 500 makes the gateway retry the delivery; idempotency below
			// keeps the retry safe.
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settlement enqueue failed"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "order_id": event.OrderID})
	}
}

// settleSuccess records the captured payment (idempotently) and hands off to
// the SettlementWorker via a 'payment_settlement' outbox event — identical
// completion semantics to the order-service webhook and the PayFast flow.
func (h *MobileWalletHandler) settleSuccess(ctx context.Context, gateway string, event *paymentservice.WebhookEvent) error {
	// Cross-endpoint dedup with the order-service webhook path (best-effort
	// without Redis; the txn idempotency key is the hard guarantee).
	settleLock := fmt.Sprintf("lock:webhook:settle:%s:%s", gateway, event.TransactionID)
	if h.redis != nil {
		acquired, err := h.redis.SetNX(ctx, settleLock, "1", 24*time.Hour).Result()
		if err == nil && !acquired {
			log.Printf("[mobile-wallet] duplicate settlement suppressed for %s txn %s", gateway, event.TransactionID)
			return nil
		}
	}

	// Authoritative order lookup: never trust webhook amounts or metadata.
	order, err := h.orders.GetOrderByTrackingID(ctx, event.OrderID)
	if err != nil {
		return fmt.Errorf("order %s not found: %w", event.OrderID, err)
	}
	// Convert to paisa for exact comparison
	eventAmountPaisa := int64(event.Amount * 100)
	orderAmountPaisa := int64(order.TotalAmount * 100)
	if eventAmountPaisa != orderAmountPaisa {
		return fmt.Errorf("amount mismatch for order %s: gateway %d paisa vs order %d paisa", event.OrderID, eventAmountPaisa, orderAmountPaisa)
	}
	settleAmountPaisa := orderAmountPaisa

	idempotencyKey := fmt.Sprintf("settle:%s:%s", gateway, event.TransactionID)
	existing, getErr := h.txnRepo.GetByIDempotencyKey(ctx, idempotencyKey)
	if getErr == nil && existing != nil {
		log.Printf("[mobile-wallet] settlement already enqueued for %s txn %s", gateway, event.TransactionID)
		return nil
	}
	txn, err := h.txnRepo.Create(ctx, &paymentRepo.PaymentTransaction{
		OrderID:        event.OrderID,
		Gateway:        gateway,
		GatewayTxnID:   event.TransactionID,
		Amount:         float64(settleAmountPaisa) / 100.0,
		Currency:       event.Currency,
		Status:         paymentRepo.TxnSettlementPending,
		Kind:           paymentRepo.KindPayment,
		IdempotencyKey: idempotencyKey,
		Metadata: map[string]any{
			"customer_id": event.CustomerID,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to record payment transaction: %w", err)
	}

	deliveryTrackingID := h.calculator.ResolveDeliveryTrackingID(ctx, event.OrderID)
	split, err := h.calculator.CalculateSplit(ctx, settleAmountPaisa, order.VendorStoreTrackID, deliveryTrackingID)
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
		// Field names match workers.SettlementPayload JSON tags.
		"internal_txn_id":        txn.TransactionID,
		"order_id":               event.OrderID,
		"gateway_txn_id":         event.TransactionID,
		"store_id":               order.VendorStoreTrackID,
		"vendor_tracking_id":     order.VendorTrackID,
		"delivery_tracking_id":   deliveryTrackingID,
		"total_amount_paisa":     settleAmountPaisa,
		"currency":               event.Currency,
		"admin_revenue_paisa":    split.AdminRevenue,
		"vendor_escrow_paisa":    split.VendorEscrow,
		"delivery_escrow_paisa":  split.DeliveryEscrow,
		"idempotency_key":      idempotencyKey,
		"transfers":            transfers,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal settlement payload: %w", err)
	}

	if _, err := h.db.Exec(ctx,
		`INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at, updated_at)
		 VALUES ($1, 'payment_settlement', $2, 'PENDING', NOW(), NOW())`,
		event.OrderID, string(outboxPayload),
	); err != nil {
		return fmt.Errorf("failed to enqueue settlement outbox event: %w", err)
	}

	log.Printf("[mobile-wallet] settlement enqueued for order %s (%d paisa %s via %s): admin=%d vendor_escrow=%d delivery_escrow=%d",
		event.OrderID, settleAmountPaisa, event.Currency, gateway, split.AdminRevenue, split.VendorEscrow, split.DeliveryEscrow)
	return nil
}

// Status handles GET /api/v1/payments/{gateway}/status/:txn_ref.
func (h *MobileWalletHandler) Status(gateway string) gin.HandlerFunc {
	return func(c *gin.Context) {
		txnRef := c.Param("txn_ref")
		if txnRef == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "txn_ref is required"})
			return
		}
		txn, err := h.txnRepo.GetByGatewayTxnID(c.Request.Context(), gateway, txnRef)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "transaction not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"order_id":       txn.OrderID,
			"gateway":        txn.Gateway,
			"gateway_txn_id": txn.GatewayTxnID,
			"status":         txn.Status,
			"amount":         txn.Amount,
			"currency":       txn.Currency,
		})
	}
}

// RegisterRoutes attaches the JazzCash / EasyPaisa endpoints.
func (h *MobileWalletHandler) RegisterRoutes(router *gin.Engine) {
	for _, gw := range []string{"jazzcash", "easypaisa"} {
		gw := gw
		router.POST("/api/v1/payments/"+gw+"/initiate", middleware.JWTAuth(), h.Initiate(gw))
		router.POST("/api/v1/payments/"+gw+"/callback", h.Callback(gw))
		router.GET("/api/v1/payments/"+gw+"/status/:txn_ref", middleware.JWTAuth(), h.Status(gw))
	}
}
