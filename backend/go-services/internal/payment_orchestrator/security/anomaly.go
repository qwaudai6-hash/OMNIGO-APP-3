package security

import (
	"fmt"
	"sync"
	"time"
)

// AnomalyDetector monitors payment patterns for suspicious activity.
type AnomalyDetector struct {
	mu             sync.Mutex
	webhookCounts  map[string]int       // IP -> count in current window
	webhookWindow  time.Time            // window start
	amounts        map[string][]float64 // order_id -> amounts seen
	maxWebhooks    int                  // max webhooks per IP per minute
	maxAmount      float64              // max single payment amount
	maxDailyAmount float64              // max daily volume per gateway
	dailyTotals    map[string]float64   // gateway -> daily total
	dailyReset     time.Time            // daily reset time
}

func NewAnomalyDetector() *AnomalyDetector {
	return &AnomalyDetector{
		webhookCounts:  make(map[string]int),
		amounts:        make(map[string][]float64),
		dailyTotals:    make(map[string]float64),
		maxWebhooks:    100,      // max 100 webhooks per IP per minute
		maxAmount:      1000000,  // max 1M PKR per payment
		maxDailyAmount: 10000000, // max 10M PKR daily per gateway
	}
}

// CheckWebhookRate checks if a webhook from this IP is suspicious.
func (d *AnomalyDetector) CheckWebhookRate(ipAddress string) (bool, string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	// Reset window every minute
	if now.Sub(d.webhookWindow) > time.Minute {
		d.webhookCounts = make(map[string]int)
		d.webhookWindow = now
	}

	d.webhookCounts[ipAddress]++
	count := d.webhookCounts[ipAddress]

	if count > d.maxWebhooks {
		return true, fmt.Sprintf("IP %s sent %d webhooks in 1 minute (limit: %d)", ipAddress, count, d.maxWebhooks)
	}

	return false, ""
}

// CheckAmount checks if a payment amount is suspicious.
func (d *AnomalyDetector) CheckAmount(orderID string, amount float64) (bool, string) {
	if amount > d.maxAmount {
		return true, fmt.Sprintf("Payment amount %.2f exceeds maximum %.2f for order %s", amount, d.maxAmount, orderID)
	}
	if amount <= 0 {
		return true, fmt.Sprintf("Payment amount %.2f is invalid for order %s", amount, orderID)
	}
	return false, ""
}

// CheckDuplicateAmount checks if the same order has been paid multiple times.
func (d *AnomalyDetector) CheckDuplicateAmount(orderID string, amount float64) (bool, string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	existing := d.amounts[orderID]
	for _, a := range existing {
		if a == amount {
			return true, fmt.Sprintf("Order %s has duplicate payment of %.2f", orderID, amount)
		}
	}
	d.amounts[orderID] = append(d.amounts[orderID], amount)
	return false, ""
}

// CheckDailyVolume checks if the daily volume for a gateway is suspicious.
func (d *AnomalyDetector) CheckDailyVolume(gateway string, amount float64) (bool, string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()

	// Reset daily totals at midnight
	if now.Day() != d.dailyReset.Day() || now.Month() != d.dailyReset.Month() {
		d.dailyTotals = make(map[string]float64)
		d.dailyReset = now
	}

	d.dailyTotals[gateway] += amount
	total := d.dailyTotals[gateway]

	if total > d.maxDailyAmount {
		return true, fmt.Sprintf("Gateway %s daily volume %.2f exceeds maximum %.2f", gateway, total, d.maxDailyAmount)
	}

	return false, ""
}

// Cleanup removes old amount records periodically.
func (d *AnomalyDetector) Cleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		d.mu.Lock()
		// Keep only last 1000 order records
		if len(d.amounts) > 1000 {
			newAmounts := make(map[string][]float64)
			i := 0
			for k, v := range d.amounts {
				if i > 500 {
					break
				}
				newAmounts[k] = v
				i++
			}
			d.amounts = newAmounts
		}
		d.mu.Unlock()
	}
}
