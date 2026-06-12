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
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/queue"
	authservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/auth"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
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
	ctx, span := tracing.StartInternalSpan(ctx, "service.payment", "payment.transfer.create_transaction",
		attribute.Int64("transaction.amount", req.Amount),
		attribute.Bool("transaction.idempotency_key_present", req.IdempotencyKey != ""),
		attribute.Bool("transaction.pin_present", req.TransactionPIN != ""),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, sql.ErrNoRows, ErrIdempotencyConflict, ErrAccountNotProvisioned)
	}()

	idempotencyCtx, idempotencySpan := tracing.StartClientSpan(ctx, "postgres", "postgres.get transaction_by_idempotency_key",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "SELECT"),
		attribute.String("db.sql.table", "transactions"),
	)
	existing, err := s.repo.GetTransactionByIdempotencyKey(idempotencyCtx, req.IdempotencyKey)
	tracing.EndSpan(idempotencySpan, err, sql.ErrNoRows)
	if err == nil {
		accountCtx, accountSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.get account_by_number",
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.sql.table", "accounts"),
		)
		account, accountErr := s.repo.GetAccountByNumber(accountCtx, req.Session.AccountNumber)
		tracing.EndSpan(accountSpan, accountErr)
		if accountErr != nil {
			spanErr = accountErr
			return nil, fmt.Errorf("load source account for idempotency comparison: %w", accountErr)
		}

		if existing.AccountID != account.ID || existing.MerchantID.Valid || existing.Amount != req.Amount {
			span.SetAttributes(attribute.String("payment.result", "idempotency_conflict"))
			spanErr = ErrIdempotencyConflict
			return nil, ErrIdempotencyConflict
		}

		eventsCtx, eventsSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.list transaction_events",
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "SELECT"),
			attribute.String("db.sql.table", "transaction_events"),
			attribute.String("transaction.id", existing.ID.String()),
		)
		events, eventsErr := s.repo.ListTransactionEventsByTransactionID(eventsCtx, existing.ID)
		tracing.EndSpan(eventsSpan, eventsErr)
		if eventsErr != nil {
			spanErr = eventsErr
			return nil, fmt.Errorf("load transaction events for idempotency comparison: %w", eventsErr)
		}

		if !transferReplayMatches(events, req.ToAccountNumber) {
			span.SetAttributes(attribute.String("payment.result", "idempotency_conflict"))
			spanErr = ErrIdempotencyConflict
			return nil, ErrIdempotencyConflict
		}

		span.SetAttributes(
			attribute.String("payment.result", "idempotency_replay"),
			attribute.String("transaction.id", existing.ID.String()),
			attribute.String("transaction.status", existing.Status),
		)
		return &QRISResult{
			Transaction: existing,
			Created:     false,
		}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		spanErr = err
		return nil, fmt.Errorf("lookup idempotency key: %w", err)
	}

	accountCtx, accountSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.get account_by_number",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "SELECT"),
		attribute.String("db.sql.table", "accounts"),
	)
	account, err := s.repo.GetAccountByNumber(accountCtx, req.Session.AccountNumber)
	tracing.EndSpan(accountSpan, err, sql.ErrNoRows)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			span.SetAttributes(attribute.String("payment.result", "account_not_provisioned"))
			spanErr = ErrAccountNotProvisioned
			return nil, ErrAccountNotProvisioned
		}
		spanErr = err
		return nil, fmt.Errorf("load local source account: %w", err)
	}

	createCtx, createSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.create transfer_transaction",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "INSERT"),
		attribute.String("db.sql.table", "transactions"),
		attribute.String("account.id", account.ID.String()),
	)
	txRow, err := s.repo.CreateTransaction(createCtx, dbsqlc.CreateTransactionParams{
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
	tracing.EndSpan(createSpan, err)
	if err != nil {
		spanErr = err
		return nil, fmt.Errorf("create transfer transaction: %w", err)
	}
	span.SetAttributes(
		attribute.String("transaction.id", txRow.ID.String()),
		attribute.String("transaction.status", txRow.Status),
	)

	metadata, err := json.Marshal(map[string]any{
		"transaction_type":  "transfer",
		"to_account_number": req.ToAccountNumber,
		"amount":            req.Amount,
		"account_id":        account.ID.String(),
	})
	if err != nil {
		spanErr = err
		return nil, fmt.Errorf("marshal transfer event metadata: %w", err)
	}

	eventCtx, eventSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.create transaction_event",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "INSERT"),
		attribute.String("db.sql.table", "transaction_events"),
		attribute.String("transaction.id", txRow.ID.String()),
		attribute.String("transaction.event_type", eventTypeTransferCreated),
	)
	if _, err := s.repo.CreateTransactionEvent(eventCtx, dbsqlc.CreateTransactionEventParams{
		TransactionID: txRow.ID,
		EventType:     eventTypeTransferCreated,
		Message:       sql.NullString{String: "transfer accepted for async processing", Valid: true},
		Metadata:      metadata,
	}); err != nil {
		tracing.EndSpan(eventSpan, err)
		spanErr = err
		return nil, fmt.Errorf("create transfer transaction event: %w", err)
	}
	tracing.EndSpan(eventSpan, nil)

	if err := s.cache.SetTransactionStatus(ctx, cacheEntryFromTransaction(txRow, nil), cache.TransactionStatusTTL); err != nil {
		spanErr = err
		return nil, fmt.Errorf("cache transfer transaction status: %w", err)
	}

	if err := s.publisher.PublishTransfer(ctx, queue.TransferMessage{
		TransactionID:     txRow.ID.String(),
		AccountUUID:       account.ID.String(),
		FromAccountID:     req.Session.AccountID,
		FromAccountNumber: req.Session.AccountNumber,
		ToAccountNumber:   req.ToAccountNumber,
		TransactionPIN:    req.TransactionPIN,
		Amount:            req.Amount,
	}); err != nil {
		span.SetAttributes(attribute.String("payment.result", "queue_publish_failed"))
		now := time.Now().UTC()
		updateCtx, updateSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.update transaction_status",
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "UPDATE"),
			attribute.String("db.sql.table", "transactions"),
			attribute.String("transaction.id", txRow.ID.String()),
			attribute.String("transaction.status", statusFailed),
		)
		failedRow, updateErr := s.repo.UpdateTransactionStatus(updateCtx, dbsqlc.UpdateTransactionStatusParams{
			ID:            txRow.ID,
			Status:        statusFailed,
			FailureReason: sql.NullString{String: "queue publish failed", Valid: true},
			ProcessedAt:   sql.NullTime{Time: now, Valid: true},
		})
		tracing.EndSpan(updateSpan, updateErr)
		if updateErr == nil {
			_ = s.cache.SetTransactionStatus(ctx, cacheEntryFromTransaction(failedRow, &now), cache.TransactionStatusTTL)
		}
		queueErrCtx, queueErrSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.create transaction_event",
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "INSERT"),
			attribute.String("db.sql.table", "transaction_events"),
			attribute.String("transaction.id", txRow.ID.String()),
			attribute.String("transaction.event_type", eventTypeTransactionQueueError),
		)
		_, queueEventErr := s.repo.CreateTransactionEvent(queueErrCtx, dbsqlc.CreateTransactionEventParams{
			TransactionID: txRow.ID,
			EventType:     eventTypeTransactionQueueError,
			Message:       sql.NullString{String: "failed to publish transfer job", Valid: true},
			Metadata:      json.RawMessage(`{"reason":"queue_publish_failed","queue":"transfer"}`),
		})
		tracing.EndSpan(queueErrSpan, queueEventErr)
		spanErr = err
		return nil, fmt.Errorf("publish transfer job: %w", err)
	}

	enqueuedCtx, enqueuedSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.create transaction_event",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "INSERT"),
		attribute.String("db.sql.table", "transaction_events"),
		attribute.String("transaction.id", txRow.ID.String()),
		attribute.String("transaction.event_type", eventTypeTransferEnqueued),
	)
	_, enqueueEventErr := s.repo.CreateTransactionEvent(enqueuedCtx, dbsqlc.CreateTransactionEventParams{
		TransactionID: txRow.ID,
		EventType:     eventTypeTransferEnqueued,
		Message:       sql.NullString{String: "transfer queued for worker processing", Valid: true},
		Metadata:      json.RawMessage(`{"queue":"transfer"}`),
	})
	tracing.EndSpan(enqueuedSpan, enqueueEventErr)

	span.SetAttributes(attribute.String("payment.result", "created_and_enqueued"))
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
