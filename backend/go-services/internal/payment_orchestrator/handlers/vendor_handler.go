package handlers

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/shared/middleware"
)

// VendorHandler handles vendor wallet and payout queries.
type VendorHandler struct {
	db *pgxpool.Pool
}

func NewVendorHandler(db *pgxpool.Pool) *VendorHandler {
	return &VendorHandler{db: db}
}

// GetWallet handles GET /api/v1/vendor/wallet/:vendor_id
func (h *VendorHandler) GetWallet(c *gin.Context) {
	vendorID := c.Param("vendor_id")
	callerID := middleware.GetTrackingID(c)
	callerRole := middleware.GetRole(c)
	if callerRole != "admin" && callerID != "" && callerID != vendorID {
		c.JSON(http.StatusForbidden, gin.H{"error": "unauthorized access to vendor wallet"})
		return
	}
	ctx := c.Request.Context()

	var balance, lifetimeEarnings, totalPayouts float64
	err := h.db.QueryRow(ctx,
		`SELECT COALESCE(balance, 0), COALESCE(lifetime_earnings, 0), COALESCE(total_payouts, 0)
		 FROM vendor_wallet WHERE vendor_tracking_id = $1`,
		vendorID,
	).Scan(&balance, &lifetimeEarnings, &totalPayouts)
	if err != nil {
		// No wallet row yet — return zeros
		balance = 0
		lifetimeEarnings = 0
		totalPayouts = 0
	}

	var pendingBalance float64
	_ = h.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM escrow_holds WHERE vendor_tracking_id = $1 AND status = 'held'`,
		vendorID,
	).Scan(&pendingBalance)

	c.JSON(http.StatusOK, gin.H{
		"vendor_tracking_id": vendorID,
		"balance":            balance,
		"lifetime_earnings":  lifetimeEarnings,
		"total_payouts":      totalPayouts,
		"pending_balance":    pendingBalance,
	})
}

// ListPayouts handles GET /api/v1/vendor/payouts/:vendor_id
func (h *VendorHandler) ListPayouts(c *gin.Context) {
	vendorID := c.Param("vendor_id")
	callerID := middleware.GetTrackingID(c)
	callerRole := middleware.GetRole(c)
	if callerRole != "admin" && callerID != "" && callerID != vendorID {
		c.JSON(http.StatusForbidden, gin.H{"error": "unauthorized access to vendor payouts"})
		return
	}
	ctx := c.Request.Context()

	rows, err := h.db.Query(ctx,
		`SELECT id, amount, COALESCE(method, ''), status, COALESCE(created_at, NOW()), COALESCE(completed_at, '0001-01-01')
		 FROM vendor_payouts WHERE vendor_tracking_id = $1 ORDER BY created_at DESC LIMIT 20`,
		vendorID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type PayoutResponse struct {
		ID          string  `json:"id"`
		Amount      float64 `json:"amount"`
		Method      string  `json:"method"`
		Status      string  `json:"status"`
		CreatedAt   string  `json:"created_at"`
		CompletedAt string  `json:"completed_at"`
	}

	var payouts []PayoutResponse
	for rows.Next() {
		var p PayoutResponse
		if err := rows.Scan(&p.ID, &p.Amount, &p.Method, &p.Status, &p.CreatedAt, &p.CompletedAt); err != nil {
			continue
		}
		payouts = append(payouts, p)
	}

	c.JSON(http.StatusOK, gin.H{"payouts": payouts})
}

type WithdrawRequest struct {
	VendorTrackingID string  `json:"vendor_tracking_id"`
	Amount           float64 `json:"amount" binding:"required,gt=0"`
	Method           string  `json:"method" binding:"required"`
}

// RequestWithdraw handles POST /api/v1/vendor/withdraw
func (h *VendorHandler) RequestWithdraw(c *gin.Context) {
	var req WithdrawRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	callerID := middleware.GetTrackingID(c)
	if callerID != "" {
		req.VendorTrackingID = callerID
	}
	if req.VendorTrackingID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing vendor identity"})
		return
	}

	ctx := c.Request.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to begin transaction: %v", err)})
		return
	}
	defer tx.Rollback(ctx)

	var balance float64
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(balance, 0) FROM vendor_wallet WHERE vendor_tracking_id = $1 FOR UPDATE`,
		req.VendorTrackingID,
	).Scan(&balance)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "vendor wallet not found or initialized"})
		return
	}

	if balance < req.Amount {
		c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient withdrawable balance"})
		return
	}

	// VW-2 FIX: AND balance >= $1 prevents negative balance if a refactor
	// removes the FOR UPDATE lock or changes the transaction boundaries.
	tag, err := tx.Exec(ctx,
		`UPDATE vendor_wallet
		 SET balance = balance - $1, total_payouts = total_payouts + $1
		 WHERE vendor_tracking_id = $2 AND balance >= $1`,
		req.Amount, req.VendorTrackingID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update wallet balance: %v", err)})
		return
	}
	if tag.RowsAffected() == 0 {
		// The SELECT FOR UPDATE saw sufficient balance but the WHERE guard
		// didn't match — this can only happen if the row was concurrently
		// debited by another transaction that committed between the SELECT
		// and UPDATE (impossible under FOR UPDATE) OR if the wallet row
		// was deleted. Surface it as 409 to alert the client to retry.
		c.JSON(http.StatusConflict, gin.H{"error": "wallet balance changed concurrently — please retry"})
		return
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO vendor_payouts (id, vendor_tracking_id, amount, method, status, batch_id)
		 VALUES (gen_random_uuid(), $1, $2, $3, 'pending', NULL)`,
		req.VendorTrackingID, req.Amount, req.Method,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to insert payout entry: %v", err)})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to commit transaction: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "withdrawal request submitted successfully",
		"status":  "pending",
	})
}

// RegisterRoutes registers vendor wallet/payout endpoints.
// NOTE: these live under /api/v1/payments/vendor so the public gateway
// (/api/v1/payments → payment-orchestrator) can reach them. Registering under
// /api/v1/vendor made them unreachable — that prefix is owned by
// vendor-store-service at the gateway.
func (h *VendorHandler) RegisterRoutes(router *gin.Engine) {
	vendor := router.Group("/api/v1/payments/vendor", middleware.JWTAuth(), middleware.RoleRequired("vendor", "admin"))
	{
		vendor.GET("/wallet/:vendor_id", h.GetWallet)
		vendor.GET("/payouts/:vendor_id", h.ListPayouts)
		vendor.POST("/withdraw", middleware.RoleRequired("vendor"), h.RequestWithdraw)
	}
}
