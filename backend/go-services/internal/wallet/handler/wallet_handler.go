package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	orderModels "github.com/omnigo/backend/internal/order/models"
	"github.com/omnigo/backend/internal/payment/payfast"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/wallet/service"
	"github.com/redis/go-redis.v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

type WalletHandler struct {
	riderWallet    *service.RiderWalletService
	customerWallet *service.CustomerWalletService
	orderSvc       OrderStatusUpdater
	kafka          *messaging.KafkaClient
	redis          redis.UniversalClient
	memStore       sync.Map
	db             *pgxpool.Pool // FIX H8: needed for outbox event creation
}

// OrderStatusUpdater is the small surface order-service exposes to wallet callbacks.
type OrderStatusUpdater interface {
	UpdateOrderStatus(ctx context.Context, trackingID string, status string) error
	GetOrder(ctx context.Context, trackingID string) (*orderModels.Order, error)
}

func NewWalletHandler() *WalletHandler {
	return &WalletHandler{}
}

// WithRiderWallet attaches the rider earnings service.
func (h *WalletHandler) WithRiderWallet(rws *service.RiderWalletService) *WalletHandler {
	h.riderWallet = rws
	return h
}

// WithCustomerWallet attaches the customer wallet service.
func (h *WalletHandler) WithCustomerWallet(cws *service.CustomerWalletService) *WalletHandler {
	h.customerWallet = cws
	return h
}

// WithOrderService attaches the order service so verified callbacks can mark
// orders as paid and read assigned riders for wallet crediting.
func (h *WalletHandler) WithOrderService(os OrderStatusUpdater) *WalletHandler {
	h.orderSvc = os
	return h
}

// WithKafka attaches the Kafka client so wallet callbacks can emit
// payments.wallet.completed events.
func (h *WalletHandler) WithKafka(k *messaging.KafkaClient) *WalletHandler {
	h.kafka = k
	return h
}

// WithRedis attaches Redis, used to persist pending wallet top-ups so a
// callback can be bound to the exact customer + amount that initiated it.
func (h *WalletHandler) WithRedis(r redis.UniversalClient) *WalletHandler {
	h.redis = r
	return h
}

// WithDB attaches the database pool for creating settlement outbox events
// when wallet callbacks arrive (FIX H8).
func (h *WalletHandler) WithDB(db *pgxpool.Pool) *WalletHandler {
	h.db = db
	return h
}

type pendingWalletLoad struct {
	CustomerTrackingID string  `json:"customer_tracking_id"`
	Gateway            string  `json:"gateway"`
	AmountPKR          float64 `json:"amount_pkr"`
	AmountCents        int64   `json:"amount_cents"`
	OrderID            string  `json:"order_id,omitempty"`
}

const pendingWalletLoadTTL = 24 * time.Hour

// storePendingWalletLoad persists the intent for a top-up so the callback can
// be validated against what was actually requested (never against unsigned
// callback fields).
func (h *WalletHandler) storePendingWalletLoad(ctx context.Context, txnID string, p pendingWalletLoad) error {
	if h.redis == nil {
		h.memStore.Store("walletload:"+txnID, p)
		return nil
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return h.redis.Set(ctx, "walletload:"+txnID, payload, pendingWalletLoadTTL).Err()
}

// consumePendingWalletLoad atomically reads and deletes the pending top-up so
// a valid callback cannot be replayed.
func (h *WalletHandler) consumePendingWalletLoad(ctx context.Context, txnID string) (*pendingWalletLoad, error) {
	if h.redis == nil {
		val, ok := h.memStore.LoadAndDelete("walletload:" + txnID)
		if !ok {
			return nil, fmt.Errorf("no pending wallet load for txn %s", txnID)
		}
		p, ok := val.(pendingWalletLoad)
		if !ok {
			return nil, fmt.Errorf("invalid pending wallet load type")
		}
		return &p, nil
	}
	payload, err := h.redis.GetDel(ctx, "walletload:"+txnID).Result()
	if err != nil {
		return nil, err
	}
	var p pendingWalletLoad
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// gatewayCheckoutURL returns the hosted-checkout endpoint for a mobile
// wallet gateway. Sandbox URLs are only defaults — production must override
// via JAZZCASH_CHECKOUT_URL / EASYPAISA_CHECKOUT_URL.
func gatewayCheckoutURL(gateway string) string {
	if gateway == "jazzcash" {
		if u := os.Getenv("JAZZCASH_CHECKOUT_URL"); u != "" {
			return u
		}
		return "https://sandbox.jazzcash.com.pk/CustomerAPI/DOTransaction"
	}
	if u := os.Getenv("EASYPAISA_CHECKOUT_URL"); u != "" {
		return u
	}
	return "https://sandbox.easypaisa.com.pk/api/v1/hosted"
}

// postFormToGateway POSTs form data to a payment gateway with a timeout and
// request-scoped context (http.PostForm has neither).
func postFormToGateway(ctx context.Context, apiURL string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return (&http.Client{Timeout: 20 * time.Second}).Do(req)
}

type ChargeRequest struct {
	CustomerID  string `json:"customer_id" binding:"required"`
	StoreID     string `json:"store_id" binding:"required"`
	Gateway     string `json:"gateway" binding:"required"` // "jazzcash" or "easypaisa"
	AmountCents int64  `json:"amount_cents" binding:"required"`
	Nonce       string `json:"nonce" binding:"required"`
}

// Charge handles POST /api/v1/wallet/charge
// Builds a signed payment request and returns the gateway redirect URL.
// JazzCash: POST to https://sandbox.jazzcash.com.pk/CustomerAPI/DOTransaction
// EasyPaisa: POST to https://sandbox.easypaisa.com.pk/api/v1/hosted
func (h *WalletHandler) Charge(c *gin.Context) {
	var req ChargeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Validate gateway
	if req.Gateway != "jazzcash" && req.Gateway != "easypaisa" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gateway must be 'jazzcash' or 'easypaisa'"})
		return
	}

	salt := service.WalletSalt(req.Gateway)
	if salt == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("%s integrity salt is not configured", strings.ToUpper(req.Gateway))})
		return
	}
	merchantID := os.Getenv(strings.ToUpper(req.Gateway) + "_MERCHANT_ID")
	if merchantID == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("%s_MERCHANT_ID env var is required", strings.ToUpper(req.Gateway))})
		return
	}
	password := os.Getenv(strings.ToUpper(req.Gateway) + "_PASSWORD")
	returnURL := os.Getenv("WALLET_RETURN_URL")
	if returnURL == "" {
		returnURL = fmt.Sprintf("%s/api/v1/wallet/callback?gateway=%s", os.Getenv("PUBLIC_BASE_URL"), req.Gateway)
	}
	if returnURL == "" || returnURL == "/api/v1/wallet/callback?gateway="+req.Gateway {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "WALLET_RETURN_URL or PUBLIC_BASE_URL env var is required"})
		return
	}

	// Build signed payment parameters (JazzCash/EasyPaisa canonical format)
	params := map[string]string{
		"pp_MerchantID":  merchantID,
		"pp_Password":    password,
		"pp_TxnRefNo":    req.Nonce,
		"pp_Amount":      fmt.Sprintf("%d", req.AmountCents), // gateway expects paisa (PKR * 100), same as LoadCustomerWallet
		"pp_ReturnURL":   returnURL,
		"pp_CustomerID":  req.CustomerID,
		"pp_StoreID":     req.StoreID,
		"pp_TxnDateTime": time.Now().UTC().Format("20060102150405"),
		"pp_Version":     "1.1",
		"pp_Language":    "EN",
	}

	// Compute HMAC-SHA256 signature over sorted params
	signature := service.ComputeWalletSignature(params, salt)
	params["pp_SecureHash"] = signature

	// POST to gateway
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	resp, err := postFormToGateway(c.Request.Context(), gatewayCheckoutURL(req.Gateway), form)
	if err != nil {
		fmt.Printf("[Wallet Charge] Gateway POST failed: %v\n", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to reach payment gateway"})
		return
	}
	defer resp.Body.Close()

	var gatewayResp struct {
		RedirectURL  string `json:"pp_RedirectURL"`
		ResponseCode string `json:"pp_ResponseCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gatewayResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to parse gateway response"})
		return
	}

	if gatewayResp.RedirectURL == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":         "gateway returned no redirect URL",
			"response_code": gatewayResp.ResponseCode,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":       "pending_redirect",
		"gateway":      req.Gateway,
		"redirect_url": gatewayResp.RedirectURL,
		"amount_cents": req.AmountCents,
	})
}

// Callback handles POST /api/v1/wallet/callback
// Receives the async callback from JazzCash/EasyPaisa after the customer
// completes the payment on the gateway's hosted page.
// Verifies the callback signature using the gateway-specific integrity salt,
// then updates the order and credits the rider wallet on success.
func (h *WalletHandler) Callback(c *gin.Context) {
	// Parse form data (JazzCash/EasyPaisa send form-encoded callbacks)
	if err := c.Request.ParseForm(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid form data"})
		return
	}

	gateway := strings.ToLower(c.Request.FormValue("gateway"))
	if gateway != "jazzcash" && gateway != "easypaisa" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gateway must be 'jazzcash' or 'easypaisa'"})
		return
	}

	integritySalt := service.WalletSalt(gateway)
	if integritySalt == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "gateway integrity salt is not configured"})
		return
	}

	providedHash := strings.ToUpper(c.Request.FormValue("pp_SecureHash"))
	if providedHash == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "missing pp_SecureHash for wallet validation"})
		return
	}

	if !service.VerifyWalletCallback(c.Request.PostForm, integritySalt, providedHash) {
		c.JSON(http.StatusForbidden, gin.H{"error": "invalid HMAC signature - integrity compromised"})
		return
	}

	paymentStatus := c.Request.FormValue("payment_status")
	orderID := c.Request.FormValue("order_id")
	transactionID := c.Request.FormValue("transaction_id")

	fmt.Printf("[Wallet Callback] Validated Signature. Gateway=%s Status=%s Order=%s Txn=%s\n",
		gateway, paymentStatus, orderID, transactionID)

	if paymentStatus != "success" && paymentStatus != "paid" {
		c.JSON(http.StatusOK, gin.H{
			"status":         "verified_unpaid",
			"payment_status": paymentStatus,
			"order_id":       orderID,
			"transaction_id": transactionID,
		})
		return
	}

	// Enrich the callback with gateway and transaction IDs for downstream processing.
	callback := &service.WalletCallback{
		Gateway:       gateway,
		OrderID:       orderID,
		TransactionID: transactionID,
		AmountCents:   parseAmountCents(c.Request.FormValue("amount_cents")),
		Status:        paymentStatus,
	}

	ctx := c.Request.Context()
	if err := h.processWalletCallback(ctx, callback); err != nil {
		fmt.Printf("[Wallet Callback] Processing failed: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "callback verified but processing failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":         "verified",
		"payment_status": paymentStatus,
		"order_id":       orderID,
		"transaction_id": transactionID,
	})
}

func parseAmountCents(s string) int64 {
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}

// emitPaymentCompleted publishes a payments.wallet.completed event so
// downstream services (notification, analytics, settlement workers) can react.
func (h *WalletHandler) emitPaymentCompleted(ctx context.Context, orderID, txnID, gateway string, amountCents int64) {
	if h.kafka == nil {
		return
	}
	event := map[string]interface{}{
		"order_id":       orderID,
		"transaction_id": txnID,
		"gateway":        gateway,
		"amount_cents":   amountCents,
		"status":         "success",
		"timestamp":      time.Now().UnixMilli(),
	}
	eventBytes, _ := json.Marshal(event)
	h.kafka.Client.Produce(ctx, &kgo.Record{
		Topic: "payments.wallet.completed",
		Key:   []byte(orderID),
		Value: eventBytes,
	}, nil)
}

// processWalletCallback updates the order and emits a Kafka event when a verified
// wallet callback arrives.
//
// FIX H8: Creates a payment_transactions record and outbox event so the
// SettlementWorker performs the proper 3-way ledger split
// (admin_commission + vendor_escrow + delivery_escrow). Previously, wallet
// callback orders were marked paid but never split — vendors and riders
// never got paid on JazzCash/EasyPaisa wallet orders.
func (h *WalletHandler) processWalletCallback(ctx context.Context, cb *service.WalletCallback) error {
	if h.orderSvc == nil {
		return fmt.Errorf("order service not configured")
	}

	if err := h.orderSvc.UpdateOrderStatus(ctx, cb.OrderID, "paid"); err != nil {
		return fmt.Errorf("failed to mark order paid: %w", err)
	}

	// FIX H8: Create payment transaction + outbox event for SettlementWorker.
	if h.db != nil {
		// Derive amount from cents (the callback provides amount_cents).
		amountPKR := float64(cb.AmountCents) / 100.0

		// Insert payment transaction (idempotent via ON CONFLICT).
		_, err := h.db.Exec(ctx, `
			INSERT INTO payment_transactions (id, order_tracking_id, gateway, gateway_txn_id, amount, currency, status, kind, idempotency_key, created_at, updated_at)
			VALUES (gen_random_uuid(), $1, $2, $3, $4, 'PKR', 'settlement_pending', 'payment', $5, NOW(), NOW())
			ON CONFLICT (idempotency_key) DO NOTHING
		`, cb.OrderID, cb.Gateway, cb.TransactionID, amountPKR, fmt.Sprintf("wallet:%s", cb.TransactionID))
		if err != nil {
			log.Printf("[WalletCallback] Warning: failed to create payment transaction for order %s: %v", cb.OrderID, err)
			// Non-fatal: order is already marked paid. Settlement may need manual trigger.
		}

		// Create outbox event for SettlementWorker (idempotent via ON CONFLICT).
		eventPayload, _ := json.Marshal(map[string]interface{}{
			"order_id":       cb.OrderID,
			"gateway":        cb.Gateway,
			"gateway_txn_id": cb.TransactionID,
			"amount":         amountPKR,
		})
		_, err = h.db.Exec(ctx, `
			INSERT INTO outbox_events (id, topic, key, payload, status, created_at, updated_at)
			VALUES (gen_random_uuid(), 'payment_settlement', $1, $2, 'PENDING', NOW(), NOW())
			ON CONFLICT (idempotency_key) DO NOTHING
		`, cb.OrderID, eventPayload, fmt.Sprintf("wallet_settle:%s", cb.OrderID))
		if err != nil {
			log.Printf("[WalletCallback] Warning: failed to create outbox event for order %s: %v", cb.OrderID, err)
		}
	}

	h.emitPaymentCompleted(ctx, cb.OrderID, cb.TransactionID, cb.Gateway, cb.AmountCents)

	return nil
}

// GetRiderWallet handles GET /api/v1/wallet/rider/:tracking_id.
// Riders may only view their own wallet; admins may view any wallet.
func (h *WalletHandler) GetRiderWallet(c *gin.Context) {
	if h.riderWallet == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "rider wallet service not configured"})
		return
	}

	targetID := c.Param("tracking_id")
	requesterID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if role != "admin" && requesterID != targetID {
		c.JSON(http.StatusForbidden, gin.H{"error": "can only view your own wallet"})
		return
	}

	wallet, err := h.riderWallet.GetWallet(c.Request.Context(), targetID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, wallet)
}

// DepositCOD handles POST /api/v1/wallet/rider/:tracking_id/deposit.
// Settles rider COD cash collection float and reconciles wallet balance.
func (h *WalletHandler) DepositCOD(c *gin.Context) {
	if h.riderWallet == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "rider wallet service not configured"})
		return
	}

	targetID := c.Param("tracking_id")
	role := middleware.GetRole(c)
	if role != "admin" {
		c.JSON(http.StatusForbidden, gin.H{"error": "only admins can manually clear rider cash float"})
		return
	}

	var req struct {
		Amount float64 `json:"amount" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Cross-check the requested amount against the rider's actual COD float
	// so the response reflects reality instead of echoing the request.
	wallet, err := h.riderWallet.GetWallet(c.Request.Context(), targetID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	const amountEpsilon = 0.01
	if diff := req.Amount - wallet.CashInHand; diff > amountEpsilon || diff < -amountEpsilon {
		c.JSON(http.StatusConflict, gin.H{
			"error":           fmt.Sprintf("requested deposit %.2f does not match rider's COD float %.2f", req.Amount, wallet.CashInHand),
			"actual_cash_in_hand": wallet.CashInHand,
		})
		return
	}

	if err := h.riderWallet.ClearCODCollection(c.Request.Context(), targetID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":          "Deposit successful",
		"amount":           wallet.CashInHand,
		"rider_tracking_id": targetID,
	})
}

// RegisterRoutes registers wallet endpoints on the Gin engine.
// User-facing routes require JWT auth. Payment gateway callbacks are public
// but verify signatures in their respective handlers.
func (h *WalletHandler) RegisterRoutes(router *gin.Engine) {
	wallet := router.Group("/api/v1/wallet")
	{
		// Payment gateway callbacks — public endpoints verified by signature in handler
		wallet.POST("/callback", h.Callback)
		wallet.POST("/customer/load/callback", h.LoadCustomerWalletCallback)
		wallet.POST("/payfast/callback", h.PayFastCallback)

		// Authenticated user routes
		auth := wallet.Group("")
		auth.Use(middleware.JWTAuth())
		{
			auth.POST("/charge", h.Charge)
			auth.POST("/payfast/charge", h.PayFastCharge)

			// Rider earnings wallet
			auth.GET("/rider/:tracking_id", h.GetRiderWallet)
			auth.POST("/rider/:tracking_id/deposit", h.DepositCOD)

			// Customer Wallet
			auth.GET("/customer/:tracking_id", h.GetCustomerWallet)
			auth.POST("/customer/load", h.LoadCustomerWallet)
		}
	}
}

// GetCustomerWallet handles GET /api/v1/wallet/customer/:tracking_id
func (h *WalletHandler) GetCustomerWallet(c *gin.Context) {
	if h.customerWallet == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "customer wallet service not configured"})
		return
	}

	targetID := c.Param("tracking_id")
	requesterID := middleware.GetTrackingID(c)
	if requesterID != targetID {
		c.JSON(http.StatusForbidden, gin.H{"error": "can only view your own wallet"})
		return
	}

	wallet, err := h.customerWallet.GetWallet(c.Request.Context(), targetID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, wallet)
}

// LoadCustomerWallet handles POST /api/v1/wallet/customer/load
// It builds a signed redirect request for PayFast, JazzCash, or EasyPaisa,
// and returns the gateway's hosted checkout URL.
func (h *WalletHandler) LoadCustomerWallet(c *gin.Context) {
	if h.customerWallet == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "customer wallet service not configured"})
		return
	}

	requesterID := middleware.GetTrackingID(c)

	var req struct {
		Amount           float64 `json:"amount"`
		AmountCents      int64   `json:"amount_cents"`
		Gateway          string  `json:"gateway" binding:"required"`
		Currency         string  `json:"currency"`
		CustomerMobileNo string  `json:"customer_mobile_no"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Normalize amount in PKR and paisa
	if req.Amount <= 0 && req.AmountCents > 0 {
		req.Amount = float64(req.AmountCents) / 100.0
	} else if req.Amount > 0 && req.AmountCents <= 0 {
		req.AmountCents = int64(math.Round(req.Amount * 100.0))
	}

	if req.Amount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount must be greater than zero"})
		return
	}

	gateway := strings.ToLower(req.Gateway)
	if gateway != "payfast" && gateway != "jazzcash" && gateway != "easypaisa" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gateway must be 'payfast', 'jazzcash' or 'easypaisa'"})
		return
	}

	txnID := fmt.Sprintf("load_%d", time.Now().UnixNano())
	returnURL := os.Getenv("WALLET_RETURN_URL")
	if returnURL == "" {
		returnURL = fmt.Sprintf("%s/api/v1/wallet/customer/load/callback?gateway=%s", os.Getenv("PUBLIC_BASE_URL"), gateway)
	}

	// Persist the top-up intent BEFORE redirecting. The callback later
	// validates against this record — never against unsigned form fields
	// like customer_tracking_id / txnamt.
	if h.redis == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "top-up verification store unavailable"})
		return
	}
	if err := h.storePendingWalletLoad(c.Request.Context(), txnID, pendingWalletLoad{
		CustomerTrackingID: requesterID,
		Gateway:            gateway,
		AmountPKR:          req.Amount,
		AmountCents:        req.AmountCents,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record pending top-up: " + err.Error()})
		return
	}

	if gateway == "payfast" {
		merchantID := os.Getenv("PAYFAST_MERCHANT_ID")
		baseURL := os.Getenv("PAYFAST_BASE_URL")
		if baseURL == "" {
			baseURL = os.Getenv("PAYFAST_API_URL")
		}
		if merchantID == "" || baseURL == "" {
			// Never fall back to a hard-coded sandbox URL here: charging production
			// customers against the sandbox gateway would silently fail every payment.
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PayFast is not configured on the server"})
			return
		}
		if returnURL == "" || returnURL == "/api/v1/wallet/customer/load/callback?gateway=payfast" {
			// Build from the deployment's own public base URL — never assume a
			// hard-coded domain in code.
			publicBase := strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL"))
			if publicBase == "" {
				log.Printf("[wallet-load] misconfiguration: WALLET_RETURN_URL or PUBLIC_BASE_URL must be set to build the payfast return_url")
				c.JSON(http.StatusInternalServerError, gin.H{"error": "return URL is not configured on the server"})
				return
			}
			returnURL = fmt.Sprintf("%s/api/v1/wallet/customer/load/callback?gateway=payfast", publicBase)
		}

		var hostedURL string
		if strings.Contains(baseURL, "apps.net.pk") {
			formEndpoint := strings.TrimRight(baseURL, "/")
			if !strings.HasSuffix(formEndpoint, "/PostTransaction") {
				if strings.HasSuffix(formEndpoint, "/Transaction") {
					formEndpoint += "/PostTransaction"
				} else {
					formEndpoint += "/Transaction/PostTransaction"
				}
			}
			hostedURL = fmt.Sprintf("%s?merchant_id=%s&basket_id=%s&txnamt=%.2f&currency_code=PKR&success_url=%s&checkout_url=%s",
				formEndpoint, url.QueryEscape(merchantID), url.QueryEscape(txnID), req.Amount, url.QueryEscape(returnURL), url.QueryEscape(returnURL))
		} else {
			hostedURL = fmt.Sprintf("%s/hosted?merchant_id=%s&basket_id=%s&txnamt=%.2f&currency_code=PKR&success_url=%s&checkout_url=%s",
				strings.TrimRight(baseURL, "/"), url.QueryEscape(merchantID), url.QueryEscape(txnID), req.Amount, url.QueryEscape(returnURL), url.QueryEscape(returnURL))
		}
		if mobileNo := strings.TrimSpace(req.CustomerMobileNo); mobileNo != "" {
			hostedURL += "&customer_mobile_no=" + url.QueryEscape(mobileNo)
		}

		c.JSON(http.StatusOK, gin.H{
			"status":         "pending_redirect",
			"gateway":        gateway,
			"redirect_url":   hostedURL,
			"tracking_id":    requesterID,
			"amount":         req.Amount,
			"amount_cents":   req.AmountCents,
			"transaction_id": txnID,
		})
		return
	}

	salt := service.WalletSalt(gateway)
	if salt == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("%s integrity salt is not configured", strings.ToUpper(gateway))})
		return
	}
	merchantID := os.Getenv(strings.ToUpper(gateway) + "_MERCHANT_ID")
	if merchantID == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("%s_MERCHANT_ID env var is required", strings.ToUpper(gateway))})
		return
	}
	password := os.Getenv(strings.ToUpper(gateway) + "_PASSWORD")
	if returnURL == "" || returnURL == "/api/v1/wallet/customer/load/callback?gateway="+gateway {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "WALLET_RETURN_URL or PUBLIC_BASE_URL env var is required"})
		return
	}

	params := map[string]string{
		"pp_MerchantID":  merchantID,
		"pp_Password":    password,
		"pp_TxnRefNo":    txnID,
		"pp_Amount":      fmt.Sprintf("%d", req.AmountCents), // gateway expects paisa
		"pp_ReturnURL":   returnURL,
		"pp_CustomerID":  requesterID,
		"pp_TxnDateTime": time.Now().UTC().Format("20060102150405"),
		"pp_Version":     "1.1",
		"pp_Language":    "EN",
	}
	signature := service.ComputeWalletSignature(params, salt)
	params["pp_SecureHash"] = signature

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	resp, err := postFormToGateway(c.Request.Context(), gatewayCheckoutURL(gateway), form)
	if err != nil {
		fmt.Printf("[Wallet Load] Gateway POST failed: %v\n", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to reach payment gateway"})
		return
	}
	defer resp.Body.Close()

	var gatewayResp struct {
		RedirectURL  string `json:"pp_RedirectURL"`
		ResponseCode string `json:"pp_ResponseCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gatewayResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to parse gateway response"})
		return
	}

	if gatewayResp.RedirectURL == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":         "gateway returned no redirect URL",
			"response_code": gatewayResp.ResponseCode,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":         "pending_redirect",
		"gateway":        gateway,
		"redirect_url":   gatewayResp.RedirectURL,
		"tracking_id":    requesterID,
		"amount":         req.Amount,
		"amount_cents":   req.AmountCents,
		"transaction_id": txnID,
	})
}

// LoadCustomerWalletCallback handles POST/GET /api/v1/wallet/customer/load/callback
// Verifies cryptographic signature before crediting stored value accounts.
func (h *WalletHandler) LoadCustomerWalletCallback(c *gin.Context) {
	if h.customerWallet == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "customer wallet service not configured"})
		return
	}

	if err := c.Request.ParseForm(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid form data"})
		return
	}

	gateway := strings.ToLower(c.Query("gateway"))
	if gateway == "" {
		gateway = strings.ToLower(c.Request.FormValue("gateway"))
	}

	var customerTrackingID string
	var amountPKR float64
	var txnID string

	if gateway == "payfast" {
		basketID := c.Request.FormValue("basket_id")
		if basketID == "" {
			basketID = c.Request.FormValue("Basket_ID")
		}
		statusCode := c.Request.FormValue("status_code")
		if statusCode == "" {
			statusCode = c.Request.FormValue("err_code")
		}
		receivedHash := c.Request.FormValue("secured_hash")
		if receivedHash == "" {
			receivedHash = c.Request.FormValue("validation_hash")
		}

		if basketID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing basket_id in payfast callback"})
			return
		}

		// Enforce mandatory signature validation for PayFast callbacks
		securedKey := os.Getenv("PAYFAST_SECURED_KEY")
		merchantID := os.Getenv("PAYFAST_MERCHANT_ID")
		if securedKey == "" || merchantID == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "payment gateway configuration missing"})
			return
		}
		if receivedHash == "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "missing signature hash in PayFast callback"})
			return
		}
		expectedHash := payfast.CalculateResponseValidationHash(basketID, securedKey, merchantID, statusCode)
		if !payfast.VerifyResponseHash(expectedHash, receivedHash) {
			c.JSON(http.StatusForbidden, gin.H{"error": "invalid PayFast signature"})
			return
		}

		if statusCode != "" && !payfast.IsSuccessCode(statusCode) {
			c.JSON(http.StatusOK, gin.H{"status": "failed", "message": "payment unsuccessful at gateway"})
			return
		}

		// Bind the callback to the pending top-up created at initiation time.
		// The PayFast response hash only covers basket_id + status code, so
		// customer_tracking_id / txnamt from the form are NOT trustworthy —
		// we use the values we recorded when the flow started.
		// Parse the reported amount purely to cross-check against what was
		// initiated; the credited value always comes from the pending record.
		fmt.Sscanf(c.Request.FormValue("txnamt"), "%f", &amountPKR)
		pending, err := h.consumePendingWalletLoad(c.Request.Context(), basketID)
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "no matching pending top-up for this transaction (replay or tampered callback)"})
			return
		}
		if amountPKR > 0 && (amountPKR-pending.AmountPKR > 0.01 || pending.AmountPKR-amountPKR > 0.01) {
			c.JSON(http.StatusForbidden, gin.H{"error": "callback amount does not match initiated top-up"})
			return
		}
		customerTrackingID = pending.CustomerTrackingID
		txnID = basketID
		amountPKR = pending.AmountPKR
	} else if gateway == "jazzcash" || gateway == "easypaisa" {
		integritySalt := service.WalletSalt(gateway)
		if integritySalt == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gateway integrity salt is not configured"})
			return
		}
		providedHash := strings.ToUpper(c.Request.FormValue("pp_SecureHash"))
		if providedHash == "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "missing pp_SecureHash for wallet validation"})
			return
		}

		if !service.VerifyWalletCallback(c.Request.PostForm, integritySalt, providedHash) {
			c.JSON(http.StatusForbidden, gin.H{"error": "invalid HMAC signature - integrity compromised"})
			return
		}

		customerTrackingID = c.Request.FormValue("pp_CustomerID")
		if customerTrackingID == "" {
			customerTrackingID = c.Request.FormValue("customer_tracking_id")
		}
		txnRef := c.Request.FormValue("pp_TxnRefNo")
		if txnRef == "" {
			txnRef = c.Request.FormValue("transaction_id")
		}
		var amountPaisa int64
		fmt.Sscanf(c.Request.FormValue("pp_Amount"), "%d", &amountPaisa)

		// Bind the verified callback to the initiated top-up: the customer
		// and amount must match the pending record created at load time.
		pending, err := h.consumePendingWalletLoad(c.Request.Context(), txnRef)
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "no matching pending top-up for this transaction (replay or tampered callback)"})
			return
		}
		if amountPaisa > 0 && pending.AmountCents > 0 && amountPaisa != pending.AmountCents {
			c.JSON(http.StatusForbidden, gin.H{"error": "callback amount does not match initiated top-up"})
			return
		}
		customerTrackingID = pending.CustomerTrackingID
		txnID = txnRef
		amountPKR = pending.AmountPKR
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported or missing gateway"})
		return
	}

	if customerTrackingID == "" || amountPKR <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid customer_tracking_id or amount in verified callback"})
		return
	}

	amountPaisa := int64(amountPKR * 100)
	err := h.customerWallet.CreditFunds(c.Request.Context(), customerTrackingID, txnID, amountPaisa)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to credit customer wallet: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Wallet topped up successfully", "amount": amountPKR, "amount_paisa": amountPaisa})
}
