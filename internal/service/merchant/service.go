package merchant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/cachemetrics"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
)

const merchantLockWaitTimeout = 750 * time.Millisecond
const merchantLockPollInterval = 50 * time.Millisecond

// Querier describes the database access needed by the merchant service.
type Querier interface {
	GetMerchantByID(ctx context.Context, id uuid.UUID) (dbsqlc.Merchant, error)
}

// Service provides the cache -> DB -> legacy merchant lookup flow.
type Service struct {
	cache  *cache.Client
	repo   Querier
	legacy legacy.MerchantReader
	stats  cachemetrics.Recorder
}

// NewService constructs a merchant service.
func NewService(cacheClient *cache.Client, repo Querier, legacyClient legacy.MerchantReader, stats cachemetrics.Recorder) *Service {
	return &Service{
		cache:  cacheClient,
		repo:   repo,
		legacy: legacyClient,
		stats:  stats,
	}
}

// GetMerchant resolves a merchant by ID using Redis, PostgreSQL, then the legacy adapter.
func (s *Service) GetMerchant(ctx context.Context, merchantID uuid.UUID) (*dbsqlc.Merchant, error) {
	ctx, span := tracing.StartInternalSpan(ctx, "service.merchant", "merchant.lookup",
		attribute.String("merchant.id", merchantID.String()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, sql.ErrNoRows, legacy.ErrLookupInProgress)
	}()

	if merchant, err := s.cache.GetMerchant(ctx, merchantID); err == nil {
		s.stats.Inc(cachemetrics.MerchantCacheHits)
		span.SetAttributes(attribute.String("merchant.lookup_source", "redis"))
		span.AddEvent("merchant cache hit")
		return merchant, nil
	} else if errors.Is(err, sql.ErrNoRows) {
		s.stats.Inc(cachemetrics.MerchantNegativeCacheHits)
		span.SetAttributes(attribute.String("merchant.lookup_source", "redis_negative_cache"))
		span.AddEvent("merchant negative cache hit")
		spanErr = err
		return nil, err
	} else if !errors.Is(err, redis.Nil) {
		spanErr = err
		return nil, fmt.Errorf("read merchant from cache: %w", err)
	}
	s.stats.Inc(cachemetrics.MerchantCacheMisses)
	span.AddEvent("merchant cache miss")

	dbCtx, dbSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.get merchant",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "SELECT"),
		attribute.String("db.sql.table", "merchants"),
		attribute.String("merchant.id", merchantID.String()),
	)
	merchant, err := s.repo.GetMerchantByID(dbCtx, merchantID)
	tracing.EndSpan(dbSpan, err, sql.ErrNoRows)
	if err == nil {
		span.SetAttributes(attribute.String("merchant.lookup_source", "postgres"))
		span.AddEvent("merchant database hit")
		s.cacheMerchantAsync(ctx, merchant)
		return &merchant, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		spanErr = err
		return nil, fmt.Errorf("read merchant from database: %w", err)
	}
	span.AddEvent("merchant database miss")

	lockToken, acquired, err := s.cache.AcquireMerchantLock(ctx, merchantID, cache.MerchantLockTTL)
	if err != nil {
		spanErr = err
		return nil, fmt.Errorf("acquire merchant cache lock: %w", err)
	}
	if !acquired {
		s.stats.Inc(cachemetrics.MerchantLockContention)
		span.SetAttributes(attribute.Bool("merchant.lock_acquired", false))
		span.AddEvent("merchant lock contention")
		merchant, err := s.waitForMerchantFill(ctx, merchantID)
		if err == nil {
			span.SetAttributes(attribute.String("merchant.lookup_source", "redis_wait"))
			return merchant, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			spanErr = err
			return nil, err
		}
		spanErr = legacy.ErrLookupInProgress
		return nil, legacy.ErrLookupInProgress
	}
	s.stats.Inc(cachemetrics.MerchantLockAcquired)
	span.SetAttributes(attribute.Bool("merchant.lock_acquired", true))
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.cache.ReleaseMerchantLock(releaseCtx, merchantID, lockToken)
	}()

	legacyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	legacyCtx, legacySpan := tracing.StartClientSpan(legacyCtx, "legacy", "legacy.get merchant",
		attribute.String("legacy.system", "banking"),
		attribute.String("merchant.id", merchantID.String()),
	)
	merchantFromLegacy, err := s.legacy.GetMerchant(legacyCtx, merchantID)
	tracing.EndSpan(legacySpan, err, legacy.ErrNotFound)
	if err != nil {
		if errors.Is(err, legacy.ErrNotFound) {
			s.cacheMerchantNotFoundAsync(ctx, merchantID)
			span.SetAttributes(attribute.String("merchant.lookup_source", "legacy_not_found"))
			spanErr = sql.ErrNoRows
			return nil, sql.ErrNoRows
		}
		spanErr = err
		return nil, fmt.Errorf("read merchant from legacy: %w", err)
	}

	span.SetAttributes(attribute.String("merchant.lookup_source", "legacy"))
	s.cacheMerchantAsync(ctx, *merchantFromLegacy)
	return merchantFromLegacy, nil
}

func (s *Service) cacheMerchantAsync(parentCtx context.Context, merchant dbsqlc.Merchant) {
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), 2*time.Second)
		defer cancel()
		_ = s.cache.SetMerchant(ctx, merchant, cache.MerchantTTL)
	}()
}

func (s *Service) cacheMerchantNotFoundAsync(parentCtx context.Context, merchantID uuid.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), 2*time.Second)
		defer cancel()
		_ = s.cache.SetMerchantNotFound(ctx, merchantID, cache.MerchantNegativeTTL)
	}()
}

func (s *Service) waitForMerchantFill(ctx context.Context, merchantID uuid.UUID) (*dbsqlc.Merchant, error) {
	ctx, span := tracing.StartInternalSpan(ctx, "service.merchant", "merchant.wait_for_cache_fill",
		attribute.String("merchant.id", merchantID.String()),
		attribute.Int64("merchant.lock_wait_timeout_ms", merchantLockWaitTimeout.Milliseconds()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, sql.ErrNoRows, context.DeadlineExceeded)
	}()

	waitCtx, cancel := context.WithTimeout(ctx, merchantLockWaitTimeout)
	defer cancel()

	ticker := time.NewTicker(merchantLockPollInterval)
	defer ticker.Stop()

	for {
		merchant, err := s.cache.GetMerchant(waitCtx, merchantID)
		if err == nil {
			span.SetAttributes(attribute.String("merchant.wait_result", "filled"))
			return merchant, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			span.SetAttributes(attribute.String("merchant.wait_result", "negative_cache"))
			spanErr = err
			return nil, err
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			spanErr = err
			return nil, fmt.Errorf("wait for merchant cache fill: %w", err)
		}

		select {
		case <-waitCtx.Done():
			span.SetAttributes(attribute.String("merchant.wait_result", "timeout"))
			spanErr = waitCtx.Err()
			return nil, waitCtx.Err()
		case <-ticker.C:
		}
	}
}
