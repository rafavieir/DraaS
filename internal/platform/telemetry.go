package platform

import (
	"context"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"log/slog"
)

// Export structured OpenTelemetry spans to the container log collector in the lab.
// Production can replace this exporter with OTLP without changing instrumentation.
type spanLogExporter struct{}

func (spanLogExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	for _, span := range spans {
		slog.Info("otel.span", "operation", span.Name(), "trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String(), "parent_span_id", span.Parent().SpanID().String(), "duration_seconds", span.EndTime().Sub(span.StartTime()).Seconds())
	}
	return nil
}
func (spanLogExporter) Shutdown(context.Context) error { return nil }
