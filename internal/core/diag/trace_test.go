package diag_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestTraceID_emptyContext(t *testing.T) {
	t.Parallel()
	if got := diag.TraceID(nil); got != "" { //nolint:staticcheck // nil ctx is an explicit contract surface
		t.Fatalf("TraceID(nil) = %q, want empty", got)
	}
	if got := diag.TraceID(context.Background()); got != "" {
		t.Fatalf("TraceID(Background) = %q, want empty", got)
	}
	if got := diag.ALegID(nil); got != "" { //nolint:staticcheck // nil ctx is an explicit contract surface
		t.Fatalf("ALegID(nil) = %q, want empty", got)
	}
}

func TestTraceID_prefersOpenTelemetrySpan(t *testing.T) {
	t.Parallel()
	tp := sdktrace.NewTracerProvider()
	tr := tp.Tracer("test")
	ctx, span := tr.Start(context.Background(), "op")
	defer span.End()
	want := span.SpanContext().TraceID().String()
	if got := diag.TraceID(ctx); got != want {
		t.Fatalf("TraceID=%q want %q", got, want)
	}
}

func TestTraceID_explicitWithTraceIDBeatsOpenTelemetrySpan(t *testing.T) {
	t.Parallel()
	tp := sdktrace.NewTracerProvider()
	tr := tp.Tracer("test")
	ctx, span := tr.Start(context.Background(), "op")
	defer span.End()
	ctx = diag.WithTraceID(ctx, "client-correlation")
	if got := diag.TraceID(ctx); got != "client-correlation" {
		t.Fatalf("TraceID=%q want client-correlation (span was %q)", got, span.SpanContext().TraceID().String())
	}
}

func TestTraceID_roundTrip(t *testing.T) {
	t.Parallel()
	ctx := diag.WithTraceID(context.Background(), "tid-1")
	if diag.TraceID(ctx) != "tid-1" {
		t.Fatalf("TraceID = %q", diag.TraceID(ctx))
	}
	ctx = diag.WithALeg(ctx, "a_1")
	if diag.ALegID(ctx) != "a_1" {
		t.Fatalf("ALegID = %q", diag.ALegID(ctx))
	}
}

func TestWithCallDiag_roundTrip(t *testing.T) {
	t.Parallel()
	ctx := diag.WithCallDiag(context.Background(), "tid-2", "aleg-2")
	if diag.TraceID(ctx) != "tid-2" {
		t.Fatalf("TraceID = %q", diag.TraceID(ctx))
	}
	if diag.ALegID(ctx) != "aleg-2" {
		t.Fatalf("ALegID = %q", diag.ALegID(ctx))
	}
}

func TestEnsureCallDiag_sameAsWithCallDiag(t *testing.T) {
	t.Parallel()
	base := context.Background()
	wantT, wantA := "tid-eq", "aleg-eq"
	c1 := diag.WithCallDiag(base, wantT, wantA)
	c2 := diag.EnsureCallDiag(base, wantT, wantA)
	if diag.TraceID(c1) != diag.TraceID(c2) || diag.ALegID(c1) != diag.ALegID(c2) {
		t.Fatalf("WithCallDiag vs EnsureCallDiag mismatch: (%q,%q) vs (%q,%q)",
			diag.TraceID(c1), diag.ALegID(c1), diag.TraceID(c2), diag.ALegID(c2))
	}
}

func TestEnsureCallDiag_identityWhenAlreadySet(t *testing.T) {
	t.Parallel()
	ctx := diag.WithCallDiag(context.Background(), "tid-id", "aleg-id")
	out := diag.EnsureCallDiag(ctx, "tid-id", "aleg-id")
	if out != ctx {
		t.Fatalf("EnsureCallDiag should return same ctx when values match")
	}
}

func TestEnsureCallDiag_nilContext(t *testing.T) {
	t.Parallel()
	ctx := diag.EnsureCallDiag(nil, "tid-nil", "aleg-nil") //nolint:staticcheck // SA1012: intentional nil parent contract
	if diag.TraceID(ctx) != "tid-nil" || diag.ALegID(ctx) != "aleg-nil" {
		t.Fatalf("EnsureCallDiag(nil,...) = (%q,%q)", diag.TraceID(ctx), diag.ALegID(ctx))
	}
}

func TestTraceIDGenerator_deterministicSequence(t *testing.T) {
	t.Parallel()
	g := diag.NewTraceIDGenerator()
	a := g.Next()
	b := g.Next()
	if a != "t_00000001" {
		t.Fatalf("first trace id = %q, want t_00000001", a)
	}
	if b != "t_00000002" {
		t.Fatalf("second trace id = %q, want t_00000002", b)
	}
}
