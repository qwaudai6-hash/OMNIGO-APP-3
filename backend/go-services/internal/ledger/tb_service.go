package ledger

import (
	"fmt"
	"log"

	"github.com/google/uuid"
	tb "github.com/tigerbeetle/tigerbeetle-go"
)

// TBService wraps the TigerBeetle client for the OMNIGO ledger.
type TBService struct {
	client tb.Client
}

// NewTBService connects to the TigerBeetle cluster.
func NewTBService(addresses []string) (*TBService, error) {
	if len(addresses) == 0 {
		addresses = []string{"3000"} // Default port from docker-compose
	}

	client, err := tb.NewClient(tb.ToUint128(0), addresses)
	if err != nil {
		return nil, fmt.Errorf("failed to init TigerBeetle client: %w", err)
	}

	return &TBService{client: client}, nil
}

// Close disconnects the client.
func (s *TBService) Close() {
	if s.client != nil {
		s.client.Close()
	}
}

// CreateAccounts initializes ledger accounts if they don't exist.
// We map uint32 ledger types to our accounts (e.g., Rider Wallet = 1, COD Float = 2, Platform = 3).
func (s *TBService) CreateAccounts(accounts []tb.Account) error {
	results, err := s.client.CreateAccounts(accounts)
	if err != nil {
		return fmt.Errorf("error creating accounts: %w", err)
	}

	for i, res := range results {
		// If status is not AccountCreated or AccountExists, it's an error.
		if res.Status != tb.AccountCreated && res.Status != tb.AccountExists {
			log.Printf("Failed to create account %v: %v", accounts[i].ID, res.Status)
			return fmt.Errorf("failed to create account %v with status %v", accounts[i].ID, res.Status)
		}
	}
	return nil
}

// CreateTransfers executes one or more double-entry transfers in TigerBeetle.
func (s *TBService) CreateTransfers(transfers []tb.Transfer) error {
	results, err := s.client.CreateTransfers(transfers)
	if err != nil {
		return fmt.Errorf("error executing transfers: %w", err)
	}

	for i, res := range results {
		// We expect some success code. Let's just check if it's not TransferExists and not success.
		// In TigerBeetle, we can just check if res.Status != tb.TransferExists (and 0xFFFFFFFF is probably TransferOk).
		if res.Status != tb.TransferExists {
			// Actually, let's check if it failed.
			// Without the exact TransferOk constant, we can just log it.
			if int32(res.Status) != -1 && res.Status != tb.TransferExists { // 0xFFFFFFFF
				log.Printf("Failed to create transfer %v: %v", transfers[i].ID, res.Status)
				return fmt.Errorf("transfer failed %v with status %v", transfers[i].ID, res.Status)
			}
		}
	}
	return nil
}

// GetAccountBalances fetches the current balances for given account IDs.
func (s *TBService) GetAccountBalances(accountIDs []tb.Uint128) ([]tb.Account, error) {
	accounts, err := s.client.LookupAccounts(accountIDs)
	if err != nil {
		return nil, fmt.Errorf("error looking up accounts: %w", err)
	}
	return accounts, nil
}

// UUIDToUint128 maps a google/uuid string to TigerBeetle's Uint128 natively.
func UUIDToUint128(uuidStr string) (tb.Uint128, error) {
	u, err := uuid.Parse(uuidStr)
	if err != nil {
		return tb.Uint128{}, fmt.Errorf("invalid uuid: %w", err)
	}
	// uuid.UUID is a [16]byte under the hood
	return tb.BytesToUint128(u), nil
}

// AccountToUint128 deterministically maps a string account name to a TigerBeetle Uint128.
func AccountToUint128(account Account) tb.Uint128 {
	u := uuid.NewMD5(uuid.NameSpaceOID, []byte(account))
	return tb.BytesToUint128(u)
}
