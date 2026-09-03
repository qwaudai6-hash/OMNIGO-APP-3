package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	tb "github.com/tigerbeetle/tigerbeetle-go"
)

// TBOutboxEntry represents a pending TigerBeetle transfer to be relayed.
type TBOutboxEntry struct {
	ID            int64           `json:"id"`
	TransactionID uuid.UUID       `json:"transaction_id"`
	Payload       json.RawMessage `json:"payload"`
	Status        string          `json:"status"`
	RetryCount    int             `json:"retry_count"`
	MaxRetries    int             `json:"max_retries"`
	ErrorMessage  *string         `json:"error_message"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	CompletedAt   *time.Time      `json:"completed_at"`
}

// InsertTBOutboxEntry writes an outbox row inside an existing Postgres tx.
// This must be called BEFORE tx.Commit() so it commits atomically with ledger_entries.
func InsertTBOutboxEntry(ctx context.Context, tx pgx.Tx, transactionID uuid.UUID, transfers []tb.Transfer) error {
	payload, err := json.Marshal(transfers)
	if err != nil {
		return fmt.Errorf("failed to marshal TB transfers: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO tigerbeetle_outbox (transaction_id, payload, status)
		VALUES ($1, $2, 'PENDING')
		ON CONFLICT (transaction_id) WHERE status IN ('PENDING', 'PROCESSING')
		DO NOTHING
	`, transactionID, payload)
	return err
}

// FetchPendingTBOutbox returns a batch of PENDING outbox entries, locked for update.
func FetchPendingTBOutbox(ctx context.Context, db *pgxpool.Pool, limit int) ([]TBOutboxEntry, error) {
	rows, err := db.Query(ctx, `
		SELECT id, transaction_id, payload, status, retry_count, max_retries,
		       error_message, created_at, updated_at, completed_at
		FROM tigerbeetle_outbox
		WHERE status = 'PENDING'
		ORDER BY created_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []TBOutboxEntry
	for rows.Next() {
		var e TBOutboxEntry
		if err := rows.Scan(&e.ID, &e.TransactionID, &e.Payload, &e.Status,
			&e.RetryCount, &e.MaxRetries, &e.ErrorMessage,
			&e.CreatedAt, &e.UpdatedAt, &e.CompletedAt); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// MarkTBOutboxCompleted marks an entry as successfully relayed to TigerBeetle.
func MarkTBOutboxCompleted(ctx context.Context, db *pgxpool.Pool, id int64) error {
	_, err := db.Exec(ctx, `
		UPDATE tigerbeetle_outbox
		SET status = 'COMPLETED', completed_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, id)
	return err
}

// MarkTBOutboxFailed increments retry_count and records error.
// If max_retries is reached, status transitions to FAILED (no more retries).
func MarkTBOutboxFailed(ctx context.Context, db *pgxpool.Pool, id int64, errMsg string, maxRetries int) error {
	_, err := db.Exec(ctx, `
		UPDATE tigerbeetle_outbox
		SET retry_count = retry_count + 1,
		    error_message = $2,
		    updated_at = NOW(),
		    status = CASE WHEN retry_count + 1 >= $3 THEN 'FAILED' ELSE 'PENDING' END
		WHERE id = $1
	`, id, errMsg, maxRetries)
	return err
}
