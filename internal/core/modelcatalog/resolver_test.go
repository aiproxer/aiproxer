package modelcatalog_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCatalogResolver_pairWinsOverModelAndCatalog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	call := aipapi.Call{}
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-4o": {
			Tools:     modelcatalog.CapabilitySupported,
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchExact,
		},
	})
	set := modelcatalog.OverrideSet{
		Pair: map[string]modelcatalog.ModelFacts{
			"openai-responses:gpt-4o": {
				Tools:     modelcatalog.CapabilityUnsupported,
				Source:    modelcatalog.FactSourcePairOverride,
				MatchKind: modelcatalog.MatchExact,
			},
		},
		Model: map[string]modelcatalog.ModelFacts{
			"gpt-4o": {
				Tools:     modelcatalog.CapabilitySupported,
				Source:    modelcatalog.FactSourceModelOverride,
				MatchKind: modelcatalog.MatchExact,
			},
		},
	}
	r := modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(set),
		true,
		modelcatalog.StaticActiveSnapshotProvider{Index: idx, Ref: modelcatalog.SnapshotRef{Generation: "g1"}},
	)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-responses", Model: "gpt-4o"}}
	backend := aipapi.NewBackendCaps(aipapi.CapabilityTools, aipapi.CapabilityStreaming)
	got := r.Resolve(ctx, cand, call, backend)
	if !got.Matched {
		t.Fatal("want matched override")
	}
	if got.Facts.Source != modelcatalog.FactSourcePairOverride {
		t.Fatalf("source: %v", got.Facts.Source)
	}
	if _, ok := got.EffectiveCaps[aipapi.CapabilityTools]; ok {
		t.Fatal("pair override unsupported tools should strip tools from effective caps")
	}
	if _, ok := got.EffectiveCaps[aipapi.CapabilityStreaming]; !ok {
		t.Fatal("streaming should remain from backend")
	}
}

func TestCatalogResolver_modelOverrideOverCatalog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-4o": {
			Tools:     modelcatalog.CapabilityUnsupported,
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchExact,
		},
	})
	set := modelcatalog.OverrideSet{
		Model: map[string]modelcatalog.ModelFacts{
			"openai/gpt-4o": {
				Tools:     modelcatalog.CapabilitySupported,
				Source:    modelcatalog.FactSourceModelOverride,
				MatchKind: modelcatalog.MatchExact,
			},
		},
	}
	r := modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(set),
		true,
		modelcatalog.StaticActiveSnapshotProvider{Index: idx, Ref: modelcatalog.SnapshotRef{}},
	)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-responses", Model: "openai/gpt-4o"}}
	backend := aipapi.NewBackendCaps(aipapi.CapabilityTools)
	got := r.Resolve(ctx, cand, aipapi.Call{}, backend)
	if got.Facts.Source != modelcatalog.FactSourceModelOverride {
		t.Fatalf("source: %v", got.Facts.Source)
	}
	if _, ok := got.EffectiveCaps[aipapi.CapabilityTools]; !ok {
		t.Fatal("model override supported + backend has tools -> effective keeps tools")
	}
}

func TestCatalogResolver_catalogWhenNoOverride(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"anthropic/claude-sonnet-4": {
			Tools:             modelcatalog.CapabilitySupported,
			StructuredOutputs: modelcatalog.CapabilityUnsupported,
			Source:            modelcatalog.FactSourceCatalog,
			MatchKind:         modelcatalog.MatchExact,
		},
	})
	r := modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}),
		true,
		modelcatalog.StaticActiveSnapshotProvider{Index: idx, Ref: modelcatalog.SnapshotRef{Generation: "snap"}},
	)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "amazon/claude-sonnet-4"}}
	backend := aipapi.NewBackendCaps(aipapi.CapabilityTools, aipapi.CapabilityStructuredOutputs)
	got := r.Resolve(ctx, cand, aipapi.Call{}, backend)
	if !got.Matched {
		t.Fatal("catalog match should set Matched")
	}
	if got.Facts.Source != modelcatalog.FactSourceCatalog {
		t.Fatalf("source: %v", got.Facts.Source)
	}
	if got.Match.Kind != modelcatalog.MatchNonExact {
		t.Fatalf("match kind: %v", got.Match.Kind)
	}
	if _, ok := got.EffectiveCaps[aipapi.CapabilityStructuredOutputs]; ok {
		t.Fatal("catalog unsupported structured outputs should remove from effective")
	}
}

func TestCatalogResolver_noOverrideNoCatalogMatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-4o": {Source: modelcatalog.FactSourceCatalog},
	})
	r := modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}),
		true,
		modelcatalog.StaticActiveSnapshotProvider{Index: idx, Ref: modelcatalog.SnapshotRef{}},
	)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "totally/unknown"}}
	backend := aipapi.NewBackendCaps(aipapi.CapabilityTools)
	got := r.Resolve(ctx, cand, aipapi.Call{}, backend)
	if got.Matched {
		t.Fatal("no match should not set Matched")
	}
	if got.Facts.Source != modelcatalog.FactSourceBackendDeclaration {
		t.Fatalf("source: %v", got.Facts.Source)
	}
	if got.Match.Kind != modelcatalog.MatchNoMatch {
		t.Fatalf("match: %v", got.Match.Kind)
	}
	if len(got.EffectiveCaps) != 1 || !containsCap(got.EffectiveCaps, aipapi.CapabilityTools) {
		t.Fatalf("effective caps should equal backend: %v", got.EffectiveCaps)
	}
}

func TestCatalogResolver_ambiguousNoCatalogLimiting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"anthropic/claude-sonnet-4": {
			Tools: modelcatalog.CapabilitySupported, Source: modelcatalog.FactSourceCatalog,
		},
		"amazon/claude-sonnet-4": {
			Tools: modelcatalog.CapabilityUnsupported, Source: modelcatalog.FactSourceCatalog,
		},
	})
	r := modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}),
		true,
		modelcatalog.StaticActiveSnapshotProvider{Index: idx, Ref: modelcatalog.SnapshotRef{}},
	)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-sonnet-4"}}
	backend := aipapi.NewBackendCaps(aipapi.CapabilityTools, aipapi.CapabilityStreaming)
	got := r.Resolve(ctx, cand, aipapi.Call{}, backend)
	if got.Matched {
		t.Fatal("ambiguous catalog must not set Matched")
	}
	if got.Match.Kind != modelcatalog.MatchAmbiguous {
		t.Fatalf("match: %v", got.Match.Kind)
	}
	if len(got.EffectiveCaps) != 2 {
		t.Fatalf("backend-only effective: %v", got.EffectiveCaps)
	}
}

func TestCatalogResolver_catalogDisabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-4o": {Tools: modelcatalog.CapabilityUnsupported, Source: modelcatalog.FactSourceCatalog},
	})
	r := modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}),
		false,
		modelcatalog.StaticActiveSnapshotProvider{Index: idx, Ref: modelcatalog.SnapshotRef{}},
	)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "openai/gpt-4o"}}
	backend := aipapi.NewBackendCaps(aipapi.CapabilityTools)
	got := r.Resolve(ctx, cand, aipapi.Call{}, backend)
	if got.Matched {
		t.Fatal("disabled catalog should not match")
	}
	if _, ok := got.EffectiveCaps[aipapi.CapabilityTools]; !ok {
		t.Fatal("effective should mirror backend when catalog disabled")
	}
}

func TestCatalogResolver_intersectUnsupportedStripsCap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"m/m": {
			Tools:     modelcatalog.CapabilityUnsupported,
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchExact,
		},
	})
	r := modelcatalog.NewCatalogResolver(
		modelcatalog.DefaultMatcher{},
		modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}),
		true,
		modelcatalog.StaticActiveSnapshotProvider{Index: idx, Ref: modelcatalog.SnapshotRef{}},
	)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m/m"}}
	backend := aipapi.NewBackendCaps(aipapi.CapabilityTools, aipapi.CapabilityStreaming)
	got := r.Resolve(ctx, cand, aipapi.Call{}, backend)
	if !got.Matched {
		t.Fatal("matched catalog")
	}
	if _, ok := got.EffectiveCaps[aipapi.CapabilityTools]; ok {
		t.Fatal("tools unsupported in facts must be stripped")
	}
}

func containsCap(c aipapi.BackendCaps, want aipapi.Capability) bool {
	if c == nil {
		return false
	}
	_, ok := c[want]
	return ok
}

type flipSnapshotProvider struct {
	aIdx, bIdx *modelcatalog.SnapshotIndex
	aRef, bRef modelcatalog.SnapshotRef
	useB       atomic.Bool
}

func (f *flipSnapshotProvider) ActiveIndex() (*modelcatalog.SnapshotIndex, modelcatalog.SnapshotRef) {
	if f.useB.Load() {
		return f.bIdx, f.bRef
	}
	return f.aIdx, f.aRef
}

func TestCatalogResolver_activeSnapshotUpdatesBetweenResolveCalls(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	idxEmpty := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{})
	idxFull := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-4o": {
			Tools:     modelcatalog.CapabilitySupported,
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchExact,
		},
	})
	fp := &flipSnapshotProvider{
		aIdx: idxEmpty,
		aRef: modelcatalog.SnapshotRef{Generation: "g0"},
		bIdx: idxFull,
		bRef: modelcatalog.SnapshotRef{Generation: "g1"},
	}
	r := modelcatalog.NewCatalogResolver(modelcatalog.DefaultMatcher{}, modelcatalog.NewOverrideResolver(modelcatalog.OverrideSet{}), true, fp)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "openai/gpt-4o"}}
	backend := aipapi.NewBackendCaps(aipapi.CapabilityTools)

	got1 := r.Resolve(ctx, cand, aipapi.Call{}, backend)
	if got1.Matched {
		t.Fatal("first resolve: empty snapshot index should not produce a catalog match")
	}
	fp.useB.Store(true)
	got2 := r.Resolve(ctx, cand, aipapi.Call{}, backend)
	if !got2.Matched {
		t.Fatal("second resolve: populated index should match catalog")
	}
	if got2.Snapshot.Generation != "g1" {
		t.Fatalf("generation: got %q", got2.Snapshot.Generation)
	}
	if got2.Facts.Source != modelcatalog.FactSourceCatalog {
		t.Fatalf("source: %v", got2.Facts.Source)
	}
}
