package payment_orchestrator

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SplitResult contains the calculated payment split for an order.
type SplitResult struct {
	OrderTotal     float64 `json:"order_total"`
	DeliveryFee    float64 `json:"delivery_fee"`
	AdminRevenue   float64 `json:"admin_revenue"`   // Dynamic rate based on store commission + COD surcharge
	VendorEscrow   float64 `json:"vendor_escrow"`   // Remainder after admin + delivery
	DeliveryEscrow float64 `json:"delivery_escrow"` // delivery_fee → held for rider payout
	CommissionRate float64 `json:"commission_rate"` // Base store commission rate
}

func envFloat(key string, fallback float64) float64 {
	valStr := os.Getenv(key)
	if valStr != "" {
		if val, err := strconv.ParseFloat(valStr, 64); err == nil {
			return val
		} else {
			log.Printf("Invalid float for %s: %v, using fallback %f", key, err, fallback)
		}
	}
	return fallback
}

// CommissionCalculator computes payment splits from order and delivery data.
type CommissionCalculator struct {
	db *pgxpool.Pool
}

func NewCommissionCalculator(db *pgxpool.Pool) *CommissionCalculator {
	return &CommissionCalculator{db: db}
}

// CalculateSplit computes the three-way split for an online payment.
// It reads the store's commission_rate and the delivery gig's fee.
func (c *CommissionCalculator) CalculateSplit(ctx context.Context, orderTotal float64, storeTrackingID, deliveryTrackingID string) (*SplitResult, error) {
	// 1. Read store commission_rate
	var commissionRate float64 = envFloat("DEFAULT_COMMISSION_RATE", 2.00)
	err := c.db.QueryRow(ctx,
		`SELECT commission_rate FROM stores WHERE store_tracking_id = $1`, storeTrackingID,
	).Scan(&commissionRate)
	if err != nil {
		// Default if store not found
		commissionRate = envFloat("DEFAULT_COMMISSION_RATE", 2.00)
	}

	// 2. Read delivery fee from the gig
	var deliveryFee float64
	if deliveryTrackingID != "" {
		err = c.db.QueryRow(ctx,
			`SELECT COALESCE(delivery_fee, 0) FROM deliveries WHERE tracking_id = $1`, deliveryTrackingID,
		).Scan(&deliveryFee)
		if err != nil {
			deliveryFee = 0
		}
	}

	// 3. Calculate splits
	adminRevenue := orderTotal * commissionRate / 100.0
	deliveryEscrow := deliveryFee
	vendorEscrow := orderTotal - adminRevenue - deliveryEscrow

	// Ensure vendor escrow is not negative
	if vendorEscrow < 0 {
		vendorEscrow = 0
	}

	return &SplitResult{
		OrderTotal:     orderTotal,
		DeliveryFee:    deliveryFee,
		AdminRevenue:   adminRevenue,
		VendorEscrow:   vendorEscrow,
		DeliveryEscrow: deliveryEscrow,
		CommissionRate: commissionRate,
	}, nil
}

// CalculateCODSplit computes the split for COD settlement (higher admin cut since rider handled cash).
func (c *CommissionCalculator) CalculateCODSplit(ctx context.Context, orderTotal float64, storeTrackingID string, orderTrackingID string) (*SplitResult, error) {
	var commissionRate float64 = envFloat("DEFAULT_COMMISSION_RATE", 2.00)
	err := c.db.QueryRow(ctx,
		`SELECT commission_rate FROM stores WHERE store_tracking_id = $1`, storeTrackingID,
	).Scan(&commissionRate)
	if err != nil {
		commissionRate = envFloat("DEFAULT_COMMISSION_RATE", 2.00)
	}

	var deliveryFee float64
	if orderTrackingID != "" {
		err = c.db.QueryRow(ctx,
			`SELECT COALESCE(delivery_fee, 0) FROM deliveries WHERE order_tracking_id = $1 LIMIT 1`, orderTrackingID,
		).Scan(&deliveryFee)
		if err != nil {
			deliveryFee = 0
		}
	}

	codSurcharge := envFloat("COD_SURCHARGE_RATE", 0.5)

	adminRevenue := orderTotal * (commissionRate + codSurcharge) / 100.0
	vendorEscrow := orderTotal - adminRevenue - deliveryFee

	if vendorEscrow < 0 {
		vendorEscrow = 0
	}

	return &SplitResult{
		OrderTotal:     orderTotal,
		DeliveryFee:    deliveryFee,
		AdminRevenue:   adminRevenue,
		VendorEscrow:   vendorEscrow,
		DeliveryEscrow: deliveryFee,
		CommissionRate: commissionRate,
	}, nil
}

// CalculateRiderDeliveryCredit computes the rider's earning from a completed delivery.
// Rider gets: delivery_fee - admin_commission
func (c *CommissionCalculator) CalculateRiderDeliveryCredit(ctx context.Context, deliveryTrackingID string) (riderEarning, adminCommission float64, err error) {
	var deliveryFee float64
	err = c.db.QueryRow(ctx,
		`SELECT COALESCE(delivery_fee, 0), COALESCE(admin_commission, 0) FROM deliveries WHERE tracking_id = $1`,
		deliveryTrackingID,
	).Scan(&deliveryFee, &adminCommission)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read delivery gig: %w", err)
	}

	riderEarning = deliveryFee - adminCommission
	if riderEarning < 0 {
		riderEarning = 0
	}

	return riderEarning, adminCommission, nil
}
