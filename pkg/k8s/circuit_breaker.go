// Package k8s provides Kubernetes client helpers for node and pod operations.
package k8s

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when the circuit breaker is open.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitState represents the state of a circuit breaker.
type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig configures the circuit breaker behavior.
type CircuitBreakerConfig struct {
	// FailureThreshold is the number of failures before opening the circuit.
	FailureThreshold int
	// SuccessThreshold is the number of successes in half-open state to close the circuit.
	SuccessThreshold int
	// Timeout is how long to wait before transitioning from open to half-open.
	Timeout time.Duration
	// OnStateChange is called when the circuit state changes.
	OnStateChange func(from, to CircuitState)
}

// DefaultCircuitBreakerConfig returns sensible defaults for edge environments.
func DefaultCircuitBreakerConfig() *CircuitBreakerConfig {
	return &CircuitBreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Timeout:          30 * time.Second,
	}
}

// CircuitBreaker implements the circuit breaker pattern for API calls.
type CircuitBreaker struct {
	config *CircuitBreakerConfig

	mu                  sync.RWMutex
	state               CircuitState
	failures            int
	successes           int
	lastFailureTime     time.Time
	lastStateChangeTime time.Time
}

// NewCircuitBreaker creates a new circuit breaker.
func NewCircuitBreaker(config *CircuitBreakerConfig) *CircuitBreaker {
	if config == nil {
		config = DefaultCircuitBreakerConfig()
	}
	return &CircuitBreaker{
		config:              config,
		state:               CircuitClosed,
		lastStateChangeTime: time.Now(),
	}
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Allow checks if a request should be allowed through the circuit breaker.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true

	case CircuitOpen:
		// Check if timeout has elapsed
		if time.Since(cb.lastStateChangeTime) >= cb.config.Timeout {
			cb.transitionTo(CircuitHalfOpen)
			return true
		}
		return false

	case CircuitHalfOpen:
		// Allow limited requests in half-open state
		return true

	default:
		return false
	}
}

// RecordSuccess records a successful operation.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitHalfOpen:
		cb.successes++
		if cb.successes >= cb.config.SuccessThreshold {
			cb.transitionTo(CircuitClosed)
		}
	case CircuitClosed:
		// Reset failure count on success
		cb.failures = 0
	}
}

// RecordFailure records a failed operation.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.lastFailureTime = time.Now()

	switch cb.state {
	case CircuitClosed:
		cb.failures++
		if cb.failures >= cb.config.FailureThreshold {
			cb.transitionTo(CircuitOpen)
		}
	case CircuitHalfOpen:
		// Any failure in half-open state reopens the circuit
		cb.transitionTo(CircuitOpen)
	}
}

// transitionTo changes the circuit state (must be called with lock held).
func (cb *CircuitBreaker) transitionTo(newState CircuitState) {
	if cb.state == newState {
		return
	}

	oldState := cb.state
	cb.state = newState
	cb.lastStateChangeTime = time.Now()
	cb.failures = 0
	cb.successes = 0

	if cb.config.OnStateChange != nil {
		// Call callback without holding lock
		go cb.config.OnStateChange(oldState, newState)
	}
}

// Stats returns current circuit breaker statistics.
type CircuitBreakerStats struct {
	State               string    `json:"state"`
	Failures            int       `json:"failures"`
	Successes           int       `json:"successes"`
	LastFailureTime     time.Time `json:"last_failure_time,omitempty"`
	LastStateChangeTime time.Time `json:"last_state_change_time"`
}

// GetStats returns the current circuit breaker statistics.
func (cb *CircuitBreaker) GetStats() CircuitBreakerStats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return CircuitBreakerStats{
		State:               cb.state.String(),
		Failures:            cb.failures,
		Successes:           cb.successes,
		LastFailureTime:     cb.lastFailureTime,
		LastStateChangeTime: cb.lastStateChangeTime,
	}
}

// Reset resets the circuit breaker to closed state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.transitionTo(CircuitClosed)
}
