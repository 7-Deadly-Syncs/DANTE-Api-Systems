package queue

import (
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	defaultQRISQueue        = "dante.qris.payments"
	defaultTransfersQueue   = "dante.transfers"
	defaultMaxRetryAttempts = 3
	defaultRetryBaseDelay   = 2 * time.Second
	defaultWorkerCount      = 1
	defaultWorkerPrefetch   = 1
	headerRetryCount        = "x-retry-count"
	headerFirstEnqueuedAt   = "x-first-enqueued-at"
)

type queueNames struct {
	Primary    string
	Retry      string
	DeadLetter string
}

func qrisQueueName(cfg config.RabbitMQConfig) string {
	if cfg.QRISPaymentsQueue != "" {
		return cfg.QRISPaymentsQueue
	}
	return defaultQRISQueue
}

func transfersQueueName(cfg config.RabbitMQConfig) string {
	if cfg.TransfersQueue != "" {
		return cfg.TransfersQueue
	}
	return defaultTransfersQueue
}

func namesForQueue(primary string) queueNames {
	return queueNames{
		Primary:    primary,
		Retry:      primary + ".retry",
		DeadLetter: primary + ".dlq",
	}
}

func maxRetryAttempts(cfg config.RabbitMQConfig) int {
	if cfg.MaxRetryAttempts > 0 {
		return cfg.MaxRetryAttempts
	}
	return defaultMaxRetryAttempts
}

func retryBaseDelay(cfg config.RabbitMQConfig) time.Duration {
	if cfg.RetryBaseDelay > 0 {
		return cfg.RetryBaseDelay
	}
	return defaultRetryBaseDelay
}

func workerCount(value int) int {
	if value > 0 {
		return value
	}
	return defaultWorkerCount
}

func prefetchCount(cfg config.RabbitMQConfig) int {
	if cfg.PrefetchCount > 0 {
		return cfg.PrefetchCount
	}
	return defaultWorkerPrefetch
}

func retryDelayForAttempt(cfg config.RabbitMQConfig, attempt int) time.Duration {
	delay := retryBaseDelay(cfg)
	if attempt <= 1 {
		return delay
	}
	for i := 1; i < attempt; i++ {
		delay *= 2
	}
	return delay
}

func declareQueueTopology(ch *amqp.Channel, names queueNames) error {
	mainArgs := amqp.Table{
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": names.DeadLetter,
	}
	if _, err := ch.QueueDeclare(names.Primary, true, false, false, false, mainArgs); err != nil {
		return fmt.Errorf("declare primary queue %s: %w", names.Primary, err)
	}

	retryArgs := amqp.Table{
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": names.Primary,
	}
	if _, err := ch.QueueDeclare(names.Retry, true, false, false, false, retryArgs); err != nil {
		return fmt.Errorf("declare retry queue %s: %w", names.Retry, err)
	}

	if _, err := ch.QueueDeclare(names.DeadLetter, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dead-letter queue %s: %w", names.DeadLetter, err)
	}

	return nil
}
