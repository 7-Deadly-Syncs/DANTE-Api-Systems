package queue

import amqp "github.com/rabbitmq/amqp091-go"

type amqpHeaderCarrier struct {
	headers amqp.Table
}

func newAMQPHeaderCarrier(headers amqp.Table) amqpHeaderCarrier {
	if headers == nil {
		headers = amqp.Table{}
	}
	return amqpHeaderCarrier{headers: headers}
}

func (c amqpHeaderCarrier) Get(key string) string {
	if c.headers == nil {
		return ""
	}

	switch value := c.headers[key].(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return ""
	}
}

func (c amqpHeaderCarrier) Set(key, value string) {
	if c.headers != nil {
		c.headers[key] = value
	}
}

func (c amqpHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(c.headers))
	for key := range c.headers {
		keys = append(keys, key)
	}
	return keys
}

