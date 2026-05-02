# DANTE API Systems

DANTE is a Go-based middleware layer that sits in front of a legacy banking system. This repo runs the API, PostgreSQL, Redis, RabbitMQ, Nginx, and a one-shot migration job with Docker Compose.

## Stack
- Go 1.25
- PostgreSQL 15
- Redis 7
- RabbitMQ 3 Management
- Nginx
- `sqlc` for typed SQL
- `goose` for migrations

## Prerequisites
- Docker Desktop or Docker Engine with Compose
- `make`
- Go 1.25+ if you want to run commands outside Docker

## First-Time Setup
1. Clone the repository.
2. Create local environment config:

```bash
make init
```

This creates `.env` from `.env.example` and fills generated secrets where needed.

3. Start the full stack:

```bash
docker compose up --build -d
```

On startup, Compose will:
- start Postgres, Redis, and RabbitMQ
- run the `migrate` service once
- start the API only after migrations succeed
- expose Nginx on `http://localhost:8080`

## Fresh Machine Checklist
After `docker compose up --build -d`, verify:

1. Containers are healthy or completed successfully:

```bash
docker compose ps
```

Expected result:
- `db`, `redis`, and `listen` are `running`
- `migrate` shows `exited (0)` after completing
- `proxy` is `running`

2. API health responds:

```bash
curl http://localhost:8080/health
```

Expected response:

```json
{"status":"ok"}
```

3. Service metadata responds:

```bash
curl http://localhost:8080/info
```

4. In development, OpenAPI is available:

```bash
curl http://localhost:8080/openapi.json
```

## Local URLs
- API root: `http://localhost:8080/`
- Health: `http://localhost:8080/health`
- Ready: `http://localhost:8080/ready`
- Service info: `http://localhost:8080/info`
- OpenAPI JSON: `http://localhost:8080/openapi.json`
- Docs UI: `http://localhost:8080/docs`
- Metrics: `http://localhost:8080/metrics`
- RabbitMQ UI: `http://localhost:15672`

OpenAPI docs are only enabled when `APP_ENV=development`.

## Common Commands
```bash
make run
make down
make test
make goose-status
make goose-up
make goose-down
make sqlc-generate
```

`make goose-*` uses the Go migration command in `cmd/migrate`, so it works on Windows, Linux, and macOS. Host-side migrations use `DB_HOST_EXTERNAL` / `DB_PORT_EXTERNAL` from `.env`.

## Troubleshooting
If `listen` fails because Postgres authentication is wrong, your existing DB volume probably has old credentials:

```bash
docker compose down -v
docker compose up --build -d
```

If the migration job fails:

```bash
docker compose logs migrate --tail 100
```

If the API fails:

```bash
docker compose logs listen --tail 100
```

## Database Workflow
- SQL schema for `sqlc`: `db/schema/init.sql`
- Goose migrations: `db/migrations`
- Hand-written queries: `db/queries`
- Generated code: `internal/database/sqlc`

Do not edit generated files under `internal/database/sqlc` manually.

## Relevant Docs
- [docs/roadmap.md](/docs/roadmap.md): current ownership-oriented implementation tracker
- [docs/cache_strategies.md](/docs/cache_strategies.md): Redis key, TTL, and fallback policy
- [docs/project_context.md](/docs/project_context.md): project brief and scope framing
