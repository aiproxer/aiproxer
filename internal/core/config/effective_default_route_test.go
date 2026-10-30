package config_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestEffectiveDefaultRouteSelector_explicitYAML(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			DefaultRoute: "stub:custom-model",
		},
	}
	got := config.EffectiveDefaultRouteSelector(cfg, func(string) string { return "ignored" })
	if got != "stub:custom-model" {
		t.Fatalf("got %q want stub:custom-model", got)
	}
}

func TestEffectiveDefaultRouteSelector_firstEnabledBackend(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "anthropic", Enabled: false},
				{ID: "stub", Enabled: true},
			},
		},
	}
	got := config.EffectiveDefaultRouteSelector(cfg, func(id string) string {
		if id == "stub" {
			return "m1"
		}
		return "x"
	})
	if got != "stub:m1" {
		t.Fatalf("got %q want stub:m1", got)
	}
}

func TestEffectiveDefaultRouteSelector_fallbackUsesWireModel(t *testing.T) {
	t.Parallel()
	got := config.EffectiveDefaultRouteSelector(nil, defaultWireModelForTest)
	if got != "openai-responses:gpt-test" {
		t.Fatalf("got %q", got)
	}
}

// TestEffectiveDefaultRouteSelector_noCanonicalDefaultModel proves a backend
// kind without a canonical default model never receives a synthesized route.
// The kind discovers its models at runtime, so the configuration names the
// route instead of the proxy inventing a placeholder model.
func TestEffectiveDefaultRouteSelector_noCanonicalDefaultModel(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "claude-acp", Kind: "claudeagentacp", Enabled: true},
			},
		},
	}
	got := config.EffectiveDefaultRouteSelector(cfg, func(id string) string {
		if id == "claudeagentacp" {
			return ""
		}
		return "m"
	})
	if got != "" {
		t.Fatalf("got %q want no default route", got)
	}
}

// TestEffectiveDefaultRouteSelector_firstEnabledBackendWithoutCanonicalModel
// proves the default route never falls through to a later backend. The first
// enabled backend decides: when its kind has no canonical default model there is
// no default route, because routing to another backend would send the request
// to a provider the operator did not put first.
func TestEffectiveDefaultRouteSelector_firstEnabledBackendWithoutCanonicalModel(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "claude-acp", Kind: "claudeagentacp", Enabled: true},
				{ID: "openai-responses", Kind: "openai-responses", Enabled: true},
			},
		},
	}
	got := config.EffectiveDefaultRouteSelector(cfg, func(id string) string {
		if id == "claudeagentacp" {
			return ""
		}
		return "gpt-test"
	})
	if got != "" {
		t.Fatalf("got %q want no default route", got)
	}
}

// TestEffectiveDefaultRouteSelector_laterBackendWithCanonicalModel proves the
// first enabled backend still provides the default route when its kind has one.
func TestEffectiveDefaultRouteSelector_laterBackendWithCanonicalModel(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "openai-responses", Kind: "openai-responses", Enabled: true},
				{ID: "claude-acp", Kind: "claudeagentacp", Enabled: true},
			},
		},
	}
	got := config.EffectiveDefaultRouteSelector(cfg, func(id string) string {
		if id == "claudeagentacp" {
			return ""
		}
		return "gpt-test"
	})
	if got != "openai-responses:gpt-test" {
		t.Fatalf("got %q want openai-responses:gpt-test", got)
	}
}

func defaultWireModelForTest(backendID string) string {
	if backendID == "openai-responses" {
		return "gpt-test"
	}
	return "m"
}

// connectorConfig is a configuration whose only enabled backend is a connector:
// its kind has no canonical default model, so the model it discovers at
// runtime is the only default route it can have.
func connectorConfig() *config.Config {
	return &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "claude-acp", Kind: "claudeagentacp", Enabled: true},
			},
		},
	}
}

// noCanonicalWireModel is the wire model of a kind without a canonical default
// model, such as an executable connector.
func noCanonicalWireModel(string) string { return "" }

// TestResolveEffectiveDefaultRoute_discoveredModelIsTheDefaultRoute proves a
// connector takes the model it published in its runtime inventory as the
// default route. The operator does not have to copy a model id into the
// configuration for a deployment that discovers its models.
func TestResolveEffectiveDefaultRoute_discoveredModelIsTheDefaultRoute(t *testing.T) {
	t.Parallel()
	got := config.ResolveEffectiveDefaultRoute(
		connectorConfig(),
		noCanonicalWireModel,
		func(instanceID string) string {
			if instanceID == "claude-acp" {
				return "anthropic/claude-sonnet-5.5"
			}
			return ""
		},
	)
	if got.Selector != "claude-acp:anthropic/claude-sonnet-5.5" {
		t.Fatalf("selector = %q want claude-acp:anthropic/claude-sonnet-5.5", got.Selector)
	}
	if got.Source != config.DefaultRouteSourceDiscoveredInventory {
		t.Fatalf("source = %q want %q", got.Source, config.DefaultRouteSourceDiscoveredInventory)
	}
}

// TestResolveEffectiveDefaultRoute_configuredRouteWinsOverTheDiscoveredModel
// proves the configured route stays authoritative. The operator named it, so a
// discovered model never replaces it.
func TestResolveEffectiveDefaultRoute_configuredRouteWinsOverTheDiscoveredModel(t *testing.T) {
	t.Parallel()
	cfg := connectorConfig()
	cfg.Routing.DefaultRoute = "claude-acp:anthropic/claude-opus-4.5"
	got := config.ResolveEffectiveDefaultRoute(
		cfg,
		noCanonicalWireModel,
		func(string) string { return "anthropic/claude-sonnet-5.5" },
	)
	if got.Selector != "claude-acp:anthropic/claude-opus-4.5" {
		t.Fatalf("selector = %q want the configured route", got.Selector)
	}
	if got.Source != config.DefaultRouteSourceConfigured {
		t.Fatalf("source = %q want %q", got.Source, config.DefaultRouteSourceConfigured)
	}
}

// TestResolveEffectiveDefaultRoute_canonicalModelWinsOverTheDiscoveredModel
// proves the compile-time default of a backend kind stays the default route.
// The discovered model is the fallback for kinds without a canonical default.
func TestResolveEffectiveDefaultRoute_canonicalModelWinsOverTheDiscoveredModel(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "openai-responses", Kind: "openai-responses", Enabled: true},
			},
		},
	}
	got := config.ResolveEffectiveDefaultRoute(
		cfg,
		defaultWireModelForTest,
		func(string) string { return "discovered-model" },
	)
	if got.Selector != "openai-responses:gpt-test" {
		t.Fatalf("selector = %q want openai-responses:gpt-test", got.Selector)
	}
	if got.Source != config.DefaultRouteSourceCanonicalModel {
		t.Fatalf("source = %q want %q", got.Source, config.DefaultRouteSourceCanonicalModel)
	}
}

// TestResolveEffectiveDefaultRoute_noDiscoveredModelKeepsNoDefaultRoute proves
// a connector whose inventory cannot be discovered still has no default route.
// The route stays absent instead of naming a model no backend serves.
func TestResolveEffectiveDefaultRoute_noDiscoveredModelKeepsNoDefaultRoute(t *testing.T) {
	t.Parallel()
	got := config.ResolveEffectiveDefaultRoute(
		connectorConfig(),
		noCanonicalWireModel,
		func(string) string { return "" },
	)
	if got.Selector != "" {
		t.Fatalf("selector = %q want no default route", got.Selector)
	}
	if got.Source != "" {
		t.Fatalf("source = %q want none", got.Source)
	}
}

// TestResolveEffectiveDefaultRoute_discoveredModelNeverFallsThroughToALaterBackend
// proves the first enabled backend still decides. A connector that published
// no model leaves the configuration without a default route, because routing
// to a later backend would send the request to a provider the operator did not
// put first.
func TestResolveEffectiveDefaultRoute_discoveredModelNeverFallsThroughToALaterBackend(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "claude-acp", Kind: "claudeagentacp", Enabled: true},
				{ID: "openai-responses", Kind: "openai-responses", Enabled: true},
			},
		},
	}
	got := config.ResolveEffectiveDefaultRoute(
		cfg,
		func(id string) string {
			if id == "claudeagentacp" {
				return ""
			}
			return "gpt-test"
		},
		func(instanceID string) string {
			if instanceID == "openai-responses" {
				return "discovered-model"
			}
			return ""
		},
	)
	if got.Selector != "" {
		t.Fatalf("selector = %q want no default route", got.Selector)
	}
}

// TestDefaultRouteGuidance_namesTheDiscoveredRemedy proves the advisory names
// the model the instance discovers when no inventory was consulted, and the
// explicit route when the inventory holds no model for it.
func TestDefaultRouteGuidance_namesTheDiscoveredRemedy(t *testing.T) {
	t.Parallel()
	withoutInventory := config.DefaultRouteGuidance(connectorConfig(), noCanonicalWireModel, nil)
	if !strings.Contains(withoutInventory, "no default route") {
		t.Fatalf("guidance = %q", withoutInventory)
	}
	if !strings.Contains(withoutInventory, "designates at runtime") {
		t.Fatalf("guidance must name the discovered model: %q", withoutInventory)
	}
	if !strings.Contains(withoutInventory, "routing.default_route") {
		t.Fatalf("guidance must name the remedy: %q", withoutInventory)
	}
	withInventory := config.DefaultRouteGuidance(connectorConfig(), noCanonicalWireModel, func(string) string { return "" })
	if !strings.Contains(withInventory, "designated no default model") {
		t.Fatalf("guidance must name the empty inventory: %q", withInventory)
	}
	if !strings.Contains(withInventory, "routing.default_route") {
		t.Fatalf("guidance must name the remedy: %q", withInventory)
	}
}
