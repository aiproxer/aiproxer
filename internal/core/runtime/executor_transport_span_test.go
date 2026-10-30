package runtime_test

import (
	"context"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestExecutor_transportRejectSpan_recordsDecisionAttributes(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.TransportFallbackPolicy = aipapi.TransportFallbackExact
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"stub": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				t.Fatal("backend must not open after transport reject")
				return nil, nil
			},
		},
	}

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected transport reject")
	}

	span := findEndedSpan(t, rec.Ended(), "aip.executor.candidate_admission")
	if span.Status().Code != codes.Error {
		t.Fatalf("span status = %v, want error", span.Status())
	}
	assertSpanAttr(t, span.Attributes(), "aip.operation", string(aipapi.OperationOpenAIChatCompletions))
	assertSpanAttr(t, span.Attributes(), "aip.client_delivery_mode", string(aipapi.DeliveryModeNonStreaming))
	assertSpanAttr(t, span.Attributes(), "aip.transport_mode", string(aipapi.TransportModeNonStreaming))
	assertSpanAttr(t, span.Attributes(), "aip.transport_negotiation_kind", string(aipapi.NegotiationReject))
}

func findEndedSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range slices.Backward(spans) {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("missing span %q; ended=%d", name, len(spans))
	return nil
}

func assertSpanAttr(t *testing.T, attrs []attribute.KeyValue, key, want string) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			if got := attr.Value.AsString(); got != want {
				t.Fatalf("span attr %q = %q, want %q", key, got, want)
			}
			return
		}
	}
	t.Fatalf("span missing attr %q: %#v", key, attrs)
}
