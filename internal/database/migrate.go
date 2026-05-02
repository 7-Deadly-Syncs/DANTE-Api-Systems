package database

import (
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

const dialect = "postgres"

// Migrate applies all pending SQL migrations in the provided directory.
func Migrate(db *sql.DB, dir string) error {
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	if err := goose.Up(db, dir); err != nil {
		return fmt.Errorf("run goose migrations: %w", err)
	}

	return nil
}

// Rollback reverts the most recent SQL migration.
func Rollback(db *sql.DB, dir string) error {
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	if err := goose.Down(db, dir); err != nil {
		return fmt.Errorf("rollback goose migration: %w", err)
	}

	return nil
}

// Status prints the migration status for the provided migration directory.
func Status(db *sql.DB, dir string) error {
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	if err := goose.Status(db, dir); err != nil {
		return fmt.Errorf("read goose status: %w", err)
	}

	return nil
}
