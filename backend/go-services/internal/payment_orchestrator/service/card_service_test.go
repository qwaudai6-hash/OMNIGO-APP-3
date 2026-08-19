package service

import (
	"context"
	"testing"
)

func TestSavedCardModels(t *testing.T) {
	card := SavedCard{
		CardID:             "card_123",
		CustomerTrackingID: "usr_456",
		Gateway:            "payfast",
		CardBrand:          "visa",
		LastFour:           "4242",
		ExpiryMonth:        "12",
		ExpiryYear:         "2028",
		IsDefault:          true,
	}

	if card.CardBrand != "visa" || card.LastFour != "4242" {
		t.Errorf("unexpected card model fields: %+v", card)
	}
}

func TestCardVaultNilDBValidation(t *testing.T) {
	vault := NewCardVaultService(nil)
	ctx := context.Background()

	_, err := vault.SaveCard(ctx, "", "", "", "", "", "", "", false)
	if err == nil {
		t.Errorf("expected error on empty fields")
	}
}
