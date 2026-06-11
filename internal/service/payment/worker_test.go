package payment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/queue"
	"github.com/google/uuid"
)

type fakeWorkerRepo struct {
	updatedTx      dbsqlc.Transaction
	updateErr      error
	updatedArgs    []dbsqlc.UpdateTransactionStatusParams
	createdEvents  []dbsqlc.CreateTransactionEventParams
	legacyLogs     []dbsqlc.CreateLegacyCallLogParams
	createEventErr error
}

func (f *fakeWorkerRepo) UpdateTransactionStatus(ctx context.Context, arg dbsqlc.UpdateTransactionStatusParams) (dbsqlc.Transaction, error) {
	if f.updateErr != nil {
		return dbsqlc.Transaction{}, f.updateErr
	}
	f.updatedArgs = append(f.updatedArgs, arg)

	tx := f.updatedTx
	tx.ID = arg.ID
	tx.Status = arg.Status
	tx.FailureReason = arg.FailureReason
	tx.ProcessedAt = arg.ProcessedAt
	return tx, nil
}

func (f *fakeWorkerRepo) CreateTransactionEvent(ctx context.Context, arg dbsqlc.CreateTransactionEventParams) (dbsqlc.TransactionEvent, error) {
	if f.createEventErr != nil {
		return dbsqlc.TransactionEvent{}, f.createEventErr
	}
	f.createdEvents = append(f.createdEvents, arg)
	return dbsqlc.TransactionEvent{}, nil
}

func (f *fakeWorkerRepo) CreateLegacyCallLog(ctx context.Context, arg dbsqlc.CreateLegacyCallLogParams) (dbsqlc.LegacyCallLog, error) {
	f.legacyLogs = append(f.legacyLogs, arg)
	return dbsqlc.LegacyCallLog{}, nil
}

type fakeQRISExecutor struct {
	result *legacy.QRISPaymentResult
	err    error
}

func (f *fakeQRISExecutor) PayQRIS(ctx context.Context, accountID, merchantCode string, amount int64) (*legacy.QRISPaymentResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

type fakeWorkerStatusCache struct {
	entry cache.TransactionStatusCacheEntry
	ttl   time.Duration
	err   error
}

func (f *fakeWorkerStatusCache) SetTransactionStatus(ctx context.Context, entry cache.TransactionStatusCacheEntry, ttl time.Duration) error {
	if f.err != nil {
		return f.err
	}
	f.entry = entry
	f.ttl = ttl
	return nil
}

type fakeBalanceCache struct {
	deletedAccountID uuid.UUID
	err              error
}

func (f *fakeBalanceCache) DeleteAccountBalance(ctx context.Context, accountID uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	f.deletedAccountID = accountID
	return nil
}

func TestQRISWorkerMarksTransactionSuccess(t *testing.T) {
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
	executor := &fakeQRISExecutor{
		result: &legacy.QRISPaymentResult{
			AccountID:    "LEGACY-ACC-1",
			MerchantCode: "MERCHANT001",
			Amount:       2500,
		},
	}

	worker := NewQRISWorker(repo, statusCache, balanceCache, executor)
	err := worker.HandleQRISPayment(context.Background(), queue.QRISPaymentMessage{
		TransactionID: transactionID.String(),
		AccountUUID:   accountUUID.String(),
		AccountID:     "LEGACY-ACC-1",
		MerchantCode:  "MERCHANT001",
		Amount:        2500,
	})
	if err != nil {
		t.Fatalf("HandleQRISPayment returned error: %v", err)
	}

	if len(repo.updatedArgs) != 1 {
		t.Fatalf("expected one transaction update, got %d", len(repo.updatedArgs))
	}
	if repo.updatedArgs[0].Status != statusSuccess {
		t.Fatalf("expected status %s, got %s", statusSuccess, repo.updatedArgs[0].Status)
	}
	if statusCache.entry.Status != statusSuccess {
		t.Fatalf("expected cached status %s, got %s", statusSuccess, statusCache.entry.Status)
	}
	if len(repo.createdEvents) != 2 {
		t.Fatalf("expected 2 events, got %d", len(repo.createdEvents))
	}
	if repo.createdEvents[1].EventType != eventTypeTransactionSucceeded {
		t.Fatalf("unexpected success event type: %s", repo.createdEvents[1].EventType)
	}
	if balanceCache.deletedAccountID != accountUUID {
		t.Fatalf("expected balance cache invalidation for %s, got %s", accountUUID, balanceCache.deletedAccountID)
	}
	if len(repo.legacyLogs) != 1 {
		t.Fatalf("expected 1 legacy call log, got %d", len(repo.legacyLogs))
	}
	if !repo.legacyLogs[0].Success {
		t.Fatalf("expected success legacy call log")
	}
}

func TestQRISWorkerMarksTransactionFailedOnLegacyError(t *testing.T) {
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
	executor := &fakeQRISExecutor{
		err: errors.New("legacy qris timeout"),
	}

	worker := NewQRISWorker(repo, statusCache, &fakeBalanceCache{}, executor)
	err := worker.HandleQRISPayment(context.Background(), queue.QRISPaymentMessage{
		TransactionID: transactionID.String(),
		AccountID:     "LEGACY-ACC-1",
		MerchantCode:  "MERCHANT001",
		Amount:        2500,
	})
	if err != nil {
		t.Fatalf("HandleQRISPayment returned error: %v", err)
	}

	if len(repo.updatedArgs) != 1 {
		t.Fatalf("expected one transaction update, got %d", len(repo.updatedArgs))
	}
	if repo.updatedArgs[0].Status != statusFailed {
		t.Fatalf("expected status %s, got %s", statusFailed, repo.updatedArgs[0].Status)
	}
	if !repo.updatedArgs[0].FailureReason.Valid {
		t.Fatalf("expected failure reason to be set")
	}
	if statusCache.entry.Status != statusFailed {
		t.Fatalf("expected cached status %s, got %s", statusFailed, statusCache.entry.Status)
	}
	if len(repo.createdEvents) != 2 {
		t.Fatalf("expected 2 events, got %d", len(repo.createdEvents))
	}
	if repo.createdEvents[1].EventType != eventTypeTransactionFailed {
		t.Fatalf("unexpected failure event type: %s", repo.createdEvents[1].EventType)
	}
	if len(repo.legacyLogs) != 1 {
		t.Fatalf("expected 1 legacy call log, got %d", len(repo.legacyLogs))
	}
	if repo.legacyLogs[0].Success {
		t.Fatalf("expected failure legacy call log")
	}
	if !repo.legacyLogs[0].ErrorMessage.Valid {
		t.Fatalf("expected logged error message")
	}
}

func TestQRISWorkerReturnsErrorWhenPersistenceFails(t *testing.T) {
	t.Parallel()

	transactionID := uuid.New()
	repo := &fakeWorkerRepo{
		updateErr: errors.New("db unavailable"),
	}
	worker := NewQRISWorker(repo, &fakeWorkerStatusCache{}, &fakeBalanceCache{}, &fakeQRISExecutor{
		result: &legacy.QRISPaymentResult{
			AccountID:    "LEGACY-ACC-1",
			MerchantCode: "MERCHANT001",
			Amount:       2500,
		},
	})

	err := worker.HandleQRISPayment(context.Background(), queue.QRISPaymentMessage{
		TransactionID: transactionID.String(),
		AccountID:     "LEGACY-ACC-1",
		MerchantCode:  "MERCHANT001",
		Amount:        2500,
	})
	if err == nil {
		t.Fatalf("expected persistence error")
	}
}
