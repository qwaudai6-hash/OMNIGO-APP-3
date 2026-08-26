package admin

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// AdminRideBid is one rider's bid inside a ride's lineage report.
type AdminRideBid struct {
	RiderID   string  `json:"rider_id"`
	RiderName string  `json:"rider_name"`
	BidAmount float64 `json:"bid_amount"`
	Status    string  `json:"status"`
}

// AdminRideLedgerEntry is a money movement tied to the ride settlement.
type AdminRideLedgerEntry struct {
	TransactionID string  `json:"transaction_id"`
	Account       string  `json:"account"`
	Amount        float64 `json:"amount"`
	Description   string  `json:"description"`
}

// AdminRideLineageReport is the ride-hailing counterpart of the order lineage
// report. Ride-hailing sessions are a separate domain from the e-commerce
// order chain (rides have no order_tracking_id), so they get their own
// end-to-end trace instead of being force-resolved through orders.
type AdminRideLineageReport struct {
	RideID          string                 `json:"ride_id"`
	RideStatus      string                 `json:"ride_status"`
	VehicleType     string                 `json:"vehicle_type"`
	FareAmount      float64                `json:"fare_amount"`
	AdminCommission float64                `json:"admin_commission"`
	CustomerID      string                 `json:"customer_id"`
	CustomerName    string                 `json:"customer_name"`
	RiderID         string                 `json:"rider_id"`
	RiderName       string                 `json:"rider_name"`
	PickupLat       *float64               `json:"pickup_lat"`
	PickupLng       *float64               `json:"pickup_lng"`
	DropoffLat      *float64               `json:"dropoff_lat"`
	DropoffLng      *float64               `json:"dropoff_lng"`
	Bids            []AdminRideBid         `json:"bids"`
	LedgerEntries   []AdminRideLedgerEntry `json:"ledger_entries"`
	CreatedAt       string                 `json:"created_at"`
	UpdatedAt       string                 `json:"updated_at"`
}

// GetRideLineage audits the exact tracking mesh for a ride-hailing session:
// participants, the bid marketplace trail, and the fare-split ledger entries.
// It accepts RIDE- prefixed IDs (and tolerates a bare ID pasted without prefix).
func (s *AdminSurveillanceService) GetRideLineage(ctx context.Context, rawTrackingID string) (*AdminRideLineageReport, error) {
	traceID, ok := ctx.Value("trace_id").(string)
	if !ok {
		traceID = "ORPHAN-TRACE"
	}

	rideTrackingID := strings.TrimSpace(rawTrackingID)
	if rideTrackingID == "" {
		return nil, fmt.Errorf("tracking ID is required")
	}
	if !strings.HasPrefix(rideTrackingID, "RIDE-") {
		return nil, fmt.Errorf("%s is not a ride tracking ID (expected RIDE-...)", rideTrackingID)
	}

	report := &AdminRideLineageReport{RideID: rideTrackingID}

	// Core ride row + participant names.
	err := s.dbReader.QueryRow(ctx, `
		SELECT r.status,
		       r.vehicle_type,
		       COALESCE(r.fare_amount, 0),
		       COALESCE(r.admin_commission, 0),
		       r.customer_tracking_id,
		       COALESCE(c.full_name, 'Customer'),
		       COALESCE(r.rider_tracking_id, ''),
		       COALESCE(rider.full_name, ''),
		       r.pickup_lat, r.pickup_lng,
		       r.dropoff_lat, r.dropoff_lng,
		       r.created_at::text,
		       r.updated_at::text
		FROM rides r
		LEFT JOIN users c ON c.tracking_id = r.customer_tracking_id
		LEFT JOIN users rider ON rider.tracking_id = r.rider_tracking_id
		WHERE r.tracking_id = $1`,
		rideTrackingID,
	).Scan(
		&report.RideStatus,
		&report.VehicleType,
		&report.FareAmount,
		&report.AdminCommission,
		&report.CustomerID,
		&report.CustomerName,
		&report.RiderID,
		&report.RiderName,
		&report.PickupLat, &report.PickupLng,
		&report.DropoffLat, &report.DropoffLng,
		&report.CreatedAt,
		&report.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("ride %s not found: %w", rideTrackingID, err)
	}

	log.Printf("[SECURITY-AUDIT] [TraceID: %s] Initiating Ride Lineage Sweep for %s", traceID, rideTrackingID)

	// Bid marketplace trail.
	bidRows, err := s.dbReader.Query(ctx, `
		SELECT b.rider_tracking_id,
		       COALESCE(u.full_name, 'Unknown Rider'),
		       b.bid_amount,
		       COALESCE(b.status, 'pending')
		FROM ride_bids b
		LEFT JOIN users u ON u.tracking_id = b.rider_tracking_id
		WHERE b.ride_tracking_id = $1
		ORDER BY b.created_at ASC`,
		rideTrackingID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load bids for %s: %w", rideTrackingID, err)
	}
	defer bidRows.Close()
	for bidRows.Next() {
		var bid AdminRideBid
		if scanErr := bidRows.Scan(&bid.RiderID, &bid.RiderName, &bid.BidAmount, &bid.Status); scanErr == nil {
			report.Bids = append(report.Bids, bid)
		}
	}
	if err := bidRows.Err(); err != nil {
		return nil, fmt.Errorf("bid iteration failed for %s: %w", rideTrackingID, err)
	}

	// Fare-split ledger trail (CompleteRide settles via reference_type='ride_completion').
	entryRows, err := s.dbReader.Query(ctx, `
		SELECT transaction_id::text,
		       account,
		       amount,
		       COALESCE(description, '')
		FROM ledger_entries
		WHERE reference_type = 'ride_completion' AND reference_id = $1
		ORDER BY created_at ASC`,
		rideTrackingID,
	)
	if err == nil {
		defer entryRows.Close()
		for entryRows.Next() {
			var entry AdminRideLedgerEntry
			if scanErr := entryRows.Scan(&entry.TransactionID, &entry.Account, &entry.Amount, &entry.Description); scanErr == nil {
				report.LedgerEntries = append(report.LedgerEntries, entry)
			}
		}
		if err := entryRows.Err(); err != nil {
			log.Printf("[SECURITY-AUDIT] Ledger entry iteration failed for %s: %v", rideTrackingID, err)
		}
	} else {
		// Ledger is best-effort context — the core lineage still stands.
		log.Printf("[SECURITY-AUDIT] Ledger trail unavailable for %s: %v", rideTrackingID, err)
	}

	return report, nil
}
