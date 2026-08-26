package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/ledger"
	middleware "github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/order/repository"
	orderSvc "github.com/omnigo/backend/internal/order/service"
	paymentRepo "github.com/omnigo/backend/internal/payment/repository"
	"github.com/omnigo/backend/internal/payment/service"
)

// RefundHandler exposes returns/cancellations/refund operations.
type RefundHandler struct {
	orchestrator *service.Orchestrator
	ledgerSvc    *ledger.Service
	txnRepo      *paymentRepo.Repository
	orderRepo    *repository.OrderRepository
	orderSvc     *orderSvc.OrderService
}

func NewRefundHandler(orchestrator *service.Orchestrator, ledgerSvc *ledger.Service, txnRepo *paymentRepo.Repository, orderRepo *repository.OrderRepository, orderSvc *orderSvc.OrderService) *RefundHandler {
	return &RefundHandler{
		orchestrator: orchestrator,
		ledgerSvc:    ledgerSvc,
		txnRepo:      txnRepo,
		orderRepo:    orderRepo,
		orderSvc:     orderSvc,
	}
}

type RefundRequest struct {
	OrderID     string  `json:"order_tracking_id" binding:"required"`
	Reason      string  `json:"reason" binding:"required"`
	Amount      float64 `json:"amount"`    // 0 = full refund
	RefundTo    string  `json:"refund_to"` // original | wallet
	RequestedBy string  `json:"requested_by" binding:"required"`
}

type CancelRequest struct {
	OrderID string `json:"order_tracking_id" binding:"required"`
	Reason  string `json:"reason" binding:"required"`
}

// ProcessRefund handles POST /api/v1/finance/refund.
// It records a refund transaction, calls the gateway reversal API when
// applicable, and moves funds from customer_refund_account.
func (h *RefundHandler) ProcessRefund(c *gin.Context) {
	var req RefundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, code, _ := h.executeRefund(c.Request.Context(), req)
	c.JSON(code, resp)
}

// executeRefund encapsulates the core refund business logic independently of gin.Context.
func (h *RefundHandler) executeRefund(ctx context.Context, req RefundRequest) (gin.H, int, error) {
	order, err := h.orderRepo.GetOrderByTrackingID(ctx, req.OrderID)
	if err != nil {
		return gin.H{"error": "order not found"}, http.StatusNotFound, err
	}

	refundAmount := req.Amount
	if refundAmount == 0 || refundAmount > order.TotalAmount {
		refundAmount = order.TotalAmount
	}

	// Idempotency: one successful refund per order for now.
	idempotencyKey := fmt.Sprintf("refund:%s", req.OrderID)
	if existing, err := h.txnRepo.GetByIDempotencyKey(ctx, idempotencyKey); err == nil && existing != nil {
		return gin.H{
			"status":         "already_processed",
			"transaction_id": existing.TransactionID,
		}, http.StatusOK, nil
	}

	gateway := order.PaymentGateway
	if gateway == "" {
		gateway = "cod"
	}

	var gatewayTxnID string
	// Look up the captured payment transaction to get the gateway reference.
	if paymentTxn, err := h.txnRepo.GetByOrderID(ctx, req.OrderID, paymentRepo.KindPayment); err == nil && paymentTxn != nil {
		gatewayTxnID = paymentTxn.GatewayTxnID
		
		if paymentTxn.Amount > 0 && refundAmount > paymentTxn.Amount {
			refundAmount = paymentTxn.Amount
		}
	}

	// Call gateway refund for online payments. COD is handled as a reversal.
	if gateway != "cod" && gateway != "wallet" {
		if h.orchestrator != nil {
			if err := h.orchestrator.Refund(ctx, gateway, gatewayTxnID, refundAmount); err != nil {
				return gin.H{"error": "gateway refund failed: " + err.Error()}, http.StatusBadGateway, err
			}
		}
	}

	// Record refund transaction.
	txn := &paymentRepo.PaymentTransaction{
		OrderID:        req.OrderID,
		Gateway:        gateway,
		GatewayTxnID:   gatewayTxnID,
		Amount:         refundAmount,
		Currency:       order.Currency,
		Status:         paymentRepo.TxnRefunded,
		Kind:           paymentRepo.KindRefund,
		IdempotencyKey: idempotencyKey,
		Metadata: map[string]any{
			"reason":       req.Reason,
			"requested_by": req.RequestedBy,
			"refund_to":    req.RefundTo,
		},
	}
	if _, err := h.txnRepo.Create(ctx, txn); err != nil {
		return gin.H{"error": "failed to record refund transaction: " + err.Error()}, http.StatusInternalServerError, err
	}

	// Ledger movement: central_escrow / gateway_clearing → customer_refund_account
	creditAccount := ledger.AccountCustomerRefund
	if req.RefundTo == "wallet" {
		creditAccount = ledger.AccountCustomerWallet
	}

	_, err = h.ledgerSvc.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   ledger.AccountCentralEscrow,
		CreditAccount:  creditAccount,
		Amount:         refundAmount,
		Currency:       order.Currency,
		ReferenceType:  "order_refund",
		ReferenceID:    req.OrderID,
		Description:    fmt.Sprintf("Refund for order %s: %s", req.OrderID, req.Reason),
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return gin.H{"error": "ledger refund transfer failed: " + err.Error()}, http.StatusInternalServerError, err
	}

	// Release reserved stock before finalising refund.
	if h.orderSvc != nil {
		if err := h.orderSvc.ReleaseStockForOrder(ctx, order); err != nil {
			return gin.H{"error": "refund recorded but stock release failed: " + err.Error()}, http.StatusInternalServerError, err
		}
	}

	// Update order status to refunded via order repo.
	if err := h.orderRepo.UpdateOrderStatus(ctx, req.OrderID, "refunded"); err != nil {
		return gin.H{"error": "refund recorded but order status update failed: " + err.Error()}, http.StatusInternalServerError, err
	}

	// Notify dependent services (notification, SMS, email) of the refund.
	if h.orderSvc != nil {
		h.orderSvc.EmitRefundEvent(ctx, req.OrderID, req.Reason, refundAmount, order.Currency)
	}

	return gin.H{
		"status":         "refunded",
		"order_id":       req.OrderID,
		"amount":         refundAmount,
		"currency":       order.Currency,
		"transaction_id": txn.TransactionID,
	}, http.StatusOK, nil
}

// ProcessCancellation handles POST /api/v1/finance/cancel.
// For COD orders that have not been delivered, it records a reversal.
// For already-paid online orders, it triggers a refund if eligible.
func (h *RefundHandler) ProcessCancellation(c *gin.Context) {
	var req CancelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	order, err := h.orderRepo.GetOrderByTrackingID(c.Request.Context(), req.OrderID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}

	gateway := order.PaymentGateway
	if gateway == "" {
		gateway = "cod"
	}

	// Cancellation idempotency key.
	idempotencyKey := fmt.Sprintf("cancel:%s", req.OrderID)
	if existing, err := h.txnRepo.GetByIDempotencyKey(c.Request.Context(), idempotencyKey); err == nil && existing != nil {
		c.JSON(http.StatusOK, gin.H{
			"status":         "already_cancelled",
			"transaction_id": existing.TransactionID,
		})
		return
	}

	switch gateway {
	case "cod":
		// COD cancellation before delivery: no cash collected, just mark cancelled.
		txn := &paymentRepo.PaymentTransaction{
			OrderID:        req.OrderID,
			Gateway:        "cod",
			Amount:         order.TotalAmount,
			Currency:       order.Currency,
			Status:         paymentRepo.TxnReversed,
			Kind:           paymentRepo.KindReversal,
			IdempotencyKey: idempotencyKey,
			Metadata: map[string]any{
				"reason": req.Reason,
				"event":  "cancel_before_delivery",
			},
		}
		if _, err := h.txnRepo.Create(c.Request.Context(), txn); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record cancellation: " + err.Error()})
			return
		}

	case "stripe", "payfast", "jazzcash", "easypaisa", "wallet":
		// If paid, issue refund. If still pending, record a reversal instead.
		paymentTxn, _ := h.txnRepo.GetByOrderID(c.Request.Context(), req.OrderID, paymentRepo.KindPayment)
		if paymentTxn != nil && paymentTxn.Status == paymentRepo.TxnCaptured {
			// Delegate directly to executeRefund without re-binding the HTTP request body.
			refundReq := RefundRequest{
				OrderID:     req.OrderID,
				Reason:      req.Reason,
				Amount:      order.TotalAmount,
				RefundTo:    "original",
				RequestedBy: "system_cancellation",
			}
			resp, code, _ := h.executeRefund(c.Request.Context(), refundReq)
			c.JSON(code, resp)
			return
		}

		// Payment not captured yet: reversal, no ledger movement required.
		txn := &paymentRepo.PaymentTransaction{
			OrderID:        req.OrderID,
			Gateway:        gateway,
			Amount:         order.TotalAmount,
			Currency:       order.Currency,
			Status:         paymentRepo.TxnReversed,
			Kind:           paymentRepo.KindReversal,
			IdempotencyKey: idempotencyKey,
			Metadata: map[string]any{
				"reason": req.Reason,
				"event":  "cancel_before_capture",
			},
		}
		if _, err := h.txnRepo.Create(c.Request.Context(), txn); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record reversal: " + err.Error()})
			return
		}

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported payment gateway: " + gateway})
		return
	}

	// Release reserved stock for cancelled orders.
	if h.orderSvc != nil {
		if err := h.orderSvc.ReleaseStockForOrder(c.Request.Context(), order); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "cancellation recorded but stock release failed: " + err.Error()})
			return
		}
	}

	_ = h.orderRepo.UpdateOrderStatus(c.Request.Context(), req.OrderID, "cancelled")

	// Notify dependent services of the cancellation.
	if h.orderSvc != nil {
		h.orderSvc.EmitCancelEvent(c.Request.Context(), req.OrderID, req.Reason)
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "cancelled",
		"order_id": req.OrderID,
		"reason":   req.Reason,
	})
}

// GetRefundStatus handles GET /api/v1/finance/refund/:order_tracking_id.
func (h *RefundHandler) GetRefundStatus(c *gin.Context) {
	orderID := c.Param("order_tracking_id")
	txn, err := h.txnRepo.GetByOrderID(c.Request.Context(), orderID, paymentRepo.KindRefund)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if txn == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no refund found for order"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"order_id":       orderID,
		"transaction_id": txn.TransactionID,
		"status":         txn.Status,
		"amount":         txn.Amount,
		"currency":       txn.Currency,
		"gateway":        txn.Gateway,
		"updated_at":     time.UnixMilli(txn.UpdatedAt).Format(time.RFC3339),
	})
}

// RegisterRefundRoutes attaches finance endpoints. These move real money, so
// they require an authenticated admin (or internal service) — never public.
func (h *RefundHandler) RegisterRefundRoutes(router *gin.Engine) {
	finance := router.Group("/api/v1/finance", middleware.JWTAuth(), middleware.RoleRequired("admin"))
	{
		finance.POST("/refund", h.ProcessRefund)
		finance.POST("/cancel", h.ProcessCancellation)
		finance.GET("/refund/:order_tracking_id", h.GetRefundStatus)
	}
}
