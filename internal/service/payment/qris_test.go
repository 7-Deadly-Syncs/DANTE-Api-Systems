package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/queue"
	authservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/auth"
	"github.com/google/uuid"
)

type fakeRepo struct {
	account       dbsqlc.Account
	accountErr    error
	merchant      dbsqlc.Merchant
	merchantErr   error
	existingTx    dbsqlc.Transaction
	existingTxErr error
	createdTx     dbsqlc.Transaction
	createTxErr   error
	createdEvents []dbsqlc.CreateTransactionEventParams
	listedEvents  []dbsqlc.TransactionEvent
	updatedTx     dbsqlc.Transaction
}

func (f *fakeRepo) GetAccountByNumber(ctx context.Context, accountNumber string) (dbsqlc.Account, error) {
	return f.account, f.accountErr
}

func (f *fakeRepo) GetMerchantByID(ctx context.Context, id uuid.UUID) (dbsqlc.Merchant, error) {
	return f.merchant, f.merchantErr
}

func (f *fakeRepo) GetTransactionByIdempotencyKey(ctx context.Context, idempotencyKey string) (dbsqlc.Transaction, error) {
	return f.existingTx, f.existingTxErr
}

func (f *fakeRepo) CreateTransaction(ctx context.Context, arg dbsqlc.CreateTransactionParams) (dbsqlc.Transaction, error) {
	if f.createTxErr != nil {
		return dbsqlc.Transaction{}, f.createTxErr
	}
	tx := f.createdTx
	tx.UserID = arg.UserID
	tx.MerchantID = arg.MerchantID
	tx.AccountID = arg.AccountID
	tx.Amount = arg.Amount
	tx.Status = arg.Status
	tx.IdempotencyKey = arg.IdempotencyKey
	return tx, nil
}

func (f *fakeRepo) CreateTransactionEvent(ctx context.Context, arg dbsqlc.CreateTransactionEventParams) (dbsqlc.TransactionEvent, error) {
	f.createdEvents = append(f.createdEvents, arg)
	return dbsqlc.TransactionEvent{}, nil
}

func (f *fakeRepo) ListTransactionEventsByTransactionID(ctx context.Context, transactionID uuid.UUID) ([]dbsqlc.TransactionEvent, error) {
	if len(f.listedEvents) > 0 {
		return f.listedEvents, nil
	}

	events := make([]dbsqlc.TransactionEvent, 0, len(f.createdEvents))
	for _, event := range f.createdEvents {
		events = append(events, dbsqlc.TransactionEvent{
			TransactionID: event.TransactionID,
			EventType:     event.EventType,
			Message:       event.Message,
			Metadata:      event.Metadata,
		})
	}
	return events, nil
}

func (f *fakeRepo) UpdateTransactionStatus(ctx context.Context, arg dbsqlc.UpdateTransactionStatusParams) (dbsqlc.Transaction, error) {
	tx := f.updatedTx
	tx.ID = arg.ID
	tx.Status = arg.Status
	tx.FailureReason = arg.FailureReason
	tx.ProcessedAt = arg.ProcessedAt
	return tx, nil
}

type fakePublisher struct {
	message         queue.QRISPaymentMessage
	transferMessage queue.TransferMessage
	err             error
}

func (f *fakePublisher) PublishQRISPayment(ctx context.Context, msg queue.QRISPaymentMessage) error {
	f.message = msg
	return f.err
}

func (f *fakePublisher) PublishTransfer(ctx context.Context, msg queue.TransferMessage) error {
	f.transferMessage = msg
	return f.err
}

type fakeStatusCache struct {
	entry cache.TransactionStatusCacheEntry
	ttl   time.Duration
}

func (f *fakeStatusCache) SetTransactionStatus(ctx context.Context, entry cache.TransactionStatusCacheEntry, ttl time.Duration) error {
	f.entry = entry
	f.ttl = ttl
	return nil
}

func TestCreateTransactionCreatesProcessingTransactionAndPublishes(t *testing.T) {
	t.Parallel()

	transactionID := uuid.New()
	accountID := uuid.New()
	userID := uuid.New()
	merchantID := uuid.New()
	now := time.Now().UTC()

	repo := &fakeRepo{
		account: dbsqlc.Account{
			ID:            accountID,
			UserID:        userID,
			AccountNumber: "2623860486223779",
		},
		merchant: dbsqlc.Merchant{
			ID:       merchantID,
			QrisCode: "MERCHANT001",
		},
		existingTxErr: sql.ErrNoRows,
		createdTx: dbsqlc.Transaction{
			ID:             transactionID,
			RequestedAt:    now,
			CreatedAt:      now,
			UpdatedAt:      now,
			IdempotencyKey: "idem-1",
			Status:         statusProcessing,
			MerchantID:     uuid.NullUUID{UUID: merchantID, Valid: true},
		},
	}
	publisher := &fakePublisher{}
	statusCache := &fakeStatusCache{}
	svc := NewQRISService(repo, statusCache, publisher)

	result, err := svc.CreateTransaction(context.Background(), QRISRequest{
		Session: authservice.SessionView{
			AccountID:     "LEGACY-ACC-1",
			AccountNumber: "2623860486223779",
		},
		MerchantID:     merchantID,
		Amount:         2500,
		IdempotencyKey: "idem-1",
	})
	if err != nil {
		t.Fatalf("CreateTransaction returned error: %v", err)
	}
	if !result.Created {
		t.Fatalf("expected transaction to be newly created")
	}
	if publisher.message.TransactionID != transactionID.String() {
		t.Fatalf("unexpected published transaction id: %s", publisher.message.TransactionID)
	}
	if publisher.message.AccountUUID != accountID.String() {
		t.Fatalf("unexpected account uuid: %s", publisher.message.AccountUUID)
	}
	if publisher.message.MerchantCode != "MERCHANT001" {
		t.Fatalf("unexpected merchant code: %s", publisher.message.MerchantCode)
	}
	if statusCache.entry.Status != statusProcessing {
		t.Fatalf("expected cached status %s, got %s", statusProcessing, statusCache.entry.Status)
	}

	if len(repo.createdEvents) != 2 {
		t.Fatalf("expected 2 transaction events, got %d", len(repo.createdEvents))
	}
	if repo.createdEvents[0].EventType != eventTypeTransactionCreated {
		t.Fatalf("unexpected first event type: %s", repo.createdEvents[0].EventType)
	}
	if repo.createdEvents[1].EventType != eventTypeTransactionEnqueued {
		t.Fatalf("unexpected second event type: %s", repo.createdEvents[1].EventType)
	}

	var metadata map[string]any
	if err := json.Unmarshal(repo.createdEvents[0].Metadata, &metadata); err != nil {
		t.Fatalf("event metadata should be valid json: %v", err)
	}
	if metadata["merchant_code"] != "MERCHANT001" {
		t.Fatalf("unexpected event metadata: %+v", metadata)
	}
}

func TestCreateTransactionReturnsExistingOnIdempotentReplay(t *testing.T) {
	t.Parallel()

	accountID := uuid.New()
	merchantID := uuid.New()
	existing := dbsqlc.Transaction{
		ID:             uuid.New(),
		AccountID:      accountID,
		MerchantID:     uuid.NullUUID{UUID: merchantID, Valid: true},
		Amount:         2500,
		IdempotencyKey: "idem-1",
	}

	svc := NewQRISService(&fakeRepo{
		account:    dbsqlc.Account{ID: accountID, AccountNumber: "2623860486223779"},
		existingTx: existing,
	}, &fakeStatusCache{}, &fakePublisher{})

	result, err := svc.CreateTransaction(context.Background(), QRISRequest{
		Session:        authservice.SessionView{AccountNumber: "2623860486223779"},
		MerchantID:     merchantID,
		Amount:         2500,
		IdempotencyKey: "idem-1",
	})
	if err != nil {
		t.Fatalf("CreateTransaction returned error: %v", err)
	}
	if result.Created {
		t.Fatalf("expected idempotent replay to reuse existing transaction")
	}
}

func TestCreateTransactionRejectsIdempotencyMismatch(t *testing.T) {
	t.Parallel()

	accountID := uuid.New()
	merchantID := uuid.New()
	svc := NewQRISService(&fakeRepo{
		account: dbsqlc.Account{ID: accountID, AccountNumber: "2623860486223779"},
		existingTx: dbsqlc.Transaction{
			ID:         uuid.New(),
			AccountID:  accountID,
			MerchantID: uuid.NullUUID{UUID: merchantID, Valid: true},
			Amount:     2500,
		},
	}, &fakeStatusCache{}, &fakePublisher{})

	_, err := svc.CreateTransaction(context.Background(), QRISRequest{
		Session:        authservice.SessionView{AccountNumber: "2623860486223779"},
		MerchantID:     merchantID,
		Amount:         9999,
		IdempotencyKey: "idem-1",
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}
