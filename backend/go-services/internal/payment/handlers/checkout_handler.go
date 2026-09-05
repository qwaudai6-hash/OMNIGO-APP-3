package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	payment_orchestrator "github.com/omnigo/backend/internal/payment_orchestrator"
	"github.com/omnigo/backend/internal/shared/middleware"
	"github.com/omnigo/backend/internal/order/repository"
	"github.com/omnigo/backend/internal/payment/service"
	walletService "github.com/omnigo/backend/internal/wallet/service"
)

type CheckoutHandler struct {
	orchestrator *service.Orchestrator
	walletSvc    *walletService.CustomerWalletService
	orderRepo    *repository.OrderRepository
	escrowSvc    escrowService
	calculator   *payment_orchestrator.CommissionCalculator
	db           *pgxpool.Pool
}

type escrowService interface {
	CreateHold(ctx context.Context, orderID, vendorID string, amount int64) error
}

func NewCheckoutHandler(orchestrator *service.Orchestrator, walletSvc *walletService.CustomerWalletService, orderRepo *repository.OrderRepository, escrowSvc escrowService, calculator *payment_orchestrator.CommissionCalculator) *CheckoutHandler {
	return &CheckoutHandler{
		orchestrator: orchestrator,
		walletSvc:    walletSvc,
		orderRepo:    orderRepo,
		escrowSvc:    escrowSvc,
		calculator:   calculator,
	}
}

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

	if order.UserTrackID != req.CustomerID {
		c.JSON(http.StatusForbidden, gin.H{"error": "order does not belong to authenticated customer"})
		return
	}

	const amountEpsilon = 0.01
	if diff := req.Amount - order.TotalAmount; diff > amountEpsilon || diff < -amountEpsilon {
		c.JSON(http.StatusBadRequest, gin.H{"error": "amount mismatch: provided amount does not match order total"})
		return
	}

	if order.Status == "paid" || order.PaymentStatus == "paid" {
		c.JSON(http.StatusConflict, gin.H{"error": "order has already been paid"})
		return
	}

	req.Amount = order.TotalAmount
	amountPaisa := order.TotalAmountPaisa

	if req.Gateway == "wallet" {
		if h.walletSvc == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "wallet payments disabled"})
			return
		}

		err := h.walletSvc.DeductForPurchase(c.Request.Context(), req.CustomerID, req.OrderID, amountPaisa)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "wallet payment failed: " + err.Error()})
			return
		}

		if h.db != nil && h.orderRepo != nil {
			tx, err := h.db.Begin(c.Request.Context())
			if err != nil {
				log.Printf("CRITICAL: Wallet deducted but failed to begin transaction for order %s: %v", req.OrderID, err)
				refundErr := h.walletSvc.RefundForFailedPayment(c.Request.Context(), req.CustomerID, req.OrderID, amountPaisa)
				if refundErr != nil {
					log.Printf("CRITICAL: Wallet deducted (₿%d), tx begin failed, AND refund failed for order %s: begin_err=%v, refund_err=%v",
						amountPaisa, req.OrderID, err, refundErr)
				} else {
					log.Printf("Wallet refund completed after tx begin failure for order %s: ₿%d returned", req.OrderID, amountPaisa)
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": "payment processing failed, wallet has been refunded"})
				return
			}
			defer tx.Rollback(c.Request.Context())

			_, err = tx.Exec(c.Request.Context(),
				`UPDATE orders SET status = 'paid', payment_status = 'paid', updated_at = NOW() WHERE order_tracking_id = $1`,
				req.OrderID)
			if err != nil {
				log.Printf("CRITICAL: Wallet deducted but order update failed for order %s: %v", req.OrderID, err)
				refundErr := h.walletSvc.RefundForFailedPayment(c.Request.Context(), req.CustomerID, req.OrderID, amountPaisa)
				if refundErr != nil {
					log.Printf("CRITICAL: Wallet deducted (₿%d), order update failed, AND refund failed for order %s: update_err=%v, refund_err=%v",
						amountPaisa, req.OrderID, err, refundErr)
				} else {
					log.Printf("Wallet refund completed after order update failure for order %s: ₿%d returned", req.OrderID, amountPaisa)
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": "payment processing failed, wallet has been refunded"})
				return
			}

			txnID := fmt.Sprintf("wallet_%d", time.Now().UnixNano())
			internalTxnID := fmt.Sprintf("wallet_%d", time.Now().UnixNano())

			var split *payment_orchestrator.SplitResult
			var deliveryTrackingID string
			if h.calculator != nil {
				_ = h.db.QueryRow(c.Request.Context(),
					`SELECT tracking_id FROM deliveries WHERE order_tracking_id = $1 AND status IN ('broadcasting','accepted','picked_up','in_transit') LIMIT 1`,
					req.OrderID).Scan(&deliveryTrackingID)

				split, _ = h.calculator.CalculateSplit(
					c.Request.Context(),
					amountPaisa,
					order.VendorStoreTrackID,
					deliveryTrackingID,
				)
			}

			adminRevenue := int64(0)
			vendorHoldPaisa := int64(0)
			if split != nil {
				vendorHoldPaisa = split.VendorEscrow
				adminRevenue = split.AdminRevenue
			}

			metadataJSON, marshalErr := json.Marshal(map[string]interface{}{
				"customer_id":  req.CustomerID,
				"amount_paisa": amountPaisa,
			})
			if marshalErr != nil {
				log.Printf("CRITICAL: failed to marshal payment metadata for order %s: %v", req.OrderID, marshalErr)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "payment processing failed"})
				return
			}
			_, err = tx.Exec(c.Request.Context(), `
				INSERT INTO payment_transactions (transaction_id, order_tracking_id, gateway, gateway_txn_id, amount, currency, status, kind, idempotency_key, metadata, created_at, updated_at)
				VALUES ($1, $2, 'wallet', $3, $4, 'PKR', 'settlement_pending', 'payment', $5, $6::jsonb, NOW(), NOW())
				ON CONFLICT (idempotency_key) DO NOTHING
			`, internalTxnID, req.OrderID, txnID, float64(amountPaisa)/100.0, fmt.Sprintf("wallet:%s", req.OrderID), string(metadataJSON))
			if err != nil {
				log.Printf("CRITICAL: Wallet deducted but payment_transactions insert failed for order %s: %v", req.OrderID, err)
				refundErr := h.walletSvc.RefundForFailedPayment(c.Request.Context(), req.CustomerID, req.OrderID, amountPaisa)
				if refundErr != nil {
					log.Printf("CRITICAL: Wallet deducted (₿%d), payment_transactions insert failed, AND refund failed for order %s: insert_err=%v, refund_err=%v",
						amountPaisa, req.OrderID, err, refundErr)
				} else {
					log.Printf("Wallet refund completed after payment_transactions insert failure for order %s: ₿%d returned", req.OrderID, amountPaisa)
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": "payment processing failed, wallet has been refunded"})
				return
			}

			eventPayload := fmt.Sprintf(
				`{"internal_txn_id":"%s","order_id":"%s","gateway":"wallet","gateway_txn_id":"%s","store_id":"%s","vendor_tracking_id":"%s","delivery_tracking_id":"%s","total_amount_paisa":%d,"currency":"PKR","admin_revenue_paisa":%d,"vendor_escrow_paisa":%d,"delivery_escrow_paisa":%d,"idempotency_key":"%s","transfers":[{"debit_account":"gateway_clearing","credit_account":"admin_revenue","amount_paisa":%d,"idempotency":"wallet:%s:admin"},{"debit_account":"gateway_clearing","credit_account":"vendor_locked_escrow","amount_paisa":%d,"idempotency":"wallet:%s:vendor"},{"debit_account":"gateway_clearing","credit_account":"central_escrow","amount_paisa":%d,"idempotency":"wallet:%s:delivery"}]}`,
				internalTxnID, req.OrderID, txnID, order.VendorStoreTrackID, order.VendorTrackID, "", amountPaisa, adminRevenue, vendorHoldPaisa, 0, fmt.Sprintf("wallet:%s", req.OrderID), adminRevenue, fmt.Sprintf("wallet:%s:admin", req.OrderID), vendorHoldPaisa, fmt.Sprintf("wallet:%s:vendor", req.OrderID), 0, fmt.Sprintf("wallet:%s:delivery", req.OrderID),
			)

			_, err = tx.Exec(c.Request.Context(), `
				INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at, updated_at)
				VALUES ($1, 'payment_settlement', $2, 'PENDING', NOW(), NOW())
			`, req.OrderID, eventPayload)
			if err != nil {
				log.Printf("CRITICAL: Wallet deducted but outbox_events insert failed for order %s: %v", req.OrderID, err)
				refundErr := h.walletSvc.RefundForFailedPayment(c.Request.Context(), req.CustomerID, req.OrderID, amountPaisa)
				if refundErr != nil {
					log.Printf("CRITICAL: Wallet deducted (₿%d), outbox insert failed, AND refund failed for order %s: insert_err=%v, refund_err=%v",
						amountPaisa, req.OrderID, err, refundErr)
				} else {
					log.Printf("Wallet refund completed after outbox insert failure for order %s: ₿%d returned", req.OrderID, amountPaisa)
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": "payment processing failed, wallet has been refunded"})
				return
			}

			if err := tx.Commit(c.Request.Context()); err != nil {
				log.Printf("CRITICAL: Wallet deducted but transaction commit failed for order %s: %v", req.OrderID, err)
				refundErr := h.walletSvc.RefundForFailedPayment(c.Request.Context(), req.CustomerID, req.OrderID, amountPaisa)
				if refundErr != nil {
					log.Printf("CRITICAL: Wallet deducted (₿%d), tx commit failed, AND refund failed for order %s: commit_err=%v, refund_err=%v",
						amountPaisa, req.OrderID, err, refundErr)
				} else {
					log.Printf("Wallet refund completed after tx commit failure for order %s: ₿%d returned", req.OrderID, amountPaisa)
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": "payment processing failed, wallet has been refunded"})
				return
			}

			log.Printf("Wallet payment completed successfully for order %s: ₿%d (txn=%s)", req.OrderID, amountPaisa, internalTxnID)
			c.JSON(http.StatusOK, service.CheckoutResponse{
				SessionID:   fmt.Sprintf("wallet_txn_%d", time.Now().UnixNano()),
				RedirectURL: req.ReturnURL,
			})
			return
		}

		if err := h.orderRepo.UpdateOrderStatus(c.Request.Context(), req.OrderID, "paid"); err != nil {
			refundErr := h.walletSvc.RefundForFailedPayment(c.Request.Context(), req.CustomerID, req.OrderID, amountPaisa)
			if refundErr != nil {
				log.Printf("CRITICAL: Wallet deducted (₿%d) but order update failed AND refund failed for order %s: update_err=%v, refund_err=%v",
					amountPaisa, req.OrderID, err, refundErr)
			} else {
				log.Printf("Wallet refund completed for failed order %s: ₿%d returned to customer %s", req.OrderID, amountPaisa, req.CustomerID)
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "payment processing failed, wallet has been refunded"})
			return
		}
		if err := h.orderRepo.UpdatePaymentStatus(c.Request.Context(), req.OrderID, "paid"); err != nil {
			log.Printf("Warning: failed to update payment_status to paid for order %s: %v", req.OrderID, err)
		}

		c.JSON(http.StatusOK, service.CheckoutResponse{
			SessionID:   fmt.Sprintf("wallet_txn_%d", time.Now().UnixNano()),
			RedirectURL: req.ReturnURL,
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
