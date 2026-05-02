package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Client wraps the shared Redis client used by the application.
type Client struct {
	Redis *redis.Client
}

type merchantCacheEntry struct {
	NotFound bool             `json:"not_found"`
	Merchant *dbsqlc.Merchant `json:"merchant,omitempty"`
}

// TransactionStatusCacheEntry is the cached shape for transaction status responses.
type TransactionStatusCacheEntry struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	RequestedAt time.Time  `json:"requested_at"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Open creates a Redis client and verifies connectivity with a ping.
func Open(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:            cfg.Address(),
		Password:        cfg.Password,
		DB:              cfg.DB,
		DialTimeout:     cfg.DialTimeout,
		ReadTimeout:     cfg.ReadTimeout,
		WriteTimeout:    cfg.WriteTimeout,
		PoolSize:        cfg.PoolSize,
		MinIdleConns:    cfg.MinIdleConns,
		PoolTimeout:     cfg.PoolTimeout,
		ConnMaxIdleTime: cfg.ConnMaxIdleTime,
		ConnMaxLifetime: cfg.ConnMaxLifetime,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}

// NewClient constructs the cache dependency wrapper used by higher-level services.
func NewClient(client *redis.Client) *Client {
	return &Client{Redis: client}
}

// GetMerchant fetches a cached merchant by its ID.
func (c *Client) GetMerchant(ctx context.Context, merchantID uuid.UUID) (*dbsqlc.Merchant, error) {
	payload, err := c.Redis.Get(ctx, MerchantKey(merchantID)).Bytes()
	if err != nil {
		return nil, err
	}

	var entry merchantCacheEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		return nil, fmt.Errorf("unmarshal merchant cache payload: %w", err)
	}

	if entry.NotFound {
		return nil, sql.ErrNoRows
	}
	if entry.Merchant == nil {
		return nil, fmt.Errorf("merchant cache payload missing merchant value")
	}

	return entry.Merchant, nil
}

// SetMerchant caches a merchant for the provided TTL.
func (c *Client) SetMerchant(ctx context.Context, merchant dbsqlc.Merchant, ttl time.Duration) error {
	payload, err := json.Marshal(merchantCacheEntry{
		Merchant: &merchant,
	})
	if err != nil {
		return fmt.Errorf("marshal merchant cache payload: %w", err)
	}

	if err := c.Redis.Set(ctx, MerchantKey(merchant.ID), payload, ttl).Err(); err != nil {
		return fmt.Errorf("set merchant cache payload: %w", err)
	}

	return nil
}

// SetMerchantNotFound writes a short-lived negative cache entry for merchant lookups.
func (c *Client) SetMerchantNotFound(ctx context.Context, merchantID uuid.UUID, ttl time.Duration) error {
	payload, err := json.Marshal(merchantCacheEntry{NotFound: true})
	if err != nil {
		return fmt.Errorf("marshal merchant negative cache payload: %w", err)
	}

	if err := c.Redis.Set(ctx, MerchantKey(merchantID), payload, ttl).Err(); err != nil {
		return fmt.Errorf("set merchant negative cache payload: %w", err)
	}

	return nil
}

// GetTransactionStatus fetches cached transaction status data by transaction ID.
func (c *Client) GetTransactionStatus(ctx context.Context, transactionID uuid.UUID) (*TransactionStatusCacheEntry, error) {
	payload, err := c.Redis.Get(ctx, TransactionStatusKey(transactionID)).Bytes()
	if err != nil {
		return nil, err
	}

	var entry TransactionStatusCacheEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		return nil, fmt.Errorf("unmarshal transaction status cache payload: %w", err)
	}

	return &entry, nil
}

// SetTransactionStatus caches transaction status data for the provided TTL.
func (c *Client) SetTransactionStatus(ctx context.Context, entry TransactionStatusCacheEntry, ttl time.Duration) error {
	payload, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal transaction status cache payload: %w", err)
	}

	if err := c.Redis.Set(ctx, TransactionStatusKey(uuid.MustParse(entry.ID)), payload, ttl).Err(); err != nil {
		return fmt.Errorf("set transaction status cache payload: %w", err)
	}

	return nil
}

// AcquireMerchantLock attempts to lock a merchant cache fill so only one request performs fallback work.
func (c *Client) AcquireMerchantLock(ctx context.Context, merchantID uuid.UUID, ttl time.Duration) (string, bool, error) {
	token := uuid.NewString()
	acquired, err := c.Redis.SetNX(ctx, MerchantLockKey(merchantID), token, ttl).Result()
	if err != nil {
		return "", false, fmt.Errorf("acquire merchant lock: %w", err)
	}
	if !acquired {
		return "", false, nil
	}
	return token, true, nil
}

// ReleaseMerchantLock releases a merchant cache fill lock if the token still matches.
func (c *Client) ReleaseMerchantLock(ctx context.Context, merchantID uuid.UUID, token string) error {
	result, err := c.Redis.Eval(
		ctx,
		`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`,
		[]string{MerchantLockKey(merchantID)},
		token,
	).Result()
	if err != nil {
		return fmt.Errorf("release merchant lock: %w", err)
	}

	_ = result
	return nil
}
