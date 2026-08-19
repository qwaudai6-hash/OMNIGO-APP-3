package handlers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/delivery/models"
	"github.com/omnigo/backend/internal/delivery/service"
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

	delivery := router.Group("/api/v1/delivery")
	{
		delivery.POST("/location", h.UpdateLocation)
		delivery.POST("/gig/accept", h.AcceptGig)
		delivery.PATCH("/gig/:id/status", h.UpdateGigStatus)
		delivery.GET("/gig/:id/route", h.GetRoute)
		delivery.POST("/gig/upload-proof", h.UploadProof)
		delivery.POST("/gig/dispute", h.DisputeGig)
		delivery.POST("/gig/cancel", h.CancelGig)
		delivery.GET("/surge-heatmap", h.GetSurgeHeatmap)
	}

	ride := router.Group("/api/v1/ride")
	{
		ride.POST("/estimate", h.EstimateRide)
		ride.POST("/bid", h.CreateRideBid)
		ride.POST("/bid/counter", h.SubmitCounterBid)
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

// UploadProof handles rider uploading photos of delivery/pickup
func (h *DeliveryHandler) UploadProof(c *gin.Context) {
	file, err := c.FormFile("photo")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing photo file"})
		return
	}

	dstDir := "./uploads"
	filename := filepath.Base(file.Filename)
	uniqueFilename := fmt.Sprintf("%d_%s", time.Now().UnixMilli(), filename)
	dst := filepath.Join(dstDir, uniqueFilename)

	if err := os.MkdirAll(dstDir, os.ModePerm); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create upload directory"})
		return
	}

	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
		return
	}

	photoURL := fmt.Sprintf("/uploads/%s", uniqueFilename)
	c.JSON(http.StatusOK, gin.H{"photo_url": photoURL})
}

// DisputeGig handles customer filing a dispute on an order
func (h *DeliveryHandler) DisputeGig(c *gin.Context) {
	var req models.DisputeOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	err := h.svc.DisputeGig(c.Request.Context(), &req)
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
