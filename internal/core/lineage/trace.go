// Package lineage holds request-lineage [context.Context] values (trace, A-leg) and accessors
// without depending on [github.com/aiproxer/aiproxer/internal/core/diag] or
// [github.com/aiproxer/aiproxer/internal/core/hooks] so the hook runtime can
// import correlation helpers without creating import cycles.
package lineage

import (
	"context"
	"fmt"
	"sync/atomic"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// TraceIDGenerator is a test-friendly, monotonically-increasing id generator.
type TraceIDGenerator struct {
	seq atomic.Uint64
}

// NewTraceIDGenerator returns a [TraceIDGenerator] with a zeroed counter.
func NewTraceIDGenerator() *TraceIDGenerator {
	return &TraceIDGenerator{}
}

// Next returns the next trace id, formatted as t_%08d.
func (g *TraceIDGenerator) Next() string {
	return fmt.Sprintf("t_%08d", g.seq.Add(1))
}

type ctxKey int

const (
	keyTraceID ctxKey = iota + 1
	keyALegID
	keyCallDiag
)

// callDiag carries trace and A-leg identifiers in a single context.Value for hot paths.
type callDiag struct {
	Trace string
	ALeg  string
}

// WithTraceID returns a child context that carries traceID for diagnostics propagation.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, keyTraceID, traceID)
}

// WithCallDiag attaches traceID and aLegID in one context layer (one allocation).
// Use on streaming hot paths instead of chaining WithTraceID and WithALeg.
// ctx must be non-nil for production call paths; use [context.TODO] only in tests.
func WithCallDiag(ctx context.Context, traceID, aLegID string) context.Context {
	return context.WithValue(ctx, keyCallDiag, callDiag{Trace: traceID, ALeg: aLegID})
}

// EnsureCallDiag behaves like [WithCallDiag], but returns ctx unchanged when [TraceID]
// and [ALegID] already match traceID and aLegID (including legacy WithTraceID/WithALeg pairs).
//
// If ctx is nil, the result is based on [context.TODO] (not [context.Background]) so call
// sites without cancellation are obvious in code review. APIs that require a request-scoped
// context should still validate non-nil [context.Context] at their boundary; do not rely on
// nil here for production traffic.
func EnsureCallDiag(ctx context.Context, traceID, aLegID string) context.Context {
	if ctx == nil {
		return WithCallDiag(context.TODO(), traceID, aLegID)
	}
	if TraceID(ctx) == traceID && ALegID(ctx) == aLegID {
		return ctx
	}
	return WithCallDiag(ctx, traceID, aLegID)
}

// TraceID returns the trace identifier from ctx, or empty string if unset.
// Precedence: explicit AIProxer context ([WithCallDiag], [WithTraceID]) over the OpenTelemetry
// W3C trace id so client X-Trace-ID and in-process correlation win in logs while OTLP export
// still uses the active span.
func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(keyCallDiag).(callDiag); ok && v.Trace != "" {
		return v.Trace
	}
	if v, ok := ctx.Value(keyTraceID).(string); ok && v != "" {
		return v
	}
	if span := oteltrace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		return span.SpanContext().TraceID().String()
	}
	return ""
}

// WithALeg returns a child context that carries the A-leg identifier for lineage diagnostics.
func WithALeg(ctx context.Context, aLegID string) context.Context {
	return context.WithValue(ctx, keyALegID, aLegID)
}

// ALegID returns the A-leg identifier from ctx, or empty string if unset.
func ALegID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(keyCallDiag).(callDiag); ok {
		return v.ALeg
	}
	if v, ok := ctx.Value(keyALegID).(string); ok {
		return v
	}
	return ""
}
