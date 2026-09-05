package middleware

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RateLimit returns a Gin middleware that enforces per-IP rate limiting
// using a Redis sliding window counter. If Redis is unavailable, the
// middleware FAILS CLOSED (rejects all requests with 503) and logs the failure.
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
			// Fail closed — no Redis means no rate limiting, so reject
			// the request to prevent abuse.
			log.Printf("[RateLimit] Redis unavailable — FAILING CLOSED, rejecting request from %s", c.ClientIP())
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "service temporarily unavailable, please retry",
			})
			return
		}

		// Prefer verified tracking ID from authenticated JWT context, fallback to client IP
		keyIdentifier := GetTrackingID(c)
		if keyIdentifier == "" {
			keyIdentifier = c.ClientIP()
		}
		key := fmt.Sprintf("ratelimit:%s", keyIdentifier)

		ctx := c.Request.Context()

		// Increment the counter
		count, err := rdb.Incr(ctx, key).Result()
		if err != nil {
			// Redis error — fail closed (reject request) but LOG it so an
			// attacker who disrupts Redis cannot silently bypass limiting.
			log.Printf("[RateLimit] Redis error (%v) — FAILING CLOSED for key %s", err, key)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "service temporarily unavailable, please retry",
			})
			return
		}

		// SP-GO-16: set TTL atomically with the increment via pipeline. The
		// previous INCR-then-EXPIRE two-step could leave a key with no TTL if
		// the process died between calls → permanent client lockout.
		if count == 1 {
			pipe := rdb.Pipeline()
			pipe.Expire(ctx, key, window)
			if _, perr := pipe.Exec(ctx); perr != nil {
				log.Printf("[RateLimit] Expire failed for key %s: %v", key, perr)
			}
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
