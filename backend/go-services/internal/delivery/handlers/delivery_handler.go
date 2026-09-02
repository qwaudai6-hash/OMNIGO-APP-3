package handlers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/omnigo/backend/internal/delivery/models"
	"github.com/omnigo/backend/internal/delivery/service"
	"github.com/omnigo/backend/internal/shared/middleware"
)

const (
	osrmSourceHeader = "OSRM-Source"
)

type DeliveryHandler struct {
	svc *service.DeliveryService
}

func NewDeliveryHandler(svc *service.DeliveryService) *DeliveryHandler {
	return &DeliveryHandler{svc: svc}
}

// UpdateLocation HTTP handler for POST /delivery/location
func (h *DeliveryHandler) UpdateLocation(c *gin.Context) {
	var req models.UpdateLocationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := h.svc.UpdateLocation(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "location updated"})
}

// AcceptGig HTTP handler for POST /delivery/gig/accept
func (h *DeliveryHandler) AcceptGig(c *gin.Context) {
	var req models.AcceptGigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := h.svc.AcceptGig(c.Request.Context(), &req)
	if err != nil {
		errMsg := err.Error()
		if errMsg == "gig not found" || (len(errMsg) >= 8 && errMsg[:8] == "conflict") {
			c.JSON(http.StatusConflict, gin.H{"error": errMsg})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": errMsg})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "gig accepted"})
}

// UpdateGigStatus HTTP handler for PATCH /delivery/gig/:id/status
func (h *DeliveryHandler) UpdateGigStatus(c *gin.Context) {
	trackingID := c.Param("id")
	if trackingID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing tracking ID"})
		return
	}

	var req models.UpdateGigStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	gig, err := h.svc.UpdateGigStatus(c.Request.Context(), trackingID, &req)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "conflict") {
			c.JSON(http.StatusConflict, gin.H{"error": errStr})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": errStr})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            "gig status updated",
		"tracking_id":       gig.TrackingID,
		"new_status":        gig.Status,
		"assigned_rider_id": gig.AssignedRiderID,
	})
}

// GetRoute HTTP handler for GET /delivery/gig/:id/route
func (h *DeliveryHandler) GetRoute(c *gin.Context) {
	trackingID := c.Param("id")
	if trackingID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing tracking ID"})
		return
	}

	route, err := h.svc.GetRoute(c.Request.Context(), trackingID)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "no pickup") || strings.Contains(errStr, "no dropoff") || strings.Contains(errStr, "failed to load gig") {
			c.JSON(http.StatusNotFound, gin.H{"error": errStr})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": errStr})
		return
	}

	c.Header(osrmSourceHeader, route.Source)
	c.JSON(http.StatusOK, route)
}

// GetSurgeHeatmap HTTP handler for GET /delivery/surge-heatmap
func (h *DeliveryHandler) GetSurgeHeatmap(c *gin.Context) {
	heatmaps, err := h.svc.GetSurgeHeatmap(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"heatmaps": heatmaps})
}

// EstimateRide HTTP handler for POST /ride/estimate
func (h *DeliveryHandler) EstimateRide(c *gin.Context) {
	var req models.RideEstimateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	estimates, err := h.svc.EstimateRide(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, estimates)
}

// RegisterRoutes attaches the handlers to the gin router
func (h *DeliveryHandler) RegisterRoutes(router *gin.Engine) {
	// Serve static files from uploads folder
	router.Static("/uploads", "./uploads")

	delivery := router.Group("/api/v1/delivery", middleware.JWTAuth())
	{
		// Rider-only delivery actions
		riderOnly := delivery.Group("", middleware.RoleRequired("rider"))
		{
			riderOnly.POST("/location", h.UpdateLocation)
			riderOnly.POST("/gig/accept", h.AcceptGig)
			riderOnly.PATCH("/gig/:id/status", h.UpdateGigStatus)
			riderOnly.POST("/gig/upload-proof", h.UploadProof)
			riderOnly.POST("/gig/cancel", h.CancelGig)
		}

		// Authenticated delivery queries & disputes
		delivery.GET("/gig/:id/route", h.GetRoute)
		delivery.POST("/gig/dispute", h.DisputeGig)
		delivery.GET("/surge-heatmap", h.GetSurgeHeatmap)
	}

	ride := router.Group("/api/v1/ride", middleware.JWTAuth())
	{
		ride.POST("/estimate", h.EstimateRide)
		ride.POST("/bid", middleware.RoleRequired("customer"), h.CreateRideBid)
		ride.POST("/bid/counter", middleware.RoleRequired("rider"), h.SubmitCounterBid)
	}
}

// CreateRideBid handles customer custom fare negotiation requests
func (h *DeliveryHandler) CreateRideBid(c *gin.Context) {
	var req models.CreateBidRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	bid, err := h.svc.CreateRideBid(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, bid)
}

// SubmitCounterBid handles rider counter-offers
func (h *DeliveryHandler) SubmitCounterBid(c *gin.Context) {
	var req models.CounterBidRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.SubmitCounterBid(c.Request.Context(), &req); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "counter bid published"})
}

var allowedProofExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
}

// UploadProof handles rider uploading photos of delivery/pickup
func (h *DeliveryHandler) UploadProof(c *gin.Context) {
	callerID := middleware.GetTrackingID(c)
	if callerID == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	file, err := c.FormFile("photo")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing photo file"})
		return
	}

	// 5MB maximum file size check
	if file.Size > 5*1024*1024 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file exceeds 5MB limit"})
		return
	}

	ext := strings.ToLower(filepath.Ext(file.Filename))
	if !allowedProofExtensions[ext] {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "only JPG, PNG, or WEBP images are supported"})
		return
	}

	dstDir := "./uploads/proofs"
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create upload directory"})
		return
	}

	uniqueFilename := fmt.Sprintf("%s_%s%s", callerID, uuid.NewString(), ext)
	dst := filepath.Join(dstDir, uniqueFilename)

	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
		return
	}

	photoURL := fmt.Sprintf("/uploads/proofs/%s", uniqueFilename)
	c.JSON(http.StatusOK, gin.H{"photo_url": photoURL})
}

// DisputeGig handles customer filing a dispute on an order
func (h *DeliveryHandler) DisputeGig(c *gin.Context) {
	var req models.DisputeOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	callerID := middleware.GetTrackingID(c)
	if callerID == "" {
		callerID = c.GetHeader("X-User-Tracking-ID")
	}

	err := h.svc.DisputeGig(c.Request.Context(), &req, callerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "dispute reported"})
}

// CancelGig handles rider cancelling an active delivery
func (h *DeliveryHandler) CancelGig(c *gin.Context) {
	var req models.CancelGigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := h.svc.CancelGig(c.Request.Context(), &req)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "conflict") {
			c.JSON(http.StatusConflict, gin.H{"error": errStr})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": errStr})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "gig cancelled"})
}
