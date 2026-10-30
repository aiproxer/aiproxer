package runtime_test

import (
	"context"
	"errors"
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

func TestBoundModel_WrongBackendCanonicalRejectsRoutePlan(t *testing.T) {
	t.Parallel()

	provider := &refreshableInventory{
		models: []modelinventory.Model{{CanonicalID: "vendor/canonical-a", NativeID: "native-a"}},
	}
	other := &refreshableInventory{
		models: []modelinventory.Model{{CanonicalID: "vendor/other", NativeID: "native-other"}},
	}
	regRT := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{
			{
				BackendID: "backend-a", Kind: "openai-responses",
				BackendPrefixes: []string{"openai-a"}, Provider: provider,
			},
			{
				BackendID: "backend-b", Kind: "openai-responses",
				BackendPrefixes: []string{"openai-b"}, Provider: other,
			},
		},
		Cache: unavailableModelCache{},
	})
	if err := regRT.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	regBound := regRT.BoundView()
	ctx := routing.WithNativeModelResolver(context.Background(), regBound)

	var opened atomic.Bool
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"backend-b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened.Store(true)
				return nil, errors.New("must not open")
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "backend-b:vendor/canonical-a"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, err = ex.Execute(ctx, call)
	if !errors.Is(err, routing.ErrWrongBackendCanonical) {
		t.Fatalf("Execute err = %v", err)
	}
	if opened.Load() {
		t.Fatal("wrong-backend canonical must not reach backend Open")
	}
}

func TestBoundModel_LogicalCatalogSeesCanonicalBackendGetsNative(t *testing.T) {
	t.Parallel()

	// Catalog only knows the canonical id; NativeID would not match.
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-canonical": {
			Tools:     modelcatalog.CapabilitySupported,
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchExact,
		},
	})
	catRT := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{})
	catRT.PublishSnapshot(modelcatalog.Snapshot{Generation: "cat-a", Index: idx})

	provider := &refreshableInventory{
		models: []modelinventory.Model{{
			CanonicalID: "openai/gpt-canonical", NativeID: "wire-native-xyz",
		}},
	}
	regRT := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "backend-a", Kind: "openai-responses",
			BackendPrefixes: []string{"openai-responses"}, Provider: provider,
		}},
		Cache: unavailableModelCache{},
	})
	if err := regRT.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	regBound := regRT.BoundView()
	ctx := modelregistry.WithBoundView(context.Background(), regBound)
	ctx = modelcatalog.WithBoundView(ctx, catRT.BoundView())
	ctx = routing.WithNativeModelResolver(ctx, regBound)

	var openedModel atomic.Value
	var resolveCapsModel atomic.Value
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"backend-a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityTools, aipapi.CapabilityStreaming),
			ResolveCaps: func(_ context.Context, _ aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
				resolveCapsModel.Store(cand.Primary.Model)
				return aipapi.NewBackendCaps(aipapi.CapabilityTools, aipapi.CapabilityStreaming)
			},
			Open: func(_ context.Context, _ aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openedModel.Store(cand.Primary.Model)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "ok"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.CatalogResolver = modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}),
		true,
		catRT,
	)
	ex.Rand = routing.NewSeededRng(1)

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "backend-a:openai/gpt-canonical"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if got, _ := openedModel.Load().(string); got != "wire-native-xyz" {
		t.Fatalf("Open model = %q, want native", got)
	}
	if got, _ := resolveCapsModel.Load().(string); got != "wire-native-xyz" {
		t.Fatalf("ResolveCaps model = %q, want native", got)
	}
	facts := ex.CatalogResolver.Resolve(ctx, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "backend-a", Model: "openai/gpt-canonical", NativeModel: "wire-native-xyz"},
	}, aipapi.Call{}, aipapi.NewBackendCaps(aipapi.CapabilityTools))
	if !facts.Matched || facts.Snapshot.Generation != "cat-a" {
		t.Fatalf("catalog must match canonical: matched=%v gen=%q", facts.Matched, facts.Snapshot.Generation)
	}
	// Matching against native alone must fail (proves catalog uses logical Model).
	nativeOnly := ex.CatalogResolver.Resolve(ctx, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "backend-a", Model: "wire-native-xyz"},
	}, aipapi.Call{}, aipapi.NewBackendCaps(aipapi.CapabilityTools))
	if nativeOnly.Matched {
		t.Fatal("catalog must not match NativeID as model id")
	}
}

func TestBoundModel_RecvFailoverKeepsBoundCatalogAfterRefresh(t *testing.T) {
	t.Parallel()

	idxA := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-a": {
			Tools: modelcatalog.CapabilitySupported, Source: modelcatalog.FactSourceCatalog, MatchKind: modelcatalog.MatchExact,
		},
	})
	idxB := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-b": {
			Tools: modelcatalog.CapabilitySupported, Source: modelcatalog.FactSourceCatalog, MatchKind: modelcatalog.MatchExact,
		},
	})
	catRT := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{})
	catRT.PublishSnapshot(modelcatalog.Snapshot{Generation: "cat-a", Index: idxA})

	provider := &refreshableInventory{
		models: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "native-a"}},
	}
	regRT := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "backend-a", Kind: "openai-responses",
			BackendPrefixes: []string{"openai-responses"}, Provider: provider,
		}, {
			BackendID: "backend-b", Kind: "openai-responses",
			BackendPrefixes: []string{"openai-b"}, Provider: &refreshableInventory{
				models: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "native-a-b"}},
			},
		}},
		Cache: unavailableModelCache{},
		Now:   func() time.Time { return time.Unix(50, 0).UTC() },
	})
	if err := regRT.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	regBound := regRT.BoundView()
	ctx := modelregistry.WithBoundView(context.Background(), regBound)
	ctx = modelcatalog.WithBoundView(ctx, catRT.BoundView())
	ctx = routing.WithNativeModelResolver(ctx, regBound)

	var openModels []string
	var openMu sync.Mutex
	var openCount atomic.Int32
	replacementStarted := make(chan struct{})
	var replacementOnce sync.Once

	openFn := func(_ context.Context, _ aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		n := openCount.Add(1)
		openMu.Lock()
		openModels = append(openModels, cand.Primary.Model)
		openMu.Unlock()
		if n == 1 {
			return &failBeforeOutputStream{}, nil
		}
		replacementOnce.Do(func() { close(replacementStarted) })
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

	call := &aipapi.Call{
		ID: "req-recv-failover",
		Route: aipapi.RouteIntent{
			Selector: "backend-a:openai/gpt-a|backend-b:openai/gpt-a",
		},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}

	// Refresh to B after initial open bound A, before Recv triggers replacement.
	catRT.PublishSnapshot(modelcatalog.Snapshot{Generation: "cat-b", Index: idxB})
	provider.set([]modelinventory.Model{{CanonicalID: "openai/gpt-b", NativeID: "native-b"}})
	regRT.RunRefresh(context.Background())

	// Bare Recv context must reattach bound A views for replacement.
	done := make(chan error, 1)
	go func() {
		_, err := aipapi.Collect(context.Background(), stream)
		done <- err
	}()
	var collectErr error
	select {
	case <-replacementStarted:
		select {
		case collectErr = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for collection after replacement open")
		}
	case collectErr = <-done:
		select {
		case <-replacementStarted:
		default:
			t.Fatal("collection completed before replacement open")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for replacement open")
	}
	if collectErr != nil {
		t.Fatalf("collect: %v", collectErr)
	}
	openMu.Lock()
	defer openMu.Unlock()
	if len(openModels) < 2 {
		t.Fatalf("expected failover open, got %v", openModels)
	}
	for _, m := range openModels {
		if m != "native-a" && m != "native-a-b" {
			t.Fatalf("replacement opened %q, want bound A natives", m)
		}
	}

	// Later logical request binds/sees B.
	laterBound := regRT.BoundView()
	if _, ok := laterBound.Lookup("openai/gpt-b"); !ok {
		t.Fatal("later bind must see B")
	}
	laterCtx := modelcatalog.WithBoundView(context.Background(), catRT.BoundView())
	facts := ex.CatalogResolver.Resolve(laterCtx, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "openai/gpt-b"},
	}, aipapi.Call{}, aipapi.NewBackendCaps(aipapi.CapabilityTools))
	if !facts.Matched || facts.Snapshot.Generation != "cat-b" {
		t.Fatalf("later catalog = matched=%v gen=%q", facts.Matched, facts.Snapshot.Generation)
	}
}

func TestBoundModel_ParallelRecvFailoverKeepsBoundView(t *testing.T) {
	t.Parallel()

	idxA := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-a": {Tools: modelcatalog.CapabilitySupported, Source: modelcatalog.FactSourceCatalog, MatchKind: modelcatalog.MatchExact},
	})
	catRT := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{})
	catRT.PublishSnapshot(modelcatalog.Snapshot{Generation: "cat-a", Index: idxA})

	provider := &refreshableInventory{
		models: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "native-a"}},
	}
	regRT := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID: "backend-a", Kind: "openai-responses",
			BackendPrefixes: []string{"openai-a"}, Provider: provider,
		}, {
			BackendID: "backend-b", Kind: "openai-responses",
			BackendPrefixes: []string{"openai-b"}, Provider: &refreshableInventory{
				models: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "native-b"}},
			},
		}},
		Cache: unavailableModelCache{},
	})
	if err := regRT.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	regBound := regRT.BoundView()
	ctx := modelregistry.WithBoundView(context.Background(), regBound)
	ctx = modelcatalog.WithBoundView(ctx, catRT.BoundView())
	ctx = routing.WithNativeModelResolver(ctx, regBound)

	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	var opened []string
	var mu sync.Mutex

	openFn := func(openCtx context.Context, _ aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		mu.Lock()
		opened = append(opened, cand.Primary.Model)
		mu.Unlock()
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
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
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
		true, catRT,
	)
	ex.Rand = routing.NewSeededRng(1)

	done := make(chan error, 1)
	go func() {
		call := &aipapi.Call{
			Route: aipapi.RouteIntent{Selector: "backend-a:openai/gpt-a!backend-b:openai/gpt-a"},
			Messages: []aipapi.Message{{
				Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
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
	catRT.PublishSnapshot(modelcatalog.Snapshot{
		Generation: "cat-b",
		Index: modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
			"openai/gpt-b": {Source: modelcatalog.FactSourceCatalog},
		}),
	})
	provider.set([]modelinventory.Model{{CanonicalID: "openai/gpt-b", NativeID: "native-new"}})
	regRT.RunRefresh(context.Background())
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, m := range opened {
		if m != "native-a" && m != "native-b" {
			t.Fatalf("parallel open used refreshed model %q", m)
		}
	}
}

// failBeforeOutputStream fails on first Recv before any output event.
type failBeforeOutputStream struct{}

func (f *failBeforeOutputStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, aipapi.RecoverablePreOutputError(errors.New("recoverable pre-output failure"))
}
func (f *failBeforeOutputStream) Close() error { return nil }
func (f *failBeforeOutputStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

var _ aipapi.ManagedEventStream = (*failBeforeOutputStream)(nil)
