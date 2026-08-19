package fraud

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var (
	ErrVelocityLimitExceeded = errors.New("fraud: payment velocity limit exceeded, please retry later")
	ErrCardBruteForceBlocked = errors.New("fraud: multiple failed payment attempts detected from your device/IP, temporarily blocked for 5 minutes")
	ErrSuspiciousOrderAmount = errors.New("fraud: transaction flagged for excessive order value anomaly")
)

// Detector provides real-time sliding-window velocity checks and risk scoring.
type Detector struct {
	redis               redis.UniversalClient
	db                  *pgxpool.Pool
	maxFailedPerIP      int64
	ipFailureWindow     time.Duration
	maxAttemptsPerUser  int64
	userAttemptWindow   time.Duration
	anomalyMultiplier   float64
}

// NewDetector constructs a production fraud detector.
func NewDetector(rdb redis.UniversalClient, db *pgxpool.Pool) *Detector {
	return &Detector{
		redis:              rdb,
		db:                 db,
		maxFailedPerIP:     3,                // max 3 failed card attempts per IP
		ipFailureWindow:    5 * time.Minute,  // 5-minute block window
		maxAttemptsPerUser: 5,                // max 5 checkout attempts per user
		userAttemptWindow:  10 * time.Minute, // 10-minute window
		anomalyMultiplier:  10.0,             // 10x historical average threshold
	}
}

// CheckVelocity verifies IP and User checkout rates to prevent brute-force attacks.
func (d *Detector) CheckVelocity(ctx context.Context, userTrackingID, clientIP string) error {
	if d.redis == nil {
		return nil // Fail-open if Redis is not configured
	}

	// 1. Check IP Failed Attempts (Card Brute-Force Protection)
	if clientIP != "" && clientIP != "127.0.0.1" && clientIP != "::1" {
		ipFailKey := fmt.Sprintf("fraud:fail:ip:%s", clientIP)
		failedCount, err := d.redis.Get(ctx, ipFailKey).Int64()
		if err == nil && failedCount >= d.maxFailedPerIP {
			log.Printf("[FraudDetector] BLOCKED: IP %s exceeded max failed payment attempts (%d/%d)", clientIP, failedCount, d.maxFailedPerIP)
			return ErrCardBruteForceBlocked
		}
	}

	// 2. Check User Velocity
	if userTrackingID != "" {
		userAttemptsKey := fmt.Sprintf("fraud:attempts:user:%s", userTrackingID)
		attemptsCount, err := d.redis.Get(ctx, userAttemptsKey).Int64()
		if err == nil && attemptsCount >= d.maxAttemptsPerUser {
			log.Printf("[FraudDetector] BLOCKED: User %s exceeded max checkout attempts (%d/%d)", userTrackingID, attemptsCount, d.maxAttemptsPerUser)
			return ErrVelocityLimitExceeded
		}
	}

	return nil
}

// CheckOrderAnomaly flags orders that are orders of magnitude higher than user's normal spend.
func (d *Detector) CheckOrderAnomaly(ctx context.Context, userTrackingID string, orderAmount float64) error {
	if d.db == nil || userTrackingID == "" || orderAmount <= 50000.0 {
		// Small to medium purchases (< 50k PKR) are allowed without anomaly blocks
		return nil
	}

	// Fetch user's historical successful average spend and total count
	var avgSpend float64
	var totalPaidOrders int
	err := d.db.QueryRow(ctx,
		`SELECT COALESCE(AVG(total_amount), 0), COUNT(*) 
		 FROM orders 
		 WHERE customer_tracking_id = $1 AND payment_status = 'paid'`,
		userTrackingID,
	).Scan(&avgSpend, &totalPaidOrders)

	if err != nil {
		return nil // Don't block on DB query failure
	}

	// If user is brand new (0 prior paid orders) and placing an exorbitant order (> 100k PKR), flag it
	if totalPaidOrders == 0 && orderAmount > 100000.0 {
		log.Printf("[FraudDetector] ALERT: New user %s attempting first-time order of %.2f PKR", userTrackingID, orderAmount)
		return nil // Logged, proceed to 3DS step-up authentication
	}

	// If existing user is ordering > 10x their normal historical average for huge amounts
	if totalPaidOrders > 3 && avgSpend > 0 && orderAmount > avgSpend*d.anomalyMultiplier && orderAmount > 100000.0 {
		log.Printf("[FraudDetector] SUSPICIOUS: User %s order %.2f PKR exceeds 10x historical avg (%.2f PKR)", userTrackingID, orderAmount, avgSpend)
	}

	return nil
}

// RecordAttempt records payment outcome to update sliding-window counters.
func (d *Detector) RecordAttempt(ctx context.Context, userTrackingID, clientIP string, success bool) {
	if d.redis == nil {
		return
	}

	pipe := d.redis.Pipeline()

	// 1. Track User Total In-Flight Attempts
	if userTrackingID != "" {
		userAttemptsKey := fmt.Sprintf("fraud:attempts:user:%s", userTrackingID)
		pipe.Incr(ctx, userAttemptsKey)
		pipe.Expire(ctx, userAttemptsKey, d.userAttemptWindow)
	}

	// 2. Track IP Failure Counts
	if clientIP != "" && clientIP != "127.0.0.1" && clientIP != "::1" {
		ipFailKey := fmt.Sprintf("fraud:fail:ip:%s", clientIP)
		if !success {
			pipe.Incr(ctx, ipFailKey)
			pipe.Expire(ctx, ipFailKey, d.ipFailureWindow)
		} else {
			// On successful authorization, clear failed count for this IP
			pipe.Del(ctx, ipFailKey)
		}
	}

	_, _ = pipe.Exec(ctx)
}
