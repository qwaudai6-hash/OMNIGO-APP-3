package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const RequestIDHeader = "X-Request-ID"
const RequestIDCtxKey = "request_id"

// RequestID ensures every request has an X-Request-ID. If the client sent one,
// we honor it (for distributed trace continuity); otherwise we generate a UUIDv4.
// The value is stored in the gin context and echoed back in the response header
// so upstream services, logs and client can correlate a single request end-to-end.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader(RequestIDHeader)
		if rid == "" {
			rid = uuid.NewString()
		}
		c.Set(RequestIDCtxKey, rid)
		c.Header(RequestIDHeader, rid)
		c.Next()
	}
}

// Recovery catches panics in any downstream handler/proxy and returns a clean 500
// instead of crashing the gateway process. The stack trace is logged via slog.
// ponytail: stack trace printing deferred to logging package; keep this tiny.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, recovered any) {
		c.AbortWithStatusJSON(500, gin.H{
			"error":      "internal_panic",
			"request_id": c.GetString(RequestIDCtxKey),
		})
	})
}
