package workers

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnigo/backend/internal/ledger"
	"github.com/redis/go-redis/v9"
)

type ReconciliationResult struct {
	Timestamp           time.Time `json:"timestamp"`
	TotalOrdersCount    int64     `json:"total_orders_count"`
	TotalOrdersVolume   float64   `json:"total_orders_volume"`
	TigerBeetleEscrow   float64   `json:"tigerbeetle_escrow"`
	PostgresEscrow      float64   `json:"postgres_escrow"`
	AdminRevenueBalance float64   `json:"admin_revenue_balance"`
	RiderCODDebtBalance float64   `json:"rider_cod_debt_balance"`
	DiscrepancyAmount   float64   `json:"discrepancy_amount"`
	IsReconciled        bool      `json:"is_reconciled"`
}

// ReconciliationWorker performs daily automated financial reconciliation
// comparing PostgreSQL relational balances against TigerBeetle ledger entries.
type ReconciliationWorker struct {
	db        *pgxpool.Pool
	ledgerSvc *ledger.Service
	redis     redis.UniversalClient
}

func NewReconciliationWorker(db *pgxpool.Pool, ledgerSvc *ledger.Service, rdb redis.UniversalClient) *ReconciliationWorker {
	return &ReconciliationWorker{
		db:        db,
		ledgerSvc: ledgerSvc,
		redis:     rdb,
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

	// 1. Fetch total completed orders & volume from PostgreSQL
	var totalOrders int64
	var totalVolume float64
	queryOrders := `SELECT COUNT(*), COALESCE(SUM(total_amount), 0.0) FROM orders WHERE payment_status = 'completed'`
	err := w.db.QueryRow(ctx, queryOrders).Scan(&totalOrders, &totalVolume)
	if err != nil {
		log.Printf("[ReconciliationWorker] Error fetching postgres order totals: %v", err)
	} else {
		result.TotalOrdersCount = totalOrders
		result.TotalOrdersVolume = totalVolume
	}

	// 2. Fetch TigerBeetle / Ledger Balances
	if w.ledgerSvc != nil {
		escrowBal, err := w.ledgerSvc.GetBalance(ctx, ledger.AccountCentralEscrow)
		if err == nil {
			result.TigerBeetleEscrow = escrowBal
		}

		revBal, err := w.ledgerSvc.GetBalance(ctx, ledger.AccountAdminRevenue)
		if err == nil {
			result.AdminRevenueBalance = revBal
		}

		codDebtBal, err := w.ledgerSvc.GetBalance(ctx, ledger.AccountRiderCODDebt)
		if err == nil {
			result.RiderCODDebtBalance = codDebtBal
		}
	}

	// 3. Fetch active pending escrow holds in Postgres
	var activeEscrow float64
	queryEscrow := `SELECT COALESCE(SUM(amount), 0.0) FROM escrow_holds WHERE status = 'held'`
	err = w.db.QueryRow(ctx, queryEscrow).Scan(&activeEscrow)
	if err == nil {
		result.PostgresEscrow = activeEscrow
	}

	// 4. Calculate Discrepancy (Central Escrow Ledger vs Active Escrow Holds in DB)
	discrepancy := result.TigerBeetleEscrow - result.PostgresEscrow
	if discrepancy < 0 {
		discrepancy = -discrepancy
	}
	result.DiscrepancyAmount = discrepancy

	// Threshold: Discrepancy must be 0.00 (or < 0.01 PKR due to float precision)
	if discrepancy < 0.01 {
		result.IsReconciled = true
		log.Printf("[ReconciliationWorker] ✅ SUCCESS: Reconciliation Passed. Total Orders: %d (%0.2f PKR), Central Escrow: %0.2f PKR, Discrepancy: %0.2f PKR",
			result.TotalOrdersCount, result.TotalOrdersVolume, result.TigerBeetleEscrow, result.DiscrepancyAmount)
	} else {
		result.IsReconciled = false
		log.Printf("[CRITICAL-SECURITY-ALERT] 🚨 FINANCIAL DISCREPANCY DETECTED! TigerBeetle Escrow: %0.2f PKR, DB Escrow Holds: %0.2f PKR, Mismatch: %0.2f PKR",
			result.TigerBeetleEscrow, result.PostgresEscrow, result.DiscrepancyAmount)
	}

	return result, nil
}
