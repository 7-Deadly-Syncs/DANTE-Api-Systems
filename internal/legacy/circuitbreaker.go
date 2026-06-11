package legacy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

// CircuitBreakerState represents the current state of the circuit breaker.
type CircuitBreakerState int

const (
	StateClosed CircuitBreakerState = iota
	StateOpen
	StateHalfOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF_OPEN"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", int(s))
	}
}

// CircuitBreakerConfig defines the thresholds and timeout used by the circuit breaker.
type CircuitBreakerConfig struct {
	FailureThreshold int
	SuccessThreshold int
	OpenTimeout      time.Duration
}

// ErrCircuitOpen is returned when the circuit breaker is open and requests are blocked.
var ErrCircuitOpen = errors.New("legacy circuit breaker is open")

// CircuitBreaker is a lightweight stateful circuit breaker for legacy calls.
type CircuitBreaker struct {
	mu           sync.Mutex
	state        CircuitBreakerState
	failureCount int
	successCount int
	openedAt     time.Time
	cfg          CircuitBreakerConfig
}

// NewCircuitBreaker constructs a circuit breaker with the provided configuration.
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 3
	}
	if cfg.SuccessThreshold <= 0 {
		cfg.SuccessThreshold = 1
	}
	if cfg.OpenTimeout <= 0 {
		cfg.OpenTimeout = 5 * time.Second
	}

	return &CircuitBreaker{
		state: StateClosed,
		cfg:   cfg,
	}
}

func (cb *CircuitBreaker) allowRequest() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state != StateOpen {
		return nil
	}

	if time.Since(cb.openedAt) >= cb.cfg.OpenTimeout {
		cb.state = StateHalfOpen
		cb.successCount = 0
		return nil
	}

	return ErrCircuitOpen
}

func (cb *CircuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateHalfOpen {
		cb.successCount++
		if cb.successCount >= cb.cfg.SuccessThreshold {
			cb.state = StateClosed
			cb.failureCount = 0
			cb.successCount = 0
		}
		return
	}

	cb.failureCount = 0
}

func (cb *CircuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	if cb.failureCount >= cb.cfg.FailureThreshold {
		cb.state = StateOpen
		cb.openedAt = time.Now()
		cb.successCount = 0
	}
}

// Execute runs an operation under checkbox state management.
func (cb *CircuitBreaker) Execute(ctx context.Context, operation func(context.Context) error) error {
	if err := cb.allowRequest(); err != nil {
		return err
	}

	if err := operation(ctx); err != nil {
		cb.recordFailure()
		return err
	}

	cb.recordSuccess()
	return nil
}

// ExecuteWithValue runs a typed operation under circuit breaker management.
func ExecuteWithValue[T any](cb *CircuitBreaker, ctx context.Context, operation func(context.Context) (T, error)) (T, error) {
	var zero T

	if err := cb.allowRequest(); err != nil {
		return zero, err
	}

	result, err := operation(ctx)
	if err != nil {
		cb.recordFailure()
		return zero, err
	}

	cb.recordSuccess()
	return result, nil
}

// CircuitBreakingMerchantClient wraps a MerchantReader and applies circuit breaker logic.
type CircuitBreakingMerchantClient struct {
	inner   MerchantReader
	breaker *CircuitBreaker
}

// NewCircuitBreakingMerchantClient wraps the given legacy merchant client with a circuit breaker.
func NewCircuitBreakingMerchantClient(inner MerchantReader, cfg CircuitBreakerConfig) *CircuitBreakingMerchantClient {
	return &CircuitBreakingMerchantClient{
		inner:   inner,
		breaker: NewCircuitBreaker(cfg),
	}
}

// GetMerchant calls the wrapped legacy reader through the circuit breaker.
func (c *CircuitBreakingMerchantClient) GetMerchant(ctx context.Context, merchantID uuid.UUID) (*dbsqlc.Merchant, error) {
	ctx, span := tracing.StartInternalSpan(ctx, "legacy.circuitbreaker", "legacy.circuit_breaker merchant",
		attribute.String("merchant.id", merchantID.String()),
		attribute.String("legacy.circuit_state", c.breaker.State().String()),
	)
	var spanErr error
	defer func() {
		span.SetAttributes(attribute.String("legacy.circuit_state_after", c.breaker.State().String()))
		tracing.EndSpan(span, spanErr, ErrNotFound)
	}()

	merchant, err := ExecuteWithValue(c.breaker, ctx, func(ctx context.Context) (*dbsqlc.Merchant, error) {
		return c.inner.GetMerchant(ctx, merchantID)
	})
	spanErr = err
	return merchant, err
}

// State reports the current circuit breaker state.
func (cb *CircuitBreaker) State() CircuitBreakerState {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	return cb.state
}
