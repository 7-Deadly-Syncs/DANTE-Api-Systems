package tracing

import (
	"context"
	"net/url"
	"strings"

	"github.com/7-Deadly-Syncs/DANTE-Api-Systems/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Init initializes OpenTelemetry tracing and exports spans to Jaeger through OTLP HTTP.
func Init(ctx context.Context, obs config.ObservabilityConfig, app config.AppConfig) (func(context.Context) error, error) {
	if !obs.TracingEnabled {
		return func(context.Context) error { return nil }, nil
	}

	endpoint, insecure := normalizeEndpoint(obs.JaegerEndpoint)

	options := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
	}
	if insecure {
		options = append(options, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, err
	}

	ratio := obs.TraceSampleRatio
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", obs.ServiceName),
			attribute.String("service.version", app.Version),
			attribute.String("deployment.environment", app.Environment),
		),
	)
	if err != nil {
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(
			sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio)),
		),
	)

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	return provider.Shutdown, nil
}

// Tracer returns a named OpenTelemetry tracer.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

func normalizeEndpoint(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "jaeger:4318", true
	}

	parsed, err := url.Parse(value)
	if err == nil && parsed.Host != "" {
		return parsed.Host, parsed.Scheme != "https"
	}

	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimSuffix(value, "/")

	return value, true
}
