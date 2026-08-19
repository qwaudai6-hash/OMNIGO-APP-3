package middleware

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORS returns a Gin middleware that sets Cross-Origin Resource Sharing
// headers. The allowed origins are read from the CORS_ALLOWED_ORIGINS
// env var (comma-separated).
//
// Security: in production (APP_ENV != "development") an empty allow-list is a
// FATAL error — shipping wildcard + credentials is a credential-leak footgun.
// In development, an empty list falls back to "*" for local convenience.
func CORS() gin.HandlerFunc {
	allowedOrigins := os.Getenv("CORS_ALLOWED_ORIGINS")
	env := os.Getenv("APP_ENV")

	if allowedOrigins == "" {
		if env == "development" || env == "" {
			allowedOrigins = "*" // dev convenience only
		} else {
			// ponytail: fail-fast. A misconfigured prod CORS is worse than a down service —
			// it leaks credentials. Upgrade path: read from a managed secrets store.
			panic("FATAL: CORS_ALLOWED_ORIGINS must be set in production (APP_ENV=" + env + "). " +
				"Example: CORS_ALLOWED_ORIGINS=https://omnigo-app-production.up.railway.app," +
				"https://store.omnigo.pk")
		}
	}
	origins := strings.Split(allowedOrigins, ",")
	for i, o := range origins {
		origins[i] = strings.TrimSpace(o)
	}

	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		allowed := false

		if allowedOrigins == "*" {
			allowed = true
		} else {
			for _, o := range origins {
				if o == origin {
					allowed = true
					break
				}
			}
		}

		if allowed {
			if allowedOrigins == "*" {
				c.Header("Access-Control-Allow-Origin", "*")
			} else {
				c.Header("Access-Control-Allow-Origin", origin)
			}
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, X-Trace-Id, X-Customer-ID, X-Store-ID, X-Device-Session-Nonce")
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Max-Age", "86400")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
