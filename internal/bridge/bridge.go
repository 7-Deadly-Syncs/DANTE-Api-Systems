package bridge

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/cache"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database"
	dbsqlc "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database/sqlc"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/legacy"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/cachemetrics"
	observabilitymetrics "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/observability/metrics"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/queue"
	merchantservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/merchant"
	transactionservice "github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/service/transaction"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
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

type merchantPathParams struct {
	MerchantID string `path:"merchantId" format:"uuid" doc:"Merchant UUID"`
}

type transactionPathParams struct {
	TransactionID string `path:"transactionId" format:"uuid" doc:"Transaction UUID"`
}

type accountPathParams struct {
	AccountID string `path:"accountId" format:"uuid" doc:"Account UUID"`
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

type systemStatusResponse struct {
	Body systemStatusDTO
}

type merchantDTO struct {
	ID        string `json:"id" doc:"Merchant UUID"`
	Name      string `json:"name" doc:"Merchant display name"`
	QRISCode  string `json:"qris_code" doc:"Merchant QRIS code"`
	Category  string `json:"category,omitempty" doc:"Optional merchant category label"`
	CreatedAt string `json:"created_at" doc:"Merchant record creation timestamp in RFC3339 format"`
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
	MerchantID        string  `json:"merchant_id" doc:"Merchant UUID associated with the transaction"`
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

type dependencyStatus struct {
	Status string `json:"status" doc:"Dependency state such as ok, error, or stub"`
	Detail string `json:"detail,omitempty" doc:"Optional detail describing the current dependency state"`
}

func Start() {
	cfg := config.Load()

	redisClient, err := cache.Open(context.Background(), cfg.Redis)
	if err != nil {
		log.Fatalf("redis startup failed: %v", err)
	}
	defer redisClient.Close()

	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		log.Fatalf("database startup failed: %v", err)
	}
	defer db.Close()

	cacheClient := cache.NewClient(redisClient)
	store := database.NewStore(db)
	cacheStats := cachemetrics.NewInMemoryRecorder()
	merchantSvc := merchantservice.NewService(cacheClient, store.Queries, legacy.NoopMerchantClient{}, cacheStats)
	transactionStatusSvc := transactionservice.NewStatusService(cacheClient, store.Queries, cacheStats)
	transactionDetailSvc := transactionservice.NewDetailService(store.Queries)
	transactionHistorySvc := transactionservice.NewHistoryService(store.Queries)
	metricsHandler := observabilitymetrics.NewHandler(observabilitymetrics.Config{
		Service:     "dante-api-systems",
		Version:     cfg.App.Version,
		Environment: cfg.App.Environment,
	}, cacheStats, db, redisClient)

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	router.Handle("/metrics", metricsHandler)

	apiConfig := huma.DefaultConfig("DANTE API Systems", "0.1.0")
	if cfg.App.IsDevelopment() {
		apiConfig.OpenAPIPath = "/openapi"
		apiConfig.DocsPath = "/docs"
	} else {
		apiConfig.OpenAPIPath = ""
		apiConfig.DocsPath = ""
		apiConfig.SchemasPath = ""
	}

	api := humachi.New(router, apiConfig)

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

		if err := db.PingContext(readyCtx); err != nil {
			dependencies["database"] = "error"
		}

		if err := redisClient.Ping(readyCtx).Err(); err != nil {
			dependencies["redis"] = "error"
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
		Description: "Returns cursor-paginated transaction history for an account ordered from newest to oldest.",
		Tags:        []string{"Accounts", "Transactions"},
	}, func(ctx context.Context, input *struct {
		accountPathParams
		accountTransactionsQueryParams
	}) (*accountTransactionsResponse, error) {
		accountID, err := uuid.Parse(input.AccountID)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid account id", err)
		}

		limit, err := transactionservice.NormalizeLimit(input.Limit)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid limit", err)
		}

		page, err := transactionHistorySvc.ListByAccount(ctx, transactionservice.HistoryParams{
			AccountID: accountID,
			Limit:     limit,
			Cursor:    input.Cursor,
		})
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to fetch account transaction history", err)
		}

		resp := &accountTransactionsResponse{}
		resp.Body.Items = make([]transactionDetailDTO, 0, len(page.Items))
		for _, item := range page.Items {
			resp.Body.Items = append(resp.Body.Items, mapTransactionDetailResponse(item))
		}
		resp.Body.NextCursor = page.NextCursor
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
			"legacy":   {Status: "stub", Detail: "legacy adapter is not implemented yet"},
		}

		if err := db.PingContext(checkCtx); err != nil {
			dependencies["database"] = dependencyStatus{Status: "error", Detail: err.Error()}
		}

		if err := redisClient.Ping(checkCtx).Err(); err != nil {
			dependencies["redis"] = dependencyStatus{Status: "error", Detail: err.Error()}
		}

		if err := queue.CheckRabbitMQ(checkCtx, cfg.RabbitMQ); err != nil {
			dependencies["rabbitmq"] = dependencyStatus{Status: "error", Detail: err.Error()}
		}

		resp := &systemStatusResponse{}
		resp.Body.Dependencies = dependencies
		resp.Body.Status = "ok"

		for _, name := range []string{"database", "redis", "rabbitmq"} {
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
		MerchantID:     detail.MerchantID.String(),
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

	if detail.FailureReason != nil {
		resp.FailureReason = detail.FailureReason
	}

	if detail.ProcessedAt != nil {
		processedAt := detail.ProcessedAt.UTC().Format(time.RFC3339)
		resp.ProcessedAt = &processedAt
	}

	return resp
}
