package account

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	authservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/auth"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// ErrAccountAccessDenied reports that the requested local account does not belong to the authenticated session.
var ErrAccountAccessDenied = errors.New("account access denied")

// Querier describes the database operations needed by account read services.
type Querier interface {
	GetAccountByID(ctx context.Context, id uuid.UUID) (dbsqlc.Account, error)
	UpdateAccountBalance(ctx context.Context, arg dbsqlc.UpdateAccountBalanceParams) (dbsqlc.Account, error)
}

// BalanceReader describes the legacy balance operation.
type BalanceReader interface {
	GetBalance(ctx context.Context, accountID, pin string) (*legacy.BalanceSnapshot, error)
}

// BalanceCache describes the Redis-backed balance cache used by account reads.
type BalanceCache interface {
	GetAccountBalance(ctx context.Context, accountID uuid.UUID) (*cache.AccountBalanceCacheEntry, error)
	SetAccountBalance(ctx context.Context, entry cache.AccountBalanceCacheEntry, ttl time.Duration) error
}

// ProfileView is the API-facing account profile view.
type ProfileView struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	AccountNumber string
	CustomerID    string
	CustomerName  string
	CreatedAt     time.Time
}

// BalanceView is the API-facing account balance snapshot.
type BalanceView struct {
	AccountID          uuid.UUID
	ReferenceAccountID string
	Balance            int64
	Source             string
	FetchedAt          time.Time
}

// Service coordinates authenticated account reads.
type Service struct {
	repo    Querier
	cache   BalanceCache
	legacy  BalanceReader
	nowFunc func() time.Time
}

// NewService constructs an account read service.
func NewService(repo Querier, cacheClient BalanceCache, legacyClient BalanceReader) *Service {
	return &Service{
		repo:    repo,
		cache:   cacheClient,
		legacy:  legacyClient,
		nowFunc: time.Now,
	}
}

// GetProfile returns the authenticated account profile for the requested local account.
func (s *Service) GetProfile(ctx context.Context, localAccountID uuid.UUID, session authservice.SessionView) (*ProfileView, error) {
	account, err := s.repo.GetAccountByID(ctx, localAccountID)
	if err != nil {
		return nil, err
	}
	if account.AccountNumber != session.AccountNumber {
		return nil, ErrAccountAccessDenied
	}

	return &ProfileView{
		ID:            account.ID,
		UserID:        account.UserID,
		AccountNumber: account.AccountNumber,
		CustomerID:    session.CustomerID,
		CustomerName:  session.CustomerName,
		CreatedAt:     account.CreatedAt,
	}, nil
}

// GetBalance returns a cached or legacy-refreshed balance snapshot for the authenticated account.
func (s *Service) GetBalance(ctx context.Context, localAccountID uuid.UUID, session authservice.SessionView, transactionPIN string) (*BalanceView, error) {
	account, err := s.repo.GetAccountByID(ctx, localAccountID)
	if err != nil {
		return nil, err
	}
	if account.AccountNumber != session.AccountNumber {
		return nil, ErrAccountAccessDenied
	}

	if entry, err := s.cache.GetAccountBalance(ctx, localAccountID); err == nil {
		return &BalanceView{
			AccountID:          localAccountID,
			ReferenceAccountID: entry.ReferenceAccountID,
			Balance:            entry.Balance,
			Source:             "cache",
			FetchedAt:          entry.FetchedAt.UTC(),
		}, nil
	} else if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("read account balance from cache: %w", err)
	}

	snapshot, err := s.legacy.GetBalance(ctx, session.AccountNumber, transactionPIN)
	if err != nil {
		return nil, fmt.Errorf("load account balance from legacy: %w", err)
	}

	fetchedAt := s.nowFunc().UTC()
	if _, err := s.repo.UpdateAccountBalance(ctx, dbsqlc.UpdateAccountBalanceParams{
		ID:      localAccountID,
		Balance: snapshot.Balance,
	}); err != nil {
		return nil, fmt.Errorf("update local account balance snapshot: %w", err)
	}

	if err := s.cache.SetAccountBalance(ctx, cache.AccountBalanceCacheEntry{
		AccountID:          localAccountID.String(),
		ReferenceAccountID: snapshot.ReferenceAccountID,
		Balance:            snapshot.Balance,
		FetchedAt:          fetchedAt,
	}, cache.AccountBalanceTTL); err != nil {
		return nil, fmt.Errorf("cache account balance snapshot: %w", err)
	}

	return &BalanceView{
		AccountID:          localAccountID,
		ReferenceAccountID: snapshot.ReferenceAccountID,
		Balance:            snapshot.Balance,
		Source:             "legacy",
		FetchedAt:          fetchedAt,
	}, nil
}
