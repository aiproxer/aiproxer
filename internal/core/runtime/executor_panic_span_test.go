package runtime

import (
	"context"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestExecutor_executeSpan_recordsErrorWhenOpenPanicExhaustsCandidates(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(7)
	ex.Backends = map[string]execbackend.Backend{
		"only": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				panic("open boom")
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "span-panic-open"},
		Route:   aipapi.RouteIntent{Selector: "only:model-x"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected Execute error after sole candidate open panic exhausts routing")
	}

	var execSpan sdktrace.ReadOnlySpan
	ended := rec.Ended()
	for _, s := range slices.Backward(ended) {
		if s.Name() == "aip.executor.execute" && s.Status().Code == codes.Error {
			execSpan = s
			break
		}
	}
	if execSpan == nil {
		t.Fatalf("missing failed aip.executor.execute span; ended=%d", len(ended))
	}
}
