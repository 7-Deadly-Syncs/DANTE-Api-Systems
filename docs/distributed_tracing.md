# Distributed Tracing DANTE API

Implementasi tracing memakai OpenTelemetry Go dan mengirim trace ke Jaeger melalui OTLP HTTP.

## Environment

```env
TRACING_ENABLED=true
OTEL_SERVICE_NAME=dante-api
OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4318
TRACE_SAMPLE_RATIO=1.0
```

`OTEL_EXPORTER_OTLP_ENDPOINT` adalah konfigurasi utama. `JAEGER_OTLP_ENDPOINT` lama masih didukung sebagai fallback.

## Struktur Folder

```text
cmd/
  dante/main.go                         # entrypoint API
internal/
  bridge/bridge.go                      # wiring router, middleware, service, worker
  config/config.go                      # env tracing
  observability/
    tracing/tracing.go                  # tracer provider OTLP -> Jaeger
    tracing/spans.go                    # helper server/internal/client/producer/consumer span
    httpobs/middleware.go               # inbound HTTP span dan handler/controller span
  database/
    db.go                               # PostgreSQL connection dan traced SQLC store
    tracing.go                          # wrapper DBTX untuk query PostgreSQL
  cache/redis.go                        # Redis spans
  queue/
    publisher.go                        # RabbitMQ producer span dan context injection
    consumer.go                         # RabbitMQ consumer span dan context extraction
    tracing.go                          # AMQP header carrier untuk trace context
  legacy/client.go                      # outbound legacy HTTP/SOAP span dan traceparent injection
docs/
  distributed_tracing.md                # panduan ini
docker-compose.yml                      # Jaeger all-in-one + OTLP 4318
```

## Span Yang Dibuat

- Request masuk ke API: `GET /route`, `POST /route` dari `internal/observability/httpobs/middleware.go`.
- Proses handler/controller: `handler METHOD /route` sebagai child span dari request HTTP.
- Query PostgreSQL: `postgres.query`, `postgres.query_row`, `postgres.exec`, `postgres.prepare` dari wrapper SQLC.
- Akses Redis: `redis.get`, `redis.set`, `redis.del`, `redis.setnx`, `redis.eval`.
- Publish message RabbitMQ: `rabbitmq.publish qris`, `rabbitmq.publish transfer`, dan `rabbitmq.publish retry`.
- Proses worker RabbitMQ: `rabbitmq.consume <queue>`.
- Request keluar ke legacy system: `legacy.login`, `legacy.balance`, `legacy.transfer`, `legacy.qris`, dan operasi SOAP lain.

## Cara Menjalankan

1. Buat `.env` dari contoh:

```bash
cp .env.example .env
```

2. Jalankan stack:

```bash
docker compose up --build
```

3. Buka UI:

- API: `http://localhost:8080`
- OpenAPI docs: `http://localhost:8080/docs`
- Jaeger UI: `http://localhost:16686`
- RabbitMQ UI: `http://localhost:15672`
- Prometheus: `http://localhost:9090`
- Grafana: `http://localhost:3000`

Di Jaeger, pilih service `dante-api`, lalu klik `Find Traces`.

## Tes Endpoint Dengan Curl

System endpoints:

```bash
curl http://localhost:8080/health
curl http://localhost:8080/ready
curl http://localhost:8080/info
curl http://localhost:8080/internal/system/status
curl http://localhost:8080/internal/queue/status
curl http://localhost:8080/internal/cache/stats
```

Auth dan session:

```bash
curl -X POST http://localhost:8080/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"name":"Budi Santoso","email":"budi@example.com","password":"secret","pin":"123456"}'

TOKEN=$(curl -s -X POST http://localhost:8080/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"budi@example.com","password":"secret"}' | jq -r '.token')

ACCOUNT_ID=$(curl -s -X GET http://localhost:8080/v1/auth/session \
  -H "Authorization: Bearer $TOKEN" | jq -r '.account_id')

curl -X GET http://localhost:8080/v1/auth/session \
  -H "Authorization: Bearer $TOKEN"
```

Account balance. Endpoint ini memanggil legacy balance ketika cache Redis belum punya snapshot, dan request outbound membawa context tracing:

```bash
curl -X GET "http://localhost:8080/v1/accounts/$ACCOUNT_ID/balance" \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Transaction-PIN: 123456"
```

Account profile dan history:

```bash
curl -X GET "http://localhost:8080/v1/accounts/$ACCOUNT_ID" \
  -H "Authorization: Bearer $TOKEN"

curl -X GET "http://localhost:8080/v1/accounts/$ACCOUNT_ID/transactions?limit=20" \
  -H "Authorization: Bearer $TOKEN"
```

QRIS payment. Request API membuat span HTTP, handler, Redis, PostgreSQL, RabbitMQ publish, RabbitMQ worker, dan legacy qris:

```bash
PAYMENT_ID=$(curl -s -X POST http://localhost:8080/v1/payments/qris \
  -H "Authorization: Bearer $TOKEN" \
  -H "Idempotency-Key: qris-demo-001" \
  -H "Content-Type: application/json" \
  -d '{"merchant_id":"QRIS-DEMO-001","amount":25000}' | jq -r '.id')

curl "http://localhost:8080/v1/transactions/$PAYMENT_ID/status"
curl "http://localhost:8080/v1/transactions/$PAYMENT_ID"
```

Transfer. Request API membuat span RabbitMQ publish dan worker transfer:

```bash
TRANSFER_ID=$(curl -s -X POST http://localhost:8080/v1/transfers \
  -H "Authorization: Bearer $TOKEN" \
  -H "Idempotency-Key: transfer-demo-001" \
  -H "Content-Type: application/json" \
  -d '{"to_account_number":"100200300","amount":15000,"transaction_pin":"123456"}' | jq -r '.id')

curl "http://localhost:8080/v1/transactions/$TRANSFER_ID/status"
curl "http://localhost:8080/v1/transactions/$TRANSFER_ID"
```

Invalidate cache:

```bash
curl -X POST http://localhost:8080/internal/cache/invalidate \
  -H "Content-Type: application/json" \
  -d "{\"account_id\":\"$ACCOUNT_ID\"}"
```

Logout:

```bash
curl -X POST http://localhost:8080/v1/auth/logout \
  -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\"}"
```

## Tes Dengan Postman

1. Buat collection dengan base URL `http://localhost:8080`.
2. Panggil `POST /v1/auth/login`, simpan field `token` dari response ke collection variable.
3. Gunakan header `Authorization: Bearer {{token}}` untuk endpoint account, QRIS, dan transfer.
4. Buka Jaeger UI di `http://localhost:16686`, pilih service `dante-api`, lalu cari trace terbaru.

## Catatan Legacy

Endpoint auth, balance, QRIS worker, dan transfer worker membutuhkan `LEGACY_BANKSERVICE_URL`. Default `.env.example` mengarah ke:

```env
LEGACY_BANKSERVICE_URL=http://host.docker.internal:8081/axis2/services/BankService
```

Jika legacy simulator belum berjalan, endpoint legacy-dependent akan tetap membuat span error yang terlihat di Jaeger.

