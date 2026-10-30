package runtimebundle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

func doctorDiscoveryConfig(pluginRoot string) config.BackendDiscoveryConfig {
	return config.BackendDiscoveryConfig{
		Enabled:         true,
		DevelopmentMode: true,
		Paths:           []string{pluginRoot},
	}
}

// TestResolveConfiguredArtifact_ReturnsOwnedStaging proves
// resolveConfiguredArtifact retains staging ownership together with the
// verified artifact and that Close releases the handle before removing the
// aip-plugin-doctor-* root.
func TestResolveConfiguredArtifact_ReturnsOwnedStaging(t *testing.T) {
	root := StageLocalStubForTest(t)
	owned, err := resolveConfiguredArtifact(doctorDiscoveryConfig(root), "local-stub")
	if err != nil {
		t.Fatal(err)
	}
	if owned == nil || owned.Artifact == nil {
		t.Fatal("expected verified doctor artifact")
	}
	if owned.StagingDir == "" {
		t.Fatal("expected retained doctor staging ownership")
	}
	if owned.Artifact.OpenFile() == nil {
		t.Fatal("expected staged handle open before Close")
	}
	staging := owned.StagingDir
	art := owned.Artifact
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("staging %q missing before Close: %v", staging, err)
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging %q still present after Close", staging)
	}
	if art.OpenFile() != nil {
		t.Fatal("artifact handle must be closed after Close")
	}
}

// TestResolveConfiguredArtifact_FailedDescriptorLoopCleansStaging proves a
// failed descriptor loop (no trusted artifact for the kind) removes its
// aip-plugin-doctor-* staging root instead of leaking it.
func TestResolveConfiguredArtifact_FailedDescriptorLoopCleansStaging(t *testing.T) {
	// Isolate the temp root: production creates staging via
	// os.MkdirTemp("", "aip-plugin-doctor-*"), which lands in os.TempDir()
	// — a process-global directory shared with every concurrent go test
	// process. Without isolation, a staging directory created by another
	// process inside this test's snapshot→lookup window is misattributed to
	// this test and the no-leak assertion fails spuriously. Pointing the
	// temp root at a per-test directory makes the before/after glob observe
	// only this test's own staging.
	// Cross-platform: os.TempDir honours TMPDIR on Unix but TMP/TEMP on
	// Windows, so all three are redirected. This test is top-level and never
	// calls t.Parallel (t.Setenv would panic otherwise), and Go runs
	// sequential top-level tests exclusively — parallel tests in this package
	// stay paused until sequential ones finish — so no other test can observe
	// the redirected variables concurrently.
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)
	t.Setenv("TMP", isolated)
	t.Setenv("TEMP", isolated)
	before := listTempDirsWithPrefix("aip-plugin-doctor-")
	_, err := resolveConfiguredArtifact(doctorDiscoveryConfig(badDoctorRoot(t)), "local-stub")
	if err == nil {
		t.Fatal("expected trust failure for bad digest root")
	}
	assertNoNewDirs(t, before, "aip-plugin-doctor-")
}

// badDoctorRoot stages a discovery root whose manifest exports local-stub but
// declares a digest that never matches, forcing the failed descriptor loop.
func badDoctorRoot(t *testing.T) string {
	t.Helper()
	bin := cachedBuiltBinary(t, specLocalStub)
	root := t.TempDir()
	rel := filepath.ToSlash(filepath.Join("bin", filepath.Base(bin.path)))
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyFileMode(dst, bin.path, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.backend.localstub",
  "version":"0.1.0",
  "build_id":"test",
  "executable":%q,
  "sha256":"%064x",
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":%q,"arch":%q}],
  "exports":[{
    "kind":"local-stub",
    "credential_mode":"none",
    "access_scope":"any",
    "process_sharing":"per_instance"
  }]
}`, rel, 0xdeadbeef, runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestDoctorBackendPlugin_ReleasesArtifactAndStaging proves DoctorBackendPlugin
// closes the verified doctor artifact and removes its aip-plugin-doctor-*
// staging root after the operation, leaving no new matching temp dirs.
func TestDoctorBackendPlugin_ReleasesArtifactAndStaging(t *testing.T) {
	// Isolate the temp root: production creates staging via
	// os.MkdirTemp("", "aip-plugin-doctor-*"), which lands in os.TempDir()
	// — a process-global directory shared with every concurrent go test
	// process. Without isolation, a staging directory created by another
	// process inside this test's snapshot→lookup window is misattributed to
	// this test and the no-leak assertion fails spuriously. Pointing the
	// temp root at a per-test directory makes the before/after glob observe
	// only this test's own staging.
	// Cross-platform: os.TempDir honours TMPDIR on Unix but TMP/TEMP on
	// Windows, so all three are redirected. This test is top-level and never
	// calls t.Parallel (t.Setenv would panic otherwise), and Go runs
	// sequential top-level tests exclusively — parallel tests in this package
	// stay paused until sequential ones finish — so no other test can observe
	// the redirected variables concurrently.
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)
	t.Setenv("TMP", isolated)
	t.Setenv("TEMP", isolated)
	root := StageLocalStubForTest(t)
	cfg := &config.Config{
		Access: config.AccessConfig{Mode: "single_user"},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled:         true,
				DevelopmentMode: true,
				Paths:           []string{root},
			},
			Backends: []config.PluginConfig{
				{Kind: "local-stub", ID: "ext-1", Enabled: true},
			},
		},
	}
	before := listTempDirsWithPrefix("aip-plugin-doctor-")
	h := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9203},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = h.Close() })

	rep, err := DoctorBackendPlugin(context.Background(), cfg, pluginreg.NewRegistry(), "ext-1", h)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 || rep.Results[0].State != catalog.StateActive {
		t.Fatalf("doctor results: %+v", rep.Results)
	}
	assertNoNewDirs(t, before, "aip-plugin-doctor-")
}
