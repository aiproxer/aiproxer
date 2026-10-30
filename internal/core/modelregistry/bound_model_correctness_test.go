package modelregistry_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestRuntime_AllowlistUnionBeforePublication(t *testing.T) {
	t.Parallel()

	acceptStarted := make(chan struct{})
	acceptRelease := make(chan struct{})
	var acceptGen atomic.Int32
	var publishedSeenDuringAccept atomic.Bool

	provider := &barrierAcceptProvider{
		models: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "native-a"}},
		onAccept: func(models []modelinventory.Model) {
			gen := acceptGen.Add(1)
			if gen == 2 {
				// Second accept is refresh to B (union A+B). Block so the test
				// can prove publication has not advanced yet.
				close(acceptStarted)
				<-acceptRelease
			}
			_ = models
		},
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
		Now:   func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	boundA := rt.BoundView()
	bodyA, _, ok := boundA.ModelsJSON()
	if !ok {
		t.Fatal("ModelsJSON A")
	}

	provider.set([]modelinventory.Model{{CanonicalID: "openai/gpt-b", NativeID: "native-b"}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.RunRefresh(context.Background())
	}()
	<-acceptStarted

	// While AcceptInventory for B is blocked, /v1/models must still advertise A.
	liveBody, _, _ := rt.ModelsJSON()
	if string(liveBody) != string(bodyA) {
		publishedSeenDuringAccept.Store(true)
		t.Fatal("must not advertise B before provider acceptance completes")
	}
	if _, ok := rt.Lookup("openai/gpt-b"); ok {
		publishedSeenDuringAccept.Store(true)
		t.Fatal("Lookup must not see B before acceptance")
	}

	close(acceptRelease)
	<-done
	if publishedSeenDuringAccept.Load() {
		t.Fatal("publication raced ahead of acceptance")
	}

	accepted := provider.Accepted()
	natives := map[string]bool{}
	for _, m := range accepted {
		natives[m.NativeID] = true
	}
	if !natives["native-a"] || !natives["native-b"] {
		t.Fatalf("accepted union = %+v", accepted)
	}
	boundB := rt.BoundView()
	if _, ok := boundB.Lookup("openai/gpt-a"); ok {
		t.Fatal("new BoundView must advertise only B")
	}
	if _, ok := boundB.Lookup("openai/gpt-b"); !ok {
		t.Fatal("new BoundView must include B")
	}
	// Old bound request still resolves A mapping.
	native, mapped := boundA.ResolveNative("backend", "openai/gpt-a")
	if !mapped || native != "native-a" {
		t.Fatalf("old bound ResolveNative = %q %v", native, mapped)
	}
}

func TestRuntime_ConflictingExactMappingRejectsBuildAndRefresh(t *testing.T) {
	t.Parallel()

	conflict := &countingInventoryProvider{models: []modelinventory.Model{
		{CanonicalID: "openai/gpt-a", NativeID: "native-1"},
		{CanonicalID: "openai/gpt-a", NativeID: "native-2"},
	}}
	_, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{{
		BackendID:       "backend",
		Kind:            "openai-responses",
		BackendPrefixes: []string{"openai-responses"},
		Provider:        conflict,
	}}, nil)
	if !errors.Is(err, modelregistry.ErrConflictingMapping) {
		t.Fatalf("Build conflict err = %v", err)
	}

	good := &gatedInventoryProvider{
		modelsA: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "native-a"}},
		modelsB: []modelinventory.Model{
			{CanonicalID: "openai/gpt-a", NativeID: "native-1"},
			{CanonicalID: "openai/gpt-a", NativeID: "native-2"},
		},
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        good,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
		Now:   func() time.Time { return time.Unix(200, 0).UTC() },
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, ok := rt.Lookup("openai/gpt-a")
	if !ok || before[0].NativeID != "native-a" {
		t.Fatalf("before = %+v", before)
	}

	good.useB = true
	rt.RunRefresh(context.Background())
	after, ok := rt.Lookup("openai/gpt-a")
	if !ok || after[0].NativeID != "native-a" {
		t.Fatalf("refresh must preserve prior publication: %+v", after)
	}
	if _, ok := rt.Lookup("openai/gpt-b"); ok {
		t.Fatal("conflicting refresh must not publish new models")
	}
}

func TestBoundView_PublicationDiscoveryCoherentUnderRace(t *testing.T) {
	t.Parallel()

	provider := &gatedInventoryProvider{
		modelsA: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "native-a"}},
		modelsB: []modelinventory.Model{{CanonicalID: "openai/gpt-b", NativeID: "native-b"}},
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: &fakeModelRegistryCache{loadErr: modelregistry.ErrSnapshotUnavailable},
		Now: func() time.Time {
			return time.Unix(300, 0).UTC()
		},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for range 8 {
		wg.Go(func() {
			<-start
			for range 100 {
				v := rt.BoundView()
				d := v.Diagnostics()
				hasA, _ := v.Lookup("openai/gpt-a")
				hasB, _ := v.Lookup("openai/gpt-b")
				switch {
				case hasA != nil && hasB == nil:
					if d.ModelCount != 1 {
						errs <- "A publication with wrong model_count"
					}
					for _, disc := range d.BackendDiscoveries {
						if disc.ModelCount != 0 && disc.ModelCount != 1 {
							errs <- "mixed discovery count for A"
						}
					}
				case hasB != nil && hasA == nil:
					if d.ModelCount != 1 {
						errs <- "B publication with wrong model_count"
					}
				case hasA == nil && hasB == nil:
					// empty / transitional
				default:
					errs <- "mixed A/B lookup on one BoundView"
				}
			}
		})
	}
	close(start)
	for i := range 20 {
		provider.useB = i%2 == 1
		rt.RunRefresh(context.Background())
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatal(msg)
	}
}

type barrierAcceptProvider struct {
	mu       sync.Mutex
	models   []modelinventory.Model
	accepted []modelinventory.Model
	onAccept func([]modelinventory.Model)
}

func (p *barrierAcceptProvider) set(models []modelinventory.Model) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models = append([]modelinventory.Model(nil), models...)
}

func (p *barrierAcceptProvider) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote,
		Models: append([]modelinventory.Model(nil), p.models...),
	}, nil
}

func (p *barrierAcceptProvider) AcceptInventory(models []modelinventory.Model) {
	if p.onAccept != nil {
		p.onAccept(models)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accepted = append([]modelinventory.Model(nil), models...)
}

func (p *barrierAcceptProvider) Accepted() []modelinventory.Model {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]modelinventory.Model(nil), p.accepted...)
}
