package pluginreg_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refautoappend"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftoolpolicy"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftraffictranscript"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refverifier"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refworkspaceguard"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

// proofReferenceIDs are the stage-four reference proof plugins (design §19, task 11).
var proofReferenceIDs = []string{
	refautoappend.ID,
	reftoolpolicy.ID,
	refworkspaceguard.ID,
	reftraffictranscript.ID,
	refverifier.ID,
}

func TestProofReferenceFeatures_buildEmptyYAML(t *testing.T) {
	t.Parallel()
	reg := &pluginreg.Registry{}
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	for _, id := range proofReferenceIDs {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			b, err := reg.BuildFeatureBundle(id, empty)
			if err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			if b.SchemaVersion == 0 {
				t.Fatalf("%s: missing schema version", id)
			}
		})
	}
}

func TestProofReferenceFeatures_mergeSurface(t *testing.T) {
	t.Parallel()
	reg := &pluginreg.Registry{}
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var regs []aipsdk.Registration
	var empty yaml.Node
	for _, id := range proofReferenceIDs {
		regs = append(regs, aipsdk.Registration{
			Kind:        aipsdk.PluginKindFeature,
			ID:          id,
			FactoryKind: id,
			Enabled:     true,
			Config:      aipsdk.ConfigPayload{Node: empty},
		})
	}
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, regs)
	if err != nil {
		t.Fatal(err)
	}
	const need = 1
	// Coarse shape: each proof contributes at least one non-hook surface.
	if len(aipfeature.Get(gen.Frozen, aipfeature.PlaneSessionOpeners)) < need {
		t.Fatalf("openers: %d", len(aipfeature.Get(gen.Frozen, aipfeature.PlaneSessionOpeners)))
	}
	if len(aipfeature.Get(gen.Frozen, aipfeature.PlaneRequestTransforms)) < need {
		t.Fatalf("request transforms: %d", len(aipfeature.Get(gen.Frozen, aipfeature.PlaneRequestTransforms)))
	}
	if len(aipfeature.Get(gen.Frozen, aipfeature.PlaneToolCatalogFilters)) < need {
		t.Fatalf("catalog: %d", len(aipfeature.Get(gen.Frozen, aipfeature.PlaneToolCatalogFilters)))
	}
	if len(aipfeature.Get(gen.Frozen, aipfeature.PlaneWorkspaceResolvers)) < need {
		t.Fatalf("workspace: %d", len(aipfeature.Get(gen.Frozen, aipfeature.PlaneWorkspaceResolvers)))
	}
	if len(aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)) < need {
		t.Fatalf("obs: %d", len(aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficObservers)))
	}
	if len(aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)) < need {
		t.Fatalf("usage observers: %d", len(aipfeature.Get(gen.Frozen, aipfeature.PlaneUsageObservers)))
	}
	if len(aipfeature.Get(gen.Frozen, aipfeature.PlaneRawCaptureSinks)) < need {
		t.Fatalf("raw: %d", len(aipfeature.Get(gen.Frozen, aipfeature.PlaneRawCaptureSinks)))
	}
	if len(aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficRedactors)) < need {
		t.Fatalf("red: %d", len(aipfeature.Get(gen.Frozen, aipfeature.PlaneTrafficRedactors)))
	}
	if len(aipfeature.Get(gen.Frozen, aipfeature.PlaneCompletionGates)) < need {
		t.Fatalf("gates: %d", len(aipfeature.Get(gen.Frozen, aipfeature.PlaneCompletionGates)))
	}
}
