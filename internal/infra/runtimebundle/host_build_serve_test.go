package runtimebundle_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func testConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "config", "config.yaml")
}

// TestInspectRoutes_ReturnsFocusedReadModel characterizes the Task 5.3 Inspect
// invariant: [runtimebundle.InspectRoutes] returns only [runtimebundle.RoutesSnapshot]
// — never a broad bootstrap/process/generation aggregate — because it builds
// no process/generation runtime at all.
func TestInspectRoutes_ReturnsFocusedReadModel(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	snap, err := runtimebundle.InspectRoutes(ctx, runtimebundle.InspectInput{
		ConfigPath: testConfigPath(t),
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveDefaultRoute == "" {
		t.Fatal("expected effective default route")
	}
}

// TestInspectInventory_ReturnsFocusedReadModel characterizes the Task 5.3
// Inspect invariant for [runtimebundle.InspectInventory]: only
// [diag.InventorySnapshot] is returned, never a broad bootstrap aggregate.
func TestInspectInventory_ReturnsFocusedReadModel(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	snap, err := runtimebundle.InspectInventory(ctx, runtimebundle.InspectInput{
		ConfigPath: testConfigPath(t),
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Frontends) == 0 {
		t.Fatal("expected frontend rows")
	}
}

func TestBuildHost_servePublishesInitialGeneration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      testConfigPath(t),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostServeCleanup(t, host)
	if runtimebundle.HostProcess(host) == nil || runtimebundle.HostManager(host) == nil || runtimebundle.HostManager(host).Active() == nil {
		t.Fatal("BuildHost must publish process services and generation 1")
	}
	lease, ok := runtimebundle.HostManager(host).Acquire()
	if !ok || lease.Handler() == nil {
		t.Fatal("BuildHost must publish an acquireable handler")
	}
	lease.Release()
}

func TestBuildHost_serveRequiresHandlerComposer(t *testing.T) {
	t.Parallel()
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath: testConfigPath(t),
		Mandatory:  aipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		hostServeCleanup(t, host)
		t.Fatal("expected nil HandlerComposer failure")
	}
	if !strings.Contains(err.Error(), "HandlerComposer") {
		t.Fatalf("error=%v want HandlerComposer requirement", err)
	}
}

func TestBuildHost_serveSingleUserSecretGuardSnapshotsProcessEnv(t *testing.T) {
	const probe = "AIP_TEST_SECRETGUARD_INCLUDE"
	const secret = testkit.SyntheticOpenAIAPIKey
	t.Setenv(probe, secret)

	basePath := runtimebundle.MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-single-user.yaml"))
	base, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	text := bytes.Replace(base, []byte("include_popular_env: true\n          include_env: []\n"), []byte("include_popular_env: false\n          include_env: ["+probe+"]\n"), 1)
	if bytes.Equal(text, base) {
		t.Fatal("expected secrets-guard example replacement to succeed")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, text, 0o600); err != nil {
		t.Fatal(err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostServeCleanup(t, host)

	// Assert against the published startup generation. A second CompileCandidate
	// would contend on exclusive shared-artifact local-stub ownership.
	active := runtimebundle.HostManager(host).Active()
	if active == nil {
		t.Fatal("BuildHost must publish an active generation")
	}
	provider, ok := active.RequestPlane().(runtimehost.ExecutorProvider)
	if !ok || provider == nil {
		t.Fatal("published generation must expose ExecutorProvider")
	}
	ex, ok := provider.ExecutorView().(*coreruntime.Executor)
	if !ok || ex == nil || ex.RuntimeSnapshot == nil {
		t.Fatal("published generation must expose *runtime.Executor RuntimeSnapshot")
	}
	plane := ex.RuntimeSnapshot.SecretGuardPlane()
	if plane.MatcherResolver == nil {
		t.Fatal("single-user serve must bind a secret-guard matcher resolver")
	}
	m, err := plane.MatcherResolver.Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected matcher resolver")
	}
	findings, err := m.ScanString(t.Context(), "prefix="+secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("standard BuildHost serve did not load the synthetic env secret")
	}
}

func TestBuildHost_nilContext(t *testing.T) {
	t.Parallel()
	_, err := runtimebundle.BuildHost(nil, runtimebundle.BuildHostInput{ //nolint:staticcheck // intentional nil ctx contract
		ConfigPath: testConfigPath(t),
		Mandatory:  aipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestInspectRoutes_RejectsInvalidCustomBackendPrefix(t *testing.T) {
	t.Parallel()
	base, err := os.ReadFile(testConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	customBackend := `    - id: openai-legacy-copy
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: openai-legacy
        base_url: http://127.0.0.1:9/v1
`
	text := strings.Replace(string(base), "  features:\n", customBackend+"  features:\n", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = runtimebundle.InspectRoutes(t.Context(), runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err == nil {
		t.Fatal("expected custom backend prefix validation error")
	}
	var coll *pluginreg.OwnershipCollisionError
	if !errors.As(err, &coll) {
		t.Fatalf("error = %v, want OwnershipCollisionError", err)
	}
	if coll.Key != "openai-legacy" {
		t.Fatalf("collision key = %q, want openai-legacy", coll.Key)
	}
	msg := coll.Error()
	if !strings.Contains(msg, "openai-legacy") || !strings.Contains(msg, "openai-legacy-copy") {
		t.Fatalf("collision error %q must identify both owners", msg)
	}
}

func TestInspectRoutes_RejectsDuplicateEnabledSecretsGuardRegistrations(t *testing.T) {
	t.Parallel()
	base, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-log-single-user.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	original := `    - id: secrets-guard
      enabled: true
      config:
        action: log
        audit_failure_policy: best_effort
        single_user:
          include_popular_env: true
          include_env: []
          exclude_env: []
`
	duplicate := `    - kind: secrets-guard
      id: sg-log-1
      enabled: true
      config:
        action: log
        audit_failure_policy: best_effort
        single_user:
          include_popular_env: true
          include_env: []
          exclude_env: []
    - kind: secrets-guard
      id: sg-log-2
      enabled: true
      config:
        action: block
        audit_failure_policy: fail_closed
        single_user:
          include_popular_env: true
          include_env: []
          exclude_env: []
`
	text := strings.Replace(string(base), original, duplicate, 1)
	if text == string(base) {
		t.Fatal("expected secrets-guard block replacement to succeed")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = runtimebundle.InspectRoutes(t.Context(), runtimebundle.InspectInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	})
	if err == nil {
		t.Fatal("expected duplicate enabled secrets-guard registrations to fail")
	}
	for _, bad := range []string{"action:", "block", "log", "sg-log-1", "sg-log-2"} {
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("error leaked %q: %v", bad, err)
		}
	}
}
