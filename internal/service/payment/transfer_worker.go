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
	startedAt := time.Now()

	if _, err := w.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
		TransactionID: transactionID,
		EventType:     "TRANSFER_WORKER_STARTED",
		Message:       sql.NullString{String: "worker started transfer execution", Valid: true},
		Metadata:      json.RawMessage(`{"queue":"transfer"}`),
	}); err != nil {
		return fmt.Errorf("create transfer worker start event: %w", err)
	}

	result, err := w.executor.Transfer(ctx, msg.FromAccountNumber, msg.TransactionPIN, msg.ToAccountNumber, msg.Amount)
	if err != nil {
		logLegacyCall(ctx, w.repo, transactionID, "transfer", false, time.Since(startedAt), err)
		return w.markFailed(ctx, msg, err)
	}

	logLegacyCall(ctx, w.repo, transactionID, "transfer", true, time.Since(startedAt), nil)
	return w.markSucceeded(ctx, msg, result)
}

func (w *TransferWorker) markSucceeded(ctx context.Context, msg queue.TransferMessage, result *legacy.TransferResult) error {
	now := time.Now().UTC()
	tx, err := w.repo.UpdateTransactionStatus(ctx, dbsqlc.UpdateTransactionStatusParams{
		ID:            uuid.MustParse(msg.TransactionID),
		Status:        statusSuccess,
		FailureReason: sql.NullString{},
		ProcessedAt:   sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("mark transfer success: %w", err)
	}

	if err := w.cache.SetTransactionStatus(ctx, cacheEntryFromTransaction(tx, &now), cache.TransactionStatusTTL); err != nil {
		return fmt.Errorf("cache transfer success status: %w", err)
	}

	if msg.AccountUUID != "" && w.balances != nil {
		accountUUID := uuid.MustParse(msg.AccountUUID)
		if err := w.balances.DeleteAccountBalance(ctx, accountUUID); err != nil {
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
		return fmt.Errorf("marshal transfer success metadata: %w", err)
	}

	if _, err := w.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
		TransactionID: tx.ID,
		EventType:     "TRANSFER_SUCCEEDED",
		Message:       sql.NullString{String: "transfer completed successfully", Valid: true},
		Metadata:      metadata,
	}); err != nil {
		return fmt.Errorf("create transfer success event: %w", err)
	}

	return nil
}

func (w *TransferWorker) markFailed(ctx context.Context, msg queue.TransferMessage, cause error) error {
	now := time.Now().UTC()
	failureReason := cause.Error()

	tx, err := w.repo.UpdateTransactionStatus(ctx, dbsqlc.UpdateTransactionStatusParams{
		ID:            uuid.MustParse(msg.TransactionID),
		Status:        statusFailed,
		FailureReason: sql.NullString{String: failureReason, Valid: true},
		ProcessedAt:   sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("mark transfer failed: %w", err)
	}

	if err := w.cache.SetTransactionStatus(ctx, cacheEntryFromTransaction(tx, &now), cache.TransactionStatusTTL); err != nil {
		return fmt.Errorf("cache transfer failed status: %w", err)
	}

	metadata, err := json.Marshal(map[string]any{
		"reason":         failureReason,
		"to_account":     msg.ToAccountNumber,
		"amount":         msg.Amount,
		"transaction_id": msg.TransactionID,
	})
	if err != nil {
		return fmt.Errorf("marshal transfer failure metadata: %w", err)
	}

	if _, err := w.repo.CreateTransactionEvent(ctx, dbsqlc.CreateTransactionEventParams{
		TransactionID: tx.ID,
		EventType:     "TRANSFER_FAILED",
		Message:       sql.NullString{String: "transfer failed during worker execution", Valid: true},
		Metadata:      metadata,
	}); err != nil {
		return fmt.Errorf("create transfer failure event: %w", err)
	}

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

	_, _ = repo.CreateLegacyCallLog(ctx, dbsqlc.CreateLegacyCallLogParams{
		TransactionID: uuid.NullUUID{UUID: transactionID, Valid: true},
		Endpoint:      endpoint,
		Method:        "SOAP",
		StatusCode:    statusCode,
		Success:       success,
		LatencyMs:     int32(latency.Milliseconds()),
		ErrorMessage:  errorMessage,
	})
}
