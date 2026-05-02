package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Store groups shared database dependencies used by the application.
type Store struct {
	DB      *sql.DB
	Queries *sqlc.Queries
}

// Open creates a PostgreSQL connection pool and verifies connectivity.
func Open(ctx context.Context, cfg config.DatabaseConfig) (*sql.DB, error) {
	db, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return db, nil
}

// NewStore constructs the application database store.
func NewStore(db *sql.DB) *Store {
	return &Store{
		DB:      db,
		Queries: sqlc.New(db),
	}
}
