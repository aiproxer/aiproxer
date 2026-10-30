package standardplugins_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestToolCallRepair_NotMandatoryRequirement(t *testing.T) {
	t.Parallel()
	for _, r := range aipsdk.StandardDistributionRequirements() {
		if r.Kind == aipsdk.PluginKindFeature && r.ID == standardplugins.ToolCallRepairFeatureID {
			t.Fatal("tool-call-repair must not be a mandatory StandardDistributionRequirements entry; explicit disabled is the opt-out")
		}
	}
}

func TestEnsureToolCallRepairInConfig_DisabledSuppressesInjection(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Plugins.Features = []config.PluginConfig{{
		ID:      standardplugins.ToolCallRepairFeatureID,
		Enabled: false,
	}}
	if err := standardplugins.EnsureToolCallRepairInConfig(cfg, standardplugins.ToolCallRepairInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 || cfg.Plugins.Features[0].Enabled {
		t.Fatalf("disabled row must be preserved without re-enable: %+v", cfg.Plugins.Features)
	}
}

func TestEnsureToolCallRepairInConfig_AbsentInjectsEnabled(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	if err := standardplugins.EnsureToolCallRepairInConfig(cfg, standardplugins.ToolCallRepairInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 || !cfg.Plugins.Features[0].Enabled || cfg.Plugins.Features[0].ID != standardplugins.ToolCallRepairFeatureID {
		t.Fatalf("got %+v", cfg.Plugins.Features)
	}
}

func TestEnsureToolCallRepairInConfig_OmittedEnabledIsOptOut(t *testing.T) {
	t.Parallel()
	// Plain-bool PluginConfig.Enabled defaults to false when the key is omitted.
	// Presence of a matching row (even without enabled: true) is an explicit opt-out
	// and must suppress standard injection.
	cfg := &config.Config{}
	cfg.Plugins.Features = []config.PluginConfig{{
		ID: standardplugins.ToolCallRepairFeatureID,
	}}
	if cfg.Plugins.Features[0].Enabled {
		t.Fatal("precondition: omitted enabled must decode/zero as false")
	}
	if err := standardplugins.EnsureToolCallRepairInConfig(cfg, standardplugins.ToolCallRepairInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 {
		t.Fatalf("must not inject another row: %+v", cfg.Plugins.Features)
	}
	if cfg.Plugins.Features[0].Enabled {
		t.Fatal("omitted enabled must remain disabled opt-out")
	}
}

func TestEnsureToolCallRepairInConfig_FactoryKindMatchSuppressesInjection(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Plugins.Features = []config.PluginConfig{{
		Kind:    standardplugins.ToolCallRepairFeatureID,
		ID:      "custom-repair-instance",
		Enabled: false,
	}}
	if err := standardplugins.EnsureToolCallRepairInConfig(cfg, standardplugins.ToolCallRepairInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 1 {
		t.Fatalf("factory-kind match must suppress injection: %+v", cfg.Plugins.Features)
	}
}

func TestEnsureToolCallRepairInConfig_CustomBundleNoInjection(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	if err := standardplugins.EnsureToolCallRepairInConfig(cfg, standardplugins.ToolCallRepairInjectOpts{StandardDistribution: false}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Features) != 0 {
		t.Fatalf("custom/minimal bundles must not inject: %+v", cfg.Plugins.Features)
	}
}
