package runtime_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestBoundModel_refreshDuringBlockedRequestKeepsCatalogAndNativeMapping(t *testing.T) {
	t.Parallel()

	idxA := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-a": {
			Tools:     modelcatalog.CapabilitySupported,
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchExact,
		},
	})
	idxB := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-b": {
			Tools:     modelcatalog.CapabilitySupported,
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchExact,
		},
	})
	catRT := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{})
	catRT.PublishSnapshot(modelcatalog.Snapshot{Generation: "cat-a", Index: idxA})

	provider := &refreshableInventory{
		models: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "native-a"}},
	}
	providerB := &refreshableInventory{
		models: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "native-a-b"}},
	}
	regRT := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend-a",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}, {
			BackendID:       "backend-b",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-b"},
			Provider:        providerB,
		}},
		Cache: unavailableModelCache{},
		Now:   func() time.Time { return time.Unix(50, 0).UTC() },
	})
	if err := regRT.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	regBound := regRT.BoundView()
	ctx := context.Background()
	ctx = modelregistry.WithBoundView(ctx, regBound)
	ctx = modelcatalog.WithBoundView(ctx, catRT.BoundView())
	ctx = routing.WithNativeModelResolver(ctx, regBound)

	entered := make(chan struct{})
	release := make(chan struct{})
	var openedModels []string
	var openedMu sync.Mutex
	var enterOnce sync.Once

	openFn := func(openCtx context.Context, _ aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		openedMu.Lock()
		openedModels = append(openedModels, cand.Primary.Model)
		openedMu.Unlock()
		enterOnce.Do(func() { close(entered) })
		select {
		case <-release:
		case <-openCtx.Done():
			return nil, openCtx.Err()
		}
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "ok"},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	}
	caps := aipapi.NewBackendCaps(aipapi.CapabilityTools, aipapi.CapabilityStreaming)

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"backend-a": {Caps: caps, Open: openFn},
		"backend-b": {Caps: caps, Open: openFn},
	}
	ex.CatalogResolver = modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}),
		true,
		catRT,
	)
	ex.Rand = routing.NewSeededRng(1)

	done := make(chan error, 1)
	go func() {
		call := &aipapi.Call{
			ID: "req-1",
			Route: aipapi.RouteIntent{
				Selector: "backend-a:openai/gpt-a|backend-b:openai/gpt-a",
			},
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("hi")},
			}},
		}
		stream, err := ex.Execute(ctx, call)
		if err != nil {
			done <- err
			return
		}
		_, err = aipapi.Collect(context.Background(), stream)
		done <- err
	}()
	<-entered

	// Mid-request refresh publishes catalog/registry generation B.
	catRT.PublishSnapshot(modelcatalog.Snapshot{Generation: "cat-b", Index: idxB})
	provider.set([]modelinventory.Model{{CanonicalID: "openai/gpt-b", NativeID: "native-b"}})
	regRT.RunRefresh(context.Background())

	facts := ex.CatalogResolver.Resolve(ctx, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "backend-a", Model: "openai/gpt-a"},
	}, aipapi.Call{}, aipapi.NewBackendCaps(aipapi.CapabilityTools))
	if !facts.Matched || facts.Snapshot.Generation != "cat-a" {
		t.Fatalf("bound catalog during refresh: matched=%v gen=%q", facts.Matched, facts.Snapshot.Generation)
	}
	bv, ok := modelregistry.BoundViewFromContext(ctx)
	if !ok {
		t.Fatal("missing bound registry view")
	}
	native, mapped := bv.ResolveNative("backend-a", "openai/gpt-a")
	if !mapped || native != "native-a" {
		t.Fatalf("bound native during refresh: %q %v", native, mapped)
	}
	if _, ok := bv.Lookup("openai/gpt-b"); ok {
		t.Fatal("bound registry must not observe refreshed B models")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("execute: %v", err)
	}
	openedMu.Lock()
	defer openedMu.Unlock()
	if len(openedModels) == 0 {
		t.Fatal("expected at least one open")
	}
	for _, m := range openedModels {
		if m != "native-a" && m != "native-a-b" {
			t.Fatalf("opened model = %q, want bound A native", m)
		}
	}
}

func TestBoundModel_routePlanAppliesExactBackendNativeIDs(t *testing.T) {
	t.Parallel()

	provider := &refreshableInventory{
		models: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "native-a"}},
	}
	regRT := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend-a",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: unavailableModelCache{},
	})
	if err := regRT.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	regBound := regRT.BoundView()
	ctx := modelregistry.WithBoundView(context.Background(), regBound)
	ctx = routing.WithNativeModelResolver(ctx, regBound)

	var gotModel atomic.Value
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"backend-a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				gotModel.Store(cand.Primary.Model)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "ok"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "backend-a:openai/gpt-a"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if got, _ := gotModel.Load().(string); got != "native-a" {
		t.Fatalf("model = %q, want native-a", got)
	}
}

type unavailableModelCache struct{}

func (unavailableModelCache) Load(context.Context) (modelregistry.Snapshot, error) {
	return modelregistry.Snapshot{}, modelregistry.ErrSnapshotUnavailable
}
func (unavailableModelCache) Save(context.Context, modelregistry.Snapshot) error { return nil }

type refreshableInventory struct {
	mu     sync.Mutex
	models []modelinventory.Model
}

func (p *refreshableInventory) set(models []modelinventory.Model) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models = append([]modelinventory.Model(nil), models...)
}

func (p *refreshableInventory) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote,
		Models: append([]modelinventory.Model(nil), p.models...),
	}, nil
}
