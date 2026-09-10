package fraud

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ReturnFraudDetector detects suspicious return patterns using Redis-backed velocity checks.
type ReturnFraudDetector struct {
	rdb redis.UniversalClient
}

// FraudResult contains the result of a fraud check.
type FraudResult struct {
	Blocked  bool     `json:"blocked"`
	Reasons  []string `json:"reasons"`
	FlagOnly bool     `json:"flag_only"` // true = allow but flag for review
}

// ReturnFraudConfig holds configurable thresholds.
type ReturnFraudConfig struct {
	// Velocity limits
	MaxReturnsPer7Days    int // default: 3
	MaxReturnsPer30Days   int // default: 5
	MaxReturnsPer90Days   int // default: 8

	// High-value threshold (paisa)
	HighValueThresholdPaisa int64 // default: 5000000 (PKR 50,000)

	// Return-to-order ratio threshold
	MaxReturnRatio float64 // default: 0.30 (30%)
	MinOrdersForRatio int // minimum orders before ratio check applies
}

func DefaultReturnFraudConfig() *ReturnFraudConfig {
	return &ReturnFraudConfig{
		MaxReturnsPer7Days:      3,
		MaxReturnsPer30Days:     5,
		MaxReturnsPer90Days:     8,
		HighValueThresholdPaisa: 5000000, // PKR 50,000
		MaxReturnRatio:          0.30,
		MinOrdersForRatio:       5,
	}
}

func NewReturnFraudDetector(rdb redis.UniversalClient) *ReturnFraudDetector {
	return &ReturnFraudDetector{rdb: rdb}
}

// CheckReturn evaluates a return request against fraud rules.
// customerID: customer tracking ID
// orderAmountPaisa: total order amount in paisa
// returnCount: total returns by this customer (from DB)
// orderCount: total completed orders by this customer (from DB)
func (d *ReturnFraudDetector) CheckReturn(
	ctx context.Context,
	customerID string,
	orderAmountPaisa int64,
	returnCount int,
	orderCount int,
) (*FraudResult, error) {
	if d.rdb == nil {
		return &FraudResult{}, nil
	}

	config := DefaultReturnFraudConfig()
	result := &FraudResult{}

	// 1. Velocity check: returns in last 7 days
	sevenDayKey := fmt.Sprintf("fraud:return:7d:%s", customerID)
	sevenDayCount, err := d.getSlidingWindowCount(ctx, sevenDayKey, 7*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("failed to check 7-day velocity: %w", err)
	}
	if sevenDayCount >= config.MaxReturnsPer7Days {
		result.Blocked = true
		result.Reasons = append(result.Reasons, fmt.Sprintf("velocity_7d: %d returns in 7 days (max %d)", sevenDayCount, config.MaxReturnsPer7Days))
	}

	// 2. Velocity check: returns in last 30 days
	thirtyDayKey := fmt.Sprintf("fraud:return:30d:%s", customerID)
	thirtyDayCount, err := d.getSlidingWindowCount(ctx, thirtyDayKey, 30*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("failed to check 30-day velocity: %w", err)
	}
	if thirtyDayCount >= config.MaxReturnsPer30Days {
		result.Blocked = true
		result.Reasons = append(result.Reasons, fmt.Sprintf("velocity_30d: %d returns in 30 days (max %d)", thirtyDayCount, config.MaxReturnsPer30Days))
	}

	// 3. Velocity check: returns in last 90 days
	ninetyDayKey := fmt.Sprintf("fraud:return:90d:%s", customerID)
	ninetyDayCount, err := d.getSlidingWindowCount(ctx, ninetyDayKey, 90*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("failed to check 90-day velocity: %w", err)
	}
	if ninetyDayCount >= config.MaxReturnsPer90Days {
		result.Blocked = true
		result.Reasons = append(result.Reasons, fmt.Sprintf("velocity_90d: %d returns in 90 days (max %d)", ninetyDayCount, config.MaxReturnsPer90Days))
	}

	// 4. High-value return flagging
	if orderAmountPaisa >= config.HighValueThresholdPaisa {
		result.FlagOnly = true
		result.Reasons = append(result.Reasons, fmt.Sprintf("high_value: PKR %d return requested (threshold PKR %d)", orderAmountPaisa/100, config.HighValueThresholdPaisa/100))
	}

	// 5. Return-to-order ratio
	if orderCount >= config.MinOrdersForRatio {
		ratio := float64(returnCount) / float64(orderCount)
		if ratio >= config.MaxReturnRatio {
			result.Blocked = true
			result.Reasons = append(result.Reasons, fmt.Sprintf("return_ratio: %.1f%% return rate (%d returns / %d orders, max %.0f%%)", ratio*100, returnCount, orderCount, config.MaxReturnRatio*100))
		}
	}

	return result, nil
}

// RecordReturn records a return in the velocity tracking windows.
// Should be called AFTER the return is successfully created.
func (d *ReturnFraudDetector) RecordReturn(ctx context.Context, customerID string) error {
	if d.rdb == nil {
		return nil
	}

	now := time.Now()

	// Record in 7-day window
	sevenDayKey := fmt.Sprintf("fraud:return:7d:%s", customerID)
	if err := d.recordInWindow(ctx, sevenDayKey, now, 7*24*time.Hour); err != nil {
		return err
	}

	// Record in 30-day window
	thirtyDayKey := fmt.Sprintf("fraud:return:30d:%s", customerID)
	if err := d.recordInWindow(ctx, thirtyDayKey, now, 30*24*time.Hour); err != nil {
		return err
	}

	// Record in 90-day window
	ninetyDayKey := fmt.Sprintf("fraud:return:90d:%s", customerID)
	if err := d.recordInWindow(ctx, ninetyDayKey, now, 90*24*time.Hour); err != nil {
		return err
	}

	return nil
}

// getSlidingWindowCount returns the count of events in a sliding window.
func (d *ReturnFraudDetector) getSlidingWindowCount(ctx context.Context, key string, window time.Duration) (int, error) {
	// Use ZCOUNT on sorted set with score = timestamp
	min := float64(time.Now().Add(-window).UnixMilli())
	max := float64(time.Now().UnixMilli())

	count, err := d.rdb.ZCount(ctx, key, fmt.Sprintf("%f", min), fmt.Sprintf("%f", max)).Result()
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

// recordInWindow adds a timestamp to a sorted set and trims old entries.
func (d *ReturnFraudDetector) recordInWindow(ctx context.Context, key string, now time.Time, window time.Duration) error {
	pipe := d.rdb.Pipeline()

	// Add current timestamp as member (score = timestamp millis)
	score := float64(now.UnixMilli())
	pipe.ZAdd(ctx, key, redis.Z{Score: score, Member: now.Format(time.RFC3339Nano)})

	// Trim entries outside the window
	min := float64(now.Add(-window).UnixMilli())
	pipe.ZRemRangeByScore(ctx, key, "0", fmt.Sprintf("%f", min))

	// Set TTL to window + 1 day for cleanup
	pipe.Expire(ctx, key, window+24*time.Hour)

	_, err := pipe.Exec(ctx)
	return err
}

// GetCustomerReturnStats returns the return velocity stats for a customer.
func (d *ReturnFraudDetector) GetCustomerReturnStats(ctx context.Context, customerID string) (map[string]int, error) {
	if d.rdb == nil {
		return map[string]int{}, nil
	}

	stats := map[string]int{}

	// 7-day count
	sevenDayKey := fmt.Sprintf("fraud:return:7d:%s", customerID)
	if count, err := d.getSlidingWindowCount(ctx, sevenDayKey, 7*24*time.Hour); err == nil {
		stats["returns_7d"] = count
	}

	// 30-day count
	thirtyDayKey := fmt.Sprintf("fraud:return:30d:%s", customerID)
	if count, err := d.getSlidingWindowCount(ctx, thirtyDayKey, 30*24*time.Hour); err == nil {
		stats["returns_30d"] = count
	}

	// 90-day count
	ninetyDayKey := fmt.Sprintf("fraud:return:90d:%s", customerID)
	if count, err := d.getSlidingWindowCount(ctx, ninetyDayKey, 90*24*time.Hour); err == nil {
		stats["returns_90d"] = count
	}

	return stats, nil
}
