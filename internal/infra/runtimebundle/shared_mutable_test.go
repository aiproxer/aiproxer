package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func TestSharedMutable_IdentitySurvivesTwoCandidateCompiles(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	cfg.Routing.Health.CircuitBreaker = config.CircuitBreakerConfig{
		Enabled:          true,
		FailureThreshold: 2,
		OpenFor:          "30s",
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	if ps.ALegLifecycle == nil {
		t.Fatal("expected process ALegLifecycle")
	}
	if ps.AffinityStore == nil {
		t.Fatal("expected process AffinityStore")
	}
	if ps.CandidateHealth == nil {
		t.Fatal("expected process CandidateHealth")
	}
	if ps.ExtensionState == nil {
		t.Fatal("expected process ExtensionState")
	}

	c1, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate #1: %v", err)
	}
	c2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate #2: %v", err)
	}

	if c1.Executor().ALegLifecycle != ps.ALegLifecycle || c2.Executor().ALegLifecycle != ps.ALegLifecycle {
		t.Fatal("candidates must reuse process ALegLifecycle identity")
	}
	if c1.Executor().AffinityStore == nil || c2.Executor().AffinityStore == nil {
		t.Fatal("expected affinity stores on executors")
	}
	if c1.RuntimeSnapshot().State() != ps.ExtensionState || c2.RuntimeSnapshot().State() != ps.ExtensionState {
		t.Fatal("candidates must reuse process ExtensionState identity")
	}
	if c1.DecodeAdmission() != ps.DecodeAdmission || c2.DecodeAdmission() != ps.DecodeAdmission {
		t.Fatal("candidates must reuse process DecodeAdmission")
	}
	if c1.Store() != ps.Continuity || c2.Store() != ps.Continuity {
		t.Fatal("candidates must reuse process Continuity")
	}
	if runtimebundle.CandidateSecureSessionStore(c1) != ps.SecureSessions || runtimebundle.CandidateSecureSessionStore(c2) != ps.SecureSessions {
		t.Fatal("candidates must reuse process SecureSessions")
	}

	key := affinity.Key{Scope: affinity.ScopeSession, ID: "shared-sess"}
	if err := c1.Executor().AffinityStore.Set(context.Background(), affinity.Binding{
		Key: key, BackendID: "openai-responses", CandidateKey: "openai-responses:m",
	}); err != nil {
		t.Fatalf("affinity set via c1: %v", err)
	}
	b, ok, err := c2.Executor().AffinityStore.Get(context.Background(), key)
	if err != nil || !ok || b.BackendID != "openai-responses" {
		t.Fatalf("affinity identity must survive second candidate: ok=%v backend=%q err=%v", ok, b.BackendID, err)
	}

	if sink, ok := c1.Executor().CandidateHealth.(interface {
		OnRoutingAttemptOutcome(string, aipapi.AttemptOutcome)
	}); ok {
		sink.OnRoutingAttemptOutcome("openai-responses:m", aipapi.AttemptSurfacedFailure)
		sink.OnRoutingAttemptOutcome("openai-responses:m", aipapi.AttemptSurfacedFailure)
	}
	u1 := c1.Executor().CandidateHealth.UnhealthyCandidateKeys()
	u2 := c2.Executor().CandidateHealth.UnhealthyCandidateKeys()
	if _, hit := u1["openai-responses:m"]; !hit {
		t.Fatalf("expected unhealthy key on c1, got %v", u1)
	}
	if _, hit := u2["openai-responses:m"]; !hit {
		t.Fatalf("health observation identity must survive second candidate, got %v", u2)
	}

	_ = c1.Close()
	_ = c2.Close()
	if ps.Closed() {
		t.Fatal("candidate close must not close process services")
	}
}

func TestSharedMutable_ChangedBackendIdentityIsolatesStaleState(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	cfg.Plugins.Backends = []config.PluginConfig{{
		ID: "stub", Kind: "openai-responses", Enabled: false,
		Config: mustYAMLMapNode(t, map[string]any{"base_url": "http://old.example"}),
	}}
	cfg.Routing.Health.CircuitBreaker = config.CircuitBreakerConfig{
		Enabled: true, FailureThreshold: 1, OpenFor: "1m",
	}

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	c1, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate #1: %v", err)
	}

	key := affinity.Key{Scope: affinity.ScopeClient, ID: "user-1"}
	if err := c1.Executor().AffinityStore.Set(context.Background(), affinity.Binding{
		Key: key, BackendID: "stub", CandidateKey: "stub:m",
	}); err != nil {
		t.Fatal(err)
	}
	if sink, ok := c1.Executor().CandidateHealth.(interface {
		OnRoutingAttemptOutcome(string, aipapi.AttemptOutcome)
	}); ok {
		sink.OnRoutingAttemptOutcome("stub:m", aipapi.AttemptSurfacedFailure)
	}

	// Material config change → fresh state namespace for stub.
	cfg.Plugins.Backends[0].Config = mustYAMLMapNode(t, map[string]any{"base_url": "http://new.example"})

	c2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("CompileCandidate #2: %v", err)
	}

	if _, ok, err := c2.Executor().AffinityStore.Get(context.Background(), key); err != nil || ok {
		t.Fatalf("changed identity must not select stale affinity: ok=%v err=%v", ok, err)
	}
	if u := c2.Executor().CandidateHealth.UnhealthyCandidateKeys(); u != nil {
		if _, hit := u["stub:m"]; hit {
			t.Fatalf("changed identity must not surface stale health: %v", u)
		}
	}
	// Prior generation view still sees its own compatible namespace.
	if b, ok, err := c1.Executor().AffinityStore.Get(context.Background(), key); err != nil || !ok || b.BackendID != "stub" {
		t.Fatalf("prior candidate view must retain compatible affinity: ok=%v backend=%q err=%v", ok, b.BackendID, err)
	}

	_ = c1.Close()
	_ = c2.Close()
}

func TestSharedMutable_StaleAbsentBackendNotSelected(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	cfg.Plugins.Backends = []config.PluginConfig{
		{ID: "keep", Kind: "openai-responses", Enabled: false},
		{ID: "gone", Kind: "openai-responses", Enabled: false},
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	c1, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	key := affinity.Key{Scope: affinity.ScopeSession, ID: "s-gone"}
	if err := c1.Executor().AffinityStore.Set(context.Background(), affinity.Binding{
		Key: key, BackendID: "gone", CandidateKey: "gone:m",
	}); err != nil {
		t.Fatal(err)
	}

	cfg.Plugins.Backends = []config.PluginConfig{
		{ID: "keep", Kind: "openai-responses", Enabled: false},
	}
	c2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c2.Executor().AffinityStore.Get(context.Background(), key); err != nil || ok {
		t.Fatalf("affinity for absent backend must not be selected: ok=%v err=%v", ok, err)
	}
	_ = c1.Close()
	_ = c2.Close()
}

func TestSharedMutable_CandidateFailureDoesNotCloseSharedState(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	aleg := ps.ALegLifecycle
	aff := ps.AffinityStore
	decode := ps.DecodeAdmission

	bad := processServicesTestConfig()
	bad.Plugins.Backends = []config.PluginConfig{{
		ID: "missing-factory-kind", Kind: "definitely-not-registered", Enabled: true,
	}}

	_, err = runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: bad, Bus: hooks.New(hooks.Config{}),
	})
	if err == nil {
		t.Fatal("expected candidate compile failure")
	}
	if ps.Closed() {
		t.Fatal("failed candidate must not close ProcessServices")
	}
	if ps.ALegLifecycle != aleg || ps.AffinityStore != aff || ps.DecodeAdmission != decode {
		t.Fatal("failed candidate must not replace process shared identities")
	}
	if ps.ExtensionState == nil {
		t.Fatal("extension state must survive candidate failure")
	}

	c, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cfg, Bus: hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatalf("recover compile: %v", err)
	}
	if c.Executor().ALegLifecycle != aleg {
		t.Fatal("process A-leg identity must remain usable after failed candidate")
	}
	_ = c.Close()
}

func TestSharedMutable_MeteringAccountingStoresSharedAcrossCandidates(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	cfg.Accounting.Enabled = true
	cfg.Accounting.Mode = "local_only"
	cfg.Accounting.Ledger.Store = "memory"
	cfg.Metering.Enabled = true
	cfg.Metering.Journal.Store = "memory"

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	if ps.MeteringRecorder == nil {
		t.Fatal("expected process metering recorder")
	}

	c1, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtimebundle.CandidateTokenAccountingAdmin(c1) == nil && cfg.Accounting.Admin.Enabled {
		t.Fatal("expected token accounting admin when enabled")
	}
	_ = c1.Close()
	_ = c2.Close()
	if ps.Closed() {
		t.Fatal("candidate close must not close process metering/accounting stores")
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("process close: %v", err)
	}
}

func TestBackendStateIdentity_CompatDigestChangesWithMaterialConfig(t *testing.T) {
	t.Parallel()

	a := config.PluginConfig{
		ID: "x", Kind: "openai-responses", Enabled: true,
		Config: mustYAMLMapNode(t, map[string]any{"base_url": "http://a"}),
	}
	b := config.PluginConfig{
		ID: "x", Kind: "openai-responses", Enabled: true,
		Config: mustYAMLMapNode(t, map[string]any{"base_url": "http://b"}),
	}
	ka, err := runtimebundle.BackendStateIdentityFromPlugin(a)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := runtimebundle.BackendStateIdentityFromPlugin(b)
	if err != nil {
		t.Fatal(err)
	}
	if ka.InstanceID != "x" || ka.FactoryKind != "openai-responses" {
		t.Fatalf("unexpected identity %+v", ka)
	}
	if ka.Compatible(kb) {
		t.Fatal("material config change must yield incompatible backend state identity")
	}
	ka2, err := runtimebundle.BackendStateIdentityFromPlugin(a)
	if err != nil {
		t.Fatal(err)
	}
	if !ka.Compatible(ka2) {
		t.Fatal("identical plugin config must be compatible")
	}
}

func TestProcessServices_ProcessCloseOnceAfterSharedHoist(t *testing.T) {
	t.Parallel()

	cfg := processServicesTestConfig()
	cfg.Metering.Enabled = true
	cfg.Metering.Journal.Store = "memory"
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Bus: hooks.New(hooks.Config{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ps.Close(); err != nil {
		t.Fatal(err)
	}
	if !ps.Closed() {
		t.Fatal("expected closed")
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
}

func mustYAMLMapNode(t *testing.T, v any) yaml.Node {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		t.Fatal(err)
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		return *n.Content[0]
	}
	return n
}
