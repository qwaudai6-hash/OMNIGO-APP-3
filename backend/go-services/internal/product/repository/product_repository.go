package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/product/models"
)

type ProductRepository struct {
	writer *pgxpool.Pool
	reader *pgxpool.Pool
}

func NewProductRepository(writer, reader *pgxpool.Pool) *ProductRepository {
	return &ProductRepository{
		writer: writer,
		reader: reader,
	}
}

// CreateProduct inserts a new product and returns the generated IDs
func (r *ProductRepository) CreateProduct(ctx context.Context, prod *models.Product) error {
	query := `
		INSERT INTO products (product_tracking_id, vendor_tracking_id, store_tracking_id, sku, name, description, base_price, stock, is_featured, image_url, category, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, created_at, updated_at
	`
	err := r.writer.QueryRow(ctx, query,
		prod.ProductTrackingID,
		prod.VendorTrackingID,
		prod.StoreTrackingID,
		prod.SKU,
		prod.Name,
		prod.Description,
		prod.BasePrice,
		prod.Stock,
		prod.IsFeatured,
		prod.ImageURL,
		prod.Category,
		prod.IsActive,
	).Scan(&prod.ID, &prod.CreatedAt, &prod.UpdatedAt)

	return err
}

// GetProductByTrackingID fetches a product using its UTID
func (r *ProductRepository) GetProductByTrackingID(ctx context.Context, trackingID string) (*models.Product, error) {
	query := `
		SELECT id, product_tracking_id, vendor_tracking_id, store_tracking_id, sku, name, description, base_price, stock, is_featured, image_url, category, is_active, created_at, updated_at
		FROM products
		WHERE product_tracking_id = $1
	`
	var prod models.Product
	err := r.reader.QueryRow(ctx, query, trackingID).Scan(
		&prod.ID, &prod.ProductTrackingID, &prod.VendorTrackingID, &prod.StoreTrackingID, &prod.SKU, &prod.Name,
		&prod.Description, &prod.BasePrice, &prod.Stock, &prod.IsFeatured, &prod.ImageURL, &prod.Category, &prod.IsActive,
		&prod.CreatedAt, &prod.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &prod, nil
}

// ListProducts fetches a paginated list of products supporting optional search, category, sort, minPrice, maxPrice filters
func (r *ProductRepository) ListProducts(ctx context.Context, limit, offset int, search, category, sort string, minPrice, maxPrice float64) ([]*models.Product, error) {
	var query string
	var args []interface{}

	query = `
		SELECT id, product_tracking_id, vendor_tracking_id, store_tracking_id, sku, name, description, base_price, stock, is_featured, image_url, category, is_active, created_at, updated_at
		FROM products
		WHERE is_active = true
	`
	placeholderIdx := 1

	if search != "" {
		query += fmt.Sprintf(" AND (name ILIKE $%d OR description ILIKE $%d)", placeholderIdx, placeholderIdx)
		args = append(args, "%"+search+"%")
		placeholderIdx++
	}

	if category != "" {
		query += fmt.Sprintf(" AND category = $%d", placeholderIdx)
		args = append(args, category)
		placeholderIdx++
	}

	if minPrice > 0 {
		query += fmt.Sprintf(" AND base_price >= $%d", placeholderIdx)
		args = append(args, minPrice)
		placeholderIdx++
	}

	if maxPrice > 0 {
		query += fmt.Sprintf(" AND base_price <= $%d", placeholderIdx)
		args = append(args, maxPrice)
		placeholderIdx++
	}

	switch sort {
	case "price_asc":
		query += " ORDER BY base_price ASC"
	case "price_desc":
		query += " ORDER BY base_price DESC"
	case "newest":
		query += " ORDER BY created_at DESC"
	case "rating":
		query += " ORDER BY base_price DESC" // Default fallback for rating
	default:
		query += " ORDER BY created_at DESC"
	}

	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", placeholderIdx, placeholderIdx+1)
	args = append(args, limit, offset)

	rows, err := r.reader.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var products []*models.Product
	for rows.Next() {
		var prod models.Product
		err := rows.Scan(
			&prod.ID, &prod.ProductTrackingID, &prod.VendorTrackingID, &prod.StoreTrackingID, &prod.SKU, &prod.Name,
			&prod.Description, &prod.BasePrice, &prod.Stock, &prod.IsFeatured, &prod.ImageURL, &prod.Category, &prod.IsActive,
			&prod.CreatedAt, &prod.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		products = append(products, &prod)
	}
	return products, rows.Err()
}

// GetProductsByTrackingIDs fetches multiple products in a single bulk query (used for AI recommendations resolution)
func (r *ProductRepository) GetProductsByTrackingIDs(ctx context.Context, trackingIDs []string) ([]*models.Product, error) {
	if len(trackingIDs) == 0 {
		return []*models.Product{}, nil
	}

	query := `
		SELECT id, product_tracking_id, vendor_tracking_id, store_tracking_id, sku, name, description, base_price, stock, is_featured, image_url, category, is_active, created_at, updated_at
		FROM products
		WHERE product_tracking_id = ANY($1) AND is_active = true
	`

	rows, err := r.reader.Query(ctx, query, trackingIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var products []*models.Product
	for rows.Next() {
		var prod models.Product
		err := rows.Scan(
			&prod.ID, &prod.ProductTrackingID, &prod.VendorTrackingID, &prod.StoreTrackingID, &prod.SKU, &prod.Name,
			&prod.Description, &prod.BasePrice, &prod.Stock, &prod.IsFeatured, &prod.ImageURL, &prod.Category, &prod.IsActive,
			&prod.CreatedAt, &prod.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		products = append(products, &prod)
	}
	return products, rows.Err()
}

// UpdateProductStockSecure updates product stock only if owned by the authenticated vendor.
func (r *ProductRepository) UpdateProductStockSecure(ctx context.Context, productTrackingID string, stock int, vendorTrackingID string) error {
	query := `
		UPDATE products
		SET stock = $1, updated_at = NOW()
		WHERE product_tracking_id = $2 AND vendor_tracking_id = $3
	`
	res, err := r.writer.Exec(ctx, query, stock, productTrackingID, vendorTrackingID)
	if err != nil {
		return err
	}

	if res.RowsAffected() == 0 {
		return errors.New("product not found or unauthorized vendor")
	}
	return nil
}

// DeleteProductSecure deletes a product only if owned by the authenticated vendor.
func (r *ProductRepository) DeleteProductSecure(ctx context.Context, productTrackingID string, vendorTrackingID string) error {
	query := `
		DELETE FROM products
		WHERE product_tracking_id = $1 AND vendor_tracking_id = $2
	`
	res, err := r.writer.Exec(ctx, query, productTrackingID, vendorTrackingID)
	if err != nil {
		return err
	}

	if res.RowsAffected() == 0 {
		return errors.New("product not found or unauthorized vendor")
	}
	return nil
}

// VerifyStoreOwnership returns nil if the given store_tracking_id belongs to
// the given vendor_tracking_id. Used by the AddProduct vendor endpoint to
// prevent cross-vendor catalog injection.
func (r *ProductRepository) VerifyStoreOwnership(ctx context.Context, storeTrackingID, vendorTrackingID string) error {
	query := `SELECT EXISTS(SELECT 1 FROM stores WHERE store_tracking_id = $1 AND vendor_tracking_id = $2)`
	var exists bool
	err := r.reader.QueryRow(ctx, query, storeTrackingID, vendorTrackingID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("store not found or not owned by vendor")
	}
	return nil
}

// UpdateProductFields performs a partial update of the mutable product columns.
// Only non-nil fields are written; nil fields are skipped (COALESCE pattern).
func (r *ProductRepository) UpdateProductFields(ctx context.Context, productTrackingID, vendorTrackingID string, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return errors.New("no fields provided for update")
	}

	// Build the SET clause dynamically with parameterized placeholders.
	setClauses := []string{}
	args := []interface{}{}
	placeholderIdx := 1

	for col, val := range updates {
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, placeholderIdx))
		args = append(args, val)
		placeholderIdx++
	}

	// Append ownership guard placeholders.
	args = append(args, productTrackingID, vendorTrackingID)

	query := fmt.Sprintf(`
		UPDATE products
		SET %s, updated_at = NOW()
		WHERE product_tracking_id = $%d AND vendor_tracking_id = $%d
	`, joinStrings(setClauses, ", "), placeholderIdx, placeholderIdx+1)

	res, err := r.writer.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return errors.New("product not found or unauthorized vendor")
	}
	return nil
}

// joinStrings is a tiny helper to avoid importing strings just for Join.
func joinStrings(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

// ListVendorProducts fetches a paginated list of products for a specific vendor
func (r *ProductRepository) ListVendorProducts(ctx context.Context, vendorTrackingID string, limit, offset int) ([]*models.Product, error) {
	query := `
		SELECT id, product_tracking_id, vendor_tracking_id, store_tracking_id, sku, name, description, base_price, stock, is_featured, image_url, category, is_active, created_at, updated_at
		FROM products
		WHERE vendor_tracking_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`

	rows, err := r.reader.Query(ctx, query, vendorTrackingID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var products []*models.Product
	for rows.Next() {
		var prod models.Product
		err := rows.Scan(
			&prod.ID, &prod.ProductTrackingID, &prod.VendorTrackingID, &prod.StoreTrackingID, &prod.SKU, &prod.Name,
			&prod.Description, &prod.BasePrice, &prod.Stock, &prod.IsFeatured, &prod.ImageURL, &prod.Category, &prod.IsActive,
			&prod.CreatedAt, &prod.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		products = append(products, &prod)
	}
	return products, rows.Err()
}

// CountVendorProducts returns the total number of products for a vendor
func (r *ProductRepository) CountVendorProducts(ctx context.Context, vendorTrackingID string) (int, error) {
	query := `SELECT COUNT(*) FROM products WHERE vendor_tracking_id = $1`
	var count int
	err := r.reader.QueryRow(ctx, query, vendorTrackingID).Scan(&count)
	return count, err
}

// ReserveStockResponse contains the reserved items plus store/vendor lineage.
type ReserveStockResponse struct {
	Items         []models.OrderItem `json:"items"`
	VendorTrackID string             `json:"vendor_tracking_id"`
	StoreTrackID  string             `json:"store_tracking_id"`
}

// ReserveStock atomically deducts stock for an order using CAS. Returns prices at checkout
// plus the vendor and store tracking IDs so the caller can write an order without a stores lookup.
func (r *ProductRepository) ReserveStock(ctx context.Context, items []models.OrderItem) (*ReserveStockResponse, error) {
	tx, err := r.writer.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var reserved []models.OrderItem
	var storeTrackID, vendorTrackID string

	for i, item := range items {
		query := `
			UPDATE products
			SET stock = stock - $1, updated_at = NOW()
			WHERE product_tracking_id = $2 AND stock >= $1
			RETURNING base_price, store_tracking_id, vendor_tracking_id
		`
		var price float64
		err := tx.QueryRow(ctx, query, item.Quantity, item.ProductTrackingID).Scan(&price, &storeTrackID, &vendorTrackID)
		if err != nil {
			return nil, fmt.Errorf("failed to reserve stock for product %s (insufficient stock or not found)", item.ProductTrackingID)
		}

		item.PriceAtCheckout = price
		reserved = append(reserved, item)

		// Validate all items belong to the same store/vendor. Mixed-store orders are not supported in this phase.
		if i > 0 && (reserved[i].StoreTrackingID != storeTrackID || reserved[i].VendorTrackingID != vendorTrackID) {
			// ponytail: mixed-store cart support requires separate reservation/insert strategy.
			return nil, fmt.Errorf("mixed store/vendor orders are not supported")
		}
		reserved[i].StoreTrackingID = storeTrackID
		reserved[i].VendorTrackingID = vendorTrackID
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &ReserveStockResponse{
		Items:         reserved,
		VendorTrackID: vendorTrackID,
		StoreTrackID:  storeTrackID,
	}, nil
}

// ReleaseStock adds back stock after a failed order transaction
func (r *ProductRepository) ReleaseStock(ctx context.Context, items []models.OrderItem) error {
	tx, err := r.writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, item := range items {
		query := `
			UPDATE products
			SET stock = stock + $1, updated_at = NOW()
			WHERE product_tracking_id = $2
		`
		_, err := tx.Exec(ctx, query, item.Quantity, item.ProductTrackingID)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// GetVendorVerificationStatus queries the users table to check if the vendor is verified.
func (r *ProductRepository) GetVendorVerificationStatus(ctx context.Context, vendorTrackingID string) (bool, error) {
	var isVerified bool
	query := `SELECT is_verified FROM users WHERE tracking_id = $1`
	err := r.reader.QueryRow(ctx, query, vendorTrackingID).Scan(&isVerified)
	if err != nil {
		return false, err
	}
	return isVerified, nil
}
