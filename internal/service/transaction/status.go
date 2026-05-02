package transaction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/cachemetrics"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// StatusQuerier describes the database access needed for transaction status reads.
type StatusQuerier interface {
	GetTransactionStatusByID(ctx context.Context, id uuid.UUID) (dbsqlc.GetTransactionStatusByIDRow, error)
}

// StatusView is the API-facing view of transaction status.
type StatusView struct {
	ID          uuid.UUID
	Status      string
	RequestedAt time.Time
	ProcessedAt *time.Time
	UpdatedAt   time.Time
}

// StatusService resolves transaction status via Redis then PostgreSQL.
type StatusService struct {
	cache *cache.Client
	repo  StatusQuerier
	stats cachemetrics.Recorder
}

// NewStatusService constructs the transaction status service.
func NewStatusService(cacheClient *cache.Client, repo StatusQuerier, stats cachemetrics.Recorder) *StatusService {
	return &StatusService{
		cache: cacheClient,
		repo:  repo,
		stats: stats,
	}
}

// GetStatus returns transaction status using Redis first, then PostgreSQL.
func (s *StatusService) GetStatus(ctx context.Context, transactionID uuid.UUID) (*StatusView, error) {
	if cached, err := s.cache.GetTransactionStatus(ctx, transactionID); err == nil {
		s.stats.Inc(cachemetrics.TransactionStatusCacheHits)
		return &StatusView{
			ID:          uuid.MustParse(cached.ID),
			Status:      cached.Status,
			RequestedAt: cached.RequestedAt,
			ProcessedAt: cached.ProcessedAt,
			UpdatedAt:   cached.UpdatedAt,
		}, nil
	} else if !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("read transaction status from cache: %w", err)
	}
	s.stats.Inc(cachemetrics.TransactionStatusCacheMisses)

	row, err := s.repo.GetTransactionStatusByID(ctx, transactionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("read transaction status from database: %w", err)
	}

	view := &StatusView{
		ID:          row.ID,
		Status:      row.Status,
		RequestedAt: row.RequestedAt,
		UpdatedAt:   row.UpdatedAt,
	}
	if row.ProcessedAt.Valid {
		processedAt := row.ProcessedAt.Time
		view.ProcessedAt = &processedAt
	}

	s.cacheStatusAsync(*view)
	return view, nil
}

func (s *StatusService) cacheStatusAsync(view StatusView) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		entry := cache.TransactionStatusCacheEntry{
			ID:          view.ID.String(),
			Status:      view.Status,
			RequestedAt: view.RequestedAt,
			ProcessedAt: view.ProcessedAt,
			UpdatedAt:   view.UpdatedAt,
		}
		_ = s.cache.SetTransactionStatus(ctx, entry, cache.TransactionStatusTTL)
	}()
}
