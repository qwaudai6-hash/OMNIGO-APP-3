package admin

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/shared/config"
	tb "github.com/tigerbeetle/tigerbeetle-go"
)

// AdminLineageReport maps the complete multi-entity tracking chain for the auditor
type AdminLineageReport struct {
	OrderID        string  `json:"order_id"`
	OrderStatus    string  `json:"order_status"`
	TotalAmount    float64 `json:"total_amount"`
	CustomerID     string  `json:"customer_id"`
	CustomerName   string  `json:"customer_name"`
	CustomerPhone  string  `json:"customer_phone"`
	CustomerLat    float64 `json:"customer_lat"`
	CustomerLng    float64 `json:"customer_lng"`
	StoreID        string  `json:"store_id"`
	StoreName      string  `json:"store_name"`
	StoreLat       float64 `json:"store_lat"`
	StoreLng       float64 `json:"store_lng"`
	ProductID      string  `json:"product_id"`
	ProductName    string  `json:"product_name"`
	RiderID        string  `json:"rider_id"`
	RiderName      string  `json:"rider_name"`
	RiderPhone     string  `json:"rider_phone"`
	DeliveryStatus string  `json:"delivery_status"`
	CurrentHexagon string  `json:"current_h3_hexagon"`
}

// FullLineageReport extends lineage with order items, delivery gig/ride, and timeline.
type FullLineageReport struct {
	OrderID        string          `json:"order_id"`
	OrderStatus    string          `json:"order_status"`
	TotalAmount    float64         `json:"total_amount"`
	CustomerID     string          `json:"customer_id"`
	CustomerName   string          `json:"customer_name"`
	CustomerPhone  string          `json:"customer_phone"`
	CustomerLat    float64         `json:"customer_lat"`
	CustomerLng    float64         `json:"customer_lng"`
	StoreID        string          `json:"store_id"`
	StoreName      string          `json:"store_name"`
	StoreLat       float64         `json:"store_lat"`
	StoreLng       float64         `json:"store_lng"`
	RiderID        string          `json:"rider_id"`
	RiderName      string          `json:"rider_name"`
	RiderPhone     string          `json:"rider_phone"`
	DeliveryID     string          `json:"delivery_id"`
	DeliveryStatus string          `json:"delivery_status"`
	RideID         string          `json:"ride_id"`
	RideStatus     string          `json:"ride_status"`
	Items          []LineageItem   `json:"items"`
	Timeline       []TimelineEvent `json:"timeline"`
}

// LineageItem represents one product line inside an order.
type LineageItem struct {
	ProductID     string  `json:"product_id"`
	ProductName   string  `json:"product_name"`
	Quantity      int     `json:"quantity"`
	UnitPrice     float64 `json:"unit_price"`
	Subtotal      float64 `json:"subtotal"`
	BatchTracking string  `json:"batch_tracking,omitempty"`
}

// TimelineEvent is a status event for an order/delivery/ride.
type TimelineEvent struct {
	Entity   string `json:"entity"`
	EntityID string `json:"entity_id"`
	Status   string `json:"status"`
	Time     string `json:"time"`
}

// PendingUser represents a rider or vendor awaiting admin verification.
type PendingUser struct {
	TrackingID   string `json:"tracking_id"`
	Email        string `json:"email"`
	FullName     string `json:"full_name"`
	Role         string `json:"role"`
	Phone        string `json:"phone"`
	BusinessName string `json:"business_name"`
	Address      string `json:"address"`
	CNICURL      string `json:"cnic_url,omitempty"`
	LicenseURL   string `json:"license_url,omitempty"`
	IsVerified   bool   `json:"is_verified"`
}

type FinancialKPIs struct {
	AdminRevenue    float64 `json:"admin_revenue"`
	CentralEscrow   float64 `json:"central_escrow"`
	VendorLiability float64 `json:"vendor_liability"`
	RiderCashDebt   float64 `json:"rider_cash_debt"`
}

type PaymentRecord struct {
	OrderID       string  `json:"order_id"`
	CustomerName  string  `json:"customer_name"`
	TotalAmount   float64 `json:"total_amount"`
	PaymentMethod string  `json:"payment_method"`
	PaymentStatus string  `json:"payment_status"`
	CreatedAt     string  `json:"created_at"`
}

type DailyRevenue struct {
	Date            string  `json:"date"`
	GrossVolume     float64 `json:"gross_volume"`
	PlatformRevenue float64 `json:"platform_revenue"`
	OrderCount      int     `json:"order_count"`
}

type AdminSurveillanceService struct {
	dbWriter    *pgxpool.Pool
	dbReader    *pgxpool.Pool
	neo4jDriver neo4j.DriverWithContext
	neo4jDb     string
	tbService   *ledger.Service
	cfg         *config.AdminConfig
}

func NewAdminSurveillanceService(dbWriter, dbReader *pgxpool.Pool, driver neo4j.DriverWithContext, dbName string, tbService *ledger.Service, cfg *config.AdminConfig) *AdminSurveillanceService {
	return &AdminSurveillanceService{
		dbWriter:    dbWriter,
		dbReader:    dbReader,
		neo4jDriver: driver,
		neo4jDb:     dbName,
		tbService:   tbService,
		cfg:         cfg,
	}
}

// resolveOrderTrackingID resolves any of the universal tracking ID prefixes
// (CUST-, VEND-, STOR-, PROD-, DEL-, RIDR-, TXN-/pf_/uuid, ORD-) into its
// canonical order_tracking_id. RIDE- IDs belong to the ride-hailing domain
// and are rejected here with a pointer to GetRideLineage.
func (s *AdminSurveillanceService) resolveOrderTrackingID(ctx context.Context, utid string) (string, error) {
	utid = strings.TrimSpace(utid)
	if utid == "" {
		return "", fmt.Errorf("tracking ID is required")
	}

	if strings.HasPrefix(utid, "ORD-") {
		return utid, nil
	}

	var orderID string
	var err error

	switch {
	case strings.HasPrefix(utid, "CUST-"):
		query := `SELECT order_tracking_id FROM orders WHERE customer_tracking_id = $1 ORDER BY created_at DESC LIMIT 1`
		err = s.dbReader.QueryRow(ctx, query, utid).Scan(&orderID)
	case strings.HasPrefix(utid, "VEND-"):
		query := `SELECT o.order_tracking_id FROM orders o JOIN stores s ON o.store_tracking_id = s.store_tracking_id WHERE s.vendor_tracking_id = $1 ORDER BY o.created_at DESC LIMIT 1`
		err = s.dbReader.QueryRow(ctx, query, utid).Scan(&orderID)
	case strings.HasPrefix(utid, "STOR-"):
		query := `SELECT order_tracking_id FROM orders WHERE store_tracking_id = $1 ORDER BY created_at DESC LIMIT 1`
		err = s.dbReader.QueryRow(ctx, query, utid).Scan(&orderID)
	case strings.HasPrefix(utid, "PROD-"):
		query := `SELECT oi.order_tracking_id FROM order_items oi WHERE oi.product_tracking_id = $1 ORDER BY oi.created_at DESC LIMIT 1`
		err = s.dbReader.QueryRow(ctx, query, utid).Scan(&orderID)
	case strings.HasPrefix(utid, "DEL-"):
		query := `SELECT order_tracking_id FROM deliveries WHERE tracking_id = $1 OR order_tracking_id = $1 LIMIT 1`
		err = s.dbReader.QueryRow(ctx, query, utid).Scan(&orderID)
	case strings.HasPrefix(utid, "RIDR-"):
		// Rider user ID — find the most recent order they were assigned to.
		// (RIDE- IDs are NOT handled here: ride-hailing sessions have no
		// order linkage; use GetRideLineage for those.)
		query := `SELECT order_tracking_id FROM orders WHERE rider_tracking_id = $1 ORDER BY created_at DESC LIMIT 1`
		err = s.dbReader.QueryRow(ctx, query, utid).Scan(&orderID)
	case strings.HasPrefix(utid, "RIDE-"):
		return "", fmt.Errorf("%s is a ride-hailing session and has no order lineage — use the ride lineage endpoint (/lineage/ride/%s)", utid, utid)
	case strings.HasPrefix(utid, "TXN-") || strings.HasPrefix(utid, "pf_"):
		// Transaction IDs come in three generations: TXN-<uuid> (current),
		// pf_<uuid> (PayFast internal), and bare <uuid> (legacy webhook rows).
		// Accept any of them, with or without the TXN- prefix pasted in.
		stripped := strings.TrimPrefix(utid, "TXN-")
		candidates := []string{utid, stripped, "pf_" + stripped}
		query := `SELECT order_tracking_id FROM payment_transactions WHERE transaction_id = ANY($1) ORDER BY created_at DESC LIMIT 1`
		err = s.dbReader.QueryRow(ctx, query, candidates).Scan(&orderID)
	default:
		// Fallback: try direct order lookup
		return utid, nil
	}

	if err != nil {
		return "", fmt.Errorf("no linked order found for tracking ID %s: %w", utid, err)
	}
	return orderID, nil
}

// GetCompleteOrderLineage audits the exact tracking mesh across components.
// Uses tracking_id-based column names matching the Go-aligned init.sql (Session 16).
func (s *AdminSurveillanceService) GetCompleteOrderLineage(ctx context.Context, rawTrackingID string) (*AdminLineageReport, error) {
	traceID, ok := ctx.Value("trace_id").(string)
	if !ok {
		traceID = "ORPHAN-TRACE"
	}

	orderTrackingID, err := s.resolveOrderTrackingID(ctx, rawTrackingID)
	if err != nil {
		return nil, err
	}

	log.Printf("[SECURITY-AUDIT] [TraceID: %s] INITIATING E2E Lineage Sweep for Order: %s (from %s)", traceID, orderTrackingID, rawTrackingID)

	// Fixed query: uses order_tracking_id (not tracking_id), store_tracking_id
	// (not tracking_id on stores), and column names from the canonical baseline
	// schema. Rider is resolved from orders.rider_tracking_id first, falling back
	// to the linked delivery assignment.
	query := `
		SELECT
			o.order_tracking_id,
			o.status,
			o.total_amount,
			o.customer_tracking_id,
			COALESCE(c.first_name,'') || ' ' || COALESCE(c.last_name,'') as customer_name,
			COALESCE(c.phone,''),
			COALESCE(o.customer_lat, 0),
			COALESCE(o.customer_lng, 0),
			COALESCE(o.store_tracking_id, 'N/A'),
			COALESCE(s.store_name, 'N/A'),
			COALESCE(s.latitude, 0),
			COALESCE(s.longitude, 0),
			COALESCE((SELECT oi.product_tracking_id FROM order_items oi WHERE oi.order_tracking_id = o.order_tracking_id LIMIT 1), 'N/A'),
			COALESCE((SELECT p.name FROM products p JOIN order_items oi ON oi.product_tracking_id = p.product_tracking_id WHERE oi.order_tracking_id = o.order_tracking_id LIMIT 1), 'N/A'),
			COALESCE(o.rider_tracking_id, d.rider_tracking_id, 'UNASSIGNED'),
			COALESCE(ru.first_name,'') || ' ' || COALESCE(ru.last_name,'') as rider_name,
			COALESCE(ru.phone,''),
			COALESCE(d.status, 'PENDING')
		FROM orders o
		LEFT JOIN users c ON o.customer_tracking_id = c.tracking_id
		LEFT JOIN stores s ON o.store_tracking_id = s.store_tracking_id
		LEFT JOIN deliveries d ON o.order_tracking_id = d.order_tracking_id
		LEFT JOIN users ru ON COALESCE(o.rider_tracking_id, d.rider_tracking_id) = ru.tracking_id
		WHERE o.order_tracking_id = $1
		LIMIT 1
	`

	var r AdminLineageReport

	err = s.dbReader.QueryRow(ctx, query, orderTrackingID).Scan(
		&r.OrderID,
		&r.OrderStatus,
		&r.TotalAmount,
		&r.CustomerID,
		&r.CustomerName,
		&r.CustomerPhone,
		&r.CustomerLat,
		&r.CustomerLng,
		&r.StoreID,
		&r.StoreName,
		&r.StoreLat,
		&r.StoreLng,
		&r.ProductID,
		&r.ProductName,
		&r.RiderID,
		&r.RiderName,
		&r.RiderPhone,
		&r.DeliveryStatus,
	)
	if err != nil {
		return nil, fmt.Errorf("relational lineage fetch failed: %w", err)
	}

	r.CurrentHexagon = "N/A"

	// Graph audit: verify topological chain in Neo4j (graceful degradation
	// if Neo4j is down — we log a warning but don't fail the request).
	if s.neo4jDriver != nil {
		s.verifyGraphChain(ctx, orderTrackingID, traceID)
	}

	return &r, nil
}

// verifyGraphChain cross-verify the order's topological chain in Neo4j.
// Non-fatal: logs a warning if Neo4j is unavailable.
func (s *AdminSurveillanceService) verifyGraphChain(ctx context.Context, orderTrackingID, traceID string) {
	session := s.neo4jDriver.NewSession(ctx, neo4j.SessionConfig{
		AccessMode:   neo4j.AccessModeRead,
		DatabaseName: s.neo4jDb,
	})
	defer session.Close(ctx)

	cypherQuery := `
		MATCH (c:Customer)-[:ORDERED]->(o:Order {utid: $order_id})-[:SOLD_BY]->(s:Store)
		RETURN c.utid, s.utid
	`
	_, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		result, err := tx.Run(ctx, cypherQuery, map[string]interface{}{"order_id": orderTrackingID})
		if err != nil {
			return nil, err
		}
		if result.Next(ctx) {
			log.Printf("[SECURITY-AUDIT] [TraceID: %s] Graph chain verified for order: %s", traceID, orderTrackingID)
		} else {
			log.Printf("[SECURITY-CRITICAL] [TraceID: %s] GRAPH DESYNC DETECTED on order: %s", traceID, orderTrackingID)
		}
		return nil, nil
	})
	if err != nil {
		log.Printf("[SECURITY-AUDIT] [TraceID: %s] Neo4j unavailable, skipping graph verification: %v", traceID, err)
	}
}

// ListPendingVerifications returns paginated riders and vendors awaiting admin approval.
func (s *AdminSurveillanceService) ListPendingVerifications(ctx context.Context, limit, offset int) ([]PendingUser, int, error) {
	countQuery := `
		SELECT COUNT(*)
		FROM users
		WHERE is_verified = false AND role IN ('rider', 'vendor')
	`
	var total int
	if err := s.dbReader.QueryRow(ctx, countQuery).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `
		SELECT tracking_id, email, COALESCE(full_name, ''), role, COALESCE(phone, ''), COALESCE(business_name, ''), COALESCE(address, ''), COALESCE(cnic_url, ''), COALESCE(license_url, ''), is_verified
		FROM users
		WHERE is_verified = false AND role IN ('rider', 'vendor')
		ORDER BY created_at ASC
		LIMIT $1 OFFSET $2
	`
	rows, err := s.dbReader.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []PendingUser
	for rows.Next() {
		var u PendingUser
		if err := rows.Scan(&u.TrackingID, &u.Email, &u.FullName, &u.Role, &u.Phone, &u.BusinessName, &u.Address, &u.CNICURL, &u.LicenseURL, &u.IsVerified); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// ApproveUser verifies a user (rider/vendor) so they can log in and cascades activation to their store/products.
func (s *AdminSurveillanceService) ApproveUser(ctx context.Context, trackingID string) error {
	query := `UPDATE users SET is_verified = true, verification_status = 'approved', updated_at = NOW() WHERE tracking_id = $1`
	res, err := s.dbWriter.Exec(ctx, query, trackingID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("user not found")
	}

	// Cascade activation to vendor stores and products so their catalog is
	// immediately visible. Best-effort: the user is already verified at this
	// point, but a failed cascade leaves their catalog invisible — so every
	// failure is logged loudly for ops follow-up instead of being swallowed.
	if _, err := s.dbWriter.Exec(ctx, `UPDATE stores SET is_active = true, updated_at = NOW() WHERE vendor_tracking_id = $1`, trackingID); err != nil {
		log.Printf("[ADMIN] CRITICAL: store activation cascade failed for vendor %s: %v", trackingID, err)
	}
	if _, err := s.dbWriter.Exec(ctx, `UPDATE products SET is_active = true, updated_at = NOW() WHERE vendor_tracking_id = $1`, trackingID); err != nil {
		log.Printf("[ADMIN] CRITICAL: product activation cascade failed for vendor %s: %v", trackingID, err)
	}

	return nil
}

// GetLedgerKPIs fetches the current balances of the global platform accounts from TigerBeetle.
//
// AW-3 FIX: VendorLiability previously only summed AccountVendorWallet,
// missing the money held in AccountVendorLockedEscrow (48h hold) and
// AccountVendorPendingEscrow (approved, pending payout). Admin would
// under-report vendor liability by orders of magnitude.
//
// Previously this function returned zeroed-out FinancialKPIs when
// TigerBeetle was unavailable. That made a broken ledger look identical
// to "no money anywhere" — admin would see zeros and assume the system
// was healthy. The new behavior returns an explicit error so the admin
// dashboard can show "ledger offline" instead of misleading zeros.
func (s *AdminSurveillanceService) GetLedgerKPIs(ctx context.Context) (*FinancialKPIs, error) {
	if s.tbService == nil || s.tbService.TBService() == nil {
		return nil, errors.New("ledger KPIs unavailable: TigerBeetle is not configured or offline")
	}

	accountIDs := []tb.Uint128{
		ledger.AccountToUint128(ledger.AccountAdminRevenue),
		ledger.AccountToUint128(ledger.AccountCentralEscrow),
		// AW-3 FIX: include locked + pending escrow in vendor liability.
		ledger.AccountToUint128(ledger.AccountVendorLockedEscrow),
		ledger.AccountToUint128(ledger.AccountVendorPendingEscrow),
		ledger.AccountToUint128(ledger.AccountVendorWallet),
		ledger.AccountToUint128(ledger.AccountCashReceivable),
	}

	accounts, err := s.tbService.TBService().GetAccountBalances(accountIDs)
	if err != nil {
		return nil, err
	}

	kpis := &FinancialKPIs{}

	for _, acc := range accounts {
		// Convert TigerBeetle cents to dollars (float64)
		credits := acc.CreditsPosted.BigInt()
		debits := acc.DebitsPosted.BigInt()

		balCents := new(big.Int).Sub(credits, debits).Int64()
		bal := float64(balCents) / 100.0

		// AW-3 FIX: use += so locked + pending + wallet all accumulate.
		if acc.ID == ledger.AccountToUint128(ledger.AccountAdminRevenue) {
			kpis.AdminRevenue = bal
		} else if acc.ID == ledger.AccountToUint128(ledger.AccountCentralEscrow) {
			kpis.CentralEscrow = bal
		} else if acc.ID == ledger.AccountToUint128(ledger.AccountVendorLockedEscrow) {
			kpis.VendorLiability += bal
		} else if acc.ID == ledger.AccountToUint128(ledger.AccountVendorPendingEscrow) {
			kpis.VendorLiability += bal
		} else if acc.ID == ledger.AccountToUint128(ledger.AccountVendorWallet) {
			kpis.VendorLiability += bal
		} else if acc.ID == ledger.AccountToUint128(ledger.AccountCashReceivable) {
			kpis.RiderCashDebt = float64(new(big.Int).Sub(debits, credits).Int64()) / 100.0 // asset
		}
	}

	return kpis, nil
}

// GetRecentPayments fetches recent order payment statuses from PostgreSQL
func (s *AdminSurveillanceService) GetRecentPayments(ctx context.Context, limit int) ([]PaymentRecord, error) {
	query := `
		SELECT 
			o.order_tracking_id, 
			COALESCE(u.full_name, 'Customer'), 
			o.total_amount, 
			COALESCE(o.payment_gateway, 'payfast'), 
			COALESCE(o.payment_status, o.status), 
			to_char(o.created_at, 'YYYY-MM-DD HH24:MI:SS')
		FROM orders o
		LEFT JOIN users u ON o.customer_tracking_id = u.tracking_id
		ORDER BY o.created_at DESC
		LIMIT $1
	`
	rows, err := s.dbReader.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query recent payments: %w", err)
	}
	defer rows.Close()

	var records []PaymentRecord
	for rows.Next() {
		var r PaymentRecord
		var createdAt string
		err := rows.Scan(&r.OrderID, &r.CustomerName, &r.TotalAmount, &r.PaymentMethod, &r.PaymentStatus, &createdAt)
		if err != nil {
			return nil, err
		}
		r.CreatedAt = createdAt
		records = append(records, r)
	}
	return records, nil
}

// GetDailyRevenue aggregates completed orders grouped by day.
//
// AW-4 FIX: replaced N+1 query (one ledger query per day) with a single
// CTE-based query that joins orders and ledger entries in one round-trip.
// Also replaced the flat 10% fallback with a per-order commission
// estimate (using orders.admin_commission when available) so the
// platform revenue number is accurate even when the ledger hasn't been
// written yet for the day.
//
// The legacy flat-rate fallback has been removed. If the data sources
// (ledger + per-order commission + computed platform cut) all return
// zero for a day, the row's PlatformRevenue is left as 0 rather than
// being estimated — operators must configure ANALYTICS_ESTIMATE_RATE
// to enable an explicit, auditable estimate.
func (s *AdminSurveillanceService) GetDailyRevenue(ctx context.Context, days int, paymentMethod string) ([]DailyRevenue, error) {
	if s.cfg != nil {
		if days <= 0 {
			days = s.cfg.DefaultRevenueDays
		}
		if days > s.cfg.MaxRevenueDays {
			days = s.cfg.MaxRevenueDays
		}
	} else {
		if days <= 0 {
			days = 7
		}
		if days > 365 {
			days = 365
		}
	}

	// Build the WHERE clause. paymentFilter is built separately so the
	// ledger CTE uses the same filter (and so we only fetch ledger
	// entries for the relevant payment method).
	paymentFilterOrders := ""
	paymentFilterLedger := ""
	// Pass days as a text value (e.g. "7") so pgx encodes it as TEXT
	// and the query's `(CAST($1 AS TEXT) || ' days')::INTERVAL` works.
	daysArg := strconv.Itoa(days)
	args := []interface{}{daysArg}
	if paymentMethod != "" && paymentMethod != "all" {
		paymentFilterOrders = "AND payment_gateway = $2"
		paymentFilterLedger = "AND le.reference_id IN (SELECT order_tracking_id FROM orders WHERE payment_gateway = $2)"
		args = append(args, paymentMethod)
	}

	// Single CTE-based query:
	//  1. daily_orders — aggregate order totals per day
	//  2. daily_ledger — aggregate ledger revenue per day (joins ledger
	//     entries to the admin_revenue_account, filtered by payment method
	//     via the order they reference)
	//  3. Final SELECT joins them. Platform revenue is the ledger amount
	//     when present, else an estimate based on each order's actual
	//     admin_commission column.
	query := fmt.Sprintf(`
		WITH daily_orders AS (
			SELECT
				DATE(created_at AT TIME ZONE 'UTC') AS day,
				SUM(total_amount) AS gross_volume,
				COUNT(order_tracking_id) AS order_count,
				COALESCE(SUM(admin_commission), 0) AS commission_sum,
				COALESCE(SUM(total_amount - admin_commission - COALESCE(vendor_escrow, 0) - COALESCE(delivery_escrow, 0)), 0) AS commission_fallback_sum
			FROM orders
			WHERE status = 'completed'
			  AND created_at >= NOW() - (($1) || ' days')::INTERVAL
			  %s
			GROUP BY DATE(created_at AT TIME ZONE 'UTC')
		),
		daily_ledger AS (
			SELECT
				DATE(le.created_at AT TIME ZONE 'UTC') AS day,
				COALESCE(SUM(le.amount), 0) AS platform_revenue
			FROM ledger_entries le
			WHERE le.account = 'admin_revenue_account'
			  AND le.amount > 0
			  AND le.created_at >= NOW() - (($1) || ' days')::INTERVAL
			  %s
			GROUP BY DATE(le.created_at AT TIME ZONE 'UTC')
		)
		SELECT
			o.day,
			o.gross_volume,
			o.order_count,
			COALESCE(l.platform_revenue, 0) AS ledger_revenue,
			o.commission_sum,
			o.commission_fallback_sum
		FROM daily_orders o
		LEFT JOIN daily_ledger l ON o.day = l.day
		ORDER BY o.day ASC
	`, paymentFilterOrders, paymentFilterLedger)

	rows, err := s.dbReader.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate daily revenue: %w", err)
	}
	defer rows.Close()

	records := make([]DailyRevenue, 0)
	for rows.Next() {
		var r DailyRevenue
		var t time.Time
		var ledgerRev, commissionSum, commissionFallbackSum float64
		if err := rows.Scan(&t, &r.GrossVolume, &r.OrderCount, &ledgerRev, &commissionSum, &commissionFallbackSum); err != nil {
			return nil, err
		}
		r.Date = t.Format("2006-01-02")

		// AW-4 FIX: consistent revenue computation:
		//   1. If ledger has entries for this day, use them (most accurate).
		//   2. Else use the per-order admin_commission sum (accurate, derived
		//      from the actual commission stored on each order at split time).
		//   3. Else use total_amount - vendor_escrow - delivery_escrow (the
		//      per-order platform cut, works even when admin_commission is 0).
		//   4. Only if the operator explicitly set ANALYTICS_ESTIMATE_RATE,
		//      apply it as a last-resort estimate. If not set, leave at 0
		//      so we never silently fabricate a revenue number.
		switch {
		case ledgerRev > 0:
			r.PlatformRevenue = ledgerRev
		case commissionSum > 0:
			r.PlatformRevenue = commissionSum
		case commissionFallbackSum > 0:
			r.PlatformRevenue = commissionFallbackSum
		default:
			if s.cfg != nil && s.cfg.AnalyticsEstimateRate > 0 {
				r.PlatformRevenue = r.GrossVolume * s.cfg.AnalyticsEstimateRate
			}
			// else: leave at 0 — do not silently estimate with a hardcoded rate
		}
		records = append(records, r)
	}
	return records, nil
}

// ListAllUsers returns paginated users with an optional role filter.
func (s *AdminSurveillanceService) ListAllUsers(ctx context.Context, role string, limit, offset int) ([]PendingUser, int, error) {
	var countQuery string
	var countArgs []interface{}
	var query string
	var args []interface{}

	if role != "" {
		countQuery = `SELECT COUNT(*) FROM users WHERE role = $1`
		countArgs = append(countArgs, role)
		query = `
			SELECT tracking_id, email, COALESCE(full_name, ''), role, COALESCE(phone, ''), COALESCE(business_name, ''), COALESCE(address, ''), COALESCE(cnic_url, ''), COALESCE(license_url, ''), is_verified
			FROM users WHERE role = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3
		`
		args = append(args, role)
	} else {
		countQuery = `SELECT COUNT(*) FROM users`
		query = `
			SELECT tracking_id, email, COALESCE(full_name, ''), role, COALESCE(phone, ''), COALESCE(business_name, ''), COALESCE(address, ''), COALESCE(cnic_url, ''), COALESCE(license_url, ''), is_verified
			FROM users ORDER BY created_at DESC LIMIT $1 OFFSET $2
		`
	}

	var total int
	if err := s.dbReader.QueryRow(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	rows, err := s.dbReader.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []PendingUser
	for rows.Next() {
		var u PendingUser
		if err := rows.Scan(&u.TrackingID, &u.Email, &u.FullName, &u.Role, &u.Phone, &u.BusinessName, &u.Address, &u.CNICURL, &u.LicenseURL, &u.IsVerified); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// GetFullOrderLineage returns order items, delivery gig, ride, and timeline events for any of the 8 entity IDs.
func (s *AdminSurveillanceService) GetFullOrderLineage(ctx context.Context, rawTrackingID string) (*FullLineageReport, error) {
	traceID, ok := ctx.Value("trace_id").(string)
	if !ok {
		traceID = "ORPHAN-TRACE"
	}

	orderTrackingID, err := s.resolveOrderTrackingID(ctx, rawTrackingID)
	if err != nil {
		return nil, err
	}

	log.Printf("[SECURITY-AUDIT] [TraceID: %s] Full lineage sweep for order: %s (from %s)", traceID, orderTrackingID, rawTrackingID)

	query := `
		SELECT
			o.order_tracking_id, o.status, o.total_amount,
			o.customer_tracking_id,
			COALESCE(c.first_name,'') || ' ' || COALESCE(c.last_name,'') as customer_name,
			COALESCE(c.phone,''),
			COALESCE(o.customer_lat, 0),
			COALESCE(o.customer_lng, 0),
			COALESCE(o.store_tracking_id, 'N/A'),
			COALESCE(s.store_name, 'N/A'),
			COALESCE(s.latitude, 0),
			COALESCE(s.longitude, 0),
			COALESCE(o.rider_tracking_id, d.rider_tracking_id, 'UNASSIGNED'),
			COALESCE(ru.first_name,'') || ' ' || COALESCE(ru.last_name,'') as rider_name,
			COALESCE(ru.phone,''),
			COALESCE(d.tracking_id, 'N/A'),
			COALESCE(d.status, 'PENDING')
		FROM orders o
		LEFT JOIN users c ON o.customer_tracking_id = c.tracking_id
		LEFT JOIN stores s ON o.store_tracking_id = s.store_tracking_id
		LEFT JOIN deliveries d ON o.order_tracking_id = d.order_tracking_id
		LEFT JOIN users ru ON COALESCE(o.rider_tracking_id, d.rider_tracking_id) = ru.tracking_id
		WHERE o.order_tracking_id = $1
		LIMIT 1
	`
	var r FullLineageReport
	var riderID, deliveryID, deliveryStatus string
	err = s.dbReader.QueryRow(ctx, query, orderTrackingID).Scan(
		&r.OrderID, &r.OrderStatus, &r.TotalAmount,
		&r.CustomerID, &r.CustomerName, &r.CustomerPhone,
		&r.CustomerLat, &r.CustomerLng,
		&r.StoreID, &r.StoreName, &r.StoreLat, &r.StoreLng,
		&riderID, &r.RiderName, &r.RiderPhone,
		&deliveryID, &deliveryStatus,
	)
	if err != nil {
		return nil, fmt.Errorf("full lineage fetch failed: %w", err)
	}
	r.RiderID = riderID
	r.DeliveryID = deliveryID
	r.DeliveryStatus = deliveryStatus
	r.RideID = "N/A"
	r.RideStatus = "N/A"

	// Items
	itemQuery := `
		SELECT p.product_tracking_id, p.name, oi.quantity, oi.price_at_checkout, (oi.quantity * oi.price_at_checkout) AS subtotal, '' AS batch_tracking
		FROM order_items oi
		JOIN products p ON oi.product_tracking_id = p.product_tracking_id
		WHERE oi.order_tracking_id = $1
	`
	itemRows, err := s.dbReader.Query(ctx, itemQuery, orderTrackingID)
	if err != nil {
		return nil, fmt.Errorf("lineage items fetch failed: %w", err)
	}
	defer itemRows.Close()
	for itemRows.Next() {
		var it LineageItem
		if err := itemRows.Scan(&it.ProductID, &it.ProductName, &it.Quantity, &it.UnitPrice, &it.Subtotal, &it.BatchTracking); err != nil {
			return nil, err
		}
		r.Items = append(r.Items, it)
	}
	if err := itemRows.Err(); err != nil {
		return nil, err
	}

	// Timeline events (synthetic from order, delivery, and ride status)
	r.Timeline = []TimelineEvent{
		{Entity: "order", EntityID: r.OrderID, Status: r.OrderStatus, Time: "current"},
		{Entity: "delivery", EntityID: r.DeliveryID, Status: r.DeliveryStatus, Time: "current"},
		{Entity: "ride", EntityID: r.RideID, Status: r.RideStatus, Time: "current"},
	}

	return &r, nil
}

type DisputedOrder struct {
	OrderTrackingID string    `json:"order_tracking_id"`
	CustomerName    string    `json:"customer_name"`
	TotalAmount     float64   `json:"total_amount"`
	DisputeReason   string    `json:"dispute_reason"`
	HoldStatus      string    `json:"hold_status"`
	HoldExpiresAt   time.Time `json:"hold_expires_at"`
}

// GetDisputedOrders fetches orders that have active frozen escrow holds.
func (s *AdminSurveillanceService) GetDisputedOrders(ctx context.Context) ([]DisputedOrder, error) {
	query := `
		SELECT
			d.order_tracking_id,
			COALESCE(u.full_name, 'Unknown'),
			e.amount,
			d.reason,
			e.status,
			e.hold_until
		FROM disputes d
		JOIN escrow_holds e ON d.order_tracking_id = e.order_tracking_id
		LEFT JOIN users u ON d.filed_by = u.tracking_id
		WHERE e.status = 'disputed' AND d.status = 'open'
	`
	rows, err := s.dbReader.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query disputed orders: %w", err)
	}
	defer rows.Close()

	var records []DisputedOrder
	for rows.Next() {
		var r DisputedOrder
		if err := rows.Scan(&r.OrderTrackingID, &r.CustomerName, &r.TotalAmount, &r.DisputeReason, &r.HoldStatus, &r.HoldExpiresAt); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}

// ResolveDispute resolves a frozen escrow hold.
// If vendor_guilty or rider_guilty -> refund to customer wallet and penalize guilty party.
// If customer_guilty -> release funds to vendor/rider.
//
// Concurrency: the entire resolution runs inside a single Postgres
// transaction. If any step fails (customer credit, escrow status update,
// penalty debit), the transaction is rolled back so the financial state
// never ends up half-resolved. The escrow status UPDATE is guarded by
// `status IN ('disputed', 'held')` so already-'paid_out' holds are not
// silently re-marked as 'refunded'.
func (s *AdminSurveillanceService) ResolveDispute(ctx context.Context, orderTrackingID string, decision string) error {
	// First fetch the escrow record — try disputed escrow, then any escrow
	var escrowAmountPaisa int64
	var customerID string
	var escrowStatus string
	query := `
		SELECT COALESCE(e.amount_paisa, ROUND(e.amount * 100)::BIGINT), o.customer_tracking_id, e.status
		FROM escrow_holds e
		JOIN orders o ON e.order_tracking_id = o.order_tracking_id
		WHERE e.order_tracking_id = $1
		ORDER BY e.created_at DESC LIMIT 1
	`
	err := s.dbReader.QueryRow(ctx, query, orderTrackingID).Scan(&escrowAmountPaisa, &customerID, &escrowStatus)
	if err != nil {
		// No escrow record — get order amount directly for COD already-settled orders
		var orderTotalRupees float64
		err2 := s.dbReader.QueryRow(ctx,
			"SELECT total_amount, customer_tracking_id FROM orders WHERE order_tracking_id = $1",
			orderTrackingID).Scan(&orderTotalRupees, &customerID)
		if err2 != nil {
			return fmt.Errorf("order not found: %w", err2)
		}
		escrowAmountPaisa = int64(orderTotalRupees * 100)
		escrowStatus = "paid_out"
	}

	switch decision {
	case "vendor_guilty", "rider_guilty":
		return s.resolveDisputeGuilty(ctx, orderTrackingID, decision, customerID, escrowAmountPaisa, escrowStatus)
	case "customer_guilty":
		return s.resolveDisputeCustomerGuilty(ctx, orderTrackingID, escrowStatus)
	default:
		return fmt.Errorf("invalid dispute decision: %s", decision)
	}
}

// resolveDisputeGuilty handles the vendor_guilty and rider_guilty paths
// in a single atomic transaction. All money movements (customer credit,
// penalty debit, dispute status, escrow status) succeed together or
// roll back together.
func (s *AdminSurveillanceService) resolveDisputeGuilty(
	ctx context.Context, orderTrackingID, decision, customerID string,
	escrowAmountPaisa int64, escrowStatus string,
) error {
	tx, err := s.dbWriter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin dispute resolution tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Credit customer wallet (refund) using paisa columns
	upsertWallet := `
		INSERT INTO customer_wallet (customer_tracking_id, balance_paisa, lifetime_spent_paisa, updated_at)
		VALUES ($1, $2, 0, NOW())
		ON CONFLICT (customer_tracking_id) DO UPDATE SET balance_paisa = customer_wallet.balance_paisa + $2, updated_at = NOW()
	`
	_, err = tx.Exec(ctx, upsertWallet, customerID, escrowAmountPaisa)
	if err != nil {
		return fmt.Errorf("failed to credit customer wallet: %w", err)
	}

	// 2. Mark escrow as refunded — ONLY if still held/disputed. This prevents
	// accidentally overwriting an already-'paid_out' hold with 'refunded',
	// which would corrupt the audit trail.
	if escrowStatus == "disputed" || escrowStatus == "held" {
		_, err = tx.Exec(ctx,
			"UPDATE escrow_holds SET status = 'refunded', released_at = NOW() WHERE order_tracking_id = $1 AND status IN ('disputed', 'held')",
			orderTrackingID,
		)
		if err != nil {
			return fmt.Errorf("failed to mark escrow refunded: %w", err)
		}
	}

	// 3. Penalty debit (rider or vendor). Look up the tracking ID, then debit using paisa columns.
	if decision == "rider_guilty" {
		var riderTrackingID string
		err = tx.QueryRow(ctx,
			"SELECT COALESCE(rider_tracking_id, '') FROM orders WHERE order_tracking_id = $1",
			orderTrackingID,
		).Scan(&riderTrackingID)
		if err != nil {
			return fmt.Errorf("failed to lookup rider for penalty: %w", err)
		}
		if riderTrackingID != "" {
			_, err = tx.Exec(ctx,
				"UPDATE rider_wallet SET balance_paisa = balance_paisa - $1, cash_in_hand_paisa = cash_in_hand_paisa - $1, updated_at = NOW() WHERE rider_tracking_id = $2 AND balance_paisa >= $1",
				escrowAmountPaisa, riderTrackingID,
			)
			if err != nil {
				return fmt.Errorf("failed to debit rider penalty: %w", err)
			}
		}
	}
	if decision == "vendor_guilty" {
		var vendorTrackingID string
		err = tx.QueryRow(ctx,
			"SELECT COALESCE(s.vendor_tracking_id, '') FROM orders o JOIN stores s ON o.store_tracking_id = s.store_tracking_id WHERE o.order_tracking_id = $1",
			orderTrackingID,
		).Scan(&vendorTrackingID)
		if err != nil {
			return fmt.Errorf("failed to lookup vendor for penalty: %w", err)
		}
		if vendorTrackingID != "" {
			_, err = tx.Exec(ctx,
				"UPDATE vendor_wallet SET balance_paisa = balance_paisa - $1, updated_at = NOW() WHERE vendor_tracking_id = $2 AND balance_paisa >= $1",
				escrowAmountPaisa, vendorTrackingID,
			)
			if err != nil {
				return fmt.Errorf("failed to debit vendor penalty: %w", err)
			}
		}
	}

	// 4. Mark the dispute as resolved
	_, err = tx.Exec(ctx,
		"UPDATE disputes SET status = 'resolved', resolution = $1, resolved_at = NOW(), updated_at = NOW() WHERE order_tracking_id = $2 AND status = 'open'",
		fmt.Sprintf("admin_%s", decision), orderTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to mark dispute resolved: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit dispute resolution: %w", err)
	}
	return nil
}

// resolveDisputeCustomerGuilty handles the customer_guilty path: unfreeze
// the escrow so the funds can be released to the vendor/rider via the
// normal escrow release cron.
func (s *AdminSurveillanceService) resolveDisputeCustomerGuilty(
	ctx context.Context, orderTrackingID, escrowStatus string,
) error {
	tx, err := s.dbWriter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin customer_guilty tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Unfreeze the escrow if still held
	if escrowStatus == "disputed" || escrowStatus == "held" {
		_, err = tx.Exec(ctx,
			"UPDATE escrow_holds SET status = 'held', dispute_id = NULL WHERE order_tracking_id = $1 AND status = 'disputed'",
			orderTrackingID,
		)
		if err != nil {
			return fmt.Errorf("failed to unfreeze escrow: %w", err)
		}
	}

	_, err = tx.Exec(ctx,
		"UPDATE disputes SET status = 'resolved', resolution = 'admin_customer_guilty', resolved_at = NOW(), updated_at = NOW() WHERE order_tracking_id = $1 AND status = 'open'",
		orderTrackingID,
	)
	if err != nil {
		return fmt.Errorf("failed to mark dispute resolved: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit customer_guilty resolution: %w", err)
	}
	return nil
}

// PayFastStats provides aggregated metrics on PayFast transactions
type PayFastStats struct {
	TotalCount     int     `json:"total_count"`
	PassedCount    int     `json:"passed_count"`
	FailedCount    int     `json:"failed_count"`
	InFlightCount  int     `json:"in_flight_count"`
	TotalVolume    float64 `json:"total_volume"`
	PassedVolume   float64 `json:"passed_volume"`
	FailedVolume   float64 `json:"failed_volume"`
	InFlightVolume float64 `json:"in_flight_volume"`
}

// PayFastTransactionItem represents a detailed PayFast transaction record for the admin panel
type PayFastTransactionItem struct {
	TransactionID string  `json:"transaction_id"`
	OrderID       string  `json:"order_id"`
	GatewayTxnID  string  `json:"gateway_txn_id"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	Status        string  `json:"status"`
	ErrorMessage  string  `json:"error_message"`
	CustomerName  string  `json:"customer_name"`
	CustomerPhone string  `json:"customer_phone"`
	EscrowStatus  string  `json:"escrow_status"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// GetPayFastTransactionSummary aggregates statistics of all PayFast payments.
func (s *AdminSurveillanceService) GetPayFastTransactionSummary(ctx context.Context) (*PayFastStats, error) {
	query := `
		SELECT 
			COUNT(*) as total_count,
			COUNT(*) FILTER (WHERE status = 'captured') as passed_count,
			COUNT(*) FILTER (WHERE status = 'failed') as failed_count,
			COUNT(*) FILTER (WHERE status IN ('pending', 'processing', '3ds_required', 'settlement_pending', 'gateway_pending')) as in_flight_count,
			COALESCE(SUM(amount), 0)::float8 as total_volume,
			COALESCE(SUM(amount) FILTER (WHERE status = 'captured'), 0)::float8 as passed_volume,
			COALESCE(SUM(amount) FILTER (WHERE status = 'failed'), 0)::float8 as failed_volume,
			COALESCE(SUM(amount) FILTER (WHERE status IN ('pending', 'processing', '3ds_required', 'settlement_pending', 'gateway_pending')), 0)::float8 as in_flight_volume
		FROM payment_transactions
		WHERE gateway = 'payfast'
	`
	var stats PayFastStats
	err := s.dbReader.QueryRow(ctx, query).Scan(
		&stats.TotalCount,
		&stats.PassedCount,
		&stats.FailedCount,
		&stats.InFlightCount,
		&stats.TotalVolume,
		&stats.PassedVolume,
		&stats.FailedVolume,
		&stats.InFlightVolume,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch PayFast summary: %w", err)
	}
	return &stats, nil
}

// GetPayFastTransactions fetches a paginated, filterable list of PayFast transactions.
func (s *AdminSurveillanceService) GetPayFastTransactions(ctx context.Context, statusFilter string, limit, offset int) ([]PayFastTransactionItem, int, error) {
	baseWhere := "WHERE pt.gateway = 'payfast'"
	if statusFilter == "passed" || statusFilter == "captured" {
		baseWhere += " AND pt.status = 'captured'"
	} else if statusFilter == "failed" {
		baseWhere += " AND pt.status = 'failed'"
	} else if statusFilter == "in_flight" || statusFilter == "pending" {
		baseWhere += " AND pt.status IN ('pending', 'processing', '3ds_required', 'settlement_pending', 'gateway_pending')"
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM payment_transactions pt %s", baseWhere)
	var total int
	if err := s.dbReader.QueryRow(ctx, countQuery).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count PayFast transactions: %w", err)
	}

	query := fmt.Sprintf(`
		SELECT 
			pt.transaction_id,
			pt.order_tracking_id,
			COALESCE(pt.gateway_txn_id, ''),
			COALESCE(pt.amount, 0)::float8,
			pt.currency,
			pt.status,
			COALESCE(pt.error_message, ''),
			COALESCE(u.full_name, 'Customer'),
			COALESCE(u.phone, ''),
			COALESCE(e.status, 'none'),
			to_char(pt.created_at, 'YYYY-MM-DD HH24:MI:SS'),
			to_char(pt.updated_at, 'YYYY-MM-DD HH24:MI:SS')
		FROM payment_transactions pt
		LEFT JOIN orders o ON o.order_tracking_id = pt.order_tracking_id
		LEFT JOIN users u ON u.tracking_id = o.customer_tracking_id
		LEFT JOIN escrow_holds e ON e.order_tracking_id = pt.order_tracking_id
		%s
		ORDER BY pt.created_at DESC
		LIMIT $1 OFFSET $2
	`, baseWhere)

	rows, err := s.dbReader.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query PayFast transactions: %w", err)
	}
	defer rows.Close()

	var list []PayFastTransactionItem
	for rows.Next() {
		var item PayFastTransactionItem
		if err := rows.Scan(
			&item.TransactionID,
			&item.OrderID,
			&item.GatewayTxnID,
			&item.Amount,
			&item.Currency,
			&item.Status,
			&item.ErrorMessage,
			&item.CustomerName,
			&item.CustomerPhone,
			&item.EscrowStatus,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		list = append(list, item)
	}
	return list, total, rows.Err()
}

// AdminOrderItem represents a single order for the admin orders listing.
type AdminOrderItem struct {
	ID                  int64    `json:"id"`
	OrderTrackingID     string   `json:"order_tracking_id"`
	CustomerTrackingID  string   `json:"customer_tracking_id"`
	VendorTrackingID    string   `json:"vendor_tracking_id"`
	StoreTrackingID     string   `json:"store_tracking_id"`
	RiderTrackingID     string   `json:"rider_tracking_id"`
	Status              string   `json:"status"`
	PaymentGateway      string   `json:"payment_gateway"`
	PaymentStatus       string   `json:"payment_status"`
	TotalAmount         float64  `json:"total_amount"`
	Currency            string   `json:"currency"`
	AdminCommission     float64  `json:"admin_commission"`
	VendorEscrow        float64  `json:"vendor_escrow"`
	DeliveryEscrow      float64  `json:"delivery_escrow"`
	EscrowReleased      bool     `json:"escrow_released"`
	DisputeStatus       string   `json:"dispute_status"`
	DeliveredAt         *string  `json:"delivered_at,omitempty"`
	CreatedAt           string   `json:"created_at"`
	UpdatedAt           string   `json:"updated_at"`
	CustomerName        string   `json:"customer_name"`
	CustomerPhone       string   `json:"customer_phone"`
	CustomerLat         float64  `json:"customer_lat"`
	CustomerLng         float64  `json:"customer_lng"`
	RiderName           string   `json:"rider_name"`
	RiderPhone          string   `json:"rider_phone"`
	StoreName           string   `json:"store_name"`
	StoreLat            float64  `json:"store_lat"`
	StoreLng            float64  `json:"store_lng"`
}

// GetAllOrders returns all orders with optional status filter and pagination.
func (s *AdminSurveillanceService) GetAllOrders(ctx context.Context, status string, limit, offset int) ([]AdminOrderItem, int, error) {
	countQuery := "SELECT COUNT(*) FROM orders"
	args := []any{}
	if status != "" {
		countQuery += " WHERE status = $1"
		args = append(args, status)
	}

	var total int
	if err := s.dbReader.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("failed to count orders: %w", err)
	}

	query := `SELECT o.id, o.order_tracking_id, o.customer_tracking_id, o.vendor_tracking_id,
		o.store_tracking_id, COALESCE(o.rider_tracking_id,''), o.status,
		COALESCE(o.payment_gateway,''), COALESCE(o.payment_status,'pending'),
		o.total_amount, o.currency, o.admin_commission, o.vendor_escrow, o.delivery_escrow,
		o.escrow_released, o.dispute_status,
		CASE WHEN o.delivered_at IS NOT NULL THEN o.delivered_at::text END,
		o.created_at::text, o.updated_at::text,
		COALESCE(u.first_name,'') || ' ' || COALESCE(u.last_name,'') as customer_name,
		COALESCE(u.phone,''),
		COALESCE(o.customer_lat, 0), COALESCE(o.customer_lng, 0),
		COALESCE(ru.first_name,'') || ' ' || COALESCE(ru.last_name,'') as rider_name,
		COALESCE(ru.phone,''),
		COALESCE(s.store_name,'Unknown'),
		COALESCE(s.latitude, 0), COALESCE(s.longitude, 0)
		FROM orders o
		LEFT JOIN users u ON o.customer_tracking_id = u.tracking_id
		LEFT JOIN users ru ON o.rider_tracking_id = ru.tracking_id
		LEFT JOIN stores s ON o.store_tracking_id = s.store_tracking_id`
	if status != "" {
		query += " WHERE status = $1"
		query += " ORDER BY created_at DESC LIMIT $2 OFFSET $3"
	} else {
		query += " ORDER BY created_at DESC LIMIT $1 OFFSET $2"
	}

	var rows_data []any
	if status != "" {
		rows_data = []any{status, limit, offset}
	} else {
		rows_data = []any{limit, offset}
	}

	rows, err := s.dbReader.Query(ctx, query, rows_data...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query orders: %w", err)
	}
	defer rows.Close()

	var list []AdminOrderItem
	for rows.Next() {
		var item AdminOrderItem
		if err := rows.Scan(
			&item.ID, &item.OrderTrackingID, &item.CustomerTrackingID,
			&item.VendorTrackingID, &item.StoreTrackingID, &item.RiderTrackingID,
			&item.Status, &item.PaymentGateway, &item.PaymentStatus,
			&item.TotalAmount, &item.Currency, &item.AdminCommission,
			&item.VendorEscrow, &item.DeliveryEscrow, &item.EscrowReleased,
			&item.DisputeStatus, &item.DeliveredAt, &item.CreatedAt, &item.UpdatedAt,
			&item.CustomerName, &item.CustomerPhone, &item.CustomerLat, &item.CustomerLng,
			&item.RiderName, &item.RiderPhone,
			&item.StoreName, &item.StoreLat, &item.StoreLng,
		); err != nil {
			return nil, 0, err
		}
		list = append(list, item)
	}
	return list, total, rows.Err()
}
