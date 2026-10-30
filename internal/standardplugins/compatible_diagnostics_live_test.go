package standardplugins_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestProjectCompatibleBackendRowsLive_reportsRegistryHealthAndSamples(t *testing.T) {
	t.Parallel()
	cfg := compatibleLiveConfig(t, `backend_prefix: live-a
base_url: https://example.test/v1
models:
  source: inline
  items:
    - canonical_id: live-a/model-a
      native_id: model-a
`)
	inv := modelregistry.BackendInventory{
		BackendID:       "live-a",
		Kind:            standardplugins.CustomOpenAILegacyCompatibleID,
		BackendPrefixes: []string{"live-a"},
		Provider: modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: []modelinventory.Model{{
				CanonicalID: "live-a/model-a",
				NativeID:    "model-a",
			}},
		},
	}
	built, err := modelregistry.Build(context.Background(), []modelregistry.BackendInventory{inv}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{inv},
		Now:         func() time.Time { return time.Unix(200, 0).UTC() },
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := standardplugins.ProjectCompatibleBackendRowsLive(cfg, standardplugins.CompatibleLiveInputs{
		Registry: built.Registry,
		Runtime:  rt,
	})
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].InventoryHealth == nil {
		t.Fatal("expected live inventory health")
	}
	if rows[0].InventoryHealth.ModelCount != 1 {
		t.Fatalf("model_count=%d", rows[0].InventoryHealth.ModelCount)
	}
	if rows[0].InventoryHealth.Source != string(modelinventory.SourceStaticInline) {
		t.Fatalf("source=%q", rows[0].InventoryHealth.Source)
	}
	if len(rows[0].InventoryHealth.SampleModels) != 1 {
		t.Fatalf("samples=%+v", rows[0].InventoryHealth.SampleModels)
	}
	sample := rows[0].InventoryHealth.SampleModels[0]
	if sample.Prefix != "live-a" || sample.CapabilitySource != modelregistry.CapabilitySourceStaticConfig {
		t.Fatalf("sample=%+v", sample)
	}
}

func compatibleLiveConfig(t *testing.T, raw string) *config.Config {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	return &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				ID:      "live-a",
				Kind:    standardplugins.CustomOpenAILegacyCompatibleID,
				Enabled: true,
				Config:  node,
			}},
		},
	}
}
