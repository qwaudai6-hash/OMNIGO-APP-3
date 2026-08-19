package cache

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisClient wraps the go-redis UniversalClient (standalone or cluster)
type RedisClient struct {
	Client redis.UniversalClient
}

// NewRedisClient initializes a connection to Redis safely from URL or host:port
func NewRedisClient(ctx context.Context, addrs []string) (*RedisClient, error) {
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no redis addresses provided")
	}

	firstAddr := strings.TrimSpace(addrs[0])
	var client redis.UniversalClient

	// Parse full redis:// or rediss:// URL (e.g. Railway or Upstash REDIS_URL)
	if strings.HasPrefix(firstAddr, "redis://") || strings.HasPrefix(firstAddr, "rediss://") {
		opts, err := redis.ParseURL(firstAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse redis url %s: %w", firstAddr, err)
		}
		opts.DialTimeout = 5 * time.Second
		opts.ReadTimeout = 3 * time.Second
		opts.WriteTimeout = 3 * time.Second
		client = redis.NewClient(opts)
	} else {
		// Clean up host:port strings
		cleanedAddrs := make([]string, 0, len(addrs))
		for _, a := range addrs {
			clean := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(a), "redis://"), "rediss://")
			cleanedAddrs = append(cleanedAddrs, clean)
		}
		client = redis.NewUniversalClient(&redis.UniversalOptions{
			Addrs:        cleanedAddrs,
			PoolSize:     100,
			MinIdleConns: 20,
			DialTimeout:  5 * time.Second,
			ReadTimeout:  3 * time.Second,
			WriteTimeout: 3 * time.Second,
		})
	}

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to ping redis: %w", err)
	}

	return &RedisClient{
		Client: client,
	}, nil
}

// Close releases the Redis client
func (r *RedisClient) Close() error {
	return r.Client.Close()
}
