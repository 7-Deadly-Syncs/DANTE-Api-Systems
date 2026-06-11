package queue

import (
	"context"
	"fmt"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/attribute"
)

// CheckRabbitMQ verifies that the configured RabbitMQ broker accepts AMQP connections.
func CheckRabbitMQ(ctx context.Context, cfg config.RabbitMQConfig) error {
	ctx, span := tracing.StartClientSpan(ctx, "rabbitmq", "rabbitmq.check",
		attribute.String("messaging.system", "rabbitmq"),
		attribute.String("messaging.operation", "connect"),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	if cfg.URL == "" {
		spanErr = fmt.Errorf("rabbitmq url is not configured")
		return fmt.Errorf("rabbitmq url is not configured")
	}

	dialer := amqp.Config{
		Dial: amqp.DefaultDial(cfg.DialTimeout),
	}

	conn, err := amqp.DialConfig(cfg.URL, dialer)
	if err != nil {
		spanErr = err
		return fmt.Errorf("dial rabbitmq: %w", err)
	}
	defer conn.Close()

	select {
	case <-ctx.Done():
		spanErr = ctx.Err()
		return ctx.Err()
	default:
		return nil
	}
}
