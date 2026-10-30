package diag

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/lineage"
)

// TraceIDGenerator re-exports [lineage.TraceIDGenerator] for backward-compatible wiring.
type TraceIDGenerator = lineage.TraceIDGenerator

// NewTraceIDGenerator re-exports [lineage.NewTraceIDGenerator].
func NewTraceIDGenerator() *TraceIDGenerator {
	return lineage.NewTraceIDGenerator()
}

// WithTraceID re-exports [lineage.WithTraceID].
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return lineage.WithTraceID(ctx, traceID)
}

// WithCallDiag re-exports [lineage.WithCallDiag].
func WithCallDiag(ctx context.Context, traceID, aLegID string) context.Context {
	return lineage.WithCallDiag(ctx, traceID, aLegID)
}

// EnsureCallDiag re-exports [lineage.EnsureCallDiag].
func EnsureCallDiag(ctx context.Context, traceID, aLegID string) context.Context {
	return lineage.EnsureCallDiag(ctx, traceID, aLegID)
}

// TraceID re-exports [lineage.TraceID].
func TraceID(ctx context.Context) string {
	return lineage.TraceID(ctx)
}

// WithALeg re-exports [lineage.WithALeg].
func WithALeg(ctx context.Context, aLegID string) context.Context {
	return lineage.WithALeg(ctx, aLegID)
}

// ALegID re-exports [lineage.ALegID].
func ALegID(ctx context.Context) string {
	return lineage.ALegID(ctx)
}
