package runtimebundle_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

// inventoryCandidateConfig returns a candidate with one enabled local-stub
// backend (deterministic model inventory "local-stub/stub-default") suitable
// for model-catalog/registry generation-coherence tests (task 4.5).
func inventoryCandidateConfig(t *testing.T, backendID string) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3, DefaultRoute: backendID + ":stub-default"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes:    1024,
			MaxConcurrentDecodes:   4,
			MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind:    "local-stub",
				ID:      backendID,
				Enabled: true,
				Config:  genYAMLNode(t, "text: stub\ninput_tokens: 1\noutput_tokens: 1\n"),
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return cfg
}

func modelsListFromHandler(ctx context.Context, t *testing.T, h http.Handler) modelregistry.OpenAIModelList {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /v1/models status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list modelregistry.OpenAIModelList
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode /v1/models: %v", err)
	}
	return list
}

func modelIDs(list modelregistry.OpenAIModelList) []string {
	out := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		out = append(out, m.ID)
	}
	return out
}

func containsModelID(ids []string, id string) bool {
	return slices.Contains(ids, id)
}

// TestCompileGeneration_ModelsAndRoutingAgreeWithCandidateBackendSet proves
// /v1/models, routing DefaultRoute, and backend IDs agree with the active
// candidate's own backend set, and that ETag/model identity differ across
// two coexisting generations with different backend sets (req 9.1, 9.3, 9.6, 9.9).
func TestCompileGeneration_ModelsAndRoutingAgreeWithCandidateBackendSet(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)

	a, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: inventoryCandidateConfig(t, "alpha"), Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile A: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: inventoryCandidateConfig(t, "beta"), Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile B: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	ga, ok := a.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}
	gb, ok := b.(*runtimebundle.GenerationBundle)
	if !ok {
		t.Fatal("expected *runtimebundle.GenerationBundle")
	}

	ctxA := a.BindModelViews(context.Background())
	ctxB := b.BindModelViews(context.Background())
	listA := modelsListFromHandler(ctxA, t, a.Handler())
	listB := modelsListFromHandler(ctxB, t, b.Handler())

	idsA, idsB := modelIDs(listA), modelIDs(listB)
	if !containsModelID(idsA, "alpha:local-stub/stub-default") || containsModelID(idsA, "beta:local-stub/stub-default") {
		t.Fatalf("generation A models must be alpha-only, got %v", idsA)
	}
	if !containsModelID(idsB, "beta:local-stub/stub-default") || containsModelID(idsB, "alpha:local-stub/stub-default") {
		t.Fatalf("generation B models must be beta-only, got %v", idsB)
	}
	if ga.Routing().DefaultRoute != "alpha:stub-default" || gb.Routing().DefaultRoute != "beta:stub-default" {
		t.Fatalf("routing disagreement: A=%q B=%q", ga.Routing().DefaultRoute, gb.Routing().DefaultRoute)
	}
	if len(ga.BackendIDs()) != 1 || ga.BackendIDs()[0] != "alpha" {
		t.Fatalf("A backend IDs = %v", ga.BackendIDs())
	}
	if len(gb.BackendIDs()) != 1 || gb.BackendIDs()[0] != "beta" {
		t.Fatalf("B backend IDs = %v", gb.BackendIDs())
	}

	rrA := httptest.NewRecorder()
	a.Handler().ServeHTTP(rrA, httptest.NewRequest(http.MethodGet, "/v1/models", nil).WithContext(ctxA))
	rrB := httptest.NewRecorder()
	b.Handler().ServeHTTP(rrB, httptest.NewRequest(http.MethodGet, "/v1/models", nil).WithContext(ctxB))
	etagA, etagB := rrA.Header().Get("ETag"), rrB.Header().Get("ETag")
	if etagA == "" || etagB == "" {
		t.Fatalf("expected non-empty ETags: A=%q B=%q", etagA, etagB)
	}
	if etagA == etagB {
		t.Fatalf("expected distinct generation ETags, both %q", etagA)
	}
}

// TestGenerationBundle_BackendRemovalOmitsModelsButOldBoundViewRetainsThem
// proves a newer generation with a backend removed does not advertise its
// models while an older bound view (captured before the newer generation
// even compiled) continues to serve exactly what it observed (req 9.4, 9.5, 9.9).
func TestGenerationBundle_BackendRemovalOmitsModelsButOldBoundViewRetainsThem(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)

	old, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: inventoryCandidateConfig(t, "removable"), Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile old: %v", err)
	}
	t.Cleanup(func() { _ = old.Close() })

	// Bind the old request's model view before the newer generation exists.
	oldReqCtx := old.BindModelViews(context.Background())
	oldList := modelsListFromHandler(oldReqCtx, t, old.Handler())
	if !containsModelID(modelIDs(oldList), "removable:local-stub/stub-default") {
		t.Fatalf("old generation must advertise its own backend, got %v", modelIDs(oldList))
	}

	// New generation removes "removable" and adds "kept" instead.
	newer, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: inventoryCandidateConfig(t, "kept"), Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("compile newer: %v", err)
	}
	t.Cleanup(func() { _ = newer.Close() })

	newList := modelsListFromHandler(newer.BindModelViews(context.Background()), t, newer.Handler())
	if containsModelID(modelIDs(newList), "removable:local-stub/stub-default") {
		t.Fatalf("newer generation must not advertise the removed backend, got %v", modelIDs(newList))
	}
	if !containsModelID(modelIDs(newList), "kept:local-stub/stub-default") {
		t.Fatalf("newer generation must advertise its own backend, got %v", modelIDs(newList))
	}

	// The already-bound old request context must still resolve the same
	// (now-retired) models even after the newer generation has published.
	replayList := modelsListFromHandler(oldReqCtx, t, old.Handler())
	if !containsModelID(modelIDs(replayList), "removable:local-stub/stub-default") {
		t.Fatalf("retained old bound view must still advertise its own backend, got %v", modelIDs(replayList))
	}
}

// TestGenerationBundle_QuiesceStopsRefreshLoopButRetainsBoundView proves
// Quiesce cancels a non-static backend's model-registry refresh loop
// (PhaseQuiesce ledger entry) promptly, while a model view bound before
// Quiesce keeps serving its already-loaded models afterward (req 10.5;
// design "Quiesce stops old generation refresh loops without invalidating
// retained bound views").
func TestGenerationBundle_QuiesceStopsRefreshLoopButRetainsBoundView(t *testing.T) {
	t.Parallel()
	provider := &runtimeBundleCountingInventory{models: []modelinventory.Model{{
		CanonicalID: "dyn/model", NativeID: "dyn-model",
	}}}
	reg := stdFactoryCatalog(t)
	if err := reg.RegisterBackend("dyn-inventory-45", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"dyn-inventory-45"},
			ModelInventory:  provider,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "dyn:dyn-model"},
		Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{Kind: "dyn-inventory-45", ID: "dyn", Enabled: true}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cfg, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}

	boundCtx := bundle.BindModelViews(context.Background())
	before := modelsListFromHandler(boundCtx, t, bundle.Handler())
	if !containsModelID(modelIDs(before), "dyn:dyn/model") {
		t.Fatalf("expected dyn model before quiesce, got %v", modelIDs(before))
	}

	done := make(chan error, 1)
	go func() { done <- bundle.Quiesce(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Quiesce: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Quiesce did not return promptly; refresh loop goroutine likely leaked")
	}

	after := modelsListFromHandler(boundCtx, t, bundle.Handler())
	if !containsModelID(modelIDs(after), "dyn:dyn/model") {
		t.Fatalf("bound view must remain valid after quiesce, got %v", modelIDs(after))
	}

	if err := bundle.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Bound view survives even generation close: it holds an immutable
	// snapshot, not a live handle into the closed backend/runtime.
	final := modelsListFromHandler(boundCtx, t, bundle.Handler())
	if !containsModelID(modelIDs(final), "dyn:dyn/model") {
		t.Fatalf("bound view must remain valid after close, got %v", modelIDs(final))
	}
}

// TestCompileGeneration_StaleCacheForRemovedBackendNotAdvertised proves a
// candidate whose model-inventory cache references a backend absent from the
// candidate's own backend rows does not advertise that backend's cached
// models; it falls back to a fresh load of the candidate's live backend set
// instead (req 9.7, 9.9; "stale cache" coverage).
func TestCompileGeneration_StaleCacheForRemovedBackendNotAdvertised(t *testing.T) {
	t.Parallel()

	cachePath := filepath.Join(t.TempDir(), "stale-backend-models.json")
	writeModelRegistryCache(t, cachePath, modelregistry.Snapshot{
		Generation:  "stale",
		RefreshedAt: time.Unix(100, 0).UTC(),
		Models: []modelregistry.BackendModel{{
			CanonicalID: "local-stub/stub-default",
			NativeID:    "stub-default",
			BackendID:   "removed-instance",
			Kind:        "local-stub",
			Source:      modelinventory.SourceRemote,
			LoadedAt:    time.Unix(100, 0).UTC(),
		}},
	})

	// model_inventory.cache_path is startup-only (req 7.3): the process
	// baseline must already declare the same cache path as the candidate so
	// this candidate exercises the stale-cache/backend-removal path itself,
	// not an unrelated restart-required rejection.
	cand := inventoryCandidateConfig(t, "kept")
	cand.ModelInventory.CachePath = cachePath
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cand,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: stdFactoryCatalog(t)},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cand, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	list := modelsListFromHandler(bundle.BindModelViews(context.Background()), t, bundle.Handler())
	ids := modelIDs(list)
	if containsModelID(ids, "removed-instance:local-stub/stub-default") {
		t.Fatalf("stale cache backend must not be advertised, got %v", ids)
	}
	if !containsModelID(ids, "kept:local-stub/stub-default") {
		t.Fatalf("candidate's own live backend must be advertised, got %v", ids)
	}
}

// TestCompileGeneration_InvalidInventoryFailsCompileProcessSurvives proves a
// candidate backend with no usable model-inventory provider fails
// CompileGeneration (mirrors legacy [runtimebundle.Build] fail-strict
// behavior) and leaves ProcessServices open for a later valid candidate
// (req 9.7, 13.1).
func TestCompileGeneration_InvalidInventoryFailsCompileProcessSurvives(t *testing.T) {
	t.Parallel()
	reg := stdFactoryCatalog(t)
	if err := reg.RegisterBackend("no-inventory-45", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"no-inventory-45"},
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("not used")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	baseCfg := processBaseConfig()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  baseCfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	badCfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3, DefaultRoute: "bad:test-model"},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Server: config.ServerConfig{
			MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096,
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{Kind: "no-inventory-45", ID: "bad", Enabled: true}},
		},
	}
	if err := config.Validate(badCfg); err != nil {
		t.Fatalf("validate: %v", err)
	}

	_, err = runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: badCfg, Compose: stdhttp.ComposeStandardHTTP,
	})
	if !errors.Is(err, modelregistry.ErrMissingProvider) {
		t.Fatalf("CompileGeneration error = %v, want ErrMissingProvider", err)
	}
	if ps.Closed() {
		t.Fatal("ProcessServices must survive an invalid-inventory candidate")
	}

	ok, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: inventoryCandidateConfig(t, "recovered"), Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("recover compile after invalid-inventory rejection: %v", err)
	}
	_ = ok.Close()
}
