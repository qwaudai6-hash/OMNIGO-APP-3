package middleware

import (
	"bytes"
	"io"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/omnigo/backend/internal/shared/security"
)

// InternalOnly returns a Gin middleware that requires every request to carry
// a valid HMAC-SHA256 internal signature (see security.InternalSigner).
//
// Usage:
//
//	signer := security.NewInternalSigner(os.Getenv("INTERNAL_API_SECRET"), "order-service")
//	internal := router.Group("/api/v1/internal/products", middleware.InternalOnly(signer))
//
// This blocks any caller reachable on the cluster network that does not
// possess the shared INTERNAL_API_SECRET. It is the primary defence for
// endpoints that are called only by sibling microservices (e.g.
// order-service calling product-service to reserve stock).
//
// The /health endpoint is always allowed through for liveness probes.
func InternalOnly(signer *security.InternalSigner) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.URL.Path == "/health" {
			c.Next()
			return
		}

		// Read and buffer the body so the handler can re-read it.
		var body []byte
		if c.Request.Body != nil {
			buf, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
					"error":   "INTERNAL_BODY_READ_FAILED",
					"message": err.Error(),
				})
				return
			}
			body = buf
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}

		if err := signer.VerifyRequest(c.Request, body); err != nil {
			// GW-18: keep verification detail server-side only.
			log.Printf("[InternalOnly] signature verify failed: %v", err)
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "INTERNAL_SIGNATURE_INVALID",
			})
			return
		}

		c.Next()
	}
}
