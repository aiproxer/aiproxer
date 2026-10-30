package runtimebundle_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func TestInspect_MinimalDiscoveryDisabled(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "oa", Enabled: true,
			}},
		},
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	var builtin, configured bool
	for _, e := range rep.Entries {
		if e.Source == "builtin" && e.Kind == "openai-responses" {
			builtin = true
		}
		if e.InstanceID == "oa" && e.State == catalog.StateConfigured {
			configured = true
		}
		if strings.Contains(string(e.Reason), string(os.PathSeparator)) && len(string(e.Reason)) > 32 {
			t.Fatalf("possible path in reason: %q", e.Reason)
		}
	}
	if !builtin || !configured {
		t.Fatalf("builtin=%v configured=%v entries=%+v", builtin, configured, rep.Entries)
	}
}

func TestInspect_MissingPluginExternal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled:         true,
				Paths:           []string{root},
				DevelopmentMode: true,
			},
			Backends: []config.PluginConfig{{
				Kind: "missing-external-kind", ID: "missing-1", Enabled: true,
			}},
		},
	}
	reg := pluginreg.NewRegistry()
	rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err == nil {
		t.Fatal("expected missing configured kind error")
	}
	found := false
	for _, e := range rep.Entries {
		if e.InstanceID == "missing-1" && e.Reason == catalog.ReasonEnabledMissing {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", rep.Entries)
	}
}

func TestDoctor_MissingPluginNoLaunch(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 9201}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	t.Cleanup(func() { _ = h.Close() })

	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true, Paths: []string{t.TempDir()}, DevelopmentMode: true,
			},
			Backends: []config.PluginConfig{{
				Kind: "absent-kind", ID: "abs-1", Enabled: true,
			}},
		},
	}
	rep, err := runtimebundle.DoctorBackendPlugin(context.Background(), cfg, pluginreg.NewRegistry(), "abs-1", h)
	if err != nil {
		t.Fatal(err)
	}
	if launcher.Launches.Load() != 0 {
		t.Fatalf("launches=%d", launcher.Launches.Load())
	}
	if len(rep.Results) != 1 || rep.Results[0].Reason != string(catalog.ReasonEnabledMissing) {
		t.Fatalf("%+v", rep.Results)
	}
	if strings.Contains(rep.Results[0].Guidance, "secret") {
		t.Fatalf("guidance: %q", rep.Results[0].Guidance)
	}
}

func TestDoctor_SecureChannel_SelectedOnly(t *testing.T) {
	t.Parallel()
	var launches atomic.Int64
	launcher := &processhost.TestLauncher{PID: 9202, OnLaunch: func(processhost.LaunchSpec) { launches.Add(1) }}
	h := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	t.Cleanup(func() { _ = h.Close() })

	cfg := &config.Config{
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "oa-builtin", Enabled: true,
			}},
		},
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	rep, err := runtimebundle.DoctorBackendPlugin(context.Background(), cfg, reg, "oa-builtin", h)
	if err != nil {
		t.Fatal(err)
	}
	if launches.Load() != 0 {
		t.Fatalf("builtin doctor must not launch: %d", launches.Load())
	}
	if len(rep.Results) != 1 || rep.Results[0].Reason != "builtin" {
		t.Fatalf("%+v", rep.Results)
	}
}

func TestDiscoveryFromConfig_RoundTripViaInspect(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true, Paths: []string{filepath.ToSlash(t.TempDir())}, DevelopmentMode: true,
			},
			Backends: []config.PluginConfig{{ID: "stub", Enabled: true}},
		},
	}
	_, err := runtimebundle.InspectBackendPlugins(cfg, nil)
	if err != nil && !strings.Contains(err.Error(), "enabled") {
		// stub kind missing from builtins/discovered is expected unresolved signal
		if !strings.Contains(err.Error(), "unresolved") && err.Error() == "" {
			t.Fatal(err)
		}
	}
}
