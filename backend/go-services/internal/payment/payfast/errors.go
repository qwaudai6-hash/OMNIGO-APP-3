package payfast

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
)

var (
	ErrNotConfigured        = errors.New("payfast: gateway credentials missing or unconfigured")
	ErrInvalidCustomerEmail = errors.New("payfast: valid customer email is required")
	ErrInvalidCustomerPhone = errors.New("payfast: valid customer mobile number is required")
	ErrInvalidAmount        = errors.New("payfast: transaction amount must be greater than zero")
	ErrInvalidOrderID       = errors.New("payfast: basket/order ID is required")
	ErrSignatureMismatch    = errors.New("payfast: callback signature validation failed")
	ErrAmountMismatch       = errors.New("payfast: payment amount does not match local order amount")
	ErrCurrencyMismatch     = errors.New("payfast: payment currency does not match local order currency")
	ErrOrderAlreadyPaid     = errors.New("payfast: order is already marked as paid (idempotent rejection)")
	ErrAuthFailed           = errors.New("payfast: authentication token acquisition failed")
	ErrTransactionFailed    = errors.New("payfast: payment processing was rejected by gateway")
	ErrEscrowHoldFailed     = errors.New("payfast: vendor escrow hold creation failed")
)

// GatewayError wraps external API failures without exposing sensitive secrets in client logs.
type GatewayError struct {
	StatusCode int
	Message    string
	StatusMsg  string
	Internal   error
}

func (e *GatewayError) Error() string {
	msg := e.Message
	if e.StatusMsg != "" {
		msg = fmt.Sprintf("%s (gateway msg: %s)", msg, e.StatusMsg)
	}
	if e.Internal != nil {
		return fmt.Sprintf("payfast gateway error (HTTP %d): %s [cause: %v]", e.StatusCode, msg, e.Internal)
	}
	return fmt.Sprintf("payfast gateway error (HTTP %d): %s", e.StatusCode, msg)
}

func (e *GatewayError) Unwrap() error {
	return e.Internal
}

// IsTransient returns true if the error represents a temporary network, socket, or timeout failure
// where the transaction state at the gateway is unknown and should be reconciled via gateway_pending.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}

	// Context deadline exceeded or cancellation due to timeout
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Network / Socket errors
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) {
		if errors.Is(syscallErr.Err, syscall.ECONNREFUSED) ||
			errors.Is(syscallErr.Err, syscall.ECONNRESET) ||
			errors.Is(syscallErr.Err, syscall.ETIMEDOUT) {
			return true
		}
	}

	// Gateway HTTP status checks
	var gwErr *GatewayError
	if errors.As(err, &gwErr) {
		switch gwErr.StatusCode {
		case http.StatusRequestTimeout, // 408
			http.StatusBadGateway,          // 502
			http.StatusServiceUnavailable,  // 503
			http.StatusGatewayTimeout:      // 504
			return true
		}
	}

	return false
}

// IsDeterministicRejection returns true if the error represents an explicit, permanent refusal
// (e.g. HTTP 400 Bad Request, 401 Unauthorized, 422 Unprocessable, or invalid credentials/parameters).
func IsDeterministicRejection(err error) bool {
	if err == nil {
		return false
	}
	var gwErr *GatewayError
	if errors.As(err, &gwErr) {
		if gwErr.StatusCode >= 400 && gwErr.StatusCode < 500 && gwErr.StatusCode != http.StatusRequestTimeout {
			return true
		}
	}
	return false
}

