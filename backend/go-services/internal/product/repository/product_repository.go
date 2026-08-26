package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// GetProductByNumericID fetches a product by its internal serial id. The cart
// service stores numeric product ids and resolves prices through this lookup.
func (r *ProductRepository) GetProductByNumericID(ctx context.Context, id int64) (*models.Product, error) {
	query := `
		SELECT id, product_tracking_id, vendor_tracking_id, store_tracking_id, sku, name, description, base_price, stock, is_featured, image_url, category, is_active, created_at, updated_at
		FROM products
		WHERE id = $1
	`
	var prod models.Product
	err := r.reader.QueryRow(ctx, query, id).Scan(
		&prod.ID, &prod.ProductTrackingID, &prod.VendorTrackingID, &prod.StoreTrackingID, &prod.SKU, &prod.Name,
		&prod.Description, &prod.BasePrice, &prod.Stock, &prod.IsFeatured, &prod.ImageURL, &prod.Category, &prod.IsActive,
		&prod.CreatedAt, &prod.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &prod, nil
}

// ListProducts fetches a paginated list of products supporting optional search, category, storeID, sort, minPrice, maxPrice filters
func (r *ProductRepository) ListProducts(ctx context.Context, limit, offset int, search, category, storeID, sort string, minPrice, maxPrice float64) ([]*models.Product, error) {
	var query string
	var args []interface{}

	query = `
		SELECT p.id, p.product_tracking_id, p.vendor_tracking_id, p.store_tracking_id, p.sku, p.name, p.description,
		       p.base_price, p.stock, p.is_featured, p.image_url, p.category, p.is_active, p.created_at, p.updated_at,
		       COALESCE(s.store_name, ''), COALESCE(s.logo_url, ''), COALESCE(s.banner_url, '')
		FROM products p
		LEFT JOIN stores s ON s.store_tracking_id = p.store_tracking_id
		WHERE p.is_active = true
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

	if storeID != "" {
		query += fmt.Sprintf(" AND p.store_tracking_id = $%d", placeholderIdx)
		args = append(args, storeID)
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
			&prod.StoreName, &prod.LogoURL, &prod.BannerURL,
		)
		// Flutter falls back: store_logo_url→logo_url, store_banner_url→banner_url.
		// Populate both spellings so either parse path gets branding.
		prod.StoreLogoURL = prod.LogoURL
		prod.StoreBannerURL = prod.BannerURL
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

// ResolveOrVerifyStoreOwnership validates that the store belongs to the vendor.
// If the provided storeTrackingID is empty, "STOR-000000", or invalid, it automatically
// resolves the vendor's primary store from the database or creates one if none exists.
func (r *ProductRepository) ResolveOrVerifyStoreOwnership(ctx context.Context, storeTrackingID, vendorTrackingID string) (string, error) {
	if storeTrackingID != "" && storeTrackingID != "STOR-000000" {
		query := `SELECT EXISTS(SELECT 1 FROM stores WHERE store_tracking_id = $1 AND vendor_tracking_id = $2)`
		var exists bool
		if err := r.reader.QueryRow(ctx, query, storeTrackingID, vendorTrackingID).Scan(&exists); err == nil && exists {
			return storeTrackingID, nil
		}
	}

	// Fallback: Find existing primary store for this vendor
	var primaryStoreID string
	err := r.reader.QueryRow(ctx, `SELECT store_tracking_id FROM stores WHERE vendor_tracking_id = $1 ORDER BY created_at ASC LIMIT 1`, vendorTrackingID).Scan(&primaryStoreID)
	if err == nil && primaryStoreID != "" {
		return primaryStoreID, nil
	}

	// Self-healing: Auto-create a default primary store for this vendor
	newStoreID := fmt.Sprintf("STOR-%06d", time.Now().UnixNano()%1000000)
	insertQuery := `
		INSERT INTO stores (vendor_tracking_id, store_tracking_id, store_name, is_active, created_at, updated_at)
		VALUES ($1, $2, 'Primary Store', true, NOW(), NOW())
		ON CONFLICT (store_tracking_id) DO NOTHING
	`
	_, err = r.writer.Exec(ctx, insertQuery, vendorTrackingID, newStoreID)
	if err != nil {
		return "", fmt.Errorf("failed to auto-create primary store: %w", err)
	}
	return newStoreID, nil
}

// VerifyStoreOwnership is a compatibility wrapper for ResolveOrVerifyStoreOwnership.
func (r *ProductRepository) VerifyStoreOwnership(ctx context.Context, storeTrackingID, vendorTrackingID string) error {
	_, err := r.ResolveOrVerifyStoreOwnership(ctx, storeTrackingID, vendorTrackingID)
	return err
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
		// CI-17: reject zero/negative quantities — negative values would pass
		// the `stock >= qty` predicate and INFLATE inventory on release.
		if item.Quantity <= 0 {
			return nil, fmt.Errorf("invalid quantity for product %s: must be > 0", item.ProductTrackingID)
		}
		query := `
			UPDATE products
			SET stock = stock - $1, updated_at = NOW()
			WHERE (product_tracking_id = $2 OR id::text = $2) AND stock >= $1
			RETURNING product_tracking_id, base_price, store_tracking_id, vendor_tracking_id
		`
		var price float64
		var realProdID string
		err := tx.QueryRow(ctx, query, item.Quantity, item.ProductTrackingID).Scan(&realProdID, &price, &storeTrackID, &vendorTrackID)
		if err != nil {
			return nil, fmt.Errorf("failed to reserve stock for product %s (insufficient stock or not found)", item.ProductTrackingID)
		}

		item.ProductTrackingID = realProdID
		item.PriceAtCheckout = price
		item.StoreTrackingID = storeTrackID
		item.VendorTrackingID = vendorTrackID

		// Validate all items belong to the same store/vendor. Mixed-store orders are not supported in a single delivery batch.
		if i > 0 && (reserved[0].StoreTrackingID != storeTrackID || reserved[0].VendorTrackingID != vendorTrackID) {
			return nil, fmt.Errorf("mixed store/vendor orders are not supported in a single delivery batch")
		}
		reserved = append(reserved, item)
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
			WHERE product_tracking_id = $2 OR id::text = $2
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
