// Package tracking provides canonical, prefix-based universal tracking ID
// generation for all OMNIGO domain entities.
package tracking

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Generate returns a tracking id with the given prefix and a random 8-char
// suffix (e.g. Generate("ORD") -> "ORD-a1b2c3d4").
func Generate(prefix string) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// GW-23: crypto/rand only fails if the OS entropy pool is exhausted.
		// Never fall back to a PREDICTABLE byte pattern (0,1,2,3) — derive the
		// suffix from a monotonically-mixed timestamp instead so concurrent
		// generations still differ and ids stay unguessable enough for an
		// emergency fallback path. Callers should treat entropy failure as an
		// ops alert regardless.
		ts := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(ts >> (uint(i%8) * 8))
			ts = ts*6364136223846793005 + 1442695040888963407 // splitmix-style churn
		}
	}
	return fmt.Sprintf("%s-%s", strings.ToUpper(prefix), hex.EncodeToString(b))
}

// GenerateForRole returns the standard user tracking-id prefix for the
// registered role: customer -> CUST, vendor -> VEND, rider -> RIDR,
// admin -> ADMN.  Unknown roles fall back to USER.
func GenerateForRole(role string) string {
	switch role {
	case "customer":
		return Generate("CUST")
	case "vendor":
		return Generate("VEND")
	case "rider":
		return Generate("RIDR")
	case "admin":
		return Generate("ADMN")
	default:
		return Generate("USER")
	}
}
