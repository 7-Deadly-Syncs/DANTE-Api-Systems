package transaction

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

// DetailQuerier describes the database access needed for full transaction reads.
type DetailQuerier interface {
	GetTransactionByID(ctx context.Context, id uuid.UUID) (dbsqlc.Transaction, error)
}

// DetailView is the API-facing view of a full transaction record.
type DetailView struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	MerchantID        uuid.UUID
	AccountID         uuid.UUID
	Amount            int64
	Status            string
	IdempotencyKey    string
	LegacyReferenceID *string
	FailureReason     *string
	RequestedAt       time.Time
	ProcessedAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// DetailService reads full transaction records from PostgreSQL.
type DetailService struct {
	repo DetailQuerier
}

// NewDetailService constructs the transaction detail service.
func NewDetailService(repo DetailQuerier) *DetailService {
	return &DetailService{repo: repo}
}

// GetDetail returns the full transaction record by ID.
func (s *DetailService) GetDetail(ctx context.Context, transactionID uuid.UUID) (*DetailView, error) {
	ctx, span := tracing.StartInternalSpan(ctx, "service.transaction", "transaction.detail.lookup",
		attribute.String("transaction.id", transactionID.String()),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr, sql.ErrNoRows)
	}()

	dbCtx, dbSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.get transaction",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "SELECT"),
		attribute.String("db.sql.table", "transactions"),
		attribute.String("transaction.id", transactionID.String()),
	)
	row, err := s.repo.GetTransactionByID(dbCtx, transactionID)
	tracing.EndSpan(dbSpan, err, sql.ErrNoRows)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			spanErr = err
			return nil, err
		}
		spanErr = err
		return nil, fmt.Errorf("read transaction detail from database: %w", err)
	}

	view := &DetailView{
		ID:             row.ID,
		UserID:         row.UserID,
		MerchantID:     row.MerchantID,
		AccountID:      row.AccountID,
		Amount:         row.Amount,
		Status:         row.Status,
		IdempotencyKey: row.IdempotencyKey,
		RequestedAt:    row.RequestedAt,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}

	if row.LegacyReferenceID.Valid {
		legacyReferenceID := row.LegacyReferenceID.String
		view.LegacyReferenceID = &legacyReferenceID
	}

	if row.FailureReason.Valid {
		failureReason := row.FailureReason.String
		view.FailureReason = &failureReason
	}

	if row.ProcessedAt.Valid {
		processedAt := row.ProcessedAt.Time
		view.ProcessedAt = &processedAt
	}

	span.SetAttributes(
		attribute.String("transaction.status", view.Status),
		attribute.String("account.id", view.AccountID.String()),
		attribute.String("merchant.id", view.MerchantID.String()),
	)
	return view, nil
}
