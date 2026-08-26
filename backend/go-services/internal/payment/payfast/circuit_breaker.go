package payfast

import (
	"errors"
	"sync"
	"time"

	"github.com/omnigo/backend/internal/shared/telemetry"
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
	consecutiveFailures int
	failureThreshold    int
	cooldownDuration    time.Duration
	lastStateChange     time.Time
	halfOpenProbing     bool
}

// NewCircuitBreaker initializes a circuit breaker with sensible defaults.
func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 10 * time.Second
	}
	cb := &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: threshold,
		cooldownDuration: cooldown,
		lastStateChange:  time.Now(),
	}
	telemetry.SetCircuitBreakerState("payfast", float64(StateClosed))
	return cb
}

// setState is the SINGLE place circuit state is ever mutated, so that every transition is
// consistently reflected in both the gauge (current state) and the transition counter (audit
// trail of how often/which transitions happen) — call sites never assign cb.state directly.
// Caller must hold cb.mu.
func (cb *CircuitBreaker) setState(newState CircuitState) {
	if cb.state == newState {
		return
	}
	oldState := cb.state
	cb.state = newState
	cb.lastStateChange = time.Now()
	telemetry.SetCircuitBreakerState("payfast", float64(newState))
	telemetry.RecordCircuitBreakerTransition(oldState.String(), newState.String())
}

// Execute wraps an external network call with circuit breaking protection.
func (cb *CircuitBreaker) Execute(fn func() error) error {
	cb.mu.Lock()
	now := time.Now()

	// Check if cooldown has elapsed in Open state -> transition to HalfOpen
	if cb.state == StateOpen {
		if now.Sub(cb.lastStateChange) >= cb.cooldownDuration {
			cb.setState(StateHalfOpen)
			cb.halfOpenProbing = true
		} else {
			cb.mu.Unlock()
			return ErrCircuitBreakerOpen
		}
	} else if cb.state == StateHalfOpen {
		if cb.halfOpenProbing {
			// A trial probe is already in flight, fail fast other concurrent callers
			cb.mu.Unlock()
			return ErrCircuitBreakerOpen
		}
		cb.halfOpenProbing = true
	}
	cb.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			cb.mu.Lock()
			cb.halfOpenProbing = false
			if cb.state == StateHalfOpen {
				cb.consecutiveFailures++
				cb.setState(StateOpen)
			}
			cb.mu.Unlock()
			panic(r)
		}
	}()

	// Execute operation
	err := fn()

	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.halfOpenProbing = false

	if err != nil {
		// Only count transient network/socket/gateway errors towards tripping
		if IsTransient(err) || errors.Is(err, ErrAuthFailed) {
			cb.consecutiveFailures++

			if cb.state == StateHalfOpen || cb.consecutiveFailures >= cb.failureThreshold {
				cb.setState(StateOpen)
			}
		} else {
			// Deterministic non-transient error (e.g. 400 Bad Request, card declined)
			// Proves upstream gateway is reachable and responding!
			cb.consecutiveFailures = 0
			if cb.state == StateHalfOpen {
				cb.setState(StateClosed)
			}
		}
		return err
	}

	// Success -> Reset circuit to Closed
	cb.consecutiveFailures = 0
	if cb.state == StateHalfOpen {
		cb.setState(StateClosed)
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
	cb.setState(StateClosed)
	cb.consecutiveFailures = 0
}
