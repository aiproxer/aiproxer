package runtimebundle_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

const testSecureKey32 = "01234567890123456789012345678901"

func testRuntimeBundlePlugins() config.PluginsConfig {
	return config.PluginsConfig{
		Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
	}
}

func TestBuild_secureSession_alwaysWiresManager(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	ex := b.Executor()
	if ex.SecureSession == nil || ex.SessionDenialMapper == nil {
		t.Fatalf("expected secure-session wiring, mgr=%v mapper_set=%v", ex.SecureSession, ex.SessionDenialMapper != nil)
	}
	if !ex.SyntheticLocalPrincipal {
		t.Fatal("expected synthetic local principal on loopback + memory secure session defaults")
	}
	if runtimebundle.CandidateSecureSessionStore(b) == nil {
		t.Fatalf("expected Built.SecureSessionStore, got nil")
	}
}

func TestBuild_secureSessionMemory_rejectsShortConfiguredFingerprintKey(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{InMemory: true},
		SecureSession: config.SecureSessionConfig{
			Store:               "memory",
			TokenFingerprintKey: "short",
		},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err == nil || !strings.Contains(err.Error(), "token_fingerprint_key") {
		t.Fatalf("want token_fingerprint_key error, got %v", err)
	}
}

func TestBuild_nonLoopbackExplicitBindDisablesSyntheticLocalPrincipal(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{InMemory: true},
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "memory",
			TokenFingerprintKey: testSecureKey32,
		},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Auth:           runtimebundle.AuthOptions{RemoteDecider: &testkit.StubRemoteDecider{}},
	})
	if b.Executor().SyntheticLocalPrincipal {
		t.Fatal("non-loopback bind must not enable synthetic local principal")
	}
	if config.IsExplicitLoopbackListenAddress("0.0.0.0:8080") {
		t.Fatal("test precondition: fixture address must not be classified as explicit loopback")
	}
}

func TestBuild_nonLoopback_unauthenticatedExecuteSessionDenial(t *testing.T) {
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
		Access:     config.AccessConfig{Mode: "multi_user"},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind: "openai-responses", ID: "stub-be", Enabled: true, Config: empty,
			}},
		},
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "memory",
			TokenFingerprintKey: testSecureKey32,
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth:           runtimebundle.AuthOptions{RemoteDecider: &testkit.StubRemoteDecider{}},
	})
	if b.Executor().SyntheticLocalPrincipal {
		t.Fatal("non-loopback bind must not enable synthetic local principal")
	}
	ctx := context.Background()
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "noloop"},
		Route:   aipapi.RouteIntent{Selector: "stub-be:gpt-4o-mini"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, err := b.Executor().Execute(ctx, call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !aipapi.IsSessionDenial(err) {
		t.Fatalf("want session denial, got %T %v", err, err)
	}
}

func TestBuild_secureSessionMemory_wiresManagerDenialMapperAndStore(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: ":0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{InMemory: true},
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "memory",
			TokenFingerprintKey: testSecureKey32,
		},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	ex := b.Executor()
	if ex.SecureSession == nil {
		t.Fatal("expected secure session manager")
	}
	if ex.SyntheticLocalPrincipal {
		t.Fatal("non-loopback bind must not enable synthetic local principal")
	}
	if ex.SecureSessionRecorder == nil {
		t.Fatal("expected recorder wired for memory store (non-durable recording)")
	}
	if ex.SecureSessionRecordingMandatory {
		t.Fatal("expected recording not mandatory for memory when audit_durability is not durable")
	}
	if ex.SessionDenialMapper == nil {
		t.Fatal("expected SessionDenialMapper")
	}
	if runtimebundle.CandidateSecureSessionStore(b) == nil {
		t.Fatal("expected Built.SecureSessionStore for diagnostics when secure session enabled")
	}
}

func TestBuild_secureSessionSQLite_wiresRecorderAndMandatoryWhenAuditDurable(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "ss.db")
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    testRuntimeBundlePlugins(),
		Continuity: config.ContinuityConfig{InMemory: true},
		SecureSession: config.SecureSessionConfig{
			Enabled:             config.BoolPtr(true),
			Store:               "sqlite",
			SQLitePath:          dbPath,
			TokenFingerprintKey: testSecureKey32,
			AuditDurability:     "durable",
		},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	ex := b.Executor()
	if ex.SecureSessionRecorder == nil {
		t.Fatal("expected full recorder for sqlite")
	}
	if !ex.SecureSessionRecordingMandatory {
		t.Fatal("expected recording mandatory when audit_durability is durable")
	}
	if runtimebundle.CandidateSecureSessionStore(b) == nil {
		t.Fatal("expected CandidateRuntime.SecureSessionStore")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
