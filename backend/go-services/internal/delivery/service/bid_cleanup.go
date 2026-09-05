package service

import (
	"context"
	"log"
	"time"
)

// BidCleanupWorker runs periodically to remove expired bid data from Redis.
// This prevents memory leaks from abandoned bids that were never accepted/cancelled.
type BidCleanupWorker struct {
	bidTTL    *BidTTLManager
	interval  time.Duration
}

func NewBidCleanupWorker(bidTTL *BidTTLManager) *BidCleanupWorker {
	return &BidCleanupWorker{
		bidTTL:   bidTTL,
		interval: 5 * time.Minute, // run every 5 minutes
	}
}

// Start begins the background cleanup loop. Call cancel() to stop.
func (w *BidCleanupWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	log.Printf("[BidCleanup] Starting bid cleanup worker (interval: %v)", w.interval)

	// Run once immediately on startup to clean any stale data
	w.runCleanup(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[BidCleanup] Stopping bid cleanup worker")
			return
		case <-ticker.C:
			w.runCleanup(ctx)
		}
	}
}

func (w *BidCleanupWorker) runCleanup(ctx context.Context) {
	n, err := w.bidTTL.CleanupExpiredBids(ctx)
	if err != nil {
		log.Printf("[BidCleanup] Error during cleanup: %v", err)
		return
	}
	if n > 0 {
		log.Printf("[BidCleanup] Cleaned up %d expired bid entries", n)
	}
}
