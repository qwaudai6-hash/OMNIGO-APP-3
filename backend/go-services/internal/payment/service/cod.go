package service

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnigo/backend/internal/escrow"
	"github.com/omnigo/backend/internal/ledger"
	paymentRepo "github.com/omnigo/backend/internal/payment/repository"
)

// CODService handles cash-on-delivery order accounting. It records a pending
// transaction, moves the order amount into central escrow when the delivery is
// marked collected, and splits to admin revenue, rider wallet, and vendor escrow.
type CODService struct {
	ledger *ledger.Service
	repo   *paymentRepo.Repository
	db     *pgxpool.Pool
	escrow *escrow.Service
}

func NewCODService(ledgerSvc *ledger.Service, repo *paymentRepo.Repository) *CODService {
	return &CODService{ledger: ledgerSvc, repo: repo}
}

// SetDB sets the database pool for vendor wallet / escrow / debt operations.
func (s *CODService) SetDB(db *pgxpool.Pool) {
	s.db = db
}

// SetEscrow sets the escrow service for creating holds.
func (s *CODService) SetEscrow(escrowSvc *escrow.Service) {
	s.escrow = escrowSvc
}

// OnOrderCreated records a pending COD payment transaction for the order.
// The customer has not yet paid; the rider will collect cash on delivery.
// amountPaisa is the order total in paisa (int64).
func (s *CODService) OnOrderCreated(ctx context.Context, orderID string, amountPaisa int64, currency string) error {
	txn := &paymentRepo.PaymentTransaction{
		OrderID:        orderID,
		Gateway:        "cod",
		Amount:         float64(amountPaisa) / 100.0, // Store as rupees for payment_transactions compat
		Currency:       currency,
		Status:         paymentRepo.TxnPending,
		Kind:           paymentRepo.KindPayment,
		IdempotencyKey: fmt.Sprintf("cod:order:%s", orderID),
		Metadata: map[string]any{
			"event":        "order_created",
			"amount_paisa": amountPaisa,
		},
	}
	_, err := s.repo.Create(ctx, txn)
	return err
}

// ReverseCollection reverses a COD collection when delivery fails or cash is
// returned. It records a reversal transaction and returns funds from central
// escrow back to the customer refund account.
func (s *CODService) ReverseCollection(ctx context.Context, orderID string, amountPaisa int64, reason string) error {
	if amountPaisa <= 0 {
		return fmt.Errorf("invalid reversal amount %d paisa", amountPaisa)
	}

	_, err := s.repo.Create(ctx, &paymentRepo.PaymentTransaction{
		OrderID:        orderID,
		Gateway:        "cod",
		Amount:         float64(amountPaisa) / 100.0,
		Currency:       "PKR",
		Status:         paymentRepo.TxnReversed,
		Kind:           paymentRepo.KindReversal,
		IdempotencyKey: fmt.Sprintf("cod:reversal:%s", orderID),
		Metadata: map[string]any{
			"reason": reason,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to record COD reversal: %w", err)
	}

	_, err = s.ledger.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   ledger.AccountCentralEscrow,
		CreditAccount:  ledger.AccountCustomerRefund,
		Amount:         amountPaisa,
		Currency:       "PKR",
		ReferenceType:  "cod_reversal",
		ReferenceID:    orderID,
		Description:    fmt.Sprintf("COD reversal for order %s: %s", orderID, reason),
		IdempotencyKey: fmt.Sprintf("cod:reversal:%s", orderID),
	})
	if err != nil {
		return fmt.Errorf("cod reversal ledger transfer failed: %w", err)
	}

	return nil
}
