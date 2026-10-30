// Package extensiontrace hosts minimal OpenTelemetry helpers for internal/core/extension stages
// without importing the full internal/infra/tracing package (which depends on internal/core and would
// cycle with packages such as internal/core/diag that reference extensions).
package extensiontrace

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// TracerScope is the OpenTelemetry instrumentation scope name shared with [internal/core/extensions]
// so spans stay grouped with other extension stages when span creation lives outside that package.
const TracerScope = "github.com/aiproxer/aiproxer/internal/core/extensions"

// StartSpan begins a span for an extension stage (aip.extension.* names).
// Optional attrs are applied with [go.opentelemetry.io/otel/trace.Span.SetAttributes]; keep them
// low-cardinality (stage keys, plugin IDs, small counts)—never attach raw request payloads.
// Call the returned end function once with the stage's terminal error; it records status and ends the span.
func StartSpan(ctx context.Context, spanName string, attrs ...attribute.KeyValue) (context.Context, func(error)) {
	if ctx == nil {
		ctx = context.TODO()
	}
	ctx, span := otel.Tracer(TracerScope).Start(ctx, spanName)
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}
}
