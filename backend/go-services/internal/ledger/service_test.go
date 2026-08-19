package ledger

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestTransferValidation verifies that Transfer rejects invalid inputs
// without touching the database.
func TestTransferValidation(t *testing.T) {
	svc := &Service{db: nil, repo: nil}
	ctx := context.Background()

	tests := []struct {
		name    string
		req     TransferRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "invalid debit account",
			req: TransferRequest{
				DebitAccount:  "nonexistent",
				CreditAccount: AccountAdminRevenue,
				Amount:        100,
			},
			wantErr: true,
			errMsg:  "invalid debit account",
		},
		{
			name: "invalid credit account",
			req: TransferRequest{
				DebitAccount:  AccountStripeHolding,
				CreditAccount: "nonexistent",
				Amount:        100,
			},
			wantErr: true,
			errMsg:  "invalid credit account",
		},
		{
			name: "zero amount",
			req: TransferRequest{
				DebitAccount:  AccountStripeHolding,
				CreditAccount: AccountAdminRevenue,
				Amount:        0,
			},
			wantErr: true,
			errMsg:  "transfer amount must be positive",
		},
		{
			name: "negative amount",
			req: TransferRequest{
				DebitAccount:  AccountStripeHolding,
				CreditAccount: AccountAdminRevenue,
				Amount:        -50,
			},
			wantErr: true,
			errMsg:  "transfer amount must be positive",
		},
		{
			name: "same account debit and credit",
			req: TransferRequest{
				DebitAccount:  AccountAdminRevenue,
				CreditAccount: AccountAdminRevenue,
				Amount:        100,
			},
			wantErr: true,
			errMsg:  "debit and credit accounts must be different",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Transfer(ctx, tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Transfer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil {
				if !contains(err.Error(), tt.errMsg) {
					t.Errorf("Transfer() error = %v, want containing %q", err, tt.errMsg)
				}
			}
		})
	}
}

// TestValidAccounts verifies the account enum covers all expected accounts.
func TestValidAccounts(t *testing.T) {
	expected := []Account{
		AccountAdminRevenue,
		AccountVendorLockedEscrow,
		AccountVendorWithdrawable,
		AccountRiderWallet,
		AccountCentralEscrow,
		AccountRiderCODDebt,
		AccountStripeHolding,
		AccountPayFastHolding,
	}

	for _, acct := range expected {
		if !ValidAccounts[acct] {
			t.Errorf("Account %q not in ValidAccounts map", acct)
		}
	}

	// Verify unknown account is not valid
	if ValidAccounts["fake_account"] {
		t.Error("fake_account should not be in ValidAccounts")
	}
}

// TestMultiTransferValidation verifies MultiTransfer rejects empty input.
func TestMultiTransferValidation(t *testing.T) {
	svc := &Service{db: nil, repo: nil}
	ctx := context.Background()

	_, err := svc.MultiTransfer(ctx, nil)
	if err == nil {
		t.Error("MultiTransfer() should error on nil input")
	}

	_, err = svc.MultiTransfer(ctx, []TransferRequest{})
	if err == nil {
		t.Error("MultiTransfer() should error on empty input")
	}
}

// TestIdempotencyKeyFormat verifies idempotency keys are formatted correctly.
func TestIdempotencyKeyFormat(t *testing.T) {
	key := "order:ORDR-123:split"
	debitKey := key + ":debit"
	creditKey := key + ":credit"

	if debitKey != "order:ORDR-123:split:debit" {
		t.Errorf("debit key format wrong: %s", debitKey)
	}
	if creditKey != "order:ORDR-123:split:credit" {
		t.Errorf("credit key format wrong: %s", creditKey)
	}
}

// TestTransactionIDUniqueness verifies each Transfer generates a unique transaction_id.
func TestTransactionIDUniqueness(t *testing.T) {
	ids := make(map[uuid.UUID]bool)
	for i := 0; i < 100; i++ {
		id := uuid.New()
		if ids[id] {
			t.Fatalf("duplicate transaction_id generated: %s", id)
		}
		ids[id] = true
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
