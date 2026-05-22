# Bridge.go Modifications untuk Rate Limiting

## 1. Tambahkan Import

Di bagian `import` statement (setelah line 11), tambahkan:

```go
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/middleware"
```

**Lokasi: Setelah import yang lain di block import statement**

---

## 2. Tambahkan Rate Limiter Middleware

Setelah baris `router.Use(middleware.Recoverer)` (sekitar line 226), tambahkan:

```go
	// Rate limiting untuk melindungi public endpoints dari abuse.
	// Menggunakan token bucket algorithm (golang.org/x/time/rate) dengan per-IP tracking.
	// Default: 5 req/sec per IP dengan burst 10.
	// Endpoint internal seperti /health, /metrics, /internal/* di-exclude dari rate limiting.
	rateLimiter := middleware.NewRateLimiter(5.0, 10)
	router.Use(rateLimiter.Handler())
```

---

## 3. Struktur Lengkap Middleware Stack

Urutan middleware di router sebelum Huma registration:

```go
	router := chi.NewRouter()
	router.Use(middleware.RequestID)              // Add request ID
	router.Use(middleware.RealIP)                 // Extract real IP from X-Forwarded-For
	
	// Observability middleware (tracing & metrics)
	router.Use(httpobs.Tracing(cfg.Observability.ServiceName))
	router.Use(httpobs.Metrics(metricsHandler))
	
	// General middleware
	router.Use(middleware.Logger)                 // Log requests
	router.Use(middleware.Recoverer)              // Recover from panics
	
	// Rate limiting (NEW)
	rateLimiter := middleware.NewRateLimiter(5.0, 10)
	router.Use(rateLimiter.Handler())
	
	// Metrics handler
	router.Handle("/metrics", metricsHandler)
```

---

## Konfigurasi Rate Limiter

Untuk menyesuaikan rate limiter, modifikasi parameter di `NewRateLimiter`:

### Scenario 1: Ketat (untuk API publik yang ekspektasinya tidak tinggi)
```go
rateLimiter := middleware.NewRateLimiter(2.0, 5)  // 2 req/sec, burst 5
```

### Scenario 2: Sedang (default - cocok untuk DANTE)
```go
rateLimiter := middleware.NewRateLimiter(5.0, 10) // 5 req/sec, burst 10
```

### Scenario 3: Relaks (untuk internal/testing)
```go
rateLimiter := middleware.NewRateLimiter(20.0, 50) // 20 req/sec, burst 50
```

---

## Endpoints yang Ter-Exclude (Tidak di-Rate Limit)

- ✅ `/health`
- ✅ `/ready`
- ✅ `/info`
- ✅ `/metrics`
- ✅ `/openapi`
- ✅ `/docs`
- ✅ `/internal/*` (semua endpoint internal)

---

## Endpoints yang Di-Rate Limit (Public)

- 🔒 `GET /v1/merchants/{merchantId}`
- 🔒 `GET /v1/transactions/{transactionId}`
- 🔒 `GET /v1/transactions/{transactionId}/status`
- 🔒 `GET /v1/accounts/{accountId}/transactions`
- 🔒 `POST /v1/auth/login` (ketika sudah ada)
- 🔒 `POST /v1/auth/logout` (ketika sudah ada)
- 🔒 `POST /v1/payments/qris` (ketika sudah ada)
- 🔒 `POST /v1/transfers` (ketika sudah ada)

---

## Response pada Rate Limit Exceeded

Ketika client melebihi rate limit:
- **HTTP Status:** 429 (Too Many Requests)
- **Header Retry-After:** 1 (second)
- **Body:** "rate limit exceeded"

Contoh:
```
HTTP/1.1 429 Too Many Requests
Retry-After: 1
Content-Type: text/plain; charset=utf-8

rate limit exceeded
```

---

## Proxy Support

Rate limiter mendukung proxy scenarios dengan header priority:
1. **X-Forwarded-For** (most common - ambil IP pertama jika multiple)
2. **X-Real-IP** (alternative proxy header)
3. **RemoteAddr** (fallback untuk direct connection)

---

## Testing

Jalankan unit tests:
```bash
go test -v ./internal/middleware
```

Hasil yang diharapkan:
- ✅ Excluded paths bypass rate limiting
- ✅ Requests beyond burst limit get HTTP 429
- ✅ Per-IP tracking works correctly
- ✅ Proxy headers handled properly
- ✅ Retry-After header present in 429 response
