package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MEDIUM-26: cap request body size at the edge. Without this, the gateway
// will happily buffer multi-GB bodies before an upstream rejects them — a
// trivial memory/CPU DoS. 15 MiB covers all multipart image uploads:
//   - Product images: 5 MB max
//   - Delivery proof photos: 5 MB max
//   - KYC documents: 10 MB max
//
// Individual handlers enforce their own stricter limits via MaxBytesReader.
const MaxBodyBytes = 15 << 20 // 15 MiB (multipart overhead included)

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
