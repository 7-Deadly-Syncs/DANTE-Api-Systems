package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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

// Observer receives queue-processing telemetry events.
type Observer interface {
	ObserveWorkerLag(queue string, lag time.Duration)
	ObserveRetry(queue string, attempt int, terminal bool)
}

// Consumer consumes async transaction jobs from RabbitMQ.
type Consumer struct {
	cfg      config.RabbitMQConfig
	observer Observer
}

// NewConsumer constructs a RabbitMQ consumer from runtime config.
func NewConsumer(cfg config.RabbitMQConfig, observer Observer) *Consumer {
	return &Consumer{cfg: cfg, observer: observer}
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
	queueName := qrisQueueName(c.cfg)

	return c.runSession(ctx, namesForQueue(queueName), func(ctx context.Context, body []byte) error {
		var msg QRISPaymentMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			return err
		}
		return handler.HandleQRISPayment(ctx, msg)
	})
}

func (c *Consumer) runTransferSession(ctx context.Context, handler TransferHandler) error {
	queueName := transfersQueueName(c.cfg)

	return c.runSession(ctx, namesForQueue(queueName), func(ctx context.Context, body []byte) error {
		var msg TransferMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			return err
		}
		return handler.HandleTransfer(ctx, msg)
	})
}

func (c *Consumer) runSession(ctx context.Context, names queueNames, handle func(context.Context, []byte) error) error {
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

	if err := declareQueueTopology(ch, names); err != nil {
		return fmt.Errorf("declare rabbitmq queue topology: %w", err)
	}

	if err := ch.Qos(prefetchCount(c.cfg), 0, false); err != nil {
		return fmt.Errorf("configure rabbitmq qos: %w", err)
	}

	deliveries, err := ch.Consume(
		names.Primary,
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

			if c.observer != nil {
				if lag := messageLag(delivery.Headers); lag > 0 {
					c.observer.ObserveWorkerLag(names.Primary, lag)
				}
			}

			jobCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := handle(jobCtx, delivery.Body)
			cancel()
			if err != nil {
				if _, ok := err.(*json.SyntaxError); ok {
					if c.observer != nil {
						c.observer.ObserveRetry(names.Primary, retryCountFromHeaders(delivery.Headers), true)
					}
					_ = delivery.Reject(false)
					continue
				}
				if _, ok := err.(*json.UnmarshalTypeError); ok {
					if c.observer != nil {
						c.observer.ObserveRetry(names.Primary, retryCountFromHeaders(delivery.Headers), true)
					}
					_ = delivery.Reject(false)
					continue
				}

				attempt := retryCountFromHeaders(delivery.Headers) + 1
				if attempt > maxRetryAttempts(c.cfg) {
					if c.observer != nil {
						c.observer.ObserveRetry(names.Primary, attempt, true)
					}
					_ = delivery.Reject(false)
					continue
				}

				if err := c.publishRetry(ctx, ch, names, delivery, attempt); err != nil {
					_ = delivery.Nack(false, true)
					continue
				}
				if c.observer != nil {
					c.observer.ObserveRetry(names.Primary, attempt, false)
				}
				_ = delivery.Ack(false)
				continue
			}

			if err := delivery.Ack(false); err != nil {
				_ = delivery.Reject(false)
			}
		}
	}
}

func (c *Consumer) publishRetry(ctx context.Context, ch *amqp.Channel, names queueNames, delivery amqp.Delivery, attempt int) error {
	headers := cloneHeaders(delivery.Headers)
	headers[headerRetryCount] = int32(attempt)
	if _, ok := headers[headerFirstEnqueuedAt]; !ok {
		headers[headerFirstEnqueuedAt] = time.Now().UTC().Format(time.RFC3339Nano)
	}

	retryDelay := retryDelayForAttempt(c.cfg, attempt)
	return ch.PublishWithContext(ctx, "", names.Retry, false, false, amqp.Publishing{
		ContentType:     delivery.ContentType,
		ContentEncoding: delivery.ContentEncoding,
		Headers:         headers,
		Body:            delivery.Body,
		Timestamp:       time.Now().UTC(),
		Expiration:      strconv.FormatInt(retryDelay.Milliseconds(), 10),
	})
}

func retryCountFromHeaders(headers amqp.Table) int {
	if headers == nil {
		return 0
	}

	switch value := headers[headerRetryCount].(type) {
	case int32:
		return int(value)
	case int64:
		return int(value)
	case int:
		return value
	case string:
		parsed, err := strconv.Atoi(value)
		if err == nil {
			return parsed
		}
	}

	return 0
}

func messageLag(headers amqp.Table) time.Duration {
	if headers == nil {
		return 0
	}

	raw, ok := headers[headerFirstEnqueuedAt]
	if !ok {
		return 0
	}

	value, ok := raw.(string)
	if !ok {
		return 0
	}

	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}

	lag := time.Since(timestamp)
	if lag < 0 {
		return 0
	}
	return lag
}

func cloneHeaders(headers amqp.Table) amqp.Table {
	if len(headers) == 0 {
		return amqp.Table{}
	}

	cloned := make(amqp.Table, len(headers))
	for key, value := range headers {
		cloned[key] = value
	}
	return cloned
}
