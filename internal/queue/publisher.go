package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel/attribute"
)

// QRISPaymentMessage is the queue payload emitted for async QRIS processing.
type QRISPaymentMessage struct {
	TransactionID string `json:"transaction_id"`
	AccountUUID   string `json:"account_uuid"`
	AccountID     string `json:"account_id"`
	AccountNumber string `json:"account_number"`
	MerchantID    string `json:"merchant_id"`
	MerchantCode  string `json:"merchant_code"`
	Amount        int64  `json:"amount"`
}

// TransferMessage is the queue payload emitted for async transfer processing.
type TransferMessage struct {
	TransactionID     string `json:"transaction_id"`
	AccountUUID       string `json:"account_uuid"`
	FromAccountID     string `json:"from_account_id"`
	FromAccountNumber string `json:"from_account_number"`
	ToAccountNumber   string `json:"to_account_number"`
	TransactionPIN    string `json:"transaction_pin"`
	Amount            int64  `json:"amount"`
}

// Publisher publishes transaction jobs into RabbitMQ.
type Publisher struct {
	cfg config.RabbitMQConfig
}

// NewPublisher constructs a queue publisher from runtime config.
func NewPublisher(cfg config.RabbitMQConfig) *Publisher {
	return &Publisher{cfg: cfg}
}

// PublishQRISPayment publishes a QRIS payment message into the configured queue.
func (p *Publisher) PublishQRISPayment(ctx context.Context, msg QRISPaymentMessage) error {
	if err := p.publishJSON(ctx, namesForQueue(qrisQueueName(p.cfg)).Primary, "qris", msg); err != nil {
		return fmt.Errorf("publish qris payment message: %w", err)
	}

	return nil
}

// PublishTransfer publishes a transfer message into the configured queue.
func (p *Publisher) PublishTransfer(ctx context.Context, msg TransferMessage) error {
	if err := p.publishJSON(ctx, namesForQueue(transfersQueueName(p.cfg)).Primary, "transfer", msg); err != nil {
		return fmt.Errorf("publish transfer message: %w", err)
	}

	return nil
}

func (p *Publisher) publishJSON(ctx context.Context, queueName, messageType string, msg any) error {
	ctx, span := tracing.StartProducerSpan(ctx, "rabbitmq", "rabbitmq.publish "+queueName,
		attribute.String("messaging.system", "rabbitmq"),
		attribute.String("messaging.operation", "publish"),
		attribute.String("messaging.destination.name", queueName),
		attribute.String("messaging.message.type", messageType),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	if p.cfg.URL == "" {
		spanErr = fmt.Errorf("rabbitmq url is not configured")
		return fmt.Errorf("rabbitmq url is not configured")
	}

	body, err := json.Marshal(msg)
	if err != nil {
		spanErr = err
		return fmt.Errorf("marshal queue message: %w", err)
	}
	span.SetAttributes(attribute.Int("messaging.message.body.size", len(body)))

	dialer := amqp.Config{
		Dial: amqp.DefaultDial(p.cfg.DialTimeout),
	}

	conn, err := amqp.DialConfig(p.cfg.URL, dialer)
	if err != nil {
		spanErr = err
		return fmt.Errorf("dial rabbitmq: %w", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		spanErr = err
		return fmt.Errorf("open rabbitmq channel: %w", err)
	}
	defer ch.Close()

	if err := declareQueueTopology(ch, namesForQueue(queueName)); err != nil {
		spanErr = err
		return fmt.Errorf("declare rabbitmq queue topology: %w", err)
	}

	now := time.Now().UTC()
	headers := injectTraceContext(ctx, amqp.Table{
		headerRetryCount:      int32(0),
		headerFirstEnqueuedAt: now.Format(time.RFC3339Nano),
	})
	if err := ch.PublishWithContext(ctx, "", queueName, false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
		Timestamp:   now,
		Headers:     headers,
	}); err != nil {
		spanErr = err
		return fmt.Errorf("publish queue message: %w", err)
	}

	return nil
}
