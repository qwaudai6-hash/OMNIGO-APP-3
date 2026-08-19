package handlers

import (
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/ledger"
	paymentRepo "github.com/omnigo/backend/internal/payment/repository"
	"github.com/omnigo/backend/internal/payment/service"
)

type WebhookHandler struct {
	orchestrator *service.Orchestrator
	ledgerSvc    *ledger.Service
	txnRepo      *paymentRepo.Repository
}

func NewWebhookHandler(orchestrator *service.Orchestrator, ledgerSvc *ledger.Service, txnRepo *paymentRepo.Repository) *WebhookHandler {
	return &WebhookHandler{
		orchestrator: orchestrator,
		ledgerSvc:    ledgerSvc,
		txnRepo:      txnRepo,
	}
}

func (h *WebhookHandler) HandleWebhook(c *gin.Context) {
	gatewayName := c.Param("gateway")

	// Read payload
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf("[Webhook] Error reading body: %v", err)
		c.Status(http.StatusBadRequest)
		return
	}

	// In production, signature comes from headers (e.g. Stripe-Signature)
	// Or for JazzCash/PayFast, it's inside the payload
	signature := c.GetHeader("Stripe-Signature")

	event, err := h.orchestrator.ProcessWebhook(gatewayName, payload, signature)
	if err != nil {
		log.Printf("[Webhook] Error processing webhook for %s: %v", gatewayName, err)
		c.Status(http.StatusBadRequest)
		return
	}

	if event.Status == "SUCCESS" {
		// Persist the captured payment transaction.
		if h.txnRepo != nil {
			_, err := h.txnRepo.Create(c.Request.Context(), &paymentRepo.PaymentTransaction{
				OrderID:        event.OrderID,
				Gateway:        gatewayName,
				GatewayTxnID:   event.TransactionID,
				Amount:         event.Amount,
				Currency:       event.Currency,
				Status:         paymentRepo.TxnCaptured,
				Kind:           paymentRepo.KindPayment,
				IdempotencyKey: fmt.Sprintf("webhook:%s:%s", gatewayName, event.TransactionID),
				Metadata: map[string]any{
					"customer_id": event.CustomerID,
				},
			})
			if err != nil {
				log.Printf("[Webhook] Failed to record payment transaction for order %s: %v", event.OrderID, err)
			}
		}

		// TigerBeetle Ledger Double-Entry (Gateway Clearing -> Central Escrow)
		req := ledger.TransferRequest{
			DebitAccount:   ledger.AccountGatewayClearing,
			CreditAccount:  ledger.AccountCentralEscrow,
			Amount:         event.Amount,
			Currency:       event.Currency,
			ReferenceType:  "order_payment",
			ReferenceID:    event.OrderID,
			Description:    fmt.Sprintf("Payment via %s for order %s", gatewayName, event.OrderID),
			IdempotencyKey: fmt.Sprintf("webhook_payment_%s_%s", gatewayName, event.TransactionID),
		}

		_, err := h.ledgerSvc.Transfer(c.Request.Context(), req)
		if err != nil {
			log.Printf("[Webhook] Ledger transfer failed for order %s: %v", event.OrderID, err)
			// Return 500 so the gateway retries
			c.Status(http.StatusInternalServerError)
			return
		}

		log.Printf("[Webhook] Payment recorded in ledger for order %s", event.OrderID)
	}

	c.Status(http.StatusOK)
}
