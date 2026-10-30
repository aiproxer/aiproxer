package runtimebundle_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"gopkg.in/yaml.v3"
)

// TestBuild_injectedRegistryOnly builds a runtime using an isolated [pluginreg.Registry] so tests
// can assemble partial bundles without sharing factories across registries.
func TestBuild_injectedRegistryOnly(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if len(b.Executor().Backends) != 1 {
		t.Fatalf("backends: got %d want 1", len(b.Executor().Backends))
	}
	if _, ok := b.Executor().Backends["openai-only"]; !ok {
		t.Fatal("missing instance openai-only")
	}
	if b.PluginRegistry() != reg {
		t.Fatalf("PluginRegistry: got %p want %p", b.PluginRegistry(), reg)
	}
}
