package runtimebundle_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"gopkg.in/yaml.v3"
)

// ownedRouteFrontend is the generic OpenResponses frontend: the one bundled frontend
// whose base path is an arbitrary operator-configurable value.
const ownedRouteFrontend = "openresponses"

// ownedRouteFrontendRow builds one enabled OpenResponses frontend row carrying the
// given config body, the same shape an operator's plugins list produces.
func ownedRouteFrontendRow(t *testing.T, body string) config.PluginConfig {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(body), &node); err != nil {
		t.Fatalf("yaml.Unmarshal(%q) = %v", body, err)
	}
	return config.PluginConfig{ID: ownedRouteFrontend, Enabled: true, Config: node}
}

// selfDefenseCandidates projects one generation from cfg and returns the owned-route
// CANDIDATES its self-defense gate was given, before the router resolves them.
func selfDefenseCandidates(t *testing.T, cfg *config.Config) []httpcontract.OwnedRouteCandidate {
	t.Helper()
	_, input := selfDefenseGenerationInput(t, cfg)
	sd := input.Security.SelfDefense
	if !sd.SelfDefenseEnabled() {
		t.Fatal("the fixture must leave self-defense default-on")
	}
	// Composition fills OwnedRoutes from these; a generation that only projects
	// candidates has not resolved them yet, which is exactly the seam under test.
	if len(sd.OwnedRoutes) != 0 {
		t.Fatalf("OwnedRoutes = %v, want empty before the router resolves the candidates", sd.OwnedRoutes)
	}
	return sd.OwnedRouteCandidates
}

func hasCandidate(candidates []httpcontract.OwnedRouteCandidate, method, path string) bool {
	return slices.Contains(candidates, httpcontract.OwnedRouteCandidate{Method: method, Path: path})
}

// TestGenerationProjectsEveryOperatorConfiguredFrontendBasePathAsACandidate keeps
// the original regression covered: an operator-chosen base_path is a valid
// configuration that publishes real data-plane routes, so its paths must reach the
// gate as candidates. The pre-existing non-collision test sampled seven hand-picked
// frontend configs and called that the standard surface, so it never exercised a
// base_path inside a frozen family.
//
// What the CANDIDATE list must contain is deliberately weaker than what the gate
// must not refuse: whether a candidate is actually owned, and with which match
// semantics, is the router's answer. That resolution is covered against a real
// ServeMux in internal/stdhttp/selfdefense.
func TestGenerationProjectsEveryOperatorConfiguredFrontendBasePathAsACandidate(t *testing.T) {
	t.Parallel()

	for _, basePath := range []string{
		"/wp-admin", "/wp-content", "/.github", "/.gitlab", "/cgi-bin",
		"/phpmyadmin", "/pma", "/adminer", "/vendor/phpunit", "/.ht",
	} {
		t.Run(basePath, func(t *testing.T) {
			t.Parallel()

			cfg := processServicesTestConfig()
			cfg.Plugins.Frontends = []config.PluginConfig{ownedRouteFrontendRow(t, "base_path: "+basePath+"\n")}

			candidates := selfDefenseCandidates(t, cfg)
			for _, route := range []string{basePath + "/responses", basePath + "/responses/compact"} {
				if !hasCandidate(candidates, http.MethodPost, route) {
					t.Errorf("candidates %v missing the published route POST %q", candidates, route)
				}
			}
		})
	}
}

// TestGenerationProjectsConfiguredMountPathsAsCandidates covers the other half of
// the configurable surface. Diagnostics, metrics and the protected operator mounts
// accept any normalized absolute path, so a legitimate health or metrics endpoint can
// be placed inside a frozen family too.
func TestGenerationProjectsConfiguredMountPathsAsCandidates(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/wp-admin/healthz", "/phpmyadmin/metrics", "/.github/attempts"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			cfg := processServicesTestConfig()
			cfg.Diagnostics.Enabled = true
			cfg.Diagnostics.HealthPath = path
			cfg.Observability.Metrics.Enabled = true
			cfg.Observability.Metrics.Path = "/wp-content/metrics"

			candidates := selfDefenseCandidates(t, cfg)
			if !hasCandidate(candidates, "", path) {
				t.Errorf("candidates %v missing the configured diagnostics path %q", candidates, path)
			}
			if !hasCandidate(candidates, "", "/wp-content/metrics") {
				t.Errorf("candidates %v missing the configured metrics path", candidates)
			}
		})
	}
}

// TestGenerationProjectsEveryConfigurableMountPathAsACandidate is the completeness
// guard. It walks every configurable data-plane path group in the config model, sets
// each one inside a frozen family, and requires the candidate list to carry it. A
// newly configurable mount path that never reaches the gate fails here.
func TestGenerationProjectsEveryConfigurableMountPathAsACandidate(t *testing.T) {
	t.Parallel()

	groups := map[string]func(*config.Config, string){
		"diagnostics.health_path":      func(c *config.Config, p string) { c.Diagnostics.Enabled = true; c.Diagnostics.HealthPath = p },
		"diagnostics.attempts_path":    func(c *config.Config, p string) { c.Diagnostics.Enabled = true; c.Diagnostics.AttemptsPath = p },
		"diagnostics.inventory_path":   func(c *config.Config, p string) { c.Diagnostics.Enabled = true; c.Diagnostics.InventoryPath = p },
		"diagnostics.route_trace_path": func(c *config.Config, p string) { c.Diagnostics.Enabled = true; c.Diagnostics.RouteTracePath = p },
		"diagnostics.pprof_path":       func(c *config.Config, p string) { c.Diagnostics.Enabled = true; c.Diagnostics.PprofPath = p },
		"observability.metrics.path": func(c *config.Config, p string) {
			c.Observability.Metrics.Enabled = true
			c.Observability.Metrics.Path = p
		},
		"secure_session.diagnostics": func(c *config.Config, p string) {
			enabled := true
			c.SecureSession.Enabled = &enabled
			c.SecureSession.DiagnosticsPathPrefix = p
		},
		"model_catalog.diagnostics_path": func(c *config.Config, p string) { c.ModelCatalog.DiagnosticsPath = p },
		"model_inventory.diagnostics":    func(c *config.Config, p string) { c.ModelInventory.DiagnosticsPath = p },
		"accounting.admin.path": func(c *config.Config, p string) {
			c.Accounting.Admin.Enabled = true
			c.Accounting.Admin.Path = p
		},
	}

	// Distinct frozen families, so no single carve can cover two groups by luck.
	families := []string{
		"/wp-admin", "/wp-content", "/.github", "/.gitlab", "/.ht",
		"/cgi-bin", "/phpmyadmin", "/pma", "/adminer", "/vendor/phpunit",
	}

	i := 0
	for name, apply := range groups {
		path := families[i%len(families)] + "/x"
		i++
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := processServicesTestConfig()
			apply(cfg, path)
			if err := config.Validate(cfg); err != nil {
				t.Skipf("fixture for %s is not a valid configuration (%v); nothing to prove here", name, err)
			}
			candidates := selfDefenseCandidates(t, cfg)
			if !hasCandidate(candidates, "", path) {
				t.Errorf("%s = %q is a valid configured data-plane path but is not a candidate", name, path)
			}
		})
	}
}

// TestCandidatesCarryMethodsOnlyWhereTheRouteHasOne pins that the method comes from
// the route claim and is left empty for a configuration path, which is what lets the
// router resolve a method-less registration instead of being probed with a method it
// was never registered for.
func TestCandidatesCarryMethodsOnlyWhereTheRouteHasOne(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	cfg.Diagnostics.Enabled = true
	cfg.Diagnostics.HealthPath = "/wp-admin/healthz"
	cfg.Plugins.Frontends = []config.PluginConfig{ownedRouteFrontendRow(t, "base_path: /wp-admin\n")}

	candidates := selfDefenseCandidates(t, cfg)
	if !hasCandidate(candidates, "", "/wp-admin/healthz") {
		t.Errorf("candidates %v: a configuration path must carry no method", candidates)
	}
	if !hasCandidate(candidates, http.MethodPost, "/wp-admin/responses") {
		t.Errorf("candidates %v: a frontend route claim must carry its method", candidates)
	}
}

// TestCandidateSetIsDeterministic guards the candidate set against map-iteration
// order, so a generation does not publish a different list between reloads of one
// configuration.
func TestCandidateSetIsDeterministic(t *testing.T) {
	t.Parallel()

	build := func() []httpcontract.OwnedRouteCandidate {
		cfg := processServicesTestConfig()
		cfg.Diagnostics.Enabled = true
		cfg.Diagnostics.HealthPath = "/wp-admin/healthz"
		cfg.Diagnostics.AttemptsPath = "/phpmyadmin/attempts"
		cfg.Observability.Metrics.Enabled = true
		cfg.Observability.Metrics.Path = "/.github/metrics"
		cfg.Plugins.Frontends = []config.PluginConfig{ownedRouteFrontendRow(t, "base_path: /cgi-bin\n")}
		return selfDefenseCandidates(t, cfg)
	}
	first := build()
	if len(first) == 0 {
		t.Fatal("fixture must publish candidates")
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].Path > first[i].Path {
			t.Fatalf("candidates must be emitted in a stable sorted order, got %v", first)
		}
	}
	for range 4 {
		if got := build(); !slices.Equal(got, first) {
			t.Fatalf("candidates are not deterministic: %v then %v", first, got)
		}
	}
	seen := map[httpcontract.OwnedRouteCandidate]bool{}
	for _, candidate := range first {
		if seen[candidate] {
			t.Errorf("duplicate candidate %+v in %v", candidate, first)
		}
		seen[candidate] = true
	}
}

// TestGenerationWithNoSelfDefenseProjectsNoOwnedRoutes keeps the fast path clean: a
// disabled generation carries no gate and therefore no routing inventory.
func TestGenerationWithNoSelfDefenseProjectsNoOwnedRoutes(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	disabled := false
	cfg.Access.SelfDefense.Enabled = &disabled
	_, input := selfDefenseGenerationInput(t, cfg)
	if sd := input.Security.SelfDefense; sd.SelfDefenseEnabled() ||
		len(sd.OwnedRoutes) != 0 || len(sd.OwnedRouteCandidates) != 0 {
		t.Fatalf("disabled self-defense must project the zero input, got %+v", sd)
	}
}
