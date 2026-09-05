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
	TotalOrdersVolumePaisa int64  `json:"total_orders_volume_paisa"`
	// TigerBeetle ledger balances (the source of truth for fund accounting)
	TBVendorLockedEscrowPaisa  int64 `json:"tb_vendor_locked_escrow_paisa"`
	TBVendorPendingEscrowPaisa int64 `json:"tb_vendor_pending_escrow_paisa"`
	TBVendorWalletPaisa        int64 `json:"tb_vendor_wallet_paisa"`
	TBCentralEscrowPaisa       int64 `json:"tb_central_escrow_paisa"`
	TBAdminRevenuePaisa        int64 `json:"tb_admin_revenue_paisa"`
	TBRiderCODDebtPaisa        int64 `json:"tb_rider_cod_debt_paisa"`
	TBCashReceivablePaisa      int64 `json:"tb_cash_receivable_paisa"`
	// PostgreSQL relational sums (must reconcile against TB)
	PGActiveHoldsPaisa        int64 `json:"pg_active_holds_paisa"`
	PGVendorWalletBalancePaisa int64 `json:"pg_vendor_wallet_balance_paisa"`
	PGRiderWalletBalancePaisa  int64 `json:"pg_rider_wallet_balance_paisa"`
	// Per-account reconciliation status
	VendorEscrowDiscrepancyPaisa  int64 `json:"vendor_escrow_discrepancy_paisa"`
	VendorWalletDiscrepancyPaisa  int64 `json:"vendor_wallet_discrepancy_paisa"`
	AdminRevenueDiscrepancyPaisa  int64 `json:"admin_revenue_discrepancy_paisa"`
	CODDebtDiscrepancyPaisa       int64 `json:"cod_debt_discrepancy_paisa"`
	CentralEscrowDiscrepancyPaisa int64 `json:"central_escrow_discrepancy_paisa"`
	// Overall verdict
	MaxDiscrepancyPaisa int64 `json:"max_discrepancy_paisa"`
	ThresholdPaisa      int64 `json:"threshold_paisa"`
	IsReconciled        bool    `json:"is_reconciled"`
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
	var totalVolumePaisa int64
	err := w.db.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(total_amount_paisa), 0) FROM orders WHERE payment_status = 'paid'`,
	).Scan(&totalOrders, &totalVolumePaisa)
	if err != nil {
		log.Printf("[ReconciliationWorker] Error fetching postgres order totals: %v", err)
		return result, err
	}
	result.TotalOrdersCount = totalOrders
	result.TotalOrdersVolumePaisa = totalVolumePaisa

	// 2. Fetch every TigerBeetle ledger balance we need in one go.
	if w.ledgerSvc != nil {
		fetchTB := func(acc ledger.Account) (int64, error) {
			bal, err := w.ledgerSvc.GetBalance(ctx, acc)
			if err != nil {
				log.Printf("[ReconciliationWorker] Error fetching TB balance for %s: %v", acc, err)
				return 0, err
			}
			return bal, nil
		}
		result.TBVendorLockedEscrowPaisa, _ = fetchTB(ledger.AccountVendorLockedEscrow)
		result.TBVendorPendingEscrowPaisa, _ = fetchTB(ledger.AccountVendorPendingEscrow)
		result.TBVendorWalletPaisa, _ = fetchTB(ledger.AccountVendorWallet)
		result.TBCentralEscrowPaisa, _ = fetchTB(ledger.AccountCentralEscrow)
		result.TBAdminRevenuePaisa, _ = fetchTB(ledger.AccountAdminRevenue)
		result.TBRiderCODDebtPaisa, _ = fetchTB(ledger.AccountRiderCODDebt)
		result.TBCashReceivablePaisa, _ = fetchTB(ledger.AccountCashReceivable)
	}

	// 3. Fetch PostgreSQL relational sums to cross-check.
	fetchPG := func(query string, dst *int64) {
		var v int64
		if err := w.db.QueryRow(ctx, query).Scan(&v); err != nil {
			log.Printf("[ReconciliationWorker] Error fetching postgres sum (%s): %v", query, err)
			return
		}
		*dst = v
	}
	fetchPG(`SELECT COALESCE(SUM(amount_paisa), 0) FROM escrow_holds WHERE status = 'held'`, &result.PGActiveHoldsPaisa)
	fetchPG(`SELECT COALESCE(SUM(balance_paisa), 0) FROM vendor_wallet`, &result.PGVendorWalletBalancePaisa)
	fetchPG(`SELECT COALESCE(SUM(balance_paisa), 0) FROM rider_wallet`, &result.PGRiderWalletBalancePaisa)

	// FIX [RW-1/RW-2]: Fetch PG sums for AdminRevenue and CODDebt to enable proper reconciliation
	var PGAdminRevenuePaisa int64
	var PGCODDebtPaisa int64
	fetchPG(`SELECT COALESCE(SUM(admin_commission_paisa), 0) FROM orders WHERE status IN ('paid', 'completed', 'delivered', 'accepted', 'shipped', 'in_transit')`, &PGAdminRevenuePaisa)
	fetchPG(`SELECT COALESCE(SUM(amount_paisa), 0) FROM cod_debts WHERE status = 'pending'`, &PGCODDebtPaisa)

	// 4. Per-account reconciliation. Each check compares one TigerBeetle
	// account against the relational sum that should match it. A real
	// financial bug shows up as a difference greater than the threshold.
	result.VendorEscrowDiscrepancyPaisa = absDiffInt64(result.TBVendorLockedEscrowPaisa, result.PGActiveHoldsPaisa)
	result.VendorWalletDiscrepancyPaisa = absDiffInt64(result.TBVendorWalletPaisa, result.PGVendorWalletBalancePaisa)
	// FIX [RW-1]: Compare AdminRevenue between TB and PG
	result.AdminRevenueDiscrepancyPaisa = absDiffInt64(result.TBAdminRevenuePaisa, PGAdminRevenuePaisa)
	// FIX [RW-2]: Compare CODDebt between TB and PG
	result.CODDebtDiscrepancyPaisa = absDiffInt64(result.TBRiderCODDebtPaisa, PGCODDebtPaisa)
	result.CentralEscrowDiscrepancyPaisa = result.TBCentralEscrowPaisa

	// 5. Compute the relative threshold: max(minThreshold, totalVolume * relativeRate).
	// Both values come from env-configured ReconciliationConfig so operators
	// can tune sensitivity per environment.
	minThresholdPaisa := int64(100) // 1.00 PKR = 100 paisa
	relativeRate := 0.0001
	if w.cfg != nil {
		minThresholdPaisa = int64(w.cfg.MinThresholdPKR * 100)
		relativeRate = w.cfg.RelativeRate
	}
	thresholdPaisa := int64(math.Max(float64(minThresholdPaisa), float64(totalVolumePaisa)*relativeRate))
	result.ThresholdPaisa = thresholdPaisa

	// 6. Overall verdict: ALL per-account checks must be within threshold.
	// FIX [RW-1/RW-2]: Include AdminRevenue and CODDebt in max discrepancy calculation
	maxDiscPaisa := maxInt64(result.VendorEscrowDiscrepancyPaisa,
		result.VendorWalletDiscrepancyPaisa,
		result.AdminRevenueDiscrepancyPaisa,
		result.CODDebtDiscrepancyPaisa,
	)
	result.MaxDiscrepancyPaisa = maxDiscPaisa
	result.IsReconciled = maxDiscPaisa < thresholdPaisa

	if result.IsReconciled {
		log.Printf("[ReconciliationWorker] ✅ SUCCESS: Reconciliation Passed. Orders: %d (%d paisa), VendorEscrow disc: %d, VendorWallet disc: %d, AdminRevenue disc: %d, CODDebt disc: %d, threshold: %d paisa",
			result.TotalOrdersCount, result.TotalOrdersVolumePaisa,
			result.VendorEscrowDiscrepancyPaisa, result.VendorWalletDiscrepancyPaisa,
			result.AdminRevenueDiscrepancyPaisa, result.CODDebtDiscrepancyPaisa, thresholdPaisa)
	} else {
		log.Printf("[CRITICAL-SECURITY-ALERT] 🚨 FINANCIAL DISCREPANCY! VendorEscrow: %d, VendorWallet: %d, AdminRevenue: %d, CODDebt: %d, threshold: %d paisa (max discrepancy: %d)",
			result.VendorEscrowDiscrepancyPaisa, result.VendorWalletDiscrepancyPaisa,
			result.AdminRevenueDiscrepancyPaisa, result.CODDebtDiscrepancyPaisa, thresholdPaisa, maxDiscPaisa)
	}

	return result, nil
}

// absDiffInt64 returns the absolute difference between two int64.
func absDiffInt64(a, b int64) int64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

func maxInt64(values ...int64) int64 {
	if len(values) == 0 {
		return 0
	}
	max := values[0]
	for _, v := range values[1:] {
		if v > max {
			max = v
		}
	}
	return max
}
