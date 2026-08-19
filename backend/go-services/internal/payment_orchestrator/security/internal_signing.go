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

// InternalSigner signs and verifies internal API requests between microservices.
// This prevents external actors from calling internal endpoints directly.
type InternalSigner struct {
	sharedSecret string
	serviceName  string
}

func NewInternalSigner(secret, serviceName string) *InternalSigner {
	return &InternalSigner{
		sharedSecret: secret,
		serviceName:  serviceName,
	}
}

// SignRequest adds an internal signature header to an outgoing request.
func (s *InternalSigner) SignRequest(req *http.Request, body []byte) {
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	payload := fmt.Sprintf("%s:%s:%s:%s", s.serviceName, timestamp, req.Method, string(body))

	mac := hmac.New(sha256.New, []byte(s.sharedSecret))
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("X-Internal-Signature", signature)
	req.Header.Set("X-Internal-Timestamp", timestamp)
	req.Header.Set("X-Internal-Service", s.serviceName)
}

// VerifyRequest verifies an incoming internal API request.
func (s *InternalSigner) VerifyRequest(r *http.Request, body []byte) error {
	signature := r.Header.Get("X-Internal-Signature")
	timestamp := r.Header.Get("X-Internal-Timestamp")
	service := r.Header.Get("X-Internal-Service")

	if signature == "" || timestamp == "" || service == "" {
		return fmt.Errorf("missing internal signature headers")
	}

	// Verify timestamp (max 30 seconds old)
	ts, err := parseTimestamp(timestamp)
	if err != nil {
		return fmt.Errorf("invalid timestamp: %w", err)
	}
	if time.Since(ts) > 30*time.Second {
		return fmt.Errorf("internal request too old: %v", time.Since(ts))
	}

	// Verify HMAC
	payload := fmt.Sprintf("%s:%s:%s:%s", service, timestamp, r.Method, string(body))
	mac := hmac.New(sha256.New, []byte(s.sharedSecret))
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expectedSig)) {
		return fmt.Errorf("internal signature mismatch")
	}

	return nil
}

// InternalOnlyMiddleware blocks requests that don't have valid internal signatures.
// Use this to protect internal-only endpoints from external access.
func InternalOnlyMiddleware(signer *InternalSigner) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Allow health checks without signature
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}

			// Check internal signature
			signature := r.Header.Get("X-Internal-Signature")
			timestamp := r.Header.Get("X-Internal-Timestamp")
			service := r.Header.Get("X-Internal-Service")

			if signature == "" || timestamp == "" || service == "" {
				http.Error(w, `{"error":"forbidden: internal endpoint"}`, http.StatusForbidden)
				return
			}

			// Verify the signature
			if err := signer.VerifyRequest(r, nil); err != nil {
				http.Error(w, `{"error":"invalid internal signature"}`, http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// sanitizeInput removes potentially dangerous characters from user input.
func SanitizeInput(input string) string {
	// Remove null bytes
	input = strings.ReplaceAll(input, "\x00", "")
	// Trim whitespace
	input = strings.TrimSpace(input)
	return input
}
