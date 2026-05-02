# Repository Guidelines

## Project Overview
DANTE is a middleware layer in front of a legacy banking system. The core goal is to improve latency, reliability, and resilience without changing the legacy backend. Prefer async flows, explicit transaction state, and measurable behavior over clever abstractions.

## Structure
- `cmd/dante` starts the HTTP service.
- `internal/bridge` wires HTTP transport and app startup.
- `internal/config` owns environment-driven runtime config.
- `internal/database` owns DB bootstrap and the generated `sqlc` store.
- `internal/database/sqlc` is generated code; do not hand-edit it.
- `db/schema` is the schema source used by `sqlc`.
- `db/migrations` contains goose migrations applied to Postgres.
- `db/queries` contains hand-written SQL used to generate typed queries.
- `configs/nginx`, `docker-compose.yml`, and `scripts/` support local runtime orchestration.

## Development Commands
- `make init` creates `.env` from `.env.example` and fills secrets.
- `make run` starts the Docker stack.
- `make down` stops the Docker stack.
- `make test` runs `go test ./...`.
- `make sqlc-generate` regenerates typed query code from `db/schema` and `db/queries`.
- `make goose-up GOOSE_DBSTRING="postgres://..."` applies migrations.
- `make goose-status GOOSE_DBSTRING="postgres://..."` shows migration state.

## Database Rules
- PostgreSQL is the source of truth. Redis is cache and transient state only.
- Keep `db/schema/init.sql` and `db/migrations/00001_init.sql` aligned until the schema is split into incremental migrations.
- Put schema changes in goose migrations first, then update `db/schema` so `sqlc` stays accurate.
- Keep SQL in `db/queries` simple and explicit; let `sqlc` generate types and interfaces.
- Do not edit files under `internal/database/sqlc` directly.

## API and Runtime Conventions
- Payment-facing flows should remain async-first.
- Idempotency is mandatory for mutation endpoints.
- `/health` stays minimal for health checks.
- `/info` may expose service metadata.
- OpenAPI docs are development-only; they should not be exposed in production.

## Coding and Testing
- Use `gofmt` on all Go files.
- Prefer small packages, explicit error handling, and context-aware blocking operations.
- Write table-driven tests in `*_test.go`.
- Use Docker-backed integration testing when behavior depends on Postgres, Redis, or RabbitMQ.

## Security and Config
- Never commit `.env`.
- Commit only `.env.example`.
- Review dependency and security scan output before merging.
