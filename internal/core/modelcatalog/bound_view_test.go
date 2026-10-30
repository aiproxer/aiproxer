package modelcatalog_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestBoundView_remainsOnSnapshotAAfterPublishB(t *testing.T) {
	t.Parallel()

	s1 := snapshotJSON(t, `{"a":{"id":"a","models":[{"id":"m1","tool_call":true}]}}`, time.Unix(1, 0))
	s1.Generation = "gen-a"
	s2 := snapshotJSON(t, `{"a":{"id":"a","models":[{"id":"m2","tool_call":true}]}}`, time.Unix(2, 0))
	s2.Generation = "gen-b"
	src := &memSource{snaps: []modelcatalog.Snapshot{s1, s2}}
	cache := &memCache{loadErr: errNoCache}

	rt := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{
		Source: src,
		Cache:  cache,
	})
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	rt.RunRefresh(ctx)
	boundA := rt.BoundView()
	if !boundA.Active() || boundA.Generation() != "gen-a" {
		t.Fatalf("bound A = gen %q active=%v", boundA.Generation(), boundA.Active())
	}
	idxA, refA := boundA.ActiveIndex()
	if idxA == nil || refA.Generation != "gen-a" {
		t.Fatalf("ActiveIndex A = %v %q", idxA != nil, refA.Generation)
	}

	rt.RunRefresh(ctx)
	boundB := rt.BoundView()
	if boundB.Generation() != "gen-b" {
		t.Fatalf("later bind gen = %q, want gen-b", boundB.Generation())
	}

	if gen := boundA.Generation(); gen != "gen-a" {
		t.Fatalf("bound A generation changed to %q", gen)
	}
	idxA2, refA2 := boundA.ActiveIndex()
	if idxA2 != idxA || refA2.Generation != "gen-a" {
		t.Fatal("bound A ActiveIndex must remain snapshot A")
	}
}

func TestCatalogResolver_prefersBoundViewOverLiveProvider(t *testing.T) {
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
	fp := &flipSnapshotProvider{
		aIdx: idxA,
		aRef: modelcatalog.SnapshotRef{Generation: "g-a"},
		bIdx: idxB,
		bRef: modelcatalog.SnapshotRef{Generation: "g-b"},
	}
	r := modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}),
		true,
		fp,
	)
	rt := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{})
	rt.PublishSnapshot(modelcatalog.Snapshot{Generation: "g-a", Index: idxA})
	ctxA := modelcatalog.WithBoundView(context.Background(), rt.BoundView())

	rt.PublishSnapshot(modelcatalog.Snapshot{Generation: "g-b", Index: idxB})
	fp.useB.Store(true)

	candA := routing.AttemptCandidate{Primary: routing.Primary{Model: "openai/gpt-a"}}
	candB := routing.AttemptCandidate{Primary: routing.Primary{Model: "openai/gpt-b"}}
	backend := aipapi.NewBackendCaps(aipapi.CapabilityTools)

	gotA := r.Resolve(ctxA, candA, aipapi.Call{}, backend)
	if !gotA.Matched || gotA.Snapshot.Generation != "g-a" {
		t.Fatalf("bound ctx must keep A: matched=%v gen=%q", gotA.Matched, gotA.Snapshot.Generation)
	}
	gotBOnA := r.Resolve(ctxA, candB, aipapi.Call{}, backend)
	if gotBOnA.Matched {
		t.Fatal("bound A must not match B-only model")
	}

	ctxB := modelcatalog.WithBoundView(context.Background(), rt.BoundView())
	gotB := r.Resolve(ctxB, candB, aipapi.Call{}, backend)
	if !gotB.Matched || gotB.Snapshot.Generation != "g-b" {
		t.Fatalf("later bound ctx must see B: matched=%v gen=%q", gotB.Matched, gotB.Snapshot.Generation)
	}

	// Compatibility path without bound view still follows live provider.
	live := r.Resolve(context.Background(), candB, aipapi.Call{}, backend)
	if !live.Matched || live.Snapshot.Generation != "g-b" {
		t.Fatalf("live fallback: matched=%v gen=%q", live.Matched, live.Snapshot.Generation)
	}
}

func TestBoundView_SnapshotWirePayloadDefensiveCopy(t *testing.T) {
	t.Parallel()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"m/a": {Source: modelcatalog.FactSourceCatalog},
	})
	rt := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{})
	rt.PublishSnapshot(modelcatalog.Snapshot{
		Generation:  "g1",
		Index:       idx,
		WirePayload: []byte(`{"ok":true}`),
	})
	v := rt.BoundView()
	snap, ok := v.Snapshot()
	if !ok || len(snap.WirePayload) == 0 {
		t.Fatal("Snapshot")
	}
	snap.WirePayload[0] = 'X'
	snap2, ok := v.Snapshot()
	if !ok || snap2.WirePayload[0] == 'X' {
		t.Fatal("WirePayload must be deep-cloned")
	}
}

func TestBoundView_nilRuntimeSafe(t *testing.T) {
	t.Parallel()
	var rt *modelcatalog.CatalogRuntime
	v := rt.BoundView()
	if v.Active() {
		t.Fatal("nil runtime must yield inactive view")
	}
	idx, ref := v.ActiveIndex()
	if idx != nil || ref.Generation != "" {
		t.Fatalf("ActiveIndex = %v %q", idx != nil, ref.Generation)
	}
	if _, ok := v.Snapshot(); ok {
		t.Fatal("Snapshot ok")
	}
}

var errNoCache = stringError("no cache")

type stringError string

func (e stringError) Error() string { return string(e) }
