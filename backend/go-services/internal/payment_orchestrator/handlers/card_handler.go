package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	payfastSvc "github.com/omnigo/backend/internal/payment_orchestrator/service"
	"github.com/omnigo/backend/internal/shared/middleware"
)

// CardVaultHandler handles HTTP requests for saved card management.
type CardVaultHandler struct {
	vaultService *payfastSvc.CardVaultService
}

// NewCardVaultHandler constructs a new CardVaultHandler.
func NewCardVaultHandler(vault *payfastSvc.CardVaultService) *CardVaultHandler {
	return &CardVaultHandler{
		vaultService: vault,
	}
}

// RegisterRoutes registers card vault routes with Gin.
func (h *CardVaultHandler) RegisterRoutes(r gin.IRoutes) {
	cardsGroup := r.Use(middleware.JWTAuth())
	{
		cardsGroup.GET("/api/v1/payments/cards", h.ListCards)
		cardsGroup.DELETE("/api/v1/payments/cards/:card_id", h.DeleteCard)
		cardsGroup.POST("/api/v1/payments/cards/default", h.SetDefaultCard)
	}
}

// ListCards handles GET /api/v1/payments/cards
func (h *CardVaultHandler) ListCards(c *gin.Context) {
	customerID := middleware.GetTrackingID(c)
	if customerID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: missing customer tracking ID"})
		return
	}

	cards, err := h.vaultService.ListCards(c.Request.Context(), customerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list saved cards: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"cards": cards,
	})
}

// DeleteCard handles DELETE /api/v1/payments/cards/:card_id
func (h *CardVaultHandler) DeleteCard(c *gin.Context) {
	customerID := middleware.GetTrackingID(c)
	if customerID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: missing customer tracking ID"})
		return
	}

	cardID := c.Param("card_id")
	if cardID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "card_id path parameter is required"})
		return
	}

	if err := h.vaultService.DeleteCard(c.Request.Context(), customerID, cardID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Card deleted successfully",
		"card_id": cardID,
	})
}

// SetDefaultCard handles POST /api/v1/payments/cards/default
func (h *CardVaultHandler) SetDefaultCard(c *gin.Context) {
	customerID := middleware.GetTrackingID(c)
	if customerID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: missing customer tracking ID"})
		return
	}

	var req struct {
		CardID string `json:"card_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid payload: " + err.Error()})
		return
	}

	if err := h.vaultService.SetDefaultCard(c.Request.Context(), customerID, req.CardID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Default card updated successfully",
		"card_id": req.CardID,
	})
}
