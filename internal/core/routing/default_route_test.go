package routing_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

func aliasConfig(defaultRoute string, rules ...[2]string) *config.Config {
	cfg := &config.Config{
		Routing: config.RoutingConfig{DefaultRoute: defaultRoute},
	}
	for _, r := range rules {
		cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
			Pattern:     r[0],
			Replacement: r[1],
		})
	}
	return cfg
}

// TestResolveDefaultRoute_expandsTheConfiguredRouteOnce proves the effective
// default route is the configured route with the model aliases applied once.
//
// The executor expands every selector it receives, including the default route
// the frontends fall back to. The effective default route must therefore be the
// value that expansion produces from the configured route, so a second alias
// rule can never rewrite a route that was already validated.
func TestResolveDefaultRoute_expandsTheConfiguredRouteOnce(t *testing.T) {
	t.Parallel()
	cfg := aliasConfig("wire-backend:good-a",
		[2]string{"^wire-backend:good-a$", "wire-backend:good-b"},
		[2]string{"^wire-backend:good-b$", "alt-backend:model-x"},
	)
	res, err := routing.ResolveDefaultRoute(cfg, nil)
	if err != nil {
		t.Fatalf("ResolveDefaultRoute: %v", err)
	}
	if res.RawSelector != "wire-backend:good-a" {
		t.Fatalf("raw selector = %q want wire-backend:good-a", res.RawSelector)
	}
	if res.Selector != "wire-backend:good-b" {
		t.Fatalf("selector = %q want wire-backend:good-b", res.Selector)
	}
	if res.DefaultBackend != "wire-backend" {
		t.Fatalf("default backend = %q want wire-backend", res.DefaultBackend)
	}
	// The executor expands the raw selector it receives: the effective route
	// must be exactly that value, or the executed route differs from the
	// validated one.
	if got := res.Aliases.Resolve(res.RawSelector); got != res.Selector {
		t.Fatalf("executor would expand the raw route to %q, want %q", got, res.Selector)
	}
}

// TestResolveDefaultRoute_aliasChainToPlaceholderIsNotApplied proves a second
// alias rule cannot rewrite the default route the executor runs. The alias
// table applies its first matching rule, so the rule that names the
// placeholder never fires for the default route.
func TestResolveDefaultRoute_aliasChainToPlaceholderIsNotApplied(t *testing.T) {
	t.Parallel()
	cfg := aliasConfig("wire-backend:good-a",
		[2]string{"^wire-backend:good-a$", "wire-backend:good-b"},
		[2]string{"^wire-backend:good-b$", "wire-backend:model"},
	)
	res, err := routing.ResolveDefaultRoute(cfg, nil)
	if err != nil {
		t.Fatalf("ResolveDefaultRoute: %v", err)
	}
	if res.Selector != "wire-backend:good-b" {
		t.Fatalf("selector = %q want wire-backend:good-b", res.Selector)
	}
	// The executor expands the raw route, not the effective one: the
	// placeholder rule must not apply to what the executor runs.
	if got := res.Aliases.Resolve(res.RawSelector); got != "wire-backend:good-b" {
		t.Fatalf("executor would run %q", got)
	}
}

// TestResolveDefaultRoute_modelOnlyAliasIsRefused proves an alias that resolves
// the default route to a model-only selector is refused. The serving runtime
// routes a request to a backend, so such a route serves no request.
func TestResolveDefaultRoute_modelOnlyAliasIsRefused(t *testing.T) {
	t.Parallel()
	cfg := aliasConfig("wire-backend:good",
		[2]string{"^wire-backend:good$", "gpt-4o-mini"},
	)
	_, err := routing.ResolveDefaultRoute(cfg, nil)
	if err == nil {
		t.Fatal("expected a refusal for a model-only default route")
	}
	if !strings.Contains(err.Error(), "model-only") {
		t.Fatalf("error: %v", err)
	}
}

// TestResolveDefaultRoute_aliasExpansionToEmptySelectorIsRefused proves a
// configured default route that expands to nothing is refused. An alias
// replacement may name a capture group the pattern does not have, and the
// expansion of such a rule is the empty string: an absent default route is a
// different thing from a configured route that expands to nothing.
func TestResolveDefaultRoute_aliasExpansionToEmptySelectorIsRefused(t *testing.T) {
	t.Parallel()
	cfg := aliasConfig("wire-backend:good",
		[2]string{"^wire-backend:good$", "$1"},
	)
	_, err := routing.ResolveDefaultRoute(cfg, nil)
	if err == nil {
		t.Fatal("expected a refusal for the alias expansion that produces an empty route")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error: %v", err)
	}
}

// TestResolveDefaultRoute_noDefaultRouteHasNoBackend proves a configuration
// without a default route resolves to no selector and no default backend, and
// carries the advisory the operator needs.
func TestResolveDefaultRoute_noDefaultRouteHasNoBackend(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{ID: "wire-backend", Kind: "custom-openai-legacy-compatible", Enabled: true},
			},
		},
	}
	res, err := routing.ResolveDefaultRoute(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatalf("ResolveDefaultRoute: %v", err)
	}
	if res.Selector != "" || res.DefaultBackend != "" || res.RawSelector != "" {
		t.Fatalf("selector=%q backend=%q raw=%q want none", res.Selector, res.DefaultBackend, res.RawSelector)
	}
	if !strings.Contains(res.Warning, "no default route") {
		t.Fatalf("warning = %q", res.Warning)
	}
}

// connectorConfig is a configuration whose only enabled backend is a connector:
// its kind has no canonical default model, so the model it publishes in its
// runtime inventory is the only default route it can have.
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

// TestResolveDefaultRouteWithInventory_usesTheDiscoveredModel proves the
// default route of a connector is the model it published in its runtime
// inventory, and that the resolution names the source and the model.
func TestResolveDefaultRouteWithInventory_usesTheDiscoveredModel(t *testing.T) {
	t.Parallel()
	res, err := routing.ResolveDefaultRouteWithInventory(
		connectorConfig(),
		noCanonicalWireModel,
		func(instanceID string) string {
			if instanceID == "claude-acp" {
				return "anthropic/claude-sonnet-5.5"
			}
			return ""
		},
	)
	if err != nil {
		t.Fatalf("ResolveDefaultRouteWithInventory: %v", err)
	}
	if res.Selector != "claude-acp:anthropic/claude-sonnet-5.5" {
		t.Fatalf("selector = %q", res.Selector)
	}
	if res.DefaultBackend != "claude-acp" {
		t.Fatalf("default backend = %q want claude-acp", res.DefaultBackend)
	}
	if res.Source != config.DefaultRouteSourceDiscoveredInventory {
		t.Fatalf("source = %q want %q", res.Source, config.DefaultRouteSourceDiscoveredInventory)
	}
	if res.Model != "anthropic/claude-sonnet-5.5" {
		t.Fatalf("model = %q want anthropic/claude-sonnet-5.5", res.Model)
	}
	if res.Warning != "" {
		t.Fatalf("warning = %q want none", res.Warning)
	}
}

// TestResolveDefaultRouteWithInventory_configuredRouteWins proves the
// configured route stays authoritative over a discovered model.
func TestResolveDefaultRouteWithInventory_configuredRouteWins(t *testing.T) {
	t.Parallel()
	cfg := connectorConfig()
	cfg.Routing.DefaultRoute = "claude-acp:anthropic/claude-opus-4.5"
	res, err := routing.ResolveDefaultRouteWithInventory(
		cfg,
		noCanonicalWireModel,
		func(string) string { return "anthropic/claude-sonnet-5.5" },
	)
	if err != nil {
		t.Fatalf("ResolveDefaultRouteWithInventory: %v", err)
	}
	if res.Selector != "claude-acp:anthropic/claude-opus-4.5" {
		t.Fatalf("selector = %q want the configured route", res.Selector)
	}
	if res.Source != config.DefaultRouteSourceConfigured {
		t.Fatalf("source = %q want %q", res.Source, config.DefaultRouteSourceConfigured)
	}
	if res.Model != "anthropic/claude-opus-4.5" {
		t.Fatalf("model = %q", res.Model)
	}
}

// TestResolveDefaultRouteWithInventory_discoveredRouteIsAliasExpanded proves a
// discovered route is expanded by the model aliases exactly like a configured
// one: the route the executor runs is the effective route, and the published
// model is the one that route names.
func TestResolveDefaultRouteWithInventory_discoveredRouteIsAliasExpanded(t *testing.T) {
	t.Parallel()
	cfg := connectorConfig()
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Pattern: "^claude-acp:anthropic/claude-sonnet-5.5$", Replacement: "claude-acp:anthropic/claude-sonnet-5.5-fast"},
	}
	res, err := routing.ResolveDefaultRouteWithInventory(
		cfg,
		noCanonicalWireModel,
		func(string) string { return "anthropic/claude-sonnet-5.5" },
	)
	if err != nil {
		t.Fatalf("ResolveDefaultRouteWithInventory: %v", err)
	}
	if res.Selector != "claude-acp:anthropic/claude-sonnet-5.5-fast" {
		t.Fatalf("selector = %q", res.Selector)
	}
	if res.Model != "anthropic/claude-sonnet-5.5-fast" {
		t.Fatalf("model = %q", res.Model)
	}
}

// TestResolveDefaultRouteWithInventory_noDiscoveredModelKeepsTheAdvisory
// proves a connector whose inventory cannot be discovered still has no default
// route, and that the advisory names the remedy.
func TestResolveDefaultRouteWithInventory_noDiscoveredModelKeepsTheAdvisory(t *testing.T) {
	t.Parallel()
	res, err := routing.ResolveDefaultRouteWithInventory(
		connectorConfig(),
		noCanonicalWireModel,
		func(string) string { return "" },
	)
	if err != nil {
		t.Fatalf("ResolveDefaultRouteWithInventory: %v", err)
	}
	if res.Selector != "" {
		t.Fatalf("selector = %q want no default route", res.Selector)
	}
	if !strings.Contains(res.Warning, "no default route") {
		t.Fatalf("warning = %q", res.Warning)
	}
	if !strings.Contains(res.Warning, "routing.default_route") {
		t.Fatalf("warning must name the remedy: %q", res.Warning)
	}
}

// TestResolveDefaultRouteWithInventory_discoveredModelCannotChangeTheRoute
// proves a discovered model identifier cannot change the shape of the default
// route.
//
// A model id is external data: a catalog that publishes a model id carrying
// route grammar must not be able to inject failover, parallel execution,
// weights or query parameters into the route the proxy falls back to, and must
// not be able to name another configured backend.
func TestResolveDefaultRouteWithInventory_discoveredModelCannotChangeTheRoute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		model string
	}{
		{name: "failover alternative", model: "vendor/safe|paid-backend:premium"},
		{name: "parallel branch", model: "vendor/safe!paid-backend:premium"},
		{name: "weighted branch", model: "vendor/safe^paid-backend:premium"},
		{name: "query parameters", model: "vendor/safe?reasoning_effort=high"},
		{name: "global parameter block", model: "{ttft_timeout=60}vendor/safe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := routing.ResolveDefaultRouteWithInventory(
				connectorConfig(),
				noCanonicalWireModel,
				func(instanceID string) string {
					if instanceID == "claude-acp" {
						return tc.model
					}
					return ""
				},
			)
			if err != nil {
				t.Fatalf("ResolveDefaultRouteWithInventory: %v", err)
			}
			if res.Selector != "" {
				t.Fatalf("selector = %q want no default route", res.Selector)
			}
			if !strings.Contains(res.Warning, "not a valid route model") {
				t.Fatalf("warning must name the reason: %q", res.Warning)
			}
		})
	}
}

// TestResolveDefaultRouteWithInventory_discoveredRouteNamesOneLeaf proves a
// well-formed discovered model is accepted and published with its source.
func TestResolveDefaultRouteWithInventory_discoveredRouteNamesOneLeaf(t *testing.T) {
	t.Parallel()
	res, err := routing.ResolveDefaultRouteWithInventory(
		connectorConfig(),
		noCanonicalWireModel,
		func(instanceID string) string {
			if instanceID == "claude-acp" {
				return "anthropic/claude-sonnet-5.5"
			}
			return ""
		},
	)
	if err != nil {
		t.Fatalf("ResolveDefaultRouteWithInventory: %v", err)
	}
	if res.Selector != "claude-acp:anthropic/claude-sonnet-5.5" {
		t.Fatalf("selector = %q", res.Selector)
	}
	if res.Model != "anthropic/claude-sonnet-5.5" {
		t.Fatalf("model = %q", res.Model)
	}
	if res.Source != config.DefaultRouteSourceDiscoveredInventory {
		t.Fatalf("source = %q", res.Source)
	}
}

// TestResolveDefaultRoute_compositeRouteHasNoSingleModel proves a composite
// default route publishes no model: failover, weighted and parallel routes can
// run more than one model, so naming the first branch's model would
// overstate what the route does.
func TestResolveDefaultRoute_compositeRouteHasNoSingleModel(t *testing.T) {
	t.Parallel()
	cfg := aliasConfig("wire-backend:model-a^wire-backend:model-b")
	res, err := routing.ResolveDefaultRoute(cfg, nil)
	if err != nil {
		t.Fatalf("ResolveDefaultRoute: %v", err)
	}
	if res.Selector == "" {
		t.Fatal("expected a resolved default route")
	}
	if res.Model != "" {
		t.Fatalf("model = %q want none for a composite route", res.Model)
	}
}

// TestResolveDefaultRoute_modelOnlyBranchKeepsItsLeaf proves a default route
// with a model-only branch exposes that branch as a leaf.
//
// The routing compiler fills a model-only branch with the default backend, so
// the branch runs a model the generation's default route can address. The
// leaves are collected after that expansion, so the branch keeps its model
// binding when the inventory is refreshed.
func TestResolveDefaultRoute_modelOnlyBranchKeepsItsLeaf(t *testing.T) {
	t.Parallel()
	cfg := aliasConfig("wire-backend:model-a",
		[2]string{"^wire-backend:model-a$", "wire-backend:model-a|model-b"},
	)
	res, err := routing.ResolveDefaultRoute(cfg, nil)
	if err != nil {
		t.Fatalf("ResolveDefaultRoute: %v", err)
	}
	if res.DefaultBackend != "wire-backend" {
		t.Fatalf("default backend = %q want wire-backend", res.DefaultBackend)
	}
	if len(res.Leaves) != 2 {
		t.Fatalf("leaves = %+v want the named branch and the model-only branch", res.Leaves)
	}
	if res.Leaves[0].Backend != "wire-backend" || res.Leaves[0].Model != "model-a" {
		t.Fatalf("leaf 0 = %+v", res.Leaves[0])
	}
	if res.Leaves[1].Backend != "wire-backend" || res.Leaves[1].Model != "model-b" {
		t.Fatalf("leaf 1 = %+v want the model-only branch on the default backend", res.Leaves[1])
	}
}
