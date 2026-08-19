package service

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnigo/backend/internal/ledger"
)

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

// RiderWalletResponse is the wallet overview returned to the rider/admin.
type RiderWalletResponse struct {
	RiderTrackingID  string              `json:"rider_tracking_id"`
	Balance          float64             `json:"balance"`
	LifetimeEarnings float64             `json:"lifetime_earnings"`
	CashInHand       float64             `json:"cash_in_hand"`
	IsCashBlocked    bool                `json:"is_cash_blocked"`
	RecentCredits    []RiderWalletCredit `json:"recent_credits"`
	UpdatedAt        string              `json:"updated_at"`
}

// RiderWalletCredit represents a single completed-delivery credit.
type RiderWalletCredit struct {
	DeliveryID  string  `json:"delivery_id"`
	OrderID     string  `json:"order_id"`
	DeliveryFee float64 `json:"delivery_fee"`
	Commission  float64 `json:"admin_commission"`
	NetCredit   float64 `json:"net_credit"`
	CreditedAt  string  `json:"credited_at"`
}

type RiderWalletService struct {
	db     *pgxpool.Pool
	ledger *ledger.Service // optional: for double-entry ledger sync
}

func NewRiderWalletService(db *pgxpool.Pool) *RiderWalletService {
	return &RiderWalletService{db: db}
}

// NewRiderWalletServiceWithLedger creates a wallet service that also writes
// to the double-entry ledger when crediting rider earnings.
func NewRiderWalletServiceWithLedger(db *pgxpool.Pool, ledgerSvc *ledger.Service) *RiderWalletService {
	return &RiderWalletService{db: db, ledger: ledgerSvc}
}

// GetWallet returns the rider wallet including recent delivery credits.
func (s *RiderWalletService) GetWallet(ctx context.Context, riderTrackingID string) (*RiderWalletResponse, error) {
	var resp RiderWalletResponse
	resp.RiderTrackingID = riderTrackingID

	walletQuery := `
		SELECT COALESCE(balance, 0), COALESCE(lifetime_earnings, 0), COALESCE(cash_in_hand, 0), updated_at
		FROM rider_wallet
		WHERE rider_tracking_id = $1
	`
	var updatedAt time.Time
	err := s.db.QueryRow(ctx, walletQuery, riderTrackingID).Scan(
		&resp.Balance, &resp.LifetimeEarnings, &resp.CashInHand, &updatedAt,
	)
	if err != nil {
		// No wallet row yet is valid — return zeros.
		resp.Balance = 0
		resp.LifetimeEarnings = 0
		resp.CashInHand = 0
		resp.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	} else {
		resp.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	}

	// Block if cash >= threshold
	resp.IsCashBlocked = resp.CashInHand >= envFloat("RIDER_CASH_BLOCK_THRESHOLD", 5000.0)

	creditQuery := `
		SELECT d.tracking_id, d.order_tracking_id, COALESCE(d.delivery_fee, 0), COALESCE(d.admin_commission, 0), d.updated_at
		FROM deliveries d
		WHERE d.rider_tracking_id = $1 AND d.status = 'completed'
		ORDER BY d.updated_at DESC
		LIMIT 20
	`
	rows, err := s.db.Query(ctx, creditQuery, riderTrackingID)
	if err != nil {
		return nil, fmt.Errorf("recent credits query failed: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var c RiderWalletCredit
		var deliveredAt time.Time
		if err := rows.Scan(&c.DeliveryID, &c.OrderID, &c.DeliveryFee, &c.Commission, &deliveredAt); err != nil {
			return nil, err
		}
		c.NetCredit = c.DeliveryFee - c.Commission
		c.CreditedAt = deliveredAt.UTC().Format(time.RFC3339)
		resp.RecentCredits = append(resp.RecentCredits, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &resp, nil
}

// CreditDelivery adds the net rider earning to a rider's wallet atomically.
// It also creates a double-entry ledger transfer from central_escrow → rider_wallet
// if a ledger service is configured, keeping the ledger in sync with the wallet table.
//
// The transfer source is central_escrow (delivery fee pool funded by online payments).
// For COD orders, central_escrow is funded by the COD settlement handler.
func (s *RiderWalletService) CreditDelivery(ctx context.Context, riderTrackingID, deliveryID string, riderEarning, adminCommission float64) error {
	netCredit := riderEarning
	if netCredit < 0 {
		return fmt.Errorf("net credit cannot be negative")
	}

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("failed to begin wallet transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	upsertQuery := `
		INSERT INTO rider_wallet (rider_tracking_id, balance, lifetime_earnings, updated_at)
		VALUES ($1, $2, $2, NOW())
		ON CONFLICT (rider_tracking_id)
		DO UPDATE SET
			balance = rider_wallet.balance + $2,
			lifetime_earnings = rider_wallet.lifetime_earnings + $2,
			updated_at = NOW()
	`
	_, err = tx.Exec(ctx, upsertQuery, riderTrackingID, netCredit)
	if err != nil {
		return fmt.Errorf("wallet credit failed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("wallet commit failed: %w", err)
	}

	// Create double-entry ledger transfer: central_escrow → rider_wallet
	// This keeps the ledger in sync with the wallet table.
	if s.ledger != nil && netCredit > 0 {
		idempotencyKey := fmt.Sprintf("rider:credit:%s:%s", deliveryID, riderTrackingID)
		if _, err := s.ledger.Transfer(ctx, ledger.TransferRequest{
			DebitAccount:   ledger.AccountCentralEscrow,
			CreditAccount:  ledger.AccountRiderWallet,
			Amount:         netCredit,
			Currency:       "PKR",
			ReferenceType:  "delivery_credit",
			ReferenceID:    deliveryID,
			Description:    fmt.Sprintf("Rider delivery earning: %.2f PKR (fee: %.2f, commission: %.2f)", netCredit, riderEarning, adminCommission),
			IdempotencyKey: idempotencyKey,
		}); err != nil {
			// Log but don't fail — wallet table is already updated
			fmt.Printf("[RiderWallet] Warning: ledger transfer failed for delivery %s: %v\n", deliveryID, err)
		}
	}

	return nil
}

// AddCODCollection adds collected cash to the rider's cash_in_hand balance.
func (s *RiderWalletService) AddCODCollection(ctx context.Context, riderTrackingID string, amount float64) error {
	if amount <= 0 {
		return nil
	}
	query := `
		INSERT INTO rider_wallet (rider_tracking_id, cash_in_hand, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (rider_tracking_id)
		DO UPDATE SET
			cash_in_hand = rider_wallet.cash_in_hand + $2,
			updated_at = NOW()
	`
	_, err := s.db.Exec(ctx, query, riderTrackingID, amount)
	if err != nil {
		return fmt.Errorf("failed to add COD collection: %w", err)
	}
	return nil
}

// ClearCODCollection resets the cash_in_hand balance after a deposit.
func (s *RiderWalletService) ClearCODCollection(ctx context.Context, riderTrackingID string) error {
	query := `
		UPDATE rider_wallet
		SET cash_in_hand = 0, updated_at = NOW()
		WHERE rider_tracking_id = $1
	`
	_, err := s.db.Exec(ctx, query, riderTrackingID)
	if err != nil {
		return fmt.Errorf("failed to clear COD collection: %w", err)
	}
	return nil
}
