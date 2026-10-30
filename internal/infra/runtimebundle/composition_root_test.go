package runtimebundle

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/testkit/localstubreg"
)

func TestInstallDiscoveredBackendExports_SkipsWhenRegistryAuthoritative(t *testing.T) {
	t.Parallel()
	pluginRoot := StageLocalStubForTest(t)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled:         true,
				DevelopmentMode: true,
				// Deliberately point at a missing root: authoritative registry must skip discovery.
				Paths: []string{filepath.Join(pluginRoot, "missing-root")},
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
	disc, err := installDiscoveredBackendExports(cfg, reg)
	if err != nil {
		t.Fatalf("authoritative registry must skip artifact discovery: %v", err)
	}
	if disc != nil {
		t.Fatalf("skip path must not create install ownership; got host=%v staging=%q arts=%d", disc.Host, disc.StagingDir, len(disc.Artifacts))
	}
}

func TestInstallDiscoveredBackendExports_FailClosedWhenKindUnresolved(t *testing.T) {
	t.Parallel()
	emptyRoot := t.TempDir()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled:         true,
				DevelopmentMode: true,
				Paths:           []string{emptyRoot},
			},
			Backends: []config.PluginConfig{
				{Kind: "local-stub", ID: "dogfood-local", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	disc, err := installDiscoveredBackendExports(cfg, reg)
	if disc != nil {
		disc.release()
	}
	if err == nil {
		t.Fatal("expected fail-closed unresolved enabled kind")
	}
	if !errors.Is(err, catalog.ErrEnabledUnresolved) {
		t.Fatalf("want ErrEnabledUnresolved, got %v", err)
	}
}

func TestInstallDiscoveredBackendExports_InstallsWhenDiscoveryEnabled(t *testing.T) {
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
	disc, err := installDiscoveredBackendExports(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	if disc == nil {
		t.Fatal("expected discovery install ownership")
	}
	t.Cleanup(disc.release)
	if !reg.HasBackend("local-stub") {
		t.Fatal("expected local-stub installed from discovery")
	}
	if _, ok := reg.BackendSecurityProfile("local-stub"); !ok {
		t.Fatal("expected security profile for discovered local-stub")
	}
	if disc.Host == nil || disc.StagingDir == "" {
		t.Fatal("install path must transfer host/staging ownership to caller")
	}
	if len(disc.Artifacts) == 0 {
		t.Fatal("install path must retain verified artifacts for staged-handle close")
	}
	if _, err := os.Stat(disc.StagingDir); err != nil {
		t.Fatalf("staging missing before release: %v", err)
	}
}

func TestInstallDiscoveredBackendExports_DisabledSkips(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{Enabled: false},
			Backends: []config.PluginConfig{
				{Kind: "local-stub", ID: "dogfood-local", Enabled: true},
			},
		},
	}
	reg := pluginreg.NewRegistry()
	disc, err := installDiscoveredBackendExports(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	if disc != nil || reg.HasBackend("local-stub") {
		t.Fatal("disabled discovery must be a no-op")
	}
}
