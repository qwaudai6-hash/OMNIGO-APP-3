package service

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/shared/money"
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
// All amounts are in paisa (int64) for internal precision.
// JSON fields remain float64 for backward compatibility with frontend.
type RiderWalletResponse struct {
	RiderTrackingID    string              `json:"rider_tracking_id"`
	Balance            float64             `json:"balance"`              // rupees for display
	BalancePaisa       int64               `json:"balance_paisa"`        // internal
	LifetimeEarnings   float64             `json:"lifetime_earnings"`    // rupees for display
	LifetimeEarningsPaisa int64            `json:"lifetime_earnings_paisa"` // internal
	CashInHand         float64             `json:"cash_in_hand"`         // rupees for display
	CashInHandPaisa    int64               `json:"cash_in_hand_paisa"`   // internal
	IsCashBlocked      bool                `json:"is_cash_blocked"`
	RecentCredits      []RiderWalletCredit `json:"recent_credits"`
	UpdatedAt          string              `json:"updated_at"`
}

// RiderWalletCredit represents a single completed-delivery credit.
type RiderWalletCredit struct {
	DeliveryID       string  `json:"delivery_id"`
	OrderID          string  `json:"order_id"`
	DeliveryFee      float64 `json:"delivery_fee"`       // rupees for display
	DeliveryFeePaisa int64   `json:"delivery_fee_paisa"` // internal
	Commission       float64 `json:"admin_commission"`   // rupees for display
	CommissionPaisa  int64   `json:"admin_commission_paisa"` // internal
	NetCredit        float64 `json:"net_credit"`         // rupees for display
	NetCreditPaisa   int64   `json:"net_credit_paisa"`   // internal
	CreditedAt       string  `json:"credited_at"`
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
		SELECT COALESCE(balance_paisa, 0), COALESCE(lifetime_earnings_paisa, 0), COALESCE(cash_in_hand_paisa, 0), updated_at
		FROM rider_wallet
		WHERE rider_tracking_id = $1
	`
	var updatedAt time.Time
	var balancePaisa, lifetimeEarningsPaisa, cashInHandPaisa int64
	err := s.db.QueryRow(ctx, walletQuery, riderTrackingID).Scan(
		&balancePaisa, &lifetimeEarningsPaisa, &cashInHandPaisa, &updatedAt,
	)
	if err != nil {
		// No wallet row yet is valid — return zeros.
		resp.Balance = 0
		resp.BalancePaisa = 0
		resp.LifetimeEarnings = 0
		resp.LifetimeEarningsPaisa = 0
		resp.CashInHand = 0
		resp.CashInHandPaisa = 0
		resp.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	} else {
		resp.Balance = money.PaisaToRupees(balancePaisa)
		resp.BalancePaisa = balancePaisa
		resp.LifetimeEarnings = money.PaisaToRupees(lifetimeEarningsPaisa)
		resp.LifetimeEarningsPaisa = lifetimeEarningsPaisa
		resp.CashInHand = money.PaisaToRupees(cashInHandPaisa)
		resp.CashInHandPaisa = cashInHandPaisa
		resp.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	}

	// Block if cash >= threshold
	cashThresholdPaisa := int64(envFloat("RIDER_CASH_BLOCK_THRESHOLD", 5000.0) * 100)
	resp.IsCashBlocked = resp.CashInHandPaisa >= cashThresholdPaisa

	creditQuery := `
		SELECT d.tracking_id, d.order_tracking_id, COALESCE(d.amount_paisa, 0), COALESCE(d.commission_paisa, 0), d.updated_at
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
		if err := rows.Scan(&c.DeliveryID, &c.OrderID, &c.DeliveryFeePaisa, &c.CommissionPaisa, &deliveredAt); err != nil {
			return nil, err
		}
		c.DeliveryFee = money.PaisaToRupees(c.DeliveryFeePaisa)
		c.Commission = money.PaisaToRupees(c.CommissionPaisa)
		c.NetCreditPaisa = c.DeliveryFeePaisa - c.CommissionPaisa
		c.NetCredit = money.PaisaToRupees(c.NetCreditPaisa)
		c.CreditedAt = deliveredAt.UTC().Format(time.RFC3339)
		resp.RecentCredits = append(resp.RecentCredits, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &resp, nil
}

// CreditDelivery creates a double-entry ledger transfer from central_escrow → rider_wallet
// keeping the ledger in sync with the wallet table.
// All amounts are in paisa (int64).
//
// The transfer source is central_escrow (delivery fee pool funded by online payments).
// For COD orders, central_escrow is funded by the COD settlement handler.
// NOTE: The actual Postgres rider_wallet balance update is now handled atomically
// within the delivery_repository's UpdateGigStatus transaction to prevent race conditions.
func (s *RiderWalletService) CreditDelivery(ctx context.Context, riderTrackingID, deliveryID string, riderEarningPaisa, adminCommissionPaisa int64) error {
	netCredit := riderEarningPaisa
	if netCredit < 0 {
		return fmt.Errorf("net credit cannot be negative")
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
			Description:    fmt.Sprintf("Rider delivery earning: %d paisa (fee: %d, commission: %d)", netCredit, riderEarningPaisa, adminCommissionPaisa),
			IdempotencyKey: idempotencyKey,
		}); err != nil {
			// Log but don't fail — wallet table is already updated
			fmt.Printf("[RiderWallet] Warning: ledger transfer failed for delivery %s: %v\n", deliveryID, err)
		}
	}

	return nil
}

// AddCODCollection adds collected cash to the rider's cash_in_hand balance.
// amountPaisa is in paisa (int64).
func (s *RiderWalletService) AddCODCollection(ctx context.Context, riderTrackingID string, amountPaisa int64) error {
	if amountPaisa <= 0 {
		return nil
	}
	query := `
		INSERT INTO rider_wallet (rider_tracking_id, cash_in_hand_paisa, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (rider_tracking_id)
		DO UPDATE SET
			cash_in_hand_paisa = rider_wallet.cash_in_hand_paisa + $2,
			updated_at = NOW()
	`
	_, err := s.db.Exec(ctx, query, riderTrackingID, amountPaisa)
	if err != nil {
		return fmt.Errorf("failed to add COD collection: %w", err)
	}
	return nil
}

// CreateCODDebtLedger creates the ledger entry for COD debt: cash_receivable → rider_cod_debt.
// This is the single source of truth for the rider_cod_debt ledger account.
// It is idempotent via the orderTrackingID in the idempotency key.
// Called by both:
//   - Confirm endpoint (rider confirms before delivery)
//   - Delivery service (when delivery completes — fallback if Confirm was not called)
func (s *RiderWalletService) CreateCODDebtLedger(ctx context.Context, orderTrackingID string, amountPaisa int64) error {
	if amountPaisa <= 0 {
		return nil
	}
	if s.ledger == nil {
		// No ledger configured — skip silently (debt row is still created)
		return nil
	}
	idempotencyKey := fmt.Sprintf("cod:debt:%s", orderTrackingID)
	_, err := s.ledger.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   ledger.AccountCashReceivable,
		CreditAccount:  ledger.AccountRiderCODDebt,
		Amount:         amountPaisa,
		Currency:       "PKR",
		ReferenceType:  "cod_debt",
		ReferenceID:    orderTrackingID,
		Description:    fmt.Sprintf("COD debt: rider collected %d paisa for order %s", amountPaisa, orderTrackingID),
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("failed to create COD debt ledger entry: %w", err)
	}
	return nil
}

// ClearCODCollection resets the cash_in_hand balance after a deposit.
func (s *RiderWalletService) ClearCODCollection(ctx context.Context, riderTrackingID string) error {
	query := `
		UPDATE rider_wallet
		SET cash_in_hand_paisa = 0, updated_at = NOW()
		WHERE rider_tracking_id = $1
	`
	_, err := s.db.Exec(ctx, query, riderTrackingID)
	if err != nil {
		return fmt.Errorf("failed to clear COD collection: %w", err)
	}
	return nil
}

// DecrementCODCollection decrements cash_in_hand when a COD debt is settled.
// FIX H1: Ensures cash_in_hand accurately reflects actual cash on hand.
// amountPaisa is in paisa (int64).
func (s *RiderWalletService) DecrementCODCollection(ctx context.Context, riderTrackingID string, amountPaisa int64) error {
	if amountPaisa <= 0 {
		return nil
	}
	query := `
		UPDATE rider_wallet
		SET cash_in_hand_paisa = GREATEST(0, cash_in_hand_paisa - $1), updated_at = NOW()
		WHERE rider_tracking_id = $2
	`
	_, err := s.db.Exec(ctx, query, amountPaisa, riderTrackingID)
	if err != nil {
		return fmt.Errorf("failed to decrement COD collection: %w", err)
	}
	return nil
}
