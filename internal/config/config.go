package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config contains the runtime configuration used by the service.
type Config struct {
	App      AppConfig
	Redis    RedisConfig
	RabbitMQ RabbitMQConfig
	Database DatabaseConfig
	Observability ObservabilityConfig
}

// AppConfig contains application-level settings.
type AppConfig struct {
	Environment string
	Version     string
	Port        string
}

// DatabaseConfig contains PostgreSQL connection settings.
type DatabaseConfig struct {
	Host            string
	ExternalHost    string
	Port            int
	ExternalPort    int
	User            string
	Password        string
	Name            string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxIdleTime time.Duration
	ConnMaxLifetime time.Duration
}

// RedisConfig contains Redis connection settings.
type RedisConfig struct {
	Addr            string
	Host            string
	Port            int
	Password        string
	DB              int
	DialTimeout     time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	PoolSize        int
	MinIdleConns    int
	PoolTimeout     time.Duration
	ConnMaxIdleTime time.Duration
	ConnMaxLifetime time.Duration
}

// RabbitMQConfig contains RabbitMQ connection settings.
type RabbitMQConfig struct {
	URL         string
	DialTimeout time.Duration
}

// ObservabilityConfig contains metrics/tracing runtime settings.
type ObservabilityConfig struct {
	ServiceName      string
	TracingEnabled  bool
	JaegerEndpoint  string
	TraceSampleRatio float64
}

// Load reads configuration from environment variables.
func Load() Config {
	return Config{
		App: AppConfig{
			Environment: getenv("APP_ENV", "production"),
			Version:     getenv("APP_VERSION", "0.1.0"),
			Port:        getenv("APP_PORT", "8080"),
		},
		Redis: RedisConfig{
			Addr:            getenv("REDIS_ADDR", ""),
			Host:            getenv("REDIS_HOST", "localhost"),
			Port:            getenvInt("REDIS_PORT", 6379),
			Password:        os.Getenv("REDIS_PASSWORD"),
			DB:              getenvInt("REDIS_DB", 0),
			DialTimeout:     getenvDuration("REDIS_DIAL_TIMEOUT", 5*time.Second),
			ReadTimeout:     getenvDuration("REDIS_READ_TIMEOUT", 3*time.Second),
			WriteTimeout:    getenvDuration("REDIS_WRITE_TIMEOUT", 3*time.Second),
			PoolSize:        getenvInt("REDIS_POOL_SIZE", 10),
			MinIdleConns:    getenvInt("REDIS_MIN_IDLE_CONNS", 5),
			PoolTimeout:     getenvDuration("REDIS_POOL_TIMEOUT", 4*time.Second),
			ConnMaxIdleTime: getenvDuration("REDIS_CONN_MAX_IDLE_TIME", 5*time.Minute),
			ConnMaxLifetime: getenvDuration("REDIS_CONN_MAX_LIFETIME", 30*time.Minute),
		},
		RabbitMQ: RabbitMQConfig{
			URL:         getenv("RABBITMQ_URL", ""),
			DialTimeout: getenvDuration("RABBITMQ_DIAL_TIMEOUT", 3*time.Second),
		},
		Database: DatabaseConfig{
			Host:            getenv("DB_HOST", "localhost"),
			ExternalHost:    getenv("DB_HOST_EXTERNAL", ""),
			Port:            getenvInt("DB_PORT", 5432),
			ExternalPort:    getenvInt("DB_PORT_EXTERNAL", 0),
			User:            getenv("DB_USER", "postgres"),
			Password:        os.Getenv("DB_PASS"),
			Name:            getenv("DB_NAME", "postgres"),
			SSLMode:         getenv("DB_SSLMODE", "disable"),
			MaxOpenConns:    getenvInt("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    getenvInt("DB_MAX_IDLE_CONNS", 25),
			ConnMaxIdleTime: getenvDuration("DB_CONN_MAX_IDLE_TIME", 5*time.Minute),
			ConnMaxLifetime: getenvDuration("DB_CONN_MAX_LIFETIME", 30*time.Minute),
		},
		Observability: ObservabilityConfig{
			ServiceName:       getenv("OTEL_SERVICE_NAME", "dante-api-systems"),
			TracingEnabled:   getenvBool("TRACING_ENABLED", true),
			JaegerEndpoint:   getenv("JAEGER_OTLP_ENDPOINT", "jaeger:4318"),
			TraceSampleRatio: getenvFloat("TRACE_SAMPLE_RATIO", 1.0),
		},
	}
}

// IsDevelopment reports whether the configured application environment is a local/dev mode.
func (c AppConfig) IsDevelopment() bool {
	switch strings.ToLower(strings.TrimSpace(c.Environment)) {
	case "development", "dev", "local":
		return true
	default:
		return false
	}
}

// DSN returns a PostgreSQL connection string for pgx and goose.
func (c DatabaseConfig) DSN() string {
	return c.dsn(c.Host, c.Port)
}

// ExternalDSN returns a PostgreSQL connection string suitable for host-side tools.
func (c DatabaseConfig) ExternalDSN() string {
	host := c.ExternalHost
	if host == "" {
		host = c.Host
	}

	port := c.ExternalPort
	if port == 0 {
		port = c.Port
	}

	return c.dsn(host, port)
}

func (c DatabaseConfig) dsn(host string, port int) string {
	values := url.Values{}
	values.Set("sslmode", c.SSLMode)

	dsn := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.User, c.Password),
		Host:     fmt.Sprintf("%s:%d", host, port),
		Path:     c.Name,
		RawQuery: values.Encode(),
	}

	return dsn.String()
}

// Address returns the Redis network address used by clients.
func (c RedisConfig) Address() string {
	if strings.TrimSpace(c.Addr) != "" {
		return strings.TrimSpace(c.Addr)
	}

	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

func getenv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getenvInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}

	return value
}

func getenvBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}

	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}

	return value
}

func getenvFloat(key string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}

	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}

	return value
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}

	return value
}
