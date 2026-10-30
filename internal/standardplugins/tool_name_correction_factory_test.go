package standardplugins_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolnamecorrection"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"gopkg.in/yaml.v3"
)

// TestToolNameCorrectionFactory_ConfiguredRowBuildsFinalizer proves the standard
// distribution registration: one enabled configuration row builds exactly one
// finalizer at order 45 from its YAML subtree.
func TestToolNameCorrectionFactory_ConfiguredRowBuildsFinalizer(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("max_corrections_per_turn: 2\n"), &n); err != nil {
		t.Fatal(err)
	}
	surface, err := featurebundle.MergeFeatureSurfacesWithHost(reg, []aipsdk.Registration{{
		Kind:    aipsdk.PluginKindFeature,
		ID:      toolnamecorrection.ID,
		Enabled: true,
		Config:  aipsdk.ConfigPayload{Node: n},
	}}, featurebundle.HostContributions{})
	if err != nil {
		t.Fatalf("MergeFeatureSurfacesWithHost: %v", err)
	}
	finalizers := aipfeature.Get(surface.Frozen, aipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("want 1 finalizer, got %d", len(finalizers))
	}
	fin := finalizers[0]
	if fin.ID() != toolnamecorrection.ID {
		t.Fatalf("ID() = %q, want %q", fin.ID(), toolnamecorrection.ID)
	}
	const wantOrder = 45
	if fin.Order() != wantOrder {
		t.Fatalf("Order() = %d, want %d", fin.Order(), wantOrder)
	}
	res, err := fin.Finalize(context.Background(), toolcall.CompletedCall{
		ToolCallID: "call-1",
		ToolName:   "invented_tool",
		ArgsJSON:   []byte(`{}`),
	}, aipapi.ToolDef{}, []aipapi.ToolDef{{Name: "get_weather"}}, toolcall.Meta{})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if res.Action != toolcall.ActionSteer || res.Steer == nil || res.Steer.TurnBudget != 2 {
		t.Fatalf("configured row not reflected in the finalizer: %+v", res)
	}
}

// TestToolNameCorrectionFactory_AbsentOrDisabledRowContributesNothing proves the
// feature is opt-in: the standard bundle registers the factory, but without a
// row (or with a disabled row) no finalizer is installed.
func TestToolNameCorrectionFactory_AbsentOrDisabledRowContributesNothing(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for name, registrations := range map[string][]aipsdk.Registration{
		"absent_row": nil,
		"disabled_row": {{
			Kind:    aipsdk.PluginKindFeature,
			ID:      toolnamecorrection.ID,
			Enabled: false,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			surface, err := featurebundle.MergeFeatureSurfacesWithHost(reg, registrations, featurebundle.HostContributions{})
			if err != nil {
				t.Fatalf("MergeFeatureSurfacesWithHost: %v", err)
			}
			for _, fin := range aipfeature.Get(surface.Frozen, aipfeature.PlaneToolCallFinalizers) {
				if fin.ID() == toolnamecorrection.ID {
					t.Fatalf("%s installed finalizer %q", name, fin.ID())
				}
			}
		})
	}
}

// TestToolNameCorrectionFactory_NotInjectedByStandardDefaults pins the opt-in
// contract at the composition root: the standard bootstrap's default feature
// rows stay tool-call-repair and reasoning-output-preservation, and the
// mandatory requirements never demand the correction feature.
func TestToolNameCorrectionFactory_NotInjectedByStandardDefaults(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	if err := standardplugins.EnsureToolCallRepairInConfig(&cfg, standardplugins.ToolCallRepairInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(&cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	for _, row := range cfg.Plugins.Features {
		if row.FactoryID() == toolnamecorrection.ID || row.InstanceID() == toolnamecorrection.ID {
			t.Fatalf("standard bootstrap injected %q", toolnamecorrection.ID)
		}
	}
	for _, req := range aipsdk.StandardDistributionRequirements() {
		if req.Kind == aipsdk.PluginKindFeature && req.ID == toolnamecorrection.ID {
			t.Fatal("correction feature must stay opt-in, not mandatory")
		}
	}
}
