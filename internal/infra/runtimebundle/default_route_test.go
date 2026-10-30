package runtimebundle_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestValidateStructural_acceptsConfigWithoutDefaultRoute proves a configuration
// whose enabled backend has no canonical default model is accepted with the
// advisory the operator needs. The configuration serves the clients that route
// explicitly, so refusing it would report a working configuration as invalid.
func TestValidateStructural_acceptsConfigWithoutDefaultRoute(t *testing.T) {
	t.Parallel()
	path := writeDefaultRouteConfig(t, "")
	report, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatalf("ValidateStructural: %v", err)
	}
	if !strings.Contains(report.DefaultRouteWarning, "no default route") {
		t.Fatalf("warning = %q", report.DefaultRouteWarning)
	}
	if !strings.Contains(report.DefaultRouteWarning, "routing.default_route") {
		t.Fatalf("warning must name the remedy: %q", report.DefaultRouteWarning)
	}
}

// TestValidateStructural_refusesPlaceholderDefaultRouteModel proves the
// placeholder model the configuration layer used to synthesize is refused. No
// backend serves a model named exactly "model", so a default route naming it
// sends every request to a model the backend refuses.
func TestValidateStructural_refusesPlaceholderDefaultRouteModel(t *testing.T) {
	t.Parallel()
	path := writeDefaultRouteConfig(t, "  default_route: \"compat-structural:model\"\n")
	_, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err == nil {
		t.Fatal("expected a refusal for the placeholder default route model")
	}
	if !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("error: %v", err)
	}
}

// TestValidateStructural_refusesAliasExpandedPlaceholderDefaultRoute proves the
// placeholder check runs on the effective, alias-resolved route. A default route
// that is valid on its own but expands into the placeholder through an alias
// must still be refused: the expanded route is the one the runtime uses.
func TestValidateStructural_refusesAliasExpandedPlaceholderDefaultRoute(t *testing.T) {
	t.Parallel()
	path := writeDefaultRouteConfig(t,
		"  default_route: \"compat-structural:model-a\"\n")
	alias := `
model_aliases:
  - pattern: "^compat-structural:model-a$"
    replacement: "compat-structural:model"
`
	path = appendConfigSection(t, path, alias)
	_, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err == nil {
		t.Fatal("expected a refusal for the alias-expanded placeholder model")
	}
	if !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("error: %v", err)
	}
}

// TestValidateStructural_acceptsAliasExpandedRealDefaultRoute proves the check
// follows the alias: a default route that names the placeholder but expands into
// a real model is accepted, because the effective route is the expanded one.
func TestValidateStructural_acceptsAliasExpandedRealDefaultRoute(t *testing.T) {
	t.Parallel()
	path := writeDefaultRouteConfig(t, "  default_route: \"compat-structural:model\"\n")
	alias := `
model_aliases:
  - pattern: "^compat-structural:model$"
    replacement: "compat-structural:model-a"
`
	path = appendConfigSection(t, path, alias)
	_, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatalf("ValidateStructural: %v", err)
	}
}

// TestRoutesSnapshotFrom_aliasChainAppliesTheFirstRuleOnly proves the
// effective default route is the configured route with the first matching alias
// rule applied. A second rule that would rewrite the route further never
// applies to the default route, so the route the operator is told about is the
// route the executor runs.
func TestRoutesSnapshotFrom_aliasChainAppliesTheFirstRuleOnly(t *testing.T) {
	t.Parallel()
	path := writeDefaultRouteConfig(t, "  default_route: \"compat-structural:model-a\"\n")
	alias := `
model_aliases:
  - pattern: "^compat-structural:model-a$"
    replacement: "compat-structural:model-b"
  - pattern: "^compat-structural:model-b$"
    replacement: "compat-structural:model"
`
	path = appendConfigSection(t, path, alias)
	if _, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	}); err != nil {
		t.Fatalf("ValidateStructural: %v", err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	snap, err := runtimebundle.RoutesSnapshotFrom(cfg, pluginreg.NewRegistry())
	if err != nil {
		t.Fatalf("RoutesSnapshotFrom: %v", err)
	}
	if snap.EffectiveDefaultRoute != "compat-structural:model-b" {
		t.Fatalf("effective default route = %q want compat-structural:model-b", snap.EffectiveDefaultRoute)
	}
}

// TestValidateStructural_refusesEmptyAliasExpandedDefaultRoute proves a
// configured default route that expands to nothing is refused by the operator
// check. An absent default route is accepted with the advisory; a configured
// route that expands to nothing is a configuration error.
func TestValidateStructural_refusesEmptyAliasExpandedDefaultRoute(t *testing.T) {
	t.Parallel()
	path := writeDefaultRouteConfig(t, "  default_route: \"compat-structural:model-a\"\n")
	alias := `
model_aliases:
  - pattern: "^compat-structural:model-a$"
    replacement: "$1"
`
	path = appendConfigSection(t, path, alias)
	_, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err == nil {
		t.Fatal("expected a refusal for the alias expansion that produces an empty route")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error: %v", err)
	}
}

// TestValidateDistribution_refusesEmptyAliasExpandedDefaultRoute proves the
// serving bootstrap refuses the same configuration, so a request never reaches
// the executor without a route.
func TestValidateDistribution_refusesEmptyAliasExpandedDefaultRoute(t *testing.T) {
	t.Parallel()
	path := writeDefaultRouteConfig(t, "  default_route: \"compat-structural:model-a\"\n")
	alias := `
model_aliases:
  - pattern: "^compat-structural:model-a$"
    replacement: "$1"
`
	path = appendConfigSection(t, path, alias)
	err := runtimebundle.ValidateDistribution(context.Background(), runtimebundle.ValidateDistributionInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected the bootstrap to refuse the alias expansion that produces an empty route")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error: %v", err)
	}
}

// TestValidateStructural_refusesModelOnlyAliasDefaultRoute proves an alias that
// resolves the default route to a model-only selector is refused. The serving
// runtime routes a request to a backend, so such a route serves no request.
func TestValidateStructural_refusesModelOnlyAliasDefaultRoute(t *testing.T) {
	t.Parallel()
	path := writeDefaultRouteConfig(t, "  default_route: \"compat-structural:model-a\"\n")
	alias := `
model_aliases:
  - pattern: "^compat-structural:model-a$"
    replacement: "gpt-4o-mini"
`
	path = appendConfigSection(t, path, alias)
	_, err := runtimebundle.ValidateStructural(context.Background(), runtimebundle.ValidateStructuralInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err == nil {
		t.Fatal("expected a refusal for the model-only default route")
	}
	if !strings.Contains(err.Error(), "model-only") {
		t.Fatalf("error: %v", err)
	}
}

// TestValidateDistribution_refusesModelOnlyAliasDefaultRoute proves the
// serving bootstrap applies the same acceptance predicate as the operator
// check: a configuration the check refuses must not start.
func TestValidateDistribution_refusesModelOnlyAliasDefaultRoute(t *testing.T) {
	t.Parallel()
	path := writeDefaultRouteConfig(t, "  default_route: \"compat-structural:model-a\"\n")
	alias := `
model_aliases:
  - pattern: "^compat-structural:model-a$"
    replacement: "gpt-4o-mini"
`
	path = appendConfigSection(t, path, alias)
	err := runtimebundle.ValidateDistribution(context.Background(), runtimebundle.ValidateDistributionInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected the bootstrap to refuse the model-only default route")
	}
	if !strings.Contains(err.Error(), "model-only") {
		t.Fatalf("error: %v", err)
	}
}

// TestValidateDistribution_refusesPlaceholderDefaultRoute proves the serving
// runtime applies the same invariant as the operator check: a default route
// that names a model no backend serves is refused at bootstrap too.
func TestValidateDistribution_refusesPlaceholderDefaultRoute(t *testing.T) {
	t.Parallel()
	path := writeDefaultRouteConfig(t, "  default_route: \"compat-structural:model\"\n")
	err := runtimebundle.ValidateDistribution(context.Background(), runtimebundle.ValidateDistributionInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected the bootstrap to refuse the placeholder default route model")
	}
	if !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("error: %v", err)
	}
}

// TestValidateDistribution_acceptsConfigWithoutDefaultRoute proves the serving
// runtime accepts a configuration without a default route: only the operator
// check reports the advisory, because a client that routes explicitly keeps
// working. The refusal must not remove a capability the runtime has.
func TestValidateDistribution_acceptsConfigWithoutDefaultRoute(t *testing.T) {
	t.Parallel()
	path := writeDefaultRouteConfig(t, "")
	err := runtimebundle.ValidateDistribution(context.Background(), runtimebundle.ValidateDistributionInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("ValidateDistribution: %v", err)
	}
}

// TestRoutesSnapshotFrom_reportsMissingDefaultRoute proves the operator read
// model names the missing default route instead of showing an empty value the
// operator has to interpret.
func TestRoutesSnapshotFrom_reportsMissingDefaultRoute(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "compat-structural", Kind: "custom-openai-legacy-compatible", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	snap, err := runtimebundle.RoutesSnapshotFrom(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveDefaultRoute != "" {
		t.Fatalf("effective default route = %q want none", snap.EffectiveDefaultRoute)
	}
	if !strings.Contains(snap.EffectiveDefaultRouteNote, "no default route") {
		t.Fatalf("note = %q", snap.EffectiveDefaultRouteNote)
	}
	if !strings.Contains(snap.EffectiveDefaultRouteNote, "routing.default_route") {
		t.Fatalf("note must name the remedy: %q", snap.EffectiveDefaultRouteNote)
	}
}

// TestRoutesSnapshotFrom_catchAllAliasDoesNotCreateDefaultRoute proves an alias
// rule that matches everything cannot turn an absent default route into a
// route: the absent route stays absent.
func TestRoutesSnapshotFrom_catchAllAliasDoesNotCreateDefaultRoute(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{
			{Pattern: `^.*$`, Replacement: "compat-structural:model-a"},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "compat-structural", Kind: "custom-openai-legacy-compatible", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	snap, err := runtimebundle.RoutesSnapshotFrom(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveDefaultRoute != "" {
		t.Fatalf("effective default route = %q want none", snap.EffectiveDefaultRoute)
	}
	if snap.EffectiveDefaultRouteNote == "" {
		t.Fatal("the missing default route must be reported")
	}
}

// TestRoutesSnapshotFrom_publishesTheDiscoveredDefaultRoute proves the
// operator read model publishes the route a connector's discovered inventory
// provides, together with the model it used and the source it came from.
func TestRoutesSnapshotFrom_publishesTheDiscoveredDefaultRoute(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "claude-acp", Kind: "claudeagentacp", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	snap, err := runtimebundle.RoutesSnapshotFromWithInventory(cfg, reg, func(instanceID string) string {
		if instanceID == "claude-acp" {
			return "anthropic/claude-sonnet-5.5"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveDefaultRoute != "claude-acp:anthropic/claude-sonnet-5.5" {
		t.Fatalf("effective default route = %q", snap.EffectiveDefaultRoute)
	}
	if snap.EffectiveDefaultRouteModel != "anthropic/claude-sonnet-5.5" {
		t.Fatalf("effective default route model = %q", snap.EffectiveDefaultRouteModel)
	}
	if snap.EffectiveDefaultRouteSource != "discovered_inventory" {
		t.Fatalf("effective default route source = %q", snap.EffectiveDefaultRouteSource)
	}
	if snap.EffectiveDefaultRouteNote != "" {
		t.Fatalf("note = %q want none", snap.EffectiveDefaultRouteNote)
	}
}

// TestRoutesSnapshotFrom_configuredRouteWinsOverTheDiscoveredModel proves the
// operator read model keeps the configured route authoritative: the discovered
// model never replaces the route the operator named.
func TestRoutesSnapshotFrom_configuredRouteWinsOverTheDiscoveredModel(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{DefaultRoute: "claude-acp:anthropic/claude-opus-4.5"},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "claude-acp", Kind: "claudeagentacp", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	snap, err := runtimebundle.RoutesSnapshotFromWithInventory(cfg, reg, func(string) string {
		return "anthropic/claude-sonnet-5.5"
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveDefaultRoute != "claude-acp:anthropic/claude-opus-4.5" {
		t.Fatalf("effective default route = %q", snap.EffectiveDefaultRoute)
	}
	if snap.EffectiveDefaultRouteModel != "anthropic/claude-opus-4.5" {
		t.Fatalf("effective default route model = %q", snap.EffectiveDefaultRouteModel)
	}
	if snap.EffectiveDefaultRouteSource != "configured" {
		t.Fatalf("effective default route source = %q", snap.EffectiveDefaultRouteSource)
	}
}

// TestRoutesSnapshotFrom_withoutInventoryKeepsTheMissingDefaultRoute proves a
// connector whose inventory cannot be discovered keeps the absent default
// route and the advisory that names the remedy.
func TestRoutesSnapshotFrom_withoutInventoryKeepsTheMissingDefaultRoute(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "claude-acp", Kind: "claudeagentacp", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	snap, err := runtimebundle.RoutesSnapshotFrom(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveDefaultRoute != "" {
		t.Fatalf("effective default route = %q want none", snap.EffectiveDefaultRoute)
	}
	if !strings.Contains(snap.EffectiveDefaultRouteNote, "no default route") {
		t.Fatalf("note = %q", snap.EffectiveDefaultRouteNote)
	}
	if snap.EffectiveDefaultRouteSource != "" {
		t.Fatalf("source = %q want none", snap.EffectiveDefaultRouteSource)
	}
}

// writeDefaultRouteConfig writes a configuration whose only enabled backend is a
// compatible-mode instance: its kind has no canonical default model, so the
// default route comes from routing.default_route. The defaultRoute argument is
// the routing block content under routing:; an empty string removes the block's
// default route line.
func writeDefaultRouteConfig(t *testing.T, defaultRoute string) string {
	t.Helper()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rows := `    - id: compat-structural
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: compat-structural
        base_url: https://example.test/v1
        api_key_env_var_root: COMPAT_STRUCTURAL_KEY
        tokenizer: o200k_base
        max_concurrent_requests: 2
        models:
          source: inline
          items:
            - canonical_id: compat-structural/model-a
              native_id: model-a
`
	text := strings.Replace(string(base), "  features:\n", rows+"  features:\n", 1)
	const configured = "  default_route: \"openai-responses:gpt-4o-mini\"\n"
	if !strings.Contains(text, configured) {
		t.Fatal("config/config.yaml must carry the documented default route")
	}
	text = strings.Replace(text, configured, defaultRoute, 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// appendConfigSection appends a top-level configuration section to a file
// written by writeDefaultRouteConfig.
func appendConfigSection(t *testing.T, path, section string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(out, append(raw, []byte(section)...), 0o600); err != nil {
		t.Fatal(err)
	}
	return out
}
