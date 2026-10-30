package tracing

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	corehttp "github.com/aiproxer/aiproxer/internal/core/http"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestInit_tracingDisabled(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	res, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Active {
		t.Fatal("expected tracing inactive")
	}
	if err := res.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCoarsePathGroup_spanPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		want string
	}{
		{"empty", "", "/"},
		{"root", "/", "/"},
		{"v1_nested", "/v1/foo/bar", "/v1"},
		{"v1_exact", "/v1", "/v1"},
		{"long_single_segment", "/longsegment-only", "/longsegment-only"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := corehttp.CoarsePathGroup(tc.path); got != tc.want {
				t.Fatalf("CoarsePathGroup(%q)=%q want %q", tc.path, got, tc.want)
			}
		})
	}
}

func Test_spanName_coarse(t *testing.T) {
	t.Parallel()
	r, err := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := spanName(r); got != "POST /v1" {
		t.Fatalf("spanName = %q", got)
	}
}

// TestInit_tracingEnabled_installsProvidersAndPropagator locks the enabled-tracing path of
// [Init]: it must install an sdk trace provider and the tracecontext+baggage propagator on the
// global OpenTelemetry handles. Migrated from stdhttp.TestRun_initializesTracingAndOutboundPropagation
// when the orphaned stdhttp.Run convenience wrapper was removed (arch review Phase 1 Task 1.1);
// BuildHost calls this same Init for the canonical serve path.
func TestInit_tracingEnabled_installsProvidersAndPropagator(t *testing.T) { //nolint:paralleltest // mutates global OpenTelemetry providers
	originalProvider := otel.GetTracerProvider()
	originalPropagator := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(originalProvider)
		otel.SetTextMapPropagator(originalPropagator)
	})
	cfg := &config.Config{
		Observability: config.ObservabilityConfig{
			Tracing: config.TracingConfig{Enabled: true},
		},
	}
	res, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Active {
		t.Fatal("expected tracing active")
	}
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Fatalf("expected sdk tracer provider, got %T", otel.GetTracerProvider())
	}
	got := slices.Clone(otel.GetTextMapPropagator().Fields())
	want := []string{"traceparent", "tracestate", "baggage"}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("expected tracecontext+baggage propagator fields, got %v", got)
	}
	if err := res.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
