package runtimebundle_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"gopkg.in/yaml.v3"
)

// credentialProbeEnv is a hermetic host environment stub: candidate captures
// read from it, never from the process environment.
type credentialProbeEnv struct {
	values map[string]string
}

func (e credentialProbeEnv) Lookup(name string) (string, bool) {
	v, ok := e.values[name]
	return v, ok
}

func (e credentialProbeEnv) Snapshot() []string { return nil }

var _ featurehost.HostEnvironment = credentialProbeEnv{}

// credentialProbeFactory records the per-candidate credential snapshot handed
// to factories and builds a no-op backend.
type credentialProbeFactory struct {
	recorded []pluginreg.HostCredentialSnapshot
}

func (f *credentialProbeFactory) build(_ string, _ yaml.Node, _ *http.Client, deps pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
	f.recorded = append(f.recorded, deps.Credentials)
	return pluginreg.BackendBuildResult{Backend: execbackend.Backend{
		BackendPrefixes: []string{"probe"},
		ModelInventory:  testModelInventory(),
	}}, nil
}

func credentialProbeConfig(maxEnvIndex int) *config.Config {
	return &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		UpstreamCredentials: config.UpstreamCredentialsConfig{
			MaxEnvIndex: maxEnvIndex,
		},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "cred-probe", ID: "probe", Enabled: true}}},
	}
}

func credentialProbeProcess(t *testing.T, cfg *config.Config, env featurehost.HostEnvironment) (*runtimebundle.ProcessServices, *credentialProbeFactory) {
	t.Helper()
	probe := &credentialProbeFactory{}
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterLifecycleBackendWithProfilesAndSource("cred-probe", probe.build,
		pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone},
		pluginreg.BackendExecutionProfile{},
		pluginreg.BackendSourceBuiltin); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:     cfg,
		Log:     testkit.DiscardLogger(),
		Opts:    &runtimebundle.BuildOptions{PluginRegistry: reg},
		HostEnv: env,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps, probe
}

// TestCredentialReload_CandidateSnapshotRefreshesKeys proves the authorized
// per-candidate capture: each candidate compile resolves the current host
// environment through the candidate policy, so a reload picks up rotated keys
// without any request-time environment read.
func TestCredentialReload_CandidateSnapshotRefreshesKeys(t *testing.T) {
	env := credentialProbeEnv{values: map[string]string{"OPENAI_API_KEY": "first-key"}}
	ps, probe := credentialProbeProcess(t, credentialProbeConfig(0), env)

	first, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{Process: ps})
	if err != nil {
		t.Fatalf("first CompileCandidate: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if len(probe.recorded) != 1 {
		t.Fatalf("recorded snapshots = %d, want 1", len(probe.recorded))
	}
	if got := probe.recorded[0].Keys("OPENAI_API_KEY"); len(got) != 1 || got[0] != "first-key" {
		t.Fatalf("first snapshot OPENAI_API_KEY = %#v, want [first-key]", got)
	}

	env.values["OPENAI_API_KEY"] = "rotated-key"
	second, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{Process: ps})
	if err != nil {
		t.Fatalf("second CompileCandidate: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if len(probe.recorded) != 2 {
		t.Fatalf("recorded snapshots = %d, want 2", len(probe.recorded))
	}
	if got := probe.recorded[1].Keys("OPENAI_API_KEY"); len(got) != 1 || got[0] != "rotated-key" {
		t.Fatalf("second snapshot OPENAI_API_KEY = %#v, want [rotated-key]", got)
	}
	if got := probe.recorded[0].Keys("OPENAI_API_KEY"); len(got) != 1 || got[0] != "first-key" {
		t.Fatalf("first snapshot mutated to %#v, want [first-key]", got)
	}
}

// TestCredentialReload_InvalidPolicyRejectsCandidate proves a candidate with
// an out-of-bounds scan policy fails compilation with a secret-free,
// field-naming error before any publication.
func TestCredentialReload_InvalidPolicyRejectsCandidate(t *testing.T) {
	env := credentialProbeEnv{values: map[string]string{"OPENAI_API_KEY": "some-key"}}
	ps, _ := credentialProbeProcess(t, credentialProbeConfig(0), env)

	bad := credentialProbeConfig(999)
	_, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: bad,
	})
	if err == nil {
		t.Fatal("expected invalid max_env_index candidate rejection")
	}
	if !strings.Contains(err.Error(), "max_env_index") {
		t.Fatalf("error %q must name max_env_index", err.Error())
	}
}

// TestCredentialReload_FailedCandidateKeepsActiveGeneration proves reload
// isolation for credential failures: publishing a good generation, then
// failing a bad-policy candidate, leaves the active generation untouched.
func TestCredentialReload_FailedCandidateKeepsActiveGeneration(t *testing.T) {
	env := credentialProbeEnv{values: map[string]string{"OPENAI_API_KEY": "some-key"}}
	good := credentialProbeConfig(0)
	ps, _ := credentialProbeProcess(t, good, env)

	goodGen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: good,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("good CompileGeneration: %v", err)
	}
	mgr := runtimehost.NewManager(4, nil)
	published := mgr.PrepareRequestPlane("credential-good", goodGen)
	if err := mgr.Publish(published); err != nil {
		t.Fatalf("publish: %v", err)
	}
	t.Cleanup(func() { _ = mgr.ShutdownDetached(context.Background()) })

	bad := credentialProbeConfig(999)
	if _, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: bad,
		Compose:   stdhttp.ComposeStandardHTTP,
	}); err == nil {
		t.Fatal("expected bad-policy candidate rejection")
	}
	if mgr.Active() != published {
		t.Fatal("failed credential candidate replaced the active generation")
	}
	lease, ok := mgr.Acquire()
	if !ok {
		t.Fatal("Acquire active generation after failed candidate")
	}
	lease.Release()
}
