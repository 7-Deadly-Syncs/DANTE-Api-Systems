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
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
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
	if merchant, err := s.cache.GetMerchant(ctx, merchantID); err == nil {
		s.stats.Inc(cachemetrics.MerchantCacheHits)
		return merchant, nil
	} else if errors.Is(err, sql.ErrNoRows) {
		s.stats.Inc(cachemetrics.MerchantNegativeCacheHits)
		return nil, err
	} else if !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("read merchant from cache: %w", err)
	}
	s.stats.Inc(cachemetrics.MerchantCacheMisses)

	merchant, err := s.repo.GetMerchantByID(ctx, merchantID)
	if err == nil {
		s.cacheMerchantAsync(merchant)
		return &merchant, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read merchant from database: %w", err)
	}

	lockToken, acquired, err := s.cache.AcquireMerchantLock(ctx, merchantID, cache.MerchantLockTTL)
	if err != nil {
		return nil, fmt.Errorf("acquire merchant cache lock: %w", err)
	}
	if !acquired {
		s.stats.Inc(cachemetrics.MerchantLockContention)
		merchant, err := s.waitForMerchantFill(ctx, merchantID)
		if err == nil {
			return merchant, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, legacy.ErrLookupInProgress
	}
	s.stats.Inc(cachemetrics.MerchantLockAcquired)
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.cache.ReleaseMerchantLock(releaseCtx, merchantID, lockToken)
	}()

	legacyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	merchantFromLegacy, err := s.legacy.GetMerchant(legacyCtx, merchantID)
	if err != nil {
		if errors.Is(err, legacy.ErrNotFound) {
			s.cacheMerchantNotFoundAsync(merchantID)
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("read merchant from legacy: %w", err)
	}

	s.cacheMerchantAsync(*merchantFromLegacy)
	return merchantFromLegacy, nil
}

func (s *Service) cacheMerchantAsync(merchant dbsqlc.Merchant) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.cache.SetMerchant(ctx, merchant, cache.MerchantTTL)
	}()
}

func (s *Service) cacheMerchantNotFoundAsync(merchantID uuid.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.cache.SetMerchantNotFound(ctx, merchantID, cache.MerchantNegativeTTL)
	}()
}

func (s *Service) waitForMerchantFill(ctx context.Context, merchantID uuid.UUID) (*dbsqlc.Merchant, error) {
	waitCtx, cancel := context.WithTimeout(ctx, merchantLockWaitTimeout)
	defer cancel()

	ticker := time.NewTicker(merchantLockPollInterval)
	defer ticker.Stop()

	for {
		merchant, err := s.cache.GetMerchant(waitCtx, merchantID)
		if err == nil {
			return merchant, nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("wait for merchant cache fill: %w", err)
		}

		select {
		case <-waitCtx.Done():
			return nil, waitCtx.Err()
		case <-ticker.C:
		}
	}
}
