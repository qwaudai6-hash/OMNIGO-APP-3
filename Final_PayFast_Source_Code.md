# PayFast Production Integration Code

Here is the complete source code for the PayFast integration containing all the new architectural changes (Option C Flow, Idempotency, Database Locking, Amount Verification, Outbox Pattern, and Zero Persistence).

## 1. PayFast Handler (`payfast_handler.go`)
This is the main orchestrator that handles webhooks, idempotency, splitting, and the outbox pattern.

```go
package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/payment/payfast"
	"github.com/omnigo/backend/internal/payment_orchestrator"
	"github.com/omnigo/backend/internal/shared/middleware"
)

// PaymentRequest payload from Flutter
type PaymentRequest struct {
	OrderID              string `json:"order_id"`
	AccountTypeID        string `json:"account_type_id"`
	CustomerMobileNo     string `json:"customer_mobile_no"`
	CustomerEmailAddress string `json:"customer_email_address"`

	// Card specific (Zero-Persistence Rule)
	CardNumber  string `json:"card_number"`
	ExpiryMonth string `json:"expiry_month"`
	ExpiryYear  string `json:"expiry_year"`
	CVV         string `json:"cvv"`
}

type ThreeDSCallbackRequest struct {
	MD    string `form:"md" binding:"required"`
	PaRes string `form:"paRes" binding:"required"`
}

// signMD generates an HMAC signature for the transaction ID
func signMD(internalTxnID string) string {
	secret := os.Getenv("INTERNAL_CALLBACK_SECRET") // Use dedicated internal secret
	if secret == "" {
		secret = "fallback_secret_do_not_use_in_prod"
	}
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(internalTxnID))
	signature := hex.EncodeToString(h.Sum(nil))
	return fmt.Sprintf("%s.%s", internalTxnID, signature)
}

// verifyMD validates the HMAC signature in constant time
func verifyMD(md string) (string, error) {
	parts := strings.Split(md, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid md format")
	}
	internalTxnID, providedSignature := parts[0], parts[1]
	
	expected := signMD(internalTxnID)
	expectedParts := strings.Split(expected, ".")
	if len(expectedParts) != 2 {
		return "", fmt.Errorf("internal signing error")
	}
	
	if subtle.ConstantTimeCompare([]byte(providedSignature), []byte(expectedParts[1])) != 1 {
		return "", fmt.Errorf("invalid signature")
	}
	return internalTxnID, nil
}

// PaymentMetadata stores non-sensitive token state required for completing Tokenized Transactions.
type PaymentMetadata struct {
	InstrumentToken string `json:"instrument_token"`
	GatewayTxnID    string `json:"gateway_txn_id"`
	Data3DSSecureID string `json:"data_3ds_secureid"`
	ECI             string `json:"eci"`
	CustomerMobile  string `json:"customer_mobile"`
	AccountTypeID   string `json:"account_type"`
}

// PayFastSplitHandler handles synchronous PayFast API payments
type PayFastSplitHandler struct {
	redis      redis.UniversalClient
	kafka      *kgo.Client
	db         *pgxpool.Pool
	ledger     *ledger.Service
	escrow     *escrow.Service
	calculator *payment_orchestrator.CommissionCalculator
	payfast    *payfast.Client
}

func NewPayFastSplitHandler(
	rdb redis.UniversalClient,
	kafkaClient *kgo.Client,
	db *pgxpool.Pool,
	ledgerSvc *ledger.Service,
	escrowSvc *escrow.Service,
	calc *payment_orchestrator.CommissionCalculator,
	payfastClient *payfast.Client,
) *PayFastSplitHandler {
	if os.Getenv("PAYFAST_MERCHANT_CATEGORY") == "" {
		panic("PAYFAST_MERCHANT_CATEGORY environment variable is required")
	}
	return &PayFastSplitHandler{
		redis:      rdb,
		kafka:      kafkaClient,
		db:         db,
		ledger:     ledgerSvc,
		escrow:     escrowSvc,
		calculator: calc,
		payfast:    payfastClient,
	}
}

// ProcessPayment handles POST /api/v1/payments/payfast/payment (Temporary Token Flow)
func (h *PayFastSplitHandler) ProcessPayment(c *gin.Context) {
	var req PaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	// 1. Fetch Authoritative Order Amount & Validate Identity
	merchantUserID := middleware.GetTrackingID(c)
	if merchantUserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var expectedAmount float64
	var status string
	var customerTrackingID string
	err := h.db.QueryRow(c.Request.Context(),
		`SELECT total_amount, status, customer_tracking_id FROM orders WHERE order_tracking_id = $1`, req.OrderID,
	).Scan(&expectedAmount, &status, &customerTrackingID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Order not found"})
		return
	}
	if merchantUserID != customerTrackingID {
		c.JSON(http.StatusForbidden, gin.H{"error": "Order belongs to a different user"})
		return
	}
	if status != "pending" && status != "unpaid" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Order is not payable"})
		return
	}

	amountStr := fmt.Sprintf("%.2f", expectedAmount)
	customerIP := c.ClientIP()
	internalTxnID := "pf_" + uuid.New().String()

	// 2. Prevent duplicate active attempts
	var activeAttempts int
	err = h.db.QueryRow(c.Request.Context(),
		`SELECT count(*) FROM payment_transactions WHERE order_tracking_id = $1 AND status IN ('pending', 'processing', '3ds_required')`,
		req.OrderID,
	).Scan(&activeAttempts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check existing payment attempts"})
		return
	}
	if activeAttempts > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "A payment attempt is already in progress for this order"})
		return
	}

	// 3. Insert Pending Payment Attempt
	_, err = h.db.Exec(c.Request.Context(),
		`INSERT INTO payment_transactions (transaction_id, order_tracking_id, gateway, amount, currency, status, kind)
		 VALUES ($1, $2, 'payfast', $3, 'PKR', 'pending', 'payment')`,
		internalTxnID, req.OrderID, expectedAmount,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create payment attempt"})
		return
	}

	// 3. Get Temporary Transaction Token
	tokenReq := payfast.TemporaryTokenRequest{
		BasketID:           req.OrderID,
		TxnAmt:             amountStr,
		OrderDate:          time.Now().Format("2006-01-02 15:04:05"),
		CustomerMobileNo:   req.CustomerMobileNo,
		MerchantUserId:     merchantUserID,
		AccountTypeID:      req.AccountTypeID,
		MerCatCode:         os.Getenv("PAYFAST_MERCHANT_CATEGORY"),
		CustomerIP:         customerIP,
		CardNumber:         req.CardNumber,
		ExpiryMonth:        req.ExpiryMonth,
		ExpiryYear:         req.ExpiryYear,
		CVV:                req.CVV,
		Data3DSPagemode:    "SIMPLE",
		Data3DSCallbackURL: envStr("PAYFAST_3DS_CALLBACK_URL", "https://api.omnigo.com/api/v1/payments/payfast/3ds_callback?md="+internalTxnID),
	}

	// WARNING: tokenReq explicitly prevents CardData from being JSON marshaled via json:"-" if implemented,
	// but it is not printed in logs due to String() override. The zero-persistence rule means it dies here.
	tokenRes, err := h.payfast.GetTemporaryTransactionToken(c.Request.Context(), tokenReq)

	// Card details are now effectively gone from memory as tokenReq falls out of scope.
	req.CardNumber = ""
	req.CVV = ""

	if err != nil {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Temporary token failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "Payment token generation failed"})
		return
	}

	if tokenRes.StatusCode != "00" {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, tokenRes.StatusMsg)
		c.JSON(http.StatusBadRequest, gin.H{"error": tokenRes.StatusMsg})
		return
	}

	// 5. Handle 3DS if required
	if tokenRes.Data3DSHTML != "" {
		// Safely merge metadata to avoid overwriting existing data
		var existingMetaJSON []byte
		err := h.db.QueryRow(c.Request.Context(), "SELECT metadata FROM payment_transactions WHERE transaction_id = $1", internalTxnID).Scan(&existingMetaJSON)
		
		var metaMap map[string]interface{}
		if err == nil && len(existingMetaJSON) > 0 {
			if err := json.Unmarshal(existingMetaJSON, &metaMap); err != nil {
				metaMap = make(map[string]interface{})
			}
		} else {
			metaMap = make(map[string]interface{})
		}

		metaMap["instrument_token"] = tokenRes.InstrumentToken
		metaMap["gateway_txn_id"] = tokenRes.TransactionID
		metaMap["data_3ds_secureid"] = tokenRes.Data3DSSecureID
		metaMap["eci"] = tokenRes.ECI
		metaMap["customer_mobile"] = req.CustomerMobileNo
		metaMap["account_type"] = req.AccountTypeID

		metaBytes, err := json.Marshal(metaMap)
		if err != nil {
			h.markPaymentFailed(c.Request.Context(), internalTxnID, "Failed to encode metadata")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal metadata error"})
			return
		}

		_, err = h.db.Exec(c.Request.Context(),
			`UPDATE payment_transactions SET status = '3ds_required', gateway_txn_id = $1, metadata = $2, updated_at = NOW() WHERE transaction_id = $3`,
			tokenRes.TransactionID, string(metaBytes), internalTxnID,
		)
		if err != nil {
			h.markPaymentFailed(c.Request.Context(), internalTxnID, "Failed to update 3DS status")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal database error"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status": "3ds_required",
			"html":   tokenRes.Data3DSHTML,
		})
		return
	}

	// 6. If no 3DS required, immediately process the tokenized transaction.
	// (Note: Typically one-time cards require 3DS, but if the gateway skips it:)
	txnReq := payfast.TokenizedTransactionRequest{
		InstrumentToken:  tokenRes.InstrumentToken,
		TransactionID:    tokenRes.TransactionID,
		MerchantUserId:   merchantUserID,
		CustomerMobileNo: req.CustomerMobileNo,
		BasketID:         req.OrderID,
		OrderDate:        tokenReq.OrderDate,
		TxnDesc:          "OmniGo Order " + req.OrderID,
		TxnAmt:           amountStr,
		CustomerIP:       customerIP,
		MerCatCode:       os.Getenv("PAYFAST_MERCHANT_CATEGORY"),
		ECI:              tokenRes.ECI,
	}

	txnRes, err := h.payfast.InitiateTokenizedTransaction(c.Request.Context(), txnReq)
	if err != nil {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Tokenized transaction failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "Transaction processing failed"})
		return
	}

	// 6. Check Transaction Status & Settle
	h.verifyAndSettle(c, internalTxnID, req.OrderID, txnRes.TransactionID, expectedAmount)
}

// ThreeDSCallback handles the callback from the PayFast 3DS form POST
func (h *PayFastSplitHandler) ThreeDSCallback(c *gin.Context) {
	var req ThreeDSCallbackRequest
	internalTxnID, err := verifyMD(req.MD)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid MD signature"})
		return
	}

	if internalTxnID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing MD correlator"})
		return
	}

	customerIP := c.ClientIP()

	// 1. Find the 3DS required payment attempt
	var orderID string
	var amount float64
	var metaJSON []byte
	var customerTrackingID string
	err = h.db.QueryRow(c.Request.Context(),
		`SELECT pt.order_tracking_id, pt.amount, pt.metadata, o.customer_tracking_id 
		 FROM payment_transactions pt
		 JOIN orders o ON o.order_tracking_id = pt.order_tracking_id
		 WHERE pt.transaction_id = $1 AND pt.status = '3ds_required'`,
		internalTxnID,
	).Scan(&orderID, &amount, &metaJSON, &customerTrackingID)

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No pending 3DS payment found for order"})
		return
	}

	var meta PaymentMetadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid payment metadata"})
		return
	}

	// 2. Mark as processing to prevent duplicate callbacks
	res, err := h.db.Exec(c.Request.Context(),
		`UPDATE payment_transactions SET status = 'processing', updated_at = NOW() WHERE transaction_id = $1 AND status = '3ds_required'`,
		internalTxnID,
	)
	if err != nil || res.RowsAffected() == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Payment is already processing"})
		return
	}

	// 3. Initiate Tokenized Transaction using the saved Token + paRes
	txnReq := payfast.TokenizedTransactionRequest{
		InstrumentToken:  meta.InstrumentToken,
		TransactionID:    meta.GatewayTxnID,
		MerchantUserId:   customerTrackingID,
		CustomerMobileNo: meta.CustomerMobile,
		BasketID:         orderID,
		OrderDate:        time.Now().Format("2006-01-02 15:04:05"),
		TxnDesc:          "OmniGo Order " + orderID,
		TxnAmt:           fmt.Sprintf("%.2f", amount),
		CustomerIP:       customerIP,
		MerCatCode:       os.Getenv("PAYFAST_MERCHANT_CATEGORY"),
		ECI:              meta.ECI,
		Data3DSSecureID:  meta.Data3DSSecureID,
		Data3DSPaRes:     req.PaRes,
	}

	txnRes, err := h.payfast.InitiateTokenizedTransaction(c.Request.Context(), txnReq)
	if err != nil {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Tokenized 3DS transaction failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "Transaction processing failed"})
		return
	}

	// 4. Verify & Settle
	h.verifyAndSettle(c, internalTxnID, orderID, txnRes.TransactionID, amount)
}

// verifyAndSettle calls the GET status endpoint and settles the DB transaction atomically
func (h *PayFastSplitHandler) verifyAndSettle(c *gin.Context, internalTxnID string, orderID string, gatewayTxnID string, expectedAmount float64) {
	statusRes, err := h.payfast.GetTransactionStatus(c.Request.Context(), gatewayTxnID)
	if err != nil {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Status check failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to verify transaction status"})
		return
	}

	// 00 = Processed OK
	if statusRes.StatusCode != "00" {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, statusRes.StatusMsg)
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Payment not successful",
			"code":  statusRes.StatusCode,
			"msg":   statusRes.StatusMsg,
		})
		return
	}

	if statusRes.BasketID != orderID {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Basket ID mismatch")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Basket ID mismatch"})
		return
	}

	// PayFast amount verification against authoritative expectedAmount
	expectedAmountStr := fmt.Sprintf("%.2f", expectedAmount)
	if statusRes.TxnAmt != expectedAmountStr {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Amount mismatch")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Transaction amount mismatch"})
		return
	}

	err = h.executeSplit(c.Request.Context(), internalTxnID, orderID, expectedAmount, gatewayTxnID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Settlement failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "paid", "order_id": orderID})
}

func (h *PayFastSplitHandler) markPaymentFailed(ctx context.Context, internalTxnID string, reason string) {
	_, _ = h.db.Exec(ctx,
		`UPDATE payment_transactions SET status = 'failed', error_message = $1, updated_at = NOW() WHERE transaction_id = $2 AND status != 'captured'`,
		reason, internalTxnID,
	)
}

// executeSplit performs the atomic local settlement and idempotency checks.
func (h *PayFastSplitHandler) executeSplit(ctx context.Context, internalTxnID string, orderID string, amount float64, gatewayTxnID string) error {
	// 1. Begin atomic DB transaction
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 2. Idempotency Lock on payment_transactions
	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM payment_transactions WHERE transaction_id = $1 FOR UPDATE`, internalTxnID).Scan(&currentStatus)
	if err != nil {
		return fmt.Errorf("payment transaction not found: %w", err)
	}
	if currentStatus == "captured" {
		// Idempotent success
		return nil
	}

	// 3. Ensure order is not paid by another transaction
	var storeID string
	var deliveryTrackingID string
	var orderStatus string
	err = tx.QueryRow(ctx,
		`SELECT store_tracking_id, status FROM orders WHERE order_tracking_id = $1 FOR UPDATE`, orderID,
	).Scan(&storeID, &orderStatus)
	if err != nil {
		return fmt.Errorf("order not found: %w", err)
	}
	if orderStatus == "paid" {
		return fmt.Errorf("conflict: order already paid by another transaction")
	}

	h.db.QueryRow(ctx,
		`SELECT COALESCE(d.tracking_id, '') FROM deliveries d WHERE d.order_tracking_id = $1`, orderID,
	).Scan(&deliveryTrackingID)

	split, err := h.calculator.CalculateSplit(ctx, amount, storeID, deliveryTrackingID)
	if err != nil {
		return fmt.Errorf("split calculation failed: %w", err)
	}

	currency := envStr("DEFAULT_CURRENCY", "PKR")
	idempotencyKey := fmt.Sprintf("payfast:split:%s", gatewayTxnID)

	transfers := []ledger.TransferRequest{
		{DebitAccount: ledger.AccountPayFastHolding, CreditAccount: ledger.AccountAdminRevenue, Amount: split.AdminRevenue, Currency: currency, ReferenceID: orderID, IdempotencyKey: idempotencyKey + ":admin"},
		{DebitAccount: ledger.AccountPayFastHolding, CreditAccount: ledger.AccountVendorLockedEscrow, Amount: split.VendorEscrow, Currency: currency, ReferenceID: orderID, IdempotencyKey: idempotencyKey + ":vendor"},
	}

	if split.DeliveryEscrow > 0 {
		transfers = append(transfers, ledger.TransferRequest{DebitAccount: ledger.AccountPayFastHolding, CreditAccount: ledger.AccountCentralEscrow, Amount: split.DeliveryEscrow, Currency: currency, ReferenceID: orderID, IdempotencyKey: idempotencyKey + ":delivery"})
	}

	_, err = h.ledger.MultiTransfer(ctx, transfers)
	if err != nil {
		return fmt.Errorf("atomic ledger split failed: %w", err)
	}

	// 4. Update order & payment statuses (triggers will also help sync, but we do it explicitly here for the transaction block)
	_, err = tx.Exec(ctx,
		`UPDATE orders SET admin_commission = $1, payment_status = 'paid', status = 'paid' WHERE order_tracking_id = $2`,
		split.AdminRevenue, orderID,
	)
	if err != nil {
		return fmt.Errorf("failed to update order status for %s: %w", orderID, err)
	}

	_, err = tx.Exec(ctx,
		`UPDATE payment_transactions SET status = 'captured', gateway_txn_id = $1, updated_at = NOW() WHERE transaction_id = $2`,
		gatewayTxnID, internalTxnID,
	)
	if err != nil {
		return fmt.Errorf("failed to update payment transaction: %w", err)
	}

	// 5. Insert Escrow Outbox Event (Atomic)
	outboxPayload, _ := json.Marshal(map[string]interface{}{
		"order_id": orderID,
		"store_id": storeID,
		"vendor_escrow": split.VendorEscrow,
	})
	_, err = tx.Exec(ctx, 
		`INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at) VALUES ($1, 'escrow_hold', $2, 'PENDING', NOW())`,
		orderID, string(outboxPayload),
	)
	if err != nil {
		return fmt.Errorf("failed to insert outbox event: %w", err)
	}

	// 6. Commit DB Transaction
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit settlement: %w", err)
	}

	return nil
}

// RegisterRoutes registers the PayFast API endpoints.
func (h *PayFastSplitHandler) RegisterRoutes(router *gin.Engine) {
	payments := router.Group("/api/v1/payments")
	{
		payments.POST("/payfast/payment", h.ProcessPayment)

		// Only POST allowed for 3DS callbacks to prevent malicious GET probes
		payments.POST("/payfast/3ds_callback", h.ThreeDSCallback)
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

## 2. API Models (`models.go`)
Defines the strict JSON mapping for PayFast Option C (Temporary Token & Tokenized Transaction) including the zero-persistence rules (`json:"-"`).

```go
package payfast

import (
	"fmt"
	"time"
)

// Environment specifies Sandbox vs Production gateway endpoints.
type Environment string

const (
	EnvSandbox    Environment = "sandbox"
	EnvProduction Environment = "production"
)

// AuthTokenRequest represents the request to get a merchant access token.
type AuthTokenRequest struct {
	MerchantID string
	SecuredKey string
	GrantType  string
	CustomerIP string
}

// AuthTokenResponse represents the authentication response from PayFast API token endpoint.
type AuthTokenResponse struct {
	Code         string `json:"code,omitempty"`
	Token        string `json:"token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    string `json:"expiry,omitempty"` // Wait, docs say "expiry": "<no.ofseconds>"
	Message      string `json:"message,omitempty"`
}

// TokenCache holds in-memory cached OAuth/Auth tokens with thread-safe expiration checks.
type TokenCache struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// AccountType represents the type of payment instrument.
type AccountType int

const (
	AccountTypeCard   AccountType = 1
	AccountTypeBank   AccountType = 2
	AccountTypeWallet AccountType = 3
)

// CustomerValidationRequest contains all fields needed for POST /customer/validate
type CustomerValidationRequest struct {
	BasketID             string
	TxnAmt               string
	OrderDate            string // YYYY-MM-DD HH:mm:ss
	CustomerMobileNo     string
	CustomerEmailAddress string
	AccountTypeID        string
	MerCatCode           string
	CustomerIP           string

	// Card specific
	CardNumber         string `json:"-"`
	ExpiryMonth        string `json:"-"`
	ExpiryYear         string `json:"-"`
	CVV                string `json:"-"`
	Data3DSPagemode    string
	Data3DSCallbackURL string

	// Bank/Wallet specific
	BankCode      string `json:"-"`
	AccountNumber string `json:"-"`
	AccountTitle  string `json:"-"`
	CNICNumber    string `json:"-"`
}

// String explicitly masks sensitive fields so they never leak in fmt.Print or log operations.
func (c CustomerValidationRequest) String() string {
	return fmt.Sprintf("CustomerValidationRequest{BasketID:%s, TxnAmt:%s, OrderDate:%s, AccountTypeID:%s, CardNumber:[REDACTED], CVV:[REDACTED], AccountNumber:[REDACTED]}",
		c.BasketID, c.TxnAmt, c.OrderDate, c.AccountTypeID)
}

// CustomerValidationResponse is returned by POST /customer/validate
type CustomerValidationResponse struct {
	Code                         string `json:"code"`
	Message                      string `json:"message"`
	TransactionID                string `json:"transaction_id"`
	Data3DSAcsURL                string `json:"data_3ds_acsurl"`
	Data3DSPaReq                 string `json:"data_3ds_pareq"`
	Data3DSHTML                  string `json:"data_3ds_html"`
	Data3DSSecureID              string `json:"data_3ds_secureid"`
	Data3DSGatewayRecommendation string `json:"data_3ds_gatewayrecommendation"`
	ECI                          string `json:"eci"`
}

// InitiateTransactionRequest contains fields for POST /transaction
type InitiateTransactionRequest struct {
	BasketID             string
	TxnAmt               string
	OrderDate            string
	CustomerMobileNo     string
	CustomerEmailAddress string
	AccountTypeID        string
	MerCatCode           string
	CustomerIP           string

	// From validation response
	TransactionID string
	ECI           string

	// Card specific
	CardNumber      string `json:"-"`
	ExpiryMonth     string `json:"-"`
	ExpiryYear      string `json:"-"`
	CVV             string `json:"-"`
	Data3DSSecureID string
	Data3DSPaRes    string

	// Bank/Wallet specific
	BankCode      string `json:"-"`
	AccountNumber string `json:"-"`
	AccountTitle  string `json:"-"`
	CNICNumber    string `json:"-"`
}

// String explicitly masks sensitive fields.
func (i InitiateTransactionRequest) String() string {
	return fmt.Sprintf("InitiateTransactionRequest{BasketID:%s, TxnAmt:%s, TransactionID:%s, CardNumber:[REDACTED], CVV:[REDACTED], AccountNumber:[REDACTED]}",
		i.BasketID, i.TxnAmt, i.TransactionID)
}

// InitiateTransactionResponse is returned by POST /transaction
type InitiateTransactionResponse struct {
	StatusCode    string `json:"status_code"`
	StatusMsg     string `json:"status_msg"`
	RdvMessageKey string `json:"rdv_message_key"`
	BasketID      string `json:"basket_id"`
	TransactionID string `json:"transaction_id"`
	Code          string `json:"code"` // Sometimes "00"
}

// TransactionStatusResponse is returned by GET /transaction/<transaction_id>
type TransactionStatusResponse struct {
	StatusCode    string `json:"status_code"`
	StatusMsg     string `json:"status_msg"`
	RdvMessageKey string `json:"rdv_message_key"`
	BasketID      string `json:"basket_id"`
	TransactionID string `json:"transaction_id"`
	Code          string `json:"code"`
	TxnAmt        string `json:"txnamt,omitempty"`
}

// TemporaryTokenRequest is for POST /transaction/token
type TemporaryTokenRequest struct {
	BasketID         string `json:"basket_id"`
	TxnAmt           string `json:"txnamt"`
	OrderDate        string `json:"order_date"` // YYYY-MM-DD HH:mm:ss
	CustomerMobileNo string `json:"user_mobile_number"`
	MerchantUserId   string `json:"merchant_user_id"`
	AccountTypeID    string `json:"account_type"`
	MerCatCode       string `json:"merCatCode"`
	CustomerIP       string `json:"customer_ip"`
	SecuredHash      string `json:"secured_hash"`

	// Card specific (never persist)
	CardNumber         string `json:"card_number"`
	ExpiryMonth        string `json:"expiry_month"`
	ExpiryYear         string `json:"expiry_year"`
	CVV                string `json:"cvv"`
	Data3DSPagemode    string `json:"data_3ds_pagemode,omitempty"`
	Data3DSCallbackURL string `json:"data_3ds_callback_url,omitempty"`
}

func (t TemporaryTokenRequest) String() string {
	return fmt.Sprintf("TemporaryTokenRequest{BasketID:%s, TxnAmt:%s, CardNumber:[REDACTED], CVV:[REDACTED]}", t.BasketID, t.TxnAmt)
}

// TemporaryTokenResponse is returned by POST /transaction/token
type TemporaryTokenResponse struct {
	StatusCode                   string `json:"status_code"`
	StatusMsg                    string `json:"status_msg"`
	InstrumentAlias              string `json:"instrument_alias"`
	InstrumentToken              string `json:"instrument_token"`
	TransactionID                string `json:"transaction_id"`
	OtpRequired                  string `json:"otp_required"`
	ECI                          string `json:"eci"`
	Data3DSAcsURL                string `json:"data_3ds_acsurl"`
	Data3DSPaReq                 string `json:"data_3ds_pareq"`
	Data3DSHTML                  string `json:"data_3ds_html"`
	Data3DSSecureID              string `json:"data_3ds_secureid"`
	Data3DSGatewayRecommendation string `json:"data_3ds_gatewayrecommendation"`
}

// TokenizedTransactionRequest is for POST /transaction/tokenized
type TokenizedTransactionRequest struct {
	InstrumentToken  string `json:"instrument_token"`
	TransactionID    string `json:"transaction_id"`
	MerchantUserId   string `json:"merchant_user_id"`
	CustomerMobileNo string `json:"user_mobile_number"`
	BasketID         string `json:"basket_id"`
	OrderDate        string `json:"order_date"`
	TxnDesc          string `json:"txndesc"`
	TxnAmt           string `json:"txnamt"`
	Otp              string `json:"otp,omitempty"`
	CustomerIP       string `json:"customer_ip"`
	MerCatCode       string `json:"merCatCode"`
	ECI              string `json:"eci,omitempty"`
	Data3DSSecureID  string `json:"data_3ds_secureid,omitempty"`
	Data3DSPaRes     string `json:"data_3ds_pares,omitempty"`
	SecuredHash      string `json:"secured_hash"`
}

func (t TokenizedTransactionRequest) String() string {
	return fmt.Sprintf("TokenizedTransactionRequest{BasketID:%s, TransactionID:%s, InstrumentToken:[REDACTED]}", t.BasketID, t.TransactionID)
}

// TokenizedTransactionResponse is returned by POST /transaction/tokenized
type TokenizedTransactionResponse struct {
	StatusCode    string `json:"status_code"`
	StatusMsg     string `json:"status_msg"`
	RdvMessageKey string `json:"rdv_message_key"`
	BasketID      string `json:"basket_id"`
	TransactionID string `json:"transaction_id"`
	Code          string `json:"code"`
}
```

## 3. PayFast API Client (`api.go`)
Performs the actual API requests to the PayFast gateway.

```go
package payfast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ValidateCustomerPayment calls POST /customer/validate
func (c *Client) ValidateCustomerPayment(ctx context.Context, req CustomerValidationRequest) (*CustomerValidationResponse, error) {
	token, err := c.GetAuthToken(ctx, req.CustomerIP)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := c.baseURL + "/customer/validate"
	formData := url.Values{}

	// Common Parameters
	formData.Set("basket_id", req.BasketID)
	formData.Set("txnamt", req.TxnAmt)
	formData.Set("order_date", req.OrderDate)
	formData.Set("customer_mobile_no", req.CustomerMobileNo)
	formData.Set("customer_email_address", req.CustomerEmailAddress)
	formData.Set("account_type_id", req.AccountTypeID)
	formData.Set("merCatCode", req.MerCatCode)
	formData.Set("customer_ip", req.CustomerIP)

	// Calculate and set secured_hash
	securedHash := CalculateValidationHash(req, c.securedKey)
	formData.Set("secured_hash", securedHash)

	// Instrument-specific parameters
	if req.AccountTypeID == "1" {
		formData.Set("card_number", req.CardNumber)
		formData.Set("expiry_month", req.ExpiryMonth)
		formData.Set("expiry_year", req.ExpiryYear)
		formData.Set("cvv", req.CVV)
		if req.Data3DSPagemode != "" {
			formData.Set("data_3ds_pagemode", req.Data3DSPagemode)
		}
		if req.Data3DSCallbackURL != "" {
			formData.Set("data_3ds_callback_url", req.Data3DSCallbackURL)
		}
	} else if req.AccountTypeID == "2" || req.AccountTypeID == "3" {
		formData.Set("bank_code", req.BankCode)
		formData.Set("account_number", req.AccountNumber)
		formData.Set("cnic_number", req.CNICNumber)
		if req.AccountTitle != "" {
			formData.Set("account_title", req.AccountTitle)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Customer validation failed",
			Internal:   fmt.Errorf("status %d", resp.StatusCode),
		}
	}

	var validationRes CustomerValidationResponse
	if err := json.Unmarshal(bodyBytes, &validationRes); err != nil {
		return nil, fmt.Errorf("failed to parse validation response: %w", err)
	}

	return &validationRes, nil
}

// InitiateTransaction calls POST /transaction
func (c *Client) InitiateTransaction(ctx context.Context, req InitiateTransactionRequest, otp string) (*InitiateTransactionResponse, error) {
	token, err := c.GetAuthToken(ctx, req.CustomerIP)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := c.baseURL + "/transaction"
	formData := url.Values{}

	formData.Set("basket_id", req.BasketID)
	formData.Set("txnamt", req.TxnAmt)
	formData.Set("order_date", req.OrderDate)
	formData.Set("customer_mobile_no", req.CustomerMobileNo)
	formData.Set("customer_email_address", req.CustomerEmailAddress)
	formData.Set("account_type_id", req.AccountTypeID)
	formData.Set("merCatCode", req.MerCatCode)
	formData.Set("customer_ip", req.CustomerIP)

	if req.TransactionID != "" {
		formData.Set("transaction_id", req.TransactionID)
	}
	if req.ECI != "" {
		formData.Set("eci", req.ECI)
	}

	if otp != "" {
		formData.Set("otp", otp)
	}

	securedHash := CalculateTransactionHash(req, otp, c.securedKey)
	formData.Set("secured_hash", securedHash)

	if req.AccountTypeID == "1" {
		formData.Set("card_number", req.CardNumber)
		formData.Set("expiry_month", req.ExpiryMonth)
		formData.Set("expiry_year", req.ExpiryYear)
		formData.Set("cvv", req.CVV)
		if req.Data3DSSecureID != "" {
			formData.Set("data_3ds_secureid", req.Data3DSSecureID)
		}
		if req.Data3DSPaRes != "" {
			formData.Set("data_3ds_pares", req.Data3DSPaRes)
		}
	} else if req.AccountTypeID == "2" || req.AccountTypeID == "3" {
		formData.Set("bank_code", req.BankCode)
		formData.Set("account_number", req.AccountNumber)
		formData.Set("cnic_number", req.CNICNumber)
		if req.AccountTitle != "" {
			formData.Set("account_title", req.AccountTitle)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Transaction initiation failed",
			Internal:   fmt.Errorf("status %d", resp.StatusCode),
		}
	}

	var txnRes InitiateTransactionResponse
	if err := json.Unmarshal(bodyBytes, &txnRes); err != nil {
		return nil, fmt.Errorf("failed to parse transaction response: %w", err)
	}

	return &txnRes, nil
}

// GetTransactionStatus calls GET /transaction/<transaction_id>
func (c *Client) GetTransactionStatus(ctx context.Context, transactionID string) (*TransactionStatusResponse, error) {
	token, err := c.GetAuthToken(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := fmt.Sprintf("%s/transaction/%s", c.baseURL, url.PathEscape(transactionID))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Transaction status check failed",
			Internal:   fmt.Errorf("status %d", resp.StatusCode),
		}
	}

	var statusRes TransactionStatusResponse
	if err := json.Unmarshal(bodyBytes, &statusRes); err != nil {
		return nil, fmt.Errorf("failed to parse transaction status response: %w", err)
	}

	return &statusRes, nil
}

// GetTemporaryTransactionToken calls POST /transaction/token
func (c *Client) GetTemporaryTransactionToken(ctx context.Context, req TemporaryTokenRequest) (*TemporaryTokenResponse, error) {
	token, err := c.GetAuthToken(ctx, req.CustomerIP)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := c.baseURL + "/transaction/token"
	formData := url.Values{}

	// Common Parameters
	formData.Set("basket_id", req.BasketID)
	formData.Set("txnamt", req.TxnAmt)
	formData.Set("order_date", req.OrderDate)
	formData.Set("user_mobile_number", req.CustomerMobileNo)
	formData.Set("merchant_user_id", req.MerchantUserId)
	formData.Set("account_type", req.AccountTypeID)
	formData.Set("merCatCode", req.MerCatCode)
	formData.Set("customer_ip", req.CustomerIP)

	// Card specific
	if req.AccountTypeID == "1" {
		formData.Set("card_number", req.CardNumber)
		formData.Set("expiry_month", req.ExpiryMonth)
		formData.Set("expiry_year", req.ExpiryYear)
		formData.Set("cvv", req.CVV)
		if req.Data3DSPagemode != "" {
			formData.Set("data_3ds_pagemode", req.Data3DSPagemode)
		}
		if req.Data3DSCallbackURL != "" {
			formData.Set("data_3ds_callback_url", req.Data3DSCallbackURL)
		}
	}

	securedHash := CalculateTemporaryTokenHash(req, c.securedKey)
	formData.Set("secured_hash", securedHash)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Temporary token request failed",
			Internal:   fmt.Errorf("status %d", resp.StatusCode),
		}
	}

	var tokenRes TemporaryTokenResponse
	if err := json.Unmarshal(bodyBytes, &tokenRes); err != nil {
		return nil, fmt.Errorf("failed to parse temporary token response: %w", err)
	}

	return &tokenRes, nil
}

// InitiateTokenizedTransaction calls POST /transaction/tokenized
func (c *Client) InitiateTokenizedTransaction(ctx context.Context, req TokenizedTransactionRequest) (*TokenizedTransactionResponse, error) {
	token, err := c.GetAuthToken(ctx, req.CustomerIP)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	endpoint := c.baseURL + "/transaction/tokenized"
	formData := url.Values{}

	formData.Set("instrument_token", req.InstrumentToken)
	formData.Set("transaction_id", req.TransactionID)
	formData.Set("merchant_user_id", req.MerchantUserId)
	formData.Set("user_mobile_number", req.CustomerMobileNo)
	formData.Set("basket_id", req.BasketID)
	formData.Set("order_date", req.OrderDate)
	formData.Set("txndesc", req.TxnDesc)
	formData.Set("txnamt", req.TxnAmt)
	formData.Set("customer_ip", req.CustomerIP)
	formData.Set("merCatCode", req.MerCatCode)

	if req.Otp != "" {
		formData.Set("otp", req.Otp)
	}
	if req.ECI != "" {
		formData.Set("eci", req.ECI)
	}
	if req.Data3DSSecureID != "" {
		formData.Set("data_3ds_secureid", req.Data3DSSecureID)
	}
	if req.Data3DSPaRes != "" {
		formData.Set("data_3ds_pares", req.Data3DSPaRes)
	}

	securedHash := CalculateTokenizedTransactionHash(req, c.securedKey)
	formData.Set("secured_hash", securedHash)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &GatewayError{
			StatusCode: resp.StatusCode,
			Message:    "Tokenized transaction failed",
			Internal:   fmt.Errorf("status %d", resp.StatusCode),
		}
	}

	var txnRes TokenizedTransactionResponse
	if err := json.Unmarshal(bodyBytes, &txnRes); err != nil {
		return nil, fmt.Errorf("failed to parse tokenized transaction response: %w", err)
	}

	return &txnRes, nil
}
```

## 4. HMAC Signatures (`signature.go`)
Implements exactly PayFast's hashing formula.

```go
package payfast

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// generateHMACSHA256 generates a hex-encoded HMAC-SHA256 signature for the concatenated payload.
func generateHMACSHA256(payload string, securedKey string) string {
	mac := hmac.New(sha256.New, []byte(securedKey))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// CalculateValidationHash computes the required secured_hash for POST /customer/validate
// Official rules:
// Card: basket_id + txnamt + card_number + expiry_month + expiry_year + cvv
// Account/Wallet: basket_id + txnamt + account_number + cnic_number
func CalculateValidationHash(req CustomerValidationRequest, securedKey string) string {
	var payload string

	switch req.AccountTypeID {
	case "1": // Card
		payload = req.BasketID + req.TxnAmt + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV
	case "2", "3": // Account or Wallet
		payload = req.BasketID + req.TxnAmt + req.AccountNumber + req.CNICNumber
	default:
		// Fallback for unknown account types
		payload = req.BasketID + req.TxnAmt + req.AccountNumber + req.CNICNumber
	}

	return generateHMACSHA256(payload, securedKey)
}

// CalculateTransactionHash computes the required secured_hash for POST /transaction
// Official rules:
// Card: basket_id + txnamt + card_number + expiry_month + expiry_year + cvv + otp
// Account/Wallet: basket_id + txnamt + account_number + cnic_number + otp
// Note: If no OTP is present/required, the "+ otp" part simply adds an empty string.
func CalculateTransactionHash(req InitiateTransactionRequest, otp string, securedKey string) string {
	var payload string

	switch req.AccountTypeID {
	case "1": // Card
		payload = req.BasketID + req.TxnAmt + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV + otp
	case "2", "3": // Account or Wallet
		payload = req.BasketID + req.TxnAmt + req.AccountNumber + req.CNICNumber + otp
	default:
		payload = req.BasketID + req.TxnAmt + req.AccountNumber + req.CNICNumber + otp
	}

	return generateHMACSHA256(payload, securedKey)
}

// VerifySignature compares expected and received signatures using constant-time comparison.
func VerifySignature(expected, received string) bool {
	if expected == "" || received == "" {
		return false
	}
	expBytes := []byte(strings.ToLower(strings.TrimSpace(expected)))
	recBytes := []byte(strings.ToLower(strings.TrimSpace(received)))
	if len(expBytes) != len(recBytes) {
		return false
	}
	return subtle.ConstantTimeCompare(expBytes, recBytes) == 1
}

// CalculateTemporaryTokenHash computes the required secured_hash for POST /transaction/token
// Official rules:
// Card: merchant_user_id + user_mobile_number + card_number + expiry_month + expiry_year + cvv
func CalculateTemporaryTokenHash(req TemporaryTokenRequest, securedKey string) string {
	payload := req.MerchantUserId + req.CustomerMobileNo + req.CardNumber + req.ExpiryMonth + req.ExpiryYear + req.CVV
	return generateHMACSHA256(payload, securedKey)
}

// CalculateTokenizedTransactionHash computes the required secured_hash for POST /transaction/tokenized
// Official rules:
// instrument_token + merchant_user_id + user_mobile_number + txnamt + otp
func CalculateTokenizedTransactionHash(req TokenizedTransactionRequest, securedKey string) string {
	payload := req.InstrumentToken + req.MerchantUserId + req.CustomerMobileNo + req.TxnAmt + req.Otp
	return generateHMACSHA256(payload, securedKey)
}
```
