package payment

import (
	"context"
	"errors"
	"testing"
	"time"

	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/queue"
	"github.com/google/uuid"
)

type fakeTransferExecutor struct {
	result         *legacy.TransferResult
	err            error
	gotFromAccount string
	gotPIN         string
	gotToAccount   string
	gotAmount      int64
}

func (f *fakeTransferExecutor) Transfer(ctx context.Context, fromAccount, pin, toAccount string, amount int64) (*legacy.TransferResult, error) {
	f.gotFromAccount = fromAccount
	f.gotPIN = pin
	f.gotToAccount = toAccount
	f.gotAmount = amount
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestTransferWorkerUsesAccountNumberForLegacyExecution(t *testing.T) {
	t.Parallel()

	transactionID := uuid.New()
	now := time.Now().UTC()
	repo := &fakeWorkerRepo{
		updatedTx: dbsqlc.Transaction{
			ID:          transactionID,
			RequestedAt: now.Add(-1 * time.Minute),
			UpdatedAt:   now,
		},
	}
	statusCache := &fakeWorkerStatusCache{}
	balanceCache := &fakeBalanceCache{}
	accountUUID := uuid.New()
	executor := &fakeTransferExecutor{
		result: &legacy.TransferResult{
			FromAccount: "100000001",
			ToAccount:   "200000002",
			Amount:      5000,
		},
	}

	worker := NewTransferWorker(repo, statusCache, balanceCache, executor)
	err := worker.HandleTransfer(context.Background(), queue.TransferMessage{
		TransactionID:     transactionID.String(),
		AccountUUID:       accountUUID.String(),
		FromAccountID:     "LEGACY-ID-1",
		FromAccountNumber: "100000001",
		ToAccountNumber:   "200000002",
		TransactionPIN:    "123456",
		Amount:            5000,
	})
	if err != nil {
		t.Fatalf("HandleTransfer returned error: %v", err)
	}

	if executor.gotFromAccount != "100000001" {
		t.Fatalf("expected transfer executor to use account number, got %q", executor.gotFromAccount)
	}
	if executor.gotToAccount != "200000002" {
		t.Fatalf("expected destination account to be preserved, got %q", executor.gotToAccount)
	}
	if executor.gotPIN != "123456" {
		t.Fatalf("expected pin to be preserved, got %q", executor.gotPIN)
	}
	if executor.gotAmount != 5000 {
		t.Fatalf("expected amount 5000, got %d", executor.gotAmount)
	}
	if len(repo.updatedArgs) != 1 || repo.updatedArgs[0].Status != statusSuccess {
		t.Fatalf("expected successful transaction update, got %+v", repo.updatedArgs)
	}
	if balanceCache.deletedAccountID != accountUUID {
		t.Fatalf("expected balance cache invalidation for %s, got %s", accountUUID, balanceCache.deletedAccountID)
	}
}

func TestTransferWorkerMarksFailureOnLegacyError(t *testing.T) {
	t.Parallel()

	transactionID := uuid.New()
	now := time.Now().UTC()
	repo := &fakeWorkerRepo{
		updatedTx: dbsqlc.Transaction{
			ID:          transactionID,
			RequestedAt: now.Add(-1 * time.Minute),
			UpdatedAt:   now,
		},
	}
	statusCache := &fakeWorkerStatusCache{}
	executor := &fakeTransferExecutor{
		err: errors.New("legacy transfer timeout"),
	}

	worker := NewTransferWorker(repo, statusCache, &fakeBalanceCache{}, executor)
	err := worker.HandleTransfer(context.Background(), queue.TransferMessage{
		TransactionID:     transactionID.String(),
		FromAccountNumber: "100000001",
		ToAccountNumber:   "200000002",
		TransactionPIN:    "123456",
		Amount:            5000,
	})
	if err != nil {
		t.Fatalf("HandleTransfer returned error: %v", err)
	}

	if executor.gotFromAccount != "100000001" {
		t.Fatalf("expected transfer executor to use account number, got %q", executor.gotFromAccount)
	}
	if len(repo.updatedArgs) != 1 || repo.updatedArgs[0].Status != statusFailed {
		t.Fatalf("expected failed transaction update, got %+v", repo.updatedArgs)
	}
	if !repo.updatedArgs[0].FailureReason.Valid {
		t.Fatalf("expected failure reason to be stored")
	}
}
