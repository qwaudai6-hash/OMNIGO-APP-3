package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/payment_orchestrator"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/shared/security"
)

// CODHandler handles Cash on Delivery payment flows.
type CODHandler struct {
	db         *pgxpool.Pool
	ledger     *ledger.Service
	escrow     *escrow.Service
	calculator *payment_orchestrator.CommissionCalculator
}

func NewCODHandler(db *pgxpool.Pool, ledgerSvc *ledger.Service, escrowSvc *escrow.Service, calc *payment_orchestrator.CommissionCalculator) *CODHandler {
	return &CODHandler{
		db:         db,
		ledger:     ledgerSvc,
		escrow:     escrowSvc,
		calculator: calc,
	}
}

// CODConfirmRequest is the payload for COD confirmation.
type CODConfirmRequest struct {
	OrderTrackingID string  `json:"order_tracking_id" binding:"required"`
	RiderTrackingID string  `json:"rider_tracking_id" binding:"required"`
	Amount          float64 `json:"amount" binding:"required"`
	StoreTrackingID string  `json:"store_tracking_id" binding:"required"`
}

// Confirm handles POST /api/v1/payments/cod/confirm
// Creates a ledger-only entry: rider_cod_debt goes NEGATIVE (rider owes platform).
// No real money moves at this point.
func (h *CODHandler) Confirm(c *gin.Context) {
	var req CODConfirmRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	// Validate parent references
	checks := []struct {
		id    string
		label string
		query string
	}{
		{req.OrderTrackingID, "order", "SELECT 1 FROM orders WHERE order_tracking_id = $1"},
		{req.RiderTrackingID, "user", "SELECT 1 FROM users WHERE tracking_id = $1"},
	}
	for _, check := range checks {
		ok, err := database.Exists(ctx, h.db, check.query, check.id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate " + check.label + ": " + err.Error()})
			return
		}
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("%s %s does not exist", check.label, check.id)})
			return
		}
	}

	// Check for idempotency — don't create duplicate COD debt
	var existingID string
	err := h.db.QueryRow(ctx,
		`SELECT id::text FROM cod_debts WHERE order_tracking_id = $1 AND rider_tracking_id = $2`,
		req.OrderTrackingID, req.RiderTrackingID,
	).Scan(&existingID)
	if err == nil {
		c.JSON(http.StatusOK, gin.H{
			"status":      "already_confirmed",
			"cod_debt_id": existingID,
		})
		return
	}

	// Calculate COD split
	split, err := h.calculator.CalculateCODSplit(ctx, req.Amount, req.StoreTrackingID, req.OrderTrackingID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to calculate split: " + err.Error()})
		return
	}

	// Create ledger entries: cash_receivable debited (rider owes platform), rider_cod_debt credited
	// NOTE: We do NOT debit central_escrow here because COD has no online payment.
	// central_escrow holds real money from online payments; cash_receivable is a receivable asset.
	idempotencyKey := fmt.Sprintf("cod:confirm:%s", req.OrderTrackingID)
	txID, err := h.ledger.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   ledger.AccountCashReceivable,
		CreditAccount:  ledger.AccountRiderCODDebt,
		Amount:         req.Amount,
		ReferenceType:  "cod_debt",
		ReferenceID:    req.OrderTrackingID,
		Description:    fmt.Sprintf("COD debt created: rider %s collected %.2f PKR for order %s", req.RiderTrackingID, req.Amount, req.OrderTrackingID),
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ledger transfer failed: " + err.Error()})
		return
	}

	// Insert cod_debts record
	codDebtID := uuid.New()
	_, err = h.db.Exec(ctx,
		`INSERT INTO cod_debts (id, order_tracking_id, rider_tracking_id, amount_owed, status)
		 VALUES ($1, $2, $3, $4, 'pending')`,
		codDebtID, req.OrderTrackingID, req.RiderTrackingID, req.Amount,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create COD debt record: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":         "cod_debt_created",
		"cod_debt_id":    codDebtID.String(),
		"transaction_id": txID.String(),
		"amount_owed":    req.Amount,
		"split": gin.H{
			"admin_revenue":   split.AdminRevenue,
			"vendor_escrow":   split.VendorEscrow,
			"delivery_fee":    split.DeliveryFee,
			"commission_rate": split.CommissionRate,
		},
	})
}

// CODPayNowRequest is the payload for generating a deep-link.
type CODPayNowRequest struct {
	CodDebtID string `json:"cod_debt_id" binding:"required"`
	Gateway   string `json:"gateway" binding:"required"` // "jazzcash" or "easypaisa"
}

// PayNow handles POST /api/v1/payments/cod/pay-now
// Generates a deep-link for the Rider to pay via JazzCash/EasyPaisa/PayFast.
func (h *CODHandler) PayNow(c *gin.Context) {
	var req CODPayNowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Gateway != "jazzcash" && req.Gateway != "easypaisa" && req.Gateway != "payfast" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "gateway must be 'jazzcash', 'easypaisa', or 'payfast'"})
		return
	}

	ctx := c.Request.Context()

	// Read COD debt
	var amountOwed float64
	var riderID string
	var status string
	err := h.db.QueryRow(ctx,
		`SELECT amount_owed, rider_tracking_id, status FROM cod_debts WHERE id = $1`,
		req.CodDebtID,
	).Scan(&amountOwed, &riderID, &status)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "COD debt not found"})
		return
	}
	if status != "pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("COD debt is already %s", status)})
		return
	}

	// PayFast gateway: generate hosted checkout redirect URL
	if req.Gateway == "payfast" {
		merchantID := os.Getenv("PAYFAST_MERCHANT_ID")
		if merchantID == "" {
			merchantID = "10001"
		}
		baseURL := os.Getenv("PAYFAST_BASE_URL")
		if baseURL == "" {
			baseURL = os.Getenv("PAYFAST_API_URL")
		}
		if baseURL == "" {
			baseURL = "https://ipguat.apps.net.pk/Ecommerce/api/Transaction"
		}
		returnURL := os.Getenv("PUBLIC_BASE_URL")
		if returnURL == "" {
			returnURL = "https://omnigo-app-3-production.up.railway.app"
		}
		returnURL += "/api/v1/payments/cod/settlement"

		basketID := fmt.Sprintf("COD%s", req.CodDebtID[:min(len(req.CodDebtID), 20)])

		var redirectURL string
		if strings.Contains(baseURL, "apps.net.pk") {
			formEndpoint := strings.TrimRight(baseURL, "/")
			if !strings.HasSuffix(formEndpoint, "/PostTransaction") {
				if strings.HasSuffix(formEndpoint, "/Transaction") {
					formEndpoint += "/PostTransaction"
				} else {
					formEndpoint += "/Transaction/PostTransaction"
				}
			}
			redirectURL = fmt.Sprintf(
				"%s?merchant_id=%s&basket_id=%s&txnamt=%.2f&currency_code=PKR&success_url=%s&checkout_url=%s",
				formEndpoint,
				url.QueryEscape(merchantID),
				url.QueryEscape(basketID),
				amountOwed,
				url.QueryEscape(returnURL),
				url.QueryEscape(returnURL),
			)
		} else {
			redirectURL = fmt.Sprintf(
				"%s/hosted?merchant_id=%s&basket_id=%s&txnamt=%.2f&currency_code=PKR&success_url=%s&checkout_url=%s",
				strings.TrimRight(baseURL, "/"),
				url.QueryEscape(merchantID),
				url.QueryEscape(basketID),
				amountOwed,
				url.QueryEscape(returnURL),
				url.QueryEscape(returnURL),
			)
		}

		c.JSON(http.StatusOK, gin.H{
			"status":      "redirect_ready",
			"gateway":     "payfast",
			"redirect_url": redirectURL,
			"basket_id":   basketID,
			"amount_owed": amountOwed,
			"rider_id":    riderID,
		})
		return
	}

	// JazzCash/EasyPaisa: generate deep-link
	salt := security.MustEnv("JAZZCASH_SALT")
	if req.Gateway == "easypaisa" {
		salt = security.MustEnv("EASYPAISA_SALT")
	}
	if salt == "" && req.Gateway == "jazzcash" {
		salt = os.Getenv("JAZZCASH_INTEGRITY_SALT")
	}

	payload := fmt.Sprintf("amount=%.2f&to=OMNIGO_SETTLEMENT&ref=%s", amountOwed, req.CodDebtID)
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	deepLink := fmt.Sprintf("%s://transfer?amount=%.2f&to=OMNIGO_SETTLEMENT&ref=%s&hash=%s",
		req.Gateway, amountOwed, req.CodDebtID, signature)

	c.JSON(http.StatusOK, gin.H{
		"status":      "deep_link_ready",
		"gateway":     req.Gateway,
		"deep_link":   deepLink,
		"amount_owed": amountOwed,
		"rider_id":    riderID,
	})
}

// CODSettlementRequest is the webhook payload from JazzCash/EasyPaisa.
type CODSettlementRequest struct {
	CodDebtID      string  `json:"cod_debt_id" binding:"required"`
	TransactionID  string  `json:"transaction_id" binding:"required"`
	Amount         float64 `json:"amount" binding:"required"`
	Status         string  `json:"status" binding:"required"` // "success" or "failed"
	WebhookEventID string  `json:"webhook_event_id" binding:"required"`
	Gateway        string  `json:"gateway" binding:"required"`
}

// Settlement handles POST /api/v1/payments/cod/settlement
// Webhook from JazzCash/EasyPaisa confirming the rider has paid.
func (h *CODHandler) Settlement(c *gin.Context) {
	var req CODSettlementRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	// Check idempotency on webhook_event_id
	var exists bool
	h.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM cod_debts WHERE webhook_event_id = $1)`, req.WebhookEventID,
	).Scan(&exists)
	if exists {
		c.JSON(http.StatusOK, gin.H{"status": "already_processed"})
		return
	}

	if req.Status != "success" {
		c.JSON(http.StatusOK, gin.H{"status": "settlement_failed", "reason": req.Status})
		return
	}

	// Read COD debt
	var amountOwed float64
	var riderID string
	var orderID string
	var storeID string
	var vendorTrackID string
	err := h.db.QueryRow(ctx,
		`SELECT c.amount_owed, c.rider_tracking_id, c.order_tracking_id, o.store_tracking_id, COALESCE(o.vendor_tracking_id, '')
		 FROM cod_debts c JOIN orders o ON c.order_tracking_id = o.order_tracking_id
		 WHERE c.id = $1`,
		req.CodDebtID,
	).Scan(&amountOwed, &riderID, &orderID, &storeID, &vendorTrackID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "COD debt not found"})
		return
	}

	callerRole := middleware.GetRole(c)
	callerID := middleware.GetTrackingID(c)
	if callerRole != "admin" && (callerRole != "rider" || callerID != riderID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "unauthorized settlement access"})
		return
	}

	// Calculate COD split
	split, err := h.calculator.CalculateCODSplit(ctx, amountOwed, storeID, orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "split calculation failed"})
		return
	}

	// Execute ledger transfers:
	// 1. Clear rider COD debt: debit rider_cod_debt, credit cash_receivable (receivable settled)
	// 2. Split the settled funds: debit cash_receivable, credit admin_revenue (DB commission rate + COD surcharge)
	// 3. Split the settled funds: debit cash_receivable, credit vendor_locked_escrow (product portion)
	// 4. Split the settled funds: debit cash_receivable, credit central_escrow (delivery fee portion)
	// NOTE: cash_receivable is a transit account — it goes negative on Confirm, then back to
	// zero on Settlement (Sum(Debits) == Sum(Credits)).
	idempotencyKey := fmt.Sprintf("cod:settle:%s", req.WebhookEventID)
	transferReqs := []ledger.TransferRequest{
		{
			DebitAccount:   ledger.AccountRiderCODDebt,
			CreditAccount:  ledger.AccountCashReceivable,
			Amount:         amountOwed,
			ReferenceType:  "cod_settlement",
			ReferenceID:    orderID,
			Description:    fmt.Sprintf("COD debt cleared for rider %s, order %s", riderID, orderID),
			IdempotencyKey: idempotencyKey + ":clear",
		},
		{
			DebitAccount:   ledger.AccountCashReceivable,
			CreditAccount:  ledger.AccountAdminRevenue,
			Amount:         split.AdminRevenue,
			ReferenceType:  "cod_commission",
			ReferenceID:    orderID,
			Description:    fmt.Sprintf("COD admin commission for order %s", orderID),
			IdempotencyKey: idempotencyKey + ":admin",
		},
		{
			DebitAccount:   ledger.AccountCashReceivable,
			CreditAccount:  ledger.AccountVendorLockedEscrow,
			Amount:         split.VendorEscrow,
			ReferenceType:  "cod_vendor_escrow",
			ReferenceID:    orderID,
			Description:    fmt.Sprintf("COD vendor escrow for order %s", orderID),
			IdempotencyKey: idempotencyKey + ":vendor",
		},
	}

	if split.DeliveryEscrow > 0 {
		transferReqs = append(transferReqs, ledger.TransferRequest{
			DebitAccount:   ledger.AccountCashReceivable,
			CreditAccount:  ledger.AccountCentralEscrow,
			Amount:         split.DeliveryEscrow,
			ReferenceType:  "cod_delivery_escrow",
			ReferenceID:    orderID,
			Description:    fmt.Sprintf("COD delivery fee escrow for order %s", orderID),
			IdempotencyKey: idempotencyKey + ":delivery_escrow",
		})
	}

	txID, err := h.ledger.MultiTransfer(ctx, transferReqs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ledger multi-transfer failed: " + err.Error()})
		return
	}

	// Update cod_debts record
	_, err = h.db.Exec(ctx,
		`UPDATE cod_debts SET status = 'settled', settled_via = $1, settled_at = NOW(), webhook_event_id = $2 WHERE id = $3`,
		req.Gateway, req.WebhookEventID, req.CodDebtID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update COD debt status"})
		return
	}

	// FIX H1: Decrement rider cash_in_hand with proper error handling.
	// Previously this was silently discarded (_, _) which could mask
	// balance drift between actual cash and system records.
	tag, cashErr := h.db.Exec(ctx,
		`UPDATE rider_wallet SET cash_in_hand = GREATEST(0, cash_in_hand - $1), updated_at = NOW() WHERE rider_tracking_id = $2`,
		amountOwed, riderID,
	)
	if cashErr != nil {
		log.Printf("[CODSettlement] WARNING: failed to decrement cash_in_hand for rider %s: %v", riderID, cashErr)
	} else if tag.RowsAffected() == 0 {
		log.Printf("[CODSettlement] WARNING: no rider_wallet row found for rider %s during cash_in_hand decrement", riderID)
	}

	// Create escrow hold for vendor portion
	h.escrow.CreateHold(ctx, orderID, vendorFallback(vendorTrackID, storeID), split.VendorEscrow)

	c.JSON(http.StatusOK, gin.H{
		"status":         "settled",
		"transaction_id": txID.String(),
		"order_id":       orderID,
		"settlement": gin.H{
			"admin_revenue": split.AdminRevenue,
			"vendor_escrow": split.VendorEscrow,
			"total_cleared": amountOwed,
		},
	})
}

// ListDebts handles GET /api/v1/payments/cod/debts
// Returns COD debts for a rider.
func (h *CODHandler) ListDebts(c *gin.Context) {
	riderID := c.Query("rider_id")
	if riderID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rider_id query parameter required"})
		return
	}

	ctx := c.Request.Context()
	rows, err := h.db.Query(ctx,
		`SELECT id, order_tracking_id, rider_tracking_id, amount_owed, status, COALESCE(settled_via, ''), COALESCE(settled_at, '0001-01-01'), created_at
		 FROM cod_debts WHERE rider_tracking_id = $1 ORDER BY created_at DESC LIMIT 20`,
		riderID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type DebtResponse struct {
		ID         string  `json:"id"`
		OrderID    string  `json:"order_tracking_id"`
		RiderID    string  `json:"rider_tracking_id"`
		AmountOwed float64 `json:"amount_owed"`
		Status     string  `json:"status"`
		SettledVia string  `json:"settled_via"`
	}

	var debts []DebtResponse
	for rows.Next() {
		var d DebtResponse
		if err := rows.Scan(&d.ID, &d.OrderID, &d.RiderID, &d.AmountOwed, &d.Status, &d.SettledVia, new(interface{}), new(interface{})); err != nil {
			continue
		}
		debts = append(debts, d)
	}

	c.JSON(http.StatusOK, gin.H{"debts": debts})
}

// RegisterRoutes registers COD payment endpoints.
func (h *CODHandler) RegisterRoutes(router *gin.Engine) {
	payments := router.Group("/api/v1/payments", middleware.JWTAuth())
	{
		payments.POST("/cod/confirm", middleware.RoleRequired("rider", "admin"), h.Confirm)
		payments.POST("/cod/pay-now", middleware.RoleRequired("rider", "admin"), h.PayNow)
		payments.POST("/cod/settlement", middleware.RoleRequired("rider", "admin"), h.Settlement)
		payments.GET("/cod/debts", h.ListDebts)
	}
}

// vendorFallback prefers the vendor USER id, falling back to the store id for
// legacy rows that never carried it.
func vendorFallback(vendorID, storeID string) string {
	if vendorID != "" {
		return vendorID
	}
	return storeID
}
