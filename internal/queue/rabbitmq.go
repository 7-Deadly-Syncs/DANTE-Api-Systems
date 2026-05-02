package queue

import (
	"context"
	"fmt"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	amqp "github.com/rabbitmq/amqp091-go"
)

// CheckRabbitMQ verifies that the configured RabbitMQ broker accepts AMQP connections.
func CheckRabbitMQ(ctx context.Context, cfg config.RabbitMQConfig) error {
	if cfg.URL == "" {
		return fmt.Errorf("rabbitmq url is not configured")
	}

	dialer := amqp.Config{
		Dial: amqp.DefaultDial(cfg.DialTimeout),
	}

	conn, err := amqp.DialConfig(cfg.URL, dialer)
	if err != nil {
		return fmt.Errorf("dial rabbitmq: %w", err)
	}
	defer conn.Close()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
