package featurebundle_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
)

type genFilter struct{ id string }

func (g genFilter) ID() string                      { return g.id }
func (genFilter) Order() int                        { return 0 }
func (genFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (genFilter) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type genFinalizer struct{ id string }

func (g genFinalizer) ID() string { return g.id }
func (genFinalizer) Order() int   { return 0 }
func (genFinalizer) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{}, nil
}

type genCompactionObs struct{ tag string }

func (g genCompactionObs) OnCompaction(context.Context, compaction.Event) error {
	return nil
}

func makeGenBundle(t *testing.T, tag string) aipfeature.FeatureBundle {
	t.Helper()
	return testkit.FeatureBundle(t, tag, func(cs *aipfeature.ContributionSet) error {
		if err := aipfeature.Contribute(cs, aipfeature.PlaneCompactionObservers, tag, []compaction.Observer{genCompactionObs{tag: tag}}); err != nil {
			return err
		}
		return aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, tag, []toolcall.Finalizer{genFinalizer{id: tag}})
	}, nil)
}

// TestMergeBundlesGenerated_GenerationRebuildIsolated proves candidate feature
// merges do not share slice backing arrays across generations.
func TestMergeBundlesGenerated_GenerationRebuildIsolated(t *testing.T) {
	t.Parallel()
	genA, errA := featurebundle.MergeBundlesGenerated(makeGenBundle(t, "gen-a"))
	if errA != nil {
		t.Fatalf("genA error: %v", errA)
	}
	genB, errB := featurebundle.MergeBundlesGenerated(makeGenBundle(t, "gen-b"))
	if errB != nil {
		t.Fatalf("genB error: %v", errB)
	}
	fA := aipfeature.Get(genA.Frozen, aipfeature.PlaneToolCallFinalizers)
	fB := aipfeature.Get(genB.Frozen, aipfeature.PlaneToolCallFinalizers)
	if len(fA) != 1 || fA[0].ID() != "gen-a" {
		t.Fatalf("genA finalizers=%v", fA)
	}
	if len(fB) != 1 || fB[0].ID() != "gen-b" {
		t.Fatalf("genB finalizers=%v", fB)
	}
	oA := aipfeature.Get(genA.Frozen, aipfeature.PlaneCompactionObservers)
	oB := aipfeature.Get(genB.Frozen, aipfeature.PlaneCompactionObservers)
	if len(oA) != 1 {
		t.Fatalf("genA observers=%v", oA)
	}
	if len(oB) != 1 {
		t.Fatalf("genB observers=%v", oB)
	}
}
