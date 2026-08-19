package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/payment/payfast"
)

type SettlementPayload struct {
	InternalTxnID      string                   `json:"internal_txn_id"`
	OrderID            string                   `json:"order_id"`
	GatewayTxnID       string                   `json:"gateway_txn_id"`
	StoreID            string                   `json:"store_id"`
	DeliveryTrackingID string                   `json:"delivery_tracking_id"`
	TotalAmount        float64                  `json:"total_amount"`
	Currency           string                   `json:"currency"`
	AdminRevenue       float64                  `json:"admin_revenue"`
	VendorEscrow       float64                  `json:"vendor_escrow"`
	DeliveryEscrow     float64                  `json:"delivery_escrow"`
	IdempotencyKey     string                   `json:"idempotency_key"`
	Transfers          []SettlementTransferItem `json:"transfers"`
}

type SettlementTransferItem struct {
	DebitAccount  string  `json:"debit_account"`
	CreditAccount string  `json:"credit_account"`
	Amount        float64 `json:"amount"`
	Idempotency   string  `json:"idempotency"`
}

// SettlementWorker polls and processes 'payment_settlement' outbox events and reconciles gateway_pending payments.
type SettlementWorker struct {
	db      *pgxpool.Pool
	ledger  *ledger.Service
	escrow  *escrow.Service
	payfast *payfast.Client
	redis   redis.UniversalClient
}

func NewSettlementWorker(
	db *pgxpool.Pool,
	ledgerSvc *ledger.Service,
	escrowSvc *escrow.Service,
	payfastClient *payfast.Client,
	rdb redis.UniversalClient,
) *SettlementWorker {
	return &SettlementWorker{
		db:      db,
		ledger:  ledgerSvc,
		escrow:  escrowSvc,
		payfast: payfastClient,
		redis:   rdb,
	}
}

// Start runs the settlement processing loop and gateway reconciliation loop.
func (w *SettlementWorker) Start(ctx context.Context) {
	log.Println("[SettlementWorker] Starting background settlement and reconciliation worker...")

	pollTicker := time.NewTicker(2 * time.Second)
	defer pollTicker.Stop()

	reconTicker := time.NewTicker(60 * time.Second)
	defer reconTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[SettlementWorker] Stopping settlement worker...")
			return
		case <-pollTicker.C:
			w.processPendingSettlements(ctx)
		case <-reconTicker.C:
			w.reconcileGatewayPending(ctx)
		}
	}
}

func (w *SettlementWorker) processPendingSettlements(ctx context.Context) {
	rows, err := w.db.Query(ctx,
		`SELECT id, aggregate_id, payload FROM outbox_events
		 WHERE topic = 'payment_settlement' AND status = 'PENDING'
		 ORDER BY id ASC LIMIT 50`,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	type OutboxItem struct {
		ID          int64
		AggregateID string
		Payload     []byte
	}

	var items []OutboxItem
	for rows.Next() {
		var item OutboxItem
		if err := rows.Scan(&item.ID, &item.AggregateID, &item.Payload); err == nil {
			items = append(items, item)
		}
	}

	for _, item := range items {
		if err := w.processSingleSettlement(ctx, item.ID, item.Payload); err != nil {
			log.Printf("[SettlementWorker] Error processing settlement outbox event %d: %v", item.ID, err)
		}
	}
}

func (w *SettlementWorker) processSingleSettlement(ctx context.Context, eventID int64, payloadBytes []byte) error {
	var payload SettlementPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return fmt.Errorf("failed to unmarshal settlement payload: %w", err)
	}

	currency := payload.Currency
	if currency == "" {
		currency = "PKR"
	}

	// 1. Execute Ledger Transfers
	for _, tr := range payload.Transfers {
		if tr.Amount <= 0 {
			continue
		}
		_, err := w.ledger.Transfer(ctx, ledger.TransferRequest{
			DebitAccount:   ledger.Account(tr.DebitAccount),
			CreditAccount:  ledger.Account(tr.CreditAccount),
			Amount:         tr.Amount,
			Currency:       currency,
			ReferenceType:  "order",
			ReferenceID:    payload.OrderID,
			Description:    fmt.Sprintf("Payment settlement for order %s", payload.OrderID),
			IdempotencyKey: tr.Idempotency,
		})
		if err != nil {
			return fmt.Errorf("ledger transfer failed (%s -> %s, %.2f): %w", tr.DebitAccount, tr.CreditAccount, tr.Amount, err)
		}
	}

	// 2. Create Escrow Hold for vendor if applicable
	if payload.VendorEscrow > 0 && payload.StoreID != "" {
		if err := w.escrow.CreateHold(ctx, payload.OrderID, payload.StoreID, payload.VendorEscrow); err != nil {
			log.Printf("[SettlementWorker] Warning: Escrow hold creation failed or already exists for order %s: %v", payload.OrderID, err)
		}
	}

	// 3. Update Database State atomically (payment -> captured, order -> paid, outbox -> PROCESSED)
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin db tx: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`UPDATE payment_transactions SET status = 'captured', updated_at = NOW() WHERE transaction_id = $1`,
		payload.InternalTxnID,
	)
	if err != nil {
		return fmt.Errorf("failed to update payment to captured: %w", err)
	}

	_, err = tx.Exec(ctx,
		`UPDATE orders SET status = 'paid', payment_status = 'paid', updated_at = NOW() WHERE order_tracking_id = $1`,
		payload.OrderID,
	)
	if err != nil {
		return fmt.Errorf("failed to update order to paid: %w", err)
	}

	_, err = tx.Exec(ctx,
		`UPDATE outbox_events SET status = 'PROCESSED', processed_at = NOW() WHERE id = $1`,
		eventID,
	)
	if err != nil {
		return fmt.Errorf("failed to mark outbox event processed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit db settlement: %w", err)
	}

	log.Printf("[SettlementWorker] Successfully completed settlement for Order %s (Txn %s, Amount: %.2f %s)",
		payload.OrderID, payload.InternalTxnID, payload.TotalAmount, currency)
	return nil
}

func (w *SettlementWorker) reconcileGatewayPending(ctx context.Context) {
	if w.payfast == nil || !w.payfast.IsConfigured() {
		return
	}

	// Find payments that were left in gateway_pending due to network timeouts
	rows, err := w.db.Query(ctx,
		`SELECT pt.transaction_id, pt.order_tracking_id, pt.gateway_txn_id, pt.amount
		 FROM payment_transactions pt
		 WHERE pt.status = 'gateway_pending' AND pt.gateway_txn_id IS NOT NULL AND pt.gateway_txn_id != ''
		   AND pt.updated_at < NOW() - INTERVAL '30 seconds'
		 LIMIT 20`,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	type PendingTxn struct {
		InternalTxnID string
		OrderID       string
		GatewayTxnID  string
		Amount        float64
	}

	var list []PendingTxn
	for rows.Next() {
		var p PendingTxn
		if err := rows.Scan(&p.InternalTxnID, &p.OrderID, &p.GatewayTxnID, &p.Amount); err == nil {
			list = append(list, p)
		}
	}

	for _, p := range list {
		statusRes, err := w.payfast.GetTransactionStatus(ctx, p.GatewayTxnID)
		if err != nil {
			log.Printf("[SettlementWorker] Reconciliation check failed for %s (%s): %v", p.InternalTxnID, p.GatewayTxnID, err)
			continue
		}

		if statusRes.StatusCode == "00" && statusRes.BasketID == p.OrderID {
			// Verify amount
			if statusRes.TxnAmt != "" {
				if gatewayAmt, err := strconv.ParseFloat(statusRes.TxnAmt, 64); err == nil {
					expectedPaisa := int64(math.Round(p.Amount * 100))
					gatewayPaisa := int64(math.Round(gatewayAmt * 100))
					if expectedPaisa != gatewayPaisa {
						log.Printf("[SettlementWorker] Reconciliation detected amount mismatch for %s: expected %d, got %d", p.InternalTxnID, expectedPaisa, gatewayPaisa)
						_, _ = w.db.Exec(ctx, `UPDATE payment_transactions SET status = 'failed', error_message = 'Reconciliation amount mismatch', updated_at = NOW() WHERE transaction_id = $1`, p.InternalTxnID)
						continue
					}
				}
			}

			// Successful payment on gateway! Trigger outbox event to settle
			log.Printf("[SettlementWorker] Reconciliation verified SUCCESS for %s (%s). Enqueuing settlement...", p.InternalTxnID, p.GatewayTxnID)
			w.enqueueSettlementOutbox(ctx, p.InternalTxnID, p.OrderID, p.GatewayTxnID, p.Amount)
		} else if statusRes.StatusCode != "" && statusRes.StatusCode != "00" {
			log.Printf("[SettlementWorker] Reconciliation verified REJECTION for %s: %s", p.InternalTxnID, statusRes.StatusMsg)
			_, _ = w.db.Exec(ctx,
				`UPDATE payment_transactions SET status = 'failed', error_message = $1, updated_at = NOW() WHERE transaction_id = $2`,
				statusRes.StatusMsg, p.InternalTxnID,
			)
		}
	}
}

func (w *SettlementWorker) enqueueSettlementOutbox(ctx context.Context, internalTxnID, orderID, gatewayTxnID string, amount float64) {
	var storeID, deliveryTrackingID string
	_ = w.db.QueryRow(ctx, `SELECT store_tracking_id FROM orders WHERE order_tracking_id = $1`, orderID).Scan(&storeID)
	_ = w.db.QueryRow(ctx, `SELECT COALESCE(tracking_id, '') FROM deliveries WHERE order_tracking_id = $1`, orderID).Scan(&deliveryTrackingID)

	idempotencyKey := fmt.Sprintf("payfast:split:%s", gatewayTxnID)
	currency := os.Getenv("DEFAULT_CURRENCY")
	if currency == "" {
		currency = "PKR"
	}

	adminRate := 2.0
	if v := os.Getenv("DEFAULT_COMMISSION_RATE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			adminRate = f
		}
	}
	adminRevenue := amount * (adminRate / 100.0)
	vendorEscrow := amount - adminRevenue

	transfers := []SettlementTransferItem{
		{
			DebitAccount:  string(ledger.AccountPayFastHolding),
			CreditAccount: string(ledger.AccountAdminRevenue),
			Amount:        adminRevenue,
			Idempotency:   idempotencyKey + ":admin",
		},
		{
			DebitAccount:  string(ledger.AccountPayFastHolding),
			CreditAccount: string(ledger.AccountVendorLockedEscrow),
			Amount:        vendorEscrow,
			Idempotency:   idempotencyKey + ":vendor",
		},
	}

	outboxPayload, err := json.Marshal(SettlementPayload{
		InternalTxnID:      internalTxnID,
		OrderID:            orderID,
		GatewayTxnID:       gatewayTxnID,
		StoreID:            storeID,
		DeliveryTrackingID: deliveryTrackingID,
		TotalAmount:        amount,
		Currency:           currency,
		AdminRevenue:       adminRevenue,
		VendorEscrow:       vendorEscrow,
		IdempotencyKey:     idempotencyKey,
		Transfers:          transfers,
	})
	if err != nil {
		return
	}

	_, _ = w.db.Exec(ctx,
		`UPDATE payment_transactions SET status = 'settlement_pending', updated_at = NOW() WHERE transaction_id = $1`,
		internalTxnID,
	)
	_, _ = w.db.Exec(ctx,
		`INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at) VALUES ($1, 'payment_settlement', $2, 'PENDING', NOW())`,
		orderID, string(outboxPayload),
	)
}
