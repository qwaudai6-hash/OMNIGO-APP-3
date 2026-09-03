package workers

import (
	"context"
	"fmt"
	"time"

	"github.com/omnigo/backend/internal/escrow"
	"github.com/redis/go-redis/v9"
)

// EscrowReleaserWorker runs periodically to release expired escrow holds.
type EscrowReleaserWorker struct {
	escrow *escrow.Service
	redis  redis.UniversalClient
}

func NewEscrowReleaserWorker(escrowSvc *escrow.Service, rdb redis.UniversalClient) *EscrowReleaserWorker {
	return &EscrowReleaserWorker{escrow: escrowSvc, redis: rdb}
}

// Start begins the hourly escrow release loop.
// It runs until the context is cancelled.
func (w *EscrowReleaserWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	fmt.Println("[EscrowReleaser] Started — checking every hour for expired holds")

	// Rebuild Redis index on startup (handles Redis restart or first boot)
	if err := w.escrow.RebuildIndex(ctx); err != nil {
		fmt.Printf("[EscrowReleaser] Warning: failed to rebuild Redis index: %v\n", err)
	}

	// Run immediately on start
	w.releaseExpired(ctx)

	for {
		select {
		case <-ctx.Done():
			fmt.Println("[EscrowReleaser] Shutting down")
			return
		case <-ticker.C:
			w.releaseExpired(ctx)
		}
	}
}

func (w *EscrowReleaserWorker) releaseExpired(ctx context.Context) {
	if w.redis != nil {
		lockKey := "lock:workers:escrow-releaser"
		success, err := w.redis.SetNX(ctx, lockKey, "1", 30*time.Minute).Result()
		if err != nil {
			fmt.Printf("[EscrowReleaser] Redis lock error: %v\n", err)
			return
		}
		if !success {
			return
		}
		defer w.redis.Del(ctx, lockKey)
	}

	released, err := w.escrow.ReleaseExpiredHolds(ctx)
	if err != nil {
		fmt.Printf("[EscrowReleaser] Error releasing holds: %v\n", err)
		return
	}
	if released > 0 {
		fmt.Printf("[EscrowReleaser] Released %d escrow holds\n", released)
	}
}
