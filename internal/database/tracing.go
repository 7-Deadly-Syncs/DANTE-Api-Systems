package database

import (
	"context"
	"database/sql"
	"strings"
	"unicode"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type tracedDBTX struct {
	next sqlc.DBTX
}

func newTracedDBTX(next sqlc.DBTX) sqlc.DBTX {
	return tracedDBTX{next: next}
}

func (db tracedDBTX) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	ctx, span := db.startSpan(ctx, "EXEC", query)
	result, err := db.next.ExecContext(ctx, query, args...)
	tracing.EndSpan(span, err)
	return result, err
}

func (db tracedDBTX) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	ctx, span := db.startSpan(ctx, "PREPARE", query)
	stmt, err := db.next.PrepareContext(ctx, query)
	tracing.EndSpan(span, err)
	return stmt, err
}

func (db tracedDBTX) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	ctx, span := db.startSpan(ctx, "QUERY", query)
	rows, err := db.next.QueryContext(ctx, query, args...)
	tracing.EndSpan(span, err)
	return rows, err
}

func (db tracedDBTX) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	ctx, span := db.startSpan(ctx, "QUERY_ROW", query)
	row := db.next.QueryRowContext(ctx, query, args...)
	tracing.EndSpan(span, nil)
	return row
}

func (db tracedDBTX) startSpan(ctx context.Context, operation, query string) (context.Context, trace.Span) {
	statement := compactSQL(query)
	return tracing.StartClientSpan(ctx, "postgres", "postgres."+strings.ToLower(operation),
		attribute.String("db.system", "postgresql"),
		attribute.String("db.operation", operation),
		attribute.String("db.statement", statement),
	)
}

func compactSQL(query string) string {
	fields := strings.FieldsFunc(strings.TrimSpace(query), unicode.IsSpace)
	statement := strings.Join(fields, " ")
	if len(statement) <= 500 {
		return statement
	}

	return statement[:500]
}
