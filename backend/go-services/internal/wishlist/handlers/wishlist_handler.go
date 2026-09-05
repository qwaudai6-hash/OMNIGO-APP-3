package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/wishlist/service"
)

type WishlistHandler struct {
	svc *service.WishlistService
}

func NewWishlistHandler(svc *service.WishlistService) *WishlistHandler {
	return &WishlistHandler{svc: svc}
}

// ToggleFavorite handles POST /api/v1/wishlist/:product_id
func (h *WishlistHandler) ToggleFavorite(c *gin.Context) {
	customerID := middleware.GetTrackingID(c)

	productID := c.Param("product_id")
	if productID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "product_id is required"})
		return
	}

	isFavorited, err := h.svc.ToggleFavorite(c.Request.Context(), customerID, productID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":       "success",
		"is_favorited": isFavorited,
		"product_id":   productID,
	})
}

// ListFavorites handles GET /api/v1/wishlist
func (h *WishlistHandler) ListFavorites(c *gin.Context) {
	customerID := middleware.GetTrackingID(c)

	productIDs, err := h.svc.ListFavorites(c.Request.Context(), customerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if productIDs == nil {
		productIDs = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"product_tracking_ids": productIDs})
}

// RemoveFavorite handles DELETE /api/v1/wishlist/:product_id
func (h *WishlistHandler) RemoveFavorite(c *gin.Context) {
	customerID := middleware.GetTrackingID(c)

	productID := c.Param("product_id")
	if productID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "product_id is required"})
		return
	}

	if err := h.svc.RemoveFavorite(c.Request.Context(), customerID, productID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "removed from wishlist"})
}

// RegisterRoutes registers wishlist endpoints on the Gin engine
func (h *WishlistHandler) RegisterRoutes(router *gin.Engine) {
	wl := router.Group("/api/v1/wishlist", middleware.JWTAuth())
	{
		wl.POST("/:product_id", h.ToggleFavorite)
		wl.GET("", h.ListFavorites)
		wl.GET("/", h.ListFavorites)
		wl.DELETE("/:product_id", h.RemoveFavorite)
	}
}
