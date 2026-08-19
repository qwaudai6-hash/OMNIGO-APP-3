package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/payment/payfast"
	"github.com/omnigo/backend/internal/payment_orchestrator"
)

// PaymentRequest contains client checkout parameters.
type PaymentRequest struct {
	OrderID          string `json:"order_id" binding:"required"`
	CustomerMobileNo string `json:"customer_mobile_no"`
	AccountTypeID    string `json:"account_type_id"`
	PaymentMethod    string `json:"payment_method"` // card | bank | wallet
	BankCode         string `json:"bank_code"`
	AccountNumber    string `json:"account_number"`
	AccountTitle     string `json:"account_title"`
	CNICNumber       string `json:"cnic_number"`
	CardNumber       string `json:"card_number"`
	ExpiryMonth      string `json:"expiry_month"`
	ExpiryYear       string `json:"expiry_year"`
	CVV              string `json:"cvv"`
	OTP              string `json:"otp"`
}

// Validate ensures input parameters meet financial security and card scheme formats.
func (req *PaymentRequest) Validate() error {
	if strings.TrimSpace(req.OrderID) == "" {
		return errors.New("order_id is required")
	}

	// Card validation
	if req.CardNumber != "" {
		cleanPan := strings.ReplaceAll(req.CardNumber, " ", "")
		if len(cleanPan) < 13 || len(cleanPan) > 19 {
			return errors.New("invalid card number length")
		}
		if len(req.CVV) < 3 || len(req.CVV) > 4 {
			return errors.New("invalid CVV (must be 3 or 4 digits)")
		}
		if len(req.ExpiryMonth) != 2 {
			return errors.New("invalid expiry month format (must be MM, e.g. '08')")
		}
		m, err := strconv.Atoi(req.ExpiryMonth)
		if err != nil || m < 1 || m > 12 {
			return errors.New("expiry month must be between 01 and 12")
		}
		if len(req.ExpiryYear) != 4 && len(req.ExpiryYear) != 2 {
			return errors.New("invalid expiry year format (must be YYYY, e.g. '2026')")
		}
		fullYear := req.ExpiryYear
		if len(fullYear) == 2 {
			fullYear = "20" + fullYear
		}
		y, err := strconv.Atoi(fullYear)
		if err != nil {
			return errors.New("invalid expiry year")
		}

		now := time.Now()
		currentYear := now.Year()
		currentMonth := int(now.Month())
		if y < currentYear || (y == currentYear && m < currentMonth) {
			return errors.New("card is expired")
		}
	} else if req.AccountNumber != "" {
		if strings.TrimSpace(req.AccountNumber) == "" {
			return errors.New("account number is required for bank/wallet payment")
		}
	}

	return nil
}

// PaymentResponse represents the immediate response from payment initiation.
type PaymentResponse struct {
	Status        string `json:"status"` // 3ds_redirect | settlement_pending | failed | gateway_pending
	Action        string `json:"action,omitempty"`
	ThreeDSHtml   string `json:"threed_html,omitempty"`
	OrderID       string `json:"order_id"`
	TransactionID string `json:"transaction_id"`
	Message       string `json:"message,omitempty"`
}

// PaymentMetadata stores non-sensitive gateway token state. Zero cardholder data stored.
type PaymentMetadata struct {
	InstrumentToken string                 `json:"instrument_token"`
	GatewayTxnID    string                 `json:"gateway_txn_id"`
	Data3DSSecureID string                 `json:"data_3ds_secureid"`
	ECI             string                 `json:"eci"`
	CustomerMobile  string                 `json:"customer_mobile"`
	AccountTypeID   string                 `json:"account_type"`
	CustomerIP      string                 `json:"customer_ip"`
	CalculatedSplit map[string]float64     `json:"calculated_split"`
}

// PayFastService orchestrates domain logic, database invariants, and PayFast API integration.
type PayFastService struct {
	db               *pgxpool.Pool
	ledger           *ledger.Service
	escrow           *escrow.Service
	calculator       *payment_orchestrator.CommissionCalculator
	payfast          *payfast.Client
	callbackSecret   string
	merchantCategory string
	callbackBaseURL  string
	defaultCurrency  string
}

// NewPayFastService constructs a thread-safe, production-ready PayFast service.
func NewPayFastService(
	db *pgxpool.Pool,
	ledgerSvc *ledger.Service,
	escrowSvc *escrow.Service,
	calc *payment_orchestrator.CommissionCalculator,
	payfastClient *payfast.Client,
) *PayFastService {
	cat := os.Getenv("PAYFAST_MERCHANT_CATEGORY")
	if cat == "" {
		cat = "0001" // Standard retail default fallback if unconfigured
	}
	secret := os.Getenv("INTERNAL_CALLBACK_SECRET")
	if secret == "" {
		if os.Getenv("GO_TEST_ENV") == "1" {
			secret = "test-internal-callback-secret-32-chars-long"
		} else {
			panic("INTERNAL_CALLBACK_SECRET environment variable is required")
		}
	}
	cbURL := os.Getenv("PAYFAST_3DS_CALLBACK_URL")
	if cbURL == "" {
		cbURL = "http://localhost:8080/api/v1/payments/payfast/3ds_callback"
	}
	currency := os.Getenv("DEFAULT_CURRENCY")
	if currency == "" {
		currency = "PKR"
	}

	return &PayFastService{
		db:               db,
		ledger:           ledgerSvc,
		escrow:           escrowSvc,
		calculator:       calc,
		payfast:          payfastClient,
		callbackSecret:   secret,
		merchantCategory: cat,
		callbackBaseURL:  cbURL,
		defaultCurrency:  currency,
	}
}

// generateHMACSHA256 generates a hex-encoded HMAC-SHA256 signature.
func (s *PayFastService) generateHMACSHA256(data string) string {
	h := hmac.New(sha256.New, []byte(s.callbackSecret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// SignMD generates an HMAC signature for the internal transaction ID.
func (s *PayFastService) SignMD(internalTxnID string) string {
	return internalTxnID + "." + s.generateHMACSHA256(internalTxnID)
}

// VerifyMD validates the HMAC signature in constant time.
func (s *PayFastService) VerifyMD(mdParam string) (string, error) {
	parts := strings.Split(mdParam, ".")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid MD format")
	}

	internalTxnID := parts[0]
	providedSignature := parts[1]
	expectedSignature := s.generateHMACSHA256(internalTxnID)

	if subtle.ConstantTimeCompare([]byte(providedSignature), []byte(expectedSignature)) != 1 {
		return "", fmt.Errorf("signature mismatch")
	}

	return internalTxnID, nil
}

// Build3DSCallbackURL safely constructs the 3DS callback URL with signed MD parameter.
func (s *PayFastService) Build3DSCallbackURL(internalTxnID string) (string, error) {
	signedMD := s.SignMD(internalTxnID)
	u, err := url.Parse(s.callbackBaseURL)
	if err != nil {
		return "", fmt.Errorf("invalid callback base URL: %w", err)
	}
	q := u.Query()
	q.Set("md", signedMD)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ProcessPayment handles the primary checkout initiation.
func (s *PayFastService) ProcessPayment(ctx context.Context, merchantUserID, clientIP string, req PaymentRequest) (*PaymentResponse, error) {
	// 1. Validate Input Payload
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	// 2. Fetch Authoritative Order Amount & Lock Order Row
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var expectedAmount float64
	var orderStatus string
	var customerTrackingID string
	var storeID string
	err = tx.QueryRow(ctx,
		`SELECT total_amount, status, customer_tracking_id, store_tracking_id 
		 FROM orders WHERE order_tracking_id = $1 FOR UPDATE`, req.OrderID,
	).Scan(&expectedAmount, &orderStatus, &customerTrackingID, &storeID)
	if err != nil {
		return nil, fmt.Errorf("order not found: %w", err)
	}
	if merchantUserID != customerTrackingID {
		return nil, errors.New("forbidden: order belongs to a different user")
	}
	if orderStatus != "pending" && orderStatus != "unpaid" {
		return nil, errors.New("order is not in a payable status")
	}

	internalTxnID := "pf_" + uuid.New().String()
	idempotencyKey := fmt.Sprintf("pf:%s:%s", req.OrderID, merchantUserID)

	// Check active attempts
	var activeAttempts int
	err = tx.QueryRow(ctx,
		`SELECT count(*) FROM payment_transactions 
		 WHERE order_tracking_id = $1 AND status IN ('processing', '3ds_required', 'settlement_pending', 'gateway_pending')`,
		req.OrderID,
	).Scan(&activeAttempts)
	if err != nil {
		return nil, fmt.Errorf("failed to check active attempts: %w", err)
	}
	if activeAttempts > 0 {
		return nil, errors.New("conflict: payment attempt is already in progress for this order")
	}

	// Fetch authoritative phone from users table with fallback
	var authoritativeMobile string
	err = tx.QueryRow(ctx, `SELECT COALESCE(phone, '') FROM users WHERE tracking_id = $1`, customerTrackingID).Scan(&authoritativeMobile)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user profile: %w", err)
	}
	if authoritativeMobile == "" && req.CustomerMobileNo != "" {
		authoritativeMobile = req.CustomerMobileNo
	}
	if authoritativeMobile == "" {
		return nil, errors.New("customer phone number is required for payment verification")
	}

	if req.AccountTypeID == "" {
		if req.CardNumber != "" {
			req.AccountTypeID = "2" // Card
		} else if req.AccountNumber != "" {
			req.AccountTypeID = "3" // Bank/Wallet
		} else {
			req.AccountTypeID = "1"
		}
	}

	// Pre-calculate Split and assert parity
	var deliveryTrackingID string
	_ = tx.QueryRow(ctx, `SELECT COALESCE(tracking_id, '') FROM deliveries WHERE order_tracking_id = $1`, req.OrderID).Scan(&deliveryTrackingID)
	split, err := s.calculator.CalculateSplit(ctx, expectedAmount, storeID, deliveryTrackingID)
	if err != nil {
		return nil, fmt.Errorf("commission split calculation failed: %w", err)
	}
	totalCalculated := split.AdminRevenue + split.VendorEscrow + split.DeliveryEscrow
	if math.Abs(totalCalculated-expectedAmount) > 0.01 {
		return nil, fmt.Errorf("split parity error: calculated %.2f vs total %.2f", totalCalculated, expectedAmount)
	}

	splitMeta := map[string]float64{
		"admin_revenue":   split.AdminRevenue,
		"vendor_escrow":   split.VendorEscrow,
		"delivery_escrow": split.DeliveryEscrow,
	}
	metaBytes, _ := json.Marshal(PaymentMetadata{
		CustomerMobile:  authoritativeMobile,
		AccountTypeID:   req.AccountTypeID,
		CustomerIP:      clientIP,
		CalculatedSplit: splitMeta,
	})

	// 3. Insert Initial Payment Transaction (idempotency key populated)
	_, err = tx.Exec(ctx,
		`INSERT INTO payment_transactions (transaction_id, order_tracking_id, gateway, amount, currency, status, kind, idempotency_key, metadata)
		 VALUES ($1, $2, 'payfast', $3, $4, 'pending', 'payment', $5, $6)`,
		internalTxnID, req.OrderID, expectedAmount, s.defaultCurrency, idempotencyKey, metaBytes,
	)
	if err != nil {
		if strings.Contains(err.Error(), "ux_payment_active_order") || strings.Contains(err.Error(), "unique constraint") {
			return nil, errors.New("conflict: payment attempt is already in progress")
		}
		return nil, fmt.Errorf("failed to record payment attempt: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit initial payment attempt: %w", err)
	}

	// Zero card persistence cleanup
	defer func() {
		req.CardNumber = ""
		req.CVV = ""
		req.AccountNumber = ""
		req.CNICNumber = ""
	}()

	callbackURL, err := s.Build3DSCallbackURL(internalTxnID)
	if err != nil {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Failed to build callback URL: "+err.Error())
		return nil, err
	}

	// 4. Request Temporary Transaction Token from PayFast
	tokenReq := payfast.TemporaryTokenRequest{
		MerchantUserId:     customerTrackingID,
		CustomerMobileNo:   authoritativeMobile,
		BasketID:           req.OrderID,
		OrderDate:          time.Now().Format("2006-01-02 15:04:05"),
		TxnAmt:             fmt.Sprintf("%.2f", expectedAmount),
		CustomerIP:         clientIP,
		AccountTypeID:      req.AccountTypeID,
		MerCatCode:         s.merchantCategory,
		CardNumber:         req.CardNumber,
		ExpiryMonth:        req.ExpiryMonth,
		ExpiryYear:         req.ExpiryYear,
		CVV:                req.CVV,
		AccountNumber:      req.AccountNumber,
		CNICNumber:         req.CNICNumber,
		Data3DSPagemode:    "SIMPLE",
		Data3DSCallbackURL: callbackURL,
	}

	tokenRes, err := s.payfast.GetTemporaryTransactionToken(ctx, tokenReq)
	if err != nil {
		// If initial token acquisition fails before any gateway token exists,
		// NO charge could possibly have occurred, so fail deterministically.
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Temporary token request failed: "+err.Error())
		return &PaymentResponse{
			Status:        "failed",
			OrderID:       req.OrderID,
			TransactionID: internalTxnID,
			Message:       "Failed to communicate with payment gateway",
		}, err
	}

	// 5. Evaluate Token Response (3DS Required vs Direct Tokenized Capture)
	if tokenRes.Data3DSHTML != "" {
		metaBytes, _ = json.Marshal(PaymentMetadata{
			InstrumentToken: tokenRes.InstrumentToken,
			GatewayTxnID:    tokenRes.TransactionID,
			Data3DSSecureID: tokenRes.Data3DSSecureID,
			ECI:             tokenRes.ECI.String(),
			CustomerMobile:  authoritativeMobile,
			AccountTypeID:   req.AccountTypeID,
			CustomerIP:      clientIP,
			CalculatedSplit: splitMeta,
		})

		_, _ = s.db.Exec(ctx,
			`UPDATE payment_transactions 
			 SET status = '3ds_required', gateway_txn_id = $1, metadata = $2, updated_at = NOW() 
			 WHERE transaction_id = $3`,
			tokenRes.TransactionID, metaBytes, internalTxnID,
		)

		return &PaymentResponse{
			Status:        "3ds_redirect",
			Action:        "3ds_redirect",
			ThreeDSHtml:   tokenRes.Data3DSHTML,
			OrderID:       req.OrderID,
			TransactionID: internalTxnID,
		}, nil
	}

	// 6. Direct Tokenized Capture (3DS Not Required)
	if tokenRes.InstrumentToken == "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Gateway returned empty instrument token")
		return nil, errors.New("invalid gateway token response")
	}

	// Mark processing
	_, _ = s.db.Exec(ctx, `UPDATE payment_transactions SET status = 'processing', updated_at = NOW() WHERE transaction_id = $1`, internalTxnID)

	txnReq := payfast.TokenizedTransactionRequest{
		InstrumentToken:  tokenRes.InstrumentToken,
		TransactionID:    tokenRes.TransactionID,
		MerchantUserId:   customerTrackingID,
		CustomerMobileNo: authoritativeMobile,
		BasketID:         req.OrderID,
		OrderDate:        time.Now().Format("2006-01-02 15:04:05"),
		TxnDesc:          "OmniGo Order " + req.OrderID,
		TxnAmt:           fmt.Sprintf("%.2f", expectedAmount),
		CustomerIP:       clientIP,
		MerCatCode:       s.merchantCategory,
		Otp:              req.OTP,
	}

	txnRes, err := s.payfast.InitiateTokenizedTransaction(ctx, txnReq)
	if err != nil {
		if payfast.IsTransient(err) {
			_ = s.MarkPaymentGatewayPending(ctx, internalTxnID, tokenRes.TransactionID, "Direct tokenized txn timeout: "+err.Error())
			return &PaymentResponse{
				Status:        "gateway_pending",
				OrderID:       req.OrderID,
				TransactionID: internalTxnID,
				Message:       "Payment is processing at gateway; reconciliation in progress",
			}, nil
		}

		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Tokenized capture failed: "+err.Error())
		return &PaymentResponse{
			Status:        "failed",
			OrderID:       req.OrderID,
			TransactionID: internalTxnID,
			Message:       "Payment was rejected by gateway",
		}, err
	}

	if txnRes == nil || txnRes.TransactionID == "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Gateway returned empty transaction ID")
		return nil, errors.New("invalid gateway transaction response")
	}
	if txnRes.StatusCode != "00" && txnRes.StatusCode != "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, txnRes.StatusMsg)
		return nil, fmt.Errorf("gateway error: %s (code: %s)", txnRes.StatusMsg, txnRes.StatusCode)
	}

	// 7. Verify & Settle
	if err := s.VerifyAndSettle(ctx, internalTxnID, req.OrderID, txnRes.TransactionID, expectedAmount); err != nil {
		return nil, err
	}

	return &PaymentResponse{
		Status:        "settlement_pending",
		OrderID:       req.OrderID,
		TransactionID: internalTxnID,
		Message:       "Payment verified successfully",
	}, nil
}

// Handle3DSCallback processes the ACS form post callback with replay defense.
func (s *PayFastService) Handle3DSCallback(ctx context.Context, mdParam, paRes, clientIP string) (string, error) {
	internalTxnID, err := s.VerifyMD(mdParam)
	if err != nil {
		return "", fmt.Errorf("invalid MD signature: %w", err)
	}

	// 1. Fetch 3DS Payment Record with Replay Defense Guard
	var orderID string
	var amount float64
	var metaJSON []byte
	var customerTrackingID string
	err = s.db.QueryRow(ctx,
		`SELECT pt.order_tracking_id, pt.amount, pt.metadata, o.customer_tracking_id 
		 FROM payment_transactions pt
		 JOIN orders o ON o.order_tracking_id = pt.order_tracking_id
		 WHERE pt.transaction_id = $1 AND pt.status = '3ds_required'`,
		internalTxnID,
	).Scan(&orderID, &amount, &metaJSON, &customerTrackingID)

	if err != nil {
		return "", errors.New("no pending 3DS payment found for transaction (possible replay or already finalized)")
	}

	var meta PaymentMetadata
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return "", fmt.Errorf("invalid payment metadata: %w", err)
	}

	// 2. Mark as processing and record callback_processed_at timestamp atomically
	res, err := s.db.Exec(ctx,
		`UPDATE payment_transactions 
		 SET status = 'processing', callback_processed_at = NOW(), updated_at = NOW() 
		 WHERE transaction_id = $1 AND status = '3ds_required'
		   AND (callback_processed_at IS NULL OR callback_processed_at < NOW() - INTERVAL '1 minute')`,
		internalTxnID,
	)
	if err != nil || res.RowsAffected() == 0 {
		return "", errors.New("conflict: 3DS callback is already processing or was recently processed")
	}

	origCustomerIP := meta.CustomerIP
	if origCustomerIP == "" {
		origCustomerIP = clientIP
	}

	// 3. Initiate Tokenized Transaction with 3DS PaRes
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
		MerCatCode:       s.merchantCategory,
		ECI:              meta.ECI,
		Data3DSSecureID:  meta.Data3DSSecureID,
		Data3DSPaRes:     paRes,
	}

	txnRes, err := s.payfast.InitiateTokenizedTransaction(ctx, txnReq)
	if err != nil {
		if payfast.IsTransient(err) {
			_ = s.MarkPaymentGatewayPending(ctx, internalTxnID, meta.GatewayTxnID, "Tokenized 3DS txn timeout: "+err.Error())
			return orderID, fmt.Errorf("gateway timeout during 3DS transaction: %w", err)
		}
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Tokenized 3DS txn rejected: "+err.Error())
		return orderID, fmt.Errorf("3DS payment rejected: %w", err)
	}

	if txnRes == nil || txnRes.TransactionID == "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Gateway returned empty transaction ID after 3DS")
		return orderID, errors.New("invalid gateway response")
	}
	if txnRes.StatusCode != "00" && txnRes.StatusCode != "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, txnRes.StatusMsg)
		return orderID, fmt.Errorf("gateway rejection: %s", txnRes.StatusMsg)
	}

	// 4. Verify Status & Settle
	if err := s.VerifyAndSettle(ctx, internalTxnID, orderID, txnRes.TransactionID, amount); err != nil {
		return orderID, err
	}

	return orderID, nil
}

// VerifyAndSettle queries PayFast status and settles the database transaction atomically.
func (s *PayFastService) VerifyAndSettle(ctx context.Context, internalTxnID, orderID, gatewayTxnID string, expectedAmount float64) error {
	if gatewayTxnID == "" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Missing gateway transaction ID for status verification")
		return errors.New("invalid gateway transaction ID")
	}

	statusRes, err := s.payfast.GetTransactionStatus(ctx, gatewayTxnID)
	if err != nil {
		if payfast.IsTransient(err) {
			_ = s.MarkPaymentGatewayPending(ctx, internalTxnID, gatewayTxnID, "Status verification timeout: "+err.Error())
			return fmt.Errorf("status verification timeout: %w", err)
		}
		_ = s.MarkPaymentFailed(ctx, internalTxnID, "Status verification error: "+err.Error())
		return err
	}

	if statusRes.StatusCode != "00" {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, fmt.Sprintf("Gateway status '%s': %s", statusRes.StatusCode, statusRes.StatusMsg))
		return fmt.Errorf("gateway status rejection: %s", statusRes.StatusMsg)
	}

	if statusRes.BasketID != "" && statusRes.BasketID != orderID {
		_ = s.MarkPaymentFailed(ctx, internalTxnID, fmt.Sprintf("Basket ID mismatch: expected %s, got %s", orderID, statusRes.BasketID))
		return errors.New("basket ID mismatch")
	}

	if statusRes.TxnAmt != "" {
		gatewayAmt, parseErr := strconv.ParseFloat(statusRes.TxnAmt, 64)
		if parseErr == nil {
			expectedPaisa := int64(math.Round(expectedAmount * 100))
			gatewayPaisa := int64(math.Round(gatewayAmt * 100))
			if expectedPaisa != gatewayPaisa {
				_ = s.MarkPaymentFailed(ctx, internalTxnID, fmt.Sprintf("Amount mismatch: expected %d paisa, got %d paisa", expectedPaisa, gatewayPaisa))
				return errors.New("transaction amount mismatch")
			}
		}
	}

	return s.ExecuteSplit(ctx, internalTxnID, orderID, expectedAmount, gatewayTxnID)
}

// ExecuteSplit performs atomic double-entry split preparation and outbox event enqueueing.
func (s *PayFastService) ExecuteSplit(ctx context.Context, internalTxnID, orderID string, expectedAmount float64, gatewayTxnID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Idempotency lock on payment_transactions
	var currentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM payment_transactions WHERE transaction_id = $1 FOR UPDATE`, internalTxnID).Scan(&currentStatus)
	if err != nil {
		return fmt.Errorf("payment transaction not found: %w", err)
	}
	if currentStatus == "captured" || currentStatus == "settlement_pending" {
		return nil // Idempotent success
	}

	// Lock order and re-verify authoritative state
	var dbAmount float64
	var storeID string
	var orderStatus string
	err = tx.QueryRow(ctx,
		`SELECT total_amount, store_tracking_id, status FROM orders WHERE order_tracking_id = $1 FOR UPDATE`, orderID,
	).Scan(&dbAmount, &storeID, &orderStatus)
	if err != nil {
		return fmt.Errorf("order not found: %w", err)
	}
	if orderStatus == "paid" {
		return errors.New("conflict: order already paid by another transaction")
	}
	if dbAmount != expectedAmount {
		return fmt.Errorf("order amount changed: expected %.2f, got %.2f", expectedAmount, dbAmount)
	}

	var deliveryTrackingID string
	_ = tx.QueryRow(ctx, `SELECT COALESCE(tracking_id, '') FROM deliveries WHERE order_tracking_id = $1`, orderID).Scan(&deliveryTrackingID)

	split, err := s.calculator.CalculateSplit(ctx, dbAmount, storeID, deliveryTrackingID)
	if err != nil {
		return fmt.Errorf("split calculation failed: %w", err)
	}

	idempotencyKey := fmt.Sprintf("payfast:split:%s", gatewayTxnID)

	// Update payment_transactions status
	_, err = tx.Exec(ctx,
		`UPDATE payment_transactions 
		 SET status = 'settlement_pending', gateway_txn_id = $1, updated_at = NOW() 
		 WHERE transaction_id = $2`,
		gatewayTxnID, internalTxnID,
	)
	if err != nil {
		return fmt.Errorf("failed to update payment transaction: %w", err)
	}

	// Update orders payment status and all split columns
	_, err = tx.Exec(ctx,
		`UPDATE orders 
		 SET admin_commission = $1, vendor_escrow = $2, delivery_escrow = $3, payment_status = 'settlement_pending', updated_at = NOW() 
		 WHERE order_tracking_id = $4`,
		split.AdminRevenue, split.VendorEscrow, split.DeliveryEscrow, orderID,
	)
	if err != nil {
		return fmt.Errorf("failed to update order: %w", err)
	}

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

	outboxPayload, err := json.Marshal(map[string]interface{}{
		"internal_txn_id":      internalTxnID,
		"order_id":             orderID,
		"gateway_txn_id":       gatewayTxnID,
		"store_id":             storeID,
		"delivery_tracking_id": deliveryTrackingID,
		"total_amount":         dbAmount,
		"currency":             s.defaultCurrency,
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
		`INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at, updated_at) 
		 VALUES ($1, 'payment_settlement', $2, 'PENDING', NOW(), NOW())`,
		orderID, string(outboxPayload),
	)
	if err != nil {
		return fmt.Errorf("failed to insert outbox event: %w", err)
	}

	return tx.Commit(ctx)
}

// MarkPaymentFailed updates payment status to failed with robust error checking.
func (s *PayFastService) MarkPaymentFailed(ctx context.Context, internalTxnID, reason string) error {
	res, err := s.db.Exec(ctx,
		`UPDATE payment_transactions 
		 SET status = 'failed', error_message = $1, updated_at = NOW()
		 WHERE transaction_id = $2 AND status IN ('pending', '3ds_required', 'processing', 'gateway_pending')`,
		reason, internalTxnID,
	)
	if err != nil {
		log.Printf("[PayFastService] DB error marking %s failed: %v", internalTxnID, err)
		return fmt.Errorf("db error marking failed: %w", err)
	}
	if res.RowsAffected() == 0 {
		log.Printf("[PayFastService] No active row found to mark failed for %s", internalTxnID)
	}
	return nil
}

// MarkPaymentGatewayPending updates payment status to gateway_pending on transient timeouts.
func (s *PayFastService) MarkPaymentGatewayPending(ctx context.Context, internalTxnID, gatewayTxnID, reason string) error {
	res, err := s.db.Exec(ctx,
		`UPDATE payment_transactions 
		 SET status = 'gateway_pending', gateway_txn_id = COALESCE(NULLIF($1, ''), gateway_txn_id), error_message = $2, updated_at = NOW()
		 WHERE transaction_id = $3 AND status IN ('pending', '3ds_required', 'processing')`,
		gatewayTxnID, reason, internalTxnID,
	)
	if err != nil {
		log.Printf("[PayFastService] DB error marking %s gateway_pending: %v", internalTxnID, err)
		return fmt.Errorf("db error marking gateway_pending: %w", err)
	}
	if res.RowsAffected() == 0 {
		log.Printf("[PayFastService] No active row found to mark gateway_pending for %s", internalTxnID)
	}
	return nil
}
