package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/order/models"
	"github.com/omnigo/backend/internal/shared/database"
)

type CartRepository struct {
	db *pgxpool.Pool
}

func NewCartRepository(db *pgxpool.Pool) *CartRepository {
	return &CartRepository{db: db}
}

// GetCart fetches the cart and its items for a specific user
func (r *CartRepository) GetCart(ctx context.Context, userID string) (*models.Cart, error) {
	queryCart := `SELECT id, user_id, store_id, created_at, updated_at FROM carts WHERE user_id = $1`
	var cart models.Cart
	err := r.db.QueryRow(ctx, queryCart, userID).Scan(&cart.ID, &cart.UserID, &cart.StoreID, &cart.CreatedAt, &cart.UpdatedAt)
	if err != nil {
		return nil, err
	}

	queryItems := `SELECT id, cart_id, product_id, quantity, price, created_at, updated_at FROM cart_items WHERE cart_id = $1`
	rows, err := r.db.Query(ctx, queryItems, cart.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var item models.CartItem
		if err := rows.Scan(&item.ID, &item.CartID, &item.ProductID, &item.Quantity, &item.Price, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		cart.Items = append(cart.Items, item)
	}

	return &cart, nil
}

// CreateCart creates a new cart for a user with a specific store_id (upsert style to avoid race conditions)
func (r *CartRepository) CreateCart(ctx context.Context, userID, storeID string) (*models.Cart, error) {
	query := `
		INSERT INTO carts (user_id, store_id, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (user_id) DO UPDATE SET updated_at = NOW()
		RETURNING id, created_at, updated_at
	`
	cart := &models.Cart{
		UserID:  userID,
		StoreID: storeID,
	}
	err := r.db.QueryRow(ctx, query, userID, storeID).Scan(&cart.ID, &cart.CreatedAt, &cart.UpdatedAt)
	return cart, err
}

// ClearCart deletes all items from a user's cart (and can optionally delete the cart itself)
func (r *CartRepository) ClearCart(ctx context.Context, userID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM carts WHERE user_id = $1`, userID) // cascade will delete items
	return err
}

// AddItem adds an item to the cart or updates its quantity if it already exists
func (r *CartRepository) AddItem(ctx context.Context, cartID int64, productID int64, quantity int, price float64) error {
	checks := []struct {
		id    int64
		label string
		query string
	}{
		{cartID, "cart", "SELECT 1 FROM carts WHERE id = $1"},
		{productID, "product", "SELECT 1 FROM products WHERE id = $1"},
	}
	for _, c := range checks {
		ok, err := database.Exists(ctx, r.db, c.query, c.id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s %d does not exist", c.label, c.id)
		}
	}

	query := `
		INSERT INTO cart_items (cart_id, product_id, quantity, price, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (cart_id, product_id)
		DO UPDATE SET quantity = cart_items.quantity + EXCLUDED.quantity, updated_at = NOW()
	`
	_, err := r.db.Exec(ctx, query, cartID, productID, quantity, price)
	return err
}

// UpdateItemQuantity updates the quantity of a specific cart item
func (r *CartRepository) UpdateItemQuantity(ctx context.Context, cartID int64, productID int64, quantity int) error {
	query := `UPDATE cart_items SET quantity = $1, updated_at = NOW() WHERE cart_id = $2 AND product_id = $3`
	_, err := r.db.Exec(ctx, query, quantity, cartID, productID)
	return err
}

// RemoveItem removes a specific item from the cart
func (r *CartRepository) RemoveItem(ctx context.Context, cartID int64, productID int64) error {
	query := `DELETE FROM cart_items WHERE cart_id = $1 AND product_id = $2`
	_, err := r.db.Exec(ctx, query, cartID, productID)
	return err
}
