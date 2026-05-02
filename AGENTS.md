# Repository Guidelines

## Project Overview
DANTE is a middleware layer in front of a legacy banking system. The core goal is to improve latency, reliability, and resilience without changing the legacy backend. Prefer async flows, explicit transaction state, and measurable behavior over clever abstractions.

## Current State
- Read-side foundation is already implemented.
- Write-side payment flow, queue publisher/worker flow, and real legacy integration are still pending.
- PostgreSQL is the source of truth for transaction data.
- Redis is used for cache and short-lived coordination only.
- RabbitMQ is part of the stack, but not yet the active application flow.

Current implemented endpoints:
- `GET /health`
- `GET /ready`
- `GET /info`
- `GET /metrics`
- `GET /v1/merchants/{merchantId}`
- `GET /v1/transactions/{transactionId}/status`
- `GET /v1/transactions/{transactionId}`
- `GET /v1/accounts/{accountId}/transactions`
- `GET /internal/cache/stats`
- `GET /internal/system/status`

Current important decisions:
- Merchant reads use `Redis -> PostgreSQL -> Legacy`.
- Transaction status uses `Redis -> PostgreSQL`.
- Transaction detail and account transaction history currently use PostgreSQL directly.
- Merchant reads already include negative caching and cache stampede protection.
- Balance is a cached snapshot from legacy, not DANTE-owned truth.
- Do not implement the public balance endpoint as PostgreSQL-only.

## Structure
- `cmd/dante` starts the HTTP service.
- `cmd/migrate` runs goose migrations through Go code for cross-platform use.
- `internal/bridge` wires HTTP transport and app startup.
- `internal/cache` owns Redis client bootstrap, key policy, and cache helpers.
- `internal/config` owns environment-driven runtime config.
- `internal/database` owns DB bootstrap and the generated `sqlc` store.
- `internal/database/sqlc` is generated code; do not hand-edit it.
- `internal/legacy` holds legacy-facing interfaces and temporary stubs.
- `internal/observability` holds app metrics helpers and `/metrics` support.
- `internal/queue` holds RabbitMQ connection/check helpers.
- `internal/service` holds orchestration logic between cache, DB, and future legacy/queue integrations.
- `db/schema` is the schema source used by `sqlc`.
- `db/migrations` contains goose migrations applied to Postgres.
- `db/queries` contains hand-written SQL used to generate typed queries.
- `docs/roadmap.md` is the main implementation tracker split by study-program lanes.
- `docs/cache_strategies.md` is the canonical cache strategy reference.
- `configs/nginx`, `docker-compose.yml`, and `scripts/` support local runtime orchestration.

## Development Commands
- `make init` creates `.env` from `.env.example` and fills secrets.
- `make run` starts the Docker stack.
- `make down` stops the Docker stack.
- `make test` runs `go test ./...`.
- `make sqlc-generate` regenerates typed query code from `db/schema` and `db/queries`.
- `make goose-up` applies migrations using `cmd/migrate`.
- `make goose-status` shows migration state using `cmd/migrate`.
- `docker compose logs migrate --tail 100` inspects startup migration failures.
- `docker compose logs listen --tail 100` inspects API startup/runtime failures.

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
- `/ready` is for dependency readiness checks.
- `/info` may expose service metadata.
- `/metrics` should stay Prometheus-scrape friendly.
- OpenAPI docs are development-only; they should not be exposed in production.
- Internal endpoints should stay operational and diagnostic, not business-facing.
- Prefer documenting new handlers with Huma operation metadata so `/docs` remains useful.

## Coding and Testing
- Use `gofmt` on all Go files.
- Prefer small packages, explicit error handling, and context-aware blocking operations.
- Write table-driven tests in `*_test.go`.
- Use Docker-backed integration testing when behavior depends on Postgres, Redis, or RabbitMQ.

## Coordination Notes
- Check `docs/roadmap.md` before starting larger work; it is split into `TI`, `TIF`, `TEKKOM`, and `Shared` tracks.
- TI-side work currently includes Redis behavior, read APIs, internal endpoints, cache policy, and DB/Redis tuning.
- TIF-side work currently centers on payment contract, idempotency, worker flow, and transaction lifecycle.
- TEKKOM-side work currently centers on runtime stability, deployment behavior, load tooling, and network simulation.
- Keep cross-track contracts explicit when editing shared areas like transaction state, cache invalidation, and metrics naming.

## Security and Config
- Never commit `.env`.
- Commit only `.env.example`.
- Review dependency and security scan output before merging.
