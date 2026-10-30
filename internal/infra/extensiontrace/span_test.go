package extensiontrace

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

//nolint:paralleltest // Mutates global OpenTelemetry tracer provider; must run serially.
func TestStartSpan_ok(t *testing.T) {
	ctx, end := StartSpan(context.Background(), "aip.extension.test_span")
	end(nil)
	_ = ctx
}

//nolint:paralleltest // Mutates global OpenTelemetry tracer provider; must run serially.
func TestStartSpan_withErr(t *testing.T) {
	_, end := StartSpan(context.Background(), "aip.extension.test_span_err")
	end(context.Canceled)
}

//nolint:paralleltest // Mutates global OpenTelemetry tracer provider; must run serially.
func TestStartSpan_nilParent(t *testing.T) {
	//nolint:staticcheck // nil parent: StartSpan must coerce to a usable context
	ctx, end := StartSpan(nil, "aip.extension.nil_parent")
	defer end(nil)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
}

//nolint:paralleltest // Mutates global OpenTelemetry tracer provider; must run serially.
func TestStartSpan_setsAttributes(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	const spanName = "aip.extension.attr_test"
	wantKey := attribute.Key("aip.extension.test_key")
	_, end := StartSpan(context.Background(), spanName, attribute.String(string(wantKey), "v"))
	end(nil)

	spans := rec.Started()
	if len(spans) != 1 {
		t.Fatalf("started spans: got %d want 1", len(spans))
	}
	if spans[0].Name() != spanName {
		t.Fatalf("span name: got %q want %q", spans[0].Name(), spanName)
	}
	var found bool
	for _, a := range spans[0].Attributes() {
		if a.Key == wantKey && a.Value.AsString() == "v" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing attribute %s on span", wantKey)
	}
}
