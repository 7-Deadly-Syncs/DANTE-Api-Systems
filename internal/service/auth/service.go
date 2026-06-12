package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	"github.com/redis/go-redis/v9"
)

// ErrInvalidCredentials reports that the provided username/password pair is invalid.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrEmailAlreadyRegistered reports that the registration email already exists in legacy.
var ErrEmailAlreadyRegistered = errors.New("email already registered")

// ErrInvalidToken reports that the provided DANTE session token does not exist.
var ErrInvalidToken = errors.New("invalid token")

// ErrExpiredSession reports that the legacy session is already expired.
var ErrExpiredSession = errors.New("session expired")

// Authenticator describes the legacy auth operations needed by DANTE auth flows.
type Authenticator interface {
	Register(ctx context.Context, name, email, password, pin string) (*legacy.RegisterResult, error)
	Login(ctx context.Context, email, password string) (*legacy.LoginResult, error)
	Logout(ctx context.Context, sessionID string) error
	GetAccountProfile(ctx context.Context, accountID, password string) (*legacy.AccountProfile, error)
}

// SessionStore describes the session persistence operations used by the auth service.
type SessionStore interface {
	GetSession(ctx context.Context, token string) (*cache.SessionEntry, error)
	SetSession(ctx context.Context, entry cache.SessionEntry, ttl time.Duration) error
	DeleteSession(ctx context.Context, token string) error
}

// AccountProvisioner describes the local account mapping operations needed after legacy auth.
type AccountProvisioner interface {
	GetUserByPhoneNumber(ctx context.Context, phoneNumber sql.NullString) (dbsqlc.User, error)
	CreateUser(ctx context.Context, arg dbsqlc.CreateUserParams) (dbsqlc.User, error)
	GetAccountByNumber(ctx context.Context, accountNumber string) (dbsqlc.Account, error)
	CreateAccount(ctx context.Context, arg dbsqlc.CreateAccountParams) (dbsqlc.Account, error)
}

// LoginResponse is the DANTE-issued session view returned to API handlers.
type LoginResponse struct {
	Token           string
	CustomerID      string
	AccountID       string
	LegacyAccountID string
	AccountNumber   string
	CustomerName    string
	ExpiresAt       time.Time
}

// SessionView is the validated DANTE session view used by authenticated endpoints.
type SessionView struct {
	Token           string
	CustomerID      string
	AccountID       string
	LegacyAccountID string
	AccountNumber   string
	CustomerName    string
	ExpiresAt       time.Time
	CreatedAt       time.Time
}

// Service coordinates DANTE-issued sessions with legacy credential validation.
type Service struct {
	store       SessionStore
	legacy      Authenticator
	provisioner AccountProvisioner
	now         func() time.Time
}

// NewService constructs an auth service.
func NewService(store SessionStore, legacyClient Authenticator, provisioner AccountProvisioner) *Service {
	return &Service{
		store:       store,
		legacy:      legacyClient,
		provisioner: provisioner,
		now:         time.Now,
	}
}

// Login validates credentials against legacy, then creates a DANTE-issued token.
func (s *Service) Login(ctx context.Context, username, password string) (*LoginResponse, error) {
	return s.issueSessionFromLegacyLogin(ctx, username, password)
}

// Register creates a legacy account, then issues a DANTE session by logging the user in.
func (s *Service) Register(ctx context.Context, name, email, password, pin string) (*LoginResponse, error) {
	_, err := s.legacy.Register(ctx, name, email, password, pin)
	if err != nil {
		switch {
		case legacy.IsEmailExists(err):
			return nil, ErrEmailAlreadyRegistered
		default:
			return nil, fmt.Errorf("legacy register failed: %w", err)
		}
	}

	return s.issueSessionFromLegacyLogin(ctx, email, password)
}

// Logout invalidates the legacy session, then deletes the DANTE-issued token.
func (s *Service) Logout(ctx context.Context, token string) error {
	session, err := s.store.GetSession(ctx, token)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return ErrInvalidToken
		}
		return fmt.Errorf("load dante session: %w", err)
	}

	if err := s.legacy.Logout(ctx, session.LegacySessionID); err != nil && !legacy.IsInvalidSession(err) {
		return fmt.Errorf("legacy logout failed: %w", err)
	}

	if err := s.store.DeleteSession(ctx, token); err != nil {
		return fmt.Errorf("delete dante session: %w", err)
	}

	return nil
}

// GetSession validates and returns a DANTE-issued session by token.
func (s *Service) GetSession(ctx context.Context, token string) (*SessionView, error) {
	session, err := s.store.GetSession(ctx, token)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrInvalidToken
		}
		return nil, fmt.Errorf("load dante session: %w", err)
	}

	if session.LegacySessionExpiry.UTC().Before(s.now().UTC()) {
		_ = s.store.DeleteSession(ctx, token)
		return nil, ErrExpiredSession
	}

	return &SessionView{
		Token:           session.Token,
		CustomerID:      session.CustomerID,
		AccountID:       session.LocalAccountID,
		LegacyAccountID: session.LegacyAccountID,
		AccountNumber:   session.AccountNumber,
		CustomerName:    session.CustomerName,
		ExpiresAt:       session.LegacySessionExpiry.UTC(),
		CreatedAt:       session.CreatedAt.UTC(),
	}, nil
}

func newSessionToken() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}

	return "dante_" + hex.EncodeToString(raw[:]), nil
}

func (s *Service) issueSessionFromLegacyLogin(ctx context.Context, username, password string) (*LoginResponse, error) {
	result, err := s.legacy.Login(ctx, username, password)
	if err != nil {
		switch {
		case legacy.IsInvalidCredentials(err):
			return nil, ErrInvalidCredentials
		default:
			return nil, fmt.Errorf("legacy login failed: %w", err)
		}
	}

	now := s.now().UTC()
	expiresAt := result.ExpiresAt.UTC()
	ttl := expiresAt.Sub(now)
	if ttl <= 0 {
		return nil, ErrExpiredSession
	}

	profile, err := s.legacy.GetAccountProfile(ctx, result.AccountID, password)
	if err != nil {
		return nil, fmt.Errorf("load legacy account profile after login: %w", err)
	}
	localAccount, err := s.ensureAccountProvisioned(ctx, result.CustomerID, profile.Name, profile.AccountNumber)
	if err != nil {
		return nil, fmt.Errorf("provision local account mapping: %w", err)
	}

	token, err := newSessionToken()
	if err != nil {
		return nil, fmt.Errorf("generate session token: %w", err)
	}

	entry := cache.SessionEntry{
		Token:               token,
		CustomerID:          result.CustomerID,
		LocalAccountID:      localAccount.ID.String(),
		LegacyAccountID:     result.AccountID,
		AccountNumber:       profile.AccountNumber,
		CustomerName:        profile.Name,
		LegacySessionID:     result.SessionReference,
		LegacySessionExpiry: expiresAt,
		CreatedAt:           now,
	}
	if err := s.store.SetSession(ctx, entry, ttl); err != nil {
		return nil, fmt.Errorf("store dante session: %w", err)
	}

	return &LoginResponse{
		Token:           token,
		CustomerID:      entry.CustomerID,
		AccountID:       entry.LocalAccountID,
		LegacyAccountID: entry.LegacyAccountID,
		AccountNumber:   entry.AccountNumber,
		CustomerName:    entry.CustomerName,
		ExpiresAt:       expiresAt,
	}, nil
}

func (s *Service) ensureAccountProvisioned(ctx context.Context, customerID, customerName, accountNumber string) (dbsqlc.Account, error) {
	if s.provisioner == nil {
		return dbsqlc.Account{}, nil
	}

	if account, err := s.provisioner.GetAccountByNumber(ctx, accountNumber); err == nil {
		return account, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return dbsqlc.Account{}, err
	}

	lookupKey := sql.NullString{String: customerID, Valid: customerID != ""}
	user, err := s.provisioner.GetUserByPhoneNumber(ctx, lookupKey)
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		user, err = s.provisioner.CreateUser(ctx, dbsqlc.CreateUserParams{
			Name:        customerName,
			PhoneNumber: lookupKey,
		})
		if err != nil {
			user, err = s.provisioner.GetUserByPhoneNumber(ctx, lookupKey)
			if err != nil {
				return dbsqlc.Account{}, err
			}
		}
	default:
		return dbsqlc.Account{}, err
	}

	account, err := s.provisioner.CreateAccount(ctx, dbsqlc.CreateAccountParams{
		UserID:        user.ID,
		AccountNumber: accountNumber,
		Balance:       0,
	})
	if err == nil {
		return account, nil
	}

	account, reloadErr := s.provisioner.GetAccountByNumber(ctx, accountNumber)
	if reloadErr == nil {
		return account, nil
	}

	return dbsqlc.Account{}, err
}
