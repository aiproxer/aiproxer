package runtimebundle_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

// TestGenerationRuntime_ExactCapabilitySatisfaction is the compile-time and
// runtime proof that the canonical GenerationRuntime contract is satisfied
// directly by the concrete unpublished generation runtime (Task 3.3).
func TestGenerationRuntime_ExactCapabilitySatisfaction(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "cap", "capability-text", "cap:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	rt := bundle
	var (
		_ runtimehost.PublishedRequestPlane     = rt
		_ runtimehost.ExecutorProvider          = rt
		_ runtimehost.PublishedWorkStarter      = rt
		_ runtimehost.ModelViewBinder           = rt
		_ runtimehost.BackendFactoryKindCounter = rt
		_ runtimehost.QuiesceCloser             = rt
		_ runtimehost.OwnedCloser               = rt
	)

	if rt.Handler() == nil {
		t.Fatal("Handler must be non-nil")
	}
	if rt.ExecutorView() == nil {
		t.Fatal("ExecutorView must be non-nil")
	}
	if rt.TerminalProviders() == nil {
		t.Fatal("TerminalProviders must be non-nil")
	}
	_ = rt.ReadinessReport()
	_ = rt.BackendFactoryKindCounts()
	ctx := rt.BindModelViews(context.Background())
	if ctx == nil {
		t.Fatal("BindModelViews must return a context")
	}

	body := postResponses(t, rt.Handler(), "stub-default")
	if !strings.Contains(body, "capability-text") {
		t.Fatalf("handler body=%s", body)
	}
}

// TestGenerationRuntime_UnpublishedCompilesServesAndClosesOnce exercises the
// observable Task 3.3 completion: one unpublished generation runtime compiles
// with a non-listening handler, exposes narrow capabilities, and closes once.
func TestGenerationRuntime_UnpublishedCompilesServesAndClosesOnce(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	life := &overlapLife{}
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Candidate: stubCandidateConfig(t, "pub", "publish-text", "pub:stub-default", []config.PluginConfig{
			{ID: "openai-responses", Enabled: true},
		}),
		CandidateOpts: &runtimebundle.BuildOptions{
			FeatureLifecycles: []aipplugin.Lifecycle{life},
		},
		Compose: func(context.Context, *config.Config, *slog.Logger, stdhttp.StandardHTTPInput) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("ok-unpublished"))
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}

	rt := bundle
	rr := httptest.NewRecorder()
	rt.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Body.String() != "ok-unpublished" {
		t.Fatalf("body=%q", rr.Body.String())
	}
	if rt.ExecutorView() == nil {
		t.Fatal("missing executor")
	}
	if rt.TerminalProviders() == nil {
		t.Fatal("missing terminal providers")
	}
	_ = rt.ReadinessReport()
	_ = rt.BackendFactoryKindCounts()
	_ = rt.BindModelViews(context.Background())

	if life.starts.Load() != 1 {
		t.Fatalf("lifecycle starts=%d want 1", life.starts.Load())
	}
	if ps.Closed() {
		t.Fatal("process must remain open while generation serves")
	}
	if err := bundle.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if life.stops.Load() != 1 {
		t.Fatalf("lifecycle stops=%d want 1", life.stops.Load())
	}
	if err := bundle.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if life.stops.Load() != 1 {
		t.Fatalf("doubled stops=%d", life.stops.Load())
	}
	if ps.Closed() {
		t.Fatal("process must survive generation close")
	}
}

// TestGenerationRuntime_NoGenericDependencyLookupSurface forbids service-locator
// methods and one-getter-per-dependency walls on the concrete runtime type.
func TestGenerationRuntime_NoGenericDependencyLookupSurface(t *testing.T) {
	t.Parallel()
	elem := reflect.TypeFor[runtimebundle.GenerationBundle]()
	forbidden := map[string]bool{
		"Get": true, "Lookup": true, "Resolve": true,
		"GetDependency": true, "LookupDependency": true, "ResolveDependency": true,
		"Dependencies": true, "DependencyMap": true, "Services": true,
	}
	for m := range elem.Methods() {
		if forbidden[m.Name] || isBroadDependencyGetter(m.Name) {
			t.Fatalf("GenerationBundle must not expose generic/broad dependency method %s", m.Name)
		}
	}
	iface := reflect.TypeFor[runtimebundle.GenerationRuntime]()
	if iface.NumMethod() > 12 {
		t.Fatalf("GenerationRuntime grew into a getter wall: %d methods", iface.NumMethod())
	}
}

func isBroadDependencyGetter(name string) bool {
	switch name {
	case "GetExecutor", "GetStore", "GetMetrics", "GetLedger", "GetOwner",
		"GetCandidate", "GetProcess", "GetBuilt", "GetRequestPlane", "GetApp",
		"GetConfig", "GetPluginRegistry", "GetDatabasePools", "GetClosers":
		return true
	default:
		return false
	}
}
