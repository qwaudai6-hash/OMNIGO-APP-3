package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/ledger"
	sharedAuth "github.com/omnigo/backend/internal/shared/auth"
	"github.com/omnigo/backend/internal/shared/messaging"
	"github.com/omnigo/backend/internal/wallet/service"
	"github.com/twmb/franz-go/pkg/kgo"

	orderModels "github.com/omnigo/backend/internal/order/models"
)

type WalletHandler struct {
	riderWallet    *service.RiderWalletService
	customerWallet *service.CustomerWalletService
	orderSvc       OrderStatusUpdater
	kafka          *messaging.KafkaClient
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
		"pp_Amount":      fmt.Sprintf("%d", req.AmountCents*100), // gateway expects amount in paisa
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

	// Determine gateway API endpoint
	var apiURL string
	if req.Gateway == "jazzcash" {
		apiURL = "https://sandbox.jazzcash.com.pk/CustomerAPI/DOTransaction"
	} else {
		apiURL = "https://sandbox.easypaisa.com.pk/api/v1/hosted"
	}

	// POST to gateway
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	resp, err := http.PostForm(apiURL, form)
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

// processWalletCallback updates the order and emits a Kafka event when a verified
// wallet callback arrives.
//
// IMPORTANT: This function NO LONGER credits the rider wallet directly.
// The old code passed order.TotalAmount as rider earning with 0 commission,
// which gave the rider 100% of the order value and zero revenue to the platform.
//
// Payment splits are now handled exclusively by the Payment Orchestrator service
// (port 9006) via its Stripe/PayFast split handlers. This callback only marks
// the order as paid and emits an event for downstream processing.
func (h *WalletHandler) processWalletCallback(ctx context.Context, cb *service.WalletCallback) error {
	if h.orderSvc == nil {
		return fmt.Errorf("order service not configured")
	}

	if err := h.orderSvc.UpdateOrderStatus(ctx, cb.OrderID, "paid"); err != nil {
		return fmt.Errorf("failed to mark order paid: %w", err)
	}

	if h.kafka != nil {
		event := map[string]interface{}{
			"order_id":       cb.OrderID,
			"transaction_id": cb.TransactionID,
			"gateway":        cb.Gateway,
			"amount_cents":   cb.AmountCents,
			"status":         cb.Status,
			"timestamp":      time.Now().UnixMilli(),
		}
		eventBytes, _ := json.Marshal(event)
		record := &kgo.Record{
			Topic: "payments.wallet.completed",
			Key:   []byte(cb.OrderID),
			Value: eventBytes,
		}
		h.kafka.Client.Produce(ctx, record, nil)
	}

	// DEPRECATED: Rider wallet credit is now handled by the Payment Orchestrator.
	// The old code at this location passed order.TotalAmount (the entire order value)
	// as rider earning with 0 commission, resulting in:
	// - Rider getting 100% of order value
	// - Platform getting 0% revenue
	// - No payment split, no escrow, no ledger entry
	//
	// The Payment Orchestrator (port 9006) handles all splits via:
	// - StripeSplitHandler for card payments
	// - PayFastSplitHandler for PayFast payments
	// - CODHandler for cash on delivery

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
	requesterID, role, err := sharedAuth.ParseJWTFromHeader(c.GetHeader("Authorization"))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
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
	_, role, err := sharedAuth.ParseJWTFromHeader(c.GetHeader("Authorization"))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
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

	if err := h.riderWallet.ClearCODCollection(c.Request.Context(), targetID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Deposit successful",
		"amount":  req.Amount,
	})
}

// RegisterRoutes registers wallet endpoints on the Gin engine
func (h *WalletHandler) RegisterRoutes(router *gin.Engine) {
	wallet := router.Group("/api/v1/wallet")
	{
		wallet.POST("/charge", h.Charge)
		wallet.POST("/callback", h.Callback)

		// Rider earnings wallet
		wallet.GET("/rider/:tracking_id", h.GetRiderWallet)
		wallet.POST("/rider/:tracking_id/deposit", h.DepositCOD)

		// Customer Wallet
		wallet.GET("/customer/:tracking_id", h.GetCustomerWallet)
		wallet.POST("/customer/load", h.LoadCustomerWallet)
		wallet.POST("/customer/load/callback", h.LoadCustomerWalletCallback)
	}
}

// GetCustomerWallet handles GET /api/v1/wallet/customer/:tracking_id
func (h *WalletHandler) GetCustomerWallet(c *gin.Context) {
	if h.customerWallet == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "customer wallet service not configured"})
		return
	}

	targetID := c.Param("tracking_id")
	requesterID, _, err := sharedAuth.ParseJWTFromHeader(c.GetHeader("Authorization"))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
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
// It builds a real JazzCash/EasyPaisa signed redirect request, posts it to the
// gateway, and returns the gateway's hosted checkout URL.
func (h *WalletHandler) LoadCustomerWallet(c *gin.Context) {
	if h.customerWallet == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "customer wallet service not configured"})
		return
	}

	requesterID, _, err := sharedAuth.ParseJWTFromHeader(c.GetHeader("Authorization"))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var req struct {
		AmountCents int64  `json:"amount_cents" binding:"required"`
		Gateway     string `json:"gateway" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	gateway := strings.ToLower(req.Gateway)
	if gateway != "jazzcash" && gateway != "easypaisa" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gateway must be 'jazzcash' or 'easypaisa'"})
		return
	}

	salt := service.WalletSalt(gateway)
	merchantID := os.Getenv(strings.ToUpper(gateway) + "_MERCHANT_ID")
	if merchantID == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("%s_MERCHANT_ID env var is required", strings.ToUpper(gateway))})
		return
	}
	password := os.Getenv(strings.ToUpper(gateway) + "_PASSWORD")
	returnURL := os.Getenv("WALLET_RETURN_URL")
	if returnURL == "" {
		returnURL = fmt.Sprintf("%s/api/v1/wallet/customer/load/callback?gateway=%s", os.Getenv("PUBLIC_BASE_URL"), gateway)
	}
	if returnURL == "" || returnURL == "/api/v1/wallet/customer/load/callback?gateway="+gateway {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "WALLET_RETURN_URL or PUBLIC_BASE_URL env var is required"})
		return
	}

	txnID := fmt.Sprintf("load_%d", time.Now().UnixNano())
	params := map[string]string{
		"pp_MerchantID":  merchantID,
		"pp_Password":    password,
		"pp_TxnRefNo":    txnID,
		"pp_Amount":      fmt.Sprintf("%d", req.AmountCents*100), // gateway expects paisa
		"pp_ReturnURL":   returnURL,
		"pp_CustomerID":  requesterID,
		"pp_TxnDateTime": time.Now().UTC().Format("20060102150405"),
		"pp_Version":     "1.1",
		"pp_Language":    "EN",
	}
	signature := service.ComputeWalletSignature(params, salt)
	params["pp_SecureHash"] = signature

	var apiURL string
	if gateway == "jazzcash" {
		apiURL = "https://sandbox.jazzcash.com.pk/CustomerAPI/DOTransaction"
	} else {
		apiURL = "https://sandbox.easypaisa.com.pk/api/v1/hosted"
	}

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	resp, err := http.PostForm(apiURL, form)
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
		"amount_cents":   req.AmountCents,
		"transaction_id": txnID,
	})
}

// LoadCustomerWalletCallback handles POST /api/v1/wallet/customer/load/callback
func (h *WalletHandler) LoadCustomerWalletCallback(c *gin.Context) {
	if h.customerWallet == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "customer wallet service not configured"})
		return
	}

	customerTrackingID := c.PostForm("customer_tracking_id")
	var amountCents int64
	fmt.Sscanf(c.PostForm("amount_cents"), "%d", &amountCents)
	amountPKR := float64(amountCents) / 100.0

	err := h.customerWallet.CreditFunds(c.Request.Context(), customerTrackingID, "load_"+c.PostForm("transaction_id"), amountPKR, ledger.AccountGatewayClearing, "Wallet Top-up via Gateway")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to top-up wallet: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Wallet topped up successfully"})
}
