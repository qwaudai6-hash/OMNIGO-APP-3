package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/shared/auth"
	"github.com/omnigo/backend/internal/vendorstore/models"
	"github.com/omnigo/backend/internal/vendorstore/service"
)

type VendorHandler struct {
	svc *service.VendorService
}

func NewVendorHandler(svc *service.VendorService) *VendorHandler {
	return &VendorHandler{svc: svc}
}

// CreateStore HTTP handler for POST /stores
func (h *VendorHandler) CreateStore(c *gin.Context) {
	var req models.CreateStoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	store, err := h.svc.CreateStore(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, store)
}

// GetStore HTTP handler for GET /stores/:tracking_id
func (h *VendorHandler) GetStore(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}

	store, err := h.svc.GetStore(c.Request.Context(), trackingID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "store not found"})
		return
	}

	c.JSON(http.StatusOK, store)
}

// GetMyStore returns the authenticated vendor's store.
// Expects a valid vendor JWT (role == "vendor"). The tracking_id claim is used
// to look up the store row by vendor_tracking_id.
func (h *VendorHandler) GetMyStore(c *gin.Context) {
	trackingID, role, err := auth.ParseJWTFromHeader(c.GetHeader("Authorization"))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	if role != "vendor" {
		c.JSON(http.StatusForbidden, gin.H{"error": "vendor access required"})
		return
	}

	store, err := h.svc.GetStoreByVendorID(c.Request.Context(), trackingID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "store not found"})
		return
	}

	c.JSON(http.StatusOK, store)
}

// RegisterRoutes attaches the handlers to the gin router
func (h *VendorHandler) RegisterRoutes(router *gin.Engine) {
	stores := router.Group("/api/v1/stores")
	{
		stores.POST("/", h.CreateStore)
		stores.GET("/:tracking_id", h.GetStore)
	}

	vendor := router.Group("/api/v1/vendor")
	{
		vendor.GET("/stores/me", h.GetMyStore)
	}

	metricsH := NewVendorMetricsHandler(h.svc)
	router.GET("/api/v1/vendor/metrics", metricsH.GetMetrics)
}
