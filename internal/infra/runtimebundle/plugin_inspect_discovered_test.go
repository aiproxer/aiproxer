package runtimebundle_test

import (
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"gopkg.in/yaml.v3"
)

func TestInspect_DiscoveredOptionalKinds_NoBuiltinCollision(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind  string
		stage func(testing.TB) string
	}{
		{kind: "openrouter", stage: runtimebundle.StageOpenRouterForTest},
		{kind: "opencode-go", stage: runtimebundle.StageOpenCodeForTest},
		{kind: "opencode-zen", stage: runtimebundle.StageOpenCodeForTest},
		{kind: "openai-codex", stage: runtimebundle.StageCodexForTest},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			root := tc.stage(t)

			reg := pluginreg.NewRegistry()
			if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
				t.Fatal(err)
			}
			// Simulate post-InstallDiscoveredExports: discovered provenance must not
			// classify the optional kind as a builtin (false builtin_collision).
			if err := reg.RegisterDiscoveredLifecycleBackendWithProfile(tc.kind, func(string, yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
				return pluginreg.BackendBuildResult{Backend: execbackend.Backend{}}, nil
			}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone}); err != nil {
				t.Fatal(err)
			}

			cfg := &config.Config{
				Continuity: config.ContinuityConfig{InMemory: true},
				Plugins: config.PluginsConfig{
					BackendDiscovery: config.BackendDiscoveryConfig{
						Enabled: true, Paths: []string{root}, DevelopmentMode: true,
					},
					Backends: []config.PluginConfig{{
						Kind: tc.kind, ID: tc.kind + "-1", Enabled: true,
					}},
				},
			}
			rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
			if err != nil {
				t.Fatal(err)
			}
			var sawDiscovered, sawBuiltinCollision, sawKindAsBuiltin bool
			for _, e := range rep.Entries {
				if e.Kind == tc.kind && e.Reason == catalog.ReasonBuiltinCollision {
					sawBuiltinCollision = true
				}
				if e.Source == "builtin" && e.Kind == tc.kind {
					sawKindAsBuiltin = true
				}
				if e.Kind == tc.kind && (e.Source == "discovered" || e.Source == "configured") &&
					e.Reason != catalog.ReasonBuiltinCollision {
					sawDiscovered = true
				}
			}
			if sawBuiltinCollision {
				t.Fatalf("discovered kind %q falsely classified as builtin_collision: %+v", tc.kind, rep.Entries)
			}
			if sawKindAsBuiltin {
				t.Fatalf("discovered kind %q must not appear as builtin source: %+v", tc.kind, rep.Entries)
			}
			if !sawDiscovered {
				t.Fatalf("expected discovered/configured entry for %q: %+v", tc.kind, rep.Entries)
			}
		})
	}
}

func TestInspect_RegistryPollution_OptionalKindNotBuiltin(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"openrouter", "opencode-go", "openai-codex"} {
		if err := reg.RegisterDiscoveredLifecycleBackendWithProfile(kind, func(string, yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
			return pluginreg.BackendBuildResult{Backend: execbackend.Backend{}}, nil
		}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{Enabled: false},
		},
	}
	rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Entries {
		if e.Source == "builtin" && (e.Kind == "openrouter" || e.Kind == "opencode-go" || e.Kind == "openai-codex") {
			t.Fatalf("optional kind %q classified as builtin after registry pollution: %+v", e.Kind, rep.Entries)
		}
	}
}

func TestInspect_RealBuiltinCollision_Diagnosed(t *testing.T) {
	t.Parallel()
	root := stageColliderExportingEssential(t, "openai-responses")

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true, Paths: []string{root}, DevelopmentMode: true,
			},
		},
	}
	rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, e := range rep.Entries {
		if e.Kind == "openai-responses" && e.Reason == catalog.ReasonBuiltinCollision && e.Source == "discovered" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected builtin_collision for openai-responses: %+v", rep.Entries)
	}
}

func stageColliderExportingEssential(t *testing.T, kind string) string {
	t.Helper()
	return runtimebundle.StageColliderExportingEssentialForTest(t, kind)
}
