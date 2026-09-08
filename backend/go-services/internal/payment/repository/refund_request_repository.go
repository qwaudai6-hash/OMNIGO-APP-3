package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type RefundRequestStatus string

const (
	RefundStatusPending    RefundRequestStatus = "pending"
	RefundStatusApproved   RefundRequestStatus = "approved"
	RefundStatusRejected   RefundRequestStatus = "rejected"
	RefundStatusProcessed  RefundRequestStatus = "processed"
	RefundStatusCancelled  RefundRequestStatus = "cancelled"
)

type RefundRequest struct {
	ID             string               `json:"id"`
	OrderID       string               `json:"order_tracking_id"`
	CustomerID    string               `json:"customer_tracking_id"`
	Gateway        string               `json:"gateway"`
	GatewayTxnID  string               `json:"gateway_txn_id"`
	Amount         float64              `json:"amount"`
	Currency      string               `json:"currency"`
	Status         RefundRequestStatus `json:"status"`
	Reason        string               `json:"reason"`
	RequestedBy   string               `json:"requested_by"`
	ProcessedBy   string               `json:"processed_by,omitempty"`
	ProcessedAt   *time.Time           `json:"processed_at,omitempty"`
	Notes         string               `json:"notes,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

type RefundRequestRepository struct {
	db *pgxpool.Pool
}

func NewRefundRequestRepository(db *pgxpool.Pool) *RefundRequestRepository {
	return &RefundRequestRepository{db: db}
}

func (r *RefundRequestRepository) Create(ctx context.Context, req *RefundRequest) error {
	query := `
		INSERT INTO refund_requests (
			id, order_tracking_id, customer_tracking_id, gateway, gateway_txn_id,
			amount, currency, status, reason, requested_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
	`
	_, err := r.db.Exec(ctx, query,
		req.ID, req.OrderID, req.CustomerID, req.Gateway, req.GatewayTxnID,
		req.Amount, req.Currency, req.Status, req.Reason, req.RequestedBy,
	)
	return err
}

func (r *RefundRequestRepository) GetByID(ctx context.Context, id string) (*RefundRequest, error) {
	query := `
		SELECT id, order_tracking_id, customer_tracking_id, gateway, gateway_txn_id,
			amount, currency, status, reason, requested_by, processed_by,
			processed_at, notes, created_at, updated_at
		FROM refund_requests WHERE id = $1
	`
	var req RefundRequest
	err := r.db.QueryRow(ctx, query, id).Scan(
		&req.ID, &req.OrderID, &req.CustomerID, &req.Gateway, &req.GatewayTxnID,
		&req.Amount, &req.Currency, &req.Status, &req.Reason, &req.RequestedBy,
		&req.ProcessedBy, &req.ProcessedAt, &req.Notes, &req.CreatedAt, &req.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &req, nil
}

func (r *RefundRequestRepository) GetByOrderID(ctx context.Context, orderID string) ([]*RefundRequest, error) {
	query := `
		SELECT id, order_tracking_id, customer_tracking_id, gateway, gateway_txn_id,
			amount, currency, status, reason, requested_by, processed_by,
			processed_at, notes, created_at, updated_at
		FROM refund_requests WHERE order_tracking_id = $1
		ORDER BY created_at DESC
	`
	rows, err := r.db.Query(ctx, query, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []*RefundRequest
	for rows.Next() {
		var req RefundRequest
		if err := rows.Scan(
			&req.ID, &req.OrderID, &req.CustomerID, &req.Gateway, &req.GatewayTxnID,
			&req.Amount, &req.Currency, &req.Status, &req.Reason, &req.RequestedBy,
			&req.ProcessedBy, &req.ProcessedAt, &req.Notes, &req.CreatedAt, &req.UpdatedAt,
		); err != nil {
			return nil, err
		}
		requests = append(requests, &req)
	}
	return requests, nil
}

func (r *RefundRequestRepository) ListPending(ctx context.Context) ([]*RefundRequest, error) {
	query := `
		SELECT id, order_tracking_id, customer_tracking_id, gateway, gateway_txn_id,
			amount, currency, status, reason, requested_by, processed_by,
			processed_at, notes, created_at, updated_at
		FROM refund_requests WHERE status = $1
		ORDER BY created_at ASC
	`
	rows, err := r.db.Query(ctx, query, RefundStatusPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []*RefundRequest
	for rows.Next() {
		var req RefundRequest
		if err := rows.Scan(
			&req.ID, &req.OrderID, &req.CustomerID, &req.Gateway, &req.GatewayTxnID,
			&req.Amount, &req.Currency, &req.Status, &req.Reason, &req.RequestedBy,
			&req.ProcessedBy, &req.ProcessedAt, &req.Notes, &req.CreatedAt, &req.UpdatedAt,
		); err != nil {
			return nil, err
		}
		requests = append(requests, &req)
	}
	return requests, nil
}

func (r *RefundRequestRepository) UpdateStatus(ctx context.Context, id string, status RefundRequestStatus, processedBy string, notes string) error {
	query := `
		UPDATE refund_requests
		SET status = $2, processed_by = $3, processed_at = NOW(), notes = $4, updated_at = NOW()
		WHERE id = $1
	`
	_, err := r.db.Exec(ctx, query, id, status, processedBy, notes)
	return err
}
