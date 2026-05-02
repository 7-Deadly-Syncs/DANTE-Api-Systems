package bridge

import (
	"context"
	"log"
	"net/http"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/database"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type messageResponse struct {
	Body struct {
		Message string `json:"message"`
	}
}

type healthResponse struct {
	Body struct {
		Status string `json:"status"`
	}
}

type infoResponse struct {
	Body struct {
		Service     string `json:"service"`
		Version     string `json:"version"`
		Environment string `json:"environment"`
	}
}

func Start() {
	cfg := config.Load()

	db, err := database.Open(context.Background(), cfg.Database)
	if err != nil {
		log.Fatalf("database startup failed: %v", err)
	}
	defer db.Close()

	_ = database.NewStore(db)

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)

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

	huma.Get(api, "/", func(ctx context.Context, input *struct{}) (*messageResponse, error) {
		resp := &messageResponse{}
		resp.Body.Message = "DANTE API Systems is running"
		return resp, nil
	})

	huma.Get(api, "/health", func(ctx context.Context, input *struct{}) (*healthResponse, error) {
		resp := &healthResponse{}
		resp.Body.Status = "ok"
		return resp, nil
	})

	huma.Get(api, "/info", func(ctx context.Context, input *struct{}) (*infoResponse, error) {
		resp := &infoResponse{}
		resp.Body.Service = "dante-api-systems"
		resp.Body.Version = cfg.App.Version
		resp.Body.Environment = cfg.App.Environment
		return resp, nil
	})

	addr := ":" + cfg.App.Port
	log.Printf("listening on %s", addr)

	if err := http.ListenAndServe(addr, router); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server failed: %v", err)
	}
}
