package runtimebundle_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	infraaux "github.com/aiproxer/aiproxer/internal/infra/auxiliary"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/reasoninghost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"gopkg.in/yaml.v3"
)

// canned compression config helpers
func reasoningCompressionYAML(t *testing.T, enabled bool, egressRef string) yaml.Node {
	t.Helper()
	raw := `
action: restore
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 24h
  max_turns_per_session: 10
  max_reasoning_bytes_per_turn: 100000
  max_session_bytes: 1000000
`
	if enabled {
		raw += `
compression:
  enabled: true
  mode: shadow
  route: test-route
  timeout: 5s
  max_input_tokens: 10000
  max_input_bytes: 100000
  max_output_tokens: 1000
  max_output_bytes: 100000
  max_surrogate_bytes: 50000
  min_source_bytes: 100
  min_saved_bytes: 50
  min_savings_ratio: 0.5
  max_pending_per_session: 10
  max_surrogate_bytes_per_session: 100000
  max_pending_total: 100
  max_surrogate_bytes_total: 1000000
  egress_policy_ref: ` + egressRef + `
`
	} else {
		raw += `
compression:
  enabled: false
`
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	return n
}

type allowEgress struct{ version string }

func (a allowEgress) Decide(_ context.Context, _ reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	return reasoninghost.EgressDecision{Action: reasoninghost.EgressAllow, PolicyVersion: a.version}, nil
}

type stubMatch struct{ redacted string }

func (s stubMatch) ScanBytes(_ context.Context, _ []byte) ([]secretguard.Finding, error) {
	return nil, nil
}

func (s stubMatch) ScanString(_ context.Context, _ string) ([]secretguard.Finding, error) {
	return nil, nil
}

func (s stubMatch) RedactBytes(_ context.Context, input []byte) ([]byte, []secretguard.Finding, error) {
	if s.redacted != "" {
		return []byte(strings.ReplaceAll(string(input), "SECRET", s.redacted)), nil, nil
	}
	return input, nil, nil
}

func (s stubMatch) RedactString(_ context.Context, input string) (string, []secretguard.Finding, error) {
	if s.redacted != "" {
		return strings.ReplaceAll(input, "SECRET", s.redacted), nil, nil
	}
	return input, nil, nil
}

type stubResolver struct{ m secretguard.Matcher }

func (s stubResolver) Resolve(_ context.Context) (secretguard.Matcher, error) { return s.m, nil }

type fixedRunner struct{ id string }

func (f fixedRunner) Execute(_ context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	text := "ok-" + f.id
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: text}, {Kind: aipapi.EventResponseFinished}}), nil
}

// 1. Standard factory enabled returns placeholder without error; disabled returns participants.
func TestReasoningPreservation_StandardFactoryPlaceholder(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	enabledNode := reasoningCompressionYAML(t, true, "test-allow")
	b, err := reg.BuildFeatureBundle(reasoningpreservation.ID, enabledNode)
	if err != nil {
		t.Fatalf("enabled factory should not error, got %v", err)
	}
	bTransforms := aipfeature.Get(b.PlaneSet, aipfeature.PlaneAttemptTransforms)
	bObservers := aipfeature.Get(b.PlaneSet, aipfeature.PlaneStreamObserverFactories)
	if len(bTransforms) != 0 || len(bObservers) != 0 {
		t.Fatalf("enabled placeholder should be empty, got %d transforms %d observers", len(bTransforms), len(bObservers))
	}
	disabledNode := reasoningCompressionYAML(t, false, "")
	b2, err := reg.BuildFeatureBundle(reasoningpreservation.ID, disabledNode)
	if err != nil {
		t.Fatalf("disabled factory: %v", err)
	}
	b2Transforms := aipfeature.Get(b2.PlaneSet, aipfeature.PlaneAttemptTransforms)
	b2Observers := aipfeature.Get(b2.PlaneSet, aipfeature.PlaneStreamObserverFactories)
	if len(b2Transforms) == 0 || len(b2Observers) == 0 {
		t.Fatalf("disabled should have participants")
	}
}

// 2. CompileGeneration with enabled config + real scheduler + injected policy/resolver builds participants and bound client executes.
func TestReasoningPreservation_CompileGeneration_BoundClientExecutes(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	node := reasoningCompressionYAML(t, true, "test-allow")
	cfg := &config.Config{
		Routing:     config.RoutingConfig{MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}},
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return fixedRunner{id: "proc"} }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })
	prod := runtimebundle.ProductionOptions{
		FeatureHostRegistrations: []featurehost.Registration{
			(&reasoninghost.Binding{
				EgressPolicies:  map[string]reasoninghost.EgressPolicy{"test-allow": allowEgress{version: "v1"}},
				MatcherResolver: stubResolver{m: stubMatch{redacted: "REDACTED"}},
			}).Registration(),
		},
	}
	opts := &runtimebundle.BuildOptions{PluginRegistry: reg, Production: prod}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{Cfg: cfg, Log: slog.Default(), Opts: opts, BackgroundAux: scheduler})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{Process: ps, Candidate: cfg, Compose: stdhttp.ComposeStandardHTTP})
	if err != nil {
		t.Fatalf("CompileGeneration with prerequisites should succeed, got %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	if bundle.Handler() == nil {
		t.Fatal("bundle handler nil")
	}
	// Bound client via generation runner must execute without ErrNotConfigured.
	genRunner := infraaux.NewGenerationExecutorRunner()
	bound := scheduler.BindRunner(genRunner)
	if _, ok := bound.(auxiliary.BackgroundPoller); !ok {
		t.Fatal("bound client should implement BackgroundPoller")
	}
	exec, ok := bundle.ExecutorView().(*runtime.Executor)
	if !ok || exec == nil {
		t.Fatal("executor view should be *runtime.Executor")
	}
	if exec.RuntimeSnapshot == nil {
		t.Fatal("executor extensions snapshot should not be nil")
	}
	factories := exec.RuntimeSnapshot.StreamObserverFactories()
	if len(factories) != 1 || factories[0] == nil || factories[0].ID() != reasoningpreservation.ID+"-observer" {
		t.Fatalf("expected 1 reasoning preservation stream observer factory, got %d factories", len(factories))
	}
	_, err = bound.SubmitCollect(context.Background(), auxiliary.Request{Call: &aipapi.Call{ID: "test"}}, auxiliary.SubmitOptions{CoalesceKey: "k1"})
	if err != nil && strings.Contains(err.Error(), "not configured") {
		t.Fatalf("bound SubmitCollect should not be ErrNotConfigured, got %v", err)
	}
	// Poll should be available (at least pending)
	if poller, ok := bound.(auxiliary.BackgroundPoller); ok {
		_, _ = poller.Poll(context.Background(), "nonexistent")
	}
}

// 3. Missing policy/resolver/background scheduler fail before serving.
func TestReasoningPreservation_MissingPrerequisitesFailClosed(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	node := reasoningCompressionYAML(t, true, "missing-ref")
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3}, Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins:     config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	baseScheduler, _ := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return fixedRunner{id: "x"} }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2, MaxResults: 5})
	t.Cleanup(func() { _ = baseScheduler.Close() })
	tests := []struct {
		name string
		ps   *runtimebundle.ProcessServices
	}{
		{"missing_policy", func() *runtimebundle.ProcessServices {
			prod := runtimebundle.ProductionOptions{
				FeatureHostRegistrations: []featurehost.Registration{
					(&reasoninghost.Binding{
						EgressPolicies:  map[string]reasoninghost.EgressPolicy{"other": allowEgress{version: "v1"}},
						MatcherResolver: stubResolver{m: stubMatch{}},
					}).Registration(),
				},
			}
			opts := &runtimebundle.BuildOptions{PluginRegistry: reg, Production: prod}
			ps, _ := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{Cfg: cfg, Log: slog.Default(), Opts: opts, BackgroundAux: baseScheduler})
			return ps
		}()},
		{"missing_resolver", func() *runtimebundle.ProcessServices {
			prod := runtimebundle.ProductionOptions{
				FeatureHostRegistrations: []featurehost.Registration{
					(&reasoninghost.Binding{
						EgressPolicies: map[string]reasoninghost.EgressPolicy{"missing-ref": allowEgress{version: "v1"}},
					}).Registration(),
				},
			}
			opts := &runtimebundle.BuildOptions{PluginRegistry: reg, Production: prod}
			ps, _ := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{Cfg: cfg, Log: slog.Default(), Opts: opts, BackgroundAux: baseScheduler})
			return ps
		}()},
		{"missing_scheduler", func() *runtimebundle.ProcessServices {
			prod := runtimebundle.ProductionOptions{
				FeatureHostRegistrations: []featurehost.Registration{
					(&reasoninghost.Binding{
						EgressPolicies:  map[string]reasoninghost.EgressPolicy{"missing-ref": allowEgress{version: "v1"}},
						MatcherResolver: stubResolver{m: stubMatch{}},
					}).Registration(),
				},
			}
			opts := &runtimebundle.BuildOptions{PluginRegistry: reg, Production: prod}
			ps, _ := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{Cfg: cfg, Log: slog.Default(), Opts: opts})
			// ProcessServices always creates a default scheduler; simulate missing by niling
			ps.BackgroundAux = nil
			return ps
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { _ = tc.ps.Close() })
			_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{Process: tc.ps, Candidate: cfg, Compose: stdhttp.ComposeStandardHTTP})
			if err == nil {
				t.Fatalf("expected fail closed for %s", tc.name)
			}
			if !strings.Contains(err.Error(), "reasoningpreservation") {
				t.Fatalf("expected reasoningpreservation error, got %v", err)
			}
		})
	}
}

// 4. Reload: two generations use distinct runner captures while shared scheduler remains process-owned.
func TestReasoningPreservation_ReloadDistinctRunners(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	node := reasoningCompressionYAML(t, true, "test-allow")
	baseCfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3}, Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins:     config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}},
	}
	if err := config.Validate(baseCfg); err != nil {
		t.Fatal(err)
	}
	scheduler, _ := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return fixedRunner{id: "shared"} }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	t.Cleanup(func() { _ = scheduler.Close() })
	prod := runtimebundle.ProductionOptions{
		FeatureHostRegistrations: []featurehost.Registration{
			(&reasoninghost.Binding{
				EgressPolicies:  map[string]reasoninghost.EgressPolicy{"test-allow": allowEgress{version: "v1"}},
				MatcherResolver: stubResolver{m: stubMatch{}},
			}).Registration(),
		},
	}
	opts := &runtimebundle.BuildOptions{PluginRegistry: reg, Production: prod}
	ps, _ := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{Cfg: baseCfg, Log: slog.Default(), Opts: opts, BackgroundAux: scheduler})
	t.Cleanup(func() { _ = ps.Close() })
	bundleA, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{Process: ps, Candidate: baseCfg, Compose: stdhttp.ComposeStandardHTTP})
	if err != nil {
		t.Fatalf("gen A: %v", err)
	}
	t.Cleanup(func() { _ = bundleA.Close() })
	bundleB, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{Process: ps, Candidate: baseCfg, Compose: stdhttp.ComposeStandardHTTP})
	if err != nil {
		t.Fatalf("gen B: %v", err)
	}
	t.Cleanup(func() { _ = bundleB.Close() })
	if bundleA.ExecutorView() == bundleB.ExecutorView() {
		t.Fatal("expected distinct executor views across reloads")
	}
	if ps.BackgroundAux != scheduler {
		t.Fatal("scheduler should remain process-owned")
	}
}

// 5. Resolver sanitizer resolves per-context.
func TestReasoningPreservation_ResolverSanitizerPerContext(t *testing.T) {
	t.Parallel()
	type ctxKey string
	resolver := perContextResolver{key: ctxKey("matcher")}
	san := reasoningpreservation.NewResolverSanitizer(resolver)
	if san == nil {
		t.Fatal("sanitizer nil")
	}
	mA := stubMatch{redacted: "REDACTED_A"}
	mB := stubMatch{redacted: "REDACTED_B"}
	ctxA := context.WithValue(context.Background(), ctxKey("matcher"), mA)
	ctxB := context.WithValue(context.Background(), ctxKey("matcher"), mB)
	outA, err := san.SanitizeText(ctxA, "has SECRET here")
	if err != nil || !strings.Contains(outA, "REDACTED_A") {
		t.Fatalf("ctxA sanitize %q err %v", outA, err)
	}
	outB, err := san.SanitizeText(ctxB, "has SECRET here")
	if err != nil || !strings.Contains(outB, "REDACTED_B") {
		t.Fatalf("ctxB sanitize %q err %v", outB, err)
	}
	outEmpty, err := san.SanitizeText(context.Background(), "has SECRET here")
	if err == nil || outEmpty != "" {
		t.Fatalf("empty ctx should fail closed, got %q err %v", outEmpty, err)
	}
}

type perContextResolver struct{ key any }

func (p perContextResolver) Resolve(ctx context.Context) (secretguard.Matcher, error) {
	if v := ctx.Value(p.key); v != nil {
		if m, ok := v.(secretguard.Matcher); ok {
			return m, nil
		}
	}
	return nil, nil
}

type stubStreamObsFactory struct{ id string }

func (f stubStreamObsFactory) ID() string                      { return f.id }
func (stubStreamObsFactory) Order() int                        { return 0 }
func (stubStreamObsFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubStreamObsFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

// 6. Ordinary stream observer factories along with compression are preserved in order.
func TestReasoningPreservation_OrdinaryStreamObserversPreservedWithCompression(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	node := reasoningCompressionYAML(t, true, "test-allow")
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3}, Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins: config.PluginsConfig{
			Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}},
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return fixedRunner{id: "proc"} }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })
	prod := runtimebundle.ProductionOptions{
		FeatureHostRegistrations: []featurehost.Registration{
			(&reasoninghost.Binding{
				EgressPolicies:  map[string]reasoninghost.EgressPolicy{"test-allow": allowEgress{version: "v1"}},
				MatcherResolver: stubResolver{m: stubMatch{redacted: "REDACTED"}},
			}).Registration(),
		},
	}
	opts := &runtimebundle.BuildOptions{PluginRegistry: reg, Production: prod}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{Cfg: cfg, Log: slog.Default(), Opts: opts, BackgroundAux: scheduler})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	ordinaryFactories := []response.StreamObserverFactory{
		stubStreamObsFactory{id: "ordinary-obs-1"},
		stubStreamObsFactory{id: "ordinary-obs-2"},
	}
	err = reg.RegisterFeature("ordinary-features", func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, "ordinary-features", func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "ordinary-features", ordinaryFactories)
		}, nil), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Plugins.Features = append(cfg.Plugins.Features, config.PluginConfig{ID: "ordinary-features", Enabled: true})

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	exec, ok := bundle.ExecutorView().(*runtime.Executor)
	if !ok || exec == nil {
		t.Fatal("executor view should be *runtime.Executor")
	}
	if exec.RuntimeSnapshot == nil {
		t.Fatal("executor extensions snapshot should not be nil")
	}
	factories := exec.RuntimeSnapshot.StreamObserverFactories()
	if len(factories) != 3 {
		t.Fatalf("expected 3 stream observer factories, got %d", len(factories))
	}
	expectedIDs := []string{"ordinary-obs-1", "ordinary-obs-2", reasoningpreservation.ID + "-observer"}
	for i, f := range factories {
		if f == nil {
			t.Fatalf("factory %d is nil", i)
		}
		if f.ID() != expectedIDs[i] {
			t.Errorf("factory %d ID = %q, want %q", i, f.ID(), expectedIDs[i])
		}
	}
}
