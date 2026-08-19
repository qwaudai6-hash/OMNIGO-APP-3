package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SavedCard represents a PCI-compliant tokenized customer payment card.
type SavedCard struct {
	CardID             string    `json:"card_id"`
	CustomerTrackingID string    `json:"customer_tracking_id"`
	Gateway            string    `json:"gateway"`
	CardBrand          string    `json:"card_brand"` // visa, mastercard, paypak, unionpay
	LastFour           string    `json:"last_four"`  // e.g. "4242"
	ExpiryMonth        string    `json:"expiry_month"`
	ExpiryYear         string    `json:"expiry_year"`
	CardholderName     string    `json:"cardholder_name,omitempty"`
	IsDefault          bool      `json:"is_default"`
	CreatedAt          time.Time `json:"created_at"`
}

// CardVaultService manages customer tokenized cards securely.
type CardVaultService struct {
	db *pgxpool.Pool
}

// NewCardVaultService constructs a new CardVaultService.
func NewCardVaultService(db *pgxpool.Pool) *CardVaultService {
	return &CardVaultService{
		db: db,
	}
}

// SaveCard stores a new tokenized card instrument for a customer.
func (s *CardVaultService) SaveCard(
	ctx context.Context,
	customerTrackingID, instrumentToken, cardBrand, lastFour, expiryMonth, expiryYear, cardholderName string,
	setAsDefault bool,
) (*SavedCard, error) {
	if customerTrackingID == "" || instrumentToken == "" || lastFour == "" {
		return nil, errors.New("customer tracking id, instrument token, and last four digits are required")
	}

	cardID := "card_" + uuid.New().String()
	brand := strings.ToLower(strings.TrimSpace(cardBrand))
	if brand == "" {
		brand = "visa"
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// If marked default, unset existing default cards for this customer
	if setAsDefault {
		_, _ = tx.Exec(ctx,
			`UPDATE customer_saved_cards SET is_default = false, updated_at = NOW() WHERE customer_tracking_id = $1`,
			customerTrackingID,
		)
	}

	var createdAt time.Time
	err = tx.QueryRow(ctx,
		`INSERT INTO customer_saved_cards 
		 (card_id, customer_tracking_id, gateway, instrument_token, card_brand, last_four, expiry_month, expiry_year, cardholder_name, is_default, created_at, updated_at)
		 VALUES ($1, $2, 'payfast', $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
		 RETURNING created_at`,
		cardID, customerTrackingID, instrumentToken, brand, lastFour, expiryMonth, expiryYear, cardholderName, setAsDefault,
	).Scan(&createdAt)

	if err != nil {
		return nil, fmt.Errorf("failed to insert saved card: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit saved card: %w", err)
	}

	return &SavedCard{
		CardID:             cardID,
		CustomerTrackingID: customerTrackingID,
		Gateway:            "payfast",
		CardBrand:          brand,
		LastFour:           lastFour,
		ExpiryMonth:        expiryMonth,
		ExpiryYear:         expiryYear,
		CardholderName:     cardholderName,
		IsDefault:          setAsDefault,
		CreatedAt:          createdAt,
	}, nil
}

// ListCards returns all saved tokenized cards for a customer.
func (s *CardVaultService) ListCards(ctx context.Context, customerTrackingID string) ([]SavedCard, error) {
	rows, err := s.db.Query(ctx,
		`SELECT card_id, customer_tracking_id, gateway, card_brand, last_four, expiry_month, expiry_year, COALESCE(cardholder_name, ''), is_default, created_at
		 FROM customer_saved_cards
		 WHERE customer_tracking_id = $1
		 ORDER BY is_default DESC, created_at DESC`,
		customerTrackingID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query saved cards: %w", err)
	}
	defer rows.Close()

	var cards []SavedCard
	for rows.Next() {
		var c SavedCard
		if err := rows.Scan(
			&c.CardID, &c.CustomerTrackingID, &c.Gateway, &c.CardBrand,
			&c.LastFour, &c.ExpiryMonth, &c.ExpiryYear, &c.CardholderName,
			&c.IsDefault, &c.CreatedAt,
		); err == nil {
			cards = append(cards, c)
		}
	}
	return cards, nil
}

// GetCardInstrumentToken fetches the secure instrument token for a given card ID belonging to the user.
func (s *CardVaultService) GetCardInstrumentToken(ctx context.Context, customerTrackingID, cardID string) (string, error) {
	var token string
	err := s.db.QueryRow(ctx,
		`SELECT instrument_token FROM customer_saved_cards WHERE card_id = $1 AND customer_tracking_id = $2`,
		cardID, customerTrackingID,
	).Scan(&token)
	if err != nil {
		return "", errors.New("saved card not found or unauthorized")
	}
	return token, nil
}

// DeleteCard removes a saved card token for a customer.
func (s *CardVaultService) DeleteCard(ctx context.Context, customerTrackingID, cardID string) error {
	res, err := s.db.Exec(ctx,
		`DELETE FROM customer_saved_cards WHERE card_id = $1 AND customer_tracking_id = $2`,
		cardID, customerTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to delete saved card: %w", err)
	}
	if res.RowsAffected() == 0 {
		return errors.New("saved card not found")
	}
	return nil
}

// SetDefaultCard sets a specific saved card as the default.
func (s *CardVaultService) SetDefaultCard(ctx context.Context, customerTrackingID, cardID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	_, _ = tx.Exec(ctx, `UPDATE customer_saved_cards SET is_default = false, updated_at = NOW() WHERE customer_tracking_id = $1`, customerTrackingID)

	res, err := tx.Exec(ctx,
		`UPDATE customer_saved_cards SET is_default = true, updated_at = NOW() WHERE card_id = $1 AND customer_tracking_id = $2`,
		cardID, customerTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to set default card: %w", err)
	}
	if res.RowsAffected() == 0 {
		return errors.New("saved card not found")
	}

	return tx.Commit(ctx)
}
