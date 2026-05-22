package legacy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCircuitBreakerStateTransitions(t *testing.T) {
	tests := []struct {
		name             string
		failureThreshold int
		successThreshold int
		openTimeout      time.Duration
		failureCount     int
		expectStateAfter CircuitBreakerState
		description      string
	}{
		{
			name:             "Initial state is Closed",
			failureThreshold: 3,
			successThreshold: 1,
			openTimeout:      5 * time.Second,
			failureCount:     0,
			expectStateAfter: StateClosed,
			description:      "Circuit breaker should start in Closed state",
		},
		{
			name:             "Transitions to Open after reaching failure threshold",
			failureThreshold: 3,
			successThreshold: 1,
			openTimeout:      5 * time.Second,
			failureCount:     3,
			expectStateAfter: StateOpen,
			description:      "Circuit breaker should open after 3 consecutive failures",
		},
		{
			name:             "Does not transition to Open before threshold",
			failureThreshold: 3,
			successThreshold: 1,
			openTimeout:      5 * time.Second,
			failureCount:     2,
			expectStateAfter: StateClosed,
			description:      "Circuit breaker should remain closed with fewer failures than threshold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := CircuitBreakerConfig{
				FailureThreshold: tt.failureThreshold,
				SuccessThreshold: tt.successThreshold,
				OpenTimeout:      tt.openTimeout,
			}
			cb := NewCircuitBreaker(cfg)

			// Simulate failures
			for i := 0; i < tt.failureCount; i++ {
				cb.recordFailure()
			}

			if cb.state != tt.expectStateAfter {
				t.Errorf("%s: expected state %v, got %v", tt.description, tt.expectStateAfter, cb.state)
			}
		})
	}
}

func TestCircuitBreakerClosedToOpen(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 1,
		OpenTimeout:      100 * time.Millisecond,
	}
	cb := NewCircuitBreaker(cfg)

	// Verify initial state
	if cb.state != StateClosed {
		t.Fatalf("Expected initial state Closed, got %v", cb.state)
	}

	// Record failures up to threshold
	cb.recordFailure() // 1st failure
	if cb.state != StateClosed {
		t.Errorf("After 1 failure: expected Closed, got %v", cb.state)
	}

	cb.recordFailure() // 2nd failure
	if cb.state != StateClosed {
		t.Errorf("After 2 failures: expected Closed, got %v", cb.state)
	}

	cb.recordFailure() // 3rd failure - should transition to Open
	if cb.state != StateOpen {
		t.Errorf("After 3 failures (threshold): expected Open, got %v", cb.state)
	}

	// Verify that requests are blocked in Open state
	err := cb.allowRequest()
	if err != ErrCircuitOpen {
		t.Errorf("Expected ErrCircuitOpen in Open state, got %v", err)
	}
}

func TestCircuitBreakerOpenToHalfOpen(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		OpenTimeout:      50 * time.Millisecond,
	}
	cb := NewCircuitBreaker(cfg)

	// Transition to Open
	cb.recordFailure()
	if cb.state != StateOpen {
		t.Fatalf("Expected Open state, got %v", cb.state)
	}

	// Verify circuit is blocked
	err := cb.allowRequest()
	if err != ErrCircuitOpen {
		t.Errorf("Expected ErrCircuitOpen immediately, got %v", err)
	}

	// Wait for timeout
	time.Sleep(100 * time.Millisecond)

	// Should transition to HalfOpen
	err = cb.allowRequest()
	if err != nil {
		t.Errorf("Expected nil error after timeout, got %v", err)
	}
	if cb.state != StateHalfOpen {
		t.Errorf("After timeout: expected HalfOpen, got %v", cb.state)
	}
}

func TestCircuitBreakerHalfOpenToClosedOnSuccess(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 2,
		OpenTimeout:      50 * time.Millisecond,
	}
	cb := NewCircuitBreaker(cfg)

	// Transition to Open
	cb.recordFailure()

	// Wait for timeout and transition to HalfOpen
	time.Sleep(100 * time.Millisecond)
	cb.allowRequest()

	if cb.state != StateHalfOpen {
		t.Fatalf("Expected HalfOpen state, got %v", cb.state)
	}

	// First success in HalfOpen
	cb.recordSuccess()
	if cb.state != StateHalfOpen {
		t.Errorf("After 1 success: expected HalfOpen, got %v", cb.state)
	}

	// Second success should transition to Closed
	cb.recordSuccess()
	if cb.state != StateClosed {
		t.Errorf("After reaching success threshold: expected Closed, got %v", cb.state)
	}

	// Verify counters are reset
	if cb.failureCount != 0 {
		t.Errorf("Expected failureCount to be 0, got %d", cb.failureCount)
	}
	if cb.successCount != 0 {
		t.Errorf("Expected successCount to be 0, got %d", cb.successCount)
	}
}

func TestCircuitBreakerHalfOpenToOpenOnFailure(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 2,
		OpenTimeout:      50 * time.Millisecond,
	}
	cb := NewCircuitBreaker(cfg)

	// Transition to Open
	cb.recordFailure()

	// Wait for timeout and transition to HalfOpen
	time.Sleep(100 * time.Millisecond)
	cb.allowRequest()

	if cb.state != StateHalfOpen {
		t.Fatalf("Expected HalfOpen state, got %v", cb.state)
	}

	// Failure in HalfOpen should transition back to Open
	cb.recordFailure()
	if cb.state != StateOpen {
		t.Errorf("After failure in HalfOpen: expected Open, got %v", cb.state)
	}
}

func TestExecuteWithoutCircuitBreakerOpen(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 1,
		OpenTimeout:      5 * time.Second,
	}
	cb := NewCircuitBreaker(cfg)
	ctx := context.Background()

	callCount := 0
	operation := func(ctx context.Context) error {
		callCount++
		return nil
	}

	err := cb.Execute(ctx, operation)
	if err != nil {
		t.Errorf("Expected nil error, got %v", err)
	}
	if callCount != 1 {
		t.Errorf("Expected operation to be called once, got %d", callCount)
	}
}

func TestExecuteWhenCircuitBreakerOpen(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		OpenTimeout:      5 * time.Second,
	}
	cb := NewCircuitBreaker(cfg)
	ctx := context.Background()

	// Transition to Open
	cb.recordFailure()

	callCount := 0
	operation := func(ctx context.Context) error {
		callCount++
		return nil
	}

	err := cb.Execute(ctx, operation)
	if err != ErrCircuitOpen {
		t.Errorf("Expected ErrCircuitOpen, got %v", err)
	}
	if callCount != 0 {
		t.Errorf("Expected operation not to be called, got %d calls", callCount)
	}
}

func TestExecuteRecordsFailure(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 1,
		OpenTimeout:      5 * time.Second,
	}
	cb := NewCircuitBreaker(cfg)
	ctx := context.Background()

	testErr := errors.New("test error")
	operation := func(ctx context.Context) error {
		return testErr
	}

	// First call should fail but circuit remains closed
	err := cb.Execute(ctx, operation)
	if err != testErr {
		t.Errorf("Expected test error, got %v", err)
	}
	if cb.state != StateClosed {
		t.Errorf("After 1 failure: expected Closed, got %v", cb.state)
	}

	// Second call should fail and circuit should open
	err = cb.Execute(ctx, operation)
	if err != testErr {
		t.Errorf("Expected test error, got %v", err)
	}
	if cb.state != StateOpen {
		t.Errorf("After reaching failure threshold: expected Open, got %v", cb.state)
	}
}

func TestExecuteWithValue(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 1,
		OpenTimeout:      5 * time.Second,
	}
	cb := NewCircuitBreaker(cfg)
	ctx := context.Background()

	operation := func(ctx context.Context) (string, error) {
		return "success", nil
	}

	result, err := ExecuteWithValue(cb, ctx, operation)
	if err != nil {
		t.Errorf("Expected nil error, got %v", err)
	}
	if result != "success" {
		t.Errorf("Expected 'success', got %q", result)
	}
}

func TestExecuteWithValueWhenCircuitBreakerOpen(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 1,
		SuccessThreshold: 1,
		OpenTimeout:      5 * time.Second,
	}
	cb := NewCircuitBreaker(cfg)
	ctx := context.Background()

	// Transition to Open
	cb.recordFailure()

	operation := func(ctx context.Context) (string, error) {
		return "success", nil
	}

	result, err := ExecuteWithValue(cb, ctx, operation)
	if err != ErrCircuitOpen {
		t.Errorf("Expected ErrCircuitOpen, got %v", err)
	}
	if result != "" {
		t.Errorf("Expected empty string zero value, got %q", result)
	}
}

func TestExecuteWithValueRecordsFailure(t *testing.T) {
	cfg := CircuitBreakerConfig{
		FailureThreshold: 2,
		SuccessThreshold: 1,
		OpenTimeout:      5 * time.Second,
	}
	cb := NewCircuitBreaker(cfg)
	ctx := context.Background()

	testErr := errors.New("test error")
	operation := func(ctx context.Context) (int, error) {
		return 0, testErr
	}

	// First call should fail but circuit remains closed
	result, err := ExecuteWithValue(cb, ctx, operation)
	if err != testErr {
		t.Errorf("Expected test error, got %v", err)
	}
	if result != 0 {
		t.Errorf("Expected zero value, got %d", result)
	}
	if cb.state != StateClosed {
		t.Errorf("After 1 failure: expected Closed, got %v", cb.state)
	}

	// Second call should fail and circuit should open
	result, err = ExecuteWithValue(cb, ctx, operation)
	if err != testErr {
		t.Errorf("Expected test error, got %v", err)
	}
	if cb.state != StateOpen {
		t.Errorf("After reaching failure threshold: expected Open, got %v", cb.state)
	}
}

func TestCircuitBreakerStateString(t *testing.T) {
	tests := []struct {
		state    CircuitBreakerState
		expected string
	}{
		{StateClosed, "CLOSED"},
		{StateOpen, "OPEN"},
		{StateHalfOpen, "HALF_OPEN"},
		{CircuitBreakerState(99), "UNKNOWN(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := tt.state.String()
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestNewCircuitBreakerDefaultValues(t *testing.T) {
	cfg := CircuitBreakerConfig{}
	cb := NewCircuitBreaker(cfg)

	if cb.cfg.FailureThreshold != 3 {
		t.Errorf("Expected FailureThreshold default to 3, got %d", cb.cfg.FailureThreshold)
	}
	if cb.cfg.SuccessThreshold != 1 {
		t.Errorf("Expected SuccessThreshold default to 1, got %d", cb.cfg.SuccessThreshold)
	}
	if cb.cfg.OpenTimeout != 5*time.Second {
		t.Errorf("Expected OpenTimeout default to 5s, got %v", cb.cfg.OpenTimeout)
	}
}
