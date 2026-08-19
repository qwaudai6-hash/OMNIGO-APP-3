package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/shared/database"
)

type WishlistRepository struct {
	writer *pgxpool.Pool
	reader *pgxpool.Pool
}

func NewWishlistRepository(writer, reader *pgxpool.Pool) *WishlistRepository {
	return &WishlistRepository{writer: writer, reader: reader}
}

// ToggleFavorite adds a favorite if it doesn't exist, removes it if it does.
// Returns true if the product is now favorited, false if it was removed.
func (r *WishlistRepository) ToggleFavorite(ctx context.Context, customerTrackingID, productTrackingID string) (bool, error) {
	// The favorites table uses `user_tracking_id` (per migration 0001_init.sql),
	// not `customer_tracking_id`. The Go code historically used the wrong
	// column name, so all wishlist writes silently no-op'd.
	var exists bool
	checkQuery := `SELECT EXISTS(SELECT 1 FROM favorites WHERE user_tracking_id = $1 AND product_tracking_id = $2)`
	err := r.reader.QueryRow(ctx, checkQuery, customerTrackingID, productTrackingID).Scan(&exists)
	if err != nil {
		return false, err
	}

	if exists {
		// Remove
		_, err := r.writer.Exec(ctx, `DELETE FROM favorites WHERE user_tracking_id = $1 AND product_tracking_id = $2`, customerTrackingID, productTrackingID)
		return false, err
	}

	// Validate parent references before insert
	checks := []struct {
		id    string
		label string
		query string
	}{
		{customerTrackingID, "user", "SELECT 1 FROM users WHERE tracking_id = $1"},
		{productTrackingID, "product", "SELECT 1 FROM products WHERE product_tracking_id = $1"},
	}
	for _, c := range checks {
		ok, err := database.Exists(ctx, r.writer, c.query, c.id)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, fmt.Errorf("%s %s does not exist", c.label, c.id)
		}
	}

	// Add
	_, err = r.writer.Exec(ctx, `INSERT INTO favorites (user_tracking_id, product_tracking_id) VALUES ($1, $2)`, customerTrackingID, productTrackingID)
	return true, err
}

// ListFavorites returns all favorited product tracking IDs for a customer.
func (r *WishlistRepository) ListFavorites(ctx context.Context, customerTrackingID string) ([]string, error) {
	rows, err := r.reader.Query(ctx, `SELECT product_tracking_id FROM favorites WHERE user_tracking_id = $1 ORDER BY created_at DESC`, customerTrackingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var productIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		productIDs = append(productIDs, id)
	}
	return productIDs, rows.Err()
}

// IsFavorite checks if a specific product is favorited by a customer.
func (r *WishlistRepository) IsFavorite(ctx context.Context, customerTrackingID, productTrackingID string) (bool, error) {
	var exists bool
	err := r.reader.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM favorites WHERE user_tracking_id = $1 AND product_tracking_id = $2)`, customerTrackingID, productTrackingID).Scan(&exists)
	return exists, err
}

// RemoveFavorite deletes a favorite. Returns error if not found.
func (r *WishlistRepository) RemoveFavorite(ctx context.Context, customerTrackingID, productTrackingID string) error {
	res, err := r.writer.Exec(ctx, `DELETE FROM favorites WHERE user_tracking_id = $1 AND product_tracking_id = $2`, customerTrackingID, productTrackingID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return errors.New("favorite not found")
	}
	return nil
}
