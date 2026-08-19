package middleware

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RateLimit returns a Gin middleware that enforces per-IP rate limiting
// using a Redis sliding window counter. If Redis is unavailable, the
// middleware degrades gracefully (allows all requests, logs nothing).
//
// Parameters:
//   - rdb: Redis cluster client (nil = graceful degradation)
//   - limit: max requests per window
//   - window: time window duration
func RateLimit(rdb redis.UniversalClient, limit int, window time.Duration) gin.HandlerFunc {
	if limit <= 0 {
		limit = 100
	}
	if window <= 0 {
		window = time.Minute
	}

	return func(c *gin.Context) {
		if rdb == nil {
			// Graceful degradation — no Redis, no rate limiting
			c.Next()
			return
		}

		// Use client IP as the rate-limit key. Behind a load balancer,
		// X-Forwarded-For should be trusted (configure per deployment).
		ip := c.ClientIP()
		key := fmt.Sprintf("ratelimit:%s", ip)

		ctx := c.Request.Context()

		// Increment the counter
		count, err := rdb.Incr(ctx, key).Result()
		if err != nil {
			// Redis error — fail open (allow request)
			c.Next()
			return
		}

		// Set TTL on first increment
		if count == 1 {
			rdb.Expire(ctx, key, window)
		}

		// Set rate limit headers for client visibility
		c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", limit))
		remaining := int64(limit) - count
		if remaining < 0 {
			remaining = 0
		}
		c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))

		if count > int64(limit) {
			c.Header("Retry-After", fmt.Sprintf("%d", int(window.Seconds())))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":               "rate limit exceeded",
				"retry_after_seconds": int(window.Seconds()),
			})
			return
		}

		c.Next()
	}
}
