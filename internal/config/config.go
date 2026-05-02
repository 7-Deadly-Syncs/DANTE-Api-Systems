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
	Database DatabaseConfig
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

// Load reads configuration from environment variables.
func Load() Config {
	return Config{
		App: AppConfig{
			Environment: getenv("APP_ENV", "production"),
			Version:     getenv("APP_VERSION", "0.1.0"),
			Port:        getenv("APP_PORT", "8080"),
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
