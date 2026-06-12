package bridge

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	ratelimit "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/middleware"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/cachemetrics"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/httpobs"
	observabilitymetrics "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/metrics"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/tracing"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/queue"
	accountservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/account"
	authservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/auth"
	merchantservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/merchant"
	paymentservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/payment"
	transactionservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/transaction"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

type messageResponse struct {
	Body struct {
		Message string `json:"message" doc:"Human-readable status message"`
	}
}

type healthResponse struct {
	Body struct {
		Status string `json:"status" doc:"Liveness state of the HTTP process"`
	}
}

type readyResponse struct {
	Body struct {
		Status       string            `json:"status" doc:"Overall readiness state"`
		Dependencies map[string]string `json:"dependencies" doc:"Readiness state for each required dependency"`
	}
}

type infoResponse struct {
	Body struct {
		Service     string `json:"service" doc:"Stable service identifier"`
		Version     string `json:"version" doc:"Application version string"`
		Environment string `json:"environment" doc:"Current runtime environment"`
	}
}

type authLoginRequest struct {
	Body struct {
		Username string `json:"username" format:"email" minLength:"3" doc:"Client login identifier passed to the legacy auth flow"`
		Password string `json:"password" minLength:"1" doc:"Client password that DANTE validates through the legacy system"`
	}
}

type authRegisterRequest struct {
	Body struct {
		Name     string `json:"name" minLength:"1" doc:"Customer display name to create in the legacy banking system"`
		Email    string `json:"email" format:"email" minLength:"3" doc:"Customer email used as the login identifier"`
		Password string `json:"password" minLength:"1" doc:"Customer password that legacy stores as the login secret"`
		PIN      string `json:"pin" minLength:"1" doc:"Financial transaction PIN used only for payment authorization after registration"`
	}
}

type authLogoutRequest struct {
	Body struct {
		Token string `json:"token" minLength:"1" doc:"DANTE-issued session token to invalidate"`
	}
}

type authSessionHeaders struct {
	Authorization string `header:"Authorization" doc:"Bearer token returned by DANTE login"`
}

type paymentHeaders struct {
	Authorization  string `header:"Authorization" doc:"Bearer token returned by DANTE login"`
	IdempotencyKey string `header:"Idempotency-Key" doc:"Client-supplied idempotency key used to deduplicate payment creation"`
}

type accountBalanceHeaders struct {
	Authorization  string `header:"Authorization" doc:"Bearer token returned by DANTE login"`
	TransactionPIN string `header:"X-Transaction-PIN" doc:"Transaction PIN used to authorize a legacy balance refresh when cache is cold"`
}

type accountProfileRequest struct {
	AccountID     string `path:"accountId" doc:"Legacy account identifier such as ACC000080"`
	Authorization string `header:"Authorization" doc:"Bearer token returned by DANTE login"`
}

type accountBalanceRequest struct {
	AccountID      string `path:"accountId" doc:"Legacy account identifier such as ACC000080"`
	Authorization  string `header:"Authorization" doc:"Bearer token returned by DANTE login"`
	TransactionPIN string `header:"X-Transaction-PIN" doc:"Transaction PIN used to authorize a legacy balance refresh when cache is cold"`
}

type accountTransactionsRequest struct {
	AccountID     string `path:"accountId" doc:"Legacy account identifier such as ACC000080"`
	Cursor        string `query:"cursor" doc:"Opaque pagination cursor returned by the previous page"`
	Limit         int    `query:"limit" doc:"Page size, default 20, max 100" default:"20" minimum:"1" maximum:"100"`
	Authorization string `header:"Authorization" doc:"Bearer token returned by DANTE login"`
}

type authLoginResponse struct {
	Body authSessionDTO
}

type authRegisterResponse struct {
	Body authSessionDTO
}

type authLogoutResponse struct {
	Body struct {
		Message string `json:"message" doc:"Human-readable logout status message"`
	}
}

type authSessionResponse struct {
	Body authSessionStateDTO
}

type qrisPaymentRequest struct {
	paymentHeaders
	Body struct {
		MerchantID string `json:"merchant_id" minLength:"1" doc:"Target merchant reference for the QRIS payment, either a DANTE merchant UUID or a QRIS/legacy merchant code"`
		Amount     int64  `json:"amount" minimum:"1" doc:"Payment amount in the smallest currency unit"`
	}
}

type transferRequest struct {
	paymentHeaders
	Body struct {
		ToAccountNumber string `json:"to_account_number" minLength:"1" doc:"Destination account number for the transfer"`
		Amount          int64  `json:"amount" minimum:"1" doc:"Transfer amount in the smallest currency unit"`
		TransactionPIN  string `json:"transaction_pin" minLength:"1" doc:"Transaction PIN used only for financial authorization"`
	}
}

type qrisPaymentResponse struct {
	Body transactionDetailDTO
}

type transferResponse struct {
	Body transactionDetailDTO
}

type merchantPathParams struct {
	MerchantID string `path:"merchantId" format:"uuid" doc:"Merchant UUID"`
}

type transactionPathParams struct {
	TransactionID string `path:"transactionId" format:"uuid" doc:"Transaction UUID"`
}

type accountPathParams struct {
	AccountID string `path:"accountId" doc:"Legacy account identifier such as ACC000080"`
}

type accountTransactionsQueryParams struct {
	Cursor string `query:"cursor" doc:"Opaque pagination cursor returned by the previous page"`
	Limit  int    `query:"limit" doc:"Page size, default 20, max 100" default:"20" minimum:"1" maximum:"100"`
}

type merchantResponse struct {
	Body merchantDTO
}

type transactionStatusResponse struct {
	Body transactionStatusDTO
}

type transactionDetailResponse struct {
	Body transactionDetailDTO
}

type accountTransactionsResponse struct {
	Body accountTransactionsDTO
}

type cacheStatsResponse struct {
	Body cacheStatsDTO
}

type cacheInvalidateRequest struct {
	Body struct {
		MerchantID    *string `json:"merchant_id,omitempty" format:"uuid" doc:"Merchant UUID whose cached lookup entry should be removed"`
		AccountID     *string `json:"account_id,omitempty" format:"uuid" doc:"Account UUID whose cached balance snapshot should be removed"`
		TransactionID *string `json:"transaction_id,omitempty" format:"uuid" doc:"Transaction UUID whose cached status entry should be removed"`
		SessionToken  *string `json:"session_token,omitempty" doc:"DANTE session token whose cached session entry should be removed"`
	}
}

type cacheInvalidateResponse struct {
	Body struct {
		Message     string   `json:"message" doc:"Human-readable invalidation result"`
		Invalidated []string `json:"invalidated" doc:"Cache key categories that were invalidated"`
	}
}

type systemStatusResponse struct {
	Body systemStatusDTO
}

type accountProfileResponse struct {
	Body accountProfileDTO
}

type accountBalanceResponse struct {
	Body accountBalanceDTO
}

type queueStatusResponse struct {
	Body queueStatusDTO
}

type queueSnapshotDTO struct {
	Name      string `json:"name" doc:"RabbitMQ queue name"`
	Messages  int    `json:"messages" doc:"Current RabbitMQ message depth for the queue"`
	Consumers int    `json:"consumers" doc:"Current active RabbitMQ consumer count for the queue"`
}

type merchantDTO struct {
	ID        string `json:"id" doc:"Merchant UUID"`
	Name      string `json:"name" doc:"Merchant display name"`
	QRISCode  string `json:"qris_code" doc:"Merchant QRIS code"`
	Category  string `json:"category,omitempty" doc:"Optional merchant category label"`
	CreatedAt string `json:"created_at" doc:"Merchant record creation timestamp in RFC3339 format"`
}

type accountProfileDTO struct {
	ID            string `json:"id" doc:"Legacy account identifier"`
	UserID        string `json:"user_id" doc:"Local user UUID that owns the account"`
	AccountNumber string `json:"account_number" doc:"Local account number mapped to the authenticated legacy account"`
	CustomerID    string `json:"customer_id" doc:"Authoritative customer identifier returned by legacy"`
	CustomerName  string `json:"customer_name" doc:"Customer display name returned by legacy"`
	CreatedAt     string `json:"created_at" doc:"Account record creation timestamp in RFC3339 format"`
}

type accountBalanceDTO struct {
	AccountID          string `json:"account_id" doc:"Legacy account identifier"`
	ReferenceAccountID string `json:"reference_account_id" doc:"Legacy account reference used by the authoritative balance source"`
	Balance            int64  `json:"balance" doc:"Balance amount in the smallest currency unit"`
	Source             string `json:"source" doc:"Whether the balance came from Redis cache or a fresh legacy lookup"`
	FetchedAt          string `json:"fetched_at" doc:"Timestamp when the current balance snapshot was fetched, in RFC3339 format"`
}

type authSessionDTO struct {
	Token         string `json:"token" doc:"DANTE-issued bearer token for subsequent client requests"`
	CustomerID    string `json:"customer_id" doc:"Authoritative customer identifier returned by legacy"`
	AccountID     string `json:"account_id" doc:"Authoritative legacy account identifier returned by upstream banking services"`
	AccountNumber string `json:"account_number" doc:"Legacy account number associated with the authenticated session"`
	CustomerName  string `json:"customer_name" doc:"Customer display name returned by legacy"`
	ExpiresAt     string `json:"expires_at" doc:"RFC3339 expiration timestamp for the DANTE-issued session token"`
}

type authSessionStateDTO struct {
	Token         string `json:"token" doc:"Validated DANTE-issued bearer token"`
	CustomerID    string `json:"customer_id" doc:"Authoritative customer identifier returned by legacy"`
	AccountID     string `json:"account_id" doc:"Authoritative legacy account identifier returned by upstream banking services"`
	AccountNumber string `json:"account_number" doc:"Legacy account number associated with the authenticated session"`
	CustomerName  string `json:"customer_name" doc:"Customer display name returned by legacy"`
	ExpiresAt     string `json:"expires_at" doc:"RFC3339 expiration timestamp for the DANTE-issued session token"`
	CreatedAt     string `json:"created_at" doc:"RFC3339 timestamp when DANTE created the client session"`
}

type transactionStatusDTO struct {
	ID          string  `json:"id" doc:"Transaction UUID"`
	Status      string  `json:"status" doc:"Current transaction lifecycle status"`
	RequestedAt string  `json:"requested_at" doc:"Timestamp when the transaction was first requested, in RFC3339 format"`
	ProcessedAt *string `json:"processed_at,omitempty" doc:"Timestamp when the transaction finished processing, if available, in RFC3339 format"`
	UpdatedAt   string  `json:"updated_at" doc:"Timestamp of the latest local transaction update, in RFC3339 format"`
}

type transactionDetailDTO struct {
	ID                string  `json:"id" doc:"Transaction UUID"`
	UserID            string  `json:"user_id" doc:"User UUID associated with the transaction"`
	MerchantID        *string `json:"merchant_id,omitempty" doc:"Merchant UUID associated with the transaction when the transaction is merchant-backed"`
	AccountID         string  `json:"account_id" doc:"Account UUID charged by the transaction"`
	Amount            int64   `json:"amount" doc:"Transaction amount in smallest currency unit"`
	Status            string  `json:"status" doc:"Current transaction lifecycle status"`
	IdempotencyKey    string  `json:"idempotency_key" doc:"Idempotency key used to deduplicate payment creation"`
	LegacyReferenceID *string `json:"legacy_reference_id,omitempty" doc:"Reference ID returned by the legacy backend when available"`
	FailureReason     *string `json:"failure_reason,omitempty" doc:"Failure reason when the transaction ends unsuccessfully"`
	RequestedAt       string  `json:"requested_at" doc:"Timestamp when the transaction was requested, in RFC3339 format"`
	ProcessedAt       *string `json:"processed_at,omitempty" doc:"Timestamp when the transaction finished processing, if available, in RFC3339 format"`
	CreatedAt         string  `json:"created_at" doc:"Timestamp when the local transaction record was created, in RFC3339 format"`
	UpdatedAt         string  `json:"updated_at" doc:"Timestamp when the local transaction record was last updated, in RFC3339 format"`
}

type accountTransactionsDTO struct {
	Items      []transactionDetailDTO `json:"items" doc:"Transaction records in newest-first order"`
	NextCursor *string                `json:"next_cursor,omitempty" doc:"Opaque cursor for the next page when more items are available"`
}

type cacheStatsDTO struct {
	Metrics []cachemetrics.SnapshotEntry `json:"metrics" doc:"Application-level cache counters using Prometheus-style names"`
}

type systemStatusDTO struct {
	Status       string                      `json:"status" doc:"Overall internal dependency state"`
	Dependencies map[string]dependencyStatus `json:"dependencies" doc:"Per-dependency status details"`
}

type queueStatusDTO struct {
	Status string                      `json:"status" doc:"Overall queue connectivity state"`
	Queues map[string]queueSnapshotDTO `json:"queues" doc:"Configured application queues keyed by queue name with live depth and consumer counts"`
	Broker dependencyStatus            `json:"broker" doc:"RabbitMQ broker connectivity state"`
}

type dependencyStatus struct {
	Status string `json:"status" doc:"Dependency state such as ok, error, or stub"`
	Detail string `json:"detail,omitempty" doc:"Optional detail describing the current dependency state"`
}

const openAPIDescription = `DANTE is a middleware layer in front of a legacy banking system, designed to improve latency, resilience, and operational visibility for QRIS-style and real-time transaction workloads.

### Current Scope

- Read-side APIs for merchants, transaction status, transaction detail, and account transaction history
- Redis-backed cache strategies for low-latency lookups and backend protection
- PostgreSQL as the local source of truth for transaction data
- Prometheus-friendly metrics and internal operational endpoints

### Architecture Notes

- Merchant reads follow the path: **Redis -> PostgreSQL -> Legacy**
- Transaction status follows the path: **Redis -> PostgreSQL**
- Transaction detail and account transaction history currently read from **PostgreSQL directly**
- Redis is used for cache and short-lived coordination only; it is **not** the source of truth

### Delivery Status

This documentation reflects the current development build. Auth/session issuance, account profile and balance reads, QRIS and transfer intake, RabbitMQ publishing, async worker execution, and bounded retry with dead-letter routing are active. Broader transaction retry controls and additional operational hardening are still in progress.
`

func Start() {
	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := tracing.Init(ctx, cfg.Observability, cfg.App)
	if err != nil {
		log.Fatalf("tracing startup failed: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := shutdownTracing(shutdownCtx); err != nil {
			log.Printf("tracing shutdown failed: %v", err)
		}
	}()

	redisClient, err := cache.Open(ctx, cfg.Redis)
	if err != nil {
		log.Fatalf("redis startup failed: %v", err)
	}
	defer redisClient.Close()

	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		log.Fatalf("database startup failed: %v", err)
	}
	defer db.Close()

	cacheClient := cache.NewClient(redisClient)
	store := database.NewStore(db)
	cacheStats := cachemetrics.NewInMemoryRecorder()
	legacyClient := legacy.NewClient(cfg.Legacy)
	legacyMerchantClient := legacy.NewCircuitBreakingMerchantClient(
		legacy.NoopMerchantClient{},
		legacy.CircuitBreakerConfig{
			FailureThreshold: 3,
			SuccessThreshold: 1,
			OpenTimeout:      5 * time.Second,
		},
	)
	qrisPublisher := queue.NewPublisher(cfg.RabbitMQ)
	authSvc := authservice.NewService(cacheClient, legacyClient, store.Queries)
	accountSvc := accountservice.NewService(store.Queries, cacheClient, legacyClient)
	merchantSvc := merchantservice.NewService(cacheClient, store.Queries, legacyMerchantClient, cacheStats)
	qrisPaymentSvc := paymentservice.NewQRISService(store.Queries, cacheClient, qrisPublisher, legacyClient)
	qrisWorker := paymentservice.NewQRISWorker(store.Queries, cacheClient, cacheClient, legacyClient)
	transferSvc := paymentservice.NewTransferService(store.Queries, cacheClient, qrisPublisher)
	transferWorker := paymentservice.NewTransferWorker(store.Queries, cacheClient, cacheClient, legacyClient)
	transactionStatusSvc := transactionservice.NewStatusService(cacheClient, store.Queries, cacheStats)
	transactionDetailSvc := transactionservice.NewDetailService(store.Queries)
	transactionHistorySvc := transactionservice.NewHistoryService(store.Queries)
	metricsHandler := observabilitymetrics.NewHandler(observabilitymetrics.Config{
		Service:     "dante-api-systems",
		Version:     cfg.App.Version,
		Environment: cfg.App.Environment,
	}, cacheStats, db, redisClient, cfg.RabbitMQ)
	qrisConsumer := queue.NewConsumer(cfg.RabbitMQ, metricsHandler)

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)

	// Observability middleware.
	// Tracing harus dipasang sebelum request masuk ke Huma handler.
	// Metrics dipasang agar setiap endpoint punya request count dan latency histogram.
	router.Use(httpobs.Tracing(cfg.Observability.ServiceName))
	router.Use(httpobs.Metrics(metricsHandler))

	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)

	// Rate limiting untuk melindungi public endpoints dari abuse.
	// Menggunakan token bucket algorithm (golang.org/x/time/rate) dengan per-IP tracking.
	// Default: 5 req/sec per IP dengan burst 10.
	// Endpoint internal seperti /health, /metrics, /internal/* di-exclude dari rate limiting.
	rateLimiter := ratelimit.NewRateLimiter(5.0, 10)
	router.Use(rateLimiter.Handler())

	router.Handle("/metrics", metricsHandler)

	apiConfig := huma.DefaultConfig("DANTE API Systems", "0.1.0")
	apiConfig.Info.Description = openAPIDescription
	apiConfig.Info.Contact = &huma.Contact{
		Name: "DANTE API Team",
	}
	apiConfig.Servers = []*huma.Server{
		{
			URL:         "http://localhost:8080",
			Description: "Local development gateway exposed through Nginx",
		},
	}
	apiConfig.Tags = []*huma.Tag{
		{
			Name:        "System",
			Description: "Liveness, readiness, service metadata, and platform-level runtime information.",
		},
		{
			Name:        "Auth",
			Description: "Client authentication endpoints where DANTE validates credentials through legacy and issues its own session token.",
		},
		{
			Name:        "Merchants",
			Description: "Merchant lookup endpoints optimized with Redis cache-aside, negative caching, and stampede protection.",
		},
		{
			Name:        "Transactions",
			Description: "Transaction status, detail, and history endpoints backed by PostgreSQL with selective Redis acceleration.",
		},
		{
			Name:        "Accounts",
			Description: "Account profile, balance, and transaction history endpoints backed by local mappings with controlled legacy refresh where needed.",
		},
		{
			Name:        "Cache",
			Description: "Internal cache diagnostics for Redis-backed read behavior and application-level cache metrics.",
		},
		{
			Name:        "Internal",
			Description: "Operational-only endpoints intended for diagnostics, dependency inspection, and support workflows.",
		},
	}
	if cfg.App.IsDevelopment() {
		apiConfig.OpenAPIPath = "/openapi"
		apiConfig.DocsPath = "/docs"
	} else {
		apiConfig.OpenAPIPath = ""
		apiConfig.DocsPath = ""
		apiConfig.SchemasPath = ""
	}

	api := humachi.New(router, apiConfig)

	if cfg.RabbitMQ.URL != "" {
		for i := 0; i < normalizedWorkerCount(cfg.RabbitMQ.QRISWorkers); i++ {
			go func(workerIndex int) {
				if err := qrisConsumer.RunQRISPaymentWorker(ctx, qrisWorker); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("qris worker %d stopped: %v", workerIndex+1, err)
				}
			}(i)
		}
		for i := 0; i < normalizedWorkerCount(cfg.RabbitMQ.TransferWorkers); i++ {
			go func(workerIndex int) {
				if err := qrisConsumer.RunTransferWorker(ctx, transferWorker); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("transfer worker %d stopped: %v", workerIndex+1, err)
				}
			}(i)
		}
	}

	huma.Register(api, huma.Operation{
		OperationID: "get-root",
		Method:      http.MethodGet,
		Path:        "/",
		Summary:     "Get service root",
		Description: "Returns a simple human-readable confirmation that the DANTE API service is running.",
		Tags:        []string{"System"},
	}, func(ctx context.Context, input *struct{}) (*messageResponse, error) {
		resp := &messageResponse{}
		resp.Body.Message = "DANTE API Systems is running"
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-health",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "Get liveness health",
		Description: "Returns a minimal liveness response for process-level health checks. This endpoint does not perform dependency verification.",
		Tags:        []string{"System"},
	}, func(ctx context.Context, input *struct{}) (*healthResponse, error) {
		resp := &healthResponse{}
		resp.Body.Status = "ok"
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-ready",
		Method:      http.MethodGet,
		Path:        "/ready",
		Summary:     "Get readiness status",
		Description: "Checks whether the service is ready to serve requests by verifying required runtime dependencies such as PostgreSQL and Redis.",
		Tags:        []string{"System"},
	}, func(ctx context.Context, input *struct{}) (*readyResponse, error) {
		readyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		dependencies := map[string]string{
			"database": "ok",
			"redis":    "ok",
		}

		dbReadyCtx, dbReadySpan := tracing.StartClientSpan(readyCtx, "postgres", "postgres.ready",
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "PING"),
		)
		if err := db.PingContext(dbReadyCtx); err != nil {
			tracing.EndSpan(dbReadySpan, err)
			dependencies["database"] = "error"
		} else {
			tracing.EndSpan(dbReadySpan, nil)
		}

		redisReadyCtx, redisReadySpan := tracing.StartClientSpan(readyCtx, "redis", "redis.ready",
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "PING"),
		)
		if err := redisClient.Ping(redisReadyCtx).Err(); err != nil {
			tracing.EndSpan(redisReadySpan, err)
			dependencies["redis"] = "error"
		} else {
			tracing.EndSpan(redisReadySpan, nil)
		}

		resp := &readyResponse{}
		resp.Body.Dependencies = dependencies
		resp.Body.Status = "ok"

		if dependencies["database"] != "ok" || dependencies["redis"] != "ok" {
			resp.Body.Status = "degraded"
			return nil, huma.Error503ServiceUnavailable("service dependencies are not ready")
		}

		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-info",
		Method:      http.MethodGet,
		Path:        "/info",
		Summary:     "Get service metadata",
		Description: "Returns static metadata about the running DANTE service such as environment and version.",
		Tags:        []string{"System"},
	}, func(ctx context.Context, input *struct{}) (*infoResponse, error) {
		resp := &infoResponse{}
		resp.Body.Service = "dante-api-systems"
		resp.Body.Version = cfg.App.Version
		resp.Body.Environment = cfg.App.Environment
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "post-auth-register",
		Method:      http.MethodPost,
		Path:        "/v1/auth/register",
		Summary:     "Register a new customer account",
		Description: "Creates a customer account in the legacy banking system, then immediately logs the customer in and issues a DANTE session token for subsequent requests.",
		Tags:        []string{"Auth"},
	}, func(ctx context.Context, input *authRegisterRequest) (*authRegisterResponse, error) {
		result, err := authSvc.Register(ctx, input.Body.Name, input.Body.Email, input.Body.Password, input.Body.PIN)
		if err != nil {
			switch {
			case errors.Is(err, authservice.ErrEmailAlreadyRegistered):
				return nil, huma.Error409Conflict("email is already registered")
			case errors.Is(err, authservice.ErrExpiredSession):
				return nil, huma.Error503ServiceUnavailable("legacy registration produced an expired session")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to register against legacy", err)
			}
		}

		resp := &authRegisterResponse{}
		resp.Body = mapAuthSessionResponse(*result)
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "post-auth-login",
		Method:      http.MethodPost,
		Path:        "/v1/auth/login",
		Summary:     "Authenticate a client session",
		Description: "Validates username and password through the legacy bank service, then issues a DANTE-managed session token for subsequent client requests.",
		Tags:        []string{"Auth"},
	}, func(ctx context.Context, input *authLoginRequest) (*authLoginResponse, error) {
		result, err := authSvc.Login(ctx, input.Body.Username, input.Body.Password)
		if err != nil {
			switch {
			case errors.Is(err, authservice.ErrInvalidCredentials):
				return nil, huma.Error401Unauthorized("invalid username or password")
			case errors.Is(err, authservice.ErrExpiredSession):
				return nil, huma.Error503ServiceUnavailable("legacy returned an already-expired session")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to authenticate against legacy", err)
			}
		}

		resp := &authLoginResponse{}
		resp.Body = mapAuthSessionResponse(*result)
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "post-auth-logout",
		Method:      http.MethodPost,
		Path:        "/v1/auth/logout",
		Summary:     "Invalidate a client session",
		Description: "Invalidates the DANTE-issued session token and the associated legacy session reference.",
		Tags:        []string{"Auth"},
	}, func(ctx context.Context, input *authLogoutRequest) (*authLogoutResponse, error) {
		if err := authSvc.Logout(ctx, input.Body.Token); err != nil {
			switch {
			case errors.Is(err, authservice.ErrInvalidToken):
				return nil, huma.Error401Unauthorized("invalid or expired token")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to logout session", err)
			}
		}

		resp := &authLogoutResponse{}
		resp.Body.Message = "logout successful"
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-auth-session",
		Method:      http.MethodGet,
		Path:        "/v1/auth/session",
		Summary:     "Inspect the current client session",
		Description: "Validates the DANTE-issued bearer token and returns the current authenticated session metadata.",
		Tags:        []string{"Auth"},
	}, func(ctx context.Context, input *authSessionHeaders) (*authSessionResponse, error) {
		token, err := parseBearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing or invalid bearer token")
		}

		session, err := authSvc.GetSession(ctx, token)
		if err != nil {
			switch {
			case errors.Is(err, authservice.ErrInvalidToken), errors.Is(err, authservice.ErrExpiredSession):
				return nil, huma.Error401Unauthorized("invalid or expired token")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to validate session", err)
			}
		}

		resp := &authSessionResponse{}
		resp.Body = mapAuthSessionStateResponse(*session)
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "post-qris-payment",
		Method:      http.MethodPost,
		Path:        "/v1/payments/qris",
		Summary:     "Create a QRIS payment transaction",
		Description: "Validates the authenticated session, creates a durable local transaction in PROCESSING state, caches the fast status, and publishes async QRIS work into RabbitMQ.",
		Tags:        []string{"Transactions", "Auth"},
	}, func(ctx context.Context, input *qrisPaymentRequest) (*qrisPaymentResponse, error) {
		token, err := parseBearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing or invalid bearer token")
		}
		if input.IdempotencyKey == "" {
			return nil, huma.Error400BadRequest("missing Idempotency-Key header")
		}

		session, err := authSvc.GetSession(ctx, token)
		if err != nil {
			switch {
			case errors.Is(err, authservice.ErrInvalidToken), errors.Is(err, authservice.ErrExpiredSession):
				return nil, huma.Error401Unauthorized("invalid or expired token")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to validate session", err)
			}
		}

		if input.Body.MerchantID == "" {
			return nil, huma.Error400BadRequest("merchant_id is required")
		}

		result, err := qrisPaymentSvc.CreateTransaction(ctx, paymentservice.QRISRequest{
			Session:        *session,
			MerchantRef:    input.Body.MerchantID,
			Amount:         input.Body.Amount,
			IdempotencyKey: input.IdempotencyKey,
		})
		if err != nil {
			switch {
			case errors.Is(err, paymentservice.ErrIdempotencyConflict):
				return nil, huma.Error409Conflict("idempotency key already belongs to a different payment request")
			case errors.Is(err, paymentservice.ErrAccountNotProvisioned):
				return nil, huma.Error403Forbidden("authenticated account is not provisioned in dante")
			case errors.Is(err, paymentservice.ErrMerchantNotFound):
				return nil, huma.Error404NotFound("merchant not found")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to accept qris payment", err)
			}
		}

		resp := &qrisPaymentResponse{}
		resp.Body = mapTransactionDetailResponse(transactionservice.DetailView{
			ID:             result.Transaction.ID,
			UserID:         result.Transaction.UserID,
			AccountID:      result.Transaction.AccountID,
			Amount:         result.Transaction.Amount,
			Status:         result.Transaction.Status,
			IdempotencyKey: result.Transaction.IdempotencyKey,
			RequestedAt:    result.Transaction.RequestedAt,
			CreatedAt:      result.Transaction.CreatedAt,
			UpdatedAt:      result.Transaction.UpdatedAt,
		})
		if result.Transaction.MerchantID.Valid {
			merchantID := result.Transaction.MerchantID.UUID
			resp.Body.MerchantID = stringPtr(merchantID.String())
		}
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "post-transfer",
		Method:      http.MethodPost,
		Path:        "/v1/transfers",
		Summary:     "Create a transfer transaction",
		Description: "Validates the authenticated session, requires a transaction PIN for financial authorization, creates a durable local transfer transaction in PROCESSING state, caches the fast status, and publishes async transfer work into RabbitMQ.",
		Tags:        []string{"Transactions", "Auth", "Accounts"},
	}, func(ctx context.Context, input *transferRequest) (*transferResponse, error) {
		token, err := parseBearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing or invalid bearer token")
		}
		if input.IdempotencyKey == "" {
			return nil, huma.Error400BadRequest("missing Idempotency-Key header")
		}
		if input.Body.TransactionPIN == "" {
			return nil, huma.Error400BadRequest("missing transaction_pin")
		}

		session, err := authSvc.GetSession(ctx, token)
		if err != nil {
			switch {
			case errors.Is(err, authservice.ErrInvalidToken), errors.Is(err, authservice.ErrExpiredSession):
				return nil, huma.Error401Unauthorized("invalid or expired token")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to validate session", err)
			}
		}

		result, err := transferSvc.CreateTransaction(ctx, paymentservice.TransferRequest{
			Session:         *session,
			ToAccountNumber: input.Body.ToAccountNumber,
			Amount:          input.Body.Amount,
			TransactionPIN:  input.Body.TransactionPIN,
			IdempotencyKey:  input.IdempotencyKey,
		})
		if err != nil {
			switch {
			case errors.Is(err, paymentservice.ErrIdempotencyConflict):
				return nil, huma.Error409Conflict("idempotency key already belongs to a different transfer request")
			case errors.Is(err, paymentservice.ErrAccountNotProvisioned):
				return nil, huma.Error403Forbidden("authenticated account is not provisioned in dante")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to accept transfer", err)
			}
		}

		resp := &transferResponse{}
		resp.Body = mapTransactionDetailResponse(transactionservice.DetailView{
			ID:             result.Transaction.ID,
			UserID:         result.Transaction.UserID,
			AccountID:      result.Transaction.AccountID,
			Amount:         result.Transaction.Amount,
			Status:         result.Transaction.Status,
			IdempotencyKey: result.Transaction.IdempotencyKey,
			RequestedAt:    result.Transaction.RequestedAt,
			CreatedAt:      result.Transaction.CreatedAt,
			UpdatedAt:      result.Transaction.UpdatedAt,
		})
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-account-profile",
		Method:      http.MethodGet,
		Path:        "/v1/accounts/{accountId}",
		Summary:     "Get account profile",
		Description: "Returns the authenticated account profile for the requested legacy account ID using the DANTE session plus the local account mapping.",
		Tags:        []string{"Accounts", "Auth"},
	}, func(ctx context.Context, input *accountProfileRequest) (*accountProfileResponse, error) {
		token, err := parseBearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing or invalid bearer token")
		}

		session, err := authSvc.GetSession(ctx, token)
		if err != nil {
			switch {
			case errors.Is(err, authservice.ErrInvalidToken), errors.Is(err, authservice.ErrExpiredSession):
				return nil, huma.Error401Unauthorized("invalid or expired token")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to validate session", err)
			}
		}

		account, err := resolveAuthenticatedLocalAccount(ctx, store.Queries, session, input.AccountID)
		if err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return nil, huma.Error404NotFound("account not found")
			case errors.Is(err, accountservice.ErrAccountAccessDenied):
				return nil, huma.Error403Forbidden("requested account does not belong to the authenticated session")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to resolve authenticated account", err)
			}
		}

		profile, err := accountSvc.GetProfile(ctx, account.ID, *session)
		if err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return nil, huma.Error404NotFound("account not found")
			case errors.Is(err, accountservice.ErrAccountAccessDenied):
				return nil, huma.Error403Forbidden("requested account does not belong to the authenticated session")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to fetch account profile", err)
			}
		}

		resp := &accountProfileResponse{}
		resp.Body = accountProfileDTO{
			ID:            session.LegacyAccountID,
			UserID:        profile.UserID.String(),
			AccountNumber: profile.AccountNumber,
			CustomerID:    profile.CustomerID,
			CustomerName:  profile.CustomerName,
			CreatedAt:     profile.CreatedAt.UTC().Format(time.RFC3339),
		}
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-account-balance",
		Method:      http.MethodGet,
		Path:        "/v1/accounts/{accountId}/balance",
		Summary:     "Get account balance",
		Description: "Returns a cached balance snapshot when available, otherwise refreshes the balance from legacy using the authenticated session plus the transaction PIN header.",
		Tags:        []string{"Accounts", "Auth"},
	}, func(ctx context.Context, input *accountBalanceRequest) (*accountBalanceResponse, error) {
		token, err := parseBearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing or invalid bearer token")
		}
		if input.TransactionPIN == "" {
			return nil, huma.Error400BadRequest("missing X-Transaction-PIN header")
		}

		session, err := authSvc.GetSession(ctx, token)
		if err != nil {
			switch {
			case errors.Is(err, authservice.ErrInvalidToken), errors.Is(err, authservice.ErrExpiredSession):
				return nil, huma.Error401Unauthorized("invalid or expired token")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to validate session", err)
			}
		}

		account, err := resolveAuthenticatedLocalAccount(ctx, store.Queries, session, input.AccountID)
		if err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return nil, huma.Error404NotFound("account not found")
			case errors.Is(err, accountservice.ErrAccountAccessDenied):
				return nil, huma.Error403Forbidden("requested account does not belong to the authenticated session")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to resolve authenticated account", err)
			}
		}

		balance, err := accountSvc.GetBalance(ctx, account.ID, *session, input.TransactionPIN)
		if err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return nil, huma.Error404NotFound("account not found")
			case errors.Is(err, accountservice.ErrAccountAccessDenied):
				return nil, huma.Error403Forbidden("requested account does not belong to the authenticated session")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to fetch account balance", err)
			}
		}

		resp := &accountBalanceResponse{}
		resp.Body = accountBalanceDTO{
			AccountID:          session.LegacyAccountID,
			ReferenceAccountID: balance.ReferenceAccountID,
			Balance:            balance.Balance,
			Source:             balance.Source,
			FetchedAt:          balance.FetchedAt.UTC().Format(time.RFC3339),
		}
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-merchant",
		Method:      http.MethodGet,
		Path:        "/v1/merchants/{merchantId}",
		Summary:     "Get merchant by ID",
		Description: "Returns merchant information for a QRIS-style merchant lookup using the Redis -> PostgreSQL -> Legacy read path.",
		Tags:        []string{"Merchants"},
	}, func(ctx context.Context, input *merchantPathParams) (*merchantResponse, error) {
		merchantID, err := uuid.Parse(input.MerchantID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid merchant id", err)
		}

		merchant, err := merchantSvc.GetMerchant(ctx, merchantID)
		if err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return nil, huma.Error404NotFound("merchant not found")
			case errors.Is(err, legacy.ErrLookupInProgress):
				return nil, huma.Error503ServiceUnavailable("merchant lookup is already in progress")
			case errors.Is(err, legacy.ErrUnavailable):
				return nil, huma.Error503ServiceUnavailable("merchant lookup requires legacy fallback, but legacy is unavailable")
			default:
				return nil, huma.Error500InternalServerError("failed to fetch merchant", err)
			}
		}

		resp := &merchantResponse{}
		resp.Body = mapMerchantResponse(*merchant)
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-transaction-status",
		Method:      http.MethodGet,
		Path:        "/v1/transactions/{transactionId}/status",
		Summary:     "Get transaction status",
		Description: "Returns the current transaction status using Redis first and PostgreSQL as the authoritative fallback.",
		Tags:        []string{"Transactions"},
	}, func(ctx context.Context, input *transactionPathParams) (*transactionStatusResponse, error) {
		transactionID, err := uuid.Parse(input.TransactionID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid transaction id", err)
		}

		status, err := transactionStatusSvc.GetStatus(ctx, transactionID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, huma.Error404NotFound("transaction not found")
			}
			return nil, huma.Error500InternalServerError("failed to fetch transaction status", err)
		}

		resp := &transactionStatusResponse{}
		resp.Body = mapTransactionStatusResponse(*status)
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-transaction-detail",
		Method:      http.MethodGet,
		Path:        "/v1/transactions/{transactionId}",
		Summary:     "Get transaction detail",
		Description: "Returns the full locally stored transaction record from PostgreSQL for the requested transaction ID.",
		Tags:        []string{"Transactions"},
	}, func(ctx context.Context, input *transactionPathParams) (*transactionDetailResponse, error) {
		transactionID, err := uuid.Parse(input.TransactionID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid transaction id", err)
		}

		detail, err := transactionDetailSvc.GetDetail(ctx, transactionID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, huma.Error404NotFound("transaction not found")
			}
			return nil, huma.Error500InternalServerError("failed to fetch transaction detail", err)
		}

		resp := &transactionDetailResponse{}
		resp.Body = mapTransactionDetailResponse(*detail)
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-account-transactions",
		Method:      http.MethodGet,
		Path:        "/v1/accounts/{accountId}/transactions",
		Summary:     "List account transactions",
		Description: "Returns cursor-paginated transaction history for the authenticated legacy account ordered from newest to oldest.",
		Tags:        []string{"Accounts", "Transactions"},
	}, func(ctx context.Context, input *accountTransactionsRequest) (*accountTransactionsResponse, error) {
		token, err := parseBearerToken(input.Authorization)
		if err != nil {
			return nil, huma.Error401Unauthorized("missing or invalid bearer token")
		}

		session, err := authSvc.GetSession(ctx, token)
		if err != nil {
			switch {
			case errors.Is(err, authservice.ErrInvalidToken), errors.Is(err, authservice.ErrExpiredSession):
				return nil, huma.Error401Unauthorized("invalid or expired token")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to validate session", err)
			}
		}

		account, err := resolveAuthenticatedLocalAccount(ctx, store.Queries, session, input.AccountID)
		if err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return nil, huma.Error404NotFound("account not found")
			case errors.Is(err, accountservice.ErrAccountAccessDenied):
				return nil, huma.Error403Forbidden("requested account does not belong to the authenticated session")
			default:
				return nil, huma.Error503ServiceUnavailable("failed to resolve authenticated account", err)
			}
		}

		limit, err := transactionservice.NormalizeLimit(input.Limit)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid limit", err)
		}

		page, err := transactionHistorySvc.ListByAccount(ctx, transactionservice.HistoryParams{
			AccountID: account.ID,
			Limit:     limit,
			Cursor:    input.Cursor,
		})
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to fetch account transaction history", err)
		}

		resp := &accountTransactionsResponse{}
		resp.Body.Items = make([]transactionDetailDTO, 0, len(page.Items))
		for _, item := range page.Items {
			dto := mapTransactionDetailResponse(item)
			dto.AccountID = session.LegacyAccountID
			resp.Body.Items = append(resp.Body.Items, dto)
		}
		resp.Body.NextCursor = page.NextCursor
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "post-internal-cache-invalidate",
		Method:      http.MethodPost,
		Path:        "/internal/cache/invalidate",
		Summary:     "Invalidate internal cache entries",
		Description: "Deletes selected Redis-backed cache entries for merchant lookups, account balance snapshots, transaction status snapshots, or DANTE-issued session tokens.",
		Tags:        []string{"Internal", "Cache"},
	}, func(ctx context.Context, input *cacheInvalidateRequest) (*cacheInvalidateResponse, error) {
		invalidated := make([]string, 0, 4)

		if input.Body.MerchantID == nil && input.Body.AccountID == nil && input.Body.TransactionID == nil && input.Body.SessionToken == nil {
			return nil, huma.Error400BadRequest("at least one cache target must be provided")
		}

		if input.Body.MerchantID != nil {
			merchantID, err := uuid.Parse(*input.Body.MerchantID)
			if err != nil {
				return nil, huma.Error400BadRequest("invalid merchant_id", err)
			}
			if err := cacheClient.DeleteMerchant(ctx, merchantID); err != nil {
				return nil, huma.Error503ServiceUnavailable("failed to invalidate merchant cache", err)
			}
			invalidated = append(invalidated, "merchant")
		}

		if input.Body.AccountID != nil {
			accountID, err := uuid.Parse(*input.Body.AccountID)
			if err != nil {
				return nil, huma.Error400BadRequest("invalid account_id", err)
			}
			if err := cacheClient.DeleteAccountBalance(ctx, accountID); err != nil {
				return nil, huma.Error503ServiceUnavailable("failed to invalidate account balance cache", err)
			}
			invalidated = append(invalidated, "account_balance")
		}

		if input.Body.TransactionID != nil {
			transactionID, err := uuid.Parse(*input.Body.TransactionID)
			if err != nil {
				return nil, huma.Error400BadRequest("invalid transaction_id", err)
			}
			if err := cacheClient.DeleteTransactionStatus(ctx, transactionID); err != nil {
				return nil, huma.Error503ServiceUnavailable("failed to invalidate transaction status cache", err)
			}
			invalidated = append(invalidated, "transaction_status")
		}

		if input.Body.SessionToken != nil {
			if *input.Body.SessionToken == "" {
				return nil, huma.Error400BadRequest("session_token must not be empty")
			}
			if err := cacheClient.DeleteSession(ctx, *input.Body.SessionToken); err != nil {
				return nil, huma.Error503ServiceUnavailable("failed to invalidate session cache", err)
			}
			invalidated = append(invalidated, "session")
		}

		resp := &cacheInvalidateResponse{}
		resp.Body.Message = "cache invalidation completed"
		resp.Body.Invalidated = invalidated
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-internal-cache-stats",
		Method:      http.MethodGet,
		Path:        "/internal/cache/stats",
		Summary:     "Get internal cache stats",
		Description: "Returns in-process cache counters using Prometheus-style metric names for Redis-backed cache behavior.",
		Tags:        []string{"Internal", "Cache"},
	}, func(ctx context.Context, input *struct{}) (*cacheStatsResponse, error) {
		resp := &cacheStatsResponse{}
		resp.Body.Metrics = cacheStats.Snapshot()
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-internal-system-status",
		Method:      http.MethodGet,
		Path:        "/internal/system/status",
		Summary:     "Get internal system status",
		Description: "Returns dependency status for PostgreSQL, Redis, RabbitMQ, and the current legacy adapter state.",
		Tags:        []string{"Internal", "System"},
	}, func(ctx context.Context, input *struct{}) (*systemStatusResponse, error) {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		dependencies := map[string]dependencyStatus{
			"database": {Status: "ok"},
			"redis":    {Status: "ok"},
			"rabbitmq": {Status: "ok"},
			"legacy":   legacyDependencyStatus(checkCtx, legacyClient),
		}

		dbCheckCtx, dbCheckSpan := tracing.StartClientSpan(checkCtx, "postgres", "postgres.system_status",
			attribute.String("db.system", "postgresql"),
			attribute.String("db.operation", "PING"),
		)
		if err := db.PingContext(dbCheckCtx); err != nil {
			tracing.EndSpan(dbCheckSpan, err)
			dependencies["database"] = dependencyStatus{Status: "error", Detail: err.Error()}
		} else {
			tracing.EndSpan(dbCheckSpan, nil)
		}

		redisCheckCtx, redisCheckSpan := tracing.StartClientSpan(checkCtx, "redis", "redis.system_status",
			attribute.String("db.system", "redis"),
			attribute.String("db.operation", "PING"),
		)
		if err := redisClient.Ping(redisCheckCtx).Err(); err != nil {
			tracing.EndSpan(redisCheckSpan, err)
			dependencies["redis"] = dependencyStatus{Status: "error", Detail: err.Error()}
		} else {
			tracing.EndSpan(redisCheckSpan, nil)
		}

		if err := queue.CheckRabbitMQ(checkCtx, cfg.RabbitMQ); err != nil {
			dependencies["rabbitmq"] = dependencyStatus{Status: "error", Detail: err.Error()}
		}

		resp := &systemStatusResponse{}
		resp.Body.Dependencies = dependencies
		resp.Body.Status = "ok"

		for _, name := range []string{"database", "redis", "rabbitmq", "legacy"} {
			if dependencies[name].Status != "ok" {
				resp.Body.Status = "degraded"
				break
			}
		}

		if resp.Body.Status != "ok" {
			return nil, huma.Error503ServiceUnavailable("one or more system dependencies are unavailable")
		}

		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-internal-queue-status",
		Method:      http.MethodGet,
		Path:        "/internal/queue/status",
		Summary:     "Get internal queue status",
		Description: "Returns operational queue configuration, live depth, active consumer counts, and RabbitMQ broker connectivity for async transaction intake.",
		Tags:        []string{"Internal"},
	}, func(ctx context.Context, input *struct{}) (*queueStatusResponse, error) {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		brokerStatus := dependencyStatus{Status: "ok"}
		queueSnapshots := map[string]queueSnapshotDTO{}
		if err := queue.CheckRabbitMQ(checkCtx, cfg.RabbitMQ); err != nil {
			brokerStatus = dependencyStatus{Status: "error", Detail: err.Error()}
		} else {
			stats, err := queue.InspectConfiguredQueues(checkCtx, cfg.RabbitMQ)
			if err != nil {
				brokerStatus = dependencyStatus{Status: "error", Detail: err.Error()}
			} else {
				for key, stat := range stats {
					queueSnapshots[key] = queueSnapshotDTO{
						Name:      stat.Name,
						Messages:  stat.Messages,
						Consumers: stat.Consumers,
					}
				}
			}
		}

		resp := &queueStatusResponse{}
		resp.Body.Queues = queueSnapshots
		resp.Body.Broker = brokerStatus
		resp.Body.Status = "ok"
		if brokerStatus.Status != "ok" {
			resp.Body.Status = "degraded"
			return nil, huma.Error503ServiceUnavailable("rabbitmq broker is unavailable")
		}

		return resp, nil
	})

	addr := ":" + cfg.App.Port
	log.Printf("listening on %s", addr)

	if err := http.ListenAndServe(addr, router); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server failed: %v", err)
	}
}

func mapMerchantResponse(merchant dbsqlc.Merchant) merchantDTO {
	return merchantDTO{
		ID:        merchant.ID.String(),
		Name:      merchant.Name,
		QRISCode:  merchant.QrisCode,
		Category:  merchant.Category.String,
		CreatedAt: merchant.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func mapAuthSessionResponse(session authservice.LoginResponse) authSessionDTO {
	return authSessionDTO{
		Token:         session.Token,
		CustomerID:    session.CustomerID,
		AccountID:     session.LegacyAccountID,
		AccountNumber: session.AccountNumber,
		CustomerName:  session.CustomerName,
		ExpiresAt:     session.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func mapAuthSessionStateResponse(session authservice.SessionView) authSessionStateDTO {
	return authSessionStateDTO{
		Token:         session.Token,
		CustomerID:    session.CustomerID,
		AccountID:     session.LegacyAccountID,
		AccountNumber: session.AccountNumber,
		CustomerName:  session.CustomerName,
		ExpiresAt:     session.ExpiresAt.UTC().Format(time.RFC3339),
		CreatedAt:     session.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func legacyDependencyStatus(ctx context.Context, client *legacy.Client) dependencyStatus {
	if client == nil || client.Endpoint() == "" {
		return dependencyStatus{Status: "stub", Detail: "legacy adapter is not configured"}
	}

	if err := client.Ping(ctx); err != nil {
		return dependencyStatus{Status: "error", Detail: err.Error()}
	}

	return dependencyStatus{
		Status: "ok",
		Detail: client.Endpoint(),
	}
}

func resolveAuthenticatedLocalAccount(ctx context.Context, repo interface {
	GetAccountByNumber(ctx context.Context, accountNumber string) (dbsqlc.Account, error)
}, session *authservice.SessionView, requestedLegacyAccountID string) (dbsqlc.Account, error) {
	if session == nil || requestedLegacyAccountID != session.LegacyAccountID {
		return dbsqlc.Account{}, accountservice.ErrAccountAccessDenied
	}

	account, err := repo.GetAccountByNumber(ctx, session.AccountNumber)
	if err != nil {
		return dbsqlc.Account{}, err
	}

	return account, nil
}

func parseBearerToken(header string) (string, error) {
	const prefix = "Bearer "

	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return "", errors.New("invalid authorization header")
	}

	token := header[len(prefix):]
	if token == "" {
		return "", errors.New("empty bearer token")
	}

	return token, nil
}

func mapTransactionStatusResponse(status transactionservice.StatusView) transactionStatusDTO {
	resp := transactionStatusDTO{
		ID:          status.ID.String(),
		Status:      status.Status,
		RequestedAt: status.RequestedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   status.UpdatedAt.UTC().Format(time.RFC3339),
	}

	if status.ProcessedAt != nil {
		processedAt := status.ProcessedAt.UTC().Format(time.RFC3339)
		resp.ProcessedAt = &processedAt
	}

	return resp
}

func mapTransactionDetailResponse(detail transactionservice.DetailView) transactionDetailDTO {
	resp := transactionDetailDTO{
		ID:             detail.ID.String(),
		UserID:         detail.UserID.String(),
		AccountID:      detail.AccountID.String(),
		Amount:         detail.Amount,
		Status:         detail.Status,
		IdempotencyKey: detail.IdempotencyKey,
		RequestedAt:    detail.RequestedAt.UTC().Format(time.RFC3339),
		CreatedAt:      detail.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:      detail.UpdatedAt.UTC().Format(time.RFC3339),
	}

	if detail.LegacyReferenceID != nil {
		resp.LegacyReferenceID = detail.LegacyReferenceID
	}

	if detail.MerchantID != nil {
		resp.MerchantID = stringPtr(detail.MerchantID.String())
	}

	if detail.FailureReason != nil {
		resp.FailureReason = detail.FailureReason
	}

	if detail.ProcessedAt != nil {
		processedAt := detail.ProcessedAt.UTC().Format(time.RFC3339)
		resp.ProcessedAt = &processedAt
	}

	return resp
}

func stringPtr(value string) *string {
	return &value
}

func normalizedWorkerCount(value int) int {
	if value > 0 {
		return value
	}
	return 1
}
