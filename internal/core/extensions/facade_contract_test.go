package extensions_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	coretraffic "github.com/aiproxer/aiproxer/internal/core/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

type gateID string

func (g gateID) ID() string                      { return string(g) }
func (gateID) Order() int                        { return 0 }
func (gateID) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateID) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

func TestCompletionGatesFromContext_withoutSnapshotUsesFallback(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneCompletionGates, "test", []completion.Gate{gateID("fb")})
	fallback := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	// t.Context() (like Background/TODO) has no snapshot; must hit fallback path.
	got := extensions.CompletionGatesFromContext(t.Context(), fallback)
	if len(got) != 1 || got[0].ID() != "fb" {
		t.Fatalf("got %+v", got)
	}
}

func TestCompletionGatesFromContext_prefersContextOverFallback(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	csetCtx := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(csetCtx, aipfeature.PlaneCompletionGates, "test", []completion.Gate{gateID("ctx")})
	ctxSnap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: csetCtx.Freeze(),
	})
	csetFb := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(csetFb, aipfeature.PlaneCompletionGates, "test", []completion.Gate{gateID("fb")})
	fallback := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: csetFb.Freeze(),
	})
	ctx := extensions.WithRequestRuntimeSnapshot(t.Context(), ctxSnap)
	got := extensions.CompletionGatesFromContext(ctx, fallback)
	if len(got) != 1 || got[0].ID() != "ctx" {
		t.Fatalf("want ctx gate, got %+v", got)
	}
}

func TestCompletionGatesFromContext_missingContextUsesFallback(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneCompletionGates, "test", []completion.Gate{gateID("fb")})
	fallback := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	got := extensions.CompletionGatesFromContext(t.Context(), fallback)
	if len(got) != 1 || got[0].ID() != "fb" {
		t.Fatalf("got %+v", got)
	}
}

func TestCompletionGatesFromContext_nilFallback(t *testing.T) {
	t.Parallel()
	got := extensions.CompletionGatesFromContext(t.Context(), nil)
	if len(got) != 0 {
		t.Fatalf("want empty, got len=%d", len(got))
	}
	if got == nil {
		t.Fatal("want non-nil empty slice")
	}
}

type otherGatesView struct {
	gates []completion.Gate
}

func (o otherGatesView) CompletionGates() []completion.Gate {
	return o.gates
}

func TestCompletionGatesFromContext_fallbackIsInterface(t *testing.T) {
	t.Parallel()
	fallback := otherGatesView{gates: []completion.Gate{gateID("alt")}}
	got := extensions.CompletionGatesFromContext(t.Context(), fallback)
	if len(got) != 1 || got[0].ID() != "alt" {
		t.Fatalf("got %+v", got)
	}
}

func TestRequestRuntimeSnapshot_TrafficPortBundle_nil(t *testing.T) {
	t.Parallel()
	var snap *extensions.RequestRuntimeSnapshot
	b := snap.TrafficPortBundle()
	if !b.EmitIsNoop() {
		t.Fatal("nil snapshot traffic bundle should be noop")
	}
}

func TestRequestRuntimeSnapshot_TrafficPortBundle_matchesPortBundleFromSnapshot(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneTrafficRedactors, "test", []sdktraffic.Redactor{stubTrafficRed{}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		TrafficObserver: sdktraffic.NoopObserver{},
		FeaturePlanes:   cset.Freeze(),
	})
	got := snap.TrafficPortBundle()
	want := coretraffic.PortBundleFromSnapshot(snap)
	if got.Raw != want.Raw || got.Obs != want.Obs || len(got.Red) != len(want.Red) {
		t.Fatalf("got vs want Raw=%v Obs=%v lens red %d/%d", got.Raw, got.Obs, len(got.Red), len(want.Red))
	}
	for i := range got.Red {
		if got.Red[i] != want.Red[i] {
			t.Fatalf("redactor %d ptr mismatch", i)
		}
	}
}
