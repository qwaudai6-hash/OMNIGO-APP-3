package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/ride/models"
	"github.com/omnigo/backend/internal/ride/repository"
	"github.com/omnigo/backend/internal/ride/service"
	"github.com/omnigo/backend/internal/shared/middleware"
)

type RideHandler struct {
	svc *service.RideService
}

func NewRideHandler(svc *service.RideService) *RideHandler {
	return &RideHandler{svc: svc}
}

// RequestRide HTTP handler for POST /api/v1/rides (customer only).
func (h *RideHandler) RequestRide(c *gin.Context) {
	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if role != "customer" || !strings.HasPrefix(callerID, "CUST-") {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN_ROLE",
			"message": "only customers may request rides",
		})
		return
	}

	var req models.RequestRidePayload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// SECURITY: ignore the body's customer_tracking_id — always use the JWT.
	req.CustomerTrackID = callerID

	ride, err := h.svc.RequestRide(c.Request.Context(), &req)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, ride)
}

// GetRide HTTP handler for GET /api/v1/rides/:tracking_id.
// Any authenticated party (customer, rider, admin) may read; ownership
// is verified by comparing the JWT identity to the ride's customer or rider.
func (h *RideHandler) GetRide(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}

	ride, err := h.svc.GetRide(c.Request.Context(), trackingID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "ride not found"})
		return
	}

	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if role != "admin" && ride.CustomerTrackID != callerID && ride.RiderTrackID != callerID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN_NOT_RIDE_PARTICIPANT",
			"message": "only the customer, the assigned rider, or an admin may read this ride",
		})
		return
	}

	c.JSON(http.StatusOK, ride)
}

// AcceptRide HTTP handler for POST /api/v1/rides/:tracking_id/accept (rider only).
func (h *RideHandler) AcceptRide(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}
	if !isRider(c) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN_ROLE"})
		return
	}

	var req models.AcceptRidePayload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Override body with JWT identity.
	req.RiderTrackID = middleware.GetTrackingID(c)

	ride, err := h.svc.AcceptRide(c.Request.Context(), trackingID, &req)
	if err != nil {
		if errors.Is(err, repository.ErrRideAlreadyAccepted) {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error":   "RIDE_ALREADY_ACCEPTED",
				"message": "this ride has already been accepted by another rider",
			})
			return
		}
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ride)
}

// UpdateRideStatus HTTP handler for PATCH /api/v1/rides/:tracking_id/status (rider only).
func (h *RideHandler) UpdateRideStatus(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}
	if !isRider(c) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN_ROLE"})
		return
	}

	var req models.UpdateRideStatusPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.RiderTrackID = middleware.GetTrackingID(c)

	ride, err := h.svc.UpdateRideStatus(c.Request.Context(), trackingID, &req)
	if err != nil {
		if errors.Is(err, repository.ErrInvalidRideTransition) {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error":   "INVALID_RIDE_TRANSITION",
				"message": err.Error(),
			})
			return
		}
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ride)
}

// CompleteRide HTTP handler for POST /api/v1/rides/:tracking_id/complete (rider only).
// This is the new endpoint that closes the ride and triggers the ledger split.
func (h *RideHandler) CompleteRide(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}
	if !isRider(c) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN_ROLE"})
		return
	}

	var req models.CompleteRidePayload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.RiderTrackID = middleware.GetTrackingID(c)

	ride, err := h.svc.CompleteRide(c.Request.Context(), trackingID, &req)
	if err != nil {
		if errors.Is(err, repository.ErrInvalidRideTransition) {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error":   "INVALID_RIDE_TRANSITION",
				"message": "ride is not in_progress",
			})
			return
		}
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ride)
}

// CancelRide HTTP handler for POST /api/v1/rides/:tracking_id/cancel
// (customer or rider — the service validates the state).
func (h *RideHandler) CancelRide(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}

	var req models.CancelRidePayload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.ActorTrackID = middleware.GetTrackingID(c)

	ride, err := h.svc.CancelRide(c.Request.Context(), trackingID, &req)
	if err != nil {
		if errors.Is(err, repository.ErrInvalidRideTransition) {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error":   "INVALID_RIDE_TRANSITION",
				"message": "ride cannot be cancelled in its current state",
			})
			return
		}
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ride)
}

// SubmitBid HTTP handler for POST /api/v1/rides/:tracking_id/bid (rider only).
func (h *RideHandler) SubmitBid(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}
	if !isRider(c) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN_ROLE"})
		return
	}

	var req models.SubmitBidPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.RiderTrackID = middleware.GetTrackingID(c)

	bid, err := h.svc.SubmitBid(c.Request.Context(), trackingID, &req)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, bid)
}

// GetBidsForRide HTTP handler for GET /api/v1/rides/:tracking_id/bids (customer only).
func (h *RideHandler) GetBidsForRide(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}
	role := middleware.GetRole(c)
	if role != "customer" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN_ROLE"})
		return
	}

	bids, err := h.svc.GetBidsForRide(c.Request.Context(), trackingID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"bids": bids})
}

// AcceptBid HTTP handler for POST /api/v1/rides/:tracking_id/accept-bid (customer only).
func (h *RideHandler) AcceptBid(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}
	role := middleware.GetRole(c)
	if role != "customer" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "FORBIDDEN_ROLE"})
		return
	}

	var req models.AcceptBidPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.CustomerTrackID = middleware.GetTrackingID(c)

	bid, err := h.svc.AcceptBid(c.Request.Context(), trackingID, &req)
	if err != nil {
		if errors.Is(err, repository.ErrRideAlreadyAccepted) {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error":   "RIDE_ALREADY_ACCEPTED",
				"message": "this ride has already been accepted or assigned",
			})
			return
		}
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, bid)
}

func isRider(c *gin.Context) bool {
	return middleware.GetRole(c) == "rider" &&
		strings.HasPrefix(middleware.GetTrackingID(c), "RIDR-")
}

// RegisterRoutes attaches the ride handlers to the gin router.
// All routes require a valid JWT; per-route authorization is enforced
// in the handler because the rule depends on both the role claim and the
// identity of the ride record being acted upon.
func (h *RideHandler) RegisterRoutes(router *gin.Engine) {
	rides := router.Group("/api/v1/rides", middleware.JWTAuth())
	{
		rides.POST("", h.RequestRide)
		rides.POST("/", h.RequestRide)
		rides.GET("/:tracking_id", h.GetRide)
		rides.POST("/:tracking_id/accept", h.AcceptRide)
		rides.PATCH("/:tracking_id/status", h.UpdateRideStatus)
		rides.POST("/:tracking_id/complete", h.CompleteRide)
		rides.POST("/:tracking_id/cancel", h.CancelRide)
		rides.POST("/:tracking_id/bid", h.SubmitBid)
		rides.GET("/:tracking_id/bids", h.GetBidsForRide)
		rides.POST("/:tracking_id/accept-bid", h.AcceptBid)
	}
}
