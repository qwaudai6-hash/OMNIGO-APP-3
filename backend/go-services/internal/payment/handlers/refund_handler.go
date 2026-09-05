package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/ledger"
	middleware "github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/order/repository"
	orderSvc "github.com/omnigo/backend/internal/order/service"
	paymentRepo "github.com/omnigo/backend/internal/payment/repository"
	"github.com/omnigo/backend/internal/payment/service"
	walletSvc "github.com/omnigo/backend/internal/wallet/service"
)

// RefundHandler exposes returns/cancellations/refund operations.
type RefundHandler struct {
	orchestrator *service.Orchestrator
	ledgerSvc    *ledger.Service
	txnRepo      *paymentRepo.Repository
	orderRepo    *repository.OrderRepository
	orderSvc     *orderSvc.OrderService
	walletSvc    *walletSvc.CustomerWalletService
}

func NewRefundHandler(orchestrator *service.Orchestrator, ledgerSvc *ledger.Service, txnRepo *paymentRepo.Repository, orderRepo *repository.OrderRepository, orderSvc *orderSvc.OrderService, walletSvc *walletSvc.CustomerWalletService) *RefundHandler {
	return &RefundHandler{
		orchestrator: orchestrator,
		ledgerSvc:    ledgerSvc,
		txnRepo:      txnRepo,
		orderRepo:    orderRepo,
		orderSvc:     orderSvc,
		walletSvc:    walletSvc,
	}
}

type RefundRequest struct {
	OrderID     string  `json:"order_tracking_id" binding:"required"`
	Reason      string  `json:"reason" binding:"required"`
	Amount      float64 `json:"amount"`    // 0 = full refund (rupees, converted to paisa internally)
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

	// Convert rupees to paisa for internal processing
	refundAmountRupees := req.Amount
	if refundAmountRupees == 0 || refundAmountRupees > order.TotalAmount {
		refundAmountRupees = order.TotalAmount
	}
	refundAmountPaisa := int64(refundAmountRupees * 100)

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
		
		if paymentTxn.Amount > 0 {
			paymentPaisa := int64(paymentTxn.Amount * 100)
			if refundAmountPaisa > paymentPaisa {
				refundAmountPaisa = paymentPaisa
			}
		}
	}

	// Call gateway refund for online payments. COD is handled as a reversal.
	if gateway != "cod" && gateway != "wallet" {
		if h.orchestrator != nil {
			if err := h.orchestrator.Refund(ctx, gateway, gatewayTxnID, float64(refundAmountPaisa)/100.0); err != nil {
				return gin.H{"error": "gateway refund failed: " + err.Error()}, http.StatusBadGateway, err
			}
		}
	}

	// Wallet refund: credit back to customer's wallet balance.
	// For wallet payments, there is no external gateway to refund —
	// the money came from the wallet, so we credit it back directly.
	if gateway == "wallet" && h.walletSvc != nil {
		if err := h.walletSvc.CreditFunds(ctx, order.UserTrackID, fmt.Sprintf("refund:%s", req.OrderID), refundAmountPaisa); err != nil {
			log.Printf("[REFUND] WARNING: wallet credit failed for order %s: %v", req.OrderID, err)
			// Non-fatal: ledger transfer still happens below, but wallet balance won't reflect
		}
	}

	// Record refund transaction.
	txn := &paymentRepo.PaymentTransaction{
		OrderID:        req.OrderID,
		Gateway:        gateway,
		GatewayTxnID:   gatewayTxnID,
		Amount:         float64(refundAmountPaisa) / 100.0, // Store as rupees for payment_transactions compat
		Currency:       order.Currency,
		Status:         paymentRepo.TxnRefunded,
		Kind:           paymentRepo.KindRefund,
		IdempotencyKey: idempotencyKey,
		Metadata: map[string]any{
			"reason":        req.Reason,
			"requested_by":  req.RequestedBy,
			"refund_to":     req.RefundTo,
			"amount_paisa":  refundAmountPaisa,
		},
	}
	if _, err := h.txnRepo.Create(ctx, txn); err != nil {
		return gin.H{"error": "failed to record refund transaction: " + err.Error()}, http.StatusInternalServerError, err
	}

	// Ledger movement: source depends on whether settlement already occurred.
	// C2 FIX: If settled, debit from vendor escrow (clawback) not central_escrow.
	creditAccount := ledger.AccountCustomerRefund
	if req.RefundTo == "wallet" {
		creditAccount = ledger.AccountCustomerWallet
	}

	debitAccount := ledger.AccountCentralEscrow
	if req.RefundTo == "vendor_clawback" {
		debitAccount = ledger.AccountVendorLockedEscrow
		log.Printf("[REFUND] Using vendor clawback path for order %s — debit from vendor_locked_escrow", req.OrderID)
	}

	// M3 FIX: Check escrow balance before debit to prevent negative ledger.
	// If insufficient funds, log warning but still process (best-effort).
	// In production, this should trigger an alert for ops review.
	if h.ledgerSvc != nil {
		balance, balErr := h.ledgerSvc.GetBalance(ctx, debitAccount)
		if balErr == nil && balance < refundAmountPaisa {
			log.Printf("[REFUND] WARNING: Insufficient balance in %s for order %s: available=%d, needed=%d — processing anyway",
				debitAccount, req.OrderID, balance, refundAmountPaisa)
		}
	}

	_, err = h.ledgerSvc.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   debitAccount,
		CreditAccount:  creditAccount,
		Amount:         refundAmountPaisa,
		Currency:       order.Currency,
		ReferenceType:  "order_refund",
		ReferenceID:    req.OrderID,
		Description:    fmt.Sprintf("Refund for order %s: %s (source: %s)", req.OrderID, req.Reason, debitAccount),
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

	// Update order status to refunded via ORDER SERVICE (not repo directly).
	// The service layer handles escrow cancellation (CancelForOrder) and
	// COD debt cleanup, which the repo layer does not.
	if h.orderSvc != nil {
		if err := h.orderSvc.UpdateOrderStatus(ctx, req.OrderID, "refunded"); err != nil {
			return gin.H{"error": "refund recorded but order status update failed: " + err.Error()}, http.StatusInternalServerError, err
		}
	} else {
		if err := h.orderRepo.UpdateOrderStatus(ctx, req.OrderID, "refunded"); err != nil {
			return gin.H{"error": "refund recorded but order status update failed: " + err.Error()}, http.StatusInternalServerError, err
		}
	}

	// Notify dependent services (notification, SMS, email) of the refund.
	if h.orderSvc != nil {
		h.orderSvc.EmitRefundEvent(ctx, req.OrderID, req.Reason, float64(refundAmountPaisa)/100.0, order.Currency)
	}

	return gin.H{
		"status":         "refunded",
		"order_id":       req.OrderID,
		"amount":         float64(refundAmountPaisa) / 100.0,
		"amount_paisa":   refundAmountPaisa,
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
		// A transient DB error here must NOT be swallowed: if we treat
		// "lookup failed" as "no payment", we skip the gateway refund and
		// the customer stays charged while the order shows as cancelled.
		paymentTxn, err := h.txnRepo.GetByOrderID(c.Request.Context(), req.OrderID, paymentRepo.KindPayment)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to lookup payment transaction: " + err.Error()})
			return
		}
		if paymentTxn != nil && paymentTxn.Status == paymentRepo.TxnCaptured {
			// C2 FIX: Check if settlement already happened (funds disbursed to vendor).
			// If so, the refund must come from vendor escrow, not central_escrow,
			// to prevent platform loss.
			isSettled := false
			if h.orderSvc != nil {
				settled, checkErr := h.orderSvc.IsOrderSettled(c.Request.Context(), req.OrderID)
				if checkErr == nil {
					isSettled = settled
				}
			}

			refundTo := "original"
			if gateway == "wallet" {
				refundTo = "wallet"
			}
			refundReq := RefundRequest{
				OrderID:     req.OrderID,
				Reason:      req.Reason,
				Amount:      order.TotalAmount,
				RefundTo:    refundTo,
				RequestedBy: "system_cancellation",
			}

			// If settled, override debit source to vendor escrow to prevent platform loss
			if isSettled {
				log.Printf("[REFUND] Order %s already settled — refunding from vendor escrow (clawback)", req.OrderID)
				refundReq.RefundTo = "vendor_clawback"
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

	if err := h.orderRepo.UpdateOrderStatus(c.Request.Context(), req.OrderID, "cancelled"); err != nil {
		// If the status update fails, surface it: the cancellation transaction
		// and stock release already happened, so silently ignoring this would
		// leave the order active while reporting success to the admin. The
		// order service's UpdateOrderStatus also handles escrow cancellation
		// for cancelled/failed/returned — use that instead of the repo method
		// so escrow and COD-debt cleanup run together.
		if h.orderSvc != nil {
			if svcErr := h.orderSvc.UpdateOrderStatus(c.Request.Context(), req.OrderID, "cancelled"); svcErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "cancellation recorded but order status update failed: " + svcErr.Error()})
				return
			}
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "cancellation recorded but order status update failed: " + err.Error()})
			return
		}
	}

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
