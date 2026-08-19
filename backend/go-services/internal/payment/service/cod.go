package service

import (
	"context"
	"fmt"

	"github.com/omnigo/backend/internal/ledger"
	paymentRepo "github.com/omnigo/backend/internal/payment/repository"
)

// CODService handles cash-on-delivery order accounting. It records a pending
// transaction, moves the order amount into central escrow when the delivery is
// marked collected, and splits to admin revenue, rider wallet, and vendor escrow.
type CODService struct {
	ledger *ledger.Service
	repo   *paymentRepo.Repository
}

func NewCODService(ledgerSvc *ledger.Service, repo *paymentRepo.Repository) *CODService {
	return &CODService{ledger: ledgerSvc, repo: repo}
}

// OnOrderCreated records a pending COD payment transaction for the order.
// The customer has not yet paid; the rider will collect cash on delivery.
func (s *CODService) OnOrderCreated(ctx context.Context, orderID string, amount float64, currency string) error {
	txn := &paymentRepo.PaymentTransaction{
		OrderID:        orderID,
		Gateway:        "cod",
		Amount:         amount,
		Currency:       currency,
		Status:         paymentRepo.TxnPending,
		Kind:           paymentRepo.KindPayment,
		IdempotencyKey: fmt.Sprintf("cod:order:%s", orderID),
		Metadata: map[string]any{
			"event": "order_created",
		},
	}
	_, err := s.repo.Create(ctx, txn)
	return err
}

// OnCashCollected is called when the rider hands the order to the customer
// and collects the cash. It records a captured transaction and moves funds
// through the ledger:
//   - debit cash_receivable (money rider owes platform)
//   - credit central_escrow (platform holds the cash until release)
func (s *CODService) OnCashCollected(ctx context.Context, orderID string, amount float64) error {
	if amount <= 0 {
		return fmt.Errorf("invalid COD amount %f", amount)
	}

	// Record capture if not already present.
	existing, _ := s.repo.GetByOrderID(ctx, orderID, paymentRepo.KindPayment)
	if existing == nil {
		_, err := s.repo.Create(ctx, &paymentRepo.PaymentTransaction{
			OrderID:        orderID,
			Gateway:        "cod",
			Amount:         amount,
			Currency:       "PKR",
			Status:         paymentRepo.TxnCaptured,
			Kind:           paymentRepo.KindPayment,
			IdempotencyKey: fmt.Sprintf("cod:order:%s", orderID),
		})
		if err != nil {
			return fmt.Errorf("failed to record COD capture transaction: %w", err)
		}
	} else if existing.Status != paymentRepo.TxnCaptured {
		if err := s.repo.UpdateStatus(ctx, existing.TransactionID, paymentRepo.TxnCaptured, "", map[string]any{"event": "cash_collected"}, ""); err != nil {
			return fmt.Errorf("failed to update COD transaction to captured: %w", err)
		}
	}

	// Ledger: rider now owes the platform the cash collected.
	_, err := s.ledger.Transfer(ctx, ledger.TransferRequest{
		DebitAccount:   ledger.AccountCashReceivable, // rider owes cash
		CreditAccount:  ledger.AccountCentralEscrow,  // platform holds it
		Amount:         amount,
		Currency:       "PKR",
		ReferenceType:  "cod_collection",
		ReferenceID:    orderID,
		Description:    fmt.Sprintf("COD cash collected for order %s", orderID),
		IdempotencyKey: fmt.Sprintf("cod:collection:%s", orderID),
	})
	if err != nil {
		return fmt.Errorf("cod collection ledger transfer failed: %w", err)
	}

	return nil
}

// ReleaseAfterDelivery splits the collected central escrow into admin revenue,
// rider wallet, and vendor locked escrow. This is invoked by the order/delivery
// service once delivery is confirmed and COD cash has been collected.
func (s *CODService) ReleaseAfterDelivery(ctx context.Context, orderID, vendorID, riderID string, orderTotal, adminCommission, riderEarning float64) error {
	if orderTotal <= 0 {
		return fmt.Errorf("invalid order total %f", orderTotal)
	}
	if adminCommission+riderEarning > orderTotal {
		return fmt.Errorf("commission + rider earning %.2f exceeds order total %.2f", adminCommission+riderEarning, orderTotal)
	}
	vendorAmount := orderTotal - adminCommission - riderEarning

	var transfers []ledger.TransferRequest

	// 1) Move collected cash from central escrow to admin revenue and vendor escrow.
	transfers = append(transfers, ledger.TransferRequest{
		DebitAccount:   ledger.AccountCentralEscrow,
		CreditAccount:  ledger.AccountAdminRevenue,
		Amount:         adminCommission,
		Currency:       "PKR",
		ReferenceType:  "cod_admin_commission",
		ReferenceID:    orderID,
		Description:    fmt.Sprintf("COD admin commission for order %s", orderID),
		IdempotencyKey: fmt.Sprintf("cod:admin:%s", orderID),
	})

	transfers = append(transfers, ledger.TransferRequest{
		DebitAccount:   ledger.AccountCentralEscrow,
		CreditAccount:  ledger.AccountVendorLockedEscrow,
		Amount:         vendorAmount,
		Currency:       "PKR",
		ReferenceType:  "cod_vendor_escrow",
		ReferenceID:    orderID,
		Description:    fmt.Sprintf("COD vendor escrow for order %s", orderID),
		IdempotencyKey: fmt.Sprintf("cod:vendor:%s", orderID),
	})

	// 2) Credit rider wallet for their earning.
	if riderEarning > 0 {
		transfers = append(transfers, ledger.TransferRequest{
			DebitAccount:   ledger.AccountAdminRevenue, // sourced from admin revenue pool
			CreditAccount:  ledger.AccountRiderWallet,
			Amount:         riderEarning,
			Currency:       "PKR",
			ReferenceType:  "cod_rider_earning",
			ReferenceID:    orderID,
			Description:    fmt.Sprintf("COD rider earning for order %s", orderID),
			IdempotencyKey: fmt.Sprintf("cod:rider:%s", orderID),
		})
	}

	_, err := s.ledger.MultiTransfer(ctx, transfers)
	if err != nil {
		return fmt.Errorf("cod release multi-transfer failed: %w", err)
	}

	return nil
}

// ReverseCollection reverses a COD collection when delivery fails or cash is
// returned. It records a reversal transaction and returns funds from central
// escrow back to the customer refund account.
func (s *CODService) ReverseCollection(ctx context.Context, orderID string, amount float64, reason string) error {
	if amount <= 0 {
		return fmt.Errorf("invalid reversal amount %f", amount)
	}

	// Record reversal.
	_, err := s.repo.Create(ctx, &paymentRepo.PaymentTransaction{
		OrderID:        orderID,
		Gateway:        "cod",
		Amount:         amount,
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
		Amount:         amount,
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
