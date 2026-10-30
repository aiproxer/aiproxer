package extensions

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var tracerProviderMu sync.Mutex

func TestStartSpan_ok(t *testing.T) {
	t.Parallel()
	span := recordStartSpan(t, "aip.extension.test_span", nil)
	if span.Status().Code != codes.Ok {
		t.Fatalf("status code = %v, want %v", span.Status().Code, codes.Ok)
	}
}

func TestStartSpan_withErr(t *testing.T) {
	t.Parallel()
	span := recordStartSpan(t, "aip.extension.test_span_err", context.Canceled)
	if span.Status().Code != codes.Error {
		t.Fatalf("status code = %v, want %v", span.Status().Code, codes.Error)
	}
	if span.Status().Description != context.Canceled.Error() {
		t.Fatalf("status description = %q, want %q", span.Status().Description, context.Canceled.Error())
	}
	if !spanHasExceptionMessage(span, context.Canceled.Error()) {
		t.Fatalf("missing recorded error event for %q", context.Canceled.Error())
	}
}

func TestStartSpan_nilParent(t *testing.T) {
	t.Parallel()
	//nolint:staticcheck // nil parent: startSpan must coerce to a usable context
	ctx, end := startSpan(nil, "aip.extension.nil_parent")
	defer end(nil)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
}

func TestStartSpan_setsAttributes(t *testing.T) {
	t.Parallel()
	const spanName = "aip.extension.attr_test"
	wantKey := attribute.Key("aip.extension.test_key")
	span := recordStartSpan(t, spanName, nil, attribute.String(string(wantKey), "v"))

	var found bool
	for _, a := range span.Attributes() {
		if a.Key == wantKey && a.Value.AsString() == "v" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing attribute %s on span", wantKey)
	}
}

func recordStartSpan(t *testing.T, spanName string, err error, attrs ...attribute.KeyValue) sdktrace.ReadOnlySpan {
	t.Helper()
	tracerProviderMu.Lock()
	defer tracerProviderMu.Unlock()

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(prev)

	_, end := startSpan(context.Background(), spanName, attrs...)
	end(err)

	for _, s := range rec.Ended() {
		if s.Name() == spanName {
			return s
		}
	}
	t.Fatalf("span %q not found among %d ended spans", spanName, len(rec.Ended()))
	return nil
}

func spanHasExceptionMessage(span sdktrace.ReadOnlySpan, want string) bool {
	for _, event := range span.Events() {
		for _, attr := range event.Attributes {
			if string(attr.Key) == "exception.message" && attr.Value.AsString() == want {
				return true
			}
		}
	}
	return false
}
