package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/shared/database"
	"github.com/omnigo/backend/internal/vendorstore/models"
)

type VendorRepository struct {
	writer *pgxpool.Pool
	reader *pgxpool.Pool
}

func NewVendorRepository(writer, reader *pgxpool.Pool) *VendorRepository {
	return &VendorRepository{
		writer: writer,
		reader: reader,
	}
}

// CreateStore inserts a new store mapped to the actual public.stores table schema
func (r *VendorRepository) CreateStore(ctx context.Context, store *models.VendorStore) error {
	ok, err := database.Exists(ctx, r.writer, "SELECT 1 FROM users WHERE tracking_id = $1", store.VendorTrackingID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("user %s does not exist", store.VendorTrackingID)
	}

	query := `
		INSERT INTO stores (vendor_tracking_id, store_tracking_id, store_name, logo_url, banner_url, latitude, longitude, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
		RETURNING id, created_at
	`
	err = r.writer.QueryRow(ctx, query,
		store.VendorTrackingID,
		store.StoreTrackingID,
		store.StoreName,
		store.LogoURL,
		store.BannerURL,
		store.Latitude,
		store.Longitude,
	).Scan(&store.ID, &store.CreatedAt)

	return err
}

// GetStoreByTrackingID fetches a store using its store_tracking_id DSN column
func (r *VendorRepository) GetStoreByTrackingID(ctx context.Context, storeTrackingID string) (*models.VendorStore, error) {
	query := `
		SELECT id, vendor_tracking_id, store_tracking_id, store_name, COALESCE(logo_url, ''), COALESCE(banner_url, ''), COALESCE(latitude, 0.0), COALESCE(longitude, 0.0), created_at
		FROM stores
		WHERE store_tracking_id = $1
	`
	var store models.VendorStore
	err := r.reader.QueryRow(ctx, query, storeTrackingID).Scan(
		&store.ID, &store.VendorTrackingID, &store.StoreTrackingID, &store.StoreName,
		&store.LogoURL, &store.BannerURL, &store.Latitude, &store.Longitude, &store.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &store, nil
}

// GetStoreByVendorID fetches a store using the vendor_tracking_id column.
func (r *VendorRepository) GetStoreByVendorID(ctx context.Context, vendorTrackingID string) (*models.VendorStore, error) {
	query := `
		SELECT id, vendor_tracking_id, store_tracking_id, store_name, COALESCE(logo_url, ''), COALESCE(banner_url, ''), COALESCE(latitude, 0.0), COALESCE(longitude, 0.0), created_at
		FROM stores
		WHERE vendor_tracking_id = $1
		LIMIT 1
	`
	var store models.VendorStore
	err := r.reader.QueryRow(ctx, query, vendorTrackingID).Scan(
		&store.ID, &store.VendorTrackingID, &store.StoreTrackingID, &store.StoreName,
		&store.LogoURL, &store.BannerURL, &store.Latitude, &store.Longitude, &store.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &store, nil
}

// ListStores fetches a paginated list of public stores.
func (r *VendorRepository) ListStores(ctx context.Context, limit, offset int) ([]models.VendorStore, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT id, vendor_tracking_id, store_tracking_id, store_name, COALESCE(logo_url, ''), COALESCE(banner_url, ''), COALESCE(latitude, 0.0), COALESCE(longitude, 0.0), created_at
		FROM stores
		ORDER BY id DESC
		LIMIT $1 OFFSET $2
	`
	rows, err := r.reader.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stores := make([]models.VendorStore, 0)
	for rows.Next() {
		var store models.VendorStore
		if err := rows.Scan(
			&store.ID, &store.VendorTrackingID, &store.StoreTrackingID, &store.StoreName,
			&store.LogoURL, &store.BannerURL, &store.Latitude, &store.Longitude, &store.CreatedAt,
		); err != nil {
			return nil, err
		}
		stores = append(stores, store)
	}
	return stores, nil
}

// GetVendorMetricsSecure queries order statistics with COALESCE locks to prevent database NULL crashes
//
// Status literals are lowercase to match the canonical `orders.status`
// enum installed by migration 0015. The previous uppercase values
// (`COMPLETED`, `PENDING`, `CANCELLED`) returned zero rows because no
// rows ever carry those values any more.
func (r *VendorRepository) GetVendorMetricsSecure(ctx context.Context, vendorTrackingID string) (float64, int64, int64, int64, float64, float64, error) {
	query := `
		SELECT 
			COALESCE(SUM(total_amount), 0.00) AS total_revenue,
			COALESCE(COUNT(id) FILTER (WHERE status = 'delivered' OR status = 'completed'), 0) AS completed_orders,
			COALESCE(COUNT(id) FILTER (WHERE status = 'pending' OR status = 'accepted'), 0) AS pending_orders,
			COALESCE(COUNT(id) FILTER (WHERE status = 'cancelled'), 0) AS cancelled_orders,
			COALESCE(SUM(CASE WHEN created_at >= NOW() - INTERVAL '7 days' AND (status = 'delivered' OR status = 'completed') THEN total_amount ELSE 0 END), 0.00) AS current_week_revenue,
			COALESCE(SUM(CASE WHEN created_at >= NOW() - INTERVAL '14 days' AND created_at < NOW() - INTERVAL '7 days' AND (status = 'delivered' OR status = 'completed') THEN total_amount ELSE 0 END), 0.00) AS previous_week_revenue
		FROM orders
		WHERE vendor_tracking_id = $1
	`
	var totalRevenue float64
	var completed, pending, cancelled int64
	var currentWeekRev, prevWeekRev float64

	err := r.reader.QueryRow(ctx, query, vendorTrackingID).Scan(
		&totalRevenue,
		&completed,
		&pending,
		&cancelled,
		&currentWeekRev,
		&prevWeekRev,
	)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}

	return totalRevenue, completed, pending, cancelled, currentWeekRev, prevWeekRev, nil
}

// GetVendorDailyTrends aggregates daily sales over the last 7 days for sparkline display
func (r *VendorRepository) GetVendorDailyTrends(ctx context.Context, vendorTrackingID string) ([]models.DailyTrend, error) {
	query := `
		SELECT 
			TO_CHAR(created_at, 'YYYY-MM-DD') AS order_date,
			COALESCE(SUM(total_amount), 0.00) AS daily_revenue
		FROM orders
		WHERE vendor_tracking_id = $1 AND (status = 'delivered' OR status = 'completed') AND created_at >= NOW() - INTERVAL '7 days'
		GROUP BY TO_CHAR(created_at, 'YYYY-MM-DD')
		ORDER BY order_date ASC
	`
	rows, err := r.reader.Query(ctx, query, vendorTrackingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	trends := make([]models.DailyTrend, 0)
	for rows.Next() {
		var t models.DailyTrend
		if err := rows.Scan(&t.Date, &t.Revenue); err != nil {
			return nil, err
		}
		trends = append(trends, t)
	}

	return trends, nil
}

// GetVendorProductStats retrieves total catalog count and active (in stock) listings count
func (r *VendorRepository) GetVendorProductStats(ctx context.Context, vendorTrackingID string) (int64, int64, error) {
	query := `
		SELECT 
			COALESCE(COUNT(id), 0) AS total_products,
			COALESCE(COUNT(id) FILTER (WHERE stock > 0), 0) AS active_products
		FROM products
		WHERE vendor_tracking_id = $1
	`
	var total, active int64
	err := r.reader.QueryRow(ctx, query, vendorTrackingID).Scan(&total, &active)
	if err != nil {
		return 0, 0, err
	}
	return total, active, nil
}
