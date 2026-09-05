package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/shared/middleware"
)

// VendorHandler handles vendor wallet and payout queries.
type VendorHandler struct {
	db     *pgxpool.Pool
	escrow *escrow.Repository
}

func NewVendorHandler(db *pgxpool.Pool) *VendorHandler {
	return &VendorHandler{db: db, escrow: escrow.NewRepository(db)}
}

// GetWallet handles GET /api/v1/vendor/wallet/:vendor_id
// Returns comprehensive wallet breakdown including escrow status.
func (h *VendorHandler) GetWallet(c *gin.Context) {
	vendorID := c.Param("vendor_id")
	callerID := middleware.GetTrackingID(c)
	callerRole := middleware.GetRole(c)
	if callerRole != "admin" && callerID != "" && callerID != vendorID {
		c.JSON(http.StatusForbidden, gin.H{"error": "unauthorized access to vendor wallet"})
		return
	}
	ctx := c.Request.Context()

	var balancePaisa, lifetimeEarningsPaisa, totalPayoutsPaisa int64
	err := h.db.QueryRow(ctx,
		`SELECT COALESCE(balance_paisa, 0), COALESCE(lifetime_earnings_paisa, 0), COALESCE(total_payouts_paisa, 0)
		 FROM vendor_wallet WHERE vendor_tracking_id = $1`,
		vendorID,
	).Scan(&balancePaisa, &lifetimeEarningsPaisa, &totalPayoutsPaisa)
	if err != nil {
		balancePaisa = 0
		lifetimeEarningsPaisa = 0
		totalPayoutsPaisa = 0
	}

	// Escrow held: status='held' (48h hold period)
	var escrowHeldPaisa int64
	_ = h.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_paisa), 0) FROM escrow_holds WHERE vendor_tracking_id = $1 AND status = 'held'`,
		vendorID,
	).Scan(&escrowHeldPaisa)

	// Ready to release: status='released' (hold expired, pending payout worker)
	var readyToReleasePaisa int64
	_ = h.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_paisa), 0) FROM escrow_holds WHERE vendor_tracking_id = $1 AND status = 'released'`,
		vendorID,
	).Scan(&readyToReleasePaisa)

	c.JSON(http.StatusOK, gin.H{
		"vendor_tracking_id":    vendorID,
		"withdrawable_paisa":    balancePaisa,
		"withdrawable_rupees":   float64(balancePaisa) / 100.0,
		"escrow_held_paisa":     escrowHeldPaisa,
		"escrow_held_rupees":    float64(escrowHeldPaisa) / 100.0,
		"ready_to_release_paisa": readyToReleasePaisa,
		"ready_to_release_rupees": float64(readyToReleasePaisa) / 100.0,
		"lifetime_earnings_paisa": lifetimeEarningsPaisa,
		"lifetime_earnings_rupees": float64(lifetimeEarningsPaisa) / 100.0,
		"total_payouts_paisa":   totalPayoutsPaisa,
		"total_payouts_rupees":  float64(totalPayoutsPaisa) / 100.0,
		"currency":              "PKR",
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

// EscrowHoldItem is one row in the vendor's escrow breakdown.
type EscrowHoldItem struct {
	ID              string  `json:"id"`
	OrderTrackingID string  `json:"order_tracking_id"`
	AmountPaisa     int64   `json:"amount_paisa"`
	AmountRupees    float64 `json:"amount_rupees"`
	Status          string  `json:"status"`
	PaymentGateway  string  `json:"payment_gateway"`
	Currency        string  `json:"currency"`
	OrderStatus     string  `json:"order_status"`
	HoldUntil       string  `json:"hold_until"`
	ReleasedAt      *string `json:"released_at,omitempty"`
	CreatedAt       string  `json:"created_at"`
	// Computed: time remaining until release, or "released"/"stuck" hint
	ReleaseHint string `json:"release_hint"`
}

// ListEscrowHolds handles GET /api/v1/payments/vendor/escrow-holds
// Returns per-order escrow breakdown for the authenticated vendor.
//
// Authorization: vendor can only view their own holds. Admin can view any.
//
// This is a READ-ONLY endpoint — no state change, no double-spend risk.
// All amounts are in paisa (int64) for precision. JSON includes both paisa
// (internal) and rupees (display) for convenience.
//
// "release_hint" provides a human-readable status:
//   - "held_remaining_Xh_Ym" — escrow is held, Xh Ym until release
//   - "ready_to_release" — hold period expired, awaiting payout worker
//   - "released" — funds have been moved to withdrawable balance
//   - "paid_out" — funds have been paid to bank account
//   - "refunded" — order was refunded, escrow returned to customer
//   - "disputed" — escrow is on hold due to open dispute
//   - "cancelled" — order was cancelled, escrow released to customer
func (h *VendorHandler) ListEscrowHolds(c *gin.Context) {
	vendorID := c.Param("vendor_id")
	if vendorID == "" {
		vendorID = c.Query("vendor_id")
	}
	callerID := middleware.GetTrackingID(c)
	callerRole := middleware.GetRole(c)
	if callerRole != "admin" && callerID != "" && callerID != vendorID {
		c.JSON(http.StatusForbidden, gin.H{"error": "unauthorized access to escrow holds"})
		return
	}
	if vendorID == "" {
		// Fall back to caller's own vendor ID
		vendorID = callerID
	}
	if vendorID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "vendor_id required"})
		return
	}

	ctx := c.Request.Context()
	holds, err := h.escrow.GetHoldsByVendorWithOrder(ctx, vendorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch escrow holds: " + err.Error()})
		return
	}

	now := time.Now()
	items := make([]EscrowHoldItem, 0, len(holds))
	for _, h := range holds {
		var releasedAt *string
		if h.ReleasedAt != nil {
			s := h.ReleasedAt.Format(time.RFC3339)
			releasedAt = &s
		}

		hint := computeReleaseHint(h.Status, h.HoldUntil, h.ReleasedAt, now)

		items = append(items, EscrowHoldItem{
			ID:              h.ID,
			OrderTrackingID: h.OrderTrackingID,
			AmountPaisa:     h.AmountPaisa,
			AmountRupees:    float64(h.AmountPaisa) / 100.0,
			Status:          h.Status,
			PaymentGateway:  h.PaymentGateway,
			Currency:        h.Currency,
			OrderStatus:     h.OrderStatus,
			HoldUntil:       h.HoldUntil.Format(time.RFC3339),
			ReleasedAt:      releasedAt,
			CreatedAt:       h.CreatedAt.Format(time.RFC3339),
			ReleaseHint:     hint,
		})
	}

	// Summary buckets
	summary := map[string]int64{
		"held_paisa":             0,
		"released_paisa":         0,
		"paid_out_paisa":         0,
		"refunded_paisa":         0,
		"disputed_paisa":         0,
		"cancelled_paisa":        0,
		"releasing_paisa":        0,
		"ready_to_release_paisa": 0, // released but not yet paid out
	}
	for _, h := range holds {
		switch h.Status {
		case "held":
			summary["held_paisa"] += h.AmountPaisa
		case "released":
			summary["released_paisa"] += h.AmountPaisa
			summary["ready_to_release_paisa"] += h.AmountPaisa
		case "paid_out":
			summary["paid_out_paisa"] += h.AmountPaisa
		case "refunded":
			summary["refunded_paisa"] += h.AmountPaisa
		case "disputed":
			summary["disputed_paisa"] += h.AmountPaisa
		case "cancelled":
			summary["cancelled_paisa"] += h.AmountPaisa
		case "releasing":
			summary["releasing_paisa"] += h.AmountPaisa
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"vendor_tracking_id": vendorID,
		"holds":              items,
		"summary":            summary,
		"count":              len(items),
		"currency":           "PKR",
		"generated_at":       now.Format(time.RFC3339),
	})
}

// computeReleaseHint returns a human-readable hint about the hold's status.
func computeReleaseHint(status string, holdUntil time.Time, releasedAt *time.Time, now time.Time) string {
	switch status {
	case "held":
		remaining := holdUntil.Sub(now)
		if remaining <= 0 {
			return "ready_to_release" // hold expired, awaiting worker
		}
		hours := int(remaining.Hours())
		minutes := int(remaining.Minutes()) % 60
		if hours > 0 {
			return fmt.Sprintf("held_remaining_%dh_%dm", hours, minutes)
		}
		return fmt.Sprintf("held_remaining_%dm", minutes)
	case "released":
		return "ready_to_release"
	case "paid_out":
		return "released"
	case "refunded":
		return "refunded"
	case "disputed":
		return "disputed"
	case "cancelled":
		return "cancelled"
	case "releasing":
		return "releasing"
	default:
		return status
	}
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

	var balancePaisa int64
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(balance_paisa, 0) FROM vendor_wallet WHERE vendor_tracking_id = $1 FOR UPDATE`,
		req.VendorTrackingID,
	).Scan(&balancePaisa)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "vendor wallet not found or initialized"})
		return
	}

	// Convert request amount (rupees) to paisa for comparison
	amountPaisa := int64(req.Amount * 100)
	if balancePaisa < amountPaisa {
		c.JSON(http.StatusBadRequest, gin.H{"error": "insufficient withdrawable balance"})
		return
	}

	// VW-2 FIX: AND balance_paisa >= $1 prevents negative balance if a refactor
	// removes the FOR UPDATE lock or changes the transaction boundaries.
	tag, err := tx.Exec(ctx,
		`UPDATE vendor_wallet
		 SET balance_paisa = balance_paisa - $1, total_payouts_paisa = total_payouts_paisa + $1
		 WHERE vendor_tracking_id = $2 AND balance_paisa >= $1`,
		amountPaisa, req.VendorTrackingID,
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
		vendor.GET("/escrow-holds/:vendor_id", h.ListEscrowHolds)
		vendor.POST("/withdraw", middleware.RoleRequired("vendor"), h.RequestWithdraw)
	}
}
