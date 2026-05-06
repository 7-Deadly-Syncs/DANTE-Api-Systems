package httpobs

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// MetricsRecorder is implemented by internal/observability/metrics.Handler.
type MetricsRecorder interface {
	IncHTTPInFlight()
	DecHTTPInFlight()
	ObserveHTTPRequest(method, path string, statusCode int, duration time.Duration)
}

// Metrics records HTTP request count, latency, status code, and in-flight request count.
func Metrics(recorder MetricsRecorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			startedAt := time.Now()
			wrapped := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)

			recorder.IncHTTPInFlight()

			defer func() {
				recorder.DecHTTPInFlight()

				statusCode := wrapped.Status()
				if recovered := recover(); recovered != nil {
					if statusCode == 0 {
						statusCode = http.StatusInternalServerError
					}

					recorder.ObserveHTTPRequest(
						r.Method,
						routePattern(r),
						statusCode,
						time.Since(startedAt),
					)

					panic(recovered)
				}

				if statusCode == 0 {
					statusCode = http.StatusOK
				}

				recorder.ObserveHTTPRequest(
					r.Method,
					routePattern(r),
					statusCode,
					time.Since(startedAt),
				)
			}()

			next.ServeHTTP(wrapped, r)
		})
	}
}

// Tracing creates an OpenTelemetry server span for every HTTP request.
func Tracing(serviceName string) func(http.Handler) http.Handler {
	tracer := otel.Tracer(serviceName + "/http")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := otel.GetTextMapPropagator().Extract(
				r.Context(),
				propagation.HeaderCarrier(r.Header),
			)

			ctx, span := tracer.Start(
				ctx,
				r.Method+" "+r.URL.Path,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.target", r.URL.RequestURI()),
					attribute.String("http.scheme", scheme(r)),
					attribute.String("net.host.name", r.Host),
					attribute.String("user_agent.original", r.UserAgent()),
				),
			)
			defer span.End()

			if span.SpanContext().IsValid() {
				w.Header().Set("X-Trace-Id", span.SpanContext().TraceID().String())
			}

			r = r.WithContext(ctx)
			wrapped := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)

			defer func() {
				statusCode := wrapped.Status()

				if recovered := recover(); recovered != nil {
					if statusCode == 0 {
						statusCode = http.StatusInternalServerError
					}

					finishSpan(span, r, statusCode)
					span.RecordError(fmt.Errorf("panic: %v", recovered))
					span.SetStatus(codes.Error, "panic")

					panic(recovered)
				}

				if statusCode == 0 {
					statusCode = http.StatusOK
				}

				finishSpan(span, r, statusCode)
			}()

			next.ServeHTTP(wrapped, r)
		})
	}
}

func finishSpan(span trace.Span, r *http.Request, statusCode int) {
	path := routePattern(r)

	span.SetName(r.Method + " " + path)
	span.SetAttributes(
		attribute.String("http.route", path),
		attribute.Int("http.status_code", statusCode),
	)

	if statusCode >= 500 {
		span.SetStatus(codes.Error, http.StatusText(statusCode))
	}
}

func routePattern(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx != nil {
		pattern := rctx.RoutePattern()
		if pattern != "" {
			return pattern
		}
	}

	if r.URL != nil && r.URL.Path != "" {
		return r.URL.Path
	}

	return "unknown"
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}

	if forwardedProto := r.Header.Get("X-Forwarded-Proto"); forwardedProto != "" {
		return forwardedProto
	}

	return "http"
}
