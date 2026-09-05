package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// BidTTLManager handles automatic cleanup of stale bid data in Redis.
// Bids that are never accepted/cancelled are cleaned up after their TTL expires.
type BidTTLManager struct {
	rdb redis.UniversalClient
}

func NewBidTTLManager(rdb redis.UniversalClient) *BidTTLManager {
	return &BidTTLManager{rdb: rdb}
}

const (
	// BidDataTTL is how long bid data lives in Redis before auto-cleanup.
	// Matches the typical bid lifecycle: customer creates → rider counters →
	// customer accepts/rejects. 30 minutes is generous for slow networks.
	BidDataTTL = 30 * time.Minute

	// BidSearchTTL is how long a "searching" bid stays in the search index.
	BidSearchTTL = 15 * time.Minute
)

// SetBidData stores bid data with automatic TTL expiration.
// Key: bid:{bid_id} → JSON payload
func (btm *BidTTLManager) SetBidData(ctx context.Context, bidID string, data interface{}) error {
	if btm.rdb == nil {
		return nil
	}
	key := fmt.Sprintf("bid:%s", bidID)
	return btm.rdb.Set(ctx, key, data, BidDataTTL).Err()
}

// GetBidData retrieves bid data from Redis.
func (btm *BidTTLManager) GetBidData(ctx context.Context, bidID string) (string, error) {
	if btm.rdb == nil {
		return "", fmt.Errorf("redis not available")
	}
	key := fmt.Sprintf("bid:%s", bidID)
	return btm.rdb.Get(ctx, key).Result()
}

// DeleteBidData removes bid data from Redis immediately.
func (btm *BidTTLManager) DeleteBidData(ctx context.Context, bidID string) error {
	if btm.rdb == nil {
		return nil
	}
	key := fmt.Sprintf("bid:%s", bidID)
	return btm.rdb.Del(ctx, key).Err()
}

// MarkBidSearching adds a bid to the search index with TTL.
// Riders poll this to find active bids nearby.
func (btm *BidTTLManager) MarkBidSearching(ctx context.Context, bidID string, pickupLng, pickupLat float64) error {
	if btm.rdb == nil {
		return nil
	}

	// Store in a sorted set keyed by geohash prefix for proximity search
	geohashKey := fmt.Sprintf("bids:searching:%s", geohashPrefix(pickupLng, pickupLat, 5))
	pipe := btm.rdb.Pipeline()
	pipe.ZAdd(ctx, geohashKey, redis.Z{
		Score:  float64(time.Now().UnixMilli()),
		Member: bidID,
	})
	pipe.Expire(ctx, geohashKey, BidSearchTTL)
	_, err := pipe.Exec(ctx)

	if err != nil {
		log.Printf("Warning: failed to mark bid %s as searching: %v", bidID, err)
		return err
	}

	// Also set individual bid key with TTL
	return btm.SetBidData(ctx, bidID, map[string]interface{}{
		"bid_id":      bidID,
		"status":      "searching",
		"pickup_lng":  pickupLng,
		"pickup_lat":  pickupLat,
		"created_at":  time.Now().UnixMilli(),
	})
}

// RemoveFromSearchIndex removes a bid from the searching index.
func (btm *BidTTLManager) RemoveFromSearchIndex(ctx context.Context, bidID string, pickupLng, pickupLat float64) error {
	if btm.rdb == nil {
		return nil
	}
	geohashKey := fmt.Sprintf("bids:searching:%s", geohashPrefix(pickupLng, pickupLat, 5))
	return btm.rdb.ZRem(ctx, geohashKey, bidID).Err()
}

// CleanupExpiredBids removes bids older than maxAge from the search index.
// Called periodically by a background worker.
func (btm *BidTTLManager) CleanupExpiredBids(ctx context.Context) (int64, error) {
	if btm.rdb == nil {
		return 0, nil
	}

	cutoff := float64(time.Now().Add(-BidSearchTTL).UnixMilli())
	var totalCleaned int64

	// Scan for all bid search index keys
	iter := btm.rdb.Scan(ctx, 0, "bids:searching:*", 100).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		// Remove all bids older than cutoff
		n, err := btm.rdb.ZRemRangeByScore(ctx, key, "0", fmt.Sprintf("%f", cutoff)).Result()
		if err != nil {
			log.Printf("Warning: cleanup failed for %s: %v", key, err)
			continue
		}
		totalCleaned += n

		// Remove the key itself if it's now empty
		card, _ := btm.rdb.ZCard(ctx, key).Result()
		if card == 0 {
			btm.rdb.Del(ctx, key)
		}
	}

	return totalCleaned, iter.Err()
}

// geohashPrefix computes a simple geohash-like prefix for geospatial bucketing.
// precision controls the number of characters (5 ≈ ~5km grid cells).
func geohashPrefix(lng, lat float64, precision int) string {
	const base32 = "0123456789bcdefghjkmnpqrstuvwxyz"
	latRange := [2]float64{-90, 90}
	lngRange := [2]float64{-180, 180}
	isLat := false
	bit := 0
	ch := 0
	geohash := ""

	for len(geohash) < precision {
		if isLat {
			mid := (latRange[0] + latRange[1]) / 2
			if lat >= mid {
				ch |= 1 << (4 - bit)
				latRange[0] = mid
			} else {
				latRange[1] = mid
			}
		} else {
			mid := (lngRange[0] + lngRange[1]) / 2
			if lng >= mid {
				ch |= 1 << (4 - bit)
				lngRange[0] = mid
			} else {
				lngRange[1] = mid
			}
		}
		isLat = !isLat
		bit++
		if bit == 5 {
			geohash += string(base32[ch])
			bit = 0
			ch = 0
		}
	}

	return geohash
}
