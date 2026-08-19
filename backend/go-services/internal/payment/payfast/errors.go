package payfast

import (
	"errors"
	"fmt"
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
)

// GatewayError wraps external API failures without exposing sensitive secrets in client logs.
type GatewayError struct {
	StatusCode int
	Message    string
	Internal   error
}

func (e *GatewayError) Error() string {
	if e.Internal != nil {
		return fmt.Sprintf("payfast gateway error (HTTP %d): %s [cause: %v]", e.StatusCode, e.Message, e.Internal)
	}
	return fmt.Sprintf("payfast gateway error (HTTP %d): %s", e.StatusCode, e.Message)
}

func (e *GatewayError) Unwrap() error {
	return e.Internal
}
