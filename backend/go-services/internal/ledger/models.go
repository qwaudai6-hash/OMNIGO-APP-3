package ledger

import (
	"time"

	"github.com/google/uuid"
)

// Account represents a named ledger account.
type Account string

const (
	AccountAdminRevenue        Account = "admin_revenue_account"
	AccountCentralEscrow       Account = "central_escrow_account"
	AccountGatewayClearing     Account = "gateway_clearing_account"
	AccountVendorPendingEscrow Account = "vendor_pending_escrow_account"
	AccountVendorWallet        Account = "vendor_wallet_account"
	AccountCustomerRefund      Account = "customer_refund_account"
	AccountCustomerWallet      Account = "customer_wallet_account" // Daraz style stored value
	AccountRiderWallet         Account = "rider_wallet"
	AccountRiderCODDebt        Account = "rider_cod_debt"
	AccountCashReceivable      Account = "cash_receivable" // Money rider owes platform for COD
	AccountVendorLockedEscrow  Account = "vendor_locked_escrow_account"
	AccountVendorWithdrawable  Account = "vendor_withdrawable_account"
	AccountStripeHolding       Account = "stripe_holding_account"
	AccountPayFastHolding      Account = "payfast_holding_account"
	AccountVendorBankPayout    Account = "vendor_bank_payout_account"
)

// ValidAccounts is the set of all recognized ledger accounts.
var ValidAccounts = map[Account]bool{
	AccountAdminRevenue:        true,
	AccountCentralEscrow:       true,
	AccountGatewayClearing:     true,
	AccountVendorPendingEscrow: true,
	AccountVendorWallet:        true,
	AccountCustomerRefund:      true,
	AccountCustomerWallet:      true,
	AccountRiderWallet:         true,
	AccountRiderCODDebt:        true,
	AccountCashReceivable:      true,
	AccountVendorLockedEscrow:  true,
	AccountVendorWithdrawable:  true,
	AccountStripeHolding:       true,
	AccountPayFastHolding:      true,
	AccountVendorBankPayout:    true,
}

// LedgerEntry represents a single double-entry ledger row.
type LedgerEntry struct {
	ID               uuid.UUID `json:"id"`
	TransactionID    uuid.UUID `json:"transaction_id"`
	Account          Account   `json:"account"`
	Amount           float64   `json:"amount"` // negative = debit, positive = credit
	Currency         string    `json:"currency"`
	ReferenceType    string    `json:"reference_type"`
	ReferenceID      string    `json:"reference_id"`
	Description      string    `json:"description"`
	IdempotencyKey   string    `json:"idempotency_key"`
	Signature        string    `json:"signature"`         // HMAC-SHA256 signature for integrity
	SignatureVersion int       `json:"signature_version"` // Allows rotating HMAC keys
	CreatedAt        time.Time `json:"created_at"`
}

// TransferRequest describes a debit + credit pair to be executed atomically.
type TransferRequest struct {
	DebitAccount   Account
	CreditAccount  Account
	Amount         float64
	Currency       string
	ReferenceType  string
	ReferenceID    string
	Description    string
	IdempotencyKey string
}

// AccountBalance represents the net balance of a ledger account.
type AccountBalance struct {
	Account  Account `json:"account"`
	Balance  float64 `json:"balance"`
	Currency string  `json:"currency"`
}
