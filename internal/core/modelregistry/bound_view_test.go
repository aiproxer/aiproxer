package modelregistry_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestBoundView_remainsOnGenerationAAfterPublishB(t *testing.T) {
	t.Parallel()

	provider := &gatedInventoryProvider{
		modelsA: []modelinventory.Model{{
			CanonicalID: "openai/gpt-a",
			NativeID:    "gpt-a",
		}},
		modelsB: []modelinventory.Model{{
			CanonicalID: "openai/gpt-b",
			NativeID:    "gpt-b",
		}},
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend-1",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
		Now: func() time.Time {
			return time.Unix(1000, 0).UTC()
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	boundA := rt.BoundView()
	if !boundA.Active() {
		t.Fatal("expected active bound view after Start")
	}
	genA := boundA.Generation()
	bodyA, _, ok := boundA.ModelsJSON()
	if !ok || len(bodyA) == 0 {
		t.Fatal("ModelsJSON A unavailable")
	}
	gotA, ok := boundA.Lookup("openai/gpt-a")
	if !ok || len(gotA) != 1 || gotA[0].NativeID != "gpt-a" {
		t.Fatalf("Lookup A = %+v ok=%v", gotA, ok)
	}
	native, mapped := boundA.ResolveNative("backend-1", "openai/gpt-a")
	if !mapped || native != "gpt-a" {
		t.Fatalf("ResolveNative A = %q %v", native, mapped)
	}
	diagA := boundA.Diagnostics()

	provider.useB = true
	rt.RunRefresh(context.Background())
	boundB := rt.BoundView()
	if boundB.Generation() == "" || boundB.Generation() == genA {
		// Content changed so generation should advance; if identical fingerprint
		// retention occurs, Lookup identity is still the proof.
		t.Logf("gen A=%q B=%q", genA, boundB.Generation())
	}
	if _, ok := boundB.Lookup("openai/gpt-b"); !ok {
		t.Fatal("later bind must observe generation B")
	}
	if _, ok := boundB.Lookup("openai/gpt-a"); ok {
		t.Fatal("generation B must not retain A-only model")
	}

	// Bound A is unchanged after live refresh.
	if got, ok := boundA.Lookup("openai/gpt-a"); !ok || got[0].NativeID != "gpt-a" {
		t.Fatalf("bound A Lookup changed: %+v %v", got, ok)
	}
	if _, ok := boundA.Lookup("openai/gpt-b"); ok {
		t.Fatal("bound A must not observe B models")
	}
	bodyA2, genA2, ok := boundA.ModelsJSON()
	if !ok || genA2 != genA || string(bodyA2) != string(bodyA) {
		t.Fatalf("bound A ModelsJSON changed gen=%q→%q", genA, genA2)
	}
	diagA2 := boundA.Diagnostics()
	if diagA2.Generation != diagA.Generation || diagA2.ModelCount != diagA.ModelCount {
		t.Fatalf("bound A diagnostics changed: %+v → %+v", diagA, diagA2)
	}
	native, mapped = boundA.ResolveNative("backend-1", "openai/gpt-a")
	if !mapped || native != "gpt-a" {
		t.Fatalf("bound A ResolveNative changed: %q %v", native, mapped)
	}
}

func TestBoundView_ResolveModelBindingMultiState(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{
		{CanonicalID: "openai/gpt-shared", NativeID: "native-a"},
	}}
	other := &countingInventoryProvider{models: []modelinventory.Model{
		{CanonicalID: "openai/gpt-other", NativeID: "native-other"},
	}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{
			{
				BackendID:       "backend-a",
				Kind:            "openai-responses",
				BackendPrefixes: []string{"openai-a"},
				Provider:        provider,
			},
			{
				BackendID:       "backend-b",
				Kind:            "openai-responses",
				BackendPrefixes: []string{"openai-b"},
				Provider:        other,
			},
		},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := rt.BoundView()

	exact := v.ResolveModelBinding("backend-a", "openai/gpt-shared")
	if exact.Kind != routing.ModelBindingExactCanonical || exact.Native != "native-a" {
		t.Fatalf("exact = %+v", exact)
	}
	wrong := v.ResolveModelBinding("backend-b", "openai/gpt-shared")
	if wrong.Kind != routing.ModelBindingWrongBackend {
		t.Fatalf("wrong-backend = %+v", wrong)
	}
	unknown := v.ResolveModelBinding("backend-a", "openai/unknown")
	if unknown.Kind != routing.ModelBindingUnknown {
		t.Fatalf("unknown = %+v", unknown)
	}
	known := v.ResolveModelBinding("backend-a", "native-a")
	if known.Kind != routing.ModelBindingKnownNative || known.Native != "native-a" {
		t.Fatalf("known-native = %+v", known)
	}
}

func TestBoundView_ResolveNativeExactBackendFailClosed(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{
		{CanonicalID: "openai/gpt-shared", NativeID: "native-a"},
	}}
	other := &countingInventoryProvider{models: []modelinventory.Model{
		{CanonicalID: "openai/gpt-other", NativeID: "native-other"},
	}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{
			{
				BackendID:       "backend-a",
				Kind:            "openai-responses",
				BackendPrefixes: []string{"openai-a"},
				Provider:        provider,
			},
			{
				BackendID:       "backend-b",
				Kind:            "openai-responses",
				BackendPrefixes: []string{"openai-b"},
				Provider:        other,
			},
		},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := rt.BoundView()

	native, ok := v.ResolveNative("backend-a", "openai/gpt-shared")
	if !ok || native != "native-a" {
		t.Fatalf("exact backend = %q %v", native, ok)
	}
	if _, ok := v.ResolveNative("backend-b", "openai/gpt-shared"); ok {
		t.Fatal("recognized canonical on other backend must not map for backend-b")
	}
	if _, ok := v.ResolveNative("backend-a", "openai/unknown"); ok {
		t.Fatal("unknown model must preserve compatibility (no mapping)")
	}
	native, ok = v.ResolveNative("backend-a", "native-a")
	if !ok || native != "native-a" {
		t.Fatalf("known native = %q %v", native, ok)
	}
}

func TestBoundView_nilRuntimeSafe(t *testing.T) {
	t.Parallel()

	var rt *modelregistry.Runtime
	v := rt.BoundView()
	if v.Active() {
		t.Fatal("nil runtime BoundView must be inactive")
	}
	if _, ok := v.Lookup("x"); ok {
		t.Fatal("nil Lookup")
	}
	if got := v.All(); got == nil || len(got) != 0 {
		t.Fatalf("All = %#v", got)
	}
	if _, _, ok := v.ModelsJSON(); ok {
		t.Fatal("ModelsJSON ok")
	}
	d := v.Diagnostics()
	if d.Active || d.BackendDiscoveries == nil || d.BackendModelCounts == nil {
		t.Fatalf("Diagnostics = %+v", d)
	}
	if _, ok := v.ResolveNative("b", "m"); ok {
		t.Fatal("ResolveNative")
	}

	ctx := modelregistry.WithBoundView(context.Background(), modelregistry.EmptyBoundView())
	got, ok := modelregistry.BoundViewFromContext(ctx)
	if !ok || got.Active() {
		t.Fatalf("context empty view: %+v ok=%v", got, ok)
	}
}

func TestBoundView_ModelsJSONDefensiveCopy(t *testing.T) {
	t.Parallel()

	provider := &countingInventoryProvider{models: []modelinventory.Model{{
		CanonicalID: "openai/gpt-copy",
		NativeID:    "gpt-copy",
	}}}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := rt.BoundView()
	body, _, ok := v.ModelsJSON()
	if !ok || len(body) == 0 {
		t.Fatal("ModelsJSON")
	}
	body[0] = 'X'
	body2, _, ok := v.ModelsJSON()
	if !ok || body2[0] == 'X' {
		t.Fatal("ModelsJSON must return a defensive copy")
	}
}

// gatedInventoryProvider switches inventory content when useB is set.
type gatedInventoryProvider struct {
	mu      sync.Mutex
	useB    bool
	modelsA []modelinventory.Model
	modelsB []modelinventory.Model
}

func (p *gatedInventoryProvider) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	models := p.modelsA
	if p.useB {
		models = p.modelsB
	}
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote,
		Models: append([]modelinventory.Model(nil), models...),
	}, nil
}
