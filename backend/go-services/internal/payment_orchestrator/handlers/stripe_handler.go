package handlers

import (
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/payment_orchestrator/service"
	"github.com/omnigo/backend/internal/shared/middleware"
)

// StripeSplitHandler is a Gin controller for Stripe checkout + webhook + refund endpoints.
type StripeSplitHandler struct {
	service *service.StripeService
}

// NewStripeSplitHandler constructs a new handler.
func NewStripeSplitHandler(svc *service.StripeService) *StripeSplitHandler {
	return &StripeSplitHandler{service: svc}
}

// RegisterRoutes registers all Stripe endpoints with the router.
func (h *StripeSplitHandler) RegisterRoutes(r gin.IRoutes) {
	r.POST("/api/v1/payments/stripe/checkout", middleware.JWTAuth(), h.ProcessCheckout)
	r.POST("/api/v1/payments/stripe/webhook", h.WebhookCallback)
	r.POST("/api/v1/payments/stripe/refund", middleware.JWTAuth(), h.ProcessRefund)
	log.Println("[stripe] Routes registered: /api/v1/payments/stripe/{checkout,webhook,refund}")
}

// ProcessCheckout handles POST /api/v1/payments/stripe/checkout
// Creates a PaymentIntent and returns client_secret for frontend PaymentSheet.
func (h *StripeSplitHandler) ProcessCheckout(c *gin.Context) {
	var req service.StripeCheckoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	merchantUserID := middleware.GetTrackingID(c)
	if merchantUserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: missing user tracking ID"})
		return
	}

	clientIP := c.ClientIP()
	resp, err := h.service.ProcessCheckout(c.Request.Context(), merchantUserID, clientIP, &req)
	if err != nil {
		errMsg := err.Error()
		switch {
		case strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "does not belong"):
			c.JSON(http.StatusNotFound, gin.H{"error": errMsg})
		case strings.Contains(errMsg, "already paid") || strings.Contains(errMsg, "idempotent"):
			c.JSON(http.StatusConflict, gin.H{"error": errMsg})
		case strings.Contains(errMsg, "amount mismatch"):
			c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		case strings.Contains(errMsg, "not configured"):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Stripe payment is not configured"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": errMsg})
		}
		return
	}

	c.JSON(http.StatusOK, resp)
}

// WebhookCallback handles POST /api/v1/payments/stripe/webhook
// Verifies Stripe signature and processes payment events.
func (h *StripeSplitHandler) WebhookCallback(c *gin.Context) {
	const maxBodyBytes = int64(65536)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)

	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request body"})
		return
	}

	sigHeader := c.GetHeader("Stripe-Signature")
	if err := h.service.HandleWebhook(c.Request.Context(), payload, sigHeader); err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "signature verification failed") {
			log.Printf("[StripeWebhook] Signature verification failed: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid signature"})
			return
		}
		log.Printf("[StripeWebhook] Processing error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Webhook processing failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"received": true})
}

// ProcessRefund handles POST /api/v1/payments/stripe/refund
// Issues a refund for a completed payment.
func (h *StripeSplitHandler) ProcessRefund(c *gin.Context) {
	var req struct {
		OrderID string  `json:"order_id" binding:"required"`
		Amount  float64 `json:"amount"` // 0 = full refund
		Reason  string  `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	merchantUserID := middleware.GetTrackingID(c)
	if merchantUserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	if err := h.service.ProcessRefund(c.Request.Context(), req.OrderID, req.Amount, req.Reason); err != nil {
		errMsg := err.Error()
		switch {
		case strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "no completed payment"):
			c.JSON(http.StatusNotFound, gin.H{"error": errMsg})
		case strings.Contains(errMsg, "exceeds"):
			c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": errMsg})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "refunded",
		"order_id": req.OrderID,
		"amount":  req.Amount,
		"message": "Refund processed successfully",
	})
}
