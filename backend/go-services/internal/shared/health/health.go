package health

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// DBPool returns a gin readiness handler that pings the supplied Postgres pool.
// It responds 503 if the pool is nil or the ping fails, and 200 if the DB is
// reachable. Keep the ping timeout short so Kubernetes/Railway probes don't
// back up during transient load.
func DBPool(pool *pgxpool.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 1*time.Second)
		defer cancel()

		if pool == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "reason": "db not initialized"})
			return
		}

		if err := pool.Ping(ctx); err != nil {
			// GW-05: never echo driver errors (they embed DSN/host details).
			log.Printf("[health] db ping failed: %v", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "reason": "database unreachable"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}

// Redis returns a gin readiness handler that pings the supplied Redis client.
func Redis(client redisPinger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 1*time.Second)
		defer cancel()

		if client == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "reason": "redis not initialized"})
			return
		}

		if err := client.Ping(ctx).Err(); err != nil {
			log.Printf("[health] redis ping failed: %v", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready", "reason": "cache unreachable"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}

// redisPinger is the small subset of go-redis clients we need for a Ping probe.
type redisPinger interface {
	Ping(context.Context) *redis.StatusCmd
}

// Ready returns a generic readiness handler for services that do not have
// external dependencies (e.g., the stateless map tile proxy).
func Ready() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	}
}
