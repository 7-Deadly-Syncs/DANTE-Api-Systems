package metrics

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/cachemetrics"
	"github.com/redis/go-redis/v9"
)

const contentType = "text/plain; version=0.0.4; charset=utf-8"

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

// Handler renders Prometheus exposition text for app-level and pool metrics.
type Handler struct {
	config    Config
	cache     Snapshotter
	db        *sql.DB
	redis     *redis.Client
	startedAt time.Time
}

// NewHandler constructs a Prometheus-compatible metrics handler.
func NewHandler(config Config, cache Snapshotter, db *sql.DB, redisClient *redis.Client) *Handler {
	return &Handler{
		config:    config,
		cache:     cache,
		db:        db,
		redis:     redisClient,
		startedAt: time.Now().UTC(),
	}
}

// ServeHTTP writes metrics in Prometheus text exposition format.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", contentType)

	var builder strings.Builder
	h.writeBuildInfo(&builder)
	h.writeUptime(&builder)
	h.writeDatabaseStats(&builder)
	h.writeRedisStats(&builder)
	h.writeCacheStats(&builder)

	_, _ = w.Write([]byte(builder.String()))
}

func (h *Handler) writeBuildInfo(builder *strings.Builder) {
	builder.WriteString("# HELP dante_build_info Static build and environment metadata for the running service.\n")
	builder.WriteString("# TYPE dante_build_info gauge\n")
	builder.WriteString(fmt.Sprintf(
		"dante_build_info{service=%s,version=%s,environment=%s} 1\n",
		quoteLabelValue(h.config.Service),
		quoteLabelValue(h.config.Version),
		quoteLabelValue(h.config.Environment),
	))
}

func (h *Handler) writeUptime(builder *strings.Builder) {
	builder.WriteString("# HELP dante_process_uptime_seconds Seconds since the DANTE process started.\n")
	builder.WriteString("# TYPE dante_process_uptime_seconds gauge\n")
	builder.WriteString(fmt.Sprintf("dante_process_uptime_seconds %.6f\n", time.Since(h.startedAt).Seconds()))
}

func (h *Handler) writeDatabaseStats(builder *strings.Builder) {
	stats := h.db.Stats()

	writeGauge(builder, "dante_db_open_connections", "Number of open PostgreSQL connections.", float64(stats.OpenConnections))
	writeGauge(builder, "dante_db_in_use_connections", "Number of PostgreSQL connections currently in use.", float64(stats.InUse))
	writeGauge(builder, "dante_db_idle_connections", "Number of idle PostgreSQL connections.", float64(stats.Idle))
	writeCounter(builder, "dante_db_wait_count_total", "Total number of waits for a PostgreSQL connection.", float64(stats.WaitCount))
	writeCounter(builder, "dante_db_wait_duration_seconds_total", "Total time spent waiting for a PostgreSQL connection.", stats.WaitDuration.Seconds())
	writeCounter(builder, "dante_db_max_idle_closed_total", "Total PostgreSQL connections closed due to idle count limits.", float64(stats.MaxIdleClosed))
	writeCounter(builder, "dante_db_max_idle_time_closed_total", "Total PostgreSQL connections closed due to idle time limits.", float64(stats.MaxIdleTimeClosed))
	writeCounter(builder, "dante_db_max_lifetime_closed_total", "Total PostgreSQL connections closed due to lifetime limits.", float64(stats.MaxLifetimeClosed))
}

func (h *Handler) writeRedisStats(builder *strings.Builder) {
	stats := h.redis.PoolStats()

	writeCounter(builder, "dante_redis_pool_hits_total", "Total Redis pool hits.", float64(stats.Hits))
	writeCounter(builder, "dante_redis_pool_misses_total", "Total Redis pool misses.", float64(stats.Misses))
	writeCounter(builder, "dante_redis_pool_timeouts_total", "Total Redis pool timeouts.", float64(stats.Timeouts))
	writeGauge(builder, "dante_redis_pool_total_connections", "Current total Redis pool connections.", float64(stats.TotalConns))
	writeGauge(builder, "dante_redis_pool_idle_connections", "Current idle Redis pool connections.", float64(stats.IdleConns))
	writeGauge(builder, "dante_redis_pool_stale_connections", "Current stale Redis pool connections.", float64(stats.StaleConns))
}

func (h *Handler) writeCacheStats(builder *strings.Builder) {
	for _, entry := range h.cache.Snapshot() {
		writeCounter(builder, entry.Name, "Application cache counter.", float64(entry.Value))
	}
}

func writeCounter(builder *strings.Builder, name, help string, value float64) {
	builder.WriteString("# HELP ")
	builder.WriteString(name)
	builder.WriteByte(' ')
	builder.WriteString(help)
	builder.WriteByte('\n')
	builder.WriteString("# TYPE ")
	builder.WriteString(name)
	builder.WriteString(" counter\n")
	builder.WriteString(name)
	builder.WriteByte(' ')
	builder.WriteString(strconv.FormatFloat(value, 'f', -1, 64))
	builder.WriteByte('\n')
}

func writeGauge(builder *strings.Builder, name, help string, value float64) {
	builder.WriteString("# HELP ")
	builder.WriteString(name)
	builder.WriteByte(' ')
	builder.WriteString(help)
	builder.WriteByte('\n')
	builder.WriteString("# TYPE ")
	builder.WriteString(name)
	builder.WriteString(" gauge\n")
	builder.WriteString(name)
	builder.WriteByte(' ')
	builder.WriteString(strconv.FormatFloat(value, 'f', -1, 64))
	builder.WriteByte('\n')
}

func quoteLabelValue(value string) string {
	return strconv.Quote(value)
}
