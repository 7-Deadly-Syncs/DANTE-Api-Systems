# Roadmap

## Purpose

This file tracks implementation progress for DANTE so work can move incrementally without losing context. Update statuses as features land.

DANTE is a resilient middleware layer for simulated QRIS payments. It sits between client applications and a legacy banking mock to reduce latency, protect the legacy system, and improve transaction visibility using Redis, RabbitMQ, PostgreSQL, and controlled experiment tooling.

Status legend:

- `[x]` done
- `[-]` in progress
- `[ ]` not started
- `[!]` blocked / needs decision

---

## Current Foundation

- [x] Docker Compose stack for API, Postgres, Redis, RabbitMQ, and Nginx
- [x] One-shot migration service during Compose startup
- [x] Go HTTP service bootstrapped with Chi + Huma
- [x] Development-only OpenAPI and docs routes
- [x] PostgreSQL schema, goose migrations, and `sqlc` generation
- [x] Cross-platform Go-based migration command (`cmd/migrate`)
- [x] Redis client and cache package
- [ ] RabbitMQ publisher / worker foundation
- [ ] Legacy adapter implementation
- [x] `/metrics` endpoint for Prometheus scraping

---

## Core Architecture Rules

- [x] PostgreSQL is the source of truth for transactions
- [x] Redis is cache and transient state only
- [x] Write flows must not call legacy inline from HTTP handlers
- [ ] Client login session should be issued by DANTE after legacy credential validation
- [ ] `username + password` is for login, while `transaction PIN` is only for financial transaction authorization
- [ ] Worker is the only component allowed to execute payment calls to legacy
- [ ] Legacy adapter must isolate all legacy API details from service logic
- [ ] Keep `db/schema/init.sql` and `db/migrations/00001_init.sql` aligned until migrations are split further

---

## Ownership Notes

These are soft ownership lanes for tracking, not hard boundaries. Anyone can contribute across tracks when needed.

- TI (Teknologi Informasi) / API & platform lane: Redis layer, API optimization, caching behavior, connection pooling, internal endpoints, deploy/runtime polish
- TIF (Teknik Informatika) / algorithm & performance lane: queue/worker design, retry logic, optimization strategies, profiling, bottleneck analysis
- TEKKOM (Teknik Komputer) / infra & network lane: infrastructure tuning, runtime stability, queue/runtime deployment support, network measurement, latency simulation, capacity-oriented testing
- Shared lane: schema evolution, observability, experiment tooling, load testing, network simulation, documentation

---

## TI Track (Teknologi Informasi)

Focus: Redis layer, read APIs, cache behavior, API/runtime polish, and database access efficiency.

### Redis, Caching, and Read APIs

- [x] Add Redis config to `internal/config`
- [x] Add Redis client/bootstrap package
- [x] Define cache key naming and TTL policy
- [x] Implement negative caching and stampede lock strategy
- [-] Add service-layer packages for merchant, account, and transaction reads
- [x] Add `GET /ready`
- [x] Add `GET /v1/merchants/:merchantId`
- [ ] Add `GET /v1/accounts/:accountId` -- depends on legacy account profile contract
- [ ] Add `GET /v1/accounts/:accountId/balance` -- wait for legacy
- [x] Add `GET /v1/transactions/:transactionId/status`
- [x] Add `GET /v1/transactions/:transactionId`
- [x] Add `GET /v1/accounts/:accountId/transactions`
- [x] Add protected internal cache/system status endpoints
- [ ] Add payload compression / response-size optimization review

### Cache Strategy Decisions

- [x] Merchant cache TTL: long-lived
- [x] Balance source model: cached from legacy, not DANTE-owned truth
- [ ] Balance cache TTL: short-lived
- [x] Transaction status TTL: short/medium
- [ ] Idempotency key TTL
- [x] Negative cache TTL for not-found merchant/account reads
- [ ] Decide when legacy fallback is allowed per endpoint
- [ ] Decide error contract when cache + DB miss and legacy is unavailable

### Database Performance Layer

- [ ] Review indexes against actual read patterns
- [ ] Tune connection pooling for PostgreSQL and Redis
- [ ] Review query plans for transaction and merchant endpoints
- [ ] Add guidance for avoiding N+1 and over-fetching patterns

### TI Operational Endpoints

- [x] `GET /ready`
- [x] `GET /internal/system/status`
- [x] `GET /internal/cache/stats`
- [ ] `POST /internal/cache/invalidate`

---

## TIF Track (Teknik Informatika)

Focus: payment flow design, worker/queue processing, retries, transaction-state rules, and performance logic.

### Payments, Queueing, and Transaction Lifecycle

- [ ] Define login/auth contract backed by legacy credential validation
- [ ] Define QRIS payment request/response contract
- [ ] Define transfer request/response contract
- [ ] Add `POST /v1/auth/login`
- [ ] Add `POST /v1/auth/logout`
- [ ] Add `POST /v1/payments/qris`
- [ ] Add `POST /v1/transfers`
- [ ] Require `Idempotency-Key` for payment writes
- [ ] Add idempotency lookup and conflict behavior
- [ ] Add transaction creation flow in PostgreSQL
- [ ] Store initial transaction status as `PROCESSING`
- [ ] Store fast transaction status in Redis
- [ ] Add RabbitMQ enqueue/publish layer
- [ ] DANTE issues client session/access token after successful legacy login
- [ ] DANTE forwards `transaction PIN` only for financial transaction authorization
- [ ] Add worker process for payment execution
- [ ] Worker executes transfer calls through legacy adapter
- [ ] Worker calls legacy through legacy adapter
- [ ] Worker updates PostgreSQL final status
- [ ] Worker updates Redis transaction status
- [ ] Add balance cache invalidation after successful payment
- [ ] Add balance cache invalidation after successful transfer
- [ ] Add async handling for non-critical side effects (logging / notification / audit-style tasks)
- [ ] Add transaction state transitions and retries
- [ ] Add queue visibility and operational metrics

### Transaction State Model

- [ ] Define allowed states: `INITIATED`, `PROCESSING`, `SUCCESS`, `FAILED`, `EXPIRED`
- [ ] Define valid state transitions
- [ ] Prevent invalid transition updates
- [ ] Store state changes in `transaction_events`
- [ ] Return clear status response for client polling
- [ ] Decide expiration behavior for long-running `PROCESSING` transactions

### Resilience Logic

- [ ] Define login failure contract for invalid credentials, blocked account, and legacy unavailability
- [ ] Add timeout policy for legacy calls
- [ ] Add worker retry strategy with exponential backoff
- [ ] Add circuit breaker for degraded legacy calls
- [ ] Add load leveling behavior through worker concurrency limits
- [ ] Decide fallback response when legacy is unavailable

### TIF Operational Endpoints

- [ ] `GET /internal/queue/status`
- [ ] `POST /internal/transactions/:transactionId/retry`

---

## TEKKOM Track (Teknik Komputer)

Focus: deployment/runtime stability, queue infrastructure support, network behavior, and experiment execution environment.

### Infrastructure and Runtime Stability

- [ ] RabbitMQ publisher / worker foundation
- [ ] Legacy adapter implementation
- [ ] Add legacy login adapter (`POST /legacy/auth/login`)
- [ ] Add legacy logout adapter (`POST /legacy/auth/logout`)
- [ ] Add legacy account profile adapter (`GET /legacy/accounts/{accountId}`)
- [ ] Add legacy balance adapter (`GET /legacy/accounts/{accountId}/balance`)
- [ ] Add legacy transfer adapter (`POST /legacy/transfers`)
- [ ] Add legacy QRIS payment adapter (`POST /legacy/payments/qris`)
- [ ] Add RabbitMQ dead-letter queue
- [ ] Add rate limiting for public endpoints
- [ ] Log all legacy failures into PostgreSQL

### Experiment & Simulation Layer

- [x] Add k6 load testing scripts
- [ ] Add baseline direct-legacy scenario
- [ ] Add DANTE-mediated scenario
- [x] Add tc-netem network simulation script
- [x] Define network profiles: normal, high latency, packet loss, constrained bandwidth
- [ ] Store benchmark run metadata
- [ ] Store benchmark result summaries
- [ ] Compare p95 latency, throughput, error rate, cache hit rate, and legacy call count

---

## Shared Track

Focus: database evolution, observability, cross-cutting repository work, and team-wide supporting pieces.

### Query / Repository Gaps

- [x] Basic user, merchant, account, and transaction starter queries
- [-] Merchant upsert/save-from-legacy query
- [ ] Account profile read/update queries
- [ ] Account balance read/update queries
- [x] Transaction creation query
- [x] Transaction state update query
- [x] Transaction detail/history queries
- [ ] Session / login audit queries
- [ ] Idempotency lookup query
- [ ] Transaction event write/read queries
- [ ] Cache metrics write/read queries
- [ ] Legacy call log write/read queries

### Observability Layer

- [x] Expose `/metrics`
- [x] Track endpoint latency
- [x] Track p50/p95/p99 latency
- [x] Track request count and error rate
- [x] Track Redis cache hit/miss
- [ ] Track legacy call latency
- [ ] Track legacy call success/failure rate
- [ ] Track RabbitMQ queue depth / worker lag
- [ ] Track transaction state counts
- [x] Add Prometheus scrape config
- [x] Add Grafana dashboard

---

## Suggested Build Order

### TI-first sequence

1. Finish the remaining merchant/account/transaction read service cleanup
2. Finalize account profile and balance endpoint rules after the legacy contracts are ready
3. Review payload compression, response size, and DB/Redis pool settings
4. Add cache invalidation endpoint and related cache error observability

### TIF sequence

1. Define the login/auth, QRIS payment, and transfer contracts
2. Implement RabbitMQ publisher and transaction creation flow
3. Implement worker process and transaction state transitions
4. Add retries, timeout handling, and queue visibility

### TEKKOM sequence

1. Support queue/runtime stability and dead-letter queue behavior
2. Harden deployment/runtime settings and public endpoint protections
3. Prepare experiment environment for load and network simulation

### Shared sequence

1. Expose `/metrics`
2. Add Prometheus scrape config and Grafana dashboards
3. Add k6 and tc-netem experiment scripts
4. Store and compare benchmark outputs

---

## Notes

- DANTE should act as a protective middleware, not just a normal API service.
- Client login should terminate at DANTE, while legacy remains the authority for credential validation and account status.
- `transaction PIN` should not be used as a login credential; it should only authorize financial transactions such as transfers and QRIS payments.
- Payment requests should return `PROCESSING` quickly.
- Actual legacy payment execution should happen in the worker.
- Transfer execution should follow the same protected legacy-adapter pattern as QRIS payment execution.
- Client-facing status reads should be fast through Redis, with PostgreSQL fallback.
- PostgreSQL must preserve durable transaction history.
- Redis must never be the only copy of critical transaction data.
- Legacy calls should be measured, logged, and controlled.
