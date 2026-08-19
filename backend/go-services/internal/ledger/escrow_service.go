package ledger

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// MoveToVendorPendingEscrow moves funds from the central order escrow to the vendor's 48-hour pending escrow.
func (s *Service) MoveToVendorPendingEscrow(ctx context.Context, orderID string, amount float64) (uuid.UUID, error) {
	req := TransferRequest{
		DebitAccount:   AccountCentralEscrow,
		CreditAccount:  AccountVendorPendingEscrow,
		Amount:         amount,
		Currency:       "PKR",
		ReferenceType:  "order_escrow_hold",
		ReferenceID:    orderID,
		Description:    fmt.Sprintf("48-hour escrow hold for order %s", orderID),
		IdempotencyKey: fmt.Sprintf("escrow_hold_%s", orderID),
	}

	return s.Transfer(ctx, req)
}

// ReleaseEscrowToVendor splits the pending escrow into admin commission and vendor spendable wallet.
func (s *Service) ReleaseEscrowToVendor(ctx context.Context, orderID string, amount float64, commissionRate float64) (uuid.UUID, error) {
	if commissionRate < 0 || commissionRate > 1 {
		return uuid.Nil, fmt.Errorf("commission rate must be between 0 and 1")
	}

	adminAmount := amount * commissionRate
	vendorAmount := amount - adminAmount

	reqs := []TransferRequest{
		{
			DebitAccount:   AccountVendorPendingEscrow,
			CreditAccount:  AccountAdminRevenue,
			Amount:         adminAmount,
			Currency:       "PKR",
			ReferenceType:  "admin_commission",
			ReferenceID:    orderID,
			Description:    fmt.Sprintf("Admin commission for order %s", orderID),
			IdempotencyKey: fmt.Sprintf("commission_%s", orderID),
		},
		{
			DebitAccount:   AccountVendorPendingEscrow,
			CreditAccount:  AccountVendorWallet,
			Amount:         vendorAmount,
			Currency:       "PKR",
			ReferenceType:  "vendor_payout",
			ReferenceID:    orderID,
			Description:    fmt.Sprintf("Vendor payout for order %s", orderID),
			IdempotencyKey: fmt.Sprintf("vendor_payout_%s", orderID),
		},
	}

	return s.MultiTransfer(ctx, reqs)
}

// ReverseEscrowToCustomer refunds the pending escrow back to the customer's refund account.
func (s *Service) ReverseEscrowToCustomer(ctx context.Context, orderID string, amount float64) (uuid.UUID, error) {
	req := TransferRequest{
		DebitAccount:   AccountVendorPendingEscrow,
		CreditAccount:  AccountCustomerRefund,
		Amount:         amount,
		Currency:       "PKR",
		ReferenceType:  "order_refund",
		ReferenceID:    orderID,
		Description:    fmt.Sprintf("Refund for order %s", orderID),
		IdempotencyKey: fmt.Sprintf("refund_%s", orderID),
	}

	return s.Transfer(ctx, req)
}
