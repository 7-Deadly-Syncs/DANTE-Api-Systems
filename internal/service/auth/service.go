package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
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

// LoginResponse is the DANTE-issued session view returned to API handlers.
type LoginResponse struct {
	Token         string
	CustomerID    string
	AccountID     string
	AccountNumber string
	CustomerName  string
	ExpiresAt     time.Time
}

// SessionView is the validated DANTE session view used by authenticated endpoints.
type SessionView struct {
	Token         string
	CustomerID    string
	AccountID     string
	AccountNumber string
	CustomerName  string
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

// Service coordinates DANTE-issued sessions with legacy credential validation.
type Service struct {
	store  SessionStore
	legacy Authenticator
	now    func() time.Time
}

// NewService constructs an auth service.
func NewService(store SessionStore, legacyClient Authenticator) *Service {
	return &Service{
		store:  store,
		legacy: legacyClient,
		now:    time.Now,
	}
}

// Login validates credentials against legacy, then creates a DANTE-issued token.
func (s *Service) Login(ctx context.Context, username, password string) (*LoginResponse, error) {
	ctx, span := tracing.StartInternalSpan(ctx, "service.auth", "auth.login",
		attribute.Bool("auth.username_present", username != ""),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, ErrInvalidCredentials, ErrExpiredSession)
	}()

	result, err := s.issueSessionFromLegacyLogin(ctx, username, password)
	spanErr = err
	if err == nil {
		span.SetAttributes(attribute.String("auth.result", "session_issued"))
	}
	return result, err
}

// Register creates a legacy account, then issues a DANTE session by logging the user in.
func (s *Service) Register(ctx context.Context, name, email, password, pin string) (*LoginResponse, error) {
	ctx, span := tracing.StartInternalSpan(ctx, "service.auth", "auth.register",
		attribute.Bool("auth.email_present", email != ""),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, ErrEmailAlreadyRegistered, ErrInvalidCredentials, ErrExpiredSession)
	}()

	legacyCtx, legacySpan := tracing.StartClientSpan(ctx, "legacy", "legacy.register",
		attribute.String("legacy.system", "banking"),
		attribute.String("legacy.operation", "register"),
	)
	_, err := s.legacy.Register(legacyCtx, name, email, password, pin)
	tracing.EndSpan(legacySpan, err)
	if err != nil {
		switch {
		case legacy.IsEmailExists(err):
			span.SetAttributes(attribute.String("auth.result", "email_already_registered"))
			spanErr = ErrEmailAlreadyRegistered
			return nil, ErrEmailAlreadyRegistered
		default:
			spanErr = err
			return nil, fmt.Errorf("legacy register failed: %w", err)
		}
	}

	result, err := s.issueSessionFromLegacyLogin(ctx, email, password)
	spanErr = err
	if err == nil {
		span.SetAttributes(attribute.String("auth.result", "registered_and_session_issued"))
	}
	return result, err
}

// Logout invalidates the legacy session, then deletes the DANTE-issued token.
func (s *Service) Logout(ctx context.Context, token string) error {
	ctx, span := tracing.StartInternalSpan(ctx, "service.auth", "auth.logout",
		attribute.Bool("auth.token_present", token != ""),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, ErrInvalidToken)
	}()

	session, err := s.store.GetSession(ctx, token)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			span.SetAttributes(attribute.String("auth.result", "invalid_token"))
			spanErr = ErrInvalidToken
			return ErrInvalidToken
		}
		spanErr = err
		return fmt.Errorf("load dante session: %w", err)
	}

	legacyCtx, legacySpan := tracing.StartClientSpan(ctx, "legacy", "legacy.logout",
		attribute.String("legacy.system", "banking"),
		attribute.String("legacy.operation", "logout"),
	)
	err = s.legacy.Logout(legacyCtx, session.LegacySessionID)
	tracing.EndSpan(legacySpan, err)
	if err != nil && !legacy.IsInvalidSession(err) {
		spanErr = err
		return fmt.Errorf("legacy logout failed: %w", err)
	}

	if err := s.store.DeleteSession(ctx, token); err != nil {
		spanErr = err
		return fmt.Errorf("delete dante session: %w", err)
	}

	span.SetAttributes(attribute.String("auth.result", "logged_out"))
	return nil
}

// GetSession validates and returns a DANTE-issued session by token.
func (s *Service) GetSession(ctx context.Context, token string) (*SessionView, error) {
	ctx, span := tracing.StartInternalSpan(ctx, "service.auth", "auth.get_session",
		attribute.Bool("auth.token_present", token != ""),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, ErrInvalidToken, ErrExpiredSession)
	}()

	session, err := s.store.GetSession(ctx, token)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			span.SetAttributes(attribute.String("auth.result", "invalid_token"))
			spanErr = ErrInvalidToken
			return nil, ErrInvalidToken
		}
		spanErr = err
		return nil, fmt.Errorf("load dante session: %w", err)
	}

	if session.LegacySessionExpiry.UTC().Before(s.now().UTC()) {
		_ = s.store.DeleteSession(ctx, token)
		span.SetAttributes(attribute.String("auth.result", "expired_session"))
		spanErr = ErrExpiredSession
		return nil, ErrExpiredSession
	}

	span.SetAttributes(attribute.String("auth.result", "valid_session"))
	return &SessionView{
		Token:         session.Token,
		CustomerID:    session.CustomerID,
		AccountID:     session.AccountID,
		AccountNumber: session.AccountNumber,
		CustomerName:  session.CustomerName,
		ExpiresAt:     session.LegacySessionExpiry.UTC(),
		CreatedAt:     session.CreatedAt.UTC(),
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
	ctx, span := tracing.StartInternalSpan(ctx, "service.auth", "auth.issue_session_from_legacy_login")
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, ErrInvalidCredentials, ErrExpiredSession)
	}()

	legacyLoginCtx, legacyLoginSpan := tracing.StartClientSpan(ctx, "legacy", "legacy.login",
		attribute.String("legacy.system", "banking"),
		attribute.String("legacy.operation", "login"),
	)
	result, err := s.legacy.Login(legacyLoginCtx, username, password)
	tracing.EndSpan(legacyLoginSpan, err)
	if err != nil {
		switch {
		case legacy.IsInvalidCredentials(err):
			span.SetAttributes(attribute.String("auth.result", "invalid_credentials"))
			spanErr = ErrInvalidCredentials
			return nil, ErrInvalidCredentials
		default:
			spanErr = err
			return nil, fmt.Errorf("legacy login failed: %w", err)
		}
	}

	now := s.now().UTC()
	expiresAt := result.ExpiresAt.UTC()
	ttl := expiresAt.Sub(now)
	if ttl <= 0 {
		span.SetAttributes(attribute.String("auth.result", "expired_legacy_session"))
		spanErr = ErrExpiredSession
		return nil, ErrExpiredSession
	}
	span.SetAttributes(attribute.Int64("auth.session_ttl_ms", ttl.Milliseconds()))

	legacyProfileCtx, legacyProfileSpan := tracing.StartClientSpan(ctx, "legacy", "legacy.get_account_profile",
		attribute.String("legacy.system", "banking"),
		attribute.String("legacy.operation", "getAccountProfile"),
	)
	profile, err := s.legacy.GetAccountProfile(legacyProfileCtx, result.AccountID, password)
	tracing.EndSpan(legacyProfileSpan, err)
	if err != nil {
		spanErr = err
		return nil, fmt.Errorf("load legacy account profile after login: %w", err)
	}

	token, err := newSessionToken()
	if err != nil {
		spanErr = err
		return nil, fmt.Errorf("generate session token: %w", err)
	}

	entry := cache.SessionEntry{
		Token:               token,
		CustomerID:          result.CustomerID,
		AccountID:           result.AccountID,
		AccountNumber:       profile.AccountNumber,
		CustomerName:        profile.Name,
		LegacySessionID:     result.SessionReference,
		LegacySessionExpiry: expiresAt,
		CreatedAt:           now,
	}
	if err := s.store.SetSession(ctx, entry, ttl); err != nil {
		spanErr = err
		return nil, fmt.Errorf("store dante session: %w", err)
	}

	span.SetAttributes(attribute.String("auth.result", "session_stored"))
	return &LoginResponse{
		Token:         token,
		CustomerID:    entry.CustomerID,
		AccountID:     entry.AccountID,
		AccountNumber: entry.AccountNumber,
		CustomerName:  entry.CustomerName,
		ExpiresAt:     expiresAt,
	}, nil
}
