package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/product/models"
	"github.com/omnigo/backend/internal/product/service"
	sharedAuth "github.com/omnigo/backend/internal/shared/auth"
)

type VendorProductHandler struct {
	svc *service.ProductService
}

func NewVendorProductHandler(svc *service.ProductService) *VendorProductHandler {
	return &VendorProductHandler{svc: svc}
}

// extractVendorTrackingID parses the Authorization header using the shared
// JWT parser. Falls back to legacy raw tracking-ID tokens for backward compat.
func (h *VendorProductHandler) extractVendorTrackingID(c *gin.Context) (string, error) {
	tid, err := sharedAuth.ExtractTrackingIDFromHeader(c.GetHeader("Authorization"))
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(tid, "VEND-") {
		return "", fmt.Errorf("token does not contain a vendor identity")
	}
	return tid, nil
}

// ToggleStock handles PATCH /api/v1/vendor/products/:product_id/stock
func (h *VendorProductHandler) ToggleStock(c *gin.Context) {
	vendorID, err := h.extractVendorTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	productID := c.Param("product_id")
	if productID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "product_id parameter is required"})
		return
	}

	var req struct {
		Stock *int `json:"stock" binding:"required,min=0"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json payload structure: " + err.Error()})
		return
	}

	err = h.svc.UpdateProductStockSecure(c.Request.Context(), productID, *req.Stock, vendorID)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":     "success",
		"message":    "product stock updated successfully",
		"product_id": productID,
		"new_stock":  req.Stock,
	})
}

// DeleteProduct handles DELETE /api/v1/vendor/products/:product_id
func (h *VendorProductHandler) DeleteProduct(c *gin.Context) {
	vendorID, err := h.extractVendorTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	productID := c.Param("product_id")
	if productID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "product_id parameter is required"})
		return
	}

	err = h.svc.DeleteProductSecure(c.Request.Context(), productID, vendorID)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":     "success",
		"message":    "product deleted successfully from catalog",
		"product_id": productID,
	})
}

// AddProduct handles POST /api/v1/vendor/products/
// The vendor tracking ID is pulled from the Authorization header and the
// store ownership is verified server-side — a vendor cannot spoof
// another merchant's catalog.
func (h *VendorProductHandler) AddProduct(c *gin.Context) {
	vendorID, err := h.extractVendorTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	var req models.VendorCreateProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload: " + err.Error()})
		return
	}

	prod, err := h.svc.CreateProductForVendor(c.Request.Context(), vendorID, &req)
	if err != nil {
		// Distinguish ownership failures (403) from DB failures (500).
		if strings.Contains(err.Error(), "ownership verification failed") {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, prod)
}

// UpdateProduct handles PUT /api/v1/vendor/products/:product_id
// Performs a partial update: only non-nil fields in the JSON body are written.
func (h *VendorProductHandler) UpdateProduct(c *gin.Context) {
	vendorID, err := h.extractVendorTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	productID := c.Param("product_id")
	if productID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "product_id parameter is required"})
		return
	}

	var req models.UpdateProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload: " + err.Error()})
		return
	}

	if err := h.svc.UpdateProduct(c.Request.Context(), productID, vendorID, &req); err != nil {
		// Ownership violations return 403; everything else 500.
		if strings.Contains(err.Error(), "not found or unauthorized vendor") {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":     "success",
		"message":    "product updated successfully",
		"product_id": productID,
	})
}

// ListVendorProducts handles GET /api/v1/vendor/products?limit=20&offset=0
func (h *VendorProductHandler) ListVendorProducts(c *gin.Context) {
	vendorID, err := h.extractVendorTrackingID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	offset, _ := strconv.Atoi(c.Query("offset"))
	if offset < 0 {
		offset = 0
	}

	products, total, err := h.svc.ListVendorProducts(c.Request.Context(), vendorID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"products":    products,
		"total_count": total,
		"limit":       limit,
		"offset":      offset,
	})
}

// RegisterRoutes registers endpoints on Gin engine
func (h *VendorProductHandler) RegisterRoutes(router *gin.Engine) {
	vendor := router.Group("/api/v1/vendor/products")
	{
		vendor.GET("/", h.ListVendorProducts)
		vendor.POST("/", h.AddProduct)
		vendor.PUT("/:product_id", h.UpdateProduct)
		vendor.PATCH("/:product_id/stock", h.ToggleStock)
		vendor.DELETE("/:product_id", h.DeleteProduct)
	}
}
