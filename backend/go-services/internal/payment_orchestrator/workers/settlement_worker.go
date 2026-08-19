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
	"github.com/omnigo/backend/internal/payment_orchestrator"
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

// SettlementWorker polls and processes 'payment_settlement' outbox events and reconciles stuck payments.
type SettlementWorker struct {
	db         *pgxpool.Pool
	ledger     *ledger.Service
	escrow     *escrow.Service
	calculator *payment_orchestrator.CommissionCalculator
	payfast    *payfast.Client
	redis      redis.UniversalClient
}

func NewSettlementWorker(
	db *pgxpool.Pool,
	ledgerSvc *ledger.Service,
	escrowSvc *escrow.Service,
	calc *payment_orchestrator.CommissionCalculator,
	payfastClient *payfast.Client,
	rdb redis.UniversalClient,
) *SettlementWorker {
	return &SettlementWorker{
		db:         db,
		ledger:     ledgerSvc,
		escrow:     escrowSvc,
		calculator: calc,
		payfast:    payfastClient,
		redis:      rdb,
	}
}

// Start runs the settlement processing loop and gateway reconciliation loop.
func (w *SettlementWorker) Start(ctx context.Context) {
	log.Println("[SettlementWorker] Starting background settlement and reconciliation worker...")

	pollTicker := time.NewTicker(2 * time.Second)
	defer pollTicker.Stop()

	reconTicker := time.NewTicker(30 * time.Second)
	defer reconTicker.Stop()

	cleanupTicker := time.NewTicker(5 * time.Minute)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[SettlementWorker] Stopping settlement worker...")
			return
		case <-pollTicker.C:
			w.processPendingSettlements(ctx)
		case <-reconTicker.C:
			w.reconcileStuckPayments(ctx)
		case <-cleanupTicker.C:
			w.cleanupStalePending(ctx)
		}
	}
}

func (w *SettlementWorker) processPendingSettlements(ctx context.Context) {
	// Claim outbox events atomically using FOR UPDATE SKIP LOCKED
	// Includes events stuck in PROCESSING due to worker crashes older than 5 minutes
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT id, aggregate_id, payload FROM outbox_events
		 WHERE topic = 'payment_settlement' 
		   AND (status IN ('PENDING', 'pending') OR (status = 'PROCESSING' AND updated_at < NOW() - INTERVAL '5 minutes'))
		 ORDER BY id ASC LIMIT 50 FOR UPDATE SKIP LOCKED`,
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
	rows.Close()

	if len(items) == 0 {
		return
	}

	// Mark claimed items as PROCESSING within the transaction
	for _, item := range items {
		_, _ = tx.Exec(ctx, `UPDATE outbox_events SET status = 'PROCESSING', updated_at = NOW() WHERE id = $1`, item.ID)
	}
	if err := tx.Commit(ctx); err != nil {
		return
	}

	for _, item := range items {
		if err := w.processSingleSettlement(ctx, item.ID, item.Payload); err != nil {
			log.Printf("[SettlementWorker] Error processing settlement outbox event %d: %v", item.ID, err)
			// Revert to PENDING with backoff count so it can be retried by the worker loop
			_, _ = w.db.Exec(ctx,
				`UPDATE outbox_events 
				 SET status = 'PENDING', retry_count = retry_count + 1, error_message = $1, updated_at = NOW() 
				 WHERE id = $2`,
				err.Error(), item.ID,
			)
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

	// 1. Create Escrow Hold for vendor — Mandatory fail-closed verification.
	if payload.VendorEscrow > 0 && payload.StoreID != "" {
		if err := w.escrow.CreateHold(ctx, payload.OrderID, payload.StoreID, payload.VendorEscrow); err != nil {
			log.Printf("[SettlementWorker] CRITICAL: Escrow hold creation failed for order %s (Store: %s, Amount: %.2f): %v",
				payload.OrderID, payload.StoreID, payload.VendorEscrow, err)
			return fmt.Errorf("mandatory escrow hold creation failed: %w", err)
		}
	}

	// 2. Execute Ledger MultiTransfer (Atomic all-or-nothing double-entry split)
	var transferReqs []ledger.TransferRequest
	for _, tr := range payload.Transfers {
		if tr.Amount <= 0 {
			continue
		}
		transferReqs = append(transferReqs, ledger.TransferRequest{
			DebitAccount:   ledger.Account(tr.DebitAccount),
			CreditAccount:  ledger.Account(tr.CreditAccount),
			Amount:         tr.Amount,
			Currency:       currency,
			ReferenceType:  "order",
			ReferenceID:    payload.OrderID,
			Description:    fmt.Sprintf("Payment settlement for order %s", payload.OrderID),
			IdempotencyKey: tr.Idempotency,
		})
	}
	if len(transferReqs) > 0 {
		_, err := w.ledger.MultiTransfer(ctx, transferReqs)
		if err != nil {
			return fmt.Errorf("ledger multi-transfer failed for order %s: %w", payload.OrderID, err)
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
		`UPDATE outbox_events SET status = 'PROCESSED', processed_at = NOW(), updated_at = NOW() WHERE id = $1`,
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

// reconcileStuckPayments runs dual-strategy status inquiries for stuck 'gateway_pending' and 'processing' payments.
func (w *SettlementWorker) reconcileStuckPayments(ctx context.Context) {
	if w.payfast == nil || !w.payfast.IsConfigured() {
		return
	}

	tx, err := w.db.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)

	// Lock stuck payments with FOR UPDATE SKIP LOCKED
	rows, err := tx.Query(ctx,
		`SELECT pt.transaction_id, pt.order_tracking_id, COALESCE(pt.gateway_txn_id, ''), pt.amount, pt.status, pt.created_at
		 FROM payment_transactions pt
		 WHERE pt.status IN ('gateway_pending', 'processing')
		   AND pt.updated_at < NOW() - INTERVAL '1 minute'
		 ORDER BY pt.updated_at ASC LIMIT 20
		 FOR UPDATE SKIP LOCKED`,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	type StuckTxn struct {
		InternalTxnID string
		OrderID       string
		GatewayTxnID  string
		Amount        float64
		Status        string
		CreatedAt     time.Time
	}

	var list []StuckTxn
	for rows.Next() {
		var p StuckTxn
		if err := rows.Scan(&p.InternalTxnID, &p.OrderID, &p.GatewayTxnID, &p.Amount, &p.Status, &p.CreatedAt); err == nil {
			list = append(list, p)
		}
	}
	rows.Close()

	// Touch updated_at inside claiming transaction so concurrent pods won't pick up the same rows
	for _, p := range list {
		_, _ = tx.Exec(ctx, `UPDATE payment_transactions SET updated_at = NOW() WHERE transaction_id = $1`, p.InternalTxnID)
	}
	_ = tx.Commit(ctx)

	for _, p := range list {
		var statusRes *payfast.TransactionStatusResponse
		var inquiryErr error

		// Dual Inquiry Strategy: by Gateway Transaction ID, or fallback to Merchant Basket ID
		if p.GatewayTxnID != "" {
			statusRes, inquiryErr = w.payfast.GetTransactionStatus(ctx, p.GatewayTxnID)
		} else {
			log.Printf("[SettlementWorker] Missing gateway_txn_id for stuck payment %s. Falling back to BasketID inquiry: %s", p.InternalTxnID, p.OrderID)
			statusRes, inquiryErr = w.payfast.GetTransactionStatusByBasketID(ctx, p.OrderID)
		}

		if inquiryErr != nil {
			log.Printf("[SettlementWorker] Reconciliation status check failed for %s (Order: %s, GatewayID: %s): %v",
				p.InternalTxnID, p.OrderID, p.GatewayTxnID, inquiryErr)

			// If payment has been stuck for > 15 minutes and gateway returns not found/error, fail deterministically
			if time.Since(p.CreatedAt) > 15*time.Minute && !payfast.IsTransient(inquiryErr) {
				log.Printf("[SettlementWorker] Timing out stale stuck payment %s (>15m). Marking failed.", p.InternalTxnID)
				_, _ = w.db.Exec(ctx,
					`UPDATE payment_transactions 
					 SET status = 'failed', error_message = 'Reconciliation timed out: no gateway transaction found', updated_at = NOW() 
					 WHERE transaction_id = $1 AND status IN ('gateway_pending', 'processing')`,
					p.InternalTxnID,
				)
			}
			continue
		}

		if statusRes.StatusCode == "00" && (statusRes.BasketID == "" || statusRes.BasketID == p.OrderID) {
			// Verify amount in paisa if returned by gateway
			if statusRes.TxnAmt != "" {
				if gatewayAmt, err := strconv.ParseFloat(statusRes.TxnAmt, 64); err == nil {
					expectedPaisa := int64(math.Round(p.Amount * 100))
					gatewayPaisa := int64(math.Round(gatewayAmt * 100))
					if expectedPaisa != gatewayPaisa {
						log.Printf("[SettlementWorker] Reconciliation amount mismatch for %s: expected %d paisa, got %d paisa", p.InternalTxnID, expectedPaisa, gatewayPaisa)
						_, _ = w.db.Exec(ctx,
							`UPDATE payment_transactions SET status = 'failed', error_message = 'Reconciliation amount mismatch', updated_at = NOW() WHERE transaction_id = $1`,
							p.InternalTxnID,
						)
						continue
					}
				}
			}

			resolvedGatewayTxnID := statusRes.TransactionID
			if resolvedGatewayTxnID == "" {
				resolvedGatewayTxnID = p.GatewayTxnID
			}

			log.Printf("[SettlementWorker] Reconciliation verified SUCCESS for %s (%s). Enqueuing atomic settlement...", p.InternalTxnID, resolvedGatewayTxnID)
			w.enqueueSettlementOutbox(ctx, p.InternalTxnID, p.OrderID, resolvedGatewayTxnID, p.Amount)
		} else if statusRes.StatusCode != "" && statusRes.StatusCode != "00" {
			log.Printf("[SettlementWorker] Reconciliation verified REJECTION for %s: %s (code: %s)", p.InternalTxnID, statusRes.StatusMsg, statusRes.StatusCode)
			_, _ = w.db.Exec(ctx,
				`UPDATE payment_transactions SET status = 'failed', error_message = $1, updated_at = NOW() WHERE transaction_id = $2`,
				statusRes.StatusMsg, p.InternalTxnID,
			)
		}
	}
}

// cleanupStalePending marks abandoned 'pending' rows (>10m old) as failed so they don't linger in DB.
func (w *SettlementWorker) cleanupStalePending(ctx context.Context) {
	res, err := w.db.Exec(ctx,
		`UPDATE payment_transactions 
		 SET status = 'failed', error_message = 'Payment initiation abandoned/expired', updated_at = NOW()
		 WHERE status = 'pending' AND created_at < NOW() - INTERVAL '10 minutes'`,
	)
	if err == nil && res.RowsAffected() > 0 {
		log.Printf("[SettlementWorker] Cleaned up %d stale pending payment attempts", res.RowsAffected())
	}
}

func (w *SettlementWorker) enqueueSettlementOutbox(ctx context.Context, internalTxnID, orderID, gatewayTxnID string, amount float64) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		log.Printf("[SettlementWorker] Failed to begin transaction for reconciliation of order %s: %v", orderID, err)
		return
	}
	defer tx.Rollback(ctx)

	var storeID string
	var currentOrderStatus string
	err = tx.QueryRow(ctx, `SELECT store_tracking_id, status FROM orders WHERE order_tracking_id = $1 FOR UPDATE`, orderID).Scan(&storeID, &currentOrderStatus)
	if err != nil {
		log.Printf("[SettlementWorker] Order not found for reconciliation %s: %v", orderID, err)
		return
	}
	if currentOrderStatus == "paid" {
		return // already paid
	}

	var currentPaymentStatus string
	err = tx.QueryRow(ctx, `SELECT status FROM payment_transactions WHERE transaction_id = $1 FOR UPDATE`, internalTxnID).Scan(&currentPaymentStatus)
	if err != nil {
		log.Printf("[SettlementWorker] Payment txn not found for reconciliation %s: %v", internalTxnID, err)
		return
	}
	if currentPaymentStatus == "captured" || currentPaymentStatus == "settlement_pending" {
		return // already settled or in progress
	}

	var deliveryTrackingID string
	_ = tx.QueryRow(ctx, `SELECT COALESCE(tracking_id, '') FROM deliveries WHERE order_tracking_id = $1`, orderID).Scan(&deliveryTrackingID)

	split, err := w.calculator.CalculateSplit(ctx, amount, storeID, deliveryTrackingID)
	if err != nil {
		log.Printf("[SettlementWorker] Error calculating split for order %s: %v", orderID, err)
		return
	}

	idempotencyKey := fmt.Sprintf("payfast:split:%s", gatewayTxnID)
	currency := os.Getenv("DEFAULT_CURRENCY")
	if currency == "" {
		currency = "PKR"
	}

	transfers := []SettlementTransferItem{
		{
			DebitAccount:  string(ledger.AccountPayFastHolding),
			CreditAccount: string(ledger.AccountAdminRevenue),
			Amount:        split.AdminRevenue,
			Idempotency:   idempotencyKey + ":admin",
		},
		{
			DebitAccount:  string(ledger.AccountPayFastHolding),
			CreditAccount: string(ledger.AccountVendorLockedEscrow),
			Amount:        split.VendorEscrow,
			Idempotency:   idempotencyKey + ":vendor",
		},
	}
	if split.DeliveryEscrow > 0 {
		transfers = append(transfers, SettlementTransferItem{
			DebitAccount:  string(ledger.AccountPayFastHolding),
			CreditAccount: string(ledger.AccountCentralEscrow),
			Amount:        split.DeliveryEscrow,
			Idempotency:   idempotencyKey + ":delivery",
		})
	}

	outboxPayload, err := json.Marshal(SettlementPayload{
		InternalTxnID:      internalTxnID,
		OrderID:            orderID,
		GatewayTxnID:       gatewayTxnID,
		StoreID:            storeID,
		DeliveryTrackingID: deliveryTrackingID,
		TotalAmount:        amount,
		Currency:           currency,
		AdminRevenue:       split.AdminRevenue,
		VendorEscrow:       split.VendorEscrow,
		DeliveryEscrow:     split.DeliveryEscrow,
		IdempotencyKey:     idempotencyKey,
		Transfers:          transfers,
	})
	if err != nil {
		log.Printf("[SettlementWorker] Failed to marshal outbox payload for reconciliation %s: %v", orderID, err)
		return
	}

	_, err = tx.Exec(ctx,
		`UPDATE orders 
		 SET admin_commission = $1, vendor_escrow = $2, delivery_escrow = $3, payment_status = 'settlement_pending', updated_at = NOW() 
		 WHERE order_tracking_id = $4`,
		split.AdminRevenue, split.VendorEscrow, split.DeliveryEscrow, orderID,
	)
	if err != nil {
		log.Printf("[SettlementWorker] Failed to update order during reconciliation %s: %v", orderID, err)
		return
	}

	_, err = tx.Exec(ctx,
		`UPDATE payment_transactions SET status = 'settlement_pending', gateway_txn_id = $1, updated_at = NOW() WHERE transaction_id = $2`,
		gatewayTxnID, internalTxnID,
	)
	if err != nil {
		log.Printf("[SettlementWorker] Failed to update payment status during reconciliation %s: %v", internalTxnID, err)
		return
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO outbox_events (aggregate_id, topic, payload, status, created_at, updated_at) 
		 VALUES ($1, 'payment_settlement', $2, 'PENDING', NOW(), NOW())`,
		orderID, string(outboxPayload),
	)
	if err != nil {
		log.Printf("[SettlementWorker] Failed to insert outbox event during reconciliation %s: %v", orderID, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("[SettlementWorker] Failed to commit atomic reconciliation transaction for order %s: %v", orderID, err)
		return
	}

	log.Printf("[SettlementWorker] Successfully enqueued atomic settlement outbox for order %s (Txn %s)", orderID, internalTxnID)
}
