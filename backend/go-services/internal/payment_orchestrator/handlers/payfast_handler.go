package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
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

// PaymentRequest payload from Flutter.
//
// SECURITY & PCI-DSS COMPLIANCE:
// 1. No Cardholder Data Persistence: CardNumber, Expiry, and CVV are never stored in DB or Redis.
// 2. Logging Hardening: Ensure reverse proxy (Nginx/Traefik), Gin middleware, and APM tracing
//    have request-body logging DISABLED for all /api/v1/payments/* endpoints.
// 3. CustomerEmailAddress is accepted for local audit records but not sent to PayFast token API.
type PaymentRequest struct {
	OrderID              string `json:"order_id"`
	PaymentMethod        string `json:"payment_method"` // "card", "bank_account", "wallet"
	AccountTypeID        string `json:"account_type_id"`
	CustomerMobileNo     string `json:"customer_mobile_no"`
	CustomerEmailAddress string `json:"customer_email_address"`

	// Card specific fields — strictly in-memory during token generation only.
	CardNumber  string `json:"card_number,omitempty"`
	ExpiryMonth string `json:"expiry_month,omitempty"`
	ExpiryYear  string `json:"expiry_year,omitempty"`
	CVV         string `json:"cvv,omitempty"`

	// Bank / Wallet specific fields — strictly in-memory during token generation only.
	BankCode      string `json:"bank_code,omitempty"`
	AccountNumber string `json:"account_number,omitempty"`
	AccountTitle  string `json:"account_title,omitempty"`
	CNICNumber    string `json:"cnic_number,omitempty"`
}

type ThreeDSCallbackRequest struct {
	MD    string `form:"md" binding:"required"`
	PaRes string `form:"paRes" binding:"required"`
}

// generateHMACSHA256 generates a hex-encoded HMAC-SHA256 signature.
func generateHMACSHA256(data string, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// signMD generates an HMAC signature for the internal transaction ID.
// Fails closed if INTERNAL_CALLBACK_SECRET is missing.
func signMD(internalTxnID string) (string, error) {
	secret := os.Getenv("INTERNAL_CALLBACK_SECRET")
	if secret == "" {
		return "", fmt.Errorf("INTERNAL_CALLBACK_SECRET is not configured")
	}
	return internalTxnID + "." + generateHMACSHA256(internalTxnID, secret), nil
}

// verifyMD validates the HMAC signature in constant time.
// Fails closed if INTERNAL_CALLBACK_SECRET is missing.
func verifyMD(mdParam string) (string, error) {
	secret := os.Getenv("INTERNAL_CALLBACK_SECRET")
	if secret == "" {
		return "", fmt.Errorf("INTERNAL_CALLBACK_SECRET is not configured")
	}

	parts := strings.Split(mdParam, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid MD format")
	}

	internalTxnID := parts[0]
	providedSignature := parts[1]

	expectedSignature := generateHMACSHA256(internalTxnID, secret)

	if subtle.ConstantTimeCompare([]byte(expectedSignature), []byte(providedSignature)) != 1 {
		return "", fmt.Errorf("signature mismatch")
	}

	return internalTxnID, nil
}

// build3DSCallbackURL safely constructs the 3DS callback URL with a signed MD parameter.
// Uses url.Parse for proper URL construction instead of string concatenation.
func build3DSCallbackURL(baseURL, internalTxnID string) (string, error) {
	signedMD, err := signMD(internalTxnID)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid callback base URL: %w", err)
	}
	q := u.Query()
	q.Set("md", signedMD)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// PaymentMetadata stores non-sensitive PayFast token state required for completing
// Tokenized Transactions after 3DS authentication.
//
// NOTE: No Cardholder Data Persistence — PAN, CVV, and expiry are never stored.
// The instrument_token, customer_ip, and 3DS metadata below are PayFast-issued gateway tokens,
// not cardholder data, and must be persisted to complete the two-step flow.
type PaymentMetadata struct {
	InstrumentToken string `json:"instrument_token"`
	GatewayTxnID    string `json:"gateway_txn_id"`
	Data3DSSecureID string `json:"data_3ds_secureid"`
	ECI             string `json:"eci"`
	CustomerMobile  string `json:"customer_mobile"`
	AccountTypeID   string `json:"account_type"`
	CustomerIP      string `json:"customer_ip"`
}

// PayFastSplitHandler handles synchronous PayFast API payments.
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
	if os.Getenv("INTERNAL_CALLBACK_SECRET") == "" {
		panic("INTERNAL_CALLBACK_SECRET environment variable is required")
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

	// 2. Prevent duplicate active attempts (using FOR UPDATE to serialize requests on the same order)
	tx, err := h.db.Begin(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to begin transaction"})
		return
	}
	defer tx.Rollback(c.Request.Context())

	err = tx.QueryRow(c.Request.Context(),
		`SELECT total_amount, status, customer_tracking_id FROM orders WHERE order_tracking_id = $1 FOR UPDATE`, req.OrderID,
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

	// Application-level duplicate check for user-friendly error messages.
	// The real guard is the database-level unique partial index ux_payment_active_order
	// which prevents race conditions that this SELECT cannot catch.
	var activeAttempts int
	err = tx.QueryRow(c.Request.Context(),
		`SELECT count(*) FROM payment_transactions WHERE order_tracking_id = $1 AND status IN ('pending', 'processing', '3ds_required', 'settlement_pending', 'gateway_pending')`,
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

	// Also fetch authoritative mobile number from the users table (never trust the frontend)
	var authoritativeMobile string
	err = tx.QueryRow(c.Request.Context(), `SELECT COALESCE(phone, '') FROM users WHERE tracking_id = $1`, customerTrackingID).Scan(&authoritativeMobile)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user profile"})
		return
	}
	if authoritativeMobile == "" && req.CustomerMobileNo != "" {
		authoritativeMobile = req.CustomerMobileNo
	}
	if authoritativeMobile == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Customer phone number is required for payment verification"})
		return
	}

	if req.AccountTypeID == "" {
		if req.CardNumber != "" {
			req.AccountTypeID = "2" // Default card account type
		} else if req.AccountNumber != "" {
			req.AccountTypeID = "3" // Default bank/wallet account type
		} else {
			req.AccountTypeID = "1"
		}
	}

	// 3. Insert Pending Payment Attempt
	// If a race condition causes a duplicate insert, the unique partial index
	// ux_payment_active_order will reject it and we handle the error gracefully.
	_, err = tx.Exec(c.Request.Context(),
		`INSERT INTO payment_transactions (transaction_id, order_tracking_id, gateway, amount, currency, status, kind)
		 VALUES ($1, $2, 'payfast', $3, 'PKR', 'pending', 'payment')`,
		internalTxnID, req.OrderID, expectedAmount,
	)
	if err != nil {
		// Check if it's a unique constraint violation from the partial index
		if strings.Contains(err.Error(), "ux_payment_active_order") || strings.Contains(err.Error(), "unique constraint") {
			c.JSON(http.StatusConflict, gin.H{"error": "A payment attempt is already in progress for this order"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create payment attempt"})
		return
	}

	if err := tx.Commit(c.Request.Context()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to commit payment attempt"})
		return
	}

	// 4. Build safe 3DS callback URL with signed MD
	callbackBaseURL := envStr("PAYFAST_3DS_CALLBACK_URL", "https://api.omnigo.com/api/v1/payments/payfast/3ds_callback")
	callbackURL, err := build3DSCallbackURL(callbackBaseURL, internalTxnID)
	if err != nil {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Failed to build callback URL: "+err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal configuration error"})
		return
	}

	// 5. Get Temporary Transaction Token
	tokenReq := payfast.TemporaryTokenRequest{
		BasketID:           req.OrderID,
		TxnAmt:             amountStr,
		OrderDate:          time.Now().Format("2006-01-02 15:04:05"),
		CustomerMobileNo:   authoritativeMobile,
		MerchantUserId:     merchantUserID,
		AccountTypeID:      req.AccountTypeID,
		MerCatCode:         os.Getenv("PAYFAST_MERCHANT_CATEGORY"),
		CustomerIP:         customerIP,
		CardNumber:         req.CardNumber,
		ExpiryMonth:        req.ExpiryMonth,
		ExpiryYear:         req.ExpiryYear,
		CVV:                req.CVV,
		AccountNumber:      req.AccountNumber,
		CNICNumber:         req.CNICNumber,
		Data3DSPagemode:    "SIMPLE",
		Data3DSCallbackURL: callbackURL,
	}

	tokenRes, err := h.payfast.GetTemporaryTransactionToken(c.Request.Context(), tokenReq)

	// Clear references to sensitive card / account fields immediately.
	req.CardNumber = ""
	req.CVV = ""
	req.AccountNumber = ""
	req.CNICNumber = ""

	if err != nil {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Temporary token failed: "+err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "Payment token generation failed"})
		return
	}

	if tokenRes.StatusCode != "00" {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, tokenRes.StatusMsg)
		c.JSON(http.StatusBadRequest, gin.H{"error": tokenRes.StatusMsg})
		return
	}

	// 6. Handle 3DS if required
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
		metaMap["eci"] = tokenRes.ECI.String()
		metaMap["customer_mobile"] = authoritativeMobile
		metaMap["account_type"] = req.AccountTypeID
		metaMap["customer_ip"] = customerIP // Preserve original customer IP for 3DS tokenized call

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

	// 7. If no 3DS required, immediately process the tokenized transaction.
	txnReq := payfast.TokenizedTransactionRequest{
		InstrumentToken:  tokenRes.InstrumentToken,
		TransactionID:    tokenRes.TransactionID,
		MerchantUserId:   merchantUserID,
		CustomerMobileNo: authoritativeMobile,
		BasketID:         req.OrderID,
		OrderDate:        tokenReq.OrderDate,
		TxnDesc:          "OmniGo Order " + req.OrderID,
		TxnAmt:           amountStr,
		CustomerIP:       customerIP,
		MerCatCode:       os.Getenv("PAYFAST_MERCHANT_CATEGORY"),
		ECI:              tokenRes.ECI.String(),
	}

	txnRes, err := h.payfast.InitiateTokenizedTransaction(c.Request.Context(), txnReq)
	if err != nil {
		// Distinguish transient network timeouts vs deterministic gateway rejections
		if payfast.IsTransient(err) {
			h.markPaymentGatewayPending(c.Request.Context(), internalTxnID, tokenRes.TransactionID, "Tokenized transaction network timeout: "+err.Error())
			c.JSON(http.StatusGatewayTimeout, gin.H{
				"status":         "gateway_pending",
				"error":          "Gateway timeout during capture; reconciliation in progress",
				"transaction_id": internalTxnID,
			})
			return
		}

		// Deterministic HTTP 4xx or gateway client error
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Tokenized transaction failed: "+err.Error())
		c.JSON(http.StatusBadRequest, gin.H{
			"status": "failed",
			"error":  "Payment processing was rejected by gateway",
		})
		return
	}

	// Validate gateway transaction response before status inquiry
	if txnRes == nil || txnRes.TransactionID == "" {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Gateway returned empty transaction ID")
		c.JSON(http.StatusBadGateway, gin.H{"error": "Invalid gateway response"})
		return
	}
	if txnRes.StatusCode != "00" && txnRes.StatusCode != "" {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, txnRes.StatusMsg)
		c.JSON(http.StatusBadRequest, gin.H{"error": txnRes.StatusMsg, "code": txnRes.StatusCode})
		return
	}

	// 8. Check Transaction Status & Settle
	h.verifyAndSettle(c, internalTxnID, req.OrderID, txnRes.TransactionID, expectedAmount)
}

// ThreeDSCallback handles the callback from the PayFast 3DS form POST
func (h *PayFastSplitHandler) ThreeDSCallback(c *gin.Context) {
	var req ThreeDSCallbackRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid 3DS callback payload"})
		return
	}
	internalTxnID, err := verifyMD(req.MD)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid MD signature"})
		return
	}

	if internalTxnID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing MD correlator"})
		return
	}

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

	// Use original customer IP preserved during initial request, fallback to client IP
	origCustomerIP := meta.CustomerIP
	if origCustomerIP == "" {
		origCustomerIP = c.ClientIP()
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
		CustomerIP:       origCustomerIP,
		MerCatCode:       os.Getenv("PAYFAST_MERCHANT_CATEGORY"),
		ECI:              meta.ECI,
		Data3DSSecureID:  meta.Data3DSSecureID,
		Data3DSPaRes:     req.PaRes,
	}

	txnRes, err := h.payfast.InitiateTokenizedTransaction(c.Request.Context(), txnReq)
	if err != nil {
		// Distinguish transient network timeouts vs deterministic gateway rejections
		if payfast.IsTransient(err) {
			h.markPaymentGatewayPending(c.Request.Context(), internalTxnID, meta.GatewayTxnID, "Tokenized 3DS transaction timeout: "+err.Error())
			c.JSON(http.StatusGatewayTimeout, gin.H{
				"status": "gateway_pending",
				"error":  "Gateway timeout during 3DS transaction; reconciliation in progress",
			})
			return
		}

		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Tokenized 3DS transaction failed: "+err.Error())
		c.JSON(http.StatusBadRequest, gin.H{
			"status": "failed",
			"error":  "3DS payment processing was rejected by gateway",
		})
		return
	}

	// Validate gateway transaction response
	if txnRes == nil || txnRes.TransactionID == "" {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Gateway returned empty transaction ID after 3DS")
		c.JSON(http.StatusBadGateway, gin.H{"error": "Invalid gateway response"})
		return
	}
	if txnRes.StatusCode != "00" && txnRes.StatusCode != "" {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, txnRes.StatusMsg)
		c.JSON(http.StatusBadRequest, gin.H{"error": txnRes.StatusMsg, "code": txnRes.StatusCode})
		return
	}

	// 4. Verify & Settle
	h.verifyAndSettle(c, internalTxnID, orderID, txnRes.TransactionID, amount)
}

// verifyAndSettle calls the GET status endpoint and settles the DB transaction atomically.
func (h *PayFastSplitHandler) verifyAndSettle(c *gin.Context, internalTxnID string, orderID string, gatewayTxnID string, expectedAmount float64) {
	if gatewayTxnID == "" {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Missing gateway transaction ID for status verification")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid gateway transaction ID"})
		return
	}

	statusRes, err := h.payfast.GetTransactionStatus(c.Request.Context(), gatewayTxnID)
	if err != nil {
		// Network timeout on status query: mark gateway_pending, do NOT mark failed
		h.markPaymentGatewayPending(c.Request.Context(), internalTxnID, gatewayTxnID, "Status check timeout: "+err.Error())
		c.JSON(http.StatusGatewayTimeout, gin.H{
			"status": "gateway_pending",
			"error":  "Gateway timeout during status verification; reconciliation pending",
		})
		return
	}

	// 00 = Processed OK per PayFast documentation
	if statusRes.StatusCode != "00" {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, statusRes.StatusMsg)
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Payment not successful",
			"code":  statusRes.StatusCode,
			"msg":   statusRes.StatusMsg,
		})
		return
	}

	// Validate that the returned gateway transaction ID matches our request if populated
	if statusRes.TransactionID != "" && statusRes.TransactionID != gatewayTxnID {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Gateway transaction ID mismatch in status response")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Transaction ID mismatch"})
		return
	}

	if statusRes.BasketID != "" && statusRes.BasketID != orderID {
		h.markPaymentFailed(c.Request.Context(), internalTxnID, "Basket ID mismatch")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Basket ID mismatch"})
		return
	}

	// Numerical amount comparison in minor units (paisa) when txnamt is returned by gateway
	if statusRes.TxnAmt != "" {
		gatewayAmt, parseErr := strconv.ParseFloat(statusRes.TxnAmt, 64)
		if parseErr != nil {
			h.markPaymentFailed(c.Request.Context(), internalTxnID, fmt.Sprintf("Invalid TxnAmt in gateway status response: %s", statusRes.TxnAmt))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid transaction amount from gateway"})
			return
		}

		expectedPaisa := int64(math.Round(expectedAmount * 100))
		gatewayPaisa := int64(math.Round(gatewayAmt * 100))
		if expectedPaisa != gatewayPaisa {
			h.markPaymentFailed(c.Request.Context(), internalTxnID, fmt.Sprintf("Amount mismatch: expected %d paisa (%.2f), got %d paisa (%.2f)", expectedPaisa, expectedAmount, gatewayPaisa, gatewayAmt))
			c.JSON(http.StatusBadRequest, gin.H{"error": "Transaction amount mismatch"})
			return
		}
	}

	err = h.executeSplit(c.Request.Context(), internalTxnID, orderID, expectedAmount, gatewayTxnID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Settlement failed"})
		return
	}

	if strings.Contains(c.GetHeader("Accept"), "text/html") {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Payment Successful</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family:sans-serif;text-align:center;padding:50px 20px;">
    <h2 style="color:#2e7d32;">Payment Authenticated Successfully</h2>
    <p>Your order #<strong>%s</strong> is being processed.</p>
    <script>
        if (window.opener) { window.opener.postMessage({status: 'success', order_id: '%s'}, '*'); }
        if (window.FlutterChannel) { window.FlutterChannel.postMessage(JSON.stringify({status: 'success', order_id: '%s'})); }
    </script>
</body>
</html>`, orderID, orderID, orderID))
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "settlement_pending", "order_id": orderID})
}

// markPaymentFailed transitions a payment to 'failed' status on deterministic rejection.
func (h *PayFastSplitHandler) markPaymentFailed(ctx context.Context, internalTxnID string, reason string) {
	_, _ = h.db.Exec(ctx,
		`UPDATE payment_transactions SET status = 'failed', error_message = $1, updated_at = NOW()
		 WHERE transaction_id = $2 AND status IN ('pending', '3ds_required', 'processing', 'gateway_pending')`,
		reason, internalTxnID,
	)
}

// markPaymentGatewayPending transitions a payment to 'gateway_pending' on transient network timeouts.
// This prevents incorrectly failing payments that might have succeeded on the gateway, allowing reconciliation.
func (h *PayFastSplitHandler) markPaymentGatewayPending(ctx context.Context, internalTxnID string, gatewayTxnID string, reason string) {
	_, _ = h.db.Exec(ctx,
		`UPDATE payment_transactions SET status = 'gateway_pending', gateway_txn_id = COALESCE(NULLIF($1, ''), gateway_txn_id), error_message = $2, updated_at = NOW()
		 WHERE transaction_id = $3 AND status IN ('pending', '3ds_required', 'processing')`,
		gatewayTxnID, reason, internalTxnID,
	)
}

// executeSplit performs the DB-atomic settlement preparation.
func (h *PayFastSplitHandler) executeSplit(ctx context.Context, internalTxnID string, orderID string, expectedAmount float64, gatewayTxnID string) error {
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
	if currentStatus == "captured" || currentStatus == "settlement_pending" {
		// Idempotent success — already processed or in progress
		return nil
	}

	// 3. Lock order and re-read authoritative amount
	var dbAmount float64
	var storeID string
	var deliveryTrackingID string
	var orderStatus string
	err = tx.QueryRow(ctx,
		`SELECT total_amount, store_tracking_id, status FROM orders WHERE order_tracking_id = $1 FOR UPDATE`, orderID,
	).Scan(&dbAmount, &storeID, &orderStatus)
	if err != nil {
		return fmt.Errorf("order not found: %w", err)
	}
	if orderStatus == "paid" {
		return fmt.Errorf("conflict: order already paid by another transaction")
	}

	// Verify the authoritative amount hasn't changed since the payment was initiated
	if dbAmount != expectedAmount {
		return fmt.Errorf("order amount changed during settlement: expected %.2f, got %.2f", expectedAmount, dbAmount)
	}

	_ = tx.QueryRow(ctx,
		`SELECT COALESCE(d.tracking_id, '') FROM deliveries d WHERE d.order_tracking_id = $1`, orderID,
	).Scan(&deliveryTrackingID)

	split, err := h.calculator.CalculateSplit(ctx, dbAmount, storeID, deliveryTrackingID)
	if err != nil {
		return fmt.Errorf("split calculation failed: %w", err)
	}

	currency := envStr("DEFAULT_CURRENCY", "PKR")
	idempotencyKey := fmt.Sprintf("payfast:split:%s", gatewayTxnID)

	// 4. Mark payment as settlement_pending (NOT captured — that happens after ledger)
	_, err = tx.Exec(ctx,
		`UPDATE payment_transactions SET status = 'settlement_pending', gateway_txn_id = $1, updated_at = NOW() WHERE transaction_id = $2`,
		gatewayTxnID, internalTxnID,
	)
	if err != nil {
		return fmt.Errorf("failed to update payment transaction: %w", err)
	}

	// 5. Mark order as settlement_pending
	_, err = tx.Exec(ctx,
		`UPDATE orders SET admin_commission = $1, payment_status = 'settlement_pending' WHERE order_tracking_id = $2`,
		split.AdminRevenue, orderID,
	)
	if err != nil {
		return fmt.Errorf("failed to update order status for %s: %w", orderID, err)
	}

	// 6. Complete 3-Way Outbox Transfers (PayFastHolding -> AdminRevenue + VendorLockedEscrow + CentralEscrow)
	transfers := []map[string]interface{}{
		{
			"debit_account":  string(ledger.AccountPayFastHolding),
			"credit_account": string(ledger.AccountAdminRevenue),
			"amount":         split.AdminRevenue,
			"idempotency":    idempotencyKey + ":admin",
		},
		{
			"debit_account":  string(ledger.AccountPayFastHolding),
			"credit_account": string(ledger.AccountVendorLockedEscrow),
			"amount":         split.VendorEscrow,
			"idempotency":    idempotencyKey + ":vendor",
		},
	}
	if split.DeliveryEscrow > 0 {
		transfers = append(transfers, map[string]interface{}{
			"debit_account":  string(ledger.AccountPayFastHolding),
			"credit_account": string(ledger.AccountCentralEscrow),
			"amount":         split.DeliveryEscrow,
			"idempotency":    idempotencyKey + ":delivery",
		})
	}

	// Insert Settlement Outbox Event (atomic with DB state changes)
	outboxPayload, err := json.Marshal(map[string]interface{}{
		"internal_txn_id":      internalTxnID,
		"order_id":             orderID,
		"gateway_txn_id":       gatewayTxnID,
		"store_id":             storeID,
		"delivery_tracking_id": deliveryTrackingID,
		"total_amount":         dbAmount,
		"currency":             currency,
		"admin_revenue":        split.AdminRevenue,
		"vendor_escrow":        split.VendorEscrow,
		"delivery_escrow":      split.DeliveryEscrow,
		"idempotency_key":      idempotencyKey,
		"transfers":            transfers,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal outbox payload: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at) VALUES ($1, 'payment_settlement', $2, 'PENDING', NOW())`,
		orderID, string(outboxPayload),
	)
	if err != nil {
		return fmt.Errorf("failed to insert outbox event: %w", err)
	}

	// 7. Commit DB Transaction — this is the single atomic boundary
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
