package metrics

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/cachemetrics"
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
	startedAt time.Time

	registry    *prometheus.Registry
	promHandler http.Handler

	httpRequestsTotal    *prometheus.CounterVec
	httpRequestDuration  *prometheus.HistogramVec
	httpRequestsInFlight prometheus.Gauge
}

// NewHandler constructs a Prometheus-compatible metrics handler.
func NewHandler(config Config, cache Snapshotter, db *sql.DB, redisClient *redis.Client) *Handler {
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

	registry.MustRegister(
		buildInfo,
		uptime,
		httpRequestsTotal,
		httpRequestDuration,
		httpRequestsInFlight,
	)

	registerDatabaseStats(registry, db)
	registerRedisStats(registry, redisClient)

	if cache != nil {
		registry.MustRegister(newCacheSnapshotCollector(cache))
	}

	handler := &Handler{
		config:               config,
		cache:                cache,
		db:                   db,
		redis:                redisClient,
		startedAt:            startedAt,
		registry:             registry,
		httpRequestsTotal:    httpRequestsTotal,
		httpRequestDuration:  httpRequestDuration,
		httpRequestsInFlight: httpRequestsInFlight,
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
