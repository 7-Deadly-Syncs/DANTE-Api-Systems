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

const (
	statusSuccess = "SUCCESS"

	eventTypeTransactionWorkerStarted = "TRANSACTION_WORKER_STARTED"
	eventTypeTransactionSucceeded     = "TRANSACTION_SUCCEEDED"
	eventTypeTransactionFailed        = "TRANSACTION_FAILED"
)

// QRISExecutor executes QRIS payments against the legacy adapter.
type QRISExecutor interface {
	PayQRIS(ctx context.Context, accountID, merchantCode string, amount int64) (*legacy.QRISPaymentResult, error)
}

// QRISWorker finalizes queued QRIS transactions by calling legacy and persisting the final result.
type QRISWorker struct {
	repo     WorkerRepository
	cache    StatusCache
	balances BalanceCache
	executor QRISExecutor
}

// WorkerRepository describes the persistence operations needed by the QRIS worker.
type WorkerRepository interface {
	UpdateTransactionStatus(ctx context.Context, arg dbsqlc.UpdateTransactionStatusParams) (dbsqlc.Transaction, error)
	CreateTransactionEvent(ctx context.Context, arg dbsqlc.CreateTransactionEventParams) (dbsqlc.TransactionEvent, error)
	CreateLegacyCallLog(ctx context.Context, arg dbsqlc.CreateLegacyCallLogParams) (dbsqlc.LegacyCallLog, error)
}

// BalanceCache describes the account balance cache invalidation needed by async payment workers.
type BalanceCache interface {
	DeleteAccountBalance(ctx context.Context, accountID uuid.UUID) error
}

// NewQRISWorker constructs a QRIS worker.
func NewQRISWorker(repo WorkerRepository, cacheClient StatusCache, balanceCache BalanceCache, executor QRISExecutor) *QRISWorker {
	return &QRISWorker{
		repo:     repo,
		cache:    cacheClient,
		balances: balanceCache,
		executor: executor,
	}
}

// HandleQRISPayment executes a QRIS payment job and stores the final local transaction outcome.
func (w *QRISWorker) HandleQRISPayment(ctx context.Context, msg queue.QRISPaymentMessage) error {
	transactionID := uuid.MustParse(msg.TransactionID)
	ctx, span := tracing.StartInternalSpan(ctx, "service.payment.worker", "worker.qris.handle",
		attribute.String("transaction.id", transactionID.String()),
		attribute.String("account.id", msg.AccountUUID),
		attribute.String("merchant.id", msg.MerchantID),
		attribute.Int64("transaction.amount", msg.Amount),
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
		attribute.String("transaction.event_type", eventTypeTransactionWorkerStarted),
	)
	if _, err := w.repo.CreateTransactionEvent(eventCtx, dbsqlc.CreateTransactionEventParams{
		TransactionID: transactionID,
		EventType:     eventTypeTransactionWorkerStarted,
		Message:       sql.NullString{String: "worker started qris payment execution", Valid: true},
		Metadata:      json.RawMessage(`{"queue":"qris"}`),
	}); err != nil {
		tracing.EndSpan(eventSpan, err)
		spanErr = err
		return fmt.Errorf("create worker start event: %w", err)
	}
	tracing.EndSpan(eventSpan, nil)

	legacyCtx, legacySpan := tracing.StartClientSpan(ctx, "legacy", "legacy.pay_qris",
		attribute.String("legacy.system", "banking"),
		attribute.String("legacy.operation", "qris"),
		attribute.Int64("transaction.amount", msg.Amount),
	)
	result, err := w.executor.PayQRIS(legacyCtx, msg.AccountID, msg.MerchantCode, msg.Amount)
	tracing.EndSpan(legacySpan, err)
	if err != nil {
		w.logLegacyCall(ctx, transactionID, false, time.Since(startedAt), err)
		markErr := w.markFailed(ctx, msg, err)
		if markErr != nil {
			spanErr = markErr
			return markErr
		}
		span.SetAttributes(attribute.String("worker.result", "failed"))
		span.RecordError(err)
		return nil
	}

	w.logLegacyCall(ctx, transactionID, true, time.Since(startedAt), nil)
	if err := w.markSucceeded(ctx, msg, result); err != nil {
		spanErr = err
		return err
	}
	span.SetAttributes(attribute.String("worker.result", "success"))
	return nil
}

func (w *QRISWorker) markSucceeded(ctx context.Context, msg queue.QRISPaymentMessage, result *legacy.QRISPaymentResult) error {
	ctx, span := tracing.StartInternalSpan(ctx, "service.payment.worker", "worker.qris.mark_succeeded",
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
		return fmt.Errorf("mark transaction success: %w", err)
	}

	if err := w.cache.SetTransactionStatus(ctx, cache.TransactionStatusCacheEntry{
		ID:          tx.ID.String(),
		Status:      tx.Status,
		RequestedAt: tx.RequestedAt,
		ProcessedAt: &now,
		UpdatedAt:   tx.UpdatedAt,
	}, cache.TransactionStatusTTL); err != nil {
		spanErr = err
		return fmt.Errorf("cache success transaction status: %w", err)
	}

	if msg.AccountUUID != "" && w.balances != nil {
		accountUUID := uuid.MustParse(msg.AccountUUID)
		if err := w.balances.DeleteAccountBalance(ctx, accountUUID); err != nil {
			spanErr = err
			return fmt.Errorf("invalidate account balance cache: %w", err)
		}
	}

	metadata, err := json.Marshal(map[string]any{
		"account_id":     result.AccountID,
		"merchant_code":  result.MerchantCode,
		"amount":         result.Amount,
		"transaction_id": msg.TransactionID,
	})
	if err != nil {
		spanErr = err
		return fmt.Errorf("marshal success event metadata: %w", err)
	}

	eventCtx, eventSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.create transaction_event",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "INSERT"),
		attribute.String("db.sql.table", "transaction_events"),
		attribute.String("transaction.id", tx.ID.String()),
		attribute.String("transaction.event_type", eventTypeTransactionSucceeded),
	)
	if _, err := w.repo.CreateTransactionEvent(eventCtx, dbsqlc.CreateTransactionEventParams{
		TransactionID: tx.ID,
		EventType:     eventTypeTransactionSucceeded,
		Message:       sql.NullString{String: "qris payment completed successfully", Valid: true},
		Metadata:      metadata,
	}); err != nil {
		tracing.EndSpan(eventSpan, err)
		spanErr = err
		return fmt.Errorf("create success event: %w", err)
	}
	tracing.EndSpan(eventSpan, nil)

	return nil
}

func (w *QRISWorker) logLegacyCall(ctx context.Context, transactionID uuid.UUID, success bool, latency time.Duration, callErr error) {
	if w.repo == nil {
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
		attribute.Bool("legacy.call_success", success),
		attribute.Int64("legacy.latency_ms", latency.Milliseconds()),
	)
	_, err := w.repo.CreateLegacyCallLog(logCtx, dbsqlc.CreateLegacyCallLogParams{
		TransactionID: uuid.NullUUID{UUID: transactionID, Valid: true},
		Endpoint:      "qris",
		Method:        "SOAP",
		StatusCode:    statusCode,
		Success:       success,
		LatencyMs:     int32(latency.Milliseconds()),
		ErrorMessage:  errorMessage,
	})
	tracing.EndSpan(logSpan, err)
}

func (w *QRISWorker) markFailed(ctx context.Context, msg queue.QRISPaymentMessage, cause error) error {
	ctx, span := tracing.StartInternalSpan(ctx, "service.payment.worker", "worker.qris.mark_failed",
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
		return fmt.Errorf("mark transaction failed: %w", err)
	}

	if err := w.cache.SetTransactionStatus(ctx, cache.TransactionStatusCacheEntry{
		ID:          tx.ID.String(),
		Status:      tx.Status,
		RequestedAt: tx.RequestedAt,
		ProcessedAt: &now,
		UpdatedAt:   tx.UpdatedAt,
	}, cache.TransactionStatusTTL); err != nil {
		spanErr = err
		return fmt.Errorf("cache failed transaction status: %w", err)
	}

	metadata, err := json.Marshal(map[string]any{
		"reason":         failureReason,
		"merchant_code":  msg.MerchantCode,
		"amount":         msg.Amount,
		"transaction_id": msg.TransactionID,
	})
	if err != nil {
		spanErr = err
		return fmt.Errorf("marshal failure event metadata: %w", err)
	}

	eventCtx, eventSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.create transaction_event",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "INSERT"),
		attribute.String("db.sql.table", "transaction_events"),
		attribute.String("transaction.id", tx.ID.String()),
		attribute.String("transaction.event_type", eventTypeTransactionFailed),
	)
	if _, err := w.repo.CreateTransactionEvent(eventCtx, dbsqlc.CreateTransactionEventParams{
		TransactionID: tx.ID,
		EventType:     eventTypeTransactionFailed,
		Message:       sql.NullString{String: "qris payment failed during worker execution", Valid: true},
		Metadata:      metadata,
	}); err != nil {
		tracing.EndSpan(eventSpan, err)
		spanErr = err
		return fmt.Errorf("create failure event: %w", err)
	}
	tracing.EndSpan(eventSpan, nil)

	return nil
}
