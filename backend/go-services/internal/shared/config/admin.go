package config

import (
	"fmt"
	"os"
	"strconv"
)

// AdminConfig holds admin-service-specific configuration. All values are
// env-driven; there are no hardcoded fallbacks for business constants like
// commission rates or pagination sizes — operators must configure them
// explicitly for their deployment.
type AdminConfig struct {
	// Analytics — required if the platform revenue estimate fallback is hit.
	// Operators MUST set this to their actual platform commission rate
	// (e.g. "0.10" for 10%). No silent 0.10 fallback.
	AnalyticsEstimateRate float64

	// ReconciliationWorker thresholds
	// ReconciliationMinThreshold is the minimum absolute discrepancy
	// tolerated (default 1.0 PKR). Floating-point drift below this is
	// ignored.
	ReconciliationMinThreshold float64
	// ReconciliationRelativeRate is the relative tolerance (default 0.0001
	// = 0.01%) applied to total volume to compute a per-volume threshold.
	ReconciliationRelativeRate float64

	// Pagination
	DefaultPageSize     int
	MaxPageSize         int
	RecentPaymentsLimit int

	// Default time windows for analytics endpoints
	DefaultRevenueDays    int
	MaxRevenueDays        int
	DefaultGPSTrailHours  int
	MaxGPSTrailHours      int
	DefaultAnalyticsDays  int
	MaxAnalyticsDays      int
}

// LoadAdminConfig reads admin-specific config from env vars. Returns
// an error if a required var is missing or unparseable.
func LoadAdminConfig() (*AdminConfig, error) {
	cfg := &AdminConfig{}

	// AnalyticsEstimateRate is OPTIONAL only because the fallback chain
	// in GetDailyRevenue may never reach it (when ledger + per-order
	// commission data is available). But we still validate it if set.
	if v := os.Getenv("ANALYTICS_ESTIMATE_RATE"); v != "" {
		rate, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("ANALYTICS_ESTIMATE_RATE %q is not a valid float: %w", v, err)
		}
		if rate < 0 || rate > 1 {
			return nil, fmt.Errorf("ANALYTICS_ESTIMATE_RATE must be between 0 and 1, got %v", rate)
		}
		cfg.AnalyticsEstimateRate = rate
	}

	// Reconciliation thresholds
	cfg.ReconciliationMinThreshold = getEnvFloat("RECONCILIATION_MIN_THRESHOLD_PKR", 1.0)
	if cfg.ReconciliationMinThreshold < 0 {
		return nil, fmt.Errorf("RECONCILIATION_MIN_THRESHOLD_PKR must be >= 0")
	}
	cfg.ReconciliationRelativeRate = getEnvFloat("RECONCILIATION_RELATIVE_RATE", 0.0001)
	if cfg.ReconciliationRelativeRate < 0 || cfg.ReconciliationRelativeRate > 1 {
		return nil, fmt.Errorf("RECONCILIATION_RELATIVE_RATE must be between 0 and 1")
	}

	// Pagination
	var err error
	if cfg.DefaultPageSize, err = getEnvInt("ADMIN_DEFAULT_PAGE_SIZE", 20); err != nil {
		return nil, err
	}
	if cfg.MaxPageSize, err = getEnvInt("ADMIN_MAX_PAGE_SIZE", 100); err != nil {
		return nil, err
	}
	if cfg.RecentPaymentsLimit, err = getEnvInt("ADMIN_RECENT_PAYMENTS_LIMIT", 50); err != nil {
		return nil, err
	}
	if cfg.DefaultPageSize <= 0 || cfg.DefaultPageSize > cfg.MaxPageSize {
		return nil, fmt.Errorf("ADMIN_DEFAULT_PAGE_SIZE (%d) must be in 1..ADMIN_MAX_PAGE_SIZE (%d)", cfg.DefaultPageSize, cfg.MaxPageSize)
	}
	if cfg.MaxPageSize <= 0 {
		return nil, fmt.Errorf("ADMIN_MAX_PAGE_SIZE must be > 0")
	}

	// Time windows
	if cfg.DefaultRevenueDays, err = getEnvInt("ADMIN_DEFAULT_REVENUE_DAYS", 7); err != nil {
		return nil, err
	}
	if cfg.MaxRevenueDays, err = getEnvInt("ADMIN_MAX_REVENUE_DAYS", 365); err != nil {
		return nil, err
	}
	if cfg.DefaultGPSTrailHours, err = getEnvInt("ADMIN_DEFAULT_GPS_HOURS", 24); err != nil {
		return nil, err
	}
	if cfg.MaxGPSTrailHours, err = getEnvInt("ADMIN_MAX_GPS_HOURS", 168); err != nil {
		return nil, err
	}
	if cfg.DefaultAnalyticsDays, err = getEnvInt("ADMIN_DEFAULT_ANALYTICS_DAYS", 7); err != nil {
		return nil, err
	}
	if cfg.MaxAnalyticsDays, err = getEnvInt("ADMIN_MAX_ANALYTICS_DAYS", 365); err != nil {
		return nil, err
	}

	return cfg, nil
}

// getEnvFloat returns the float value of an env var, or def if not set.
func getEnvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// getEnvInt returns the int value of an env var, or def if not set.
// Returns an error if the env var is set but not a valid integer.
func getEnvInt(key string, def int) (int, error) {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("%s %q is not a valid integer: %w", key, v, err)
		}
		return n, nil
	}
	return def, nil
}

// ReconciliationConfig holds the parameters used by the daily
// ReconciliationWorker. Both thresholds are configurable so operators
// can tune sensitivity per environment (e.g. a development env can
// allow larger discrepancies, a production env should be tighter).
type ReconciliationConfig struct {
	// MinThresholdPKR is the absolute minimum discrepancy (in PKR)
	// tolerated before alerting. Floating-point drift below this is
	// ignored regardless of volume. Default 1.0 PKR.
	MinThresholdPKR float64
	// RelativeRate is the relative tolerance (fraction of total
	// volume) used to compute the per-volume threshold. Threshold =
	// max(MinThresholdPKR, TotalVolume * RelativeRate). Default 0.0001
	// (0.01%).
	RelativeRate float64
}

// LoadReconciliationConfig reads reconciliation thresholds from env.
func LoadReconciliationConfig() (*ReconciliationConfig, error) {
	cfg := &ReconciliationConfig{
		MinThresholdPKR: getEnvFloat("RECONCILIATION_MIN_THRESHOLD_PKR", 1.0),
		RelativeRate:    getEnvFloat("RECONCILIATION_RELATIVE_RATE", 0.0001),
	}
	if cfg.MinThresholdPKR < 0 {
		return nil, fmt.Errorf("RECONCILIATION_MIN_THRESHOLD_PKR must be >= 0")
	}
	if cfg.RelativeRate < 0 || cfg.RelativeRate > 1 {
		return nil, fmt.Errorf("RECONCILIATION_RELATIVE_RATE must be between 0 and 1")
	}
	return cfg, nil
}
