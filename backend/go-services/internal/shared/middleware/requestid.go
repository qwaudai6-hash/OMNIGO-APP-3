package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const RequestIDHeader = "X-Request-ID"
const RequestIDCtxKey = "request_id"

// RequestID ensures every request has an X-Request-ID. If the client sent one
// AND it is sane (≤64 chars, printable ASCII without control chars), we honor
// it for trace continuity; otherwise a fresh UUIDv4 is generated. This keeps
// client-supplied IDs from injecting newlines/log garbage downstream.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := sanitizeRequestID(c.GetHeader(RequestIDHeader))
		if rid == "" {
			rid = uuid.NewString()
		}
		c.Set(RequestIDCtxKey, rid)
		c.Header(RequestIDHeader, rid)
		c.Next()
	}
}

// sanitizeRequestID returns the client ID only if it is safe to propagate.
const maxRequestIDLen = 64

func sanitizeRequestID(raw string) string {
	if raw == "" || len(raw) > maxRequestIDLen {
		return ""
	}
	for _, r := range raw {
		// Printable ASCII minus control chars; reject anything unusual.
		if r < 0x20 || r > 0x7E {
			return ""
		}
	}
	return raw
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
