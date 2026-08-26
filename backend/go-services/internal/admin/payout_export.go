package admin

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PayoutExportService generates 1LINK / 1IBFT standard corporate banking disbursement CSVs.
type PayoutExportService struct {
	db *pgxpool.Pool
}

func NewPayoutExportService(db *pgxpool.Pool) *PayoutExportService {
	return &PayoutExportService{db: db}
}

// Export1IBFTCSVPending generates a CSV file of all pending vendor payouts ready for corporate banking upload.
func (s *PayoutExportService) Export1IBFTCSVPending(ctx context.Context, batchID string) ([]byte, string, error) {
	query := `
		SELECT 
			vp.id,
			vp.batch_id,
			vp.vendor_tracking_id,
			COALESCE(u.full_name, u.business_name, 'Vendor'),
			COALESCE(u.address, 'Bank'),
			COALESCE(u.phone, ''),
			vp.amount,
			vp.status,
			vp.created_at
		FROM vendor_payouts vp
		LEFT JOIN users u ON vp.vendor_tracking_id = u.tracking_id
	`
	var args []interface{}
	if batchID != "" {
		query += ` WHERE vp.batch_id = $1 ORDER BY vp.created_at ASC`
		args = append(args, batchID)
	} else {
		// Include BOTH payout origins: 'pending_disbursement' (auto-swept
		// escrow batches from PayoutWorker) and 'pending' (vendor-initiated
		// withdrawals). Excluding 'pending' meant manually requested
		// withdrawals were debited from the wallet but never exported to the
		// bank file.
		query += ` WHERE vp.status IN ('pending', 'pending_disbursement') ORDER BY vp.created_at ASC`
	}

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("failed to query vendor payouts: %w", err)
	}
	defer rows.Close()

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	// Write 1LINK / 1IBFT Standard Corporate Disbursement Header
	header := []string{
		"Transaction Reference",
		"Batch ID",
		"Beneficiary Tracking ID",
		"Beneficiary Name",
		"Bank / Branch",
		"Beneficiary Phone / Account",
		"Amount (PKR)",
		"Currency",
		"Narration",
		"Value Date",
		"Disbursement Status",
	}
	if err := writer.Write(header); err != nil {
		return nil, "", err
	}

	totalRows := 0
	for rows.Next() {
		var id, bID, vendorID, name, bank, phone, status string
		var amount float64
		var createdAt time.Time

		if err := rows.Scan(&id, &bID, &vendorID, &name, &bank, &phone, &amount, &status, &createdAt); err != nil {
			return nil, "", err
		}

		record := []string{
			id,
			bID,
			vendorID,
			name,
			bank,
			phone,
			fmt.Sprintf("%.2f", amount),
			"PKR",
			fmt.Sprintf("Omnigo Vendor Payout - %s", vendorID),
			createdAt.Format("2006-01-02"),
			status,
		}
		if err := writer.Write(record); err != nil {
			return nil, "", err
		}
		totalRows++
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, "", fmt.Errorf("csv write error: %w", err)
	}

	filename := fmt.Sprintf("1ibft_payouts_%s.csv", time.Now().Format("20060102_150405"))
	return buf.Bytes(), filename, nil
}
