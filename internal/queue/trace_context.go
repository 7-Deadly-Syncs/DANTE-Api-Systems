package queue

import (
	"context"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
)

type amqpTableCarrier struct {
	headers amqp.Table
}

func injectTraceContext(ctx context.Context, headers amqp.Table) amqp.Table {
	if headers == nil {
		headers = amqp.Table{}
	}

	otel.GetTextMapPropagator().Inject(ctx, amqpTableCarrier{headers: headers})
	return headers
}

func extractTraceContext(ctx context.Context, headers amqp.Table) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, amqpTableCarrier{headers: headers})
}

func (c amqpTableCarrier) Get(key string) string {
	if c.headers == nil {
		return ""
	}

	value, ok := c.headers[key]
	if !ok || value == nil {
		return ""
	}

	if text, ok := value.(string); ok {
		return text
	}

	return fmt.Sprint(value)
}

func (c amqpTableCarrier) Set(key, value string) {
	if c.headers == nil {
		return
	}

	c.headers[key] = value
}

func (c amqpTableCarrier) Keys() []string {
	keys := make([]string, 0, len(c.headers))
	for key := range c.headers {
		keys = append(keys, key)
	}
	return keys
}
