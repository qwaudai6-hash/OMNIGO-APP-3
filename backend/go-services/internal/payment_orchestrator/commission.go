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

func roundToPaisa(val float64) int64 {
	return int64(math.Round(val))
}

// SplitResult contains the calculated payment split for an order.
type SplitResult struct {
	OrderTotal     int64   `json:"order_total"`     // paisa
	DeliveryFee    int64   `json:"delivery_fee"`    // paisa
	AdminRevenue   int64   `json:"admin_revenue"`   // paisa - Dynamic rate based on store commission + COD surcharge
	VendorEscrow   int64   `json:"vendor_escrow"`   // paisa - Remainder after admin + delivery
	DeliveryEscrow int64   `json:"delivery_escrow"` // paisa - delivery_fee → held for rider payout
	CommissionRate float64 `json:"commission_rate"` // Base store commission rate (e.g., 2.00 for 2%)
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
// orderTotalPaisa is the order total in paisa (int64).
func (c *CommissionCalculator) CalculateSplit(ctx context.Context, orderTotalPaisa int64, storeTrackingID, deliveryTrackingID string) (*SplitResult, error) {
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
	// NOTE: deliveries table has delivery_fee (NUMERIC rupees) and rider_earning_paisa (BIGINT)
	// Use delivery_fee converted to paisa for consistency
	var deliveryFeePaisa int64
	if deliveryTrackingID != "" {
		var deliveryFeeRupees float64
		err = c.db.QueryRow(ctx,
			`SELECT COALESCE(delivery_fee, 0) FROM deliveries WHERE tracking_id = $1`, deliveryTrackingID,
		).Scan(&deliveryFeeRupees)
		if err != nil {
			deliveryFeePaisa = 0
		} else {
			deliveryFeePaisa = int64(math.Round(deliveryFeeRupees * 100))
		}
	}

	// 3. Calculate splits in paisa with exact integer arithmetic
	adminRevenue := int64(math.Round(float64(orderTotalPaisa) * commissionRate / 100.0))
	deliveryEscrow := deliveryFeePaisa
	var vendorEscrow int64

	// Guarantee that total split never exceeds orderTotalPaisa
	if adminRevenue+deliveryEscrow > orderTotalPaisa {
		if deliveryEscrow >= orderTotalPaisa {
			deliveryEscrow = orderTotalPaisa
			adminRevenue = 0
			vendorEscrow = 0
		} else {
			adminRevenue = orderTotalPaisa - deliveryEscrow
			vendorEscrow = 0
		}
	} else {
		vendorEscrow = orderTotalPaisa - adminRevenue - deliveryEscrow
	}

	return &SplitResult{
		OrderTotal:     orderTotalPaisa,
		DeliveryFee:    deliveryEscrow,
		AdminRevenue:   adminRevenue,
		VendorEscrow:   vendorEscrow,
		DeliveryEscrow: deliveryEscrow,
		CommissionRate: commissionRate,
	}, nil
}

// CalculateCODSplit computes the split for COD settlement (higher admin cut since rider handled cash).
// orderTotalPaisa is the order total in paisa (int64).
func (c *CommissionCalculator) CalculateCODSplit(ctx context.Context, orderTotalPaisa int64, storeTrackingID string, orderTrackingID string) (*SplitResult, error) {
	var commissionRate float64 = envFloat("DEFAULT_COMMISSION_RATE", 2.00)
	err := c.db.QueryRow(ctx,
		`SELECT commission_rate FROM stores WHERE store_tracking_id = $1`, storeTrackingID,
	).Scan(&commissionRate)
	if err != nil {
		commissionRate = envFloat("DEFAULT_COMMISSION_RATE", 2.00)
	}

	var deliveryFeePaisa int64
	if orderTrackingID != "" {
		err = c.db.QueryRow(ctx,
			`SELECT COALESCE(amount_paisa, 0) FROM deliveries WHERE order_tracking_id = $1 LIMIT 1`, orderTrackingID,
		).Scan(&deliveryFeePaisa)
		if err != nil {
			deliveryFeePaisa = 0
		}
	}

	codSurcharge := envFloat("COD_SURCHARGE_RATE", 0.5)

	adminRevenue := int64(math.Round(float64(orderTotalPaisa) * (commissionRate + codSurcharge) / 100.0))
	deliveryEscrow := deliveryFeePaisa
	var vendorEscrow int64

	// Guarantee total COD split never exceeds orderTotalPaisa
	if adminRevenue+deliveryEscrow > orderTotalPaisa {
		if deliveryEscrow >= orderTotalPaisa {
			deliveryEscrow = orderTotalPaisa
			adminRevenue = 0
			vendorEscrow = 0
		} else {
			adminRevenue = orderTotalPaisa - deliveryEscrow
			vendorEscrow = 0
		}
	} else {
		vendorEscrow = orderTotalPaisa - adminRevenue - deliveryEscrow
	}

	return &SplitResult{
		OrderTotal:     orderTotalPaisa,
		DeliveryFee:    deliveryEscrow,
		AdminRevenue:   adminRevenue,
		VendorEscrow:   vendorEscrow,
		DeliveryEscrow: deliveryEscrow,
		CommissionRate: commissionRate,
	}, nil
}

// CalculateRiderDeliveryCredit computes the rider's earning from a completed delivery.
// Rider gets: delivery_fee - admin_commission
// Returns values in paisa (int64).
func (c *CommissionCalculator) CalculateRiderDeliveryCredit(ctx context.Context, deliveryTrackingID string) (riderEarning, adminCommission int64, err error) {
	var deliveryFeePaisa int64
	err = c.db.QueryRow(ctx,
		`SELECT COALESCE(amount_paisa, 0), COALESCE(commission_paisa, 0) FROM deliveries WHERE tracking_id = $1`,
		deliveryTrackingID,
	).Scan(&deliveryFeePaisa, &adminCommission)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read delivery gig: %w", err)
	}

	riderEarning = deliveryFeePaisa - adminCommission
	if riderEarning < 0 {
		riderEarning = 0
	}

	return riderEarning, adminCommission, nil
}

// ResolveDeliveryTrackingID returns the DEL- tracking ID of the delivery gig
// linked to an order, or "" when the gig does not exist yet (the gig is
// created asynchronously via the orders.created Kafka consumer, so a webhook
// can legitimately arrive before dispatch). Callers treat "" as "no delivery
// escrow component".
func (c *CommissionCalculator) ResolveDeliveryTrackingID(ctx context.Context, orderTrackingID string) string {
	if orderTrackingID == "" {
		return ""
	}
	var trackingID string
	err := c.db.QueryRow(ctx,
		`SELECT COALESCE(tracking_id, '') FROM deliveries WHERE order_tracking_id = $1 LIMIT 1`,
		orderTrackingID,
	).Scan(&trackingID)
	if err != nil {
		return ""
	}
	return trackingID
}
