package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/queue"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

// TransferExecutor executes transfers against the legacy adapter.
type TransferExecutor interface {
	Transfer(ctx context.Context, fromAccount, pin, toAccount string, amount int64) (*legacy.TransferResult, error)
}

// TransferWorker finalizes queued transfer transactions by calling legacy and persisting the final result.
type TransferWorker struct {
	repo     WorkerRepository
	cache    StatusCache
	balances BalanceCache
	executor TransferExecutor
}

// NewTransferWorker constructs a transfer worker.
func NewTransferWorker(repo WorkerRepository, cacheClient StatusCache, balanceCache BalanceCache, executor TransferExecutor) *TransferWorker {
	return &TransferWorker{
		repo:     repo,
		cache:    cacheClient,
		balances: balanceCache,
		executor: executor,
	}
}

// HandleTransfer executes a transfer job and stores the final local transaction outcome.
func (w *TransferWorker) HandleTransfer(ctx context.Context, msg queue.TransferMessage) error {
	transactionID := uuid.MustParse(msg.TransactionID)
	ctx, span := tracing.StartInternalSpan(ctx, "service.payment.worker", "worker.transfer.handle",
		attribute.String("transaction.id", transactionID.String()),
		attribute.String("account.id", msg.AccountUUID),
		attribute.Int64("transaction.amount", msg.Amount),
		attribute.Bool("transaction.pin_present", msg.TransactionPIN != ""),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	startedAt := time.Now()

	eventCtx, eventSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.create transaction_event",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "INSERT"),
		attribute.String("db.sql.table", "transaction_events"),
		attribute.String("transaction.id", transactionID.String()),
		attribute.String("transaction.event_type", "TRANSFER_WORKER_STARTED"),
	)
	if _, err := w.repo.CreateTransactionEvent(eventCtx, dbsqlc.CreateTransactionEventParams{
		TransactionID: transactionID,
		EventType:     "TRANSFER_WORKER_STARTED",
		Message:       sql.NullString{String: "worker started transfer execution", Valid: true},
		Metadata:      json.RawMessage(`{"queue":"transfer"}`),
	}); err != nil {
		tracing.EndSpan(eventSpan, err)
		spanErr = err
		return fmt.Errorf("create transfer worker start event: %w", err)
	}
	tracing.EndSpan(eventSpan, nil)

	legacyCtx, legacySpan := tracing.StartClientSpan(ctx, "legacy", "legacy.transfer",
		attribute.String("legacy.system", "banking"),
		attribute.String("legacy.operation", "transfer"),
		attribute.Int64("transaction.amount", msg.Amount),
	)
	result, err := w.executor.Transfer(legacyCtx, msg.FromAccountID, msg.TransactionPIN, msg.ToAccountNumber, msg.Amount)
	tracing.EndSpan(legacySpan, err)
	if err != nil {
		logLegacyCall(ctx, w.repo, transactionID, "transfer", false, time.Since(startedAt), err)
		markErr := w.markFailed(ctx, msg, err)
		if markErr != nil {
			spanErr = markErr
			return markErr
		}
		span.SetAttributes(attribute.String("worker.result", "failed"))
		span.RecordError(err)
		return nil
	}

	logLegacyCall(ctx, w.repo, transactionID, "transfer", true, time.Since(startedAt), nil)
	if err := w.markSucceeded(ctx, msg, result); err != nil {
		spanErr = err
		return err
	}
	span.SetAttributes(attribute.String("worker.result", "success"))
	return nil
}

func (w *TransferWorker) markSucceeded(ctx context.Context, msg queue.TransferMessage, result *legacy.TransferResult) error {
	ctx, span := tracing.StartInternalSpan(ctx, "service.payment.worker", "worker.transfer.mark_succeeded",
		attribute.String("transaction.id", msg.TransactionID),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	now := time.Now().UTC()
	updateCtx, updateSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.update transaction_status",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "UPDATE"),
		attribute.String("db.sql.table", "transactions"),
		attribute.String("transaction.id", msg.TransactionID),
		attribute.String("transaction.status", statusSuccess),
	)
	tx, err := w.repo.UpdateTransactionStatus(updateCtx, dbsqlc.UpdateTransactionStatusParams{
		ID:            uuid.MustParse(msg.TransactionID),
		Status:        statusSuccess,
		FailureReason: sql.NullString{},
		ProcessedAt:   sql.NullTime{Time: now, Valid: true},
	})
	tracing.EndSpan(updateSpan, err)
	if err != nil {
		spanErr = err
		return fmt.Errorf("mark transfer success: %w", err)
	}

	if err := w.cache.SetTransactionStatus(ctx, cacheEntryFromTransaction(tx, &now), cache.TransactionStatusTTL); err != nil {
		spanErr = err
		return fmt.Errorf("cache transfer success status: %w", err)
	}

	if msg.AccountUUID != "" && w.balances != nil {
		accountUUID := uuid.MustParse(msg.AccountUUID)
		if err := w.balances.DeleteAccountBalance(ctx, accountUUID); err != nil {
			spanErr = err
			return fmt.Errorf("invalidate transfer balance cache: %w", err)
		}
	}

	metadata, err := json.Marshal(map[string]any{
		"from_account":   result.FromAccount,
		"to_account":     result.ToAccount,
		"amount":         result.Amount,
		"transaction_id": msg.TransactionID,
	})
	if err != nil {
		spanErr = err
		return fmt.Errorf("marshal transfer success metadata: %w", err)
	}

	eventCtx, eventSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.create transaction_event",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "INSERT"),
		attribute.String("db.sql.table", "transaction_events"),
		attribute.String("transaction.id", tx.ID.String()),
		attribute.String("transaction.event_type", "TRANSFER_SUCCEEDED"),
	)
	if _, err := w.repo.CreateTransactionEvent(eventCtx, dbsqlc.CreateTransactionEventParams{
		TransactionID: tx.ID,
		EventType:     "TRANSFER_SUCCEEDED",
		Message:       sql.NullString{String: "transfer completed successfully", Valid: true},
		Metadata:      metadata,
	}); err != nil {
		tracing.EndSpan(eventSpan, err)
		spanErr = err
		return fmt.Errorf("create transfer success event: %w", err)
	}
	tracing.EndSpan(eventSpan, nil)

	return nil
}

func (w *TransferWorker) markFailed(ctx context.Context, msg queue.TransferMessage, cause error) error {
	ctx, span := tracing.StartInternalSpan(ctx, "service.payment.worker", "worker.transfer.mark_failed",
		attribute.String("transaction.id", msg.TransactionID),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	now := time.Now().UTC()
	failureReason := cause.Error()

	updateCtx, updateSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.update transaction_status",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "UPDATE"),
		attribute.String("db.sql.table", "transactions"),
		attribute.String("transaction.id", msg.TransactionID),
		attribute.String("transaction.status", statusFailed),
	)
	tx, err := w.repo.UpdateTransactionStatus(updateCtx, dbsqlc.UpdateTransactionStatusParams{
		ID:            uuid.MustParse(msg.TransactionID),
		Status:        statusFailed,
		FailureReason: sql.NullString{String: failureReason, Valid: true},
		ProcessedAt:   sql.NullTime{Time: now, Valid: true},
	})
	tracing.EndSpan(updateSpan, err)
	if err != nil {
		spanErr = err
		return fmt.Errorf("mark transfer failed: %w", err)
	}

	if err := w.cache.SetTransactionStatus(ctx, cacheEntryFromTransaction(tx, &now), cache.TransactionStatusTTL); err != nil {
		spanErr = err
		return fmt.Errorf("cache transfer failed status: %w", err)
	}

	metadata, err := json.Marshal(map[string]any{
		"reason":         failureReason,
		"to_account":     msg.ToAccountNumber,
		"amount":         msg.Amount,
		"transaction_id": msg.TransactionID,
	})
	if err != nil {
		spanErr = err
		return fmt.Errorf("marshal transfer failure metadata: %w", err)
	}

	eventCtx, eventSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.create transaction_event",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "INSERT"),
		attribute.String("db.sql.table", "transaction_events"),
		attribute.String("transaction.id", tx.ID.String()),
		attribute.String("transaction.event_type", "TRANSFER_FAILED"),
	)
	if _, err := w.repo.CreateTransactionEvent(eventCtx, dbsqlc.CreateTransactionEventParams{
		TransactionID: tx.ID,
		EventType:     "TRANSFER_FAILED",
		Message:       sql.NullString{String: "transfer failed during worker execution", Valid: true},
		Metadata:      metadata,
	}); err != nil {
		tracing.EndSpan(eventSpan, err)
		spanErr = err
		return fmt.Errorf("create transfer failure event: %w", err)
	}
	tracing.EndSpan(eventSpan, nil)

	return nil
}

func logLegacyCall(ctx context.Context, repo WorkerRepository, transactionID uuid.UUID, endpoint string, success bool, latency time.Duration, callErr error) {
	if repo == nil {
		return
	}

	statusCode := sql.NullInt32{}
	errorMessage := sql.NullString{}
	if callErr != nil {
		errorMessage = sql.NullString{String: callErr.Error(), Valid: true}
	} else {
		statusCode = sql.NullInt32{Int32: 200, Valid: true}
	}

	logCtx, logSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.create legacy_call_log",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "INSERT"),
		attribute.String("db.sql.table", "legacy_call_logs"),
		attribute.String("transaction.id", transactionID.String()),
		attribute.String("legacy.endpoint", endpoint),
		attribute.Bool("legacy.call_success", success),
		attribute.Int64("legacy.latency_ms", latency.Milliseconds()),
	)
	_, err := repo.CreateLegacyCallLog(logCtx, dbsqlc.CreateLegacyCallLogParams{
		TransactionID: uuid.NullUUID{UUID: transactionID, Valid: true},
		Endpoint:      endpoint,
		Method:        "SOAP",
		StatusCode:    statusCode,
		Success:       success,
		LatencyMs:     int32(latency.Milliseconds()),
		ErrorMessage:  errorMessage,
	})
	tracing.EndSpan(logSpan, err)
}
