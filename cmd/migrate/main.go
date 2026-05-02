package main

import (
	"context"
	"log"
	"os"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database"
)

const migrationsDir = "db/migrations"

func main() {
	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	cfg := config.Load()
	dbCfg := cfg.Database
	dbCfg.Host = firstNonEmpty(dbCfg.ExternalHost, dbCfg.Host)
	if dbCfg.ExternalPort != 0 {
		dbCfg.Port = dbCfg.ExternalPort
	}

	db, err := database.Open(context.Background(), dbCfg)
	if err != nil {
		log.Fatalf("open migration database: %v", err)
	}
	defer db.Close()

	switch command {
	case "up":
		if err := database.Migrate(db, migrationsDir); err != nil {
			log.Fatalf("run migrations: %v", err)
		}
		log.Println("migrations applied successfully")
	case "down":
		if err := database.Rollback(db, migrationsDir); err != nil {
			log.Fatalf("rollback migration: %v", err)
		}
		log.Println("migration rolled back successfully")
	case "status":
		if err := database.Status(db, migrationsDir); err != nil {
			log.Fatalf("read migration status: %v", err)
		}
	default:
		log.Fatalf("unknown migration command %q; expected one of: up, down, status", command)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
