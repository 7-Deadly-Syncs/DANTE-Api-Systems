package cache

import (
	"time"

	"github.com/google/uuid"
)

const (
	merchantCachePrefix          = "merchant:"
	merchantLockPrefix           = "lock:merchant:"
	transactionStatusCachePrefix = "transaction_status:"
	accountBalanceCachePrefix    = "account_balance:"
	idempotencyCachePrefix       = "idempotency:"
)

const (
	MerchantTTL          = 24 * time.Hour
	MerchantNegativeTTL  = 10 * time.Second
	MerchantLockTTL      = 3 * time.Second
	TransactionStatusTTL = 30 * time.Second

	// Planned policy values for future flows.
	AccountBalanceTTL = 15 * time.Second
	IdempotencyTTL    = 24 * time.Hour
)

// MerchantKey returns the Redis key for cached merchant payloads.
func MerchantKey(merchantID uuid.UUID) string {
	return merchantCachePrefix + merchantID.String()
}

// MerchantLockKey returns the Redis key for merchant cache fill locks.
func MerchantLockKey(merchantID uuid.UUID) string {
	return merchantLockPrefix + merchantID.String()
}

// TransactionStatusKey returns the Redis key for cached transaction status payloads.
func TransactionStatusKey(transactionID uuid.UUID) string {
	return transactionStatusCachePrefix + transactionID.String()
}

// AccountBalanceKey returns the Redis key for cached account balance payloads.
func AccountBalanceKey(accountID uuid.UUID) string {
	return accountBalanceCachePrefix + accountID.String()
}

// IdempotencyKey returns the Redis key for future idempotency state caching.
func IdempotencyKey(key string) string {
	return idempotencyCachePrefix + key
}
