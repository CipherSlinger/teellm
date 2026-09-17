package teellm

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when requests are rejected because the circuit breaker is in OPEN state.
var ErrCircuitOpen = errors.New("circuit breaker is OPEN: inference service is unavailable")

// State represents the state of the circuit breaker.
type State int

const (
	// StateClosed allows requests through normally.
	StateClosed State = iota
	// StateOpen fails requests immediately.
	StateOpen
	// StateHalfOpen allows a single probe request to test backend health.
	StateHalfOpen
)

// String returns a human-readable representation of the circuit breaker state.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF-OPEN"
	default:
		return fmt.Sprintf("State(%d)", s)
	}
}

// CircuitBreaker guards against cascading failures using a tri-state machine.
type CircuitBreaker struct {
	mu           sync.Mutex
	state        State
	failCount    int
	threshold    int
	cooldown     time.Duration
	lastFailTime time.Time
	probeActive  bool
}

// NewCircuitBreaker constructs a CircuitBreaker instance.
// If failureThreshold <= 0, it defaults to 3.
// If cooldown <= 0, it defaults to 30 seconds.
func NewCircuitBreaker(failureThreshold int, cooldown time.Duration) *CircuitBreaker {
	if failureThreshold <= 0 {
		failureThreshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &CircuitBreaker{
		threshold: failureThreshold,
		cooldown:  cooldown,
		state:     StateClosed,
	}
}

// AllowRequest reports whether a new request is permitted to proceed.
// In CLOSED: allows request.
// In OPEN: checks if cooldown has elapsed. If so, transitions to HALF-OPEN, sets probeActive, and allows request. Otherwise rejects.
// In HALF-OPEN: allows exactly one active probe request under probeActive lock; subsequent calls return false.
func (cb *CircuitBreaker) AllowRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if now.Sub(cb.lastFailTime) > cb.cooldown {
			cb.state = StateHalfOpen
			cb.probeActive = true
			return true
		}
		return false
	case StateHalfOpen:
		if !cb.probeActive {
			cb.probeActive = true
			return true
		}
		return false
	default:
		return true
	}
}

// RecordSuccess resets the failure count and transitions the circuit breaker to StateClosed.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failCount = 0
	cb.state = StateClosed
	cb.probeActive = false
}

// RecordFailure records a failed request, transitions to OPEN if threshold is reached or if in HALF-OPEN.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.probeActive = false
	cb.failCount++
	cb.lastFailTime = time.Now()
	if cb.state == StateHalfOpen || cb.failCount >= cb.threshold {
		cb.state = StateOpen
	}
}

// ResetProbe clears the probeActive lock on non-service aborts (e.g. client cancellation before dial).
func (cb *CircuitBreaker) ResetProbe() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.probeActive = false
}

// State returns the current operational state in a thread-safe manner.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateOpen && time.Since(cb.lastFailTime) > cb.cooldown {
		return StateHalfOpen
	}
	return cb.state
}
