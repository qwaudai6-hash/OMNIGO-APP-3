package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	middleware "github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/order/repository"
	"github.com/omnigo/backend/internal/payment/service"
	walletService "github.com/omnigo/backend/internal/wallet/service"
)

type CheckoutHandler struct {
	orchestrator *service.Orchestrator
	walletSvc    *walletService.CustomerWalletService
	orderRepo    *repository.OrderRepository
}

func NewCheckoutHandler(orchestrator *service.Orchestrator, walletSvc *walletService.CustomerWalletService, orderRepo *repository.OrderRepository) *CheckoutHandler {
	return &CheckoutHandler{
		orchestrator: orchestrator,
		walletSvc:    walletSvc,
		orderRepo:    orderRepo,
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

	// SECURITY: never trust a body-supplied customer identity. Force the
	// customer to the authenticated JWT subject so callers cannot pay with
	// (or drain) someone else's wallet.
	req.CustomerID = middleware.GetTrackingID(c)
	if req.CustomerID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authenticated customer"})
		return
	}

	order, err := h.orderRepo.GetOrderByTrackingID(c.Request.Context(), req.OrderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	// Ownership check: the order must belong to the authenticated customer.
	if order.UserTrackID != req.CustomerID {
		c.JSON(http.StatusForbidden, gin.H{"error": "order does not belong to authenticated customer"})
		return
	}

	// Float-safe amount check: exact equality on floats rejects legitimate
	// requests that differ by a rounding unit.
	const amountEpsilon = 0.01
	if diff := req.Amount - order.TotalAmount; diff > amountEpsilon || diff < -amountEpsilon {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount mismatch: provided amount does not match order total"})
		return
	}
	
	// Use DB amount to ensure correctness
	req.Amount = order.TotalAmount

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
		// Wallet deduction succeeded — mark the order paid so it does not
		// sit in `pending` forever (mirrors the gateway webhook flow).
		if h.orderRepo != nil {
			if err := h.orderRepo.UpdateOrderStatus(c.Request.Context(), req.OrderID, "paid"); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "wallet deducted but failed to update order status: " + err.Error()})
				return
			}
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
		StoreID:       order.VendorStoreTrackID,
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
