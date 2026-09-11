# COD Settlement Backend Implementation Code

## 1. Migration: `0050_cod_debts_rider_tracking_id.up.sql`

```sql
-- Add rider_tracking_id to cod_debts for PayFast card payment settlement tracking
ALTER TABLE cod_debts 
ADD COLUMN IF NOT EXISTS rider_tracking_id VARCHAR(100);

CREATE INDEX IF NOT EXISTS idx_cod_debts_rider_tracking_id 
ON cod_debts(rider_tracking_id);

COMMENT ON COLUMN cod_debts.rider_tracking_id IS 'Rider who collected COD cash and is responsible for settlement';
```

## 2. Migration: `0050_cod_debts_rider_tracking_id.down.sql`

```sql
DROP INDEX IF EXISTS idx_cod_debts_rider_tracking_id;
ALTER TABLE cod_debts DROP COLUMN IF EXISTS rider_tracking_id;
```

## 3. New Method: `PayFastService.ProcessCODCardPayment()`

File: `backend/go-services/internal/payment_orchestrator/service/payfast_service.go`

```go
// CODCardPaymentRequest contains rider's card details for COD settlement.
type CODCardPaymentRequest struct {
	OrderTrackingID string `json:"order_tracking_id" binding:"required"`
	CardNumber      string `json:"card_number" binding:"required"`
	ExpiryMonth     string `json:"expiry_month" binding:"required"`
	ExpiryYear      string `json:"expiry_year" binding:"required"`
	CVV             string `json:"cvv" binding:"required"`
}

// CODCardPaymentResponse represents the response from COD card payment.
type CODCardPaymentResponse struct {
	Status        string `json:"status"` // settled | 3ds_required | failed
	CodDebtID     string `json:"cod_debt_id"`
	AmountSettled float64 `json:"amount_settled"`
	ThreeDSURL    string `json:"three_ds_url,omitempty"`
	TransactionID string `json:"transaction_id,omitempty"`
	Message       string `json:"message,omitempty"`
}

// ProcessCODCardPayment handles COD settlement via PayFast debit/credit card.
// It uses the cod_debt_id as the basket ID for PayFast.
func (s *PayFastService) ProcessCODCardPayment(ctx context.Context, riderTrackingID, clientIP string, req *CODCardPaymentRequest) (*CODCardPaymentResponse, error) {
	// 1. Validate card input
	if err := s.validateCardInput(req); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrValidation, err)
	}

	// 2. Read COD debt and order details
	var codDebtID string
	var amountOwedPaisa int64
	var orderStatus string
	var storeID string
	var riderID string
	
	err := s.db.QueryRow(ctx,
		`SELECT c.id::text, c.amount_owed, c.rider_tracking_id, o.status, o.store_tracking_id
		 FROM cod_debts c
		 JOIN orders o ON c.order_tracking_id = o.order_tracking_id
		 WHERE c.order_tracking_id = $1 AND c.status = 'pending'`,
		req.OrderTrackingID,
	).Scan(&codDebtID, &amountOwedPaisa, &riderID, &orderStatus, &storeID)
	if err != nil {
		return nil, fmt.Errorf("%w: COD debt not found or already settled", ErrNotFound)
	}

	// 3. Verify caller is the assigned rider
	if riderTrackingID != riderID {
		return nil, fmt.Errorf("%w: only the assigned rider can settle this COD debt", ErrForbidden)
	}

	// 4. Generate internal transaction ID
	internalTxnID := "cod_" + uuid.New().String()

	// 5. Build 3DS callback URL
	callbackURL, err := s.Build3DSCallbackURL(internalTxnID)
	if err != nil {
		return nil, fmt.Errorf("failed to build callback URL: %w", err)
	}

	// 6. Request temporary token from PayFast
	tokenReq := payfast.TemporaryTokenRequest{
		MerchantUserId:     riderTrackingID,
		CustomerMobileNo:   "", // Will be fetched from users table
		BasketID:           codDebtID[:min(len(codDebtID), 20)], // PayFast basket ID limit
		OrderDate:          time.Now().Format("2006-01-02 15:04:05"),
		TxnAmt:             fmt.Sprintf("%.2f", float64(amountOwedPaisa)/100.0),
		CustomerIP:         clientIP,
		AccountTypeID:      "2", // Card
		MerCatCode:         s.merchantCategory,
		CardNumber:         req.CardNumber,
		ExpiryMonth:        req.ExpiryMonth,
		ExpiryYear:         req.ExpiryYear,
		CVV:                req.CVV,
		Data3DSPagemode:    "SIMPLE",
		Data3DSCallbackURL: callbackURL,
	}

	// 7. Get auth token and call PayFast
	tokenRes, err := s.payfast.GetTemporaryTransactionToken(ctx, tokenReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get temporary token: %w", err)
	}

	// 8. Save payment transaction for tracking
	metaBytes, _ := json.Marshal(PaymentMetadata{
		InstrumentToken: tokenRes.InstrumentToken,
		GatewayTxnID:    tokenRes.TransactionID,
		Data3DSSecureID: tokenRes.Data3DSSecureID,
		ECI:             tokenRes.ECI.String(),
		CustomerIP:      clientIP,
		AccountTypeID:   "2",
	})

	_, err = s.db.Exec(ctx,
		`INSERT INTO payment_transactions (transaction_id, order_tracking_id, gateway, amount, currency, status, kind, metadata)
		 VALUES ($1, $2, 'payfast', $3, 'PKR', 'pending', 'cod_settlement', $4::jsonb)`,
		internalTxnID, req.OrderTrackingID, float64(amountOwedPaisa)/100.0, string(metaBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to record payment transaction: %w", err)
	}

	// 9. Check if 3DS is required
	if tokenRes.Data3DSHTML != "" {
		// Update status to 3ds_required
		_, _ = s.db.Exec(ctx,
			`UPDATE payment_transactions SET status = '3ds_required', gateway_txn_id = $1 WHERE transaction_id = $2`,
			tokenRes.TransactionID, internalTxnID,
		)

		return &CODCardPaymentResponse{
			Status:        "3ds_required",
			CodDebtID:     codDebtID,
			ThreeDSURL:    tokenRes.Data3DSHTML,
			TransactionID: internalTxnID,
		}, nil
	}

	// 10. Direct capture (no 3DS required)
	txnReq := payfast.TokenizedTransactionRequest{
		InstrumentToken:  tokenRes.InstrumentToken,
		TransactionID:    tokenRes.TransactionID,
		MerchantUserId:   riderTrackingID,
		CustomerMobileNo: "",
		BasketID:         codDebtID[:min(len(codDebtID), 20)],
		OrderDate:        time.Now().Format("2006-01-02 15:04:05"),
		TxnDesc:          "COD Settlement",
		TxnAmt:           fmt.Sprintf("%.2f", float64(amountOwedPaisa)/100.0),
		CustomerIP:       clientIP,
		MerCatCode:       s.merchantCategory,
	}

	gwCtx, gwCancel := s.gatewayContext(ctx)
	defer gwCancel()
	txnRes, err := s.payfast.InitiateTokenizedTransaction(gwCtx, txnReq)
	if err != nil {
		return nil, fmt.Errorf("tokenized transaction failed: %w", err)
	}

	if !payfast.IsSuccessCode(txnRes.StatusCode) && txnRes.StatusCode != "" {
		return nil, fmt.Errorf("gateway rejection: %s", txnRes.StatusMsg)
	}

	// 11. Settle the COD debt
	if err := s.settleCODDebt(ctx, codDebtID, req.OrderTrackingID, riderTrackingID, amountOwedPaisa, storeID, internalTxnID); err != nil {
		return nil, fmt.Errorf("settlement failed: %w", err)
	}

	return &CODCardPaymentResponse{
		Status:        "settled",
		CodDebtID:     codDebtID,
		AmountSettled: float64(amountOwedPaisa) / 100.0,
		TransactionID: internalTxnID,
		Message:       "COD debt settled successfully via card",
	}, nil
}

// settleCODDebt processes the COD settlement after successful card payment.
func (s *PayFastService) settleCODDebt(ctx context.Context, codDebtID, orderTrackingID, riderTrackingID string, amountOwedPaisa int64, storeID, paymentTxnID string) error {
	ctx = context.WithoutCancel(ctx)

	// 1. Calculate COD split
	split, err := s.calculator.CalculateCODSplit(ctx, amountOwedPaisa, storeID, orderTrackingID)
	if err != nil {
		return fmt.Errorf("split calculation failed: %w", err)
	}

	// 2. Begin transaction
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 3. Update cod_debts status
	_, err = tx.Exec(ctx,
		`UPDATE cod_debts SET status = 'settled', settled_via = 'payfast_card', settled_at = NOW() WHERE id = $1`,
		codDebtID,
	)
	if err != nil {
		return fmt.Errorf("failed to update COD debt status: %w", err)
	}

	// 4. Decrement rider cash_in_hand
	_, err = tx.Exec(ctx,
		`UPDATE rider_wallet SET cash_in_hand_paisa = GREATEST(0, cash_in_hand_paisa - $1), updated_at = NOW() WHERE rider_tracking_id = $2`,
		amountOwedPaisa, riderTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to update rider cash_in_hand: %w", err)
	}

	// 5. Create ledger transfers
	idempotencyKey := fmt.Sprintf("cod:card:settle:%s", paymentTxnID)
	
	// Transfer 1: rider_cod_debt → cash_receivable (clear debt)
	_, err = s.ledger.MultiTransfer(ctx, []ledger.TransferRequest{
		{
			DebitAccount:   ledger.AccountRiderCODDebt,
			CreditAccount:  ledger.AccountCashReceivable,
			Amount:         amountOwedPaisa,
			ReferenceType:  "cod_card_settlement",
			ReferenceID:    orderTrackingID,
			Description:    fmt.Sprintf("COD debt cleared via card for order %s", orderTrackingID),
			IdempotencyKey: idempotencyKey + ":clear",
		},
	})
	if err != nil {
		return fmt.Errorf("ledger transfer failed: %w", err)
	}

	// 6. Create escrow hold for vendor
	var vendorTrackID string
	_ = tx.QueryRow(ctx, `SELECT COALESCE(vendor_tracking_id, '') FROM orders WHERE order_tracking_id = $1`, orderTrackingID).Scan(&vendorTrackID)
	
	if vendorTrackID != "" {
		// Create hold in vendor_locked_escrow
		_, err = s.ledger.MultiTransfer(ctx, []ledger.TransferRequest{
			{
				DebitAccount:   ledger.AccountCashReceivable,
				CreditAccount:  ledger.AccountVendorLockedEscrow,
				Amount:         split.VendorEscrow,
				ReferenceType:  "cod_vendor_escrow",
				ReferenceID:    orderTrackingID,
				Description:    fmt.Sprintf("COD vendor escrow for order %s", orderTrackingID),
				IdempotencyKey: idempotencyKey + ":vendor",
			},
		})
		if err != nil {
			return fmt.Errorf("vendor escrow transfer failed: %w", err)
		}
	}

	// 7. Commit transaction
	return tx.Commit(ctx)
}

// validateCardInput validates card payment input parameters.
func (s *PayFastService) validateCardInput(req *CODCardPaymentRequest) error {
	cleanPan := strings.ReplaceAll(req.CardNumber, " ", "")
	if len(cleanPan) < 13 || len(cleanPan) > 19 {
		return errors.New("invalid card number length")
	}
	if len(req.CVV) < 3 || len(req.CVV) > 4 {
		return errors.New("invalid CVV (must be 3 or 4 digits)")
	}
	if len(req.ExpiryMonth) != 2 {
		return errors.New("invalid expiry month format (must be MM)")
	}
	m, err := strconv.Atoi(req.ExpiryMonth)
	if err != nil || m < 1 || m > 12 {
		return errors.New("expiry month must be between 01 and 12")
	}
	if len(req.ExpiryYear) != 4 && len(req.ExpiryYear) != 2 {
		return errors.New("invalid expiry year format (must be YYYY)")
	}
	return nil
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

## 4. New Handler: `CODHandler.CardPayment()`

File: `backend/go-services/internal/payment_orchestrator/handlers/cod_handler.go`

```go
// CODCardPaymentRequest is the payload for card-based COD settlement.
type CODCardPaymentRequest struct {
	OrderTrackingID string `json:"order_tracking_id" binding:"required"`
	CardNumber      string `json:"card_number" binding:"required"`
	ExpiryMonth     string `json:"expiry_month" binding:"required"`
	ExpiryYear      string `json:"expiry_year" binding:"required"`
	CVV             string `json:"cvv" binding:"required"`
}

// CardPayment handles POST /api/v1/payments/cod/card-payment
// Allows riders to settle COD debt via PayFast debit/credit card.
func (h *CODHandler) CardPayment(c *gin.Context) {
	var req CODCardPaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	riderID := middleware.GetTrackingID(c)
	if riderID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// Delegate to PayFast service
	payfastSvc := service.NewPayFastService(h.db, h.ledger, h.escrow, h.calculator, nil)
	resp, err := payfastSvc.ProcessCODCardPayment(ctx, riderID, c.ClientIP(), &service.CODCardPaymentRequest{
		OrderTrackingID: req.OrderTrackingID,
		CardNumber:      req.CardNumber,
		ExpiryMonth:     req.ExpiryMonth,
		ExpiryYear:      req.ExpiryYear,
		CVV:             req.CVV,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		case errors.Is(err, service.ErrValidation):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	c.JSON(http.StatusOK, resp)
}
```

## 5. Update Route Registration

File: `backend/go-services/internal/payment_orchestrator/handlers/cod_handler.go`

```go
// RegisterRoutes registers COD payment endpoints.
func (h *CODHandler) RegisterRoutes(router *gin.Engine) {
	router.POST("/api/v1/payments/cod/confirm", middleware.JWTAuth(), middleware.RoleRequired("rider", "admin"), h.Confirm)
	router.POST("/api/v1/payments/cod/pay-now", middleware.JWTAuth(), middleware.RoleRequired("rider", "admin"), h.PayNow)
	router.POST("/api/v1/payments/cod/settlement", middleware.JWTAuth(), middleware.RoleRequired("rider", "admin"), h.Settlement)
	router.POST("/api/v1/payments/cod/card-payment", middleware.JWTAuth(), middleware.RoleRequired("rider", "admin"), h.CardPayment) // NEW
	router.GET("/api/v1/payments/cod/debts", middleware.JWTAuth(), h.ListDebts)
}
```

## 6. 3DS Callback Handler Update

File: `backend/go-services/internal/payment_orchestrator/handlers/payfast_handler.go`

The existing `Handle3DSCallback` in `payfast_service.go` already handles 3DS callbacks. The COD card payment flow uses the same callback URL, so no changes needed here.

## 7. Build and Test

```bash
# Build all services
cd backend/go-services
./build_all.sh

# Test the new endpoint
curl -X POST https://omnigo-app-3-production.up.railway.app/api/v1/payments/cod/card-payment \
  -H "Authorization: Bearer <rider_jwt_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "order_tracking_id": "ORD-xxx",
    "card_number": "4111111111111111",
    "expiry_month": "12",
    "expiry_year": "25",
    "cvv": "123"
  }'
```

---

## Summary of Changes

1. **Migration**: Add `rider_tracking_id` column to `cod_debts` table
2. **New Method**: `PayFastService.ProcessCODCardPayment()` - handles card payment flow
3. **New Handler**: `CODHandler.CardPayment()` - HTTP endpoint
4. **New Route**: `POST /api/v1/payments/cod/card-payment`
5. **Settlement Logic**: `PayFastService.settleCODDebt()` - creates ledger entries and vendor escrow

The flow reuses existing PayFast infrastructure (token, 3DS, transaction) and COD settlement logic (split calculation, ledger entries, escrow).
