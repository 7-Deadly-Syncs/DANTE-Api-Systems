package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
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

// AccountBalanceCacheEntry is the cached shape for account balance snapshots.
type AccountBalanceCacheEntry struct {
	AccountID          string    `json:"account_id"`
	ReferenceAccountID string    `json:"reference_account_id"`
	Balance            int64     `json:"balance"`
	FetchedAt          time.Time `json:"fetched_at"`
}

// SessionEntry is the cached shape for DANTE-issued client sessions.
type SessionEntry struct {
	Token               string    `json:"token"`
	CustomerID          string    `json:"customer_id"`
	AccountID           string    `json:"account_id"`
	AccountNumber       string    `json:"account_number"`
	CustomerName        string    `json:"customer_name"`
	LegacySessionID     string    `json:"legacy_session_id"`
	LegacySessionExpiry time.Time `json:"legacy_session_expiry"`
	CreatedAt           time.Time `json:"created_at"`
}

// Open creates a Redis client and verifies connectivity with a ping.
func Open(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	_, span := tracing.StartClientSpan(ctx, "redis", "redis.connect",
		attribute.String("db.system", "redis"),
		attribute.String("server.address", cfg.Address()),
		attribute.Int("db.redis.database_index", cfg.DB),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

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
		spanErr = fmt.Errorf("ping redis: %w", err)
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
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.get merchant",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "GET"),
		attribute.String("cache.key_type", "merchant"),
		attribute.String("merchant.id", merchantID.String()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, redis.Nil, sql.ErrNoRows)
	}()

	payload, err := c.Redis.Get(ctx, MerchantKey(merchantID)).Bytes()
	if err != nil {
		spanErr = err
		return nil, err
	}

	var entry merchantCacheEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		spanErr = err
		return nil, fmt.Errorf("unmarshal merchant cache payload: %w", err)
	}

	if entry.NotFound {
		span.SetAttributes(attribute.String("cache.result", "negative_hit"))
		spanErr = sql.ErrNoRows
		return nil, sql.ErrNoRows
	}
	if entry.Merchant == nil {
		spanErr = fmt.Errorf("merchant cache payload missing merchant value")
		return nil, fmt.Errorf("merchant cache payload missing merchant value")
	}

	span.SetAttributes(attribute.String("cache.result", "hit"))
	return entry.Merchant, nil
}

// SetMerchant caches a merchant for the provided TTL.
func (c *Client) SetMerchant(ctx context.Context, merchant dbsqlc.Merchant, ttl time.Duration) error {
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.set merchant",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "SET"),
		attribute.String("cache.key_type", "merchant"),
		attribute.String("merchant.id", merchant.ID.String()),
		attribute.Int64("cache.ttl_ms", ttl.Milliseconds()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	payload, err := json.Marshal(merchantCacheEntry{
		Merchant: &merchant,
	})
	if err != nil {
		spanErr = err
		return fmt.Errorf("marshal merchant cache payload: %w", err)
	}

	if err := c.Redis.Set(ctx, MerchantKey(merchant.ID), payload, ttl).Err(); err != nil {
		spanErr = err
		return fmt.Errorf("set merchant cache payload: %w", err)
	}

	return nil
}

// SetMerchantNotFound writes a short-lived negative cache entry for merchant lookups.
func (c *Client) SetMerchantNotFound(ctx context.Context, merchantID uuid.UUID, ttl time.Duration) error {
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.set merchant_not_found",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "SET"),
		attribute.String("cache.key_type", "merchant_negative"),
		attribute.String("merchant.id", merchantID.String()),
		attribute.Int64("cache.ttl_ms", ttl.Milliseconds()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	payload, err := json.Marshal(merchantCacheEntry{NotFound: true})
	if err != nil {
		spanErr = err
		return fmt.Errorf("marshal merchant negative cache payload: %w", err)
	}

	if err := c.Redis.Set(ctx, MerchantKey(merchantID), payload, ttl).Err(); err != nil {
		spanErr = err
		return fmt.Errorf("set merchant negative cache payload: %w", err)
	}

	return nil
}

// DeleteMerchant removes a cached merchant or negative-cache entry.
func (c *Client) DeleteMerchant(ctx context.Context, merchantID uuid.UUID) error {
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.del merchant",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "DEL"),
		attribute.String("cache.key_type", "merchant"),
		attribute.String("merchant.id", merchantID.String()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	if err := c.Redis.Del(ctx, MerchantKey(merchantID)).Err(); err != nil {
		spanErr = err
		return fmt.Errorf("delete merchant cache payload: %w", err)
	}

	return nil
}

// GetTransactionStatus fetches cached transaction status data by transaction ID.
func (c *Client) GetTransactionStatus(ctx context.Context, transactionID uuid.UUID) (*TransactionStatusCacheEntry, error) {
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.get transaction_status",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "GET"),
		attribute.String("cache.key_type", "transaction_status"),
		attribute.String("transaction.id", transactionID.String()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, redis.Nil)
	}()

	payload, err := c.Redis.Get(ctx, TransactionStatusKey(transactionID)).Bytes()
	if err != nil {
		spanErr = err
		return nil, err
	}

	var entry TransactionStatusCacheEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		spanErr = err
		return nil, fmt.Errorf("unmarshal transaction status cache payload: %w", err)
	}

	span.SetAttributes(
		attribute.String("cache.result", "hit"),
		attribute.String("transaction.status", entry.Status),
	)
	return &entry, nil
}

// SetTransactionStatus caches transaction status data for the provided TTL.
func (c *Client) SetTransactionStatus(ctx context.Context, entry TransactionStatusCacheEntry, ttl time.Duration) error {
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.set transaction_status",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "SET"),
		attribute.String("cache.key_type", "transaction_status"),
		attribute.String("transaction.id", entry.ID),
		attribute.String("transaction.status", entry.Status),
		attribute.Int64("cache.ttl_ms", ttl.Milliseconds()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	payload, err := json.Marshal(entry)
	if err != nil {
		spanErr = err
		return fmt.Errorf("marshal transaction status cache payload: %w", err)
	}

	if err := c.Redis.Set(ctx, TransactionStatusKey(uuid.MustParse(entry.ID)), payload, ttl).Err(); err != nil {
		spanErr = err
		return fmt.Errorf("set transaction status cache payload: %w", err)
	}

	return nil
}

// DeleteTransactionStatus removes a cached transaction status entry.
func (c *Client) DeleteTransactionStatus(ctx context.Context, transactionID uuid.UUID) error {
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.del transaction_status",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "DEL"),
		attribute.String("cache.key_type", "transaction_status"),
		attribute.String("transaction.id", transactionID.String()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	if err := c.Redis.Del(ctx, TransactionStatusKey(transactionID)).Err(); err != nil {
		spanErr = err
		return fmt.Errorf("delete transaction status cache payload: %w", err)
	}

	return nil
}

// GetAccountBalance fetches cached account balance data by account ID.
func (c *Client) GetAccountBalance(ctx context.Context, accountID uuid.UUID) (*AccountBalanceCacheEntry, error) {
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.get account_balance",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "GET"),
		attribute.String("cache.key_type", "account_balance"),
		attribute.String("account.id", accountID.String()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, redis.Nil)
	}()

	payload, err := c.Redis.Get(ctx, AccountBalanceKey(accountID)).Bytes()
	if err != nil {
		spanErr = err
		return nil, err
	}

	var entry AccountBalanceCacheEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		spanErr = err
		return nil, fmt.Errorf("unmarshal account balance cache payload: %w", err)
	}

	span.SetAttributes(attribute.String("cache.result", "hit"))
	return &entry, nil
}

// SetAccountBalance caches an account balance snapshot for the provided TTL.
func (c *Client) SetAccountBalance(ctx context.Context, entry AccountBalanceCacheEntry, ttl time.Duration) error {
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.set account_balance",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "SET"),
		attribute.String("cache.key_type", "account_balance"),
		attribute.String("account.id", entry.AccountID),
		attribute.Int64("cache.ttl_ms", ttl.Milliseconds()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	payload, err := json.Marshal(entry)
	if err != nil {
		spanErr = err
		return fmt.Errorf("marshal account balance cache payload: %w", err)
	}

	if err := c.Redis.Set(ctx, AccountBalanceKey(uuid.MustParse(entry.AccountID)), payload, ttl).Err(); err != nil {
		spanErr = err
		return fmt.Errorf("set account balance cache payload: %w", err)
	}

	return nil
}

// DeleteAccountBalance removes a cached account balance snapshot.
func (c *Client) DeleteAccountBalance(ctx context.Context, accountID uuid.UUID) error {
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.del account_balance",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "DEL"),
		attribute.String("cache.key_type", "account_balance"),
		attribute.String("account.id", accountID.String()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	if err := c.Redis.Del(ctx, AccountBalanceKey(accountID)).Err(); err != nil {
		spanErr = err
		return fmt.Errorf("delete account balance cache payload: %w", err)
	}

	return nil
}

// GetSession fetches a DANTE-issued session by token.
func (c *Client) GetSession(ctx context.Context, token string) (*SessionEntry, error) {
	payload, err := c.Redis.Get(ctx, SessionKey(token)).Bytes()
	if err != nil {
		return nil, err
	}

	var entry SessionEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		return nil, fmt.Errorf("unmarshal session cache payload: %w", err)
	}

	return &entry, nil
}

// SetSession stores a DANTE-issued session for the provided TTL.
func (c *Client) SetSession(ctx context.Context, entry SessionEntry, ttl time.Duration) error {
	payload, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal session cache payload: %w", err)
	}

	if err := c.Redis.Set(ctx, SessionKey(entry.Token), payload, ttl).Err(); err != nil {
		return fmt.Errorf("set session cache payload: %w", err)
	}

	return nil
}

// DeleteSession removes a DANTE-issued session.
func (c *Client) DeleteSession(ctx context.Context, token string) error {
	if err := c.Redis.Del(ctx, SessionKey(token)).Err(); err != nil {
		return fmt.Errorf("delete session cache payload: %w", err)
	}

	return nil
}

// AcquireMerchantLock attempts to lock a merchant cache fill so only one request performs fallback work.
func (c *Client) AcquireMerchantLock(ctx context.Context, merchantID uuid.UUID, ttl time.Duration) (string, bool, error) {
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.setnx merchant_lock",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "SETNX"),
		attribute.String("cache.key_type", "merchant_lock"),
		attribute.String("merchant.id", merchantID.String()),
		attribute.Int64("cache.ttl_ms", ttl.Milliseconds()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	token := uuid.NewString()
	acquired, err := c.Redis.SetNX(ctx, MerchantLockKey(merchantID), token, ttl).Result()
	if err != nil {
		spanErr = err
		return "", false, fmt.Errorf("acquire merchant lock: %w", err)
	}
	span.SetAttributes(attribute.Bool("cache.lock_acquired", acquired))
	if !acquired {
		return "", false, nil
	}
	return token, true, nil
}

// ReleaseMerchantLock releases a merchant cache fill lock if the token still matches.
func (c *Client) ReleaseMerchantLock(ctx context.Context, merchantID uuid.UUID, token string) error {
	ctx, span := tracing.StartClientSpan(ctx, "redis", "redis.eval release_merchant_lock",
		attribute.String("db.system", "redis"),
		attribute.String("db.operation", "EVAL"),
		attribute.String("cache.key_type", "merchant_lock"),
		attribute.String("merchant.id", merchantID.String()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	result, err := c.Redis.Eval(
		ctx,
		`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`,
		[]string{MerchantLockKey(merchantID)},
		token,
	).Result()
	if err != nil {
		spanErr = err
		return fmt.Errorf("release merchant lock: %w", err)
	}

	span.SetAttributes(attribute.String("redis.eval_result", fmt.Sprint(result)))
	_ = result
	return nil
}
