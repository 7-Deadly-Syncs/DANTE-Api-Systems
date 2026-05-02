# Cache Strategies

## Purpose
This document defines how DANTE uses Redis for read performance, resilience, and backend protection. Redis is a cache and transient coordination layer. PostgreSQL remains the source of truth for local transaction data, and legacy remains the authority for data that originates upstream.

## Core Read Pattern
For read endpoints, use:

```txt
Redis -> PostgreSQL -> Legacy
```

Do not apply this blindly. Fallback to legacy depends on the ownership model of the data.

## Source of Truth by Resource
- Merchant: legacy-initialized, then stored locally and cached
- Transaction status: PostgreSQL
- Transaction detail: PostgreSQL
- Account transaction history: PostgreSQL
- Account balance: cached snapshot from legacy, not DANTE-owned truth

## Cache Key Conventions
- `merchant:{merchantId}`
- `lock:merchant:{merchantId}`
- `transaction_status:{transactionId}`
- `account_balance:{accountId}`
- `idempotency:{key}` (reserved for future payment idempotency caching)

Key naming should stay resource-oriented and predictable. Use UUID strings as the identifier suffix.
The canonical key builders and TTLs live in `internal/cache/policy.go`.

## TTL Policy
- Merchant cache: long TTL, currently `24h`
- Merchant negative cache: short TTL, currently `10s`
- Merchant lock TTL: very short, currently `3s`
- Transaction status cache: short TTL, currently `30s`
- Account balance cache: short TTL once implemented, currently planned as `15s`
- Idempotency key cache: currently planned as `24h`

TTL guidance:
- stable reference data can live longer
- sensitive or frequently changing values should expire quickly
- negative caches must always be short-lived

## Implemented Strategies

### 1. Cache-Aside
Used for merchant and transaction status reads.

Flow:
1. Check Redis
2. On miss, read PostgreSQL
3. If needed and allowed, call legacy
4. Fill Redis asynchronously

This keeps request latency low while avoiding write-through complexity.

### 2. Negative Caching
Used for merchant lookups.

If the legacy adapter explicitly returns “not found”, store a short Redis marker so repeated misses do not hammer PostgreSQL or legacy.

Important:
- only cache a negative result when the source is authoritative for absence
- do not negative-cache temporary outages
- do not negative-cache on mere DB miss when the real source is legacy

### 3. Cache Stampede Protection
Used for merchant lookups.

On a cold miss, a short Redis lock is acquired:

```txt
lock:merchant:{merchantId}
```

Only one request is allowed to perform fallback work. Other requests wait briefly for cache fill. If the wait expires, return `503` rather than letting all requests pile into the expensive path.

### 4. Async Cache Fill
Redis writes should not block successful responses unless the cache write is itself part of a correctness guarantee. For current read paths, cache fill happens in the background after the authoritative read succeeds.

## Endpoint-Specific Rules

### Merchant
- Flow: Redis -> PostgreSQL -> Legacy
- Long TTL
- Negative cache allowed
- Stampede lock required

### Transaction Status
- Flow: Redis -> PostgreSQL
- No legacy fallback in the current design
- PostgreSQL is authoritative
- Cache should be short-lived

### Transaction Detail
- Current flow: PostgreSQL only
- No Redis cache yet
- Safe to add later if profiling shows value

### Account Transaction History
- Current flow: PostgreSQL only
- No Redis cache yet
- Pagination and filtering should remain DB-driven

### Account Balance
- Planned flow: Redis -> PostgreSQL snapshot -> Legacy
- Legacy is the authority
- PostgreSQL stores the last known snapshot
- Use short TTL and freshness rules
- Do not expose this endpoint until the legacy contract is ready

### Idempotency
- Planned Redis key: `idempotency:{key}`
- Planned TTL: `24h`
- Intended for future write-side deduplication once payment flow is implemented

## When Not to Call Legacy
Do not fallback to legacy when:
- the data is locally authoritative in PostgreSQL
- the request is a write flow like payment creation
- the fallback would break idempotency or local transaction ownership

## Failure Handling
- Redis failure should degrade to PostgreSQL reads where safe
- PostgreSQL failure should fail the request for locally authoritative data
- Legacy failure on a fallback path should return a clear upstream-unavailable style error, not a fake `404`

## Observability Targets
Future cache instrumentation should measure:
- cache hit count
- cache miss count
- negative cache hits
- lock contention rate
- fallback-to-legacy count
- cache fill errors

## Implementation Notes
- Keep Redis logic in `internal/cache`
- Keep orchestration logic in service packages
- Avoid putting Redis decision trees directly into HTTP handlers
- Do not edit cache policy ad hoc per endpoint without updating this document
