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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/payment/payfast"
	"github.com/omnigo/backend/internal/payment_orchestrator"
	"github.com/omnigo/backend/internal/payment_orchestrator/fraud"
	"github.com/omnigo/backend/internal/shared/telemetry"
)

// Classification sentinels. Handlers must classify HTTP status codes via errors.Is()
// against these — never by substring-matching error text, which breaks silently the
// moment any message wording changes.
var (
	ErrValidation = errors.New("payfast: invalid request")
	ErrNotFound   = errors.New("payfast: resource not found")
	ErrForbidden  = errors.New("payfast: forbidden")
	ErrConflict   = errors.New("payfast: conflicting state")
)

// PaymentRequest contains client checkout parameters.
type PaymentRequest struct {
	OrderID           string `json:"order_id" binding:"required"`
	CustomerMobileNo  string `json:"customer_mobile_no"`
	AccountTypeID     string `json:"account_type_id"`
	PaymentMethod     string `json:"payment_method"` // card | bank | wallet | saved_card
	SavedCardID       string `json:"saved_card_id,omitempty"`
	SaveCardForFuture bool   `json:"save_card_for_future,omitempty"`
	BankCode          string `json:"bank_code"`
	AccountNumber     string `json:"account_number"`
	AccountTitle      string `json:"account_title"`
	CNICNumber        string `json:"cnic_number"`
	CardNumber        string `json:"card_number"`
	ExpiryMonth       string `json:"expiry_month"`
	ExpiryYear        string `json:"expiry_year"`
	CVV               string `json:"cvv"`
	OTP               string `json:"otp"`

	// Optional client-supplied idempotency token (usually injected by the handler from
	// the Idempotency-Key header). Retrying the SAME checkout with the same key returns
	// a stable replay of the original attempt instead of charging twice.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Validate ensures input parameters meet financial security and card scheme formats.
func (req *PaymentRequest) Validate() error {
	if strings.TrimSpace(req.OrderID) == "" {
		return errors.New("order_id is required")
	}

	// 1-Click Saved Card Checkout
	if req.SavedCardID != "" {
		return nil
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
	Status        string `json:"status"` // 3ds_redirect | settlement_pending | failed | gateway_pending | hosted_redirect
	Action        string `json:"action,omitempty"`
	ThreeDSHtml   string `json:"threed_html,omitempty"`
	RedirectURL   string `json:"redirect_url,omitempty"`
	OrderID       string `json:"order_id"`
	TransactionID string `json:"transaction_id"`
	Message       string `json:"message,omitempty"`
}

// PaymentMetadata stores non-sensitive gateway token state. Zero cardholder data stored.
type PaymentMetadata struct {
	InstrumentToken string             `json:"instrument_token"`
	GatewayTxnID    string             `json:"gateway_txn_id"`
	Data3DSSecureID string             `json:"data_3ds_secureid"`
	ECI             string             `json:"eci"`
	CustomerMobile  string             `json:"customer_mobile"`
	AccountTypeID   string             `json:"account_type"`
	CustomerIP      string             `json:"customer_ip"`
	CalculatedSplit map[string]float64 `json:"calculated_split"`
}

// PayFastService orchestrates domain logic, database invariants, and PayFast API integration.
type PayFastService struct {
	db               *pgxpool.Pool
	ledger           *ledger.Service
	escrow           *escrow.Service
	calculator       *payment_orchestrator.CommissionCalculator
	payfast          *payfast.Client
	vault            *CardVaultService
	fraud            *fraud.Detector
	callbackSecret   string
	merchantCategory string
	callbackBaseURL  string
	checkoutURL      string
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
		secret = os.Getenv("HMAC_SECRET")
		if secret == "" {
			secret = "XLZg8xSIgUncVPqiObww9hRzOVc5Y68E+5xjB0+ac7c="
		}
	}
	publicBase := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")
	if publicBase == "" {
		publicBase = "https://omnigo-app-3-production.up.railway.app"
	}
	cbURL := os.Getenv("PAYFAST_3DS_CALLBACK_URL")
	if cbURL == "" {
		cbURL = publicBase + "/api/v1/payments/payfast/3ds_callback"
	}
	checkoutURL := os.Getenv("PAYFAST_CHECKOUT_URL")
	if checkoutURL == "" {
		checkoutURL = publicBase + "/api/v1/payments/payfast/ipn"
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
		vault:            NewCardVaultService(db),
		fraud:            fraud.NewDetector(nil, db),
		callbackSecret:   secret,
		merchantCategory: cat,
		callbackBaseURL:  cbURL,
		checkoutURL:      checkoutURL,
		defaultCurrency:  currency,
	}
}

// SetVault assigns a custom CardVaultService.
func (s *PayFastService) SetVault(vault *CardVaultService) {
	s.vault = vault
}

// SetFraudDetector assigns a custom FraudDetector.
func (s *PayFastService) SetFraudDetector(f *fraud.Detector) {
	s.fraud = f
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

// idempotentReplayResponse maps a previously-recorded transaction's DB status onto a
// stable, client-safe response for idempotent key replays.
func idempotentReplayResponse(dbStatus, orderID, txnID string) *PaymentResponse {
	resp := &PaymentResponse{
		OrderID:       orderID,
		TransactionID: txnID,
	}
	switch dbStatus {
	case "settlement_pending", "captured", "authorized":
		resp.Status = "settlement_pending"
		resp.Message = "Payment already verified for this idempotency key; settlement in progress"
	case "3ds_required":
		resp.Status = "in_progress"
		resp.Action = "3ds_redirect"
		resp.Message = "3-D Secure verification already in progress for this idempotency key; complete the challenge window you already opened"
	case "gateway_pending":
		resp.Status = "gateway_pending"
		resp.Message = "Payment is processing at gateway; reconciliation in progress"
	default: // pending, processing
		resp.Status = "in_progress"
		resp.Message = "Payment attempt already in progress for this idempotency key"
	}
	return resp
}

// gatewayContext derives a detached, deadline-bounded context for outbound
// gateway calls: cancellation from the originating HTTP request is dropped,
// and the deadline tracks the client's configured HTTP timeout plus a small
// local processing buffer instead of an arbitrary constant.
func (s *PayFastService) gatewayContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := s.payfast.Timeout() + 5*time.Second
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// ProcessPayment handles the primary checkout initiation.
func (s *PayFastService) ProcessPayment(ctx context.Context, merchantUserID, clientIP string, req *PaymentRequest) (resp *PaymentResponse, err error) {
	method := req.PaymentMethod
	if method == "" {
		if req.SavedCardID != "" {
			method = "saved_card"
		} else if req.CardNumber != "" {
			method = "card"
		} else {
			method = "bank_account"
		}
	}

	// Records the final outcome exactly once, from whichever of ProcessPayment's ~20 return
	// points is actually hit — named returns + defer means every exit path is covered without
	// scattering telemetry calls (and the risk of missing one) throughout the function body.
	defer func() {
		outcome := "failed"
		if err == nil && resp != nil {
			switch resp.Status {
			case "3ds_redirect":
				outcome = "three_ds_required"
			case "gateway_pending":
				outcome = "gateway_pending"
			case "settlement_pending", "succeeded":
				outcome = "succeeded"
			default:
				outcome = resp.Status
			}
		}
		telemetry.RecordPaymentOutcome(method, outcome)
	}()

	// 1. Validate Input Payload
	if verr := req.Validate(); verr != nil {
		return nil, fmt.Errorf("%w: %v", ErrValidation, verr)
	}

	// Optional client idempotency key: normalize and bound its size before it reaches
	// the DB (column is VARCHAR(255); "pf:" prefix consumes 3).
	if clientKey := strings.TrimSpace(req.IdempotencyKey); clientKey != "" {
		if len(clientKey) > 200 {
			return nil, fmt.Errorf("%w: idempotency key too long (max 200 characters)", ErrValidation)
		}
		req.IdempotencyKey = clientKey
	}

	telemetry.RecordPaymentAttempt("payfast", method)

	// 2. Run Pre-Authorization Fraud & Velocity Checks
	if s.fraud != nil {
		if err := s.fraud.CheckVelocity(ctx, merchantUserID, clientIP); err != nil {
			telemetry.RecordFraudBlock("velocity_limit_exceeded")
			return nil, err
		}
	}

	// 3. Fetch Authoritative Order Amount & Lock Order Row
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
		return nil, fmt.Errorf("%w: order not found: %v", ErrNotFound, err)
	}
	if merchantUserID != customerTrackingID {
		return nil, fmt.Errorf("%w: order belongs to a different user", ErrForbidden)
	}
	if orderStatus != "pending" && orderStatus != "unpaid" {
		return nil, fmt.Errorf("%w: order is not in a payable status", ErrConflict)
	}

	if s.fraud != nil {
		if err := s.fraud.CheckOrderAnomaly(ctx, merchantUserID, expectedAmount); err != nil {
			log.Printf("[PayFastService] Anomaly detected: %v", err)
		}
	}

	internalTxnID := "pf_" + uuid.New().String()

	// Idempotency key resolution:
	//   - With a client-supplied key, retries of the same checkout collapse onto one
	//     transaction (UNIQUE constraint on payment_transactions.idempotency_key).
	//   - Without one, the key is per-attempt and real protection comes from the
	//     active-attempt guard + unique partial index ux_payment_active_order.
	idempotencyKey := fmt.Sprintf("pf:%s:%s", req.OrderID, internalTxnID)
	if req.IdempotencyKey != "" {
		idempotencyKey = "pf:" + req.IdempotencyKey
	}

	// Check active attempts
	var activeAttempts int
	err = tx.QueryRow(ctx,
		`SELECT count(*) FROM payment_transactions 
		 WHERE order_tracking_id = $1 AND status IN ('pending', 'processing', '3ds_required', 'settlement_pending', 'gateway_pending')`,
		req.OrderID,
	).Scan(&activeAttempts)
	if err != nil {
		return nil, fmt.Errorf("failed to check active attempts: %w", err)
	}

	// Idempotent replay: if this exact client key already produced a live/completed
	// transaction for THIS order, return its state verbatim instead of erroring or
	// double-charging. Terminal (failed/refunded) rows fall through so a genuine
	// retry-after-failure can proceed under a derived per-attempt key.
	if req.IdempotencyKey != "" {
		var existTxnID, existOrderID, existStatus string
		qErr := tx.QueryRow(ctx,
			`SELECT transaction_id, order_tracking_id, status 
			 FROM payment_transactions WHERE idempotency_key = $1`,
			idempotencyKey,
		).Scan(&existTxnID, &existOrderID, &existStatus)
		if qErr == nil {
			if existOrderID != req.OrderID {
				return nil, fmt.Errorf("%w: idempotency key was already used for a different order", ErrConflict)
			}
			switch existStatus {
			case "failed", "refunded", "reversed", "chargeback":
				idempotencyKey = fmt.Sprintf("%s:r-%s", idempotencyKey, internalTxnID)
			default:
				return idempotentReplayResponse(existStatus, existOrderID, existTxnID), nil
			}
		} else if !errors.Is(qErr, pgx.ErrNoRows) {
			return nil, fmt.Errorf("failed to check idempotency key: %w", qErr)
		}
	}

	if activeAttempts > 0 {
		return nil, fmt.Errorf("%w: payment attempt is already in progress for this order", ErrConflict)
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
		if req.SavedCardID != "" || req.CardNumber != "" {
			req.AccountTypeID = "2" // Card
		} else if req.AccountNumber != "" {
			req.AccountTypeID = "3" // Bank/Wallet
		} else {
			req.AccountTypeID = "1"
		}
	}

	// Pre-calculate Split and assert exact zero-tolerance paisa parity
	var deliveryTrackingID string
	if scanErr := tx.QueryRow(ctx, `SELECT COALESCE(tracking_id, '') FROM deliveries WHERE order_tracking_id = $1`, req.OrderID).Scan(&deliveryTrackingID); scanErr != nil {
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			log.Printf("ERROR: delivery tracking id lookup error for order %s: %v", req.OrderID, scanErr)
		}
	}
	split, err := s.calculator.CalculateSplit(ctx, expectedAmount, storeID, deliveryTrackingID)
	if err != nil {
		return nil, fmt.Errorf("commission split calculation failed: %w", err)
	}
	totalCalculated := split.AdminRevenue + split.VendorEscrow + split.DeliveryEscrow
	expectedPaisa := int64(math.Round(expectedAmount * 100))
	calculatedPaisa := int64(math.Round(totalCalculated * 100))
	if expectedPaisa != calculatedPaisa {
		return nil, fmt.Errorf("split parity error: calculated %d paisa vs total %d paisa", calculatedPaisa, expectedPaisa)
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

	// 4. Insert Initial Payment Transaction (idempotency key populated)
	_, err = tx.Exec(ctx,
		`INSERT INTO payment_transactions (transaction_id, order_tracking_id, gateway, amount, currency, status, kind, idempotency_key, metadata)
		 VALUES ($1, $2, 'payfast', $3, $4, 'pending', 'payment', $5, $6::jsonb)`,
		internalTxnID, req.OrderID, expectedAmount, s.defaultCurrency, idempotencyKey, string(metaBytes),
	)
	if err != nil {
		if strings.Contains(err.Error(), "ux_payment_active_order") || strings.Contains(err.Error(), "unique constraint") || strings.Contains(err.Error(), "duplicate key") {
			return nil, fmt.Errorf("%w: payment attempt is already in progress", ErrConflict)
		}
		return nil, fmt.Errorf("failed to record payment attempt: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit initial payment attempt: %w", err)
	}

	// ── Hosted Checkout Redirect (apps.net.pk) ─────────────────────────────
	// apps.net.pk does NOT expose /transaction/token — only hosted checkout via
	// /Transaction/PostTransaction. Detect the gateway variant and redirect the
	// customer to PayFast's hosted payment page instead of calling the token API.
	if strings.Contains(s.payfast.BaseURL(), "apps.net.pk") {
		publicBase := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")
		returnURL := os.Getenv("WALLET_RETURN_URL")
		if returnURL == "" && publicBase != "" {
			returnURL = publicBase + "/api/v1/payments/payfast/ipn"
		}
		if returnURL == "" {
			returnURL = s.checkoutURL
		}

		hostedURL := fmt.Sprintf(
			"%s/Transaction/PostTransaction?merchant_id=%s&basket_id=%s&txnamt=%.2f&currency_code=PKR&customer_mobile_no=%s&customer_email_address=&success_url=%s&checkout_url=%s",
			strings.TrimRight(s.payfast.BaseURL(), "/"),
			url.QueryEscape(s.payfast.MerchantID()),
			url.QueryEscape(req.OrderID),
			expectedAmount,
			url.QueryEscape(authoritativeMobile),
			url.QueryEscape(returnURL),
			url.QueryEscape(returnURL),
		)

		log.Printf("[PayFastService] apps.net.pk detected — returning hosted redirect for order %s (txn %s)", req.OrderID, internalTxnID)
		return &PaymentResponse{
			Status:        "hosted_redirect",
			RedirectURL:   hostedURL,
			OrderID:       req.OrderID,
			TransactionID: internalTxnID,
			Message:       "Redirecting to PayFast hosted checkout",
		}, nil
	}

	// ── Saved Card 1-Click Checkout Flow ───────────────────────────────────
	if req.SavedCardID != "" && s.vault != nil {
		savedToken, err := s.vault.GetCardInstrumentToken(ctx, merchantUserID, req.SavedCardID)
		if err != nil {
			if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Invalid saved card token: "+err.Error()); markErr != nil {
				log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
			}
			return nil, err
		}

		if _, execErr := s.db.Exec(ctx, `UPDATE payment_transactions SET status = 'processing', updated_at = NOW() WHERE transaction_id = $1`, internalTxnID); execErr != nil {
			log.Printf("WARNING: failed to update payment %s to 'processing': %v", internalTxnID, execErr)
		}

		txnReq := payfast.TokenizedTransactionRequest{
			InstrumentToken:  savedToken,
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

		gwCtx, gwCancel := s.gatewayContext(ctx)
		defer gwCancel()
		txnRes, err := s.payfast.InitiateTokenizedTransaction(gwCtx, txnReq)
		if err != nil {
			if s.fraud != nil {
				s.fraud.RecordAttempt(ctx, merchantUserID, clientIP, false)
			}
			if payfast.IsTransient(err) {
				if markErr := s.MarkPaymentGatewayPending(ctx, internalTxnID, "", "Saved card timeout: "+err.Error()); markErr != nil {
					log.Printf("CRITICAL: failed to update payment status via MarkPaymentGatewayPending for txn %s: %v", internalTxnID, markErr)
				}
				return &PaymentResponse{
					Status:        "gateway_pending",
					OrderID:       req.OrderID,
					TransactionID: internalTxnID,
					Message:       "Payment is processing at gateway; reconciliation in progress",
				}, nil
			}
			if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Saved card capture failed: "+err.Error()); markErr != nil {
				log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
			}
			return nil, fmt.Errorf("saved card payment failed: %w", err)
		}

		// Issuer step-up: some banks demand a 3-D Secure challenge even for tokenized
		// (saved-card) transactions. Route through the same 3DS callback machinery as
		// new-card payments — persist instrument/split state and hand the challenge HTML
		// back to the client — instead of misreporting an otherwise healthy payment as failed.
		if txnRes != nil && txnRes.Data3DSHTML != "" {
			metaBytes, _ = json.Marshal(PaymentMetadata{
				InstrumentToken: savedToken,
				GatewayTxnID:    txnRes.TransactionID,
				Data3DSSecureID: txnRes.Data3DSSecureID,
				ECI:             txnRes.ECI.String(),
				CustomerMobile:  authoritativeMobile,
				AccountTypeID:   req.AccountTypeID,
				CustomerIP:      clientIP,
				CalculatedSplit: splitMeta,
			})

			// This row is what Handle3DSCallback resumes from; if the write fails we must
			// not send the customer into the ACS challenge with no resumable local state.
			res, execErr := s.db.Exec(ctx,
				`UPDATE payment_transactions 
				 SET status = '3ds_required', gateway_txn_id = NULLIF($1, ''), metadata = $2::jsonb, updated_at = NOW() 
				 WHERE transaction_id = $3 AND status IN ('pending', 'processing')`,
				txnRes.TransactionID, string(metaBytes), internalTxnID,
			)
			if execErr != nil || res.RowsAffected() == 0 {
				failReason := "failed to persist saved-card 3DS state"
				if execErr != nil {
					failReason = "failed to persist saved-card 3DS state: " + execErr.Error()
				} else {
					failReason = "saved-card transaction no longer active for 3DS step-up"
				}
				log.Printf("CRITICAL: %s for txn %s", failReason, internalTxnID)
				if markErr := s.MarkPaymentFailed(ctx, internalTxnID, failReason); markErr != nil {
					log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
				}
				return &PaymentResponse{
					Status:        "failed",
					Action:        "failed",
					OrderID:       req.OrderID,
					TransactionID: internalTxnID,
					Message:       "Failed to prepare 3-D Secure verification. Please try again.",
				}, fmt.Errorf("%s", failReason)
			}

			return &PaymentResponse{
				Status:        "3ds_redirect",
				Action:        "3ds_redirect",
				ThreeDSHtml:   txnRes.Data3DSHTML,
				OrderID:       req.OrderID,
				TransactionID: internalTxnID,
			}, nil
		}

		if txnRes.StatusCode != "00" && txnRes.StatusCode != "000" && txnRes.StatusCode != "" {
			if s.fraud != nil {
				s.fraud.RecordAttempt(ctx, merchantUserID, clientIP, false)
			}
			if markErr := s.MarkPaymentFailed(ctx, internalTxnID, txnRes.StatusMsg); markErr != nil {
				log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
			}
			return nil, fmt.Errorf("gateway error: %s (%s)", txnRes.StatusMsg, payfast.MapIssuerResponseCode(txnRes.StatusCode))
		}

		if s.fraud != nil {
			s.fraud.RecordAttempt(ctx, merchantUserID, clientIP, true)
		}

		if err := s.VerifyAndSettle(ctx, internalTxnID, req.OrderID, txnRes.TransactionID, expectedAmount); err != nil {
			return nil, err
		}

		return &PaymentResponse{
			Status:        "settlement_pending",
			OrderID:       req.OrderID,
			TransactionID: internalTxnID,
			Message:       "Saved card payment verified successfully",
		}, nil
	}

	// Zero card persistence cleanup on the original caller struct
	defer func() {
		req.CardNumber = ""
		req.CVV = ""
		req.AccountNumber = ""
		req.CNICNumber = ""
	}()

	callbackURL, err := s.Build3DSCallbackURL(internalTxnID)
	if err != nil {
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Failed to build callback URL: "+err.Error()); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return nil, err
	}

	// 5. Request Temporary Transaction Token from PayFast
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

	// Save card details metadata before zeroing if customer opted into saving card
	var cardBrand, lastFour string
	if req.SaveCardForFuture && req.CardNumber != "" {
		cleanPan := strings.ReplaceAll(req.CardNumber, " ", "")
		if len(cleanPan) >= 4 {
			lastFour = cleanPan[len(cleanPan)-4:]
		}
		if strings.HasPrefix(cleanPan, "4") {
			cardBrand = "visa"
		} else if strings.HasPrefix(cleanPan, "5") {
			cardBrand = "mastercard"
		} else {
			cardBrand = "card"
		}
	}

	tokenRes, err := s.payfast.GetTemporaryTransactionToken(ctx, tokenReq)
	if err != nil {
		if s.fraud != nil {
			s.fraud.RecordAttempt(ctx, merchantUserID, clientIP, false)
		}
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Temporary token request failed: "+err.Error()); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return &PaymentResponse{
			Status:        "failed",
			OrderID:       req.OrderID,
			TransactionID: internalTxnID,
			Message:       "Failed to communicate with payment gateway",
		}, err
	}

	// Auto-save card token in vault if customer opted in
	if req.SaveCardForFuture && tokenRes.InstrumentToken != "" && s.vault != nil && lastFour != "" {
		if _, saveErr := s.vault.SaveCard(
			ctx, customerTrackingID, tokenRes.InstrumentToken,
			cardBrand, lastFour, req.ExpiryMonth, req.ExpiryYear, req.AccountTitle, false,
		); saveErr != nil {
			// Non-critical: the current payment still proceeds without the card being
			// saved for future use, but we surface it so retries/monitoring can catch it.
			log.Printf("WARNING: failed to save card to vault for user %s: %v", customerTrackingID, saveErr)
		}
	}

	// 6. Evaluate Token Response (3DS Required vs Direct Tokenized Capture)
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

		// This row (status + gateway_txn_id + metadata) is what Handle3DSCallback looks up
		// when PayFast redirects the customer back after OTP verification. If this write
		// fails, the callback will have no instrument token / split data to resume the
		// transaction with — so we must NOT tell the client to proceed to 3DS in that case.
		if _, execErr := s.db.Exec(ctx,
			`UPDATE payment_transactions 
			 SET status = '3ds_required', gateway_txn_id = $1, metadata = $2::jsonb, updated_at = NOW() 
			 WHERE transaction_id = $3`,
			tokenRes.TransactionID, string(metaBytes), internalTxnID,
		); execErr != nil {
			log.Printf("CRITICAL: failed to persist 3ds_required state for txn %s: %v", internalTxnID, execErr)
			if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Failed to persist 3DS state: "+execErr.Error()); markErr != nil {
				log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
			}
			return &PaymentResponse{
				Status:        "failed",
				Action:        "failed",
				OrderID:       req.OrderID,
				TransactionID: internalTxnID,
				Message:       "Failed to prepare 3-D Secure verification. Please try again.",
			}, fmt.Errorf("persist 3ds_required state: %w", execErr)
		}

		return &PaymentResponse{
			Status:        "3ds_redirect",
			Action:        "3ds_redirect",
			ThreeDSHtml:   tokenRes.Data3DSHTML,
			OrderID:       req.OrderID,
			TransactionID: internalTxnID,
		}, nil
	}

	// 7. Direct Tokenized Capture (3DS Not Required)
	if tokenRes.InstrumentToken == "" {
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Gateway returned empty instrument token"); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return nil, errors.New("invalid gateway token response")
	}

	// Mark processing
	if _, execErr := s.db.Exec(ctx, `UPDATE payment_transactions SET status = 'processing', updated_at = NOW() WHERE transaction_id = $1`, internalTxnID); execErr != nil {
		log.Printf("WARNING: failed to update payment %s to 'processing': %v", internalTxnID, execErr)
	}

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

	// Use detached context preserving request context values
	gwCtx, gwCancel := s.gatewayContext(ctx)
	defer gwCancel()
	txnRes, err := s.payfast.InitiateTokenizedTransaction(gwCtx, txnReq)
	if err != nil {
		if s.fraud != nil {
			s.fraud.RecordAttempt(ctx, merchantUserID, clientIP, false)
		}
		if payfast.IsTransient(err) {
			if markErr := s.MarkPaymentGatewayPending(ctx, internalTxnID, tokenRes.TransactionID, "Direct tokenized txn timeout: "+err.Error()); markErr != nil {
				log.Printf("CRITICAL: failed to update payment status via MarkPaymentGatewayPending for txn %s: %v", internalTxnID, markErr)
			}
			return &PaymentResponse{
				Status:        "gateway_pending",
				OrderID:       req.OrderID,
				TransactionID: internalTxnID,
				Message:       "Payment is processing at gateway; reconciliation in progress",
			}, nil
		}

		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Tokenized capture failed: "+err.Error()); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return &PaymentResponse{
			Status:        "failed",
			OrderID:       req.OrderID,
			TransactionID: internalTxnID,
			Message:       "Payment was rejected by gateway",
		}, err
	}

	if txnRes == nil || txnRes.TransactionID == "" {
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Gateway returned empty transaction ID"); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return nil, errors.New("invalid gateway transaction response")
	}
	if !payfast.IsSuccessCode(txnRes.StatusCode) && txnRes.StatusCode != "" {
		if s.fraud != nil {
			s.fraud.RecordAttempt(ctx, merchantUserID, clientIP, false)
		}
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, txnRes.StatusMsg); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return nil, fmt.Errorf("gateway error: %s (%s)", txnRes.StatusMsg, payfast.MapIssuerResponseCode(txnRes.StatusCode))
	}

	if s.fraud != nil {
		s.fraud.RecordAttempt(ctx, merchantUserID, clientIP, true)
	}

	// 8. Verify & Settle
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
func (s *PayFastService) Handle3DSCallback(ctx context.Context, mdParam, paRes, clientIP string) (result string, err error) {
	callbackStart := time.Now()
	defer func() {
		outcome := "settled"
		if err != nil {
			outcome = "failed"
		}
		telemetry.Observe3DSCallbackDuration(outcome, time.Since(callbackStart))
	}()

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

	// Use detached context preserving request context values
	gwCtx, gwCancel := s.gatewayContext(ctx)
	defer gwCancel()
	txnRes, err := s.payfast.InitiateTokenizedTransaction(gwCtx, txnReq)
	if err != nil {
		if payfast.IsTransient(err) {
			if markErr := s.MarkPaymentGatewayPending(ctx, internalTxnID, meta.GatewayTxnID, "Tokenized 3DS txn timeout: "+err.Error()); markErr != nil {
				log.Printf("CRITICAL: failed to update payment status via MarkPaymentGatewayPending for txn %s: %v", internalTxnID, markErr)
			}
			return orderID, fmt.Errorf("gateway timeout during 3DS transaction: %w", err)
		}
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Tokenized 3DS txn rejected: "+err.Error()); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return orderID, fmt.Errorf("3DS payment rejected: %w", err)
	}

	if txnRes == nil || txnRes.TransactionID == "" {
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Gateway returned empty transaction ID after 3DS"); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return orderID, errors.New("invalid gateway response")
	}
	if !payfast.IsSuccessCode(txnRes.StatusCode) && txnRes.StatusCode != "" {
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, txnRes.StatusMsg); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return orderID, fmt.Errorf("gateway rejection: %s", txnRes.StatusMsg)
	}

	// 4. Verify Status & Settle
	if err := s.VerifyAndSettle(ctx, internalTxnID, orderID, txnRes.TransactionID, amount); err != nil {
		return orderID, err
	}

	return orderID, nil
}

// VerifyAndSettle queries PayFast status and settles the database transaction atomically.
//
// The context is deliberately detached (WithoutCancel) before any work: by the time this
// runs, funds have MAY already be captured at the gateway. If the originating HTTP request
// dies mid-settlement (client disconnect, load-balancer timeout), a cancellable context
// would abort the local ledger/order updates while the customer was still charged —
// leaving a paid order stuck unsettled until reconciliation. Values (tracing etc.) are
// preserved; only cancellation is dropped.
func (s *PayFastService) VerifyAndSettle(ctx context.Context, internalTxnID, orderID, gatewayTxnID string, expectedAmount float64) error {
	ctx = context.WithoutCancel(ctx)

	if gatewayTxnID == "" {
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Missing gateway transaction ID for status verification"); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return errors.New("invalid gateway transaction ID")
	}

	statusRes, err := s.payfast.GetTransactionStatus(ctx, gatewayTxnID)
	if err != nil {
		if payfast.IsTransient(err) {
			if markErr := s.MarkPaymentGatewayPending(ctx, internalTxnID, gatewayTxnID, "Status verification timeout: "+err.Error()); markErr != nil {
				log.Printf("CRITICAL: failed to update payment status via MarkPaymentGatewayPending for txn %s: %v", internalTxnID, markErr)
			}
			return fmt.Errorf("status verification timeout: %w", err)
		}
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Status verification error: "+err.Error()); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return err
	}

	if !payfast.IsSuccessCode(statusRes.StatusCode) {
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, fmt.Sprintf("Gateway status '%s': %s", statusRes.StatusCode, statusRes.StatusMsg)); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return fmt.Errorf("gateway status rejection: %s", statusRes.StatusMsg)
	}

	if statusRes.BasketID != "" && statusRes.BasketID != orderID {
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, fmt.Sprintf("Basket ID mismatch: expected %s, got %s", orderID, statusRes.BasketID)); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return errors.New("basket ID mismatch")
	}

	if statusRes.TxnAmt != "" {
		gatewayAmt, parseErr := strconv.ParseFloat(statusRes.TxnAmt, 64)
		if parseErr != nil {
			if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "Invalid amount format in gateway status response"); markErr != nil {
				log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
			}
			return fmt.Errorf("invalid gateway amount format: %w", parseErr)
		}
		expectedPaisa := int64(math.Round(expectedAmount * 100))
		gatewayPaisa := int64(math.Round(gatewayAmt * 100))
		if expectedPaisa != gatewayPaisa {
			if markErr := s.MarkPaymentFailed(ctx, internalTxnID, fmt.Sprintf("Amount mismatch: expected %d paisa, got %d paisa", expectedPaisa, gatewayPaisa)); markErr != nil {
				log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
			}
			return errors.New("transaction amount mismatch")
		}
	}

	return s.ExecuteSplit(ctx, internalTxnID, orderID, expectedAmount, gatewayTxnID)
}

// ExecuteSplit creates the outbox event and prepares the transaction for atomic settlement.
// Global lock ordering policy: ALWAYS lock orders FIRST, then payment_transactions to prevent deadlocks.
//
// Like VerifyAndSettle, the context is detached: this is pure local bookkeeping (DB + outbox)
// for money that may already be captured at the gateway — it must never be aborted by an
// upstream HTTP cancellation.
func (s *PayFastService) ExecuteSplit(ctx context.Context, internalTxnID, orderID string, expectedAmount float64, gatewayTxnID string) error {
	ctx = context.WithoutCancel(ctx)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Lock order FIRST (Global lock order: orders -> payment_transactions)
	var dbAmount float64
	var storeID string
	var vendorTrackingID string
	var orderStatus, paymentStatus string
	err = tx.QueryRow(ctx,
		`SELECT total_amount, store_tracking_id, vendor_tracking_id, status, COALESCE(payment_status, '') FROM orders WHERE order_tracking_id = $1 FOR UPDATE`, orderID,
	).Scan(&dbAmount, &storeID, &vendorTrackingID, &orderStatus, &paymentStatus)
	if err != nil {
		return fmt.Errorf("order not found: %w", err)
	}
	if orderStatus == "paid" || paymentStatus == "paid" || paymentStatus == "settlement_pending" {
		return errors.New("conflict: order already paid or settlement in progress")
	}
	if dbAmount != expectedAmount {
		return fmt.Errorf("order amount changed: expected %.2f, got %.2f", expectedAmount, dbAmount)
	}

	var deliveryTrackingID string
	if scanErr := tx.QueryRow(ctx, `SELECT COALESCE(tracking_id, '') FROM deliveries WHERE order_tracking_id = $1`, orderID).Scan(&deliveryTrackingID); scanErr != nil {
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			log.Printf("ERROR: delivery tracking id lookup error for order %s: %v", orderID, scanErr)
		}
	}

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
		"vendor_tracking_id":   vendorTrackingID,
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

// IPNParams holds the fields PayFast sends as query-string parameters when it POSTs/GETs an
// Instant Payment Notification (IPN) to the merchant's registered checkout_url.
// Supports flexible naming across PayFast APPS documentation variants.
type IPNParams struct {
	BasketID       string `form:"basket_id"`
	BasketIDAlt    string `form:"Basket_ID"`
	OrderID        string `form:"order_id"`
	StatusCode     string `form:"status_code"` // this is the "payfast_err_code" the hash formula refers to
	ErrCode        string `form:"err_code"`
	TransactionID  string `form:"transaction_id"`
	TxnAmt         string `form:"txnamt"`
	SecuredHash    string `form:"secured_hash"`
	ValidationHash string `form:"validation_hash"`
}

func (p *IPNParams) NormalizedBasketID() string {
	if p.BasketID != "" {
		return p.BasketID
	}
	if p.BasketIDAlt != "" {
		return p.BasketIDAlt
	}
	return p.OrderID
}

func (p *IPNParams) NormalizedStatusCode() string {
	if p.StatusCode != "" {
		return p.StatusCode
	}
	return p.ErrCode
}

func (p *IPNParams) NormalizedHash() string {
	if p.SecuredHash != "" {
		return p.SecuredHash
	}
	return p.ValidationHash
}

// HandleIPN processes an Instant Payment Notification from PayFast's checkout_url webhook.
func (s *PayFastService) HandleIPN(ctx context.Context, params IPNParams) error {
	basketID := params.NormalizedBasketID()
	statusCode := params.NormalizedStatusCode()
	receivedHash := params.NormalizedHash()

	if basketID == "" {
		telemetry.RecordIPNReceived(false)
		return errors.New("validation: missing basket_id in IPN payload")
	}

	if !s.payfast.VerifyIPNHash(basketID, statusCode, receivedHash) {
		telemetry.RecordIPNReceived(false)
		log.Printf("[PayFastService] SECURITY: IPN hash verification failed for basket_id=%s — possible spoofed callback, ignoring", basketID)
		return errors.New("validation: IPN hash verification failed")
	}
	telemetry.RecordIPNReceived(true)

	// Robust transaction lookup: match by gateway_txn_id first if supplied, otherwise fallback to active/latest txn for order
	var internalTxnID, gatewayTxnID string
	var amount float64
	var err error

	if params.TransactionID != "" {
		err = s.db.QueryRow(ctx,
			`SELECT transaction_id, COALESCE(gateway_txn_id, ''), amount 
			 FROM payment_transactions 
			 WHERE gateway_txn_id = $1 OR (order_tracking_id = $2 AND status IN ('processing', '3ds_required', 'gateway_pending', 'pending'))
			 ORDER BY created_at DESC LIMIT 1`,
			params.TransactionID, basketID,
		).Scan(&internalTxnID, &gatewayTxnID, &amount)
	} else {
		err = s.db.QueryRow(ctx,
			`SELECT transaction_id, COALESCE(gateway_txn_id, ''), amount 
			 FROM payment_transactions 
			 WHERE order_tracking_id = $1 
			 ORDER BY created_at DESC LIMIT 1`,
			basketID,
		).Scan(&internalTxnID, &gatewayTxnID, &amount)
	}

	if err != nil {
		return fmt.Errorf("no payment transaction found for basket_id %s: %w", basketID, err)
	}

	if gatewayTxnID == "" && params.TransactionID != "" {
		gatewayTxnID = params.TransactionID
	}

	if !payfast.IsSuccessCode(statusCode) && statusCode != "" {
		if markErr := s.MarkPaymentFailed(ctx, internalTxnID, "IPN reported failure, status_code: "+statusCode); markErr != nil {
			log.Printf("CRITICAL: failed to update payment status via MarkPaymentFailed for txn %s: %v", internalTxnID, markErr)
		}
		return nil
	}

	return s.VerifyAndSettle(ctx, internalTxnID, basketID, gatewayTxnID, amount)
}
