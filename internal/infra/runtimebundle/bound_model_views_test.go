package runtimebundle_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/modelview"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestGenerationBundle_BindModelViewsFreezesPublications(t *testing.T) {
	t.Parallel()

	provider := &bundleFlipInventory{
		a: []modelinventory.Model{{CanonicalID: "openai/gpt-a", NativeID: "gpt-a"}},
		b: []modelinventory.Model{{CanonicalID: "openai/gpt-b", NativeID: "gpt-b"}},
	}
	regRT := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "backend",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai-responses"},
			Provider:        provider,
		}},
		Cache: unavailableBundleCache{},
		Now:   func() time.Time { return time.Unix(3, 0).UTC() },
	})
	if err := regRT.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	catRT := modelcatalog.NewCatalogRuntime(modelcatalog.RuntimeConfig{})
	idxA := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-a": {Source: modelcatalog.FactSourceCatalog},
	})
	catRT.PublishSnapshot(modelcatalog.Snapshot{Generation: "cat-a", Index: idxA})

	bundle := runtimebundle.NewGenerationBundleForTest(regRT, catRT)
	var _ runtimehost.ModelViewBinder = bundle

	ctx := bundle.BindModelViews(context.Background())
	regBound, ok := modelregistry.BoundViewFromContext(ctx)
	if !ok || !regBound.Active() {
		t.Fatal("registry bound view missing")
	}
	catBound, ok := modelcatalog.BoundViewFromContext(ctx)
	if !ok || catBound.Generation() != "cat-a" {
		t.Fatalf("catalog bound view = %+v ok=%v", catBound, ok)
	}

	provider.useB = true
	regRT.RunRefresh(context.Background())
	catRT.PublishSnapshot(modelcatalog.Snapshot{
		Generation: "cat-b",
		Index: modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
			"openai/gpt-b": {Source: modelcatalog.FactSourceCatalog},
		}),
	})

	if _, ok := regBound.Lookup("openai/gpt-b"); ok {
		t.Fatal("bound registry must stay on A")
	}
	if catBound.Generation() != "cat-a" {
		t.Fatal("bound catalog must stay on A")
	}
	later := bundle.BindModelViews(context.Background())
	laterReg, _ := modelregistry.BoundViewFromContext(later)
	if _, ok := laterReg.Lookup("openai/gpt-b"); !ok {
		t.Fatal("later bind must observe B")
	}
	id, ok := modelview.FromContext(ctx)
	if !ok || id.Digest == "" {
		t.Fatal("aggregate model-view identity missing")
	}
	if id.RegistryGeneration == "" || id.CatalogGeneration != "cat-a" {
		t.Fatalf("identity = %+v", id)
	}
}

type unavailableBundleCache struct{}

func (unavailableBundleCache) Load(context.Context) (modelregistry.Snapshot, error) {
	return modelregistry.Snapshot{}, modelregistry.ErrSnapshotUnavailable
}
func (unavailableBundleCache) Save(context.Context, modelregistry.Snapshot) error { return nil }

type bundleFlipInventory struct {
	useB bool
	a, b []modelinventory.Model
}

func (p *bundleFlipInventory) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	models := p.a
	if p.useB {
		models = p.b
	}
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote,
		Models: append([]modelinventory.Model(nil), models...),
	}, nil
}
