package transaction

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultHistoryLimit = 20
	maxHistoryLimit     = 100
)

// HistoryQuerier describes the database access needed for transaction history reads.
type HistoryQuerier interface {
	ListTransactionsByAccountID(ctx context.Context, arg dbsqlc.ListTransactionsByAccountIDParams) ([]dbsqlc.Transaction, error)
}

// HistoryParams contains query options for account transaction history.
type HistoryParams struct {
	AccountID uuid.UUID
	Limit     int
	Cursor    string
}

// HistoryPage contains a page of transactions plus the next cursor when more results exist.
type HistoryPage struct {
	Items      []DetailView
	NextCursor *string
}

type historyCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// HistoryService reads cursor-paginated account transaction history from PostgreSQL.
type HistoryService struct {
	repo HistoryQuerier
}

// NewHistoryService constructs the transaction history service.
func NewHistoryService(repo HistoryQuerier) *HistoryService {
	return &HistoryService{repo: repo}
}

// ListByAccount returns a cursor-paginated page of transactions for an account.
func (s *HistoryService) ListByAccount(ctx context.Context, params HistoryParams) (*HistoryPage, error) {
	ctx, span := tracing.StartInternalSpan(ctx, "service.transaction", "transaction.history.list",
		attribute.String("account.id", params.AccountID.String()),
		attribute.Int("transaction.requested_limit", params.Limit),
		attribute.Bool("transaction.cursor_present", strings.TrimSpace(params.Cursor) != ""),
	)
	var spanErr error
	defer func() {
		tracing.EndSpan(span, spanErr)
	}()

	limit := params.Limit
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}

	cursorCreatedAt := time.Time{}
	cursorID := uuid.Nil

	if strings.TrimSpace(params.Cursor) != "" {
		cursor, err := decodeHistoryCursor(params.Cursor)
		if err != nil {
			spanErr = err
			return nil, fmt.Errorf("decode history cursor: %w", err)
		}
		cursorCreatedAt = cursor.CreatedAt
		cursorID = cursor.ID
	}
	span.SetAttributes(attribute.Int("transaction.effective_limit", limit))

	dbCtx, dbSpan := tracing.StartClientSpan(ctx, "postgres", "postgres.list transactions_by_account",
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", "SELECT"),
		attribute.String("db.sql.table", "transactions"),
		attribute.String("account.id", params.AccountID.String()),
		attribute.Int("transaction.limit", limit),
	)
	rows, err := s.repo.ListTransactionsByAccountID(dbCtx, dbsqlc.ListTransactionsByAccountIDParams{
		AccountID: params.AccountID,
		Column2:   cursorCreatedAt,
		ID:        cursorID,
		Limit:     int32(limit),
	})
	tracing.EndSpan(dbSpan, err)
	if err != nil {
		spanErr = err
		return nil, fmt.Errorf("read account transaction history from database: %w", err)
	}

	items := make([]DetailView, 0, len(rows))
	for _, row := range rows {
		items = append(items, mapTransactionRow(row))
	}

	page := &HistoryPage{Items: items}
	if len(items) == limit {
		last := items[len(items)-1]
		nextCursor := encodeHistoryCursor(historyCursor{
			CreatedAt: last.CreatedAt,
			ID:        last.ID,
		})
		page.NextCursor = &nextCursor
	}
	span.SetAttributes(
		attribute.Int("transaction.result_count", len(items)),
		attribute.Bool("transaction.next_cursor_present", page.NextCursor != nil),
	)

	return page, nil
}

func mapTransactionRow(row dbsqlc.Transaction) DetailView {
	view := DetailView{
		ID:             row.ID,
		UserID:         row.UserID,
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

	if row.MerchantID.Valid {
		merchantID := row.MerchantID.UUID
		view.MerchantID = &merchantID
	}

	return view
}

func encodeHistoryCursor(cursor historyCursor) string {
	raw := cursor.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + cursor.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeHistoryCursor(encoded string) (*historyCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}

	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return nil, errors.New("invalid cursor format")
	}

	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, err
	}

	id, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, err
	}

	return &historyCursor{
		CreatedAt: createdAt,
		ID:        id,
	}, nil
}

// NormalizeLimit converts a requested page size into a bounded history page size.
func NormalizeLimit(limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("limit must be greater than zero")
	}
	if limit > maxHistoryLimit {
		return maxHistoryLimit, nil
	}

	return limit, nil
}
