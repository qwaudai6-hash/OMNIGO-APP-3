package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MEDIUM-26: cap request body size at the edge. Without this, the gateway
// will happily buffer multi-GB bodies before an upstream rejects them — a
// trivial memory/CPU DoS. 2 MiB covers every legit JSON/multipart payload in
// OMNIGO (largest: KYC images, enforced separately at 5-10 MB by handlers
// that need more via their own MaxBytesReader).
const MaxBodyBytes = 2 << 20 // 2 MiB

func BodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil {
			c.Next()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxBodyBytes)
		c.Next()
	}
}
