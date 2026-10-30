package runtimebundle

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
)

func TestRegisterManifestDiscoveredKinds_registersWithoutProcessHost(t *testing.T) {
	t.Parallel()
	pluginRoot := StageLocalStubForTest(t)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled:         true,
				DevelopmentMode: true,
				Paths:           []string{pluginRoot},
			},
			Backends: []config.PluginConfig{
				{Kind: "local-stub", ID: "dogfood-local", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := registerManifestDiscoveredKinds(cfg, reg); err != nil {
		t.Fatal(err)
	}
	if !reg.IsDiscoveredBackend("local-stub") {
		t.Fatal("expected manifest-only registration of discovered local-stub kind")
	}
}

func TestRegisterManifestDiscoveredKinds_skipsWhenRegistryAuthoritative(t *testing.T) {
	t.Parallel()
	pluginRoot := StageLocalStubForTest(t)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled:         true,
				DevelopmentMode: true,
				Paths:           []string{pluginRoot},
			},
			Backends: []config.PluginConfig{
				{Kind: "local-stub", ID: "dogfood-local", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	if err := localstubreg.RegisterInProcess(reg); err != nil {
		t.Fatal(err)
	}
	if err := registerManifestDiscoveredKinds(cfg, reg); err != nil {
		t.Fatal(err)
	}
	if reg.IsDiscoveredBackend("local-stub") {
		t.Fatal("authoritative registry must skip manifest re-registration")
	}
}
