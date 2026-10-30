package runtimebundle_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refautoappend"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
)

func TestInventorySnapshotForOperator_matchesConfigSnapshotWithExtras(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Frontends: []config.PluginConfig{{ID: "openai-responses", Enabled: true}},
			Features: []config.PluginConfig{{
				ID:      refautoappend.ID,
				Enabled: true,
			}},
		},
	}
	extras := config.RegistrationsFromConfig(cfg)
	ctx := context.Background()

	got, err := runtimebundle.InventorySnapshotForOperator(ctx, cfg, reg, extras)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := featurebundle.MergeFeatureSurfacesWithHost(reg, extras, featurebundle.HostContributions{})
	if err != nil {
		t.Fatal(err)
	}
	var featureMetadata []diag.InventoryFeatureMetadata
	for _, feature := range merged.FeatureDiagnosticProjections() {
		featureMetadata = append(featureMetadata, featurehost.InventoryFeatureMetadataFromProjections(feature.InstanceID, feature.Projections))
	}
	want, err := diag.InventorySnapshotForConfig(ctx, cfg, &diag.InventoryExtras{
		Reg:                          reg,
		Registrations:                extras,
		FeatureMetadata:              featureMetadata,
		InstanceDiagnosticProjectors: standardplugins.StandardDiagnosticProjectors(),
	})
	if err != nil {
		t.Fatal(err)
	}

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("snapshot mismatch\ngot  %s\nwant %s", gotJSON, wantJSON)
	}
}
