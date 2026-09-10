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
// For COD orders, central_escrow is funded later by the COD settlement handler, so
// we skip the ledger transfer here and rely on CODHandler.Settlement() to fund it instead.
//
// NOTE: The actual Postgres rider_wallet balance update is handled atomically
// within the delivery_repository's UpdateGigStatus transaction to prevent race conditions.
func (s *RiderWalletService) CreditDelivery(ctx context.Context, riderTrackingID, deliveryID string, riderEarningPaisa, adminCommissionPaisa int64) error {
	netCredit := riderEarningPaisa
	if netCredit < 0 {
		return fmt.Errorf("net credit cannot be negative")
	}

	// H4 FIX: For online payments, central_escrow is already funded by ExecuteSplit.
	// For COD orders, central_escrow is funded later by CODHandler.Settlement().
	// We only attempt ledger transfer for non-COD orders.
	// The isCOD flag is passed as a context value to avoid changing the function signature.
	// If context indicates COD, skip ledger transfer and let COD settlement handle it.
	isCOD := ctx.Value("is_cod_order") == true
	if isCOD {
		// For COD, the wallet is credited via AddCODCollection in delivery_service.
		// No ledger transfer needed here since central_escrow isn't funded yet.
		return nil
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

	// Move admin commission from central_escrow to admin_revenue so it doesn't
	// sit orphaned in central_escrow forever.
	if s.ledger != nil && adminCommissionPaisa > 0 {
		idempotencyKey := fmt.Sprintf("delivery:admin_comm:%s", deliveryID)
		if _, err := s.ledger.Transfer(ctx, ledger.TransferRequest{
			DebitAccount:   ledger.AccountCentralEscrow,
			CreditAccount:  ledger.AccountAdminRevenue,
			Amount:         adminCommissionPaisa,
			Currency:       "PKR",
			ReferenceType:  "delivery_commission",
			ReferenceID:    deliveryID,
			Description:    fmt.Sprintf("Delivery admin commission from central_escrow: %d paisa", adminCommissionPaisa),
			IdempotencyKey: idempotencyKey,
		}); err != nil {
			fmt.Printf("[RiderWallet] Warning: admin commission ledger transfer failed for delivery %s: %v\n", deliveryID, err)
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

// RiderWithdrawalRequest represents a cash-out request from a rider.
type RiderWithdrawalRequest struct {
	RiderTrackingID string  `json:"rider_tracking_id"`
	Amount          float64 `json:"amount"`
	AmountPaisa     int64   `json:"amount_paisa"`
	Method          string  `json:"method"` // easypaisa, jazzcash, bank_transfer
	AccountNumber   string  `json:"account_number"`
	AccountTitle    string  `json:"account_title"`
}

// RiderWithdrawalResponse is returned after a cash-out request is created.
type RiderWithdrawalResponse struct {
	PayoutID              string  `json:"payout_id"`
	RiderTrackingID       string  `json:"rider_tracking_id"`
	Amount                float64 `json:"amount"`
	AmountPaisa           int64   `json:"amount_paisa"`
	Method                string  `json:"method"`
	Status                string  `json:"status"`
	RemainingBalance      float64 `json:"remaining_balance"`
	RemainingBalancePaisa int64   `json:"remaining_balance_paisa"`
	CreatedAt             string  `json:"created_at"`
}

// RequestWithdrawal initiates a withdrawal request for the rider, locking the wallet row,
// ensuring no COD block or float exceeds threshold, and deducting the balance atomically.
func (s *RiderWalletService) RequestWithdrawal(ctx context.Context, req RiderWithdrawalRequest) (*RiderWithdrawalResponse, error) {
	if req.AmountPaisa <= 0 && req.Amount > 0 {
		req.AmountPaisa = money.RupeesToPaisa(req.Amount)
	}
	if req.AmountPaisa <= 0 {
		return nil, fmt.Errorf("withdrawal amount must be greater than zero")
	}

	// Enforce minimum withdrawal (PKR 500 = 50,000 paisa)
	minWithdrawalPaisa := int64(envFloat("RIDER_MIN_WITHDRAWAL_PKR", 500.0) * 100)
	if req.AmountPaisa < minWithdrawalPaisa {
		return nil, fmt.Errorf("minimum withdrawal amount is PKR %.2f", float64(minWithdrawalPaisa)/100.0)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to start withdrawal transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Check wallet row and lock FOR UPDATE
	var balancePaisa, cashInHandPaisa int64
	var isCashBlocked bool
	lockQuery := `
		SELECT COALESCE(balance_paisa, 0), COALESCE(cash_in_hand_paisa, 0), COALESCE(is_cash_blocked, false)
		FROM rider_wallet
		WHERE rider_tracking_id = $1
		FOR UPDATE
	`
	err = tx.QueryRow(ctx, lockQuery, req.RiderTrackingID).Scan(&balancePaisa, &cashInHandPaisa, &isCashBlocked)
	if err != nil {
		return nil, fmt.Errorf("rider wallet not found: %w", err)
	}

	if isCashBlocked {
		return nil, fmt.Errorf("withdrawal blocked: rider has an active cash block")
	}

	cashThresholdPaisa := int64(envFloat("RIDER_CASH_BLOCK_THRESHOLD", 5000.0) * 100)
	if cashInHandPaisa >= cashThresholdPaisa {
		return nil, fmt.Errorf("withdrawal blocked: cash-in-hand float (PKR %.2f) exceeds threshold, deposit float first", float64(cashInHandPaisa)/100.0)
	}

	if balancePaisa < req.AmountPaisa {
		return nil, fmt.Errorf("insufficient wallet balance (available: PKR %.2f, requested: PKR %.2f)",
			float64(balancePaisa)/100.0, float64(req.AmountPaisa)/100.0)
	}

	var remainingBalancePaisa int64
	updateQuery := `
		UPDATE rider_wallet
		SET balance_paisa = balance_paisa - $1,
		    balance = (balance_paisa - $1)::decimal / 100.0,
		    updated_at = NOW()
		WHERE rider_tracking_id = $2 AND balance_paisa >= $1
		RETURNING balance_paisa
	`
	err = tx.QueryRow(ctx, updateQuery, req.AmountPaisa, req.RiderTrackingID).Scan(&remainingBalancePaisa)
	if err != nil {
		return nil, fmt.Errorf("failed to deduct withdrawal amount: %w", err)
	}

	var payoutID string
	var createdAt time.Time
	insertPayoutQuery := `
		INSERT INTO rider_payouts (
			id, rider_tracking_id, amount, amount_paisa, method, account_number, account_title, status, created_at, updated_at
		) VALUES (
			gen_random_uuid(), $1, $2, $3, $4, $5, $6, 'pending', NOW(), NOW()
		)
		RETURNING id::text, created_at
	`
	amountRupees := float64(req.AmountPaisa) / 100.0
	err = tx.QueryRow(ctx, insertPayoutQuery,
		req.RiderTrackingID,
		amountRupees,
		req.AmountPaisa,
		req.Method,
		req.AccountNumber,
		req.AccountTitle,
	).Scan(&payoutID, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("failed to record rider payout: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit withdrawal transaction: %w", err)
	}

	// Double-entry ledger transfer: rider_wallet → gateway_clearing
	if s.ledger != nil {
		idempotencyKey := fmt.Sprintf("rider:payout:%s", payoutID)
		if _, err := s.ledger.Transfer(ctx, ledger.TransferRequest{
			DebitAccount:   ledger.AccountRiderWallet,
			CreditAccount:  ledger.AccountGatewayClearing,
			Amount:         req.AmountPaisa,
			Currency:       "PKR",
			ReferenceType:  "rider_payout",
			ReferenceID:    payoutID,
			Description:    fmt.Sprintf("Rider payout request %s: %d paisa to %s (%s)", payoutID, req.AmountPaisa, req.Method, req.AccountNumber),
			IdempotencyKey: idempotencyKey,
		}); err != nil {
			fmt.Printf("[RiderWallet] Warning: payout ledger transfer failed: %v\n", err)
		}
	}

	return &RiderWithdrawalResponse{
		PayoutID:              payoutID,
		RiderTrackingID:       req.RiderTrackingID,
		Amount:                amountRupees,
		AmountPaisa:           req.AmountPaisa,
		Method:                req.Method,
		Status:                "pending",
		RemainingBalance:      float64(remainingBalancePaisa) / 100.0,
		RemainingBalancePaisa: remainingBalancePaisa,
		CreatedAt:             createdAt.UTC().Format(time.RFC3339),
	}, nil
}

