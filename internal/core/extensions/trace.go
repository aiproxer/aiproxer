package extensions

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const tracerScope = "github.com/aiproxer/aiproxer/internal/core/extensions"

// startSpan begins an OpenTelemetry span for an extension stage.
// Call the returned end function once with the stage's terminal error.
func startSpan(ctx context.Context, spanName string, attrs ...attribute.KeyValue) (context.Context, func(error)) {
	if ctx == nil {
		ctx = context.TODO()
	}
	ctx, span := otel.Tracer(tracerScope).Start(ctx, spanName)
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
