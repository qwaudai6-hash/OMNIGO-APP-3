package models

import (
	"time"
)

// VendorStore represents a storefront mapped to the active PostgreSQL "stores" table schema.
type VendorStore struct {
	ID               int64     `json:"id"`
	VendorTrackingID string    `json:"vendor_tracking_id"`
	StoreTrackingID  string    `json:"store_tracking_id"`
	StoreName        string    `json:"store_name"`
	LogoURL          string    `json:"logo_url"`
	BannerURL        string    `json:"banner_url"`
	Latitude         float64   `json:"latitude"`
	Longitude        float64   `json:"longitude"`
	CommissionRate   float64   `json:"commission_rate"`
	IsActive         bool      `json:"is_active"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// CreateStoreRequest is the payload for creating a new store.
type CreateStoreRequest struct {
	VendorTrackingID string  `json:"vendor_tracking_id"`
	StoreName        string  `json:"store_name" binding:"required"`
	LogoURL          string  `json:"logo_url"`
	BannerURL        string  `json:"banner_url"`
	Latitude         float64 `json:"latitude"`
	Longitude        float64 `json:"longitude"`
}

type DailyTrend struct {
	Date    string  `json:"date"`
	Revenue float64 `json:"revenue"`
}

type VendorMetricsResponse struct {
	TotalRevenue        float64      `json:"total_revenue"`
	CompletedOrders     int64        `json:"completed_orders"`
	PendingOrders       int64        `json:"pending_orders"`
	CancelledOrders     int64        `json:"cancelled_orders"`
	TotalProducts       int64        `json:"total_products"`
	ActiveProducts      int64        `json:"active_products"`
	CurrentWeekRevenue  float64      `json:"current_week_revenue"`
	PreviousWeekRevenue float64      `json:"previous_week_revenue"`
	WowGrowthPercentage float64      `json:"wow_growth_percentage"`
	DailyTrends         []DailyTrend `json:"daily_trends"`
}
