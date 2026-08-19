package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/shared/auth"
)

// AdminRequired returns a Gin middleware that validates a Bearer JWT and
// rejects any caller whose role claim is not exactly "admin".
// On success it stores the caller's tracking_id in the Gin context under the
// key "admin_tracking_id" so handlers can audit who performed the action.
func AdminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header is required"})
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenString == authHeader {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header must use Bearer scheme"})
			return
		}

		trackingID, role, err := auth.ParseJWT(tokenString)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		if role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin access required"})
			return
		}

		c.Set("admin_tracking_id", trackingID)
		c.Next()
	}
}
