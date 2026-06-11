package metrics

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/cachemetrics"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/queue"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

// Snapshotter exposes cache metrics in a form suitable for export.
type Snapshotter interface {
	Snapshot() []cachemetrics.SnapshotEntry
}

// Config contains static metadata exported with metrics.
type Config struct {
	Service     string
	Version     string
	Environment string
}

// Handler renders Prometheus metrics and owns the Prometheus registry.
type Handler struct {
	config    Config
	cache     Snapshotter
	db        *sql.DB
	redis     *redis.Client
	rabbitmq  config.RabbitMQConfig
	startedAt time.Time

	registry    *prometheus.Registry
	promHandler http.Handler

	httpRequestsTotal    *prometheus.CounterVec
	httpRequestDuration  *prometheus.HistogramVec
	httpRequestsInFlight prometheus.Gauge
	workerLagSeconds     *prometheus.HistogramVec
	workerRetriesTotal   *prometheus.CounterVec
}

// NewHandler constructs a Prometheus-compatible metrics handler.
func NewHandler(config Config, cache Snapshotter, db *sql.DB, redisClient *redis.Client, rabbitmq config.RabbitMQConfig) *Handler {
	startedAt := time.Now().UTC()

	registry := prometheus.NewRegistry()

	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dante_build_info",
		Help: "Static build and environment metadata for the running service.",
	}, []string{"service", "version", "environment"})
	buildInfo.WithLabelValues(config.Service, config.Version, config.Environment).Set(1)

	uptime := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "dante_process_uptime_seconds",
		Help: "Seconds since the DANTE process started.",
	}, func() float64 {
		return time.Since(startedAt).Seconds()
	})

	httpRequestsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "dante",
		Subsystem: "http",
		Name:      "requests_total",
		Help:      "Total HTTP requests handled by method, route, and status code.",
	}, []string{"method", "path", "status_code"})

	httpRequestDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "dante",
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "HTTP request duration distribution in seconds.",
		Buckets: []float64{
			0.005,
			0.010,
			0.025,
			0.050,
			0.100,
			0.200,
			0.500,
			1.000,
			2.500,
			5.000,
		},
	}, []string{"method", "path"})

	httpRequestsInFlight := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "dante",
		Subsystem: "http",
		Name:      "requests_in_flight",
		Help:      "Current number of HTTP requests being processed.",
	})

	workerLagSeconds := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "dante",
		Subsystem: "worker",
		Name:      "message_lag_seconds",
		Help:      "Lag between original queue enqueue time and worker consumption.",
		Buckets: []float64{
			0.010,
			0.050,
			0.100,
			0.250,
			0.500,
			1.000,
			2.500,
			5.000,
			15.000,
			30.000,
			60.000,
		},
	}, []string{"queue"})

	workerRetriesTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "dante",
		Subsystem: "worker",
		Name:      "retries_total",
		Help:      "Total worker retries and terminal dead-letter outcomes by queue.",
	}, []string{"queue", "outcome"})

	registry.MustRegister(
		buildInfo,
		uptime,
		httpRequestsTotal,
		httpRequestDuration,
		httpRequestsInFlight,
		workerLagSeconds,
		workerRetriesTotal,
	)

	registerDatabaseStats(registry, db)
	registerRedisStats(registry, redisClient)
	if db != nil {
		registry.MustRegister(newSQLCMetricsCollector(db))
	}
	if rabbitmq.URL != "" {
		registry.MustRegister(newRabbitMQCollector(rabbitmq))
	}

	if cache != nil {
		registry.MustRegister(newCacheSnapshotCollector(cache))
	}

	handler := &Handler{
		config:               config,
		cache:                cache,
		db:                   db,
		redis:                redisClient,
		rabbitmq:             rabbitmq,
		startedAt:            startedAt,
		registry:             registry,
		httpRequestsTotal:    httpRequestsTotal,
		httpRequestDuration:  httpRequestDuration,
		httpRequestsInFlight: httpRequestsInFlight,
		workerLagSeconds:     workerLagSeconds,
		workerRetriesTotal:   workerRetriesTotal,
	}

	handler.promHandler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})

	return handler
}

// ServeHTTP serves Prometheus metrics.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.promHandler.ServeHTTP(w, r)
}

// IncHTTPInFlight increments current in-flight HTTP request gauge.
func (h *Handler) IncHTTPInFlight() {
	h.httpRequestsInFlight.Inc()
}

// DecHTTPInFlight decrements current in-flight HTTP request gauge.
func (h *Handler) DecHTTPInFlight() {
	h.httpRequestsInFlight.Dec()
}

// ObserveHTTPRequest records HTTP request count and latency.
func (h *Handler) ObserveHTTPRequest(method, path string, statusCode int, duration time.Duration) {
	h.httpRequestsTotal.WithLabelValues(method, path, strconv.Itoa(statusCode)).Inc()
	h.httpRequestDuration.WithLabelValues(method, path).Observe(duration.Seconds())
}

// ObserveWorkerLag records how long a message sat in the queue before a worker consumed it.
func (h *Handler) ObserveWorkerLag(queueName string, lag time.Duration) {
	h.workerLagSeconds.WithLabelValues(queueName).Observe(lag.Seconds())
}

// ObserveRetry records whether a worker retried a message or sent it to a terminal path.
func (h *Handler) ObserveRetry(queueName string, attempt int, terminal bool) {
	outcome := "retry"
	if terminal {
		outcome = "dead_letter"
	}
	h.workerRetriesTotal.WithLabelValues(queueName, outcome).Inc()
}

func registerDatabaseStats(registry *prometheus.Registry, db *sql.DB) {
	if db == nil {
		return
	}

	registry.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "dante_db_open_connections",
			Help: "Number of open PostgreSQL connections.",
		}, func() float64 {
			return float64(db.Stats().OpenConnections)
		}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "dante_db_in_use_connections",
			Help: "Number of PostgreSQL connections currently in use.",
		}, func() float64 {
			return float64(db.Stats().InUse)
		}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "dante_db_idle_connections",
			Help: "Number of idle PostgreSQL connections.",
		}, func() float64 {
			return float64(db.Stats().Idle)
		}),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "dante_db_wait_count_total",
			Help: "Total number of waits for a PostgreSQL connection.",
		}, func() float64 {
			return float64(db.Stats().WaitCount)
		}),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "dante_db_wait_duration_seconds_total",
			Help: "Total time spent waiting for a PostgreSQL connection.",
		}, func() float64 {
			return db.Stats().WaitDuration.Seconds()
		}),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "dante_db_max_idle_closed_total",
			Help: "Total PostgreSQL connections closed due to idle count limits.",
		}, func() float64 {
			return float64(db.Stats().MaxIdleClosed)
		}),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "dante_db_max_idle_time_closed_total",
			Help: "Total PostgreSQL connections closed due to idle time limits.",
		}, func() float64 {
			return float64(db.Stats().MaxIdleTimeClosed)
		}),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "dante_db_max_lifetime_closed_total",
			Help: "Total PostgreSQL connections closed due to lifetime limits.",
		}, func() float64 {
			return float64(db.Stats().MaxLifetimeClosed)
		}),
	)
}

func registerRedisStats(registry *prometheus.Registry, redisClient *redis.Client) {
	if redisClient == nil {
		return
	}

	registry.MustRegister(
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "dante_redis_pool_hits_total",
			Help: "Total Redis pool hits.",
		}, func() float64 {
			return float64(redisClient.PoolStats().Hits)
		}),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "dante_redis_pool_misses_total",
			Help: "Total Redis pool misses.",
		}, func() float64 {
			return float64(redisClient.PoolStats().Misses)
		}),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "dante_redis_pool_timeouts_total",
			Help: "Total Redis pool timeouts.",
		}, func() float64 {
			return float64(redisClient.PoolStats().Timeouts)
		}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "dante_redis_pool_total_connections",
			Help: "Current total Redis pool connections.",
		}, func() float64 {
			return float64(redisClient.PoolStats().TotalConns)
		}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "dante_redis_pool_idle_connections",
			Help: "Current idle Redis pool connections.",
		}, func() float64 {
			return float64(redisClient.PoolStats().IdleConns)
		}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "dante_redis_pool_stale_connections",
			Help: "Current stale Redis pool connections.",
		}, func() float64 {
			return float64(redisClient.PoolStats().StaleConns)
		}),
	)
}

type cacheSnapshotCollector struct {
	snapshotter Snapshotter
}

func newCacheSnapshotCollector(snapshotter Snapshotter) *cacheSnapshotCollector {
	return &cacheSnapshotCollector{
		snapshotter: snapshotter,
	}
}

// Describe intentionally sends no descriptors, making this an unchecked collector.
// This allows the in-memory cache recorder to expose currently known metric names.
func (c *cacheSnapshotCollector) Describe(ch chan<- *prometheus.Desc) {}

func (c *cacheSnapshotCollector) Collect(ch chan<- prometheus.Metric) {
	for _, entry := range c.snapshotter.Snapshot() {
		desc := prometheus.NewDesc(
			entry.Name,
			"Application cache counter.",
			nil,
			nil,
		)

		metric, err := prometheus.NewConstMetric(
			desc,
			prometheus.CounterValue,
			float64(entry.Value),
		)
		if err != nil {
			continue
		}

		ch <- metric
	}
}

type rabbitMQCollector struct {
	cfg config.RabbitMQConfig
}

func newRabbitMQCollector(cfg config.RabbitMQConfig) *rabbitMQCollector {
	return &rabbitMQCollector{cfg: cfg}
}

func (c *rabbitMQCollector) Describe(ch chan<- *prometheus.Desc) {}

func (c *rabbitMQCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stats, err := queue.InspectConfiguredQueues(ctx, c.cfg)
	if err != nil {
		return
	}

	for _, entry := range stats {
		depthDesc := prometheus.NewDesc(
			"dante_rabbitmq_queue_messages",
			"Current RabbitMQ message depth for a DANTE queue.",
			[]string{"queue"},
			nil,
		)
		consumersDesc := prometheus.NewDesc(
			"dante_rabbitmq_queue_consumers",
			"Current RabbitMQ active consumer count for a DANTE queue.",
			[]string{"queue"},
			nil,
		)

		depthMetric, depthErr := prometheus.NewConstMetric(depthDesc, prometheus.GaugeValue, float64(entry.Messages), entry.Name)
		if depthErr == nil {
			ch <- depthMetric
		}

		consumersMetric, consumersErr := prometheus.NewConstMetric(consumersDesc, prometheus.GaugeValue, float64(entry.Consumers), entry.Name)
		if consumersErr == nil {
			ch <- consumersMetric
		}
	}
}

type sqlcMetricsCollector struct {
	queries *dbsqlc.Queries
}

func newSQLCMetricsCollector(db *sql.DB) *sqlcMetricsCollector {
	return &sqlcMetricsCollector{
		queries: dbsqlc.New(db),
	}
}

func (c *sqlcMetricsCollector) Describe(ch chan<- *prometheus.Desc) {}

func (c *sqlcMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	legacyMetrics, err := c.queries.GetLegacyCallMetrics(ctx)
	if err == nil {
		for _, row := range legacyMetrics {
			latencyDesc := prometheus.NewDesc(
				"dante_legacy_call_avg_latency_milliseconds",
				"Average legacy call latency in milliseconds grouped by endpoint and success state.",
				[]string{"endpoint", "success"},
				nil,
			)
			totalDesc := prometheus.NewDesc(
				"dante_legacy_call_total",
				"Total legacy calls grouped by endpoint and success state.",
				[]string{"endpoint", "success"},
				nil,
			)

			successLabel := strconv.FormatBool(row.Success)
			latencyMetric, latencyErr := prometheus.NewConstMetric(latencyDesc, prometheus.GaugeValue, float64(row.AvgLatencyMs), row.Endpoint, successLabel)
			if latencyErr == nil {
				ch <- latencyMetric
			}

			totalMetric, totalErr := prometheus.NewConstMetric(totalDesc, prometheus.GaugeValue, float64(row.CallsTotal), row.Endpoint, successLabel)
			if totalErr == nil {
				ch <- totalMetric
			}
		}
	}

	stateCounts, err := c.queries.GetTransactionStateCounts(ctx)
	if err == nil {
		for _, row := range stateCounts {
			desc := prometheus.NewDesc(
				"dante_transactions_by_state",
				"Current number of transactions grouped by lifecycle state.",
				[]string{"status"},
				nil,
			)
			metric, metricErr := prometheus.NewConstMetric(desc, prometheus.GaugeValue, float64(row.TransactionsTotal), row.Status)
			if metricErr == nil {
				ch <- metric
			}
		}
	}
}
