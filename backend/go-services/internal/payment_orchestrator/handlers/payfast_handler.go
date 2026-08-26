package handlers

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	pgx "github.com/jackc/pgx/v5"
	payfast "github.com/omnigo/backend/internal/payment/payfast"
	payfastSvc "github.com/omnigo/backend/internal/payment_orchestrator/service"
	"github.com/omnigo/backend/internal/shared/middleware"
)

// ThreeDSCallbackRequest payload from PayFast 3DS form POST.
type ThreeDSCallbackRequest struct {
	MD    string `form:"md" json:"md" binding:"required"`
	PaRes string `form:"paRes" json:"paRes" binding:"required"`
}

// PayFastSplitHandler is a thin HTTP controller delegating to PayFastService.
type PayFastSplitHandler struct {
	service *payfastSvc.PayFastService
}

// NewPayFastSplitHandler constructs a new PayFastSplitHandler.
func NewPayFastSplitHandler(svc *payfastSvc.PayFastService) *PayFastSplitHandler {
	return &PayFastSplitHandler{
		service: svc,
	}
}

// RegisterRoutes registers PayFast endpoints with the router.
func (h *PayFastSplitHandler) RegisterRoutes(r gin.IRoutes) {
	r.POST("/api/v1/payments/payfast/payment", middleware.JWTAuth(), h.ProcessPayment)
	r.POST("/api/v1/payments/payfast/3ds_callback", h.ThreeDSCallback)
	r.GET("/api/v1/payments/payfast/3ds_callback", h.ThreeDSCallback)
	r.POST("/api/v1/payments/payfast/ipn", h.IPNCallback)
	r.GET("/api/v1/payments/payfast/ipn", h.IPNCallback)
}

// ProcessPayment handles POST /api/v1/payments/payfast/payment (Option C Token Flow).
func (h *PayFastSplitHandler) ProcessPayment(c *gin.Context) {
	var req payfastSvc.PaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload: " + err.Error()})
		return
	}

	// Honor standard Idempotency-Key semantics: network-level retries of the same
	// checkout replay the original attempt instead of creating a second charge.
	req.IdempotencyKey = strings.TrimSpace(c.GetHeader("Idempotency-Key"))

	merchantUserID := middleware.GetTrackingID(c)
	if merchantUserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: missing user tracking ID"})
		return
	}

	clientIP := c.ClientIP()
	resp, err := h.service.ProcessPayment(c.Request.Context(), merchantUserID, clientIP, &req)
	if err != nil {
		// Classify strictly by sentinel errors — never by matching message text,
		// which breaks silently when wording changes.
		switch {
		case errors.Is(err, payfastSvc.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		case errors.Is(err, payfastSvc.ErrForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		case errors.Is(err, payfastSvc.ErrConflict):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		case errors.Is(err, payfastSvc.ErrValidation):
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	c.JSON(http.StatusOK, resp)
}

// ThreeDSCallback handles POST /api/v1/payments/payfast/3ds_callback.
func (h *PayFastSplitHandler) ThreeDSCallback(c *gin.Context) {
	var req ThreeDSCallbackRequest
	if err := c.ShouldBind(&req); err != nil {
		h.respond3DSError(c, http.StatusBadRequest, "Invalid 3DS callback parameters")
		return
	}

	clientIP := c.ClientIP()
	orderID, err := h.service.Handle3DSCallback(c.Request.Context(), req.MD, req.PaRes, clientIP)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "conflict") || strings.Contains(errMsg, "replay") {
			h.respond3DSError(c, http.StatusConflict, errMsg)
			return
		}
		if strings.Contains(errMsg, "timeout") {
			h.respond3DSError(c, http.StatusGatewayTimeout, "Payment verification timeout; reconciliation is processing in background")
			return
		}
		h.respond3DSError(c, http.StatusBadRequest, errMsg)
		return
	}

	safeOrderID := html.EscapeString(orderID)
	targetOrigin := postMessageTargetOrigin()
	if strings.Contains(c.GetHeader("Accept"), "text/html") {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Payment Successful</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family:sans-serif;text-align:center;padding:50px 20px;">
    <h2 style="color:#2e7d32;">Payment Authenticated Successfully</h2>
    <p>Your order #<strong>%s</strong> is being settled.</p>
    <script>
        if (window.opener) { window.opener.postMessage({status: 'success', order_id: '%s'}, '%s'); }
        if (window.FlutterChannel) { window.FlutterChannel.postMessage(JSON.stringify({status: 'success', order_id: '%s'})); }
    </script>
</body>
</html>`, safeOrderID, safeOrderID, targetOrigin, safeOrderID))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "settlement_pending",
		"order_id": orderID,
		"message":  "3DS payment authenticated and settlement enqueued",
	})
}

// postMessageTargetOrigin resolves the origin allowed to receive window.postMessage
// results from the 3DS popup/webview. Wildcard "*" leaks payment status to whatever
// page opened the window, so it is only used as a last-resort dev fallback when no
// trusted origin is configured.
func postMessageTargetOrigin() string {
	if o := strings.TrimSpace(os.Getenv("PAYFAST_WEB_ORIGIN")); o != "" {
		return o
	}
	// Prefer the first explicitly allow-listed CORS origin — it is already a trusted
	// frontend origin by definition.
	if origins := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS")); origins != "" {
		first := strings.TrimSpace(strings.Split(origins, ",")[0])
		if first != "" && first != "*" {
			return first
		}
	}
	log.Println("WARNING: [payfast] PAYFAST_WEB_ORIGIN / CORS_ALLOWED_ORIGINS unset — 3DS postMessage falling back to wildcard targetOrigin '*'")
	return "*"
}

// respond3DSError sends a format-appropriate response (HTML for webviews, JSON for APIs).
func (h *PayFastSplitHandler) respond3DSError(c *gin.Context, statusCode int, message string) {
	safeMessage := html.EscapeString(message)
	if strings.Contains(c.GetHeader("Accept"), "text/html") {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(statusCode, fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Payment Authentication Failed</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family:sans-serif;text-align:center;padding:50px 20px;">
    <h2 style="color:#c62828;">Payment Authentication Failed</h2>
    <p>%s</p>
</body>
</html>`, safeMessage))
		return
	}
	c.JSON(statusCode, gin.H{"error": safeMessage})
}

// IPNCallback handles the checkout_url Instant Payment Notification (IPN).
//
// HTTP status semantics matter here: PayFast retries notifications that fail with a
// server-side status. So transient failures return 503 (trigger a redelivery), while
// permanently-unprocessable payloads are acknowledged to stop pointless retries.
func (h *PayFastSplitHandler) IPNCallback(c *gin.Context) {
	var params payfastSvc.IPNParams
	if err := c.ShouldBind(&params); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid IPN payload: " + err.Error()})
		return
	}

	if err := h.service.HandleIPN(c.Request.Context(), params); err != nil {
		errMsg := err.Error()

		// Malformed/unverifiable payload: permanent, never retry.
		if strings.Contains(errMsg, "validation") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or unverifiable IPN payload"})
			return
		}

		// Unknown basket/transaction: nothing this deployment can do; acknowledge so
		// the gateway stops redelivering garbage (logged for audit).
		if errors.Is(err, pgx.ErrNoRows) || strings.Contains(errMsg, "no payment transaction found") {
			log.Printf("[PayFastHandler] IPN for unknown transaction ignored (basket_id=%s): %v", params.NormalizedBasketID(), err)
			c.JSON(http.StatusOK, gin.H{"status": "ignored"})
			return
		}

		// Transient infrastructure/gateway failure: ask PayFast to redeliver later.
		if payfast.IsTransient(err) || strings.Contains(errMsg, "timeout") || errors.Is(err, context.DeadlineExceeded) {
			log.Printf("[PayFastHandler] TRANSIENT IPN failure, returning 503 for redelivery (basket_id=%s): %v", params.NormalizedBasketID(), err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Transient processing failure; retry later"})
			return
		}

		// Everything else: genuine internal error, also retry-worthy.
		log.Printf("[PayFastHandler] IPN processing error for basket_id=%s: %v", params.BasketID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "IPN processing failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "received"})
}
