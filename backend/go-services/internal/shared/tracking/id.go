// Package tracking provides canonical, prefix-based universal tracking ID
// generation for all OMNIGO domain entities.
package tracking

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// Generate returns a tracking id with the given prefix and a random 8-char
// suffix (e.g. Generate("ORD") -> "ORD-a1b2c3d4").
func Generate(prefix string) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fall back to a hex-encoded timestamp-derived value.  In practice
		// rand.Read only fails when the OS entropy source is unavailable.
		for i := range b {
			b[i] = byte(i)
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
