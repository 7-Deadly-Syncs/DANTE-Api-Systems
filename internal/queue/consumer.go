package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	amqp "github.com/rabbitmq/amqp091-go"
)

const workerReconnectDelay = 3 * time.Second

// QRISPaymentHandler processes QRIS payment jobs consumed from RabbitMQ.
type QRISPaymentHandler interface {
	HandleQRISPayment(ctx context.Context, msg QRISPaymentMessage) error
}

// TransferHandler processes transfer jobs consumed from RabbitMQ.
type TransferHandler interface {
	HandleTransfer(ctx context.Context, msg TransferMessage) error
}

// Consumer consumes async transaction jobs from RabbitMQ.
type Consumer struct {
	cfg config.RabbitMQConfig
}

// NewConsumer constructs a RabbitMQ consumer from runtime config.
func NewConsumer(cfg config.RabbitMQConfig) *Consumer {
	return &Consumer{cfg: cfg}
}

// RunQRISPaymentWorker keeps a QRIS consumer alive until the parent context is canceled.
func (c *Consumer) RunQRISPaymentWorker(ctx context.Context, handler QRISPaymentHandler) error {
	if c.cfg.URL == "" {
		return fmt.Errorf("rabbitmq url is not configured")
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		err := c.runQRISPaymentSession(ctx, handler)
		if err == nil || ctx.Err() != nil {
			return err
		}

		timer := time.NewTimer(workerReconnectDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// RunTransferWorker keeps a transfer consumer alive until the parent context is canceled.
func (c *Consumer) RunTransferWorker(ctx context.Context, handler TransferHandler) error {
	if c.cfg.URL == "" {
		return fmt.Errorf("rabbitmq url is not configured")
	}

	for {
		if ctx.Err() != nil {
			return nil
		}

		err := c.runTransferSession(ctx, handler)
		if err == nil || ctx.Err() != nil {
			return err
		}

		timer := time.NewTimer(workerReconnectDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (c *Consumer) runQRISPaymentSession(ctx context.Context, handler QRISPaymentHandler) error {
	queueName := c.cfg.QRISPaymentsQueue
	if queueName == "" {
		queueName = "dante.qris.payments"
	}

	return c.runSession(ctx, queueName, func(ctx context.Context, body []byte) error {
		var msg QRISPaymentMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			return err
		}
		return handler.HandleQRISPayment(ctx, msg)
	})
}

func (c *Consumer) runTransferSession(ctx context.Context, handler TransferHandler) error {
	queueName := c.cfg.TransfersQueue
	if queueName == "" {
		queueName = "dante.transfers"
	}

	return c.runSession(ctx, queueName, func(ctx context.Context, body []byte) error {
		var msg TransferMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			return err
		}
		return handler.HandleTransfer(ctx, msg)
	})
}

func (c *Consumer) runSession(ctx context.Context, queueName string, handle func(context.Context, []byte) error) error {
	dialer := amqp.Config{
		Dial: amqp.DefaultDial(c.cfg.DialTimeout),
	}

	conn, err := amqp.DialConfig(c.cfg.URL, dialer)
	if err != nil {
		return fmt.Errorf("dial rabbitmq: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open rabbitmq channel: %w", err)
	}
	defer ch.Close()

	_, err = ch.QueueDeclare(
		queueName,
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("declare rabbitmq queue: %w", err)
	}

	deliveries, err := ch.Consume(
		queueName,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return fmt.Errorf("consume rabbitmq queue: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("rabbitmq delivery channel closed")
			}

			jobCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := handle(jobCtx, delivery.Body)
			cancel()
			if err != nil {
				if _, ok := err.(*json.SyntaxError); ok {
					_ = delivery.Reject(false)
					continue
				}
				if _, ok := err.(*json.UnmarshalTypeError); ok {
					_ = delivery.Reject(false)
					continue
				}
				_ = delivery.Nack(false, true)
				continue
			}

			if err := delivery.Ack(false); err != nil {
				_ = delivery.Reject(false)
			}
		}
	}
}
