package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
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

func TestLoginCreatesDanteSession(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	store := &fakeSessionStore{}
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
	})
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
	if store.setTTL != 45*time.Minute {
		t.Fatalf("unexpected ttl: %s", store.setTTL)
	}

	session := store.sessions[resp.Token]
	if session.LegacySessionID != "SESS-123" {
		t.Fatalf("unexpected legacy session id: %s", session.LegacySessionID)
	}
	if session.AccountNumber != "2623860486223779" {
		t.Fatalf("unexpected account number: %s", session.AccountNumber)
	}
}

func TestLoginMapsInvalidCredentials(t *testing.T) {
	t.Parallel()

	svc := NewService(&fakeSessionStore{}, &fakeLegacy{
		loginErr: &legacy.OperationError{
			Operation: "login",
			Response:  "ERR|INVALID_CREDENTIALS",
		},
	})

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
	})
	svc.now = func() time.Time { return now }

	resp, err := svc.Register(context.Background(), "Budi Santoso", "budi@example.com", "secret", "123456")
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	if resp.Token == "" {
		t.Fatalf("expected non-empty token")
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
	})

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
	svc := NewService(store, legacyClient)

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

	svc := NewService(&fakeSessionStore{}, &fakeLegacy{})

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
				AccountID:           "ACC123",
				CustomerName:        "Budi",
				LegacySessionExpiry: now.Add(10 * time.Minute),
				CreatedAt:           now.Add(-5 * time.Minute),
			},
		},
	}
	svc := NewService(store, &fakeLegacy{})
	svc.now = func() time.Time { return now }

	session, err := svc.GetSession(context.Background(), "dante_token")
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if session.AccountID != "ACC123" {
		t.Fatalf("unexpected account id: %s", session.AccountID)
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
	svc := NewService(store, &fakeLegacy{})
	svc.now = func() time.Time { return now }

	_, err := svc.GetSession(context.Background(), "dante_token")
	if !errors.Is(err, ErrExpiredSession) {
		t.Fatalf("expected ErrExpiredSession, got %v", err)
	}
	if _, ok := store.sessions["dante_token"]; ok {
		t.Fatalf("expected expired session to be deleted")
	}
}
