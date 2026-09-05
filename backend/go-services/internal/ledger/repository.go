package ledger

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository handles ledger persistence in PostgreSQL.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// InsertEntries writes multiple ledger entries inside an existing transaction.
// The caller MUST pass an active pgx.Tx — this method does NOT begin its own transaction.
func (r *Repository) InsertEntries(ctx context.Context, tx pgx.Tx, entries []LedgerEntry) error {
	for _, e := range entries {
		query := `
			INSERT INTO ledger_entries (id, transaction_id, account, amount_paisa, currency, reference_type, reference_id, description, idempotency_key, signature, signature_version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		`
		_, err := tx.Exec(ctx, query,
			e.ID, e.TransactionID, e.Account, e.Amount, e.Currency,
			e.ReferenceType, e.ReferenceID, e.Description, e.IdempotencyKey, e.Signature, e.SignatureVersion,
		)
		if err != nil {
			return fmt.Errorf("failed to insert ledger entry for account %s: %w", e.Account, err)
		}
	}
	return nil
}

// GetBalance returns the net balance (sum of all amounts) for an account.
func (r *Repository) GetBalance(ctx context.Context, account Account) (int64, error) {
	var balance int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_paisa), 0) FROM ledger_entries WHERE account = $1`, account,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("failed to get balance for %s: %w", account, err)
	}
	return balance, nil
}

// GetBalanceInTx returns the net balance for an account within an active transaction.
func (r *Repository) GetBalanceInTx(ctx context.Context, tx pgx.Tx, account Account) (int64, error) {
	var balance int64
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_paisa), 0) FROM ledger_entries WHERE account = $1`, account,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("failed to get balance for %s: %w", account, err)
	}
	return balance, nil
}

// GetEntriesByReference returns all ledger entries for a given reference.
func (r *Repository) GetEntriesByReference(ctx context.Context, refType, refID string) ([]LedgerEntry, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, transaction_id, account, amount_paisa, currency, reference_type, reference_id, description, idempotency_key, signature, signature_version, created_at
		 FROM ledger_entries WHERE reference_type = $1 AND reference_id = $2 ORDER BY created_at`,
		refType, refID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.TransactionID, &e.Account, &e.Amount, &e.Currency,
			&e.ReferenceType, &e.ReferenceID, &e.Description, &e.IdempotencyKey, &e.Signature, &e.SignatureVersion, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// GetTransactionEntries returns all entries sharing the same transaction_id.
func (r *Repository) GetTransactionEntries(ctx context.Context, txID uuid.UUID) ([]LedgerEntry, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, transaction_id, account, amount_paisa, currency, reference_type, reference_id, description, idempotency_key, signature, signature_version, created_at
		 FROM ledger_entries WHERE transaction_id = $1 ORDER BY created_at`,
		txID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.TransactionID, &e.Account, &e.Amount, &e.Currency,
			&e.ReferenceType, &e.ReferenceID, &e.Description, &e.IdempotencyKey, &e.Signature, &e.SignatureVersion, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
