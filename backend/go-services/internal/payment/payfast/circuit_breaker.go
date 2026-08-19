package payfast

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitBreakerOpen is returned when the gateway circuit breaker is tripped.
var ErrCircuitBreakerOpen = errors.New("payfast circuit breaker is open: gateway temporarily unreachable")

// CircuitState represents the current operating state of the circuit breaker.
type CircuitState int

const (
	StateClosed CircuitState = iota
	StateHalfOpen
	StateOpen
)

func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateHalfOpen:
		return "HALF_OPEN"
	case StateOpen:
		return "OPEN"
	default:
		return "UNKNOWN"
	}
}

// CircuitBreaker provides fast-failing resilience during PayFast gateway outages.
type CircuitBreaker struct {
	mu                  sync.Mutex
	state               CircuitState
	failureCount        int
	consecutiveFailures int
	failureThreshold    int
	cooldownDuration    time.Duration
	lastStateChange     time.Time
}

// NewCircuitBreaker initializes a circuit breaker with sensible defaults.
func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 10 * time.Second
	}
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: threshold,
		cooldownDuration: cooldown,
		lastStateChange:  time.Now(),
	}
}

// Execute wraps an external network call with circuit breaking protection.
func (cb *CircuitBreaker) Execute(fn func() error) error {
	cb.mu.Lock()
	now := time.Now()

	// Check if cooldown has elapsed in Open state -> transition to HalfOpen
	if cb.state == StateOpen {
		if now.Sub(cb.lastStateChange) >= cb.cooldownDuration {
			cb.state = StateHalfOpen
			cb.lastStateChange = now
		} else {
			cb.mu.Unlock()
			return ErrCircuitBreakerOpen
		}
	}
	cb.mu.Unlock()

	// Execute operation
	err := fn()

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		// Only count transient network/socket/gateway errors towards tripping
		if IsTransient(err) || errors.Is(err, ErrAuthFailed) {
			cb.failureCount++
			cb.consecutiveFailures++

			if cb.state == StateHalfOpen || cb.consecutiveFailures >= cb.failureThreshold {
				cb.state = StateOpen
				cb.lastStateChange = time.Now()
			}
		}
		return err
	}

	// Success -> Reset circuit to Closed
	cb.consecutiveFailures = 0
	if cb.state == StateHalfOpen {
		cb.state = StateClosed
		cb.lastStateChange = time.Now()
	}
	return nil
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// Reset forcefully resets the circuit breaker to Closed.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = StateClosed
	cb.failureCount = 0
	cb.consecutiveFailures = 0
	cb.lastStateChange = time.Now()
}
