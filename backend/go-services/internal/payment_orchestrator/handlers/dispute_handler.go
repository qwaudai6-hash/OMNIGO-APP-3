package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/shared/database"
)

// DisputeHandler handles dispute filing and resolution.
type DisputeHandler struct {
	db     *pgxpool.Pool
	escrow *escrow.Service
}

func NewDisputeHandler(db *pgxpool.Pool, escrowSvc *escrow.Service) *DisputeHandler {
	return &DisputeHandler{db: db, escrow: escrowSvc}
}

// FileDisputeRequest is the payload for filing a dispute.
type FileDisputeRequest struct {
	OrderTrackingID string `json:"order_tracking_id" binding:"required"`
	Reason          string `json:"reason" binding:"required"`
	PhotoURL        string `json:"photo_url"`
}

// File handles POST /api/v1/payments/disputes
// Files a dispute and freezes the escrow for the related order.
func (h *DisputeHandler) File(c *gin.Context) {
	var req FileDisputeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	// Get the user who filed the dispute (from JWT)
	filedBy := c.GetHeader("X-User-Tracking-ID")
	if filedBy == "" {
		filedBy = "unknown"
	}

	// Check if order exists
	ok, err := database.Exists(ctx, h.db, "SELECT 1 FROM orders WHERE order_tracking_id = $1", req.OrderTrackingID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate order: " + err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("order %s does not exist", req.OrderTrackingID)})
		return
	}

	// Check for existing open disputes on this order
	var openDisputes int
	h.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM disputes WHERE order_tracking_id = $1 AND status IN ('open', 'investigating')`,
		req.OrderTrackingID,
	).Scan(&openDisputes)
	if openDisputes > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "open dispute already exists for this order"})
		return
	}

	// Create dispute record
	disputeID := uuid.New()
	_, err = h.db.Exec(ctx,
		`INSERT INTO disputes (id, order_tracking_id, filed_by, reason, status) VALUES ($1, $2, $3, $4, 'open')`,
		disputeID, req.OrderTrackingID, filedBy, req.Reason,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create dispute: " + err.Error()})
		return
	}

	// Freeze escrow for this order
	if err := h.escrow.FreezeForDispute(ctx, req.OrderTrackingID, disputeID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to freeze escrow: " + err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"status":     "dispute_filed",
		"dispute_id": disputeID.String(),
		"order_id":   req.OrderTrackingID,
		"escrow":     "frozen",
	})
}

// ResolveDisputeRequest is the payload for resolving a dispute.
type ResolveDisputeRequest struct {
	Status     string `json:"status" binding:"required"` // "resolved" or "rejected"
	Resolution string `json:"resolution" binding:"required"`
}

// Resolve handles PATCH /api/v1/payments/disputes/:id
// Resolves or rejects a dispute, unfreezing escrow if rejected.
func (h *DisputeHandler) Resolve(c *gin.Context) {
	disputeIDStr := c.Param("id")
	disputeID, err := uuid.Parse(disputeIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid dispute ID"})
		return
	}

	var req ResolveDisputeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Status != "resolved" && req.Status != "rejected" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be 'resolved' or 'rejected'"})
		return
	}

	ctx := c.Request.Context()

	// Check dispute exists and is open
	var currentStatus string
	err = h.db.QueryRow(ctx,
		`SELECT status FROM disputes WHERE id = $1`, disputeID,
	).Scan(&currentStatus)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "dispute not found"})
		return
	}
	if currentStatus != "open" && currentStatus != "investigating" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "dispute is already " + currentStatus})
		return
	}

	// Update dispute status
	_, err = h.db.Exec(ctx,
		`UPDATE disputes SET status = $1, resolution = $2, resolved_at = NOW() WHERE id = $3`,
		req.Status, req.Resolution, disputeID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update dispute"})
		return
	}

	// If resolved in customer favor, execute double-entry refund from escrow to customer
	if req.Status == "resolved" {
		if err := h.escrow.RefundDispute(ctx, disputeID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to process escrow refund: " + err.Error()})
			return
		}
	} else if req.Status == "rejected" {
		// If rejected, unfreeze escrow so it can be released normally to vendor
		if err := h.escrow.UnfreezeOnRejection(ctx, disputeID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unfreeze escrow: " + err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":     "dispute_" + req.Status,
		"dispute_id": disputeID.String(),
		"resolution": req.Resolution,
		"escrow":     req.Status,
	})
}

// List handles GET /api/v1/payments/disputes
// Lists disputes (admin only or user's own).
func (h *DisputeHandler) List(c *gin.Context) {
	ctx := c.Request.Context()

	status := c.Query("status")
	query := `SELECT id, order_tracking_id, filed_by, reason, status, COALESCE(resolution, ''), created_at, COALESCE(resolved_at, '0001-01-01')
	           FROM disputes`
	args := []interface{}{}

	if status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT 50`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	type DisputeResponse struct {
		ID         string    `json:"id"`
		OrderID    string    `json:"order_tracking_id"`
		FiledBy    string    `json:"filed_by"`
		Reason     string    `json:"reason"`
		Status     string    `json:"status"`
		Resolution string    `json:"resolution"`
		CreatedAt  time.Time `json:"created_at"`
		ResolvedAt time.Time `json:"resolved_at"`
	}

	var disputes []DisputeResponse
	for rows.Next() {
		var d DisputeResponse
		var resolvedAt time.Time
		if err := rows.Scan(&d.ID, &d.OrderID, &d.FiledBy, &d.Reason, &d.Status, &d.Resolution, &d.CreatedAt, &resolvedAt); err != nil {
			continue
		}
		if resolvedAt.Year() > 1 {
			d.ResolvedAt = resolvedAt
		}
		disputes = append(disputes, d)
	}

	c.JSON(http.StatusOK, gin.H{"disputes": disputes})
}

// RegisterRoutes registers dispute endpoints.
func (h *DisputeHandler) RegisterRoutes(router *gin.Engine) {
	payments := router.Group("/api/v1/payments")
	{
		payments.POST("/disputes", h.File)
		payments.PATCH("/disputes/:id", h.Resolve)
		payments.GET("/disputes", h.List)
	}
}
