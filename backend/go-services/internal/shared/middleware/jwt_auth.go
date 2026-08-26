package middleware

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/shared/auth"
)

const (
	// ContextKeyTrackingID is the Gin context key under which the caller's
	// tracking_id (from JWT claims) is stored. Handlers MUST read from this
	// key instead of trusting request bodies.
	ContextKeyTrackingID = "tracking_id"

	// ContextKeyRole is the Gin context key under which the caller's role
	// claim is stored. Use it together with RoleRequired() to gate routes.
	ContextKeyRole = "role"
)

// JWTAuth returns a Gin middleware that validates a Bearer JWT and stores the
// caller's tracking_id and role in the Gin context. It does NOT enforce a
// specific role — use RoleRequired() after JWTAuth() for that.
//
// Failure modes:
//   - missing Authorization header → 401 AUTH_HEADER_MISSING
//   - non-Bearer scheme            → 401 AUTH_SCHEME_INVALID
//   - malformed/expired token      → 401 AUTH_TOKEN_INVALID
//   - empty tracking_id claim      → 401 AUTH_CLAIMS_INVALID
func JWTAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "AUTH_HEADER_MISSING",
				"message": "Authorization header is required",
			})
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenString == authHeader {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "AUTH_SCHEME_INVALID",
				"message": "Authorization header must use Bearer scheme",
			})
			return
		}

		trackingID, role, err := auth.ParseJWT(tokenString)
		if err != nil {
			log.Printf("[WARN] JWT parsing failed: %v", err)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "AUTH_TOKEN_INVALID",
				"message": "invalid or expired authentication token",
			})
			return
		}

		c.Set(ContextKeyTrackingID, trackingID)
		c.Set(ContextKeyRole, role)
		c.Next()
	}
}

// GetTrackingID reads the caller's tracking_id from the Gin context. It
// returns an empty string if the context has not been populated by JWTAuth().
func GetTrackingID(c *gin.Context) string {
	return c.GetString(ContextKeyTrackingID)
}

// GetRole reads the caller's role from the Gin context. It returns an empty
// string if the context has not been populated by JWTAuth().
func GetRole(c *gin.Context) string {
	return c.GetString(ContextKeyRole)
}
