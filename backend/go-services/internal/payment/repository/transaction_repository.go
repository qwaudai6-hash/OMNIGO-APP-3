package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TransactionStatus is the canonical state of a payment transaction.
type TransactionStatus string

const (
	TxnPending           TransactionStatus = "pending"
	TxnProcessing        TransactionStatus = "processing"
	TxnAuthorized        TransactionStatus = "authorized"
	TxnSettlementPending TransactionStatus = "settlement_pending"
	TxnCaptured          TransactionStatus = "captured"
	TxnFailed            TransactionStatus = "failed"
	TxnRefunded          TransactionStatus = "refunded"
	TxnReversed          TransactionStatus = "reversed"
	TxnChargeback        TransactionStatus = "chargeback"
)

// TransactionKind is the kind of money movement.
type TransactionKind string

const (
	KindPayment  TransactionKind = "payment"
	KindRefund   TransactionKind = "refund"
	KindReversal TransactionKind = "reversal"
	KindWallet   TransactionKind = "wallet_load"
	KindPayout   TransactionKind = "payout"
)

// PaymentTransaction is the persistence model for a payment transaction.
type PaymentTransaction struct {
	ID             int64             `json:"id"`
	TransactionID  string            `json:"transaction_id"`
	OrderID        string            `json:"order_tracking_id"`
	Gateway        string            `json:"gateway"`
	GatewayTxnID   string            `json:"gateway_txn_id,omitempty"`
	Amount         float64           `json:"amount"`
	Currency       string            `json:"currency"`
	Status         TransactionStatus `json:"status"`
	Kind           TransactionKind   `json:"kind"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
	ErrorMessage   string            `json:"error_message,omitempty"`
	CreatedAt           int64             `json:"created_at_ms"`
	UpdatedAt           int64             `json:"updated_at_ms"`
	CallbackProcessedAt *time.Time        `json:"callback_processed_at,omitempty"`
}

// Repository handles payment transaction persistence.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// Create inserts a new payment transaction and returns it. Idempotency key is
// optional; if set and already exists, the existing record is returned.
func (r *Repository) Create(ctx context.Context, txn *PaymentTransaction) (*PaymentTransaction, error) {
	if txn.TransactionID == "" {
		// UTID-aligned internal identifier (FIX-PAY-02: previously a bare UUID,
		// which the admin lineage resolver could never recognize as a
		// transaction). Legacy rows use bare UUIDs or "pf_<uuid>" (PayFast);
		// the admin resolver handles all three formats.
		txn.TransactionID = "TXN-" + uuid.New().String()
	}
	if txn.Currency == "" {
		txn.Currency = "PKR"
	}
	if txn.Status == "" {
		txn.Status = TxnPending
	}
	if txn.Kind == "" {
		txn.Kind = KindPayment
	}

	var metadataJSON []byte
	if txn.Metadata != nil {
		var err error
		metadataJSON, err = json.Marshal(txn.Metadata)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal metadata: %w", err)
		}
	}

	query := `
		INSERT INTO payment_transactions (transaction_id, order_tracking_id, gateway, gateway_txn_id, amount, currency, status, kind, idempotency_key, metadata, error_message, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW(), NOW())
		ON CONFLICT (idempotency_key) DO UPDATE SET
			updated_at = EXCLUDED.updated_at
		RETURNING id, transaction_id, order_tracking_id, gateway, gateway_txn_id, amount, currency, status, kind, idempotency_key, metadata, error_message, EXTRACT(EPOCH FROM created_at)*1000, EXTRACT(EPOCH FROM updated_at)*1000, callback_processed_at
	`

	row := r.db.QueryRow(ctx, query,
		txn.TransactionID, txn.OrderID, txn.Gateway, txn.GatewayTxnID, txn.Amount, txn.Currency,
		txn.Status, txn.Kind, txn.IdempotencyKey, metadataJSON, txn.ErrorMessage,
	)
	return scanTransaction(row)
}

// UpdateStatus updates the status of a transaction and its metadata fields.
func (r *Repository) UpdateStatus(ctx context.Context, transactionID string, status TransactionStatus, gatewayTxnID string, metadata map[string]any, errorMsg string) error {
	var metadataJSON []byte
	if metadata != nil {
		var err error
		metadataJSON, err = json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal metadata: %w", err)
		}
	}

	query := `
		UPDATE payment_transactions
		SET status = $1, gateway_txn_id = COALESCE(NULLIF($2, ''), gateway_txn_id), metadata = COALESCE($3, metadata), error_message = COALESCE(NULLIF($4, ''), error_message), updated_at = NOW()
		WHERE transaction_id = $5
	`
	_, err := r.db.Exec(ctx, query, status, gatewayTxnID, metadataJSON, errorMsg, transactionID)
	return err
}

// GetByIDempotencyKey returns a transaction by its idempotency key.
func (r *Repository) GetByIDempotencyKey(ctx context.Context, key string) (*PaymentTransaction, error) {
	query := `SELECT id, transaction_id, order_tracking_id, gateway, gateway_txn_id, amount, currency, status, kind, idempotency_key, metadata, error_message, EXTRACT(EPOCH FROM created_at)*1000, EXTRACT(EPOCH FROM updated_at)*1000, callback_processed_at FROM payment_transactions WHERE idempotency_key = $1`
	return scanTransaction(r.db.QueryRow(ctx, query, key))
}

// GetByOrderID returns the most recent payment transaction for an order.
func (r *Repository) GetByOrderID(ctx context.Context, orderID string, kind TransactionKind) (*PaymentTransaction, error) {
	query := `SELECT id, transaction_id, order_tracking_id, gateway, gateway_txn_id, amount, currency, status, kind, idempotency_key, metadata, error_message, EXTRACT(EPOCH FROM created_at)*1000, EXTRACT(EPOCH FROM updated_at)*1000, callback_processed_at FROM payment_transactions WHERE order_tracking_id = $1 AND kind = $2 ORDER BY created_at DESC LIMIT 1`
	return scanTransaction(r.db.QueryRow(ctx, query, orderID, kind))
}

// GetByTransactionID returns a transaction by its internal transaction id.
func (r *Repository) GetByTransactionID(ctx context.Context, txnID string) (*PaymentTransaction, error) {
	query := `SELECT id, transaction_id, order_tracking_id, gateway, gateway_txn_id, amount, currency, status, kind, idempotency_key, metadata, error_message, EXTRACT(EPOCH FROM created_at)*1000, EXTRACT(EPOCH FROM updated_at)*1000, callback_processed_at FROM payment_transactions WHERE transaction_id = $1`
	return scanTransaction(r.db.QueryRow(ctx, query, txnID))
}

// GetByGatewayTxnID returns the most recent payment transaction referenced by
// a gateway-side transaction/refund identifier (e.g. JazzCash pp_TxnRefNo).
func (r *Repository) GetByGatewayTxnID(ctx context.Context, gateway, gatewayTxnID string) (*PaymentTransaction, error) {
	query := `SELECT id, transaction_id, order_tracking_id, gateway, gateway_txn_id, amount, currency, status, kind, idempotency_key, metadata, error_message, EXTRACT(EPOCH FROM created_at)*1000, EXTRACT(EPOCH FROM updated_at)*1000, callback_processed_at FROM payment_transactions WHERE gateway = $1 AND gateway_txn_id = $2 ORDER BY created_at DESC LIMIT 1`
	return scanTransaction(r.db.QueryRow(ctx, query, gateway, gatewayTxnID))
}

// HashRequest returns a stable SHA-256 hash of a request payload.
func HashRequest(payload []byte) string {
	h := sha256.Sum256(payload)
	return hex.EncodeToString(h[:])
}

// LockIDempotencyKey registers an in-flight idempotency key, or returns an
// existing transaction_id if the key is already known. This allows callers to
// detect duplicate requests before talking to the gateway.
func (r *Repository) LockIDempotencyKey(ctx context.Context, key, requestHash string) (existingTxnID string, alreadyExists bool, err error) {
	query := `
		INSERT INTO payment_idempotency (key, request_hash, expires_at)
		VALUES ($1, $2, NOW() + INTERVAL '24 hours')
		ON CONFLICT (key) DO UPDATE SET
			request_hash = EXCLUDED.request_hash
		RETURNING transaction_id
	`
	var txnID *string
	err = r.db.QueryRow(ctx, query, key, requestHash).Scan(&txnID)
	if err != nil {
		return "", false, err
	}
	if txnID != nil {
		return *txnID, true, nil
	}
	return "", false, nil
}

// AssignIDempotencyKey links a transaction id to an in-flight idempotency key.
func (r *Repository) AssignIDempotencyKey(ctx context.Context, key, transactionID string) error {
	_, err := r.db.Exec(ctx, `UPDATE payment_idempotency SET transaction_id = $1 WHERE key = $2`, transactionID, key)
	return err
}

func scanTransaction(row interface {
	Scan(dest ...any) error
}) (*PaymentTransaction, error) {
	var txn PaymentTransaction
	var metadata []byte
	var createdAt float64
	var updatedAt float64
	err := row.Scan(
		&txn.ID, &txn.TransactionID, &txn.OrderID, &txn.Gateway, &txn.GatewayTxnID,
		&txn.Amount, &txn.Currency, &txn.Status, &txn.Kind, &txn.IdempotencyKey,
		&metadata, &txn.ErrorMessage, &createdAt, &updatedAt, &txn.CallbackProcessedAt,
	)
	if err != nil {
		return nil, err
	}
	if metadata != nil {
		_ = json.Unmarshal(metadata, &txn.Metadata)
	}
	txn.CreatedAt = int64(createdAt)
	txn.UpdatedAt = int64(updatedAt)
	return &txn, nil
}
