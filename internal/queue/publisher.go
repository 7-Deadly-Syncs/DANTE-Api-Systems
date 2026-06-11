package queue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	amqp "github.com/rabbitmq/amqp091-go"
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
	queueName := p.cfg.QRISPaymentsQueue
	if queueName == "" {
		queueName = "dante.qris.payments"
	}

	if err := p.publishJSON(ctx, queueName, msg); err != nil {
		return fmt.Errorf("publish qris payment message: %w", err)
	}

	return nil
}

// PublishTransfer publishes a transfer message into the configured queue.
func (p *Publisher) PublishTransfer(ctx context.Context, msg TransferMessage) error {
	queueName := p.cfg.TransfersQueue
	if queueName == "" {
		queueName = "dante.transfers"
	}

	if err := p.publishJSON(ctx, queueName, msg); err != nil {
		return fmt.Errorf("publish transfer message: %w", err)
	}

	return nil
}

func (p *Publisher) publishJSON(ctx context.Context, queueName string, msg any) error {
	if p.cfg.URL == "" {
		return fmt.Errorf("rabbitmq url is not configured")
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal queue message: %w", err)
	}

	dialer := amqp.Config{
		Dial: amqp.DefaultDial(p.cfg.DialTimeout),
	}

	conn, err := amqp.DialConfig(p.cfg.URL, dialer)
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

	if err := ch.PublishWithContext(ctx, "", queueName, false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
	}); err != nil {
		return fmt.Errorf("publish queue message: %w", err)
	}

	return nil
}
