package queue

import (
	"testing"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	amqp "github.com/rabbitmq/amqp091-go"
)

func TestRetryDelayForAttemptUsesExponentialBackoff(t *testing.T) {
	t.Parallel()

	cfg := config.RabbitMQConfig{
		RetryBaseDelay: 500 * time.Millisecond,
	}

	if got := retryDelayForAttempt(cfg, 1); got != 500*time.Millisecond {
		t.Fatalf("attempt 1 delay = %v, want %v", got, 500*time.Millisecond)
	}
	if got := retryDelayForAttempt(cfg, 2); got != time.Second {
		t.Fatalf("attempt 2 delay = %v, want %v", got, time.Second)
	}
	if got := retryDelayForAttempt(cfg, 3); got != 2*time.Second {
		t.Fatalf("attempt 3 delay = %v, want %v", got, 2*time.Second)
	}
}

func TestRetryCountFromHeadersSupportsCommonTypes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		headers amqp.Table
		want    int
	}{
		{name: "missing", headers: nil, want: 0},
		{name: "int32", headers: amqp.Table{headerRetryCount: int32(2)}, want: 2},
		{name: "int64", headers: amqp.Table{headerRetryCount: int64(3)}, want: 3},
		{name: "int", headers: amqp.Table{headerRetryCount: 4}, want: 4},
		{name: "string", headers: amqp.Table{headerRetryCount: "5"}, want: 5},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := retryCountFromHeaders(tc.headers); got != tc.want {
				t.Fatalf("retryCountFromHeaders() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestMessageLagUsesFirstEnqueuedTimestamp(t *testing.T) {
	t.Parallel()

	startedAt := time.Now().UTC().Add(-1500 * time.Millisecond)
	lag := messageLag(amqp.Table{
		headerFirstEnqueuedAt: startedAt.Format(time.RFC3339Nano),
	})
	if lag < time.Second || lag > 3*time.Second {
		t.Fatalf("message lag = %v, want between 1s and 3s", lag)
	}
}
