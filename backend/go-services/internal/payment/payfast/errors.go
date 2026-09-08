package payfast

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
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

	// Explicit client cancellation (user closed tab / connection aborted) is NOT a transient gateway timeout
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Context deadline exceeded due to timeout
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCircuitBreakerOpen) {
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
		// 408 is an explicit timeout; ANY 5xx means the gateway side failed while our
		// transaction state there is unknown (includes 500 Internal Server Error from the
		// gateway itself and "auth endpoint unreachable" wrappers). Treating 5xx as
		// transient lets the circuit breaker trip instead of hammering a dying upstream.
		return gwErr.StatusCode == http.StatusRequestTimeout || gwErr.StatusCode >= http.StatusInternalServerError
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

// MapIssuerResponseCode converts raw 1LINK/PayFast response codes into clear, actionable advice for customers.
func MapIssuerResponseCode(code string) string {
	switch strings.TrimSpace(code) {
	case "00", "000":
		return "Approved"
	case "002":
		return "Transaction timed out at bank gateway. Please retry."
	case "03":
		return "You have entered an inactive account. Please contact your bank."
	case "05", "51", "97", "881":
		return "Insufficient balance in your card/account. Please top up and retry."
	case "14", "54":
		return "Card expired or invalid expiry date entered."
	case "55":
		return "You have entered an invalid OTP or PIN. Please check and retry."
	case "57", "58", "880", "883":
		return "Online e-commerce transactions are not enabled on your card. Please enable online shopping in your bank app and retry."
	case "61", "106", "882":
		return "Daily transaction limit or count exceeded on your card. Please contact your bank."
	case "65":
		return "Exceeded transaction frequency limit on your card."
	case "75":
		return "Incorrect CVV / OTP entered too many times."
	case "104":
		return "Entered payment details are incorrect. Please verify card number, CVV, and expiry."
	case "803":
		return "OTP has been sent to your email address."
	case "804":
		return "OTP has been sent to your mobile number."
	case "805":
		return "OTP verified successfully."
	case "806":
		return "OTP could not be verified. Please request a new OTP."
	case "91", "96":
		return "Your issuing bank or 1LINK switch is temporarily unavailable. Please retry in a few moments."
	case "001":
		return "Invalid merchant configuration. Please contact support."
	case "013":
		return "Transaction amount exceeds maximum limit allowed."
	case "015":
		return "Invalid transaction currency or format."
	case "041":
		return "Card issuer is temporarily unavailable. Please retry."
	case "126":
		return "Your card has been blocked by the bank. Please contact your bank."
	case "423":
		return "Transaction could not be processed at this time. Please retry."
	case "801":
		return "OTP verification timeout. Please request a new OTP."
	case "802":
		return "OTP has expired. Please request a new OTP."
	case "807":
		return "Maximum OTP verification attempts exceeded. Please request a new OTP."
	case "808":
		return "OTP verification failed. Please check and retry."
	case "809":
		return "OTP has been resend to your registered mobile/email."
	case "810":
		return "OTP verified successfully."
	case "811":
		return "OTP could not be sent. Please try again."
	case "812":
		return "OTP delivery failed. Please try again or use another verification method."
	case "813":
		return "OTP verification is required to complete this transaction."
	case "850":
		return "Beneficiary bank is not available for this transfer. Please try another method."
	case "851":
		return "Transfer type not allowed for this account. Please contact your bank."
	case "9000":
		return "System error. Please contact support if problem persists."
	default:
		return "Payment was declined by issuing bank."
	}
}
