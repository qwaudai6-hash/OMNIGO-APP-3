package config

import (
	"os"
	"testing"
)

func TestLoadAdminConfig_Defaults(t *testing.T) {
	// Clear any env vars from the test environment
	for _, k := range []string{
		"ANALYTICS_ESTIMATE_RATE",
		"RECONCILIATION_MIN_THRESHOLD_PKR",
		"RECONCILIATION_RELATIVE_RATE",
		"ADMIN_DEFAULT_PAGE_SIZE",
		"ADMIN_MAX_PAGE_SIZE",
		"ADMIN_RECENT_PAYMENTS_LIMIT",
		"ADMIN_DEFAULT_REVENUE_DAYS",
		"ADMIN_MAX_REVENUE_DAYS",
		"ADMIN_DEFAULT_GPS_HOURS",
		"ADMIN_MAX_GPS_HOURS",
		"ADMIN_DEFAULT_ANALYTICS_DAYS",
		"ADMIN_MAX_ANALYTICS_DAYS",
	} {
		os.Unsetenv(k)
	}

	cfg, err := LoadAdminConfig()
	if err != nil {
		t.Fatalf("LoadAdminConfig failed with empty env: %v", err)
	}
	// AnalyticsEstimateRate is 0 by default (no silent 10% fallback).
	if cfg.AnalyticsEstimateRate != 0 {
		t.Errorf("AnalyticsEstimateRate = %v, want 0 (no silent fallback)", cfg.AnalyticsEstimateRate)
	}
	// Reconciliation thresholds have sensible defaults
	if cfg.ReconciliationMinThreshold != 1.0 {
		t.Errorf("ReconciliationMinThreshold = %v, want 1.0", cfg.ReconciliationMinThreshold)
	}
	if cfg.ReconciliationRelativeRate != 0.0001 {
		t.Errorf("ReconciliationRelativeRate = %v, want 0.0001", cfg.ReconciliationRelativeRate)
	}
	// Pagination defaults
	if cfg.DefaultPageSize != 20 {
		t.Errorf("DefaultPageSize = %d, want 20", cfg.DefaultPageSize)
	}
	if cfg.MaxPageSize != 100 {
		t.Errorf("MaxPageSize = %d, want 100", cfg.MaxPageSize)
	}
	if cfg.RecentPaymentsLimit != 50 {
		t.Errorf("RecentPaymentsLimit = %d, want 50", cfg.RecentPaymentsLimit)
	}
	// Time window defaults
	if cfg.DefaultRevenueDays != 7 {
		t.Errorf("DefaultRevenueDays = %d, want 7", cfg.DefaultRevenueDays)
	}
	if cfg.MaxRevenueDays != 365 {
		t.Errorf("MaxRevenueDays = %d, want 365", cfg.MaxRevenueDays)
	}
	if cfg.DefaultGPSTrailHours != 24 {
		t.Errorf("DefaultGPSTrailHours = %d, want 24", cfg.DefaultGPSTrailHours)
	}
	if cfg.MaxGPSTrailHours != 168 {
		t.Errorf("MaxGPSTrailHours = %d, want 168", cfg.MaxGPSTrailHours)
	}
	if cfg.DefaultAnalyticsDays != 7 {
		t.Errorf("DefaultAnalyticsDays = %d, want 7", cfg.DefaultAnalyticsDays)
	}
	if cfg.MaxAnalyticsDays != 365 {
		t.Errorf("MaxAnalyticsDays = %d, want 365", cfg.MaxAnalyticsDays)
	}
}

func TestLoadAdminConfig_Overrides(t *testing.T) {
	os.Setenv("ANALYTICS_ESTIMATE_RATE", "0.15")
	os.Setenv("RECONCILIATION_MIN_THRESHOLD_PKR", "5.0")
	os.Setenv("ADMIN_MAX_PAGE_SIZE", "500")
	os.Setenv("ADMIN_DEFAULT_PAGE_SIZE", "100")
	defer os.Unsetenv("ANALYTICS_ESTIMATE_RATE")
	defer os.Unsetenv("RECONCILIATION_MIN_THRESHOLD_PKR")
	defer os.Unsetenv("ADMIN_MAX_PAGE_SIZE")
	defer os.Unsetenv("ADMIN_DEFAULT_PAGE_SIZE")

	cfg, err := LoadAdminConfig()
	if err != nil {
		t.Fatalf("LoadAdminConfig failed: %v", err)
	}
	if cfg.AnalyticsEstimateRate != 0.15 {
		t.Errorf("AnalyticsEstimateRate = %v, want 0.15", cfg.AnalyticsEstimateRate)
	}
	if cfg.ReconciliationMinThreshold != 5.0 {
		t.Errorf("ReconciliationMinThreshold = %v, want 5.0", cfg.ReconciliationMinThreshold)
	}
	if cfg.MaxPageSize != 500 {
		t.Errorf("MaxPageSize = %d, want 500", cfg.MaxPageSize)
	}
	if cfg.DefaultPageSize != 100 {
		t.Errorf("DefaultPageSize = %d, want 100", cfg.DefaultPageSize)
	}
}

func TestLoadAdminConfig_InvalidAnalyticsRate(t *testing.T) {
	os.Setenv("ANALYTICS_ESTIMATE_RATE", "1.5") // > 1.0
	defer os.Unsetenv("ANALYTICS_ESTIMATE_RATE")

	_, err := LoadAdminConfig()
	if err == nil {
		t.Error("LoadAdminConfig should fail for rate > 1.0")
	}
}

func TestLoadAdminConfig_InvalidReconciliation(t *testing.T) {
	os.Setenv("RECONCILIATION_MIN_THRESHOLD_PKR", "-1")
	defer os.Unsetenv("RECONCILIATION_MIN_THRESHOLD_PKR")

	_, err := LoadAdminConfig()
	if err == nil {
		t.Error("LoadAdminConfig should fail for negative min threshold")
	}
}

func TestLoadAdminConfig_PageSizeValidation(t *testing.T) {
	os.Setenv("ADMIN_DEFAULT_PAGE_SIZE", "200")
	os.Setenv("ADMIN_MAX_PAGE_SIZE", "100") // default > max is invalid
	defer os.Unsetenv("ADMIN_DEFAULT_PAGE_SIZE")
	defer os.Unsetenv("ADMIN_MAX_PAGE_SIZE")

	_, err := LoadAdminConfig()
	if err == nil {
		t.Error("LoadAdminConfig should fail when default > max page size")
	}
}

func TestLoadReconciliationConfig(t *testing.T) {
	for _, k := range []string{"RECONCILIATION_MIN_THRESHOLD_PKR", "RECONCILIATION_RELATIVE_RATE"} {
		os.Unsetenv(k)
	}
	cfg, err := LoadReconciliationConfig()
	if err != nil {
		t.Fatalf("LoadReconciliationConfig failed: %v", err)
	}
	if cfg.MinThresholdPKR != 1.0 {
		t.Errorf("MinThresholdPKR = %v, want 1.0", cfg.MinThresholdPKR)
	}
	if cfg.RelativeRate != 0.0001 {
		t.Errorf("RelativeRate = %v, want 0.0001", cfg.RelativeRate)
	}
}
