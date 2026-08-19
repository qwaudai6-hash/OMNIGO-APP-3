package payment_orchestrator

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

func roundToPaisa(val float64) float64 {
	return math.Round(val*100.0) / 100.0
}

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

	// 3. Calculate splits with exact 2-decimal paisa precision
	adminRevenue := roundToPaisa(orderTotal * commissionRate / 100.0)
	deliveryEscrow := roundToPaisa(deliveryFee)
	var vendorEscrow float64

	// Guarantee that total split never exceeds orderTotal
	if adminRevenue+deliveryEscrow > orderTotal {
		if deliveryEscrow >= orderTotal {
			deliveryEscrow = roundToPaisa(orderTotal)
			adminRevenue = 0
			vendorEscrow = 0
		} else {
			adminRevenue = roundToPaisa(orderTotal - deliveryEscrow)
			vendorEscrow = 0
		}
	} else {
		vendorEscrow = roundToPaisa(orderTotal - adminRevenue - deliveryEscrow)
	}

	return &SplitResult{
		OrderTotal:     roundToPaisa(orderTotal),
		DeliveryFee:    deliveryEscrow,
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

	adminRevenue := roundToPaisa(orderTotal * (commissionRate + codSurcharge) / 100.0)
	vendorEscrow := roundToPaisa(orderTotal - adminRevenue - deliveryFee)

	if vendorEscrow < 0 {
		vendorEscrow = 0
	}

	return &SplitResult{
		OrderTotal:     roundToPaisa(orderTotal),
		DeliveryFee:    roundToPaisa(deliveryFee),
		AdminRevenue:   adminRevenue,
		VendorEscrow:   vendorEscrow,
		DeliveryEscrow: roundToPaisa(deliveryFee),
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

	riderEarning = roundToPaisa(deliveryFee - adminCommission)
	if riderEarning < 0 {
		riderEarning = 0
	}

	return riderEarning, roundToPaisa(adminCommission), nil
}
