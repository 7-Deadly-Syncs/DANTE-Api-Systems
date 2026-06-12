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
	eventTypeTransferCreated  = "TRANSFER_CREATED"
	eventTypeTransferEnqueued = "TRANSFER_ENQUEUED"
)

// TransferRequest is the validated service input for a transfer creation request.
type TransferRequest struct {
	Session         authservice.SessionView
	ToAccountNumber string
	Amount          int64
	TransactionPIN  string
	IdempotencyKey  string
}

// TransferService creates transfer transactions and publishes async transfer work.
type TransferService struct {
	repo      TransferRepository
	cache     StatusCache
	publisher TransferPublisher
}

// TransferRepository describes the database operations needed for transfer intake.
type TransferRepository interface {
	GetAccountByNumber(ctx context.Context, accountNumber string) (dbsqlc.Account, error)
	GetTransactionByIdempotencyKey(ctx context.Context, idempotencyKey string) (dbsqlc.Transaction, error)
	ListTransactionEventsByTransactionID(ctx context.Context, transactionID uuid.UUID) ([]dbsqlc.TransactionEvent, error)
	CreateTransaction(ctx context.Context, arg dbsqlc.CreateTransactionParams) (dbsqlc.Transaction, error)
	CreateTransactionEvent(ctx context.Context, arg dbsqlc.CreateTransactionEventParams) (dbsqlc.TransactionEvent, error)
	UpdateTransactionStatus(ctx context.Context, arg dbsqlc.UpdateTransactionStatusParams) (dbsqlc.Transaction, error)
}

// TransferPublisher describes the async queue publishing operation used by transfer intake.
type TransferPublisher interface {
	PublishTransfer(ctx context.Context, msg queue.TransferMessage) error
}

// NewTransferService constructs a transfer intake service.
func NewTransferService(repo TransferRepository, cacheClient StatusCache, publisher TransferPublisher) *TransferService {
	return &TransferService{
		repo:      repo,
		cache:     cacheClient,
		publisher: publisher,
	}
}

// CreateTransaction validates idempotency, persists a PROCESSING transfer transaction, caches status, and publishes a queue job.
func (s *TransferService) CreateTransaction(ctx context.Context, req TransferRequest) (*QRISResult, error) {
	existing, err := s.repo.GetTransactionByIdempotencyKey(ctx, req.IdempotencyKey)
	if err == nil {
		account, accountErr := s.repo.GetAccountByNumber(ctx, req.Session.AccountNumber)
		if accountErr != nil {
			return nil, fmt.Errorf("load source account for idempotency comparison: %w", accountErr)
		}

		if existing.AccountID != account.ID || existing.MerchantID.Valid || existing.Amount != req.Amount {
			return nil, ErrIdempotencyConflict
		}

		events, eventsErr := s.repo.ListTransactionEventsByTransactionID(ctx, existing.ID)
		if eventsErr != nil {
			return nil, fmt.Errorf("load transaction events for idempotency comparison: %w", eventsErr)
		}

		if !transferReplayMatches(events, req.ToAccountNumber) {
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
		return nil, fmt.Errorf("load local source account: %w", err)
	}

	txRow, err := s.repo.CreateTransaction(ctx, dbsqlc.CreateTransactionParams{
		UserID:            account.UserID,
		MerchantID:        uuid.NullUUID{},
		AccountID:         account.ID,
		Amount:            req.Amount,
		Status:            statusProcessing,
		IdempotencyKey:    req.IdempotencyKey,
		LegacyReferenceID: sql.NullString{},
		FailureReason:     sql.NullString{},
		ProcessedAt:       sql.NullTime{},
	})
	if err != nil {
		return nil, fmt.Errorf("create transfer transaction: %w", err)
	}

	metadata, err := json.Marshal(map[string]any{
		"transaction_type":  "transfer",
		"to_account_number": req.ToAccountNumber,
		"amount":            req.Amount,
		"account_id":        account.ID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal transfer event metadata: %w", err)
	}

	if _, err := s.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
		TransactionID: txRow.ID,
		EventType:     eventTypeTransferCreated,
		Message:       sql.NullString{String: "transfer accepted for async processing", Valid: true},
		Metadata:      metadata,
	}); err != nil {
		return nil, fmt.Errorf("create transfer transaction event: %w", err)
	}

	if err := s.cache.SetTransactionStatus(ctx, cacheEntryFromTransaction(txRow, nil), cache.TransactionStatusTTL); err != nil {
		return nil, fmt.Errorf("cache transfer transaction status: %w", err)
	}

	if err := s.publisher.PublishTransfer(ctx, queue.TransferMessage{
		TransactionID:     txRow.ID.String(),
		AccountUUID:       account.ID.String(),
		FromAccountID:     req.Session.LegacyAccountID,
		FromAccountNumber: req.Session.AccountNumber,
		ToAccountNumber:   req.ToAccountNumber,
		TransactionPIN:    req.TransactionPIN,
		Amount:            req.Amount,
	}); err != nil {
		now := time.Now().UTC()
		failedRow, updateErr := s.repo.UpdateTransactionStatus(ctx, dbsqlc.UpdateTransactionStatusParams{
			ID:            txRow.ID,
			Status:        statusFailed,
			FailureReason: sql.NullString{String: "queue publish failed", Valid: true},
			ProcessedAt:   sql.NullTime{Time: now, Valid: true},
		})
		if updateErr == nil {
			_ = s.cache.SetTransactionStatus(ctx, cacheEntryFromTransaction(failedRow, &now), cache.TransactionStatusTTL)
		}
		_, _ = s.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
			TransactionID: txRow.ID,
			EventType:     eventTypeTransactionQueueError,
			Message:       sql.NullString{String: "failed to publish transfer job", Valid: true},
			Metadata:      json.RawMessage(`{"reason":"queue_publish_failed","queue":"transfer"}`),
		})
		return nil, fmt.Errorf("publish transfer job: %w", err)
	}

	_, _ = s.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
		TransactionID: txRow.ID,
		EventType:     eventTypeTransferEnqueued,
		Message:       sql.NullString{String: "transfer queued for worker processing", Valid: true},
		Metadata:      json.RawMessage(`{"queue":"transfer"}`),
	})

	return &QRISResult{
		Transaction: txRow,
		Created:     true,
	}, nil
}

func transferReplayMatches(events []dbsqlc.TransactionEvent, toAccountNumber string) bool {
	for _, event := range events {
		if event.EventType != eventTypeTransferCreated {
			continue
		}

		var payload struct {
			ToAccountNumber string `json:"to_account_number"`
		}
		if err := json.Unmarshal(event.Metadata, &payload); err != nil {
			return false
		}
		return payload.ToAccountNumber == toAccountNumber
	}

	return false
}
