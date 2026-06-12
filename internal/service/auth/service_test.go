package auth

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type fakeLegacy struct {
	registerResult *legacy.RegisterResult
	registerErr    error
	loginResult    *legacy.LoginResult
	loginErr       error
	profile        *legacy.AccountProfile
	profileErr     error
	logoutErr      error
	logoutInput    string
}

func (f *fakeLegacy) Register(ctx context.Context, name, email, password, pin string) (*legacy.RegisterResult, error) {
	return f.registerResult, f.registerErr
}

func (f *fakeLegacy) Login(ctx context.Context, email, password string) (*legacy.LoginResult, error) {
	return f.loginResult, f.loginErr
}

func (f *fakeLegacy) Logout(ctx context.Context, sessionID string) error {
	f.logoutInput = sessionID
	return f.logoutErr
}

func (f *fakeLegacy) GetAccountProfile(ctx context.Context, accountID, password string) (*legacy.AccountProfile, error) {
	return f.profile, f.profileErr
}

type fakeSessionStore struct {
	sessions map[string]cache.SessionEntry
	setTTL   time.Duration
}

func (f *fakeSessionStore) GetSession(ctx context.Context, token string) (*cache.SessionEntry, error) {
	entry, ok := f.sessions[token]
	if !ok {
		return nil, redis.Nil
	}
	return &entry, nil
}

func (f *fakeSessionStore) SetSession(ctx context.Context, entry cache.SessionEntry, ttl time.Duration) error {
	if f.sessions == nil {
		f.sessions = map[string]cache.SessionEntry{}
	}
	f.sessions[entry.Token] = entry
	f.setTTL = ttl
	return nil
}

func (f *fakeSessionStore) DeleteSession(ctx context.Context, token string) error {
	delete(f.sessions, token)
	return nil
}

type fakeProvisioner struct {
	user             dbsqlc.User
	account          dbsqlc.Account
	getUserErr       error
	getAccountErr    error
	createUserErr    error
	createAccountErr error
	createUserCalls  int
	createAccCalls   int
}

func (f *fakeProvisioner) GetUserByPhoneNumber(ctx context.Context, phoneNumber sql.NullString) (dbsqlc.User, error) {
	if f.getUserErr != nil {
		return dbsqlc.User{}, f.getUserErr
	}
	return f.user, nil
}

func (f *fakeProvisioner) CreateUser(ctx context.Context, arg dbsqlc.CreateUserParams) (dbsqlc.User, error) {
	f.createUserCalls++
	if f.createUserErr != nil {
		return dbsqlc.User{}, f.createUserErr
	}
	if f.user.ID == uuid.Nil {
		f.user = dbsqlc.User{
			ID:          uuid.New(),
			Name:        arg.Name,
			PhoneNumber: arg.PhoneNumber,
			CreatedAt:   time.Now().UTC(),
		}
	}
	f.getUserErr = nil
	return f.user, nil
}

func (f *fakeProvisioner) GetAccountByNumber(ctx context.Context, accountNumber string) (dbsqlc.Account, error) {
	if f.getAccountErr != nil {
		return dbsqlc.Account{}, f.getAccountErr
	}
	return f.account, nil
}

func (f *fakeProvisioner) CreateAccount(ctx context.Context, arg dbsqlc.CreateAccountParams) (dbsqlc.Account, error) {
	f.createAccCalls++
	if f.createAccountErr != nil {
		return dbsqlc.Account{}, f.createAccountErr
	}
	if f.account.ID == uuid.Nil {
		f.account = dbsqlc.Account{
			ID:            uuid.New(),
			UserID:        arg.UserID,
			AccountNumber: arg.AccountNumber,
			Balance:       arg.Balance,
			CreatedAt:     time.Now().UTC(),
		}
	}
	f.getAccountErr = nil
	return f.account, nil
}

func TestLoginCreatesDanteSession(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	store := &fakeSessionStore{}
	provisioner := &fakeProvisioner{
		getUserErr:    sql.ErrNoRows,
		getAccountErr: sql.ErrNoRows,
	}
	svc := NewService(store, &fakeLegacy{
		loginResult: &legacy.LoginResult{
			CustomerID:       "CUST123",
			AccountID:        "ACC987654",
			AccountNumber:    "2623860486223779",
			SessionReference: "SESS-123",
			ExpiresAt:        now.Add(45 * time.Minute),
		},
		profile: &legacy.AccountProfile{
			CustomerID:    "CUST123",
			AccountID:     "ACC987654",
			AccountNumber: "2623860486223779",
			Name:          "Budi Santoso",
		},
	}, provisioner)
	svc.now = func() time.Time { return now }

	resp, err := svc.Login(context.Background(), "budi@example.com", "secret")
	if err != nil {
		t.Fatalf("Login returned error: %v", err)
	}

	if resp.Token == "" {
		t.Fatalf("expected non-empty token")
	}
	if resp.CustomerID != "CUST123" {
		t.Fatalf("unexpected customer id: %s", resp.CustomerID)
	}
	if resp.AccountID != provisioner.account.ID.String() {
		t.Fatalf("unexpected local account id: %s", resp.AccountID)
	}
	if resp.LegacyAccountID != "ACC987654" {
		t.Fatalf("unexpected legacy account id: %s", resp.LegacyAccountID)
	}
	if store.setTTL != 45*time.Minute {
		t.Fatalf("unexpected ttl: %s", store.setTTL)
	}

	session := store.sessions[resp.Token]
	if session.LocalAccountID != provisioner.account.ID.String() {
		t.Fatalf("unexpected local account id in session: %s", session.LocalAccountID)
	}
	if session.LegacyAccountID != "ACC987654" {
		t.Fatalf("unexpected legacy account id in session: %s", session.LegacyAccountID)
	}
	if session.LegacySessionID != "SESS-123" {
		t.Fatalf("unexpected legacy session id: %s", session.LegacySessionID)
	}
	if session.AccountNumber != "2623860486223779" {
		t.Fatalf("unexpected account number: %s", session.AccountNumber)
	}
	if provisioner.createUserCalls != 1 {
		t.Fatalf("expected one local user creation, got %d", provisioner.createUserCalls)
	}
	if provisioner.createAccCalls != 1 {
		t.Fatalf("expected one local account creation, got %d", provisioner.createAccCalls)
	}
}

func TestLoginMapsInvalidCredentials(t *testing.T) {
	t.Parallel()

	svc := NewService(&fakeSessionStore{}, &fakeLegacy{
		loginErr: &legacy.OperationError{
			Operation: "login",
			Response:  "ERR|INVALID_CREDENTIALS",
		},
	}, nil)

	_, err := svc.Login(context.Background(), "bad@example.com", "bad")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestRegisterCreatesLegacyAccountAndDanteSession(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	store := &fakeSessionStore{}
	svc := NewService(store, &fakeLegacy{
		registerResult: &legacy.RegisterResult{
			CustomerID:    "CUST123",
			AccountID:     "ACC987654",
			AccountNumber: "2623860486223779",
		},
		loginResult: &legacy.LoginResult{
			CustomerID:       "CUST123",
			AccountID:        "ACC987654",
			SessionReference: "SESS-123",
			ExpiresAt:        now.Add(45 * time.Minute),
		},
		profile: &legacy.AccountProfile{
			CustomerID:    "CUST123",
			AccountID:     "ACC987654",
			AccountNumber: "2623860486223779",
			Name:          "Budi Santoso",
		},
	}, &fakeProvisioner{
		getUserErr:    sql.ErrNoRows,
		getAccountErr: sql.ErrNoRows,
	})
	svc.now = func() time.Time { return now }

	resp, err := svc.Register(context.Background(), "Budi Santoso", "budi@example.com", "secret", "123456")
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	if resp.Token == "" {
		t.Fatalf("expected non-empty token")
	}
	if resp.AccountID == "" {
		t.Fatalf("expected local account id in register response")
	}
	if resp.AccountNumber != "2623860486223779" {
		t.Fatalf("unexpected account number: %s", resp.AccountNumber)
	}
}

func TestRegisterMapsDuplicateEmail(t *testing.T) {
	t.Parallel()

	svc := NewService(&fakeSessionStore{}, &fakeLegacy{
		registerErr: &legacy.OperationError{
			Operation: "register",
			Response:  "ERR|EMAIL_EXISTS",
		},
	}, nil)

	_, err := svc.Register(context.Background(), "Budi", "budi@example.com", "secret", "123456")
	if !errors.Is(err, ErrEmailAlreadyRegistered) {
		t.Fatalf("expected ErrEmailAlreadyRegistered, got %v", err)
	}
}

func TestLogoutDeletesSessionAndCallsLegacy(t *testing.T) {
	t.Parallel()

	store := &fakeSessionStore{
		sessions: map[string]cache.SessionEntry{
			"dante_token": {
				Token:           "dante_token",
				LegacySessionID: "SESS-999",
			},
		},
	}
	legacyClient := &fakeLegacy{}
	svc := NewService(store, legacyClient, nil)

	if err := svc.Logout(context.Background(), "dante_token"); err != nil {
		t.Fatalf("Logout returned error: %v", err)
	}

	if legacyClient.logoutInput != "SESS-999" {
		t.Fatalf("unexpected logout input: %s", legacyClient.logoutInput)
	}
	if _, ok := store.sessions["dante_token"]; ok {
		t.Fatalf("expected session to be deleted")
	}
}

func TestLogoutMapsMissingSessionToInvalidToken(t *testing.T) {
	t.Parallel()

	svc := NewService(&fakeSessionStore{}, &fakeLegacy{}, nil)

	err := svc.Logout(context.Background(), "missing")
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestGetSessionReturnsValidatedView(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	store := &fakeSessionStore{
		sessions: map[string]cache.SessionEntry{
			"dante_token": {
				Token:               "dante_token",
				CustomerID:          "CUST123",
				LocalAccountID:      "550e8400-e29b-41d4-a716-446655440000",
				LegacyAccountID:     "ACC123",
				CustomerName:        "Budi",
				LegacySessionExpiry: now.Add(10 * time.Minute),
				CreatedAt:           now.Add(-5 * time.Minute),
			},
		},
	}
	svc := NewService(store, &fakeLegacy{}, nil)
	svc.now = func() time.Time { return now }

	session, err := svc.GetSession(context.Background(), "dante_token")
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if session.AccountID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("unexpected account id: %s", session.AccountID)
	}
	if session.LegacyAccountID != "ACC123" {
		t.Fatalf("unexpected legacy account id: %s", session.LegacyAccountID)
	}
}

func TestGetSessionExpiresAndDeletesStaleSession(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	store := &fakeSessionStore{
		sessions: map[string]cache.SessionEntry{
			"dante_token": {
				Token:               "dante_token",
				LegacySessionExpiry: now.Add(-1 * time.Minute),
			},
		},
	}
	svc := NewService(store, &fakeLegacy{}, nil)
	svc.now = func() time.Time { return now }

	_, err := svc.GetSession(context.Background(), "dante_token")
	if !errors.Is(err, ErrExpiredSession) {
		t.Fatalf("expected ErrExpiredSession, got %v", err)
	}
	if _, ok := store.sessions["dante_token"]; ok {
		t.Fatalf("expected expired session to be deleted")
	}
}
