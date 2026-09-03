package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	middleware "github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/order/repository"
	"github.com/omnigo/backend/internal/payment/service"
	walletService "github.com/omnigo/backend/internal/wallet/service"
)

type CheckoutHandler struct {
	orchestrator *service.Orchestrator
	walletSvc    *walletService.CustomerWalletService
	orderRepo    *repository.OrderRepository
	escrowSvc    escrowService
	db           *pgxpool.Pool // FIX C4: needed for settlement outbox events
}

type escrowService interface {
	CreateHold(ctx context.Context, orderID, vendorID string, amount float64) error
}

func NewCheckoutHandler(orchestrator *service.Orchestrator, walletSvc *walletService.CustomerWalletService, orderRepo *repository.OrderRepository, escrowSvc escrowService) *CheckoutHandler {
	return &CheckoutHandler{
		orchestrator: orchestrator,
		walletSvc:    walletSvc,
		orderRepo:    orderRepo,
		escrowSvc:    escrowSvc,
	}
}

// WithDB attaches the database pool for creating settlement outbox events
// when wallet payments succeed (FIX C4).
func (h *CheckoutHandler) WithDB(db *pgxpool.Pool) *CheckoutHandler {
	h.db = db
	return h
}

type CheckoutReq struct {
	Gateway       string  `json:"gateway" binding:"required"`
	OrderID       string  `json:"order_id" binding:"required"`
	CustomerID    string  `json:"customer_id"`
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
			// Also set payment_status='paid' so delivery accept eligibility passes
			_ = h.orderRepo.UpdatePaymentStatus(c.Request.Context(), req.OrderID, "paid")
		}
		// Create escrow hold for online payment (same as PayFast webhook path).
		if h.escrowSvc != nil {
			order, _ := h.orderRepo.GetOrderByTrackingID(c.Request.Context(), req.OrderID)
			if order != nil {
				_ = h.escrowSvc.CreateHold(c.Request.Context(), req.OrderID, order.VendorTrackID, req.Amount)
			}
		}
		// FIX C4: Create payment transaction + outbox event for SettlementWorker
		// to perform the proper 3-way ledger split (admin + vendor + delivery).
		if h.db != nil {
			txnID := fmt.Sprintf("wallet_%d", time.Now().UnixNano())
			_, _ = h.db.Exec(c.Request.Context(), `
				INSERT INTO payment_transactions (id, order_tracking_id, gateway, gateway_txn_id, amount, currency, status, kind, idempotency_key, created_at, updated_at)
				VALUES (gen_random_uuid(), $1, 'wallet', $2, $3, 'PKR', 'settlement_pending', 'payment', $4, NOW(), NOW())
				ON CONFLICT (idempotency_key) DO NOTHING
			`, req.OrderID, txnID, req.Amount, fmt.Sprintf("wallet:%s", req.OrderID))
			eventPayload := fmt.Sprintf(`{"order_id":"%s","gateway":"wallet","gateway_txn_id":"%s","amount":%.2f}`, req.OrderID, txnID, req.Amount)
			_, _ = h.db.Exec(c.Request.Context(), `
				INSERT INTO outbox_events (id, topic, key, payload, status, created_at, updated_at)
				VALUES (gen_random_uuid(), 'payment_settlement', $1, $2, 'PENDING', NOW(), NOW())
				ON CONFLICT (idempotency_key) DO NOTHING
			`, req.OrderID, eventPayload, fmt.Sprintf("wallet_settle:%s", req.OrderID))
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
