package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/return/models"
	"github.com/omnigo/backend/internal/return/service"
	"github.com/omnigo/backend/internal/shared/middleware"
)

type ReturnHandler struct {
	svc *service.ReturnService
}

func NewReturnHandler(svc *service.ReturnService) *ReturnHandler {
	return &ReturnHandler{svc: svc}
}

// RequestReturn handles POST /orders/:tracking_id/return-request
// Customer requests a return on a delivered/completed order.
func (h *ReturnHandler) RequestReturn(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}

	var req models.RequestReturnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "AUTH_TOKEN_INVALID"})
		return
	}

	// Only customer or admin can request return
	if role != "admin" {
		// Service layer will validate ownership
	}

	returnReq, err := h.svc.RequestReturn(c.Request.Context(), trackingID, callerID, req.Reason, req.ReturnItems)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"status":        "return requested",
		"return_request": returnReq,
	})
}

// GetReturnRequest handles GET /returns/:id
func (h *ReturnHandler) GetReturnRequest(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "return_request_id is required"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)

	returnReq, err := h.svc.GetReturnRequest(c.Request.Context(), id)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// Ownership check: only the customer, assigned rider, vendor, or admin can view
	if role != "admin" && returnReq.CustomerTrackingID != callerID && returnReq.RiderTrackingID != callerID && returnReq.VendorTrackingID != callerID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN: you do not have access to this return"})
		return
	}

	c.JSON(http.StatusOK, returnReq)
}

// GetReturnByOrder handles GET /returns/order/:order_tracking_id
func (h *ReturnHandler) GetReturnByOrder(c *gin.Context) {
	orderID := c.Param("order_tracking_id")
	if orderID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "order_tracking_id is required"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)

	returnReq, err := h.svc.GetReturnByOrderID(c.Request.Context(), orderID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// Ownership check
	if role != "admin" && returnReq.CustomerTrackingID != callerID && returnReq.RiderTrackingID != callerID && returnReq.VendorTrackingID != callerID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN: you do not have access to this return"})
		return
	}

	c.JSON(http.StatusOK, returnReq)
}

// AcceptReturnPickup handles POST /returns/:id/accept-pickup
// Rider accepts a return pickup gig.
func (h *ReturnHandler) AcceptReturnPickup(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "return_request_id is required"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "AUTH_TOKEN_INVALID"})
		return
	}

	if role != "rider" && role != "admin" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN: only riders can accept return pickups"})
		return
	}

	var req struct {
		GigTrackingID string `json:"gig_tracking_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.AssignRider(c.Request.Context(), id, callerID, req.GigTrackingID); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "return pickup accepted", "rider_tracking_id": callerID})
}

// CompletePickup handles POST /returns/:id/pickup-complete
// Rider records pickup with photo proof.
func (h *ReturnHandler) CompletePickup(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "return_request_id is required"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "AUTH_TOKEN_INVALID"})
		return
	}

	if role != "rider" && role != "admin" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN: only riders can complete pickup"})
		return
	}

	var req struct {
		PhotoURL string `json:"photo_url" binding:"required"`
		OTPCode  string `json:"otp_code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.RecordPickup(c.Request.Context(), id, req.PhotoURL, req.OTPCode); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "pickup completed"})
}

// DeliverToVendor handles POST /returns/:id/deliver-to-vendor
// Rider delivers return to vendor store with photo proof.
func (h *ReturnHandler) DeliverToVendor(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "return_request_id is required"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "AUTH_TOKEN_INVALID"})
		return
	}

	if role != "rider" && role != "admin" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN: only riders can deliver to vendor"})
		return
	}

	var req struct {
		PhotoURL string `json:"photo_url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.RecordDeliveryToVendor(c.Request.Context(), id, req.PhotoURL); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "delivered to vendor"})
}

// VerifyReturn handles POST /returns/:id/vendor-verify
// Vendor verifies the returned product.
func (h *ReturnHandler) VerifyReturn(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "return_request_id is required"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "AUTH_TOKEN_INVALID"})
		return
	}

	if role != "vendor" && role != "admin" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN: only vendors can verify returns"})
		return
	}

	var req models.VerifyReturnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.VerifyByVendor(c.Request.Context(), id, req.Verified, req.PhotoURL, req.Notes); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	status := "return_verified"
	if !req.Verified {
		status = "return_disputed"
	}

	c.JSON(http.StatusOK, gin.H{"status": status})
}

// DisputeReturn handles POST /returns/:id/vendor-dispute
// Vendor disputes the returned product (alternative to verify).
// Photo evidence is REQUIRED for disputes.
func (h *ReturnHandler) DisputeReturn(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "return_request_id is required"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "AUTH_TOKEN_INVALID"})
		return
	}

	if role != "vendor" && role != "admin" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN: only vendors can dispute returns"})
		return
	}

	var req models.DisputeReturnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Photo evidence is REQUIRED for disputes
	if req.PhotoURL == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Photo proof is required for disputes. Please upload evidence of the product condition."})
		return
	}

	if err := h.svc.VerifyByVendor(c.Request.Context(), id, false, req.PhotoURL, req.Reason); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "return_disputed",
		"message": "Dispute submitted. Escrow has been frozen. Admin will review within 48 hours.",
	})
}

// CancelReturn handles POST /returns/:id/cancel
// Customer cancels a return request before rider pickup.
func (h *ReturnHandler) CancelReturn(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "return_request_id is required"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "AUTH_TOKEN_INVALID"})
		return
	}

	// Ownership check: only the customer who owns the return or an admin can cancel
	if role != "admin" {
		current, err := h.svc.GetReturnRequest(c.Request.Context(), id)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "RETURN_NOT_FOUND"})
			return
		}
		if current.CustomerTrackingID != callerID {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN: you can only cancel your own return"})
			return
		}
	}

	if err := h.svc.CancelReturn(c.Request.Context(), id); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "return cancelled"})
}

// RegisterRoutes attaches the return handlers to the router.
func (h *ReturnHandler) RegisterRoutes(router *gin.Engine) {
	returns := router.Group("/api/v1/returns", middleware.JWTAuth())
	{
		returns.GET("/:id", h.GetReturnRequest)
		returns.GET("/order/:order_tracking_id", h.GetReturnByOrder)
		returns.POST("/:id/accept-pickup", h.AcceptReturnPickup)
		returns.POST("/:id/pickup-complete", h.CompletePickup)
		returns.POST("/:id/deliver-to-vendor", h.DeliverToVendor)
		returns.POST("/:id/vendor-verify", h.VerifyReturn)
		returns.POST("/:id/vendor-dispute", h.DisputeReturn)
		returns.POST("/:id/cancel", h.CancelReturn)
	}

	// Return request is registered on order routes
	router.POST("/api/v1/orders/:tracking_id/return-request", middleware.JWTAuth(), h.RequestReturn)
}
