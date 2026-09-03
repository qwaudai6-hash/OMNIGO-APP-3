package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/shared/messaging"
)

// CustomerWalletResponse is the wallet overview returned to the customer.
type CustomerWalletResponse struct {
	CustomerTrackingID string    `json:"customer_tracking_id"`
	Balance            float64   `json:"balance"`
	LifetimeSpent      float64   `json:"lifetime_spent"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type CustomerWalletService struct {
	db     *pgxpool.Pool
	ledger *ledger.Service
}

func NewCustomerWalletService(db *pgxpool.Pool, ledgerSvc *ledger.Service) *CustomerWalletService {
	svc := &CustomerWalletService{db: db, ledger: ledgerSvc}
	svc.initDB()
	return svc
}

func (s *CustomerWalletService) initDB() {
	query := `
	CREATE TABLE IF NOT EXISTS customer_wallet (
		customer_tracking_id VARCHAR(255) PRIMARY KEY,
		balance DECIMAL(15, 2) NOT NULL DEFAULT 0.00,
		lifetime_spent DECIMAL(15, 2) NOT NULL DEFAULT 0.00,
		updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
	);`
	_, err := s.db.Exec(context.Background(), query)
	if err != nil {
		fmt.Printf("[CustomerWallet] Failed to create table: %v\n", err)
	}
}

// GetWallet returns the customer wallet balance.
func (s *CustomerWalletService) GetWallet(ctx context.Context, customerTrackingID string) (*CustomerWalletResponse, error) {
	var resp CustomerWalletResponse
	resp.CustomerTrackingID = customerTrackingID

	query := `
		SELECT COALESCE(balance, 0), COALESCE(lifetime_spent, 0), updated_at
		FROM customer_wallet
		WHERE customer_tracking_id = $1
	`
	err := s.db.QueryRow(ctx, query, customerTrackingID).Scan(
		&resp.Balance, &resp.LifetimeSpent, &resp.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			// No wallet row yet is valid — return zeros.
			resp.Balance = 0
			resp.LifetimeSpent = 0
			resp.UpdatedAt = time.Now().UTC()
			return &resp, nil
		}
		return nil, fmt.Errorf("wallet query failed: %w", err)
	}

	return &resp, nil
}

// CreditFunds adds funds to a customer wallet (e.g. via Refund or Top-up).
//
// FIX C5: Ledger transfer now happens BEFORE wallet commit. If the ledger
// transfer fails, the wallet transaction is rolled back. If the ledger
// succeeds but the wallet commit fails, the ledger has the entry (safe:
// money is recorded but customer can't spend — admin can reconcile).
func (s *CustomerWalletService) CreditFunds(ctx context.Context, customerTrackingID, referenceID string, amount float64, sourceAccount ledger.Account, description string) error {
	if amount <= 0 {
		return fmt.Errorf("credit amount must be positive")
	}

	// FIX C5: Do the ledger transfer FIRST (before wallet commit).
	// This ensures the ledger is always updated if the wallet is updated.
	// If the ledger fails, we don't update the wallet at all.
	if s.ledger != nil {
		idempotencyKey := fmt.Sprintf("customer_credit:%s:%s", referenceID, customerTrackingID)
		if _, err := s.ledger.Transfer(ctx, ledger.TransferRequest{
			DebitAccount:   sourceAccount,
			CreditAccount:  ledger.AccountCustomerWallet,
			Amount:         amount,
			Currency:       "PKR",
			ReferenceType:  "customer_wallet_credit",
			ReferenceID:    referenceID,
			Description:    description,
			IdempotencyKey: idempotencyKey,
		}); err != nil {
			return fmt.Errorf("ledger transfer failed, wallet not credited: %w", err)
		}
	}

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("failed to begin wallet transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	upsertQuery := `
		INSERT INTO customer_wallet (customer_tracking_id, balance, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (customer_tracking_id)
		DO UPDATE SET
			balance = customer_wallet.balance + $2,
			updated_at = NOW()
	`
	_, err = tx.Exec(ctx, upsertQuery, customerTrackingID, amount)
	if err != nil {
		return fmt.Errorf("wallet credit failed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("wallet commit failed: %w", err)
	}

	messaging.EmitFinancialNotification(
		ctx, customerTrackingID, "customer", "wallet_credited",
		"Wallet Credited", fmt.Sprintf("Aapke Omnigo Wallet mein Rs. %.2f credit ho gaye hain.", amount),
		amount, referenceID,
	)

	return nil
}

// DeductForPurchase subtracts money from wallet for a purchase.
//
// FIX C5: Ledger transfer now happens BEFORE wallet commit. If the ledger
// transfer fails, the wallet deduction is rolled back. This prevents the
// scenario where the wallet is debited but the ledger never records it.
func (s *CustomerWalletService) DeductForPurchase(ctx context.Context, customerTrackingID, orderTrackingID string, amount float64) error {
	if amount <= 0 {
		return fmt.Errorf("deduct amount must be positive")
	}

	// FIX C5: Do the ledger transfer FIRST (before wallet deduction).
	// If the ledger fails, the wallet is not deducted.
	if s.ledger != nil {
		idempotencyKey := fmt.Sprintf("customer_purchase:%s:%s", orderTrackingID, customerTrackingID)
		if _, err := s.ledger.Transfer(ctx, ledger.TransferRequest{
			DebitAccount:   ledger.AccountCustomerWallet,
			CreditAccount:  ledger.AccountCentralEscrow,
			Amount:         amount,
			Currency:       "PKR",
			ReferenceType:  "wallet_purchase",
			ReferenceID:    orderTrackingID,
			Description:    fmt.Sprintf("Customer purchase for order %s", orderTrackingID),
			IdempotencyKey: idempotencyKey,
		}); err != nil {
			return fmt.Errorf("ledger transfer failed, wallet not deducted: %w", err)
		}
	}

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("failed to begin wallet transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	query := `
		UPDATE customer_wallet
		SET 
			balance = balance - $1,
			lifetime_spent = lifetime_spent + $1,
			updated_at = NOW()
		WHERE customer_tracking_id = $2 AND balance >= $1
	`
	tag, err := tx.Exec(ctx, query, amount, customerTrackingID)
	if err != nil {
		return fmt.Errorf("wallet deduct query failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Distinguish between "wallet not found" and "insufficient funds"
		var exists bool
		err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM customer_wallet WHERE customer_tracking_id = $1)", customerTrackingID).Scan(&exists)
		if err != nil || !exists {
			return fmt.Errorf("wallet not found — please top up your wallet first")
		}
		return fmt.Errorf("insufficient wallet balance — your balance is less than PKR %.2f", amount)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("wallet commit failed: %w", err)
	}

	return nil
}
