package handlers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/product/models"
	"github.com/omnigo/backend/internal/product/service"
	"github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/shared/security"
)

type ProductHandler struct {
	svc *service.ProductService
	// internalSigner verifies the HMAC on the internal reserve/release
	// endpoints. It is nil when INTERNAL_API_SECRET is unset (development
	// mode); production must set it. The service panics at startup if
	// the env is missing — see cmd/product-service/main.go.
	internalSigner *security.InternalSigner
}

// NewProductHandler builds the handler with optional internal signing.
// If internalSigner is nil, the internal routes are not registered.
func NewProductHandler(svc *service.ProductService, internalSigner *security.InternalSigner) *ProductHandler {
	return &ProductHandler{svc: svc, internalSigner: internalSigner}
}

// CreateProduct is the public catalog create. The previous unauthenticated
// behaviour is removed: only authenticated vendors may create products, and
// the vendor_tracking_id in the body is OVERRIDDEN with the caller's JWT
// identity. This is the public mirror of vendor_product_handler.AddProduct.
//
// Reachable only by the Kong gateway's /api/v1/products/* path which the
// admin /api/v1/products/ POST route group guards.
func (h *ProductHandler) CreateProduct(c *gin.Context) {
	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "AUTH_TOKEN_INVALID",
			"message": "caller identity missing from context",
		})
		return
	}
	if !strings.HasPrefix(callerID, "VEND-") {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN_ROLE",
			"message": "only vendors may create products on the public catalog route",
		})
		return
	}

	var req models.VendorCreateProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// SECURITY: body never carries the vendor id — always derived from JWT.
	prod, err := h.svc.CreateProductForVendor(c.Request.Context(), callerID, &req)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, prod)
}

// GetProduct HTTP handler for GET /products/:tracking_id (public catalog read).
func (h *ProductHandler) GetProduct(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}

	prod, err := h.svc.GetProduct(c.Request.Context(), trackingID)
	if err != nil {
		// Fallback: internal callers (cart service) pass the numeric DB id.
		// If the identifier is all digits, retry as a numeric lookup so
		// cart price verification works against either id form.
		if id, convErr := strconv.ParseInt(trackingID, 10, 64); convErr == nil {
			prod, err = h.svc.GetProductByNumericID(c.Request.Context(), id)
		}
		if err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "product not found"})
			return
		}
	}

	c.JSON(http.StatusOK, prod)
}

// GetProductByID handles internal calls by numeric ID (for cart price verification).
func (h *ProductHandler) GetProductByID(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}
	prod, err := h.svc.GetProductByNumericID(c.Request.Context(), id)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "product not found"})
		return
	}
	c.JSON(http.StatusOK, prod)
}

// ListProducts HTTP handler for GET /products (public catalog read).
func (h *ProductHandler) ListProducts(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "20")
	offsetStr := c.DefaultQuery("offset", "0")

	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		limit = 20
	}
	offset, err := strconv.Atoi(offsetStr)
	if err != nil {
		offset = 0
	}

	search := c.Query("search")
	category := c.Query("category")
	storeID := c.Query("store_id")
	if storeID == "" {
		storeID = c.Query("store_tracking_id")
	}
	sort := c.Query("sort")
	minPrice, _ := strconv.ParseFloat(c.Query("min_price"), 64)
	maxPrice, _ := strconv.ParseFloat(c.Query("max_price"), 64)

	products, err := h.svc.ListProducts(c.Request.Context(), limit, offset, search, category, storeID, sort, minPrice, maxPrice)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, products)
}

// GetRecommendations HTTP handler for GET /products/:tracking_id/recommendations
func (h *ProductHandler) GetRecommendations(c *gin.Context) {
	trackingID := c.Param("tracking_id")
	if trackingID == "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "tracking_id is required"})
		return
	}

	recommendations, err := h.svc.GetRecommendations(c.Request.Context(), trackingID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, recommendations)
}

// RegisterRoutes attaches the handlers to the gin router.
//
// The public catalog group requires a JWT. POST /products is additionally
// gated to vendors only — see CreateProduct. The internal reserve/release
// group requires a valid HMAC signature on every request.
func (h *ProductHandler) RegisterRoutes(router *gin.Engine) {
	products := router.Group("/api/v1/products", middleware.JWTAuth())
	{
		products.POST("", h.CreateProduct)
		products.POST("/", h.CreateProduct)
		products.GET("", h.ListProducts)
		products.GET("/", h.ListProducts)
		products.GET("/:tracking_id", h.GetProduct)
		products.GET("/:tracking_id/recommendations", h.GetRecommendations)
		// Product image upload — used by the vendor add/edit product form.
		products.POST("/upload-image", h.UploadProductImage)
	}

	if h.internalSigner != nil {
		internal := router.Group("/api/v1/internal/products", middleware.InternalOnly(h.internalSigner))
		{
			internal.POST("/reserve", h.ReserveStock)
			internal.POST("/release", h.ReleaseStock)
			internal.GET("/:id", h.GetProductByID)
		}
	}
}

// ReserveStock is the saga-compensation entry point. Only sibling
// microservices with the INTERNAL_API_SECRET may call it.
func (h *ProductHandler) ReserveStock(c *gin.Context) {
	var req struct {
		Items []models.OrderItem `json:"items" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	reserved, err := h.svc.ReserveStock(c.Request.Context(), req.Items)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items":              reserved.Items,
		"vendor_tracking_id": reserved.VendorTrackID,
		"store_tracking_id":  reserved.StoreTrackID,
	})
}

// ReleaseStock is the saga-compensation entry point. Only sibling
// microservices with the INTERNAL_API_SECRET may call it.
func (h *ProductHandler) ReleaseStock(c *gin.Context) {
	var req struct {
		Items []models.OrderItem `json:"items" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.ReleaseStock(c.Request.Context(), req.Items); err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "released"})
}

// UploadProductImage handles POST /products/upload-image. It accepts a
// multipart image file and returns a public URL the vendor can paste into
// the product.create form's `image_url` field. The product row is NOT
// updated here — the vendor still has to PATCH the product with the URL.
//
// Authentication: JWT + role check for `vendor`. The uploaded file is
// scoped to a vendor-scoped directory so a malicious vendor can't browse
// another vendor's media (URLs are not enumerable: filename contains a
// millisecond timestamp + random suffix).
func (h *ProductHandler) UploadProductImage(c *gin.Context) {
	callerID := middleware.GetTrackingID(c)
	role := middleware.GetRole(c)
	if callerID == "" || role == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "AUTH_TOKEN_INVALID",
			"message": "caller identity missing from context",
		})
		return
	}
	if role != "vendor" && role != "admin" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error":   "FORBIDDEN_NOT_VENDOR",
			"message": "only vendors may upload product images",
		})
		return
	}

	file, err := c.FormFile("image")
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "missing image file"})
		return
	}

	// Cap upload size at 5 MB so a vendor can't fill the disk via a 4 GB
	// upload. The default gin limit is 32 MB but we tighten it here.
	const maxBytes = 5 * 1024 * 1024
	if file.Size > maxBytes {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": "image exceeds 5 MB limit",
		})
		return
	}

	// Whitelist content types so a vendor can't drop a .php shell into
	// ./uploads and trick the static handler into executing it.
	allowed := map[string]bool{
		"image/jpeg": true, "image/jpg": true,
		"image/png": true, "image/webp": true,
	}
	if !allowed[strings.ToLower(file.Header.Get("Content-Type"))] {
		c.AbortWithStatusJSON(http.StatusUnsupportedMediaType, gin.H{
			"error": "only JPEG, PNG, or WebP images are allowed",
		})
		return
	}

	dstDir := "./uploads/products"
	ext := strings.ToLower(filepath.Ext(file.Filename))
	if ext == "" {
		ext = ".jpg"
	}
	uniqueFilename := fmt.Sprintf("%s_%d%s", callerID, time.Now().UnixNano(), ext)
	dst := filepath.Join(dstDir, uniqueFilename)

	if err := os.MkdirAll(dstDir, os.ModePerm); err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to create upload directory"})
		return
	}
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"image_url":          "/uploads/products/" + uniqueFilename,
		"vendor_tracking_id": callerID,
		"size_bytes":         file.Size,
		"content_type":       file.Header.Get("Content-Type"),
	})
}
