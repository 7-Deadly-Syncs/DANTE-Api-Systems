package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/queue"
	authservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/auth"
	"github.com/google/uuid"
)

const (
	statusProcessing               = "PROCESSING"
	statusFailed                   = "FAILED"
	eventTypeTransactionCreated    = "TRANSACTION_CREATED"
	eventTypeTransactionEnqueued   = "TRANSACTION_ENQUEUED"
	eventTypeTransactionQueueError = "TRANSACTION_QUEUE_PUBLISH_FAILED"
)

// ErrIdempotencyConflict reports a mismatched request under an existing idempotency key.
var ErrIdempotencyConflict = errors.New("idempotency key already used for a different request")

// ErrAccountNotProvisioned reports that the authenticated legacy account is not mapped locally.
var ErrAccountNotProvisioned = errors.New("authenticated account is not provisioned in dante")

// ErrMerchantNotFound reports that the requested merchant does not exist locally.
var ErrMerchantNotFound = errors.New("merchant not found")

// QRISRequest is the validated service input for a QRIS payment creation request.
type QRISRequest struct {
	Session        authservice.SessionView
	MerchantID     uuid.UUID
	Amount         int64
	IdempotencyKey string
}

// QRISResult is the service response returned after transaction intake.
type QRISResult struct {
	Transaction dbsqlc.Transaction
	Created     bool
}

// Repository describes the database operations needed for QRIS transaction intake.
type Repository interface {
	GetAccountByNumber(ctx context.Context, accountNumber string) (dbsqlc.Account, error)
	GetMerchantByID(ctx context.Context, id uuid.UUID) (dbsqlc.Merchant, error)
	GetTransactionByIdempotencyKey(ctx context.Context, idempotencyKey string) (dbsqlc.Transaction, error)
	CreateTransaction(ctx context.Context, arg dbsqlc.CreateTransactionParams) (dbsqlc.Transaction, error)
	CreateTransactionEvent(ctx context.Context, arg dbsqlc.CreateTransactionEventParams) (dbsqlc.TransactionEvent, error)
	UpdateTransactionStatus(ctx context.Context, arg dbsqlc.UpdateTransactionStatusParams) (dbsqlc.Transaction, error)
}

// QRISPublisher describes the async queue publishing operation used by QRIS intake.
type QRISPublisher interface {
	PublishQRISPayment(ctx context.Context, msg queue.QRISPaymentMessage) error
}

// StatusCache describes the fast-status cache operations used by QRIS intake.
type StatusCache interface {
	SetTransactionStatus(ctx context.Context, entry cache.TransactionStatusCacheEntry, ttl time.Duration) error
}

// QRISService creates QRIS transactions, stores fast status, and queues async work.
type QRISService struct {
	repo      Repository
	cache     StatusCache
	publisher QRISPublisher
}

// NewQRISService constructs a QRIS payment intake service.
func NewQRISService(repo Repository, cacheClient StatusCache, publisher QRISPublisher) *QRISService {
	return &QRISService{
		repo:      repo,
		cache:     cacheClient,
		publisher: publisher,
	}
}

// CreateTransaction validates idempotency, persists a PROCESSING transaction, caches status, and publishes a queue job.
func (s *QRISService) CreateTransaction(ctx context.Context, req QRISRequest) (*QRISResult, error) {
	existing, err := s.repo.GetTransactionByIdempotencyKey(ctx, req.IdempotencyKey)
	if err == nil {
		account, accountErr := s.repo.GetAccountByNumber(ctx, req.Session.AccountNumber)
		if accountErr != nil {
			return nil, fmt.Errorf("load account for idempotency comparison: %w", accountErr)
		}

		if existing.AccountID != account.ID || !existing.MerchantID.Valid || existing.MerchantID.UUID != req.MerchantID || existing.Amount != req.Amount {
			return nil, ErrIdempotencyConflict
		}

		return &QRISResult{
			Transaction: existing,
			Created:     false,
		}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("lookup idempotency key: %w", err)
	}

	account, err := s.repo.GetAccountByNumber(ctx, req.Session.AccountNumber)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAccountNotProvisioned
		}
		return nil, fmt.Errorf("load local account: %w", err)
	}

	merchant, err := s.repo.GetMerchantByID(ctx, req.MerchantID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMerchantNotFound
		}
		return nil, fmt.Errorf("load merchant: %w", err)
	}

	txRow, err := s.repo.CreateTransaction(ctx, dbsqlc.CreateTransactionParams{
		UserID:            account.UserID,
		MerchantID:        uuid.NullUUID{UUID: merchant.ID, Valid: true},
		AccountID:         account.ID,
		Amount:            req.Amount,
		Status:            statusProcessing,
		IdempotencyKey:    req.IdempotencyKey,
		LegacyReferenceID: sql.NullString{},
		FailureReason:     sql.NullString{},
		ProcessedAt:       sql.NullTime{},
	})
	if err != nil {
		return nil, fmt.Errorf("create transaction: %w", err)
	}

	metadata, err := json.Marshal(map[string]any{
		"merchant_id":   merchant.ID.String(),
		"merchant_code": merchant.QrisCode,
		"amount":        req.Amount,
		"account_id":    account.ID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal transaction event metadata: %w", err)
	}

	if _, err := s.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
		TransactionID: txRow.ID,
		EventType:     eventTypeTransactionCreated,
		Message:       sql.NullString{String: "transaction accepted for async QRIS processing", Valid: true},
		Metadata:      metadata,
	}); err != nil {
		return nil, fmt.Errorf("create transaction event: %w", err)
	}

	if err := s.cache.SetTransactionStatus(ctx, cache.TransactionStatusCacheEntry{
		ID:          txRow.ID.String(),
		Status:      txRow.Status,
		RequestedAt: txRow.RequestedAt,
		UpdatedAt:   txRow.UpdatedAt,
	}, cache.TransactionStatusTTL); err != nil {
		return nil, fmt.Errorf("cache transaction status: %w", err)
	}

	if err := s.publisher.PublishQRISPayment(ctx, queue.QRISPaymentMessage{
		TransactionID: txRow.ID.String(),
		AccountUUID:   account.ID.String(),
		AccountID:     req.Session.AccountID,
		AccountNumber: req.Session.AccountNumber,
		MerchantID:    merchant.ID.String(),
		MerchantCode:  merchant.QrisCode,
		Amount:        txRow.Amount,
	}); err != nil {
		now := time.Now().UTC()
		failedRow, updateErr := s.repo.UpdateTransactionStatus(ctx, dbsqlc.UpdateTransactionStatusParams{
			ID:            txRow.ID,
			Status:        statusFailed,
			FailureReason: sql.NullString{String: "queue publish failed", Valid: true},
			ProcessedAt:   sql.NullTime{Time: now, Valid: true},
		})
		if updateErr == nil {
			_ = s.cache.SetTransactionStatus(ctx, cache.TransactionStatusCacheEntry{
				ID:          failedRow.ID.String(),
				Status:      failedRow.Status,
				RequestedAt: failedRow.RequestedAt,
				ProcessedAt: &now,
				UpdatedAt:   failedRow.UpdatedAt,
			}, cache.TransactionStatusTTL)
		}
		_, _ = s.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
			TransactionID: txRow.ID,
			EventType:     eventTypeTransactionQueueError,
			Message:       sql.NullString{String: "failed to publish qris job", Valid: true},
			Metadata:      json.RawMessage(`{"reason":"queue_publish_failed"}`),
		})
		return nil, fmt.Errorf("publish qris job: %w", err)
	}

	_, _ = s.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
		TransactionID: txRow.ID,
		EventType:     eventTypeTransactionEnqueued,
		Message:       sql.NullString{String: "transaction queued for worker processing", Valid: true},
		Metadata:      json.RawMessage(`{"queue":"qris"}`),
	})

	return &QRISResult{
		Transaction: txRow,
		Created:     true,
	}, nil
}
