package admin

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/omnigo/backend/internal/ledger"
	tb "github.com/tigerbeetle/tigerbeetle-go"
)

// AdminLineageReport maps the complete multi-entity tracking chain for the auditor
type AdminLineageReport struct {
	OrderID        string  `json:"order_id"`
	OrderStatus    string  `json:"order_status"`
	TotalAmount    float64 `json:"total_amount"`
	CustomerID     string  `json:"customer_id"`
	CustomerName   string  `json:"customer_name"`
	StoreID        string  `json:"store_id"`
	StoreName      string  `json:"store_name"`
	ProductID      string  `json:"product_id"`
	ProductName    string  `json:"product_name"`
	RiderID        string  `json:"rider_id"`
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
	StoreID        string          `json:"store_id"`
	StoreName      string          `json:"store_name"`
	RiderID        string          `json:"rider_id"`
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
	dbReader    *pgxpool.Pool
	neo4jDriver neo4j.DriverWithContext
	neo4jDb     string
	tbService   *ledger.Service
}

func NewAdminSurveillanceService(dbReader *pgxpool.Pool, driver neo4j.DriverWithContext, dbName string, tbService *ledger.Service) *AdminSurveillanceService {
	return &AdminSurveillanceService{
		dbReader:    dbReader,
		neo4jDriver: driver,
		neo4jDb:     dbName,
		tbService:   tbService,
	}
}

// GetCompleteOrderLineage audits the exact tracking mesh across components.
// Uses tracking_id-based column names matching the Go-aligned init.sql (Session 16).
func (s *AdminSurveillanceService) GetCompleteOrderLineage(ctx context.Context, orderTrackingID string) (*AdminLineageReport, error) {
	traceID, ok := ctx.Value("trace_id").(string)
	if !ok {
		traceID = "ORPHAN-TRACE"
	}

	log.Printf("[SECURITY-AUDIT] [TraceID: %s] INITIATING E2E Lineage Sweep for Order: %s", traceID, orderTrackingID)

	// Fixed query: uses order_tracking_id (not tracking_id), store_tracking_id
	// (not tracking_id on stores), and column names from the canonical baseline
	// schema. Rider is resolved from orders.rider_tracking_id first, falling back
	// to the linked delivery assignment.
	query := `
		SELECT
			o.order_tracking_id, o.status, o.total_amount,
			o.customer_tracking_id,
			COALESCE(s.store_name, 'N/A'),
			COALESCE(o.rider_tracking_id, d.rider_tracking_id),
			COALESCE(d.status, 'PENDING'),
			(SELECT oi.product_tracking_id FROM order_items oi WHERE oi.order_tracking_id = o.order_tracking_id LIMIT 1),
			(SELECT p.name FROM products p JOIN order_items oi ON oi.product_tracking_id = p.product_tracking_id WHERE oi.order_tracking_id = o.order_tracking_id LIMIT 1)
		FROM orders o
		LEFT JOIN stores s ON o.store_tracking_id = s.store_tracking_id
		LEFT JOIN deliveries d ON o.order_tracking_id = d.order_tracking_id
		WHERE o.order_tracking_id = $1
		LIMIT 1
	`

	var r AdminLineageReport
	var riderID *string
	var deliveryStatus *string

	err := s.dbReader.QueryRow(ctx, query, orderTrackingID).Scan(
		&r.OrderID, &r.OrderStatus, &r.TotalAmount,
		&r.CustomerID,
		&r.StoreName,
		&riderID, &deliveryStatus,
		&r.ProductID, &r.ProductName,
	)
	if err != nil {
		return nil, fmt.Errorf("relational lineage fetch failed: %w", err)
	}

	if riderID != nil && *riderID != "" {
		r.RiderID = *riderID
	} else {
		r.RiderID = "UNASSIGNED"
	}
	if deliveryStatus != nil && *deliveryStatus != "" {
		r.DeliveryStatus = *deliveryStatus
	} else {
		r.DeliveryStatus = "PENDING"
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
		SELECT tracking_id, email, COALESCE(full_name, ''), role, COALESCE(phone, ''), COALESCE(business_name, ''), COALESCE(address, ''), COALESCE(cnic_url, ''), COALESCE(license_url, '')
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
		if err := rows.Scan(&u.TrackingID, &u.Email, &u.FullName, &u.Role, &u.Phone, &u.BusinessName, &u.Address, &u.CNICURL, &u.LicenseURL); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// ApproveUser verifies a user (rider/vendor) so they can log in.
func (s *AdminSurveillanceService) ApproveUser(ctx context.Context, trackingID string) error {
	query := `UPDATE users SET is_verified = true, updated_at = NOW() WHERE tracking_id = $1`
	res, err := s.dbReader.Exec(ctx, query, trackingID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// GetLedgerKPIs fetches the current balances of the global platform accounts from TigerBeetle.
func (s *AdminSurveillanceService) GetLedgerKPIs(ctx context.Context) (*FinancialKPIs, error) {
	if s.tbService == nil {
		return nil, fmt.Errorf("TigerBeetle service not configured")
	}

	accountIDs := []tb.Uint128{
		ledger.AccountToUint128(ledger.AccountAdminRevenue),
		ledger.AccountToUint128(ledger.AccountCentralEscrow),
		ledger.AccountToUint128(ledger.AccountVendorWallet),
		ledger.AccountToUint128(ledger.AccountCashReceivable),
	}

	// Wait, TBService in ledger package expects tb.Uint128. I need to check the type.
	// Since I can't import tb in this signature without adding tigerbeetle dependency here,
	// I will just let the ledger package handle it.
	// Wait, I will use GetAccountBalances directly.
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

		// To match ID exactly, we compare:
		if acc.ID == ledger.AccountToUint128(ledger.AccountAdminRevenue) {
			kpis.AdminRevenue = bal
		} else if acc.ID == ledger.AccountToUint128(ledger.AccountCentralEscrow) {
			kpis.CentralEscrow = bal
		} else if acc.ID == ledger.AccountToUint128(ledger.AccountVendorWallet) {
			kpis.VendorLiability = bal
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
			COALESCE(u.full_name, 'Unknown Customer'), 
			o.total_amount, 
			o.payment_gateway, 
			o.status, 
			o.created_at
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

// GetDailyRevenue aggregates completed orders grouped by day
func (s *AdminSurveillanceService) GetDailyRevenue(ctx context.Context, days int, paymentMethod string) ([]DailyRevenue, error) {
	// Filter by payment method if specified
	paymentFilter := ""
	args := []interface{}{days}

	if paymentMethod != "" && paymentMethod != "all" {
		paymentFilter = "AND payment_gateway = $2"
		args = append(args, paymentMethod)
	}

	query := fmt.Sprintf(`
		SELECT 
			DATE(created_at) as date, 
			SUM(total_amount) as gross_volume, 
			COUNT(order_tracking_id) as order_count
		FROM orders 
		WHERE status = 'completed'
		  AND created_at >= NOW() - ($1 || ' days')::INTERVAL
		  %s
		GROUP BY DATE(created_at)
		ORDER BY DATE(created_at) ASC
	`, paymentFilter)

	rows, err := s.dbReader.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate daily revenue: %w", err)
	}
	defer rows.Close()

	var records []DailyRevenue
	for rows.Next() {
		var r DailyRevenue
		var t time.Time
		err := rows.Scan(&t, &r.GrossVolume, &r.OrderCount)
		if err != nil {
			return nil, err
		}
		r.Date = t.Format("2006-01-02")
		// Calculate platform revenue
		var platformRev float64
		errRev := s.dbReader.QueryRow(ctx, `SELECT COALESCE(SUM(amount), 0) FROM ledger_entries WHERE account = 'admin_revenue_account' AND amount > 0 AND created_at::date = $1`, t).Scan(&platformRev)
		if errRev != nil {
			log.Printf("[WARNING] Failed to query exact platform revenue for date %v: %v", t, errRev)
			rate := 0.10
			if envRate := os.Getenv("ANALYTICS_ESTIMATE_RATE"); envRate != "" {
				if parsed, err := strconv.ParseFloat(envRate, 64); err == nil {
					rate = parsed
				}
			}
			r.PlatformRevenue = r.GrossVolume * rate
		} else {
			r.PlatformRevenue = platformRev
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
			SELECT tracking_id, email, COALESCE(full_name, ''), role, COALESCE(phone, ''), COALESCE(business_name, ''), COALESCE(address, ''), COALESCE(cnic_url, ''), COALESCE(license_url, '')
			FROM users WHERE role = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3
		`
		args = append(args, role)
	} else {
		countQuery = `SELECT COUNT(*) FROM users`
		query = `
			SELECT tracking_id, email, COALESCE(full_name, ''), role, COALESCE(phone, ''), COALESCE(business_name, ''), COALESCE(address, ''), COALESCE(cnic_url, ''), COALESCE(license_url, '')
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
		if err := rows.Scan(&u.TrackingID, &u.Email, &u.FullName, &u.Role, &u.Phone, &u.BusinessName, &u.Address, &u.CNICURL, &u.LicenseURL); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// GetFullOrderLineage returns order items, delivery gig, ride, and timeline events.
func (s *AdminSurveillanceService) GetFullOrderLineage(ctx context.Context, orderTrackingID string) (*FullLineageReport, error) {
	traceID, ok := ctx.Value("trace_id").(string)
	if !ok {
		traceID = "ORPHAN-TRACE"
	}
	log.Printf("[SECURITY-AUDIT] [TraceID: %s] Full lineage sweep for order: %s", traceID, orderTrackingID)

	query := `
		SELECT
			o.order_tracking_id, o.status, o.total_amount,
			o.customer_tracking_id,
			COALESCE(c.full_name, ''),
			o.store_tracking_id,
			COALESCE(s.store_name, 'N/A'),
			COALESCE(o.rider_tracking_id, d.rider_tracking_id),
			d.tracking_id,
			COALESCE(d.status, 'PENDING')
		FROM orders o
		LEFT JOIN users c ON o.customer_tracking_id = c.tracking_id
		LEFT JOIN stores s ON o.store_tracking_id = s.store_tracking_id
		LEFT JOIN deliveries d ON o.order_tracking_id = d.order_tracking_id
		WHERE o.order_tracking_id = $1
		LIMIT 1
	`
	var r FullLineageReport
	var riderID, deliveryID, deliveryStatus *string
	err := s.dbReader.QueryRow(ctx, query, orderTrackingID).Scan(
		&r.OrderID, &r.OrderStatus, &r.TotalAmount,
		&r.CustomerID, &r.CustomerName,
		&r.StoreID, &r.StoreName,
		&riderID, &deliveryID, &deliveryStatus,
	)
	if err != nil {
		return nil, fmt.Errorf("full lineage fetch failed: %w", err)
	}
	if riderID != nil && *riderID != "" {
		r.RiderID = *riderID
	}
	if deliveryID != nil {
		r.DeliveryID = *deliveryID
	}
	if deliveryStatus != nil && *deliveryStatus != "" {
		r.DeliveryStatus = *deliveryStatus
	} else {
		r.DeliveryStatus = "PENDING"
	}
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
// If vendor_guilty or rider_guilty -> refund to customer wallet.
// If customer_guilty -> release funds to vendor/rider.
func (s *AdminSurveillanceService) ResolveDispute(ctx context.Context, orderTrackingID string, decision string) error {
	// First fetch the escrow record
	var escrowAmount float64
	var customerID string
	query := `
		SELECT e.amount, o.customer_tracking_id
		FROM escrow_holds e
		JOIN orders o ON e.order_tracking_id = o.order_tracking_id
		WHERE e.order_tracking_id = $1 AND e.status = 'disputed'
	`
	err := s.dbReader.QueryRow(ctx, query, orderTrackingID).Scan(&escrowAmount, &customerID)
	if err != nil {
		return fmt.Errorf("escrow not found or not frozen: %w", err)
	}

	if decision == "vendor_guilty" || decision == "rider_guilty" {
		// Refund to customer wallet!
		idempotencyKey := fmt.Sprintf("dispute_refund:%s", orderTrackingID)

		if s.tbService != nil {
			if _, err := s.tbService.Transfer(ctx, ledger.TransferRequest{
				DebitAccount:   ledger.AccountCentralEscrow,
				CreditAccount:  ledger.AccountCustomerWallet,
				Amount:         escrowAmount,
				Currency:       "PKR",
				ReferenceType:  "dispute_refund",
				ReferenceID:    orderTrackingID,
				Description:    fmt.Sprintf("Refund for disputed order %s", orderTrackingID),
				IdempotencyKey: idempotencyKey,
			}); err != nil {
				return fmt.Errorf("ledger refund transfer failed: %w", err)
			}
		}

		// Mark as refunded and close the linked dispute.
		_, err = s.dbReader.Exec(ctx, "UPDATE escrow_holds SET status = 'refunded', released_at = NOW() WHERE order_tracking_id = $1", orderTrackingID)
		if err != nil {
			return err
		}
		_, _ = s.dbReader.Exec(ctx, "UPDATE disputes SET status = 'resolved', resolution = 'admin_refund', updated_at = NOW() WHERE order_tracking_id = $1 AND status = 'open'", orderTrackingID)

		// Credit the customer wallet directly so the refund is immediately usable.
		upsertWallet := `
			INSERT INTO customer_wallet (customer_tracking_id, balance, updated_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT (customer_tracking_id) DO UPDATE SET balance = customer_wallet.balance + $2, updated_at = NOW()
		`
		_, err = s.dbReader.Exec(ctx, upsertWallet, customerID, escrowAmount)
		return err

	} else if decision == "customer_guilty" {
		// Unfreeze the escrow, let the cron job settle it
		_, err = s.dbReader.Exec(ctx, "UPDATE escrow_holds SET status = 'held', dispute_id = NULL WHERE order_tracking_id = $1", orderTrackingID)
		if err != nil {
			return err
		}
		_, _ = s.dbReader.Exec(ctx, "UPDATE disputes SET status = 'resolved', resolution = 'admin_customer_guilty', updated_at = NOW() WHERE order_tracking_id = $1 AND status = 'open'", orderTrackingID)
		return nil
	}

	return fmt.Errorf("invalid dispute decision: %s", decision)
}
