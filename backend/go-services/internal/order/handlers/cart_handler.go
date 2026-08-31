package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/order/models"
	"github.com/omnigo/backend/internal/order/service"
)

type CartHandler struct {
	svc *service.CartService
}

func NewCartHandler(svc *service.CartService) *CartHandler {
	return &CartHandler{svc: svc}
}

// GetCart GET /api/v1/cart
func (h *CartHandler) GetCart(c *gin.Context) {
	userID, exists := c.Get("tracking_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	cart, err := h.svc.GetCart(c.Request.Context(), userID.(string))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, cart)
}

// AddItem POST /api/v1/cart/items
func (h *CartHandler) AddItem(c *gin.Context) {
	userID, exists := c.Get("tracking_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var req models.AddToCartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.AddItem(c.Request.Context(), userID.(string), req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "Item added to cart"})
}

// UpdateItem PUT /api/v1/cart/items/:product_id
func (h *CartHandler) UpdateItem(c *gin.Context) {
	userID, exists := c.Get("tracking_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	productIDStr := c.Param("product_id")
	productID, err := strconv.ParseInt(productIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid product ID"})
		return
	}

	var req models.UpdateCartItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.svc.UpdateItemQuantity(c.Request.Context(), userID.(string), productID, req.Quantity); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Cart item updated"})
}

// RemoveItem DELETE /api/v1/cart/items/:product_id
func (h *CartHandler) RemoveItem(c *gin.Context) {
	userID, exists := c.Get("tracking_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	productIDStr := c.Param("product_id")
	productID, err := strconv.ParseInt(productIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid product ID"})
		return
	}

	if err := h.svc.RemoveItem(c.Request.Context(), userID.(string), productID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Cart item removed"})
}

// ClearCart DELETE /api/v1/cart
func (h *CartHandler) ClearCart(c *gin.Context) {
	userID, exists := c.Get("tracking_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	if err := h.svc.ClearCart(c.Request.Context(), userID.(string)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Cart cleared"})
}
