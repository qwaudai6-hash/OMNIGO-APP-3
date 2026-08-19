package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
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
}

// getClientIP extracts real customer IP safely taking reverse proxies into account.
func getClientIP(c *gin.Context) string {
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			ip := strings.TrimSpace(ips[0])
			if ip != "" {
				return ip
			}
		}
	}
	if xRealIP := c.GetHeader("X-Real-IP"); xRealIP != "" {
		return strings.TrimSpace(xRealIP)
	}
	return c.ClientIP()
}

// ProcessPayment handles POST /api/v1/payments/payfast/payment (Option C Token Flow).
func (h *PayFastSplitHandler) ProcessPayment(c *gin.Context) {
	var req payfastSvc.PaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload: " + err.Error()})
		return
	}

	merchantUserID := middleware.GetTrackingID(c)
	if merchantUserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: missing user tracking ID"})
		return
	}

	clientIP := getClientIP(c)
	resp, err := h.service.ProcessPayment(c.Request.Context(), merchantUserID, clientIP, req)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "not found") {
			c.JSON(http.StatusNotFound, gin.H{"error": errMsg})
			return
		}
		if strings.Contains(errMsg, "forbidden") {
			c.JSON(http.StatusForbidden, gin.H{"error": errMsg})
			return
		}
		if strings.Contains(errMsg, "conflict") {
			c.JSON(http.StatusConflict, gin.H{"error": errMsg})
			return
		}
		if strings.Contains(errMsg, "validation") || strings.Contains(errMsg, "expired") || strings.Contains(errMsg, "invalid") {
			c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": errMsg})
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

	clientIP := getClientIP(c)
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

	if strings.Contains(c.GetHeader("Accept"), "text/html") {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Payment Successful</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family:sans-serif;text-align:center;padding:50px 20px;">
    <h2 style="color:#2e7d32;">Payment Authenticated Successfully</h2>
    <p>Your order #<strong>%s</strong> is being settled.</p>
    <script>
        if (window.opener) { window.opener.postMessage({status: 'success', order_id: '%s'}, '*'); }
        if (window.FlutterChannel) { window.FlutterChannel.postMessage(JSON.stringify({status: 'success', order_id: '%s'})); }
    </script>
</body>
</html>`, orderID, orderID, orderID))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "settlement_pending",
		"order_id": orderID,
		"message":  "3DS payment authenticated and settlement enqueued",
	})
}

// respond3DSError sends a format-appropriate response (HTML for webviews, JSON for APIs).
func (h *PayFastSplitHandler) respond3DSError(c *gin.Context, statusCode int, message string) {
	if strings.Contains(c.GetHeader("Accept"), "text/html") {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(statusCode, fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Payment Authentication Failed</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family:sans-serif;text-align:center;padding:50px 20px;">
    <h2 style="color:#c62828;">Payment Authentication Failed</h2>
    <p>%s</p>
</body>
</html>`, message))
		return
	}
	c.JSON(statusCode, gin.H{"error": message})
}
