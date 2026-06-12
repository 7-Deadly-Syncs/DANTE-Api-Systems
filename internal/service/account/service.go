package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	authservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/auth"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
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
	ctx, span := tracing.StartInternalSpan(ctx, "service.account", "account.profile.lookup",
		attribute.String("account.id", localAccountID.String()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, sql.ErrNoRows, ErrAccountAccessDenied)
	}()

	dbCtx, dbSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.get account",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "SELECT"),
		attribute.String("db.sql.table", "accounts"),
		attribute.String("account.id", localAccountID.String()),
	)
	account, err := s.repo.GetAccountByID(dbCtx, localAccountID)
	tracing.EndSpan(dbSpan, err, sql.ErrNoRows)
	if err != nil {
		spanErr = err
		return nil, err
	}
	if account.AccountNumber != session.AccountNumber {
		span.SetAttributes(attribute.String("account.result", "access_denied"))
		spanErr = ErrAccountAccessDenied
		return nil, ErrAccountAccessDenied
	}

	span.SetAttributes(attribute.String("account.result", "profile_loaded"))
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
	ctx, span := tracing.StartInternalSpan(ctx, "service.account", "account.balance.lookup",
		attribute.String("account.id", localAccountID.String()),
		attribute.Bool("account.transaction_pin_present", transactionPIN != ""),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, sql.ErrNoRows, ErrAccountAccessDenied)
	}()

	dbCtx, dbSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.get account",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "SELECT"),
		attribute.String("db.sql.table", "accounts"),
		attribute.String("account.id", localAccountID.String()),
	)
	account, err := s.repo.GetAccountByID(dbCtx, localAccountID)
	tracing.EndSpan(dbSpan, err, sql.ErrNoRows)
	if err != nil {
		spanErr = err
		return nil, err
	}
	if account.AccountNumber != session.AccountNumber {
		span.SetAttributes(attribute.String("account.balance_source", "access_denied"))
		spanErr = ErrAccountAccessDenied
		return nil, ErrAccountAccessDenied
	}

	if entry, err := s.cache.GetAccountBalance(ctx, localAccountID); err == nil {
		span.SetAttributes(attribute.String("account.balance_source", "redis"))
		return &BalanceView{
			AccountID:          localAccountID,
			ReferenceAccountID: entry.ReferenceAccountID,
			Balance:            entry.Balance,
			Source:             "cache",
			FetchedAt:          entry.FetchedAt.UTC(),
		}, nil
	} else if err != nil && !errors.Is(err, redis.Nil) {
		spanErr = err
		return nil, fmt.Errorf("read account balance from cache: %w", err)
	}
	span.AddEvent("account balance cache miss")

	legacyCtx, legacySpan := tracing.StartClientSpan(ctx, "legacy", "legacy.get balance",
		attribute.String("legacy.system", "banking"),
		attribute.String("legacy.operation", "balance"),
	)
	snapshot, err := s.legacy.GetBalance(legacyCtx, session.AccountID, transactionPIN)
	tracing.EndSpan(legacySpan, err)
	if err != nil {
		spanErr = err
		return nil, fmt.Errorf("load account balance from legacy: %w", err)
	}

	fetchedAt := s.nowFunc().UTC()
	updateCtx, updateSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.update account_balance",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "UPDATE"),
		attribute.String("db.sql.table", "accounts"),
		attribute.String("account.id", localAccountID.String()),
	)
	if _, err := s.repo.UpdateAccountBalance(updateCtx, dbsqlc.UpdateAccountBalanceParams{
		ID:      localAccountID,
		Balance: snapshot.Balance,
	}); err != nil {
		tracing.EndSpan(updateSpan, err)
		spanErr = err
		return nil, fmt.Errorf("update local account balance snapshot: %w", err)
	}
	tracing.EndSpan(updateSpan, nil)

	if err := s.cache.SetAccountBalance(ctx, cache.AccountBalanceCacheEntry{
		AccountID:          localAccountID.String(),
		ReferenceAccountID: snapshot.ReferenceAccountID,
		Balance:            snapshot.Balance,
		FetchedAt:          fetchedAt,
	}, cache.AccountBalanceTTL); err != nil {
		spanErr = err
		return nil, fmt.Errorf("cache account balance snapshot: %w", err)
	}

	span.SetAttributes(attribute.String("account.balance_source", "legacy"))
	return &BalanceView{
		AccountID:          localAccountID,
		ReferenceAccountID: snapshot.ReferenceAccountID,
		Balance:            snapshot.Balance,
		Source:             "legacy",
		FetchedAt:          fetchedAt,
	}, nil
}
