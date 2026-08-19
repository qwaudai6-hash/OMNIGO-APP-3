package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/payment/service"
	walletService "github.com/omnigo/backend/internal/wallet/service"
)

type CheckoutHandler struct {
	orchestrator *service.Orchestrator
	walletSvc    *walletService.CustomerWalletService
}

func NewCheckoutHandler(orchestrator *service.Orchestrator, walletSvc *walletService.CustomerWalletService) *CheckoutHandler {
	return &CheckoutHandler{
		orchestrator: orchestrator,
		walletSvc:    walletSvc,
	}
}

type CheckoutReq struct {
	Gateway       string  `json:"gateway" binding:"required"`
	OrderID       string  `json:"order_id" binding:"required"`
	CustomerID    string  `json:"customer_id" binding:"required"`
	Amount        float64 `json:"amount" binding:"required"`
	Currency      string  `json:"currency" binding:"required"`
	ReturnURL     string  `json:"return_url"`
	CancelURL     string  `json:"cancel_url"`
	CustomerPhone string  `json:"customer_phone"`
	CustomerEmail string  `json:"customer_email"`
}

func (h *CheckoutHandler) CreateCheckout(c *gin.Context) {
	var req CheckoutReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Gateway == "wallet" {
		if h.walletSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wallet payments disabled"})
			return
		}
		err := h.walletSvc.DeductForPurchase(c.Request.Context(), req.CustomerID, req.OrderID, req.Amount)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "wallet payment failed: " + err.Error()})
			return
		}
		// Return immediate success
		c.JSON(http.StatusOK, service.CheckoutResponse{
			SessionID:   fmt.Sprintf("wallet_txn_%d", time.Now().UnixNano()),
			RedirectURL: req.ReturnURL, // Jump straight to success page
		})
		return
	}

	checkoutReq := service.CheckoutRequest{
		OrderID:       req.OrderID,
		CustomerID:    req.CustomerID,
		Amount:        req.Amount,
		Currency:      req.Currency,
		ReturnURL:     req.ReturnURL,
		CancelURL:     req.CancelURL,
		CustomerPhone: req.CustomerPhone,
		CustomerEmail: req.CustomerEmail,
	}

	res, err := h.orchestrator.CreateCheckout(c.Request.Context(), req.Gateway, checkoutReq)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, res)
}
