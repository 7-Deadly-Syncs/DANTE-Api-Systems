package queue

import (
	"context"
	"fmt"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/attribute"
)

// QueueStats describes one queue's current depth and active consumer count.
type QueueStats struct {
	Name      string `json:"name"`
	Messages  int    `json:"messages"`
	Consumers int    `json:"consumers"`
}

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

// InspectConfiguredQueues ensures the queue topology exists and returns live stats for primary, retry, and dead-letter queues.
func InspectConfiguredQueues(ctx context.Context, cfg config.RabbitMQConfig) (map[string]QueueStats, error) {
	ctx, span := tracing.StartClientSpan(ctx, "rabbitmq", "rabbitmq.inspect_queues",
		attribute.String("messaging.system", "rabbitmq"),
		attribute.String("messaging.operation", "inspect"),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	if cfg.URL == "" {
		spanErr = fmt.Errorf("rabbitmq url is not configured")
		return nil, spanErr
	}

	dialer := amqp.Config{
		Dial: amqp.DefaultDial(cfg.DialTimeout),
	}

	conn, err := amqp.DialConfig(cfg.URL, dialer)
	if err != nil {
		spanErr = err
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		spanErr = err
		return nil, fmt.Errorf("open rabbitmq channel: %w", err)
	}
	defer ch.Close()

	stats := make(map[string]QueueStats, 6)
	for _, primary := range []string{qrisQueueName(cfg), transfersQueueName(cfg)} {
		names := namesForQueue(primary)
		if err := declareQueueTopology(ch, names); err != nil {
			spanErr = err
			return nil, err
		}

		for _, queueName := range []string{names.Primary, names.Retry, names.DeadLetter} {
			q, err := ch.QueueInspect(queueName)
			if err != nil {
				spanErr = err
				return nil, fmt.Errorf("inspect queue %s: %w", queueName, err)
			}

			stats[queueName] = QueueStats{
				Name:      q.Name,
				Messages:  q.Messages,
				Consumers: q.Consumers,
			}
		}
	}

	return stats, nil
}
