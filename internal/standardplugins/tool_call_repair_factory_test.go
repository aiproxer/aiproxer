package standardplugins_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	repair "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"gopkg.in/yaml.v3"
)

func TestToolCallRepairFactory_RegistersFinalizer(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	bundle, err := reg.BuildFeatureBundle(standardplugins.ToolCallRepairFeatureID, n)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	finalizers := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("want 1 finalizer, got %d", len(finalizers))
	}
	if finalizers[0].ID() != standardplugins.ToolCallRepairFeatureID {
		t.Fatalf("id=%q", finalizers[0].ID())
	}
	maxArgs := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if maxArgs <= 0 {
		t.Fatalf("factory must contribute max_args_bytes, got %d", maxArgs)
	}
	if err := reg.ValidateBundledFactories([]aipsdk.Requirement{
		{Kind: aipsdk.PluginKindFeature, ID: standardplugins.ToolCallRepairFeatureID},
	}); err != nil {
		t.Fatalf("factory missing from registry: %v", err)
	}
}

// TestToolCallRepairFactory_DefaultMaxArgsBytesMatchCore locks composition
// wiring: empty YAML yields core/feature DefaultMaxArgsBytes on the bundle.
func TestToolCallRepairFactory_DefaultMaxArgsBytesMatchCore(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	bundle, err := reg.BuildFeatureBundle(standardplugins.ToolCallRepairFeatureID, n)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}
	maxArgs := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if maxArgs != repair.DefaultMaxArgsBytes {
		t.Fatalf("bundle max_args=%d != repair DefaultMaxArgsBytes=%d",
			maxArgs, repair.DefaultMaxArgsBytes)
	}
	if maxArgs != toolcallrepair.DefaultMaxArgsBytes {
		t.Fatalf("bundle max_args=%d != feature DefaultMaxArgsBytes=%d",
			maxArgs, toolcallrepair.DefaultMaxArgsBytes)
	}
}

// TestToolCallRepairFactory_CustomYAMLConfig_BehavioralIntegration verifies that
// standard factory constructs a Finalizer whose plane contributions and behavioral
// Finalize execution strictly reflect the custom YAML configuration (ID, order,
// max-args, schema limits, on-unrepairable rejection).
func TestToolCallRepairFactory_CustomYAMLConfig_BehavioralIntegration(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	rawYAML := `
order: 75
max_args_bytes: 8192
on_unrepairable: "error"
schema:
  max_schema_bytes: 500
  max_nesting_depth: 10
  max_nodes: 50
  max_properties: 20
  max_local_ref_depth: 5
  max_cache_entries: 16
  max_cache_bytes: 65536
`
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(rawYAML), &n); err != nil {
		t.Fatal(err)
	}

	bundle, err := reg.BuildFeatureBundle(standardplugins.ToolCallRepairFeatureID, n)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}

	// 1. Verify plane contributions
	finalizers := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("want 1 finalizer, got %d", len(finalizers))
	}
	fin := finalizers[0]
	if fin.ID() != standardplugins.ToolCallRepairFeatureID {
		t.Fatalf("fin.ID() = %q, want %q", fin.ID(), standardplugins.ToolCallRepairFeatureID)
	}
	if fin.Order() != 75 {
		t.Fatalf("fin.Order() = %d, want 75", fin.Order())
	}

	maxArgsPlane := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
	if maxArgsPlane != 8192 {
		t.Fatalf("maxArgsPlane = %d, want 8192", maxArgsPlane)
	}

	ctx := context.Background()
	validSchema := json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`)
	weatherTool := aipapi.ToolDef{Name: "get_weather", Parameters: validSchema}
	weatherCatalog := []aipapi.ToolDef{weatherTool}

	// 2. Behavioral verification: Valid pass-through
	t.Run("valid_pass_through", func(t *testing.T) {
		t.Parallel()
		res, err := fin.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: "c1",
			ToolName:   "get_weather",
			ArgsJSON:   []byte(`{"location":"NYC"}`),
		}, weatherTool, weatherCatalog, toolcall.Meta{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != toolcall.ActionPass || res.ReasonCode != toolcall.ReasonValidPassThrough {
			t.Fatalf("got action=%v reason=%q, want pass / valid_pass_through", res.Action, res.ReasonCode)
		}
		if res.ArgsJSON != nil {
			t.Fatalf("expected nil ArgsJSON on pass, got %s", res.ArgsJSON)
		}
	})

	// 3. Behavioral verification: Truncated syntax repair rewrite
	t.Run("syntax_repair_rewrite", func(t *testing.T) {
		t.Parallel()
		res, err := fin.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: "c2",
			ToolName:   "get_weather",
			ArgsJSON:   []byte(`{"location":"NYC"`),
		}, weatherTool, weatherCatalog, toolcall.Meta{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != toolcall.ActionRewrite || res.ReasonCode != toolcall.ReasonSyntaxRepaired {
			t.Fatalf("got action=%v reason=%q, want rewrite / syntax_repaired", res.Action, res.ReasonCode)
		}
		if want := `{"location":"NYC"}`; string(res.ArgsJSON) != want {
			t.Fatalf("got rewritten args %s, want %s", res.ArgsJSON, want)
		}
	})

	// 4. Behavioral verification: on_unrepairable: "error" causes ActionReject on unrepairable input
	t.Run("on_unrepairable_error_causes_reject", func(t *testing.T) {
		t.Parallel()
		res, err := fin.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: "c3",
			ToolName:   "get_weather",
			ArgsJSON:   []byte(`{}`), // missing required property "location"
		}, weatherTool, weatherCatalog, toolcall.Meta{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != toolcall.ActionReject || res.ReasonCode != toolcall.ReasonUnrepairable {
			t.Fatalf("got action=%v reason=%q, want reject / unrepairable", res.Action, res.ReasonCode)
		}
	})

	// 5. Behavioral verification: schema limit max_schema_bytes: 500 enforced
	t.Run("schema_limit_max_schema_bytes_enforced", func(t *testing.T) {
		t.Parallel()
		// Create a schema > 500 bytes (e.g. 600 bytes with long descriptions)
		pad := strings.Repeat("a", 550)
		oversizedSchema := json.RawMessage(fmt.Sprintf(`{"type":"object","description":%q,"properties":{"location":{"type":"string"}},"required":["location"]}`, pad))
		largeTool := aipapi.ToolDef{Name: "get_weather", Parameters: oversizedSchema}

		res, err := fin.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: "c4",
			ToolName:   "get_weather",
			ArgsJSON:   []byte(`{"location":"NYC"}`),
		}, largeTool, []aipapi.ToolDef{largeTool}, toolcall.Meta{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != toolcall.ActionReject || res.ReasonCode != toolcall.ReasonSchemaInvalid {
			t.Fatalf("got action=%v reason=%q, want reject / schema_invalid", res.Action, res.ReasonCode)
		}
	})

	// 6. Behavioral verification: schema limit max_nesting_depth: 10 enforced
	t.Run("schema_limit_max_nesting_depth_enforced", func(t *testing.T) {
		t.Parallel()
		// Create a schema nested 12 levels (> 10 max_nesting_depth)
		var b strings.Builder
		for range 12 {
			b.WriteString(`{"type":"object","properties":{"level":{"type":"object","properties":`)
		}
		b.WriteString(`{"val":{"type":"string"}}`)
		for range 12 {
			b.WriteString(`}}}`)
		}
		nestedSchema := json.RawMessage(b.String())
		nestedTool := aipapi.ToolDef{Name: "nested", Parameters: nestedSchema}

		res, err := fin.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: "c5",
			ToolName:   "nested",
			ArgsJSON:   []byte(`{}`),
		}, nestedTool, []aipapi.ToolDef{nestedTool}, toolcall.Meta{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != toolcall.ActionReject || res.ReasonCode != toolcall.ReasonSchemaInvalid {
			t.Fatalf("got action=%v reason=%q, want reject / schema_invalid", res.Action, res.ReasonCode)
		}
	})

	// 7. Behavioral verification: max_args_bytes: 8192 enforced
	t.Run("max_args_bytes_enforced", func(t *testing.T) {
		t.Parallel()
		// Args payload exceeding 8192 bytes
		hugeVal := strings.Repeat("x", 9000)
		hugeArgs := fmt.Appendf(nil, `{"location":%q}`, hugeVal)

		res, err := fin.Finalize(ctx, toolcall.CompletedCall{
			ToolCallID: "c6",
			ToolName:   "get_weather",
			ArgsJSON:   hugeArgs,
		}, weatherTool, weatherCatalog, toolcall.Meta{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Action != toolcall.ActionReject || res.ReasonCode != toolcall.ReasonArgsTooLarge {
			t.Fatalf("got action=%v reason=%q, want reject / args_too_large", res.Action, res.ReasonCode)
		}
	})
}

// TestToolCallRepairFactory_OnUnrepairablePassThrough_BehavioralIntegration verifies that
// when on_unrepairable is set to pass_through (or omitted), unrepairable inputs result in ActionPass.
func TestToolCallRepairFactory_OnUnrepairablePassThrough_BehavioralIntegration(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	rawYAML := `on_unrepairable: "pass_through"`
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(rawYAML), &n); err != nil {
		t.Fatal(err)
	}

	bundle, err := reg.BuildFeatureBundle(standardplugins.ToolCallRepairFeatureID, n)
	if err != nil {
		t.Fatalf("BuildFeatureBundle: %v", err)
	}

	finalizers := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(finalizers) != 1 {
		t.Fatalf("want 1 finalizer, got %d", len(finalizers))
	}
	fin := finalizers[0]

	ctx := context.Background()
	validSchema := json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"],"additionalProperties":false}`)
	weatherTool := aipapi.ToolDef{Name: "get_weather", Parameters: validSchema}
	weatherCatalog := []aipapi.ToolDef{weatherTool}

	res, err := fin.Finalize(ctx, toolcall.CompletedCall{
		ToolCallID: "c1",
		ToolName:   "get_weather",
		ArgsJSON:   []byte(`{}`), // unrepairable missing required field
	}, weatherTool, weatherCatalog, toolcall.Meta{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != toolcall.ActionPass || res.ReasonCode != toolcall.ReasonUnrepairable {
		t.Fatalf("got action=%v reason=%q, want pass / unrepairable", res.Action, res.ReasonCode)
	}
}

// TestToolCallRepair_DisabledAndAbsent_RealisticComposition verifies through realistic
// plugin registry and feature surface composition that disabled and absent feature registrations
// contribute no repair finalizers and zero max-args scalar bytes to the frozen plane set.
func TestToolCallRepair_DisabledAndAbsent_RealisticComposition(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	t.Run("absent_feature_contributes_no_planes", func(t *testing.T) {
		t.Parallel()
		registrations := []aipsdk.Registration{}
		surface, err := featurebundle.MergeFeatureSurfacesWithHost(reg, registrations, featurebundle.HostContributions{})
		if err != nil {
			t.Fatalf("MergeFeatureSurfacesWithHost: %v", err)
		}

		finalizers := aipfeature.Get(surface.Frozen, aipfeature.PlaneToolCallFinalizers)
		if len(finalizers) != 0 {
			t.Fatalf("want 0 finalizers when absent, got %d", len(finalizers))
		}
		maxArgs := aipfeature.Get(surface.Frozen, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
		if maxArgs != 0 {
			t.Fatalf("want 0 max_args_bytes when absent, got %d", maxArgs)
		}
	})

	t.Run("disabled_feature_contributes_no_planes", func(t *testing.T) {
		t.Parallel()
		registrations := []aipsdk.Registration{
			{
				Kind:    aipsdk.PluginKindFeature,
				ID:      standardplugins.ToolCallRepairFeatureID,
				Enabled: false,
			},
		}
		surface, err := featurebundle.MergeFeatureSurfacesWithHost(reg, registrations, featurebundle.HostContributions{})
		if err != nil {
			t.Fatalf("MergeFeatureSurfacesWithHost: %v", err)
		}

		finalizers := aipfeature.Get(surface.Frozen, aipfeature.PlaneToolCallFinalizers)
		if len(finalizers) != 0 {
			t.Fatalf("want 0 finalizers when disabled, got %d", len(finalizers))
		}
		maxArgs := aipfeature.Get(surface.Frozen, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
		if maxArgs != 0 {
			t.Fatalf("want 0 max_args_bytes when disabled, got %d", maxArgs)
		}
	})

	t.Run("enabled_feature_contributes_both_planes", func(t *testing.T) {
		t.Parallel()
		var n yaml.Node
		if err := yaml.Unmarshal([]byte("order: 55\nmax_args_bytes: 4096\n"), &n); err != nil {
			t.Fatal(err)
		}
		registrations := []aipsdk.Registration{
			{
				Kind:    aipsdk.PluginKindFeature,
				ID:      standardplugins.ToolCallRepairFeatureID,
				Enabled: true,
				Config:  aipsdk.ConfigPayload{Node: n},
			},
		}
		surface, err := featurebundle.MergeFeatureSurfacesWithHost(reg, registrations, featurebundle.HostContributions{})
		if err != nil {
			t.Fatalf("MergeFeatureSurfacesWithHost: %v", err)
		}

		finalizers := aipfeature.Get(surface.Frozen, aipfeature.PlaneToolCallFinalizers)
		if len(finalizers) != 1 {
			t.Fatalf("want 1 finalizer when enabled, got %d", len(finalizers))
		}
		if finalizers[0].Order() != 55 {
			t.Fatalf("fin.Order() = %d, want 55", finalizers[0].Order())
		}
		maxArgs := aipfeature.Get(surface.Frozen, aipfeature.PlaneToolCallFinalizationMaxArgsBytes)
		if maxArgs != 4096 {
			t.Fatalf("want max_args_bytes=4096, got %d", maxArgs)
		}
	})
}
