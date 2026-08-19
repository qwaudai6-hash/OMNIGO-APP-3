package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RoleRequired returns a Gin middleware that rejects any caller whose JWT
// role claim is not in the supplied allowlist. It MUST be chained AFTER
// JWTAuth() so the role claim is present in the Gin context.
//
// Example:
//
//	auth := router.Group("/api/v1", middleware.JWTAuth())
//	adminOnly := auth.Group("", middleware.RoleRequired("admin"))
//	adminOnly.GET("/users", listUsers)
//
// Multiple roles can be passed; any match is accepted.
func RoleRequired(allowed ...string) gin.HandlerFunc {
	if len(allowed) == 0 {
		// Programming error: misconfigured middleware chain. Fail loudly.
		panic("middleware.RoleRequired called with empty role allowlist")
	}

	allowedSet := make(map[string]struct{}, len(allowed))
	for _, r := range allowed {
		allowedSet[r] = struct{}{}
	}

	return func(c *gin.Context) {
		role := c.GetString(ContextKeyRole)
		if role == "" {
			// Defensive: JWTAuth was not run before RoleRequired. This is a
			// routing bug, not a client error.
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "MIDDLEWARE_CHAIN_ERROR",
				"message": "RoleRequired used without preceding JWTAuth()",
			})
			return
		}

		if _, ok := allowedSet[role]; !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":    "FORBIDDEN_ROLE",
				"message":  "caller's role is not allowed for this route",
				"required": allowed,
				"got":      role,
			})
			return
		}

		c.Next()
	}
}
