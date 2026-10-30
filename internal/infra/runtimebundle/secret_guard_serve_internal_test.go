package runtimebundle

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func stubHandlerComposer(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
}

type countingSecretGuardEnv struct {
	calls int
	names []string
}

func (e *countingSecretGuardEnv) Lookup(name string) (string, bool) {
	e.calls++
	e.names = append(e.names, name)
	return "", false
}

func (e *countingSecretGuardEnv) Snapshot() []string {
	e.calls++
	return nil
}

// assertNoSecretGuardEnvConsultation proves the secret-guard serve path never
// enumerates the host environment for its own inventory: Snapshot (the
// inventory enumeration seam) must stay untouched, and every Lookup must fall
// inside the authorized static-credential namespace captured once per
// candidate (bare built-in roots plus their _1.._N suffixed names). The
// per-candidate credential snapshot is an explicit authorized consumer;
// anything outside its namespace would be secret-guard exfiltration.
func assertNoSecretGuardEnvConsultation(t *testing.T, env *countingSecretGuardEnv) {
	t.Helper()
	allowed := map[string]struct{}{}
	for _, root := range standardplugins.BuiltInCredentialRoots() {
		allowed[root] = struct{}{}
		for i := 1; i <= config.DefaultUpstreamMaxEnvIndex; i++ {
			allowed[fmt.Sprintf("%s_%d", root, i)] = struct{}{}
		}
	}
	for _, name := range env.names {
		if _, ok := allowed[name]; !ok {
			t.Fatalf("host environment lookup outside the authorized credential namespace: %q", name)
		}
	}
}

// TestInspect_DoesNotRequestSecretGuardEnvironment characterizes the Task 5.3
// Inspect invariant: prepareInspect (shared by [InspectRoutes]/[InspectInventory])
// builds no ProcessServices and accepts no host environment seam at
// all, so it structurally cannot consult it (unlike BuildHost, which threads
// an explicit featurehost.HostEnvironment into process construction).
func TestInspect_DoesNotRequestSecretGuardEnvironment(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-single-user.yaml")
	in := InspectInput{
		ConfigPath: path,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
	}
	if _, _, _, err := prepareInspect(t.Context(), in, LoadBootstrapEffectiveWithSource); err != nil {
		t.Fatal(err)
	}
}

func TestBuildHost_serveMultiUserSecretGuardDoesNotConsultEnvironment(t *testing.T) {
	env := &countingSecretGuardEnv{}
	host, err := buildHostWithEnv(t.Context(), hostBuildInput{
		ConfigPath:      MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-multi-user.yaml")),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	}, LoadBootstrapEffectiveWithSource, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupHost(t, host) })
	if host.process == nil || host.manager == nil || host.manager.Active() == nil {
		t.Fatal("BuildHost must publish generation host handles")
	}
	assertNoSecretGuardEnvConsultation(t, env)
}

func TestBuildHost_serveDisabledSecretGuardDoesNotConsultEnvironment(t *testing.T) {
	env := &countingSecretGuardEnv{}
	base, err := os.ReadFile(MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "secrets-guard-block-single-user.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	original := `    - id: secrets-guard
      enabled: true
      config:
        action: block
        audit_failure_policy: fail_closed
        single_user:
          include_popular_env: true
          include_env: []
          exclude_env: []
`
	replacement := `    - id: secrets-guard
      enabled: false
      config:
        action: block
        audit_failure_policy: fail_closed
        single_user:
          include_popular_env: true
          include_env: []
          exclude_env: []
`
	text := strings.Replace(string(base), original, replacement, 1)
	if text == string(base) {
		t.Fatal("expected secrets-guard enabled flag replacement to succeed")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	host, err := buildHostWithEnv(t.Context(), hostBuildInput{
		ConfigPath:      path,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stubHandlerComposer,
	}, LoadBootstrapEffectiveWithSource, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupHost(t, host) })
	if host.process == nil || host.manager == nil || host.manager.Active() == nil {
		t.Fatal("BuildHost must publish generation host handles")
	}
	assertNoSecretGuardEnvConsultation(t, env)
}
