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
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/queue"
	"github.com/google/uuid"
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
	startedAt := time.Now()

	if _, err := w.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
		TransactionID: transactionID,
		EventType:     eventTypeTransactionWorkerStarted,
		Message:       sql.NullString{String: "worker started qris payment execution", Valid: true},
		Metadata:      json.RawMessage(`{"queue":"qris"}`),
	}); err != nil {
		return fmt.Errorf("create worker start event: %w", err)
	}

	result, err := w.executor.PayQRIS(ctx, msg.AccountID, msg.MerchantCode, msg.Amount)
	if err != nil {
		w.logLegacyCall(ctx, transactionID, false, time.Since(startedAt), err)
		return w.markFailed(ctx, msg, err)
	}

	w.logLegacyCall(ctx, transactionID, true, time.Since(startedAt), nil)
	return w.markSucceeded(ctx, msg, result)
}

func (w *QRISWorker) markSucceeded(ctx context.Context, msg queue.QRISPaymentMessage, result *legacy.QRISPaymentResult) error {
	now := time.Now().UTC()
	tx, err := w.repo.UpdateTransactionStatus(ctx, dbsqlc.UpdateTransactionStatusParams{
		ID:            uuid.MustParse(msg.TransactionID),
		Status:        statusSuccess,
		FailureReason: sql.NullString{},
		ProcessedAt:   sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("mark transaction success: %w", err)
	}

	if err := w.cache.SetTransactionStatus(ctx, cache.TransactionStatusCacheEntry{
		ID:          tx.ID.String(),
		Status:      tx.Status,
		RequestedAt: tx.RequestedAt,
		ProcessedAt: &now,
		UpdatedAt:   tx.UpdatedAt,
	}, cache.TransactionStatusTTL); err != nil {
		return fmt.Errorf("cache success transaction status: %w", err)
	}

	if msg.AccountUUID != "" && w.balances != nil {
		accountUUID := uuid.MustParse(msg.AccountUUID)
		if err := w.balances.DeleteAccountBalance(ctx, accountUUID); err != nil {
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
		return fmt.Errorf("marshal success event metadata: %w", err)
	}

	if _, err := w.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
		TransactionID: tx.ID,
		EventType:     eventTypeTransactionSucceeded,
		Message:       sql.NullString{String: "qris payment completed successfully", Valid: true},
		Metadata:      metadata,
	}); err != nil {
		return fmt.Errorf("create success event: %w", err)
	}

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

	_, _ = w.repo.CreateLegacyCallLog(ctx, dbsqlc.CreateLegacyCallLogParams{
		TransactionID: uuid.NullUUID{UUID: transactionID, Valid: true},
		Endpoint:      "qris",
		Method:        "SOAP",
		StatusCode:    statusCode,
		Success:       success,
		LatencyMs:     int32(latency.Milliseconds()),
		ErrorMessage:  errorMessage,
	})
}

func (w *QRISWorker) markFailed(ctx context.Context, msg queue.QRISPaymentMessage, cause error) error {
	now := time.Now().UTC()
	failureReason := cause.Error()

	tx, err := w.repo.UpdateTransactionStatus(ctx, dbsqlc.UpdateTransactionStatusParams{
		ID:            uuid.MustParse(msg.TransactionID),
		Status:        statusFailed,
		FailureReason: sql.NullString{String: failureReason, Valid: true},
		ProcessedAt:   sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("mark transaction failed: %w", err)
	}

	if err := w.cache.SetTransactionStatus(ctx, cache.TransactionStatusCacheEntry{
		ID:          tx.ID.String(),
		Status:      tx.Status,
		RequestedAt: tx.RequestedAt,
		ProcessedAt: &now,
		UpdatedAt:   tx.UpdatedAt,
	}, cache.TransactionStatusTTL); err != nil {
		return fmt.Errorf("cache failed transaction status: %w", err)
	}

	metadata, err := json.Marshal(map[string]any{
		"reason":         failureReason,
		"merchant_code":  msg.MerchantCode,
		"amount":         msg.Amount,
		"transaction_id": msg.TransactionID,
	})
	if err != nil {
		return fmt.Errorf("marshal failure event metadata: %w", err)
	}

	if _, err := w.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
		TransactionID: tx.ID,
		EventType:     eventTypeTransactionFailed,
		Message:       sql.NullString{String: "qris payment failed during worker execution", Valid: true},
		Metadata:      metadata,
	}); err != nil {
		return fmt.Errorf("create failure event: %w", err)
	}

	return nil
}
