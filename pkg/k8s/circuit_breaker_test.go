package k8s

import (
	"testing"
	"time"
)

func TestCircuitBreakerInitialState(t *testing.T) {
	cb := NewCircuitBreaker(nil)
	if cb.State() != CircuitClosed {
		t.Errorf("expected initial state to be closed, got %v", cb.State())
	}
	if !cb.Allow() {
		t.Error("expected Allow() to return true in closed state")
	}
}

func TestCircuitBreakerOpensAfterFailures(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Timeout:          100 * time.Millisecond,
	}
	cb := NewCircuitBreaker(config)

	// Record failures up to threshold
	for i := 0; i < 3; i++ {
		if !cb.Allow() {
			t.Errorf("expected Allow() to return true before threshold, iteration %d", i)
		}
		cb.RecordFailure()
	}

	// Circuit should now be open
	if cb.State() != CircuitOpen {
		t.Errorf("expected state to be open after %d failures, got %v", 3, cb.State())
	}
	if cb.Allow() {
		t.Error("expected Allow() to return false in open state")
	}
}

func TestCircuitBreakerTransitionsToHalfOpen(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Timeout:          50 * time.Millisecond,
	}
	cb := NewCircuitBreaker(config)

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	if cb.State() != CircuitOpen {
		t.Fatalf("expected state to be open, got %v", cb.State())
	}

	// Wait for timeout
	time.Sleep(60 * time.Millisecond)

	// Should transition to half-open on Allow()
	if !cb.Allow() {
		t.Error("expected Allow() to return true after timeout")
	}
	if cb.State() != CircuitHalfOpen {
		t.Errorf("expected state to be half-open, got %v", cb.State())
	}
}

func TestCircuitBreakerClosesAfterSuccesses(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 2,
		Timeout:          50 * time.Millisecond,
	}
	cb := NewCircuitBreaker(config)

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	// Wait for timeout and transition to half-open
	time.Sleep(60 * time.Millisecond)
	cb.Allow()

	// Record successes
	cb.RecordSuccess()
	if cb.State() != CircuitHalfOpen {
		t.Errorf("expected state to still be half-open after 1 success, got %v", cb.State())
	}

	cb.RecordSuccess()
	if cb.State() != CircuitClosed {
		t.Errorf("expected state to be closed after 2 successes, got %v", cb.State())
	}
}

func TestCircuitBreakerReopensOnFailureInHalfOpen(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 2,
		Timeout:          50 * time.Millisecond,
	}
	cb := NewCircuitBreaker(config)

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	// Wait for timeout and transition to half-open
	time.Sleep(60 * time.Millisecond)
	cb.Allow()

	if cb.State() != CircuitHalfOpen {
		t.Fatalf("expected state to be half-open, got %v", cb.State())
	}

	// Any failure in half-open should reopen
	cb.RecordFailure()
	if cb.State() != CircuitOpen {
		t.Errorf("expected state to be open after failure in half-open, got %v", cb.State())
	}
}

func TestCircuitBreakerReset(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 2,
		Timeout:          time.Minute,
	}
	cb := NewCircuitBreaker(config)

	// Open the circuit
	cb.RecordFailure()
	cb.RecordFailure()

	if cb.State() != CircuitOpen {
		t.Fatalf("expected state to be open, got %v", cb.State())
	}

	// Reset should close it
	cb.Reset()
	if cb.State() != CircuitClosed {
		t.Errorf("expected state to be closed after reset, got %v", cb.State())
	}
	if !cb.Allow() {
		t.Error("expected Allow() to return true after reset")
	}
}

func TestCircuitBreakerStats(t *testing.T) {
	cb := NewCircuitBreaker(nil)

	cb.RecordFailure()
	cb.RecordFailure()

	stats := cb.GetStats()
	if stats.State != "closed" {
		t.Errorf("expected state 'closed', got %s", stats.State)
	}
	if stats.Failures != 2 {
		t.Errorf("expected 2 failures, got %d", stats.Failures)
	}
}

func TestCircuitBreakerSuccessResetsFailures(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 1,
		Timeout:          time.Minute,
	}
	cb := NewCircuitBreaker(config)

	// Record some failures (but not enough to open)
	cb.RecordFailure()
	cb.RecordFailure()

	// A success should reset the failure count
	cb.RecordSuccess()

	// Now we need 3 more failures to open
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != CircuitClosed {
		t.Errorf("expected state to still be closed, got %v", cb.State())
	}

	cb.RecordFailure()
	if cb.State() != CircuitOpen {
		t.Errorf("expected state to be open after 3 consecutive failures, got %v", cb.State())
	}
}

func TestCircuitStateString(t *testing.T) {
	tests := []struct {
		state    CircuitState
		expected string
	}{
		{CircuitClosed, "closed"},
		{CircuitOpen, "open"},
		{CircuitHalfOpen, "half-open"},
		{CircuitState(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.state.String(); got != tt.expected {
			t.Errorf("CircuitState(%d).String() = %s, want %s", tt.state, got, tt.expected)
		}
	}
}
