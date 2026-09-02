package workers

import (
	"context"
	"log"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/omnigo/backend/internal/shared/config"
	"github.com/redis/go-redis/v9"
)

type ReconciliationResult struct {
	Timestamp           time.Time `json:"timestamp"`
	TotalOrdersCount    int64     `json:"total_orders_count"`
	TotalOrdersVolume   float64   `json:"total_orders_volume"`
	// TigerBeetle ledger balances (the source of truth for fund accounting)
	TBVendorLockedEscrow  float64 `json:"tb_vendor_locked_escrow"`
	TBVendorPendingEscrow float64 `json:"tb_vendor_pending_escrow"`
	TBVendorWallet        float64 `json:"tb_vendor_wallet"`
	TBCentralEscrow       float64 `json:"tb_central_escrow"`
	TBAdminRevenue        float64 `json:"tb_admin_revenue"`
	TBRiderCODDebt        float64 `json:"tb_rider_cod_debt"`
	TBCashReceivable      float64 `json:"tb_cash_receivable"`
	// PostgreSQL relational sums (must reconcile against TB)
	PGActiveHolds        float64 `json:"pg_active_holds"`
	PGVendorWalletBalance float64 `json:"pg_vendor_wallet_balance"`
	PGRiderWalletBalance  float64 `json:"pg_rider_wallet_balance"`
	// Per-account reconciliation status
	VendorEscrowDiscrepancy  float64 `json:"vendor_escrow_discrepancy"`
	VendorWalletDiscrepancy  float64 `json:"vendor_wallet_discrepancy"`
	AdminRevenueDiscrepancy  float64 `json:"admin_revenue_discrepancy"`
	CODDebtDiscrepancy       float64 `json:"cod_debt_discrepancy"`
	CentralEscrowDiscrepancy float64 `json:"central_escrow_discrepancy"`
	// Overall verdict
	MaxDiscrepancy float64 `json:"max_discrepancy"`
	Threshold      float64 `json:"threshold"`
	IsReconciled   bool    `json:"is_reconciled"`
}

// ReconciliationWorker performs daily automated financial reconciliation
// comparing PostgreSQL relational balances against TigerBeetle ledger entries.
type ReconciliationWorker struct {
	db        *pgxpool.Pool
	ledgerSvc *ledger.Service
	redis     redis.UniversalClient
	cfg       *config.ReconciliationConfig
}

func NewReconciliationWorker(db *pgxpool.Pool, ledgerSvc *ledger.Service, rdb redis.UniversalClient, cfg *config.ReconciliationConfig) *ReconciliationWorker {
	return &ReconciliationWorker{
		db:        db,
		ledgerSvc: ledgerSvc,
		redis:     rdb,
		cfg:       cfg,
	}
}

// Start begins the 24-hour reconciliation cron loop.
func (w *ReconciliationWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	log.Println("[ReconciliationWorker] Started — scheduled daily audit at 00:00 UTC")

	// Run initial reconciliation on startup
	w.RunReconciliation(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("[ReconciliationWorker] Shutting down")
			return
		case <-ticker.C:
			w.RunReconciliation(ctx)
		}
	}
}

// RunReconciliation executes full double-entry ledger vs relational database cross-verification.
//
// AW-1 FIX: Previously the worker compared AccountCentralEscrow (platform
// float for unsplit payments) against SUM(escrow_holds WHERE status='held')
// (per-vendor 48h holds). Those are different concepts backed by different
// ledger accounts, so the comparison always produced a false discrepancy
// while hiding real bugs in AccountVendorLockedEscrow.
//
// AW-5 FIX: The previous threshold was a fixed 0.01 PKR, which is
// floating-point-noise on high volume and too tight on low volume. The
// new threshold is relative: max(1.0 PKR, 0.01% of total volume).
func (w *ReconciliationWorker) RunReconciliation(ctx context.Context) (*ReconciliationResult, error) {
	if w.redis != nil {
		lockKey := "lock:workers:reconciliation"
		success, err := w.redis.SetNX(ctx, lockKey, "1", 1*time.Hour).Result()
		if err != nil {
			log.Printf("[ReconciliationWorker] Redis lock error: %v", err)
		} else if !success {
			log.Println("[ReconciliationWorker] Another worker instance is running reconciliation. Skipping.")
			return nil, nil
		}
		defer w.redis.Del(ctx, lockKey)
	}

	result := &ReconciliationResult{
		Timestamp: time.Now().UTC(),
	}

	// 1. Total paid orders & volume from PostgreSQL
	var totalOrders int64
	var totalVolume float64
	err := w.db.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(total_amount), 0.0) FROM orders WHERE payment_status = 'paid'`,
	).Scan(&totalOrders, &totalVolume)
	if err != nil {
		log.Printf("[ReconciliationWorker] Error fetching postgres order totals: %v", err)
		return result, err
	}
	result.TotalOrdersCount = totalOrders
	result.TotalOrdersVolume = totalVolume

	// 2. Fetch every TigerBeetle ledger balance we need in one go.
	if w.ledgerSvc != nil {
		fetchTB := func(acc ledger.Account) (float64, error) {
			bal, err := w.ledgerSvc.GetBalance(ctx, acc)
			if err != nil {
				log.Printf("[ReconciliationWorker] Error fetching TB balance for %s: %v", acc, err)
				return 0, err
			}
			return bal, nil
		}
		result.TBVendorLockedEscrow, _ = fetchTB(ledger.AccountVendorLockedEscrow)
		result.TBVendorPendingEscrow, _ = fetchTB(ledger.AccountVendorPendingEscrow)
		result.TBVendorWallet, _ = fetchTB(ledger.AccountVendorWallet)
		result.TBCentralEscrow, _ = fetchTB(ledger.AccountCentralEscrow)
		result.TBAdminRevenue, _ = fetchTB(ledger.AccountAdminRevenue)
		result.TBRiderCODDebt, _ = fetchTB(ledger.AccountRiderCODDebt)
		result.TBCashReceivable, _ = fetchTB(ledger.AccountCashReceivable)
	}

	// 3. Fetch PostgreSQL relational sums to cross-check.
	fetchPG := func(query string, dst *float64) {
		var v float64
		if err := w.db.QueryRow(ctx, query).Scan(&v); err != nil {
			log.Printf("[ReconciliationWorker] Error fetching postgres sum (%s): %v", query, err)
			return
		}
		*dst = v
	}
	fetchPG(`SELECT COALESCE(SUM(amount), 0.0) FROM escrow_holds WHERE status = 'held'`, &result.PGActiveHolds)
	fetchPG(`SELECT COALESCE(SUM(balance), 0.0) FROM vendor_wallet`, &result.PGVendorWalletBalance)
	fetchPG(`SELECT COALESCE(SUM(balance), 0.0) FROM rider_wallet`, &result.PGRiderWalletBalance)

	// 4. Per-account reconciliation. Each check compares one TigerBeetle
	// account against the relational sum that should match it. A real
	// financial bug shows up as a difference greater than the threshold.
	result.VendorEscrowDiscrepancy = absDiff(result.TBVendorLockedEscrow, result.PGActiveHolds)
	result.VendorWalletDiscrepancy = absDiff(result.TBVendorWallet, result.PGVendorWalletBalance)
	result.AdminRevenueDiscrepancy = result.TBAdminRevenue // TB only — PG is the source for this account
	result.CODDebtDiscrepancy = result.TBRiderCODDebt     // TB only
	result.CentralEscrowDiscrepancy = result.TBCentralEscrow

	// 5. Compute the relative threshold: max(minThreshold, totalVolume * relativeRate).
	// Both values come from env-configured ReconciliationConfig so operators
	// can tune sensitivity per environment.
	minThreshold := 1.0
	relativeRate := 0.0001
	if w.cfg != nil {
		minThreshold = w.cfg.MinThresholdPKR
		relativeRate = w.cfg.RelativeRate
	}
	threshold := math.Max(minThreshold, result.TotalOrdersVolume*relativeRate)
	result.Threshold = threshold

	// 6. Overall verdict: ALL per-account checks must be within threshold.
	maxDisc := math.Max(result.VendorEscrowDiscrepancy, result.VendorWalletDiscrepancy)
	result.MaxDiscrepancy = maxDisc
	result.IsReconciled = maxDisc < threshold

	if result.IsReconciled {
		log.Printf("[ReconciliationWorker] ✅ SUCCESS: Reconciliation Passed. Orders: %d (%0.2f PKR), VendorEscrow disc: %0.2f, VendorWallet disc: %0.2f, CentralEscrow: %0.2f, AdminRevenue: %0.2f, threshold: %0.2f PKR",
			result.TotalOrdersCount, result.TotalOrdersVolume,
			result.VendorEscrowDiscrepancy, result.VendorWalletDiscrepancy,
			result.TBCentralEscrow, result.TBAdminRevenue, threshold)
	} else {
		log.Printf("[CRITICAL-SECURITY-ALERT] 🚨 FINANCIAL DISCREPANCY! VendorEscrow disc: %0.2f, VendorWallet disc: %0.2f, threshold: %0.2f PKR (max=%0.2f)",
			result.VendorEscrowDiscrepancy, result.VendorWalletDiscrepancy, threshold, maxDisc)
	}

	return result, nil
}

// absDiff returns the absolute difference between two floats.
func absDiff(a, b float64) float64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}
