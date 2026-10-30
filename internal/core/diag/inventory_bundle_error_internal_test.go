package diag

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestBuildInventoryExtensions_PreservesCompiledBundleError(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Plugins: config.PluginsConfig{
		Features: []config.PluginConfig{{ID: "feature-with-error", Enabled: true}},
	}}
	ext := buildInventoryExtensions(t.Context(), cfg, &InventoryExtras{
		FeatureMetadata: []InventoryFeatureMetadata{{
			InstanceID:     "feature-with-error",
			StageOccupancy: []InventoryStageOccupancy{},
			BundleError:    "compiled feature projection failed",
		}},
	})
	if len(ext.Features) != 1 {
		t.Fatalf("features: %d", len(ext.Features))
	}
	feature := ext.Features[0]
	if feature.BundleError != "compiled feature projection failed" {
		t.Fatalf("bundle_error=%q", feature.BundleError)
	}
	if len(feature.StageOccupancy) != 0 {
		t.Fatalf("stage occupancy=%#v", feature.StageOccupancy)
	}
}
