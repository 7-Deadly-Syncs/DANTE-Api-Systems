package tracing

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationPrefix = "dante-api-systems/"

// StartInternalSpan starts an application-internal child span.
func StartInternalSpan(ctx context.Context, component, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(instrumentationPrefix+component).Start(
		ctx,
		name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...),
	)
}

// StartClientSpan starts a span for an outbound dependency operation.
func StartClientSpan(ctx context.Context, component, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(instrumentationPrefix+component).Start(
		ctx,
		name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
}

// EndSpan records unexpected errors and ends the span.
func EndSpan(span trace.Span, err error, ignoredErrors ...error) {
	if err != nil && !isIgnoredError(err, ignoredErrors...) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	span.End()
}

func isIgnoredError(err error, ignoredErrors ...error) bool {
	for _, ignored := range ignoredErrors {
		if ignored != nil && errors.Is(err, ignored) {
			return true
		}
	}

	return false
}
