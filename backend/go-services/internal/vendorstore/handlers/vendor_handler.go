package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/shared/middleware"
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
	callerID := middleware.GetTrackingID(c)
	callerRole := middleware.GetRole(c)
	if callerID == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req models.CreateStoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if callerRole != "admin" {
		req.VendorTrackingID = callerID
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
func (h *VendorHandler) GetMyStore(c *gin.Context) {
	trackingID := middleware.GetTrackingID(c)
	if trackingID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	store, err := h.svc.GetStoreByVendorID(c.Request.Context(), trackingID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "store not found"})
		return
	}

	c.JSON(http.StatusOK, store)
}

// ListStores HTTP handler for GET /stores
func (h *VendorHandler) ListStores(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

	stores, err := h.svc.ListStores(c.Request.Context(), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, stores)
}

// RegisterRoutes attaches the handlers to the gin router
func (h *VendorHandler) RegisterRoutes(router *gin.Engine) {
	stores := router.Group("/api/v1/stores")
	{
		stores.GET("", h.ListStores)
		stores.GET("/", h.ListStores)
		stores.POST("", middleware.JWTAuth(), middleware.RoleRequired("vendor", "admin"), h.CreateStore)
		stores.POST("/", middleware.JWTAuth(), middleware.RoleRequired("vendor", "admin"), h.CreateStore)
		stores.GET("/:tracking_id", h.GetStore)
	}

	vendor := router.Group("/api/v1/vendor", middleware.JWTAuth(), middleware.RoleRequired("vendor", "admin"))
	{
		vendor.GET("/stores/me", h.GetMyStore)
	}

	metricsH := NewVendorMetricsHandler(h.svc)
	router.GET("/api/v1/vendor/metrics", middleware.JWTAuth(), middleware.RoleRequired("vendor", "admin"), metricsH.GetMetrics)
}
