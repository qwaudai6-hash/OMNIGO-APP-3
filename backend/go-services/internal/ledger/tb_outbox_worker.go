package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tb "github.com/tigerbeetle/tigerbeetle-go"
)

// TBOutboxWorker polls the tigerbeetle_outbox table and relays pending transfers to TigerBeetle.
type TBOutboxWorker struct {
	db           *pgxpool.Pool
	tbSvc        *TBService
	pollInterval time.Duration
	batchSize    int
}

// NewTBOutboxWorker creates a new outbox relay worker.
func NewTBOutboxWorker(db *pgxpool.Pool, tbSvc *TBService) *TBOutboxWorker {
	return &TBOutboxWorker{
		db:           db,
		tbSvc:        tbSvc,
		pollInterval: 2 * time.Second,
		batchSize:    50,
	}
}

// Start begins the outbox relay loop. It runs until the context is cancelled.
func (w *TBOutboxWorker) Start(ctx context.Context) {
	log.Println("[TBOutboxWorker] Started — polling every", w.pollInterval)
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	// Process any backlog immediately on startup
	w.processBatch(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("[TBOutboxWorker] Shutting down")
			return
		case <-ticker.C:
			w.processBatch(ctx)
		}
	}
}

func (w *TBOutboxWorker) processBatch(ctx context.Context) {
	entries, err := FetchPendingTBOutbox(ctx, w.db, w.batchSize)
	if err != nil {
		log.Printf("[TBOutboxWorker] Error fetching pending: %v", err)
		return
	}
	for _, entry := range entries {
		if err := w.processEntry(ctx, entry); err != nil {
			log.Printf("[TBOutboxWorker] Error processing entry %d (tx %s): %v", entry.ID, entry.TransactionID, err)
		}
	}
}

func (w *TBOutboxWorker) processEntry(ctx context.Context, entry TBOutboxEntry) error {
	var transfers []tb.Transfer
	if err := json.Unmarshal(entry.Payload, &transfers); err != nil {
		MarkTBOutboxFailed(ctx, w.db, entry.ID, fmt.Sprintf("unmarshal: %v", err), entry.MaxRetries)
		return err
	}

	if err := w.tbSvc.CreateTransfers(transfers); err != nil {
		MarkTBOutboxFailed(ctx, w.db, entry.ID, err.Error(), entry.MaxRetries)
		return err
	}

	return MarkTBOutboxCompleted(ctx, w.db, entry.ID)
}
