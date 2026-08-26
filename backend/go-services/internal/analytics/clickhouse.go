package analytics

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// AnalyticsService wraps the ClickHouse client
type AnalyticsService struct {
	conn driver.Conn
}

// NewAnalyticsService initializes a connection to ClickHouse.
// Auth is read from CLICKHOUSE_USER / CLICKHOUSE_PASSWORD env vars
// (defaults to default/empty for dev). Addresses must be provided by caller.
func NewAnalyticsService(addresses []string) (*AnalyticsService, error) {
	if len(addresses) == 0 {
		return nil, fmt.Errorf("no ClickHouse addresses provided")
	}

	chUser := os.Getenv("CLICKHOUSE_USER")
	if chUser == "" {
		chUser = "default"
	}
	chPass := os.Getenv("CLICKHOUSE_PASSWORD")
	chDB := os.Getenv("CLICKHOUSE_DATABASE")
	if chDB == "" {
		chDB = "default"
	}

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: addresses,
		Auth: clickhouse.Auth{
			Database: chDB,
			Username: chUser,
			Password: chPass,
		},
		DialTimeout:     5 * time.Second,
		MaxOpenConns:    10,
		MaxIdleConns:    5,
		ConnMaxLifetime: time.Hour,
	})
	if err != nil {
		return nil, fmt.Errorf("clickhouse open: %w", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("clickhouse ping: %w", err)
	}

	svc := &AnalyticsService{conn: conn}
	if err := svc.initSchema(ctx); err != nil {
		return nil, err
	}

	return svc, nil
}

// initSchema creates the necessary tables in ClickHouse for analytics
func (s *AnalyticsService) initSchema(ctx context.Context) error {
	// Table for Order Events (Heatmap analysis, etc.)
	orderEventsSchema := `
		CREATE TABLE IF NOT EXISTS order_events (
			order_id String,
			user_tracking_id String,
			vendor_store_tracking_id String,
			total_amount Float64,
			dropoff_lat Float64,
			dropoff_lng Float64,
			timestamp DateTime,
			created_at DateTime DEFAULT now()
		) ENGINE = MergeTree()
		ORDER BY (timestamp, order_id)
	`
	if err := s.conn.Exec(ctx, orderEventsSchema); err != nil {
		return fmt.Errorf("create order_events table: %w", err)
	}

	log.Println("[Analytics] ClickHouse schemas initialized")
	return nil
}

// InsertOrderEvent inserts a new order event directly into ClickHouse
func (s *AnalyticsService) InsertOrderEvent(ctx context.Context, orderID, userID, vendorID string, amount, lat, lng float64, ts int64) error {
	query := `
		INSERT INTO order_events 
		(order_id, user_tracking_id, vendor_store_tracking_id, total_amount, dropoff_lat, dropoff_lng, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	eventTime := time.UnixMilli(ts)

	err := s.conn.Exec(ctx, query, orderID, userID, vendorID, amount, lat, lng, eventTime)
	if err != nil {
		return fmt.Errorf("failed to insert order event: %w", err)
	}
	return nil
}

// GetRiderDemandHeatmap returns aggregated order counts grouped by H3-like geohash or grid.
// Here we just use ClickHouse geospatial or rounding for a grid heatmap.
func (s *AnalyticsService) GetRiderDemandHeatmap(ctx context.Context, minLat, maxLat, minLng, maxLng float64) ([]map[string]interface{}, error) {
	// Group orders into a ~1km grid by rounding lat/lng
	query := `
		SELECT 
			round(dropoff_lat, 2) AS lat_grid, 
			round(dropoff_lng, 2) AS lng_grid, 
			count() as order_count
		FROM order_events
		WHERE dropoff_lat BETWEEN ? AND ?
		  AND dropoff_lng BETWEEN ? AND ?
		  AND timestamp >= now() - INTERVAL 1 HOUR
		GROUP BY lat_grid, lng_grid
		ORDER BY order_count DESC
	`
	rows, err := s.conn.Query(ctx, query, minLat, maxLat, minLng, maxLng)
	if err != nil {
		return nil, fmt.Errorf("failed to query heatmap: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var latGrid, lngGrid float64
		var count uint64
		if err := rows.Scan(&latGrid, &lngGrid, &count); err != nil {
			return nil, err
		}
		results = append(results, map[string]interface{}{
			"lat":   latGrid,
			"lng":   lngGrid,
			"count": count,
		})
	}
	// LOW-02: surface iteration-level errors (network resets mid-scan etc.)
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("heatmap row iteration failed: %w", err)
	}
	return results, nil
}
