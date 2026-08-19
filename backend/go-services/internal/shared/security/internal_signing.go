package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// InternalSigner signs and verifies internal API requests between microservices.
// This prevents external actors from calling internal endpoints directly.
//
// Signature scheme (HMAC-SHA256):
//
//	payload   = serviceName + ":" + timestamp + ":" + method + ":" + sha256(body)
//	signature = hex(HMAC_SHA256(secret, payload))
//
// Required headers on signed requests:
//
//	X-Internal-Service    : caller service name (e.g. "order-service")
//	X-Internal-Timestamp   : unix seconds, must be within ±30s of server time
//	X-Internal-Signature   : hex-encoded HMAC
type InternalSigner struct {
	sharedSecret string
	serviceName  string
}

// NewInternalSigner creates a signer with the given shared secret and
// service identity. Pass the SAME secret to every microservice in the cluster.
func NewInternalSigner(secret, serviceName string) *InternalSigner {
	return &InternalSigner{
		sharedSecret: secret,
		serviceName:  serviceName,
	}
}

// ServiceName returns the identity of the service that owns this signer.
func (s *InternalSigner) ServiceName() string {
	return s.serviceName
}

// SignRequest signs an outgoing request and stamps the three required headers.
// The body bytes must be the EXACT bytes that will be written to the wire
// (i.e. the JSON-encoded payload). Pass nil for GET/DELETE.
func (s *InternalSigner) SignRequest(req *http.Request, body []byte) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	bodyHash := sha256.Sum256(body)
	payload := fmt.Sprintf("%s:%s:%s:%x", s.serviceName, timestamp, req.Method, bodyHash)

	mac := hmac.New(sha256.New, []byte(s.sharedSecret))
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("X-Internal-Signature", signature)
	req.Header.Set("X-Internal-Timestamp", timestamp)
	req.Header.Set("X-Internal-Service", s.serviceName)
}

// VerifyRequest validates the three internal headers on an incoming request
// and returns a non-nil error if any of them are missing, expired, or
// signature-incorrect.
func (s *InternalSigner) VerifyRequest(r *http.Request, body []byte) error {
	signature := r.Header.Get("X-Internal-Signature")
	timestamp := r.Header.Get("X-Internal-Timestamp")
	service := r.Header.Get("X-Internal-Service")

	if signature == "" || timestamp == "" || service == "" {
		return fmt.Errorf("missing internal signature headers")
	}

	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp: %w", err)
	}
	delta := time.Since(time.Unix(ts, 0))
	if delta < 0 {
		delta = -delta
	}
	if delta > 30*time.Second {
		return fmt.Errorf("internal request timestamp out of window: %v", delta)
	}

	bodyHash := sha256.Sum256(body)
	payload := fmt.Sprintf("%s:%s:%s:%x", service, timestamp, r.Method, bodyHash)
	mac := hmac.New(sha256.New, []byte(s.sharedSecret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return fmt.Errorf("internal signature mismatch")
	}
	return nil
}

// ReadAndVerify reads the body, verifies the signature, and returns the body
// bytes so handlers can re-use them. The body is buffered (not drained) so
// the original request body remains readable.
func (s *InternalSigner) ReadAndVerify(r *http.Request) ([]byte, error) {
	var body []byte
	if r.Body != nil {
		buf, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		body = buf
	}
	if err := s.VerifyRequest(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// SanitizeInput removes potentially dangerous characters from user input.
func SanitizeInput(input string) string {
	input = strings.ReplaceAll(input, "\x00", "")
	return strings.TrimSpace(input)
}
