package runtimebundle

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// pluginServeLeakTestConfig is a minimal config accepted by NewProcessServices
// that also drives real discovered-plugin install (aip-plugin-serve-*
// staging with trust.Verify binding). It mirrors the dogfood discovery shape.
func pluginServeLeakTestConfig(pluginRoot string) *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
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
		Continuity: config.ContinuityConfig{InMemory: true},
	}
}

// mustDiscoveredInstall runs the production discovered-plugin install path so
// the returned bundle carries a real verified (Windows-locked) staged artifact
// plus its staging root. Callers own the bundle until they hand it to
// ProcessServices or release it.
func mustDiscoveredInstall(t *testing.T) (*config.Config, *pluginreg.Registry, *discoveredBackendInstall) {
	t.Helper()
	pluginRoot := StageLocalStubForTest(t)
	cfg := pluginServeLeakTestConfig(pluginRoot)
	reg := pluginreg.NewRegistry()
	disc, err := installDiscoveredBackendExports(cfg, reg)
	if err != nil {
		t.Fatalf("installDiscoveredBackendExports: %v", err)
	}
	if disc == nil {
		t.Fatal("expected discovery install ownership")
	}
	if len(disc.Artifacts) == 0 {
		t.Fatal("expected verified artifacts retained for staged-handle close")
	}
	if disc.StagingDir == "" {
		t.Fatal("expected staging dir")
	}
	return cfg, reg, disc
}

// TestProcessServices_DiscoveredStagingRemovedAfterClose proves a real
// verified/locked staged artifact transferred into ProcessServices is closed
// and its aip-plugin-serve-* staging root is removed on Close. On Windows
// this exercises the handle-close-before-removeAllRetry order; on all
// platforms Close is idempotent and leaves no artifact handles open.
func TestProcessServices_DiscoveredStagingRemovedAfterClose(t *testing.T) {
	t.Parallel()
	cfg, reg, disc := mustDiscoveredInstall(t)
	staging := disc.StagingDir
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("staging %q missing before NewProcessServices: %v", staging, err)
	}
	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &BuildOptions{PluginRegistry: reg},
		Tracing: ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
		PluginHost:       disc.Host,
		PluginArtifacts:  disc.Artifacts,
		PluginStagingDir: staging,
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging %q still present after Close (Windows handle not released): %v", staging, err)
	}
	for i, a := range disc.Artifacts {
		if a.OpenFile() != nil {
			t.Fatalf("artifact %d handle still open after Close", i)
		}
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
}

// TestProcessServices_DiscoveredStagingReleasedOnSetupFailure proves the
// early-failure release path (nil Opts/PluginRegistry) disposes the adopted
// host, artifacts, and staging root even though NewProcessServices never
// completes.
func TestProcessServices_DiscoveredStagingReleasedOnSetupFailure(t *testing.T) {
	t.Parallel()
	cfg, _, disc := mustDiscoveredInstall(t)
	staging := disc.StagingDir
	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:              cfg,
		Log:              testkit.DiscardLogger(),
		Opts:             nil, // forces the nil-PluginRegistry early-failure release path
		PluginHost:       disc.Host,
		PluginArtifacts:  disc.Artifacts,
		PluginStagingDir: staging,
	})
	if ps != nil {
		_ = ps.Close()
		t.Fatal("expected NewProcessServices failure with nil Opts")
	}
	if err == nil {
		t.Fatal("expected error from nil Opts")
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging %q must be released on setup failure: %v", staging, err)
	}
	for i, a := range disc.Artifacts {
		if a.OpenFile() != nil {
			t.Fatalf("artifact %d handle still open after setup failure", i)
		}
	}
}

// TestInstallDiscoveredBackendExports_ReleaseRemovesStaging proves the
// discoveredBackendInstall.release ownership closer (used by
// ValidateDistribution's logger-failure path) closes artifacts before removing
// staging with the Windows-aware retry helper.
func TestInstallDiscoveredBackendExports_ReleaseRemovesStaging(t *testing.T) {
	t.Parallel()
	_, _, disc := mustDiscoveredInstall(t)
	staging := disc.StagingDir
	artifacts := append([]*trust.VerifiedArtifact(nil), disc.Artifacts...)
	disc.release()
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging %q still present after release: %v", staging, err)
	}
	for i, a := range artifacts {
		if a.OpenFile() != nil {
			t.Fatalf("artifact %d handle still open after release", i)
		}
	}
}

// listTempDirsWithPrefix returns sorted absolute paths under os.TempDir whose
// base names start with prefix.
func listTempDirsWithPrefix(prefix string) []string {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			out = append(out, filepath.Join(os.TempDir(), e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// assertNoNewDirs fails if any os.TempDir entry matching prefix appeared after
// the before snapshot (i.e. the operation leaked a temp dir).
func assertNoNewDirs(t *testing.T, before []string, prefix string) {
	t.Helper()
	beforeSet := make(map[string]struct{}, len(before))
	for _, b := range before {
		beforeSet[b] = struct{}{}
	}
	var leaked []string
	deadline := time.Now().Add(3 * time.Second)
	for {
		after := listTempDirsWithPrefix(prefix)
		leaked = nil
		for _, d := range after {
			if _, ok := beforeSet[d]; !ok {
				if _, err := os.Stat(d); err == nil {
					leaked = append(leaked, d)
				}
			}
		}
		if len(leaked) == 0 {
			time.Sleep(20 * time.Millisecond)
			afterCheck := listTempDirsWithPrefix(prefix)
			clean := true
			for _, d := range afterCheck {
				if _, ok := beforeSet[d]; !ok {
					if _, err := os.Stat(d); err == nil {
						clean = false
						break
					}
				}
			}
			if clean {
				break
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, d := range leaked {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("leaked temp dir %q (prefix %q)", d, prefix)
		}
	}
}
