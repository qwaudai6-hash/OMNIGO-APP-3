package ledger

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	tb "github.com/tigerbeetle/tigerbeetle-go"
)

// Service is the double-entry ledger engine.
// Every money movement goes through Transfer() which atomically writes
// a DEBIT row and a CREDIT row sharing the same transaction_id.
type Service struct {
	repo      *Repository
	db        *pgxpool.Pool
	tbService *TBService // Optional TigerBeetle service for dual-writes
}

// TBService returns the underlying TigerBeetle service.
func (s *Service) TBService() *TBService {
	return s.tbService
}

// NewService builds the ledger service. It panics if HMAC_SECRET is unset.
func NewService(db *pgxpool.Pool, tbService *TBService) *Service {
	// Eagerly load the secret so misconfiguration fails fast at startup.
	// ledgerHMACSecret() also validates via sync.Once, but calling it here
	// makes the failure happen during service construction (clearer stack).
	_ = ledgerHMACSecret()
	return &Service{
		repo:      NewRepository(db),
		db:        db,
		tbService: tbService,
	}
}

func generateSignature(entry LedgerEntry) string {
	payload := fmt.Sprintf("%s:%s:%f:%s:%s", entry.TransactionID, entry.Account, entry.Amount, entry.ReferenceID, entry.IdempotencyKey)
	// We can't take a service method here because signature is a free
	// function used in entry construction. The secret was already loaded
	// at NewService() so we can read it via package-level var below.
	mac := hmac.New(sha256.New, ledgerHMACSecret())
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

var (
	_hmacSecret     []byte
	_hmacSecretOnce sync.Once
)

func ledgerHMACSecret() []byte {
	_hmacSecretOnce.Do(func() {
		sec := os.Getenv("HMAC_SECRET")
		if sec == "" {
			if os.Getenv("GO_TEST_ENV") == "1" {
				sec = "test-hmac-secret-do-not-use-in-production"
			} else {
				panic("HMAC_SECRET environment variable is required but not set")
			}
		}
		_hmacSecret = []byte(sec)
	})
	return _hmacSecret
}

// Transfer executes a double-entry transfer: debits one account and credits another.
// Both entries are written in the same Postgres transaction and share a transaction_id.
// Idempotency is enforced via the idempotency_key UNIQUE constraint.
func (s *Service) Transfer(ctx context.Context, req TransferRequest) (uuid.UUID, error) {
	// Validate accounts
	if !ValidAccounts[req.DebitAccount] {
		return uuid.Nil, fmt.Errorf("invalid debit account: %s", req.DebitAccount)
	}
	if !ValidAccounts[req.CreditAccount] {
		return uuid.Nil, fmt.Errorf("invalid credit account: %s", req.CreditAccount)
	}
	if req.Amount <= 0 {
		return uuid.Nil, fmt.Errorf("transfer amount must be positive, got %f", req.Amount)
	}
	if req.DebitAccount == req.CreditAccount {
		return uuid.Nil, fmt.Errorf("debit and credit accounts must be different")
	}
	if req.Currency == "" {
		req.Currency = "PKR"
	}

	txID := uuid.New()

	// Begin a Postgres transaction
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to begin ledger transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Check idempotency — if this key already exists, return the existing transaction
	if req.IdempotencyKey != "" {
		var existingTxID uuid.UUID
		err := tx.QueryRow(ctx,
			`SELECT transaction_id FROM ledger_entries WHERE idempotency_key = $1 LIMIT 1`,
			req.IdempotencyKey,
		).Scan(&existingTxID)
		if err == nil {
			// Idempotent hit — already processed
			return existingTxID, nil
		}
		// Not found — proceed with new transfer
	}

	debitEntry := LedgerEntry{
		ID:               uuid.New(),
		TransactionID:    txID,
		Account:          req.DebitAccount,
		Amount:           -req.Amount, // Negative = debit
		Currency:         req.Currency,
		ReferenceType:    req.ReferenceType,
		ReferenceID:      req.ReferenceID,
		Description:      req.Description,
		IdempotencyKey:   req.IdempotencyKey + ":debit",
		SignatureVersion: 1,
	}
	debitEntry.Signature = generateSignature(debitEntry)

	creditEntry := LedgerEntry{
		ID:               uuid.New(),
		TransactionID:    txID,
		Account:          req.CreditAccount,
		Amount:           req.Amount, // Positive = credit
		Currency:         req.Currency,
		ReferenceType:    req.ReferenceType,
		ReferenceID:      req.ReferenceID,
		Description:      req.Description,
		IdempotencyKey:   req.IdempotencyKey + ":credit",
		SignatureVersion: 1,
	}
	creditEntry.Signature = generateSignature(creditEntry)

	// Insert both entries atomically
	err = s.repo.InsertEntries(ctx, tx, []LedgerEntry{debitEntry, creditEntry})
	if err != nil {
		return uuid.Nil, fmt.Errorf("ledger insert failed: %w", err)
	}

	// Commit
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("ledger commit failed: %w", err)
	}

	// TigerBeetle Dual-Write (Fire and forget or sync, depending on strictness)
	// For OMNIGO billion-dollar scale, we eventually migrate fully to TB.
	// Here we mirror the transfer for analytics/ledger sync.
	if s.tbService != nil {
		go func(tId uuid.UUID, req TransferRequest) {
			tbID, err := UUIDToUint128(tId.String())
			if err != nil {
				return
			}
			transfer := tb.Transfer{
				ID:              tbID,
				DebitAccountID:  AccountToUint128(req.DebitAccount),
				CreditAccountID: AccountToUint128(req.CreditAccount),
				Amount:          tb.ToUint128(uint64(req.Amount * 100)), // Store as cents
				Ledger:          1,
				Code:            1,
			}
			err = s.tbService.CreateTransfers([]tb.Transfer{transfer})
			if err != nil {
				// Log the error. Since dual-write is eventual/analytics consistency, we don't fail the primary tx.
				fmt.Printf("[Ledger] TigerBeetle dual-write failed for tx %s: %v\n", tId, err)
			}
		}(txID, req)
	}

	return txID, nil
}

// MultiTransfer executes multiple transfers atomically.
// All transfers share the same Postgres transaction — if any fails, all are rolled back.
// Idempotency is enforced: if any idempotency key already exists, the existing
// transaction_id is returned without creating duplicate entries.
func (s *Service) MultiTransfer(ctx context.Context, reqs []TransferRequest) (uuid.UUID, error) {
	if len(reqs) == 0 {
		return uuid.Nil, fmt.Errorf("no transfers provided")
	}

	txID := uuid.New()

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to begin multi-transfer transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// IDEMPOTENCY CHECK: If any idempotency key already exists, return the
	// existing transaction. This prevents duplicate entries on webhook retries.
	for _, req := range reqs {
		if req.IdempotencyKey != "" {
			var existingTxID uuid.UUID
			err := tx.QueryRow(ctx,
				`SELECT transaction_id FROM ledger_entries WHERE idempotency_key = $1 LIMIT 1`,
				req.IdempotencyKey+":debit",
			).Scan(&existingTxID)
			if err == nil {
				// Already processed — return existing transaction
				tx.Rollback(ctx)
				return existingTxID, nil
			}
		}
	}

	var entries []LedgerEntry
	for _, req := range reqs {
		if !ValidAccounts[req.DebitAccount] {
			return uuid.Nil, fmt.Errorf("invalid debit account: %s", req.DebitAccount)
		}
		if !ValidAccounts[req.CreditAccount] {
			return uuid.Nil, fmt.Errorf("invalid credit account: %s", req.CreditAccount)
		}
		if req.Amount <= 0 {
			return uuid.Nil, fmt.Errorf("transfer amount must be positive, got %f", req.Amount)
		}
		if req.Currency == "" {
			req.Currency = "PKR"
		}

		debit := LedgerEntry{
			ID:               uuid.New(),
			TransactionID:    txID,
			Account:          req.DebitAccount,
			Amount:           -req.Amount,
			Currency:         req.Currency,
			ReferenceType:    req.ReferenceType,
			ReferenceID:      req.ReferenceID,
			Description:      req.Description,
			IdempotencyKey:   req.IdempotencyKey + ":debit",
			SignatureVersion: 1,
		}
		debit.Signature = generateSignature(debit)
		entries = append(entries, debit)

		credit := LedgerEntry{
			ID:               uuid.New(),
			TransactionID:    txID,
			Account:          req.CreditAccount,
			Amount:           req.Amount,
			Currency:         req.Currency,
			ReferenceType:    req.ReferenceType,
			ReferenceID:      req.ReferenceID,
			Description:      req.Description,
			IdempotencyKey:   req.IdempotencyKey + ":credit",
			SignatureVersion: 1,
		}
		credit.Signature = generateSignature(credit)
		entries = append(entries, credit)
	}

	err = s.repo.InsertEntries(ctx, tx, entries)
	if err != nil {
		return uuid.Nil, fmt.Errorf("multi-transfer insert failed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("multi-transfer commit failed: %w", err)
	}

	if s.tbService != nil {
		go func(tId uuid.UUID, requests []TransferRequest) {
			var tbTransfers []tb.Transfer
			for i, r := range requests {
				// Deterministically map each request ID to a unique tb ID. We use txID + index so it's stable.
				// For simplicity, generate a new ID based on tId and index.
				tbIDStr := fmt.Sprintf("%s-%d", tId.String(), i)
				u := uuid.NewMD5(uuid.NameSpaceOID, []byte(tbIDStr))
				tbTransfers = append(tbTransfers, tb.Transfer{
					ID:              tb.BytesToUint128(u),
					DebitAccountID:  AccountToUint128(r.DebitAccount),
					CreditAccountID: AccountToUint128(r.CreditAccount),
					Amount:          tb.ToUint128(uint64(r.Amount * 100)),
					Ledger:          1,
					Code:            1,
				})
			}
			err = s.tbService.CreateTransfers(tbTransfers)
			if err != nil {
				fmt.Printf("[Ledger] TigerBeetle dual-write failed for multi-tx %s: %v\n", tId, err)
			}
		}(txID, reqs)
	}

	return txID, nil
}

// GetBalance returns the net balance for an account.
func (s *Service) GetBalance(ctx context.Context, account Account) (float64, error) {
	return s.repo.GetBalance(ctx, account)
}

// GetEntriesByReference returns all ledger entries for a given order or delivery.
func (s *Service) GetEntriesByReference(ctx context.Context, refType, refID string) ([]LedgerEntry, error) {
	return s.repo.GetEntriesByReference(ctx, refType, refID)
}
