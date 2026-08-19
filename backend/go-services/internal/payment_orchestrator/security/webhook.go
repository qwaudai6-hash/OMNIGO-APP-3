package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// MaxWebhookAge is the maximum age of a webhook to prevent replay attacks.
	MaxWebhookAge = 5 * time.Minute

	// MaxBodySize is the maximum webhook body size (64KB).
	MaxBodySize = 65536
)

// WebhookVerifier verifies webhook signatures and timestamps.
type WebhookVerifier struct {
	secrets map[string]string // gateway -> secret
}

func NewWebhookVerifier() *WebhookVerifier {
	return &WebhookVerifier{
		secrets: make(map[string]string),
	}
}

// RegisterSecret registers a signing secret for a gateway.
func (v *WebhookVerifier) RegisterSecret(gateway, secret string) {
	v.secrets[gateway] = secret
}

// VerifyStripeSignature verifies a Stripe webhook signature.
// Returns true if the signature is valid and the timestamp is recent.
func (v *WebhookVerifier) VerifyStripeSignature(payload []byte, sigHeader, timestampHeader string) error {
	// 1. Check timestamp to prevent replay attacks
	if timestampHeader != "" {
		ts, err := parseTimestamp(timestampHeader)
		if err != nil {
			return fmt.Errorf("invalid timestamp: %w", err)
		}
		if time.Since(ts) > MaxWebhookAge {
			return fmt.Errorf("webhook too old: %v (max %v)", time.Since(ts), MaxWebhookAge)
		}
	}

	// 2. Verify HMAC signature
	secret := v.secrets["stripe"]
	if secret == "" {
		return fmt.Errorf("stripe secret not configured")
	}

	parts := strings.Split(sigHeader, ",")
	sigMap := make(map[string]string)
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			sigMap[kv[0]] = kv[1]
		}
	}

	sig := sigMap["v1"]
	if sig == "" {
		return fmt.Errorf("missing v1 signature")
	}

	// Build the signed payload: timestamp.body
	signedPayload := fmt.Sprintf("%s.%s", timestampHeader, string(payload))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}

// VerifyGenericHMAC verifies a generic HMAC-SHA256 signature.
// Used for JazzCash/EasyPaisa callback verification.
func (v *WebhookVerifier) VerifyGenericHMAC(gateway, payload, providedHash string) error {
	secret := v.secrets[gateway]
	if secret == "" {
		return fmt.Errorf("secret not configured for %s", gateway)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expectedHash := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(strings.ToLower(providedHash)), []byte(expectedHash)) {
		return fmt.Errorf("HMAC mismatch for %s", gateway)
	}

	return nil
}

// VerifyTimestamp checks that a timestamp is within the allowed window.
func VerifyTimestamp(timestampHeader string, maxAge time.Duration) error {
	ts, err := parseTimestamp(timestampHeader)
	if err != nil {
		return err
	}
	if time.Since(ts) > maxAge {
		return fmt.Errorf("timestamp too old: %v", time.Since(ts))
	}
	if ts.After(time.Now().Add(5 * time.Minute)) {
		return fmt.Errorf("timestamp in the future: %v", ts.Sub(time.Now()))
	}
	return nil
}

func parseTimestamp(s string) (time.Time, error) {
	var ts int64
	_, err := fmt.Sscanf(s, "%d", &ts)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(ts, 0), nil
}

// SecurityHeaders adds security headers to webhook responses.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// IPAllowlist checks if the request IP is in the allowlist.
// Empty allowlist means all IPs are allowed (development mode).
type IPAllowlist struct {
	allowed map[string]bool
}

func NewIPAllowlist(ips []string) *IPAllowlist {
	allowed := make(map[string]bool)
	for _, ip := range ips {
		allowed[ip] = true
	}
	return &IPAllowlist{allowed: allowed}
}

// IsAllowed returns true if the IP is allowed or if no allowlist is configured.
func (a *IPAllowlist) IsAllowed(ip string) bool {
	if len(a.allowed) == 0 {
		return true // No allowlist = all allowed
	}
	return a.allowed[ip]
}
