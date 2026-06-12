package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	authservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/auth"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type fakeRepo struct {
	account        dbsqlc.Account
	accountErr     error
	updatedArgs    dbsqlc.UpdateAccountBalanceParams
	updatedAccount dbsqlc.Account
	updateErr      error
}

func (f *fakeRepo) GetAccountByID(ctx context.Context, id uuid.UUID) (dbsqlc.Account, error) {
	return f.account, f.accountErr
}

func (f *fakeRepo) UpdateAccountBalance(ctx context.Context, arg dbsqlc.UpdateAccountBalanceParams) (dbsqlc.Account, error) {
	if f.updateErr != nil {
		return dbsqlc.Account{}, f.updateErr
	}
	f.updatedArgs = arg
	account := f.updatedAccount
	account.ID = arg.ID
	account.Balance = arg.Balance
	return account, nil
}

type fakeLegacy struct {
	snapshot  *legacy.BalanceSnapshot
	err       error
	accountID string
	pin       string
}

func (f *fakeLegacy) GetBalance(ctx context.Context, accountID, pin string) (*legacy.BalanceSnapshot, error) {
	f.accountID = accountID
	f.pin = pin
	if f.err != nil {
		return nil, f.err
	}
	return f.snapshot, nil
}

type fakeBalanceCache struct {
	entry   *cache.AccountBalanceCacheEntry
	getErr  error
	setErr  error
	setData cache.AccountBalanceCacheEntry
	setTTL  time.Duration
}

func (f *fakeBalanceCache) GetAccountBalance(ctx context.Context, accountID uuid.UUID) (*cache.AccountBalanceCacheEntry, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.entry, nil
}

func (f *fakeBalanceCache) SetAccountBalance(ctx context.Context, entry cache.AccountBalanceCacheEntry, ttl time.Duration) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.setData = entry
	f.setTTL = ttl
	return nil
}

func TestGetProfileReturnsAuthenticatedAccountProfile(t *testing.T) {
	t.Parallel()

	accountID := uuid.New()
	userID := uuid.New()
	svc := NewService(&fakeRepo{
		account: dbsqlc.Account{
			ID:            accountID,
			UserID:        userID,
			AccountNumber: "2623860486223779",
			CreatedAt:     time.Now().UTC(),
		},
	}, &fakeBalanceCache{}, &fakeLegacy{})

	profile, err := svc.GetProfile(context.Background(), accountID, authservice.SessionView{
		AccountNumber: "2623860486223779",
		CustomerID:    "CUST123",
		CustomerName:  "John Doe",
	})
	if err != nil {
		t.Fatalf("GetProfile returned error: %v", err)
	}
	if profile.CustomerID != "CUST123" || profile.CustomerName != "John Doe" {
		t.Fatalf("unexpected profile payload: %+v", profile)
	}
}

func TestGetBalanceReturnsCacheHit(t *testing.T) {
	t.Parallel()

	accountID := uuid.New()
	now := time.Now().UTC()
	svc := NewService(&fakeRepo{
		account: dbsqlc.Account{
			ID:            accountID,
			AccountNumber: "2623860486223779",
		},
	}, &fakeBalanceCache{
		entry: &cache.AccountBalanceCacheEntry{
			AccountID:          accountID.String(),
			ReferenceAccountID: "LEGACY-ACC-1",
			Balance:            50000,
			FetchedAt:          now,
		},
	}, &fakeLegacy{})

	balance, err := svc.GetBalance(context.Background(), accountID, authservice.SessionView{
		LegacyAccountID: "LEGACY-ACC-1",
		AccountNumber:   "2623860486223779",
	}, "123456")
	if err != nil {
		t.Fatalf("GetBalance returned error: %v", err)
	}
	if balance.Source != "cache" || balance.Balance != 50000 {
		t.Fatalf("unexpected cached balance: %+v", balance)
	}
}

func TestGetBalanceFallsBackToLegacyAndUpdatesSnapshot(t *testing.T) {
	t.Parallel()

	accountID := uuid.New()
	now := time.Now().UTC()
	cacheClient := &fakeBalanceCache{getErr: redis.Nil}
	svc := NewService(&fakeRepo{
		account: dbsqlc.Account{
			ID:            accountID,
			AccountNumber: "2623860486223779",
		},
		updatedAccount: dbsqlc.Account{ID: accountID},
	}, cacheClient, &fakeLegacy{
		snapshot: &legacy.BalanceSnapshot{
			AccountID:          "LEGACY-ACC-1",
			ReferenceAccountID: "REF-001",
			Balance:            75000,
		},
	})
	svc.nowFunc = func() time.Time { return now }

	balance, err := svc.GetBalance(context.Background(), accountID, authservice.SessionView{
		LegacyAccountID: "LEGACY-ACC-1",
		AccountNumber:   "2623860486223779",
	}, "123456")
	if err != nil {
		t.Fatalf("GetBalance returned error: %v", err)
	}
	if balance.Source != "legacy" || balance.Balance != 75000 {
		t.Fatalf("unexpected legacy balance: %+v", balance)
	}
	if svc.legacy.(*fakeLegacy).accountID != "2623860486223779" {
		t.Fatalf("expected legacy balance lookup by account number, got %s", svc.legacy.(*fakeLegacy).accountID)
	}
	if svc.legacy.(*fakeLegacy).pin != "123456" {
		t.Fatalf("expected forwarded transaction pin, got %s", svc.legacy.(*fakeLegacy).pin)
	}
	if cacheClient.setData.Balance != 75000 {
		t.Fatalf("expected cached legacy balance, got %+v", cacheClient.setData)
	}
}

func TestGetBalanceRejectsForeignAccount(t *testing.T) {
	t.Parallel()

	svc := NewService(&fakeRepo{
		account: dbsqlc.Account{
			ID:            uuid.New(),
			AccountNumber: "111",
		},
	}, &fakeBalanceCache{}, &fakeLegacy{})

	_, err := svc.GetBalance(context.Background(), uuid.New(), authservice.SessionView{
		LegacyAccountID: "LEGACY-ACC-1",
		AccountNumber:   "222",
	}, "123456")
	if !errors.Is(err, ErrAccountAccessDenied) {
		t.Fatalf("expected ErrAccountAccessDenied, got %v", err)
	}
}
