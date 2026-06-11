package payment

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	authservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/auth"
	"github.com/google/uuid"
)

func TestCreateTransferCreatesProcessingTransactionAndPublishes(t *testing.T) {
	t.Parallel()

	transactionID := uuid.New()
	accountID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()

	repo := &fakeRepo{
		account: dbsqlc.Account{
			ID:            accountID,
			UserID:        userID,
			AccountNumber: "2623860486223779",
		},
		existingTxErr: sql.ErrNoRows,
		createdTx: dbsqlc.Transaction{
			ID:             transactionID,
			RequestedAt:    now,
			CreatedAt:      now,
			UpdatedAt:      now,
			IdempotencyKey: "transfer-idem-1",
			Status:         statusProcessing,
		},
	}
	publisher := &fakePublisher{}
	statusCache := &fakeStatusCache{}
	svc := NewTransferService(repo, statusCache, publisher)

	result, err := svc.CreateTransaction(context.Background(), TransferRequest{
		Session: authservice.SessionView{
			AccountID:     "LEGACY-ACC-1",
			AccountNumber: "2623860486223779",
		},
		ToAccountNumber: "888777666555",
		Amount:          9000,
		TransactionPIN:  "123456",
		IdempotencyKey:  "transfer-idem-1",
	})
	if err != nil {
		t.Fatalf("CreateTransaction returned error: %v", err)
	}
	if !result.Created {
		t.Fatalf("expected transfer to be newly created")
	}
	if publisher.transferMessage.TransactionID != transactionID.String() {
		t.Fatalf("unexpected published transaction id: %s", publisher.transferMessage.TransactionID)
	}
	if publisher.transferMessage.ToAccountNumber != "888777666555" {
		t.Fatalf("unexpected destination account: %s", publisher.transferMessage.ToAccountNumber)
	}
	if publisher.transferMessage.TransactionPIN != "123456" {
		t.Fatalf("unexpected transaction pin forwarding")
	}
	if statusCache.entry.Status != statusProcessing {
		t.Fatalf("expected cached status %s, got %s", statusProcessing, statusCache.entry.Status)
	}
	if len(repo.createdEvents) != 2 {
		t.Fatalf("expected 2 transfer events, got %d", len(repo.createdEvents))
	}
	if repo.createdEvents[0].EventType != eventTypeTransferCreated {
		t.Fatalf("unexpected first event type: %s", repo.createdEvents[0].EventType)
	}
	if repo.createdEvents[1].EventType != eventTypeTransferEnqueued {
		t.Fatalf("unexpected second event type: %s", repo.createdEvents[1].EventType)
	}
}

func TestCreateTransferReturnsExistingOnIdempotentReplay(t *testing.T) {
	t.Parallel()

	accountID := uuid.New()
	existingID := uuid.New()
	svc := NewTransferService(&fakeRepo{
		account: dbsqlc.Account{ID: accountID, AccountNumber: "2623860486223779"},
		existingTx: dbsqlc.Transaction{
			ID:             existingID,
			AccountID:      accountID,
			MerchantID:     uuid.NullUUID{},
			Amount:         9000,
			IdempotencyKey: "transfer-idem-1",
		},
		listedEvents: []dbsqlc.TransactionEvent{
			{
				TransactionID: existingID,
				EventType:     eventTypeTransferCreated,
				Metadata:      []byte(`{"to_account_number":"888777666555"}`),
			},
		},
	}, &fakeStatusCache{}, &fakePublisher{})

	result, err := svc.CreateTransaction(context.Background(), TransferRequest{
		Session:         authservice.SessionView{AccountNumber: "2623860486223779"},
		ToAccountNumber: "888777666555",
		Amount:          9000,
		TransactionPIN:  "123456",
		IdempotencyKey:  "transfer-idem-1",
	})
	if err != nil {
		t.Fatalf("CreateTransaction returned error: %v", err)
	}
	if result.Created {
		t.Fatalf("expected idempotent replay to reuse existing transfer")
	}
}

func TestCreateTransferRejectsIdempotencyMismatch(t *testing.T) {
	t.Parallel()

	accountID := uuid.New()
	existingID := uuid.New()
	svc := NewTransferService(&fakeRepo{
		account: dbsqlc.Account{ID: accountID, AccountNumber: "2623860486223779"},
		existingTx: dbsqlc.Transaction{
			ID:         existingID,
			AccountID:  accountID,
			MerchantID: uuid.NullUUID{},
			Amount:     9000,
		},
		listedEvents: []dbsqlc.TransactionEvent{
			{
				TransactionID: existingID,
				EventType:     eventTypeTransferCreated,
				Metadata:      []byte(`{"to_account_number":"111222333444"}`),
			},
		},
	}, &fakeStatusCache{}, &fakePublisher{})

	_, err := svc.CreateTransaction(context.Background(), TransferRequest{
		Session:         authservice.SessionView{AccountNumber: "2623860486223779"},
		ToAccountNumber: "888777666555",
		Amount:          9000,
		TransactionPIN:  "123456",
		IdempotencyKey:  "transfer-idem-1",
	})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}
