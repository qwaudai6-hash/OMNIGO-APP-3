package escrow

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const holdIndexKey = "escrow:hold-index"

// HoldIndex provides O(log N) lookup for expired escrow holds via Redis sorted set.
type HoldIndex struct {
	rdb redis.UniversalClient
}

// NewHoldIndex creates a Redis-backed hold index. Safe to use with nil Redis.
func NewHoldIndex(rdb redis.UniversalClient) *HoldIndex {
	return &HoldIndex{rdb: rdb}
}

// Add indexes a hold in the sorted set. Idempotent (ZADD with same score is a no-op).
func (h *HoldIndex) Add(ctx context.Context, holdID string, holdUntil time.Time) error {
	if h == nil || h.rdb == nil {
		return nil
	}
	score := float64(holdUntil.Unix())
	return h.rdb.ZAdd(ctx, holdIndexKey, redis.Z{
		Member: holdID,
		Score:  score,
	}).Err()
}

// Remove removes a hold from the sorted set (on release/cancel/dispute/refund).
func (h *HoldIndex) Remove(ctx context.Context, holdID string) error {
	if h == nil || h.rdb == nil {
		return nil
	}
	return h.rdb.ZRem(ctx, holdIndexKey, holdID).Err()
}

// ClaimExpired atomically finds and removes up to batchCount holds whose
// hold_until has passed. Uses a Lua script for atomicity.
func (h *HoldIndex) ClaimExpired(ctx context.Context, batchCount int) ([]string, error) {
	if h == nil || h.rdb == nil {
		return nil, nil
	}
	now := time.Now().Unix()
	script := redis.NewScript(`
		local members = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
		if #members == 0 then
			return {}
		end
		for i, member in ipairs(members) do
			redis.call('ZREM', KEYS[1], member)
		end
		return members
	`)
	result, err := script.Run(ctx, h.rdb, []string{holdIndexKey}, now, batchCount).StringSlice()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("claim expired script failed: %w", err)
	}
	return result, nil
}

// Count returns the number of holds in the sorted set.
func (h *HoldIndex) Count(ctx context.Context) (int64, error) {
	if h == nil || h.rdb == nil {
		return 0, nil
	}
	return h.rdb.ZCard(ctx, holdIndexKey).Result()
}
