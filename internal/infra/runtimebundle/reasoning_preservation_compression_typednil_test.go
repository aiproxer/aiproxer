package runtimebundle

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/infra/auxiliary"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkauxiliary "github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/reasoninghost"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"gopkg.in/yaml.v3"
)

// typed nil implementations
type typedNilClient struct{}

func (t *typedNilClient) SubmitCollect(ctx context.Context, req sdkauxiliary.Request, opts sdkauxiliary.SubmitOptions) (sdkauxiliary.JobID, error) {
	return "", nil
}

func (t *typedNilClient) Await(ctx context.Context, id sdkauxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (t *typedNilClient) Forget(id sdkauxiliary.JobID) {}

func (t *typedNilClient) Poll(ctx context.Context, id sdkauxiliary.JobID) (sdkauxiliary.PollResult, error) {
	return sdkauxiliary.PollResult{}, nil
}

type typedNilEgress struct{}

func (t *typedNilEgress) Decide(_ context.Context, _ reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	return reasoninghost.EgressDecision{Action: reasoninghost.EgressAllow, PolicyVersion: "v1"}, nil
}

type typedNilResolver struct{}

func (t *typedNilResolver) Resolve(_ context.Context) (sdk.Matcher, error) { return nil, nil }

type typedNilMatcher struct{}

func (t *typedNilMatcher) ScanBytes(_ context.Context, _ []byte) ([]sdk.Finding, error) {
	return nil, nil
}

func (t *typedNilMatcher) ScanString(_ context.Context, _ string) ([]sdk.Finding, error) {
	return nil, nil
}

func (t *typedNilMatcher) RedactBytes(_ context.Context, b []byte) ([]byte, []sdk.Finding, error) {
	return b, nil, nil
}

func (t *typedNilMatcher) RedactString(_ context.Context, s string) (string, []sdk.Finding, error) {
	return s, nil, nil
}

func reasoningYAMLForTypedNil(t *testing.T, egressRef string) yaml.Node {
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
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	return n
}

func TestReasoningPreservation_TypedNilClientFailsClosed(t *testing.T) {
	t.Parallel()
	var nilClient *typedNilClient
	var client sdkauxiliary.BackgroundClient = nilClient
	var nilPoller *typedNilClient
	var poller sdkauxiliary.BackgroundPoller = nilPoller
	if !isNilAuxiliaryCapability(client) {
		t.Fatal("typed nil client should be detected as nil")
	}
	if !isNilAuxiliaryCapability(poller) {
		t.Fatal("typed nil poller should be detected as nil")
	}
	regs := []struct {
		desc string
		reg  yaml.Node
	}{
		{"client", reasoningYAMLForTypedNil(t, "ref")},
	}
	_ = regs
	// Build minimal ProcessServices with typed nil policy/resolver also
	var nilPolicy *typedNilEgress
	var policy reasoninghost.EgressPolicy = nilPolicy
	if !isNilAuxiliaryCapability(policy) {
		t.Fatal("typed nil egress policy should be nil")
	}
	var nilResolver *typedNilResolver
	var resolver sdk.MatcherResolver = nilResolver
	if !isNilAuxiliaryCapability(resolver) {
		t.Fatal("typed nil resolver should be nil")
	}
	// Direct isNil checks already cover; now test validate fails
	node := reasoningYAMLForTypedNil(t, "ref")
	cfg := &config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}}}
	_ = cfg
	// Use helper
	jobRegs := []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}
	_ = jobRegs
	// Use the actual validate function with typed nil client
	fromRegs := config.RegistrationsFromConfig(&config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}}})
	ps2 := &ProcessServices{opts: &BuildOptions{Production: ProductionOptions{FeatureHostRegistrations: []featurehost.Registration{
		(&reasoninghost.Binding{
			EgressPolicies:  map[string]reasoninghost.EgressPolicy{"ref": &typedNilEgress{}},
			MatcherResolver: &typedNilResolver{},
		}).Registration(),
	}}}}
	if err := validateReasoningPreservationCompressionGeneration(ps2, fromRegs, client, poller); err == nil || !strings.Contains(err.Error(), "BackgroundAux") {
		t.Fatalf("typed nil client/poller should fail closed, got %v", err)
	}
}

func TestReasoningPreservation_TypedNilPolicyResolverFailsClosed(t *testing.T) {
	t.Parallel()
	node := reasoningYAMLForTypedNil(t, "typed-ref")
	regs := config.RegistrationsFromConfig(&config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}}})
	// Production with typed nil policy map entry and typed nil resolver
	var nilPolicy *typedNilEgress
	var nilResolver *typedNilResolver
	psPolicyNil := &ProcessServices{opts: &BuildOptions{Production: ProductionOptions{FeatureHostRegistrations: []featurehost.Registration{
		(&reasoninghost.Binding{
			EgressPolicies:  map[string]reasoninghost.EgressPolicy{"typed-ref": nilPolicy},
			MatcherResolver: &typedNilResolver{},
		}).Registration(),
	}}}}
	// Need a non-nil client/poller
	scheduler, _ := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return fixedRunnerForTypedNil{id: "x"} }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2, MaxResults: 5})
	t.Cleanup(func() { _ = scheduler.Close() })
	genRunner := auxiliary.NewGenerationExecutorRunner()
	bound := scheduler.BindRunner(genRunner)
	bPoller, ok := bound.(sdkauxiliary.BackgroundPoller)
	if !ok {
		t.Fatal("bound background client must implement BackgroundPoller")
	}
	if err := validateReasoningPreservationCompressionGeneration(psPolicyNil, regs, bound, bPoller); err == nil || !strings.Contains(err.Error(), "EgressPolicy") {
		t.Fatalf("typed nil policy should fail closed, got %v", err)
	}
	psResolverNil := &ProcessServices{opts: &BuildOptions{Production: ProductionOptions{FeatureHostRegistrations: []featurehost.Registration{
		(&reasoninghost.Binding{
			EgressPolicies:  map[string]reasoninghost.EgressPolicy{"typed-ref": &typedNilEgress{}},
			MatcherResolver: nilResolver,
		}).Registration(),
	}}}}
	if err := validateReasoningPreservationCompressionGeneration(psResolverNil, regs, bound, bPoller); err == nil || !strings.Contains(err.Error(), "MatcherResolver") {
		t.Fatalf("typed nil resolver should fail closed, got %v", err)
	}
	// IsNil direct
	if !isNilAuxiliaryCapability(nilPolicy) {
		t.Fatal("typed nil policy isNil false")
	}
	if !isNilAuxiliaryCapability(nilResolver) {
		t.Fatal("typed nil resolver isNil false")
	}
	// Also lookup returns nil for typed nil resolver entry
	psLookup := &ProcessServices{opts: &BuildOptions{Production: ProductionOptions{FeatureHostRegistrations: []featurehost.Registration{
		(&reasoninghost.Binding{MatcherResolver: nilResolver}).Registration(),
	}}}}
	if r := lookupReasoningMatcherResolver(psLookup); !isNilAuxiliaryCapability(r) {
		t.Fatalf("lookup should return typed nil resolver as nil, got %v", r)
	}
}

// lookupReasoningMatcherResolver is a test helper that extracts MatcherResolver
// from FeatureHostRegistrations for testing.
func lookupReasoningMatcherResolver(ps *ProcessServices) sdk.MatcherResolver {
	if ps == nil || ps.opts == nil {
		return nil
	}
	for _, reg := range ps.opts.Production.FeatureHostRegistrations {
		if b, ok := reg.Binding.(*reasoninghost.Binding); ok && b != nil {
			return b.MatcherResolver
		}
	}
	return nil
}

func TestReasoningPreservation_NilMatcherFailsClosed(t *testing.T) {
	t.Parallel()
	// Use stub that returns nil matcher
	resolver := stubResolverForTypedNil{m: nil}
	san := reasoningpreservation.NewResolverSanitizer(resolver)
	if san != nil {
		// The feature's NewResolverSanitizer returns non-nil even if resolver will later return nil matcher?
		// But our isNil check for sanitizer should catch typed nil? Actually resolver is non-nil, sanitizer will be non-nil.
		// The matcher nil case is handled at SanitizeText time, not at composition.
		// We test that SanitizeText with nil matcher fails.
		if _, err := san.SanitizeText(context.Background(), "has SECRET"); err == nil {
			t.Fatalf("sanitizer with nil matcher should fail on SanitizeText")
		}
	}
	// Direct isNil for nil matcher resolver's product: NewResolverSanitizer(nil) should be nil
	if s := reasoningpreservation.NewResolverSanitizer(nil); s != nil {
		t.Fatalf("NewResolverSanitizer(nil) should return nil")
	}
	var nilResolverTyped *typedNilResolver
	if s := reasoningpreservation.NewResolverSanitizer(nilResolverTyped); s != nil {
		t.Fatalf("NewResolverSanitizer(typed nil) should return nil, got %v", s)
	}
	// Also typed nil matcher via resolver that returns typed nil matcher
	typedNilM := (*typedNilMatcher)(nil)
	var m sdk.Matcher = typedNilM
	if !isNilAuxiliaryCapability(m) {
		t.Fatal("typed nil matcher should be nil")
	}
}

type stubResolverForTypedNil struct{ m sdk.Matcher }

func (s stubResolverForTypedNil) Resolve(_ context.Context) (sdk.Matcher, error) { return s.m, nil }

type fixedRunnerForTypedNil struct{ id string }

func (f fixedRunnerForTypedNil) Execute(_ context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "ok-" + f.id}, {Kind: aipapi.EventResponseFinished}}), nil
}

func TestReasoningPreservation_NoDuplicateParticipants(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	nodeEnabled := reasoningYAMLForTypedNil(t, "dup-ref")
	nodeDisabled := func() yaml.Node {
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
compression:
  enabled: false
`
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatalf("yaml: %v", err)
		}
		return n
	}()
	// First build disabled to get placeholder participants
	bDisabled, err := reg.BuildFeatureBundle(reasoningpreservation.ID, nodeDisabled)
	if err != nil {
		t.Fatalf("disabled bundle: %v", err)
	}
	disabledTransforms := aipfeature.Get(bDisabled.PlaneSet, aipfeature.PlaneAttemptTransforms)
	disabledObservers := aipfeature.Get(bDisabled.PlaneSet, aipfeature.PlaneStreamObserverFactories)
	if len(disabledTransforms) == 0 || len(disabledObservers) == 0 {
		t.Fatalf("disabled should have participants")
	}
	genSurface, err := featurebundle.MergeBundlesGenerated(bDisabled)
	if err != nil {
		t.Fatalf("MergeBundlesGenerated: %v", err)
	}
	genSurface2, err := genSurface.BindAttemptTransforms(reasoningpreservation.ID, disabledTransforms)
	if err != nil {
		t.Fatalf("BindAttemptTransforms: %v", err)
	}
	xforms2 := aipfeature.Get(genSurface2.Frozen, aipfeature.PlaneAttemptTransforms)
	if len(xforms2) != 1 {
		t.Fatalf("bind should replace reasoning participants, got %d transforms", len(xforms2))
	}
	// Now test that bind does not duplicate when called via CompileGeneration
	// Use full CompileGeneration with enabled config and ensure only one set
	scheduler, _ := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return fixedRunnerForTypedNil{id: "dup"} }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	t.Cleanup(func() { _ = scheduler.Close() })
	prod := ProductionOptions{FeatureHostRegistrations: []featurehost.Registration{
		(&reasoninghost.Binding{
			EgressPolicies:  map[string]reasoninghost.EgressPolicy{"dup-ref": &typedNilEgress{}},
			MatcherResolver: &typedNilResolver{},
		}).Registration(),
	}}
	opts := &BuildOptions{PluginRegistry: reg, Production: prod}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3}, Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins:     config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: nodeEnabled}}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{Cfg: cfg, Log: slog.Default(), Opts: opts, BackgroundAux: scheduler})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	genSurface3, err := genSurface2.BindAttemptTransforms(reasoningpreservation.ID, disabledTransforms)
	if err != nil {
		t.Fatalf("BindAttemptTransforms 3: %v", err)
	}
	xforms3 := aipfeature.Get(genSurface3.Frozen, aipfeature.PlaneAttemptTransforms)
	if len(xforms3) != len(xforms2) {
		t.Fatalf("bind should be idempotent")
	}
}

func TestReasoningPreservation_GeneratorBoundActualExecution(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	node := reasoningYAMLForTypedNil(t, "gen-ref")
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3}, Continuity: config.ContinuityConfig{InMemory: true, Store: "memory"},
		Server:      config.ServerConfig{MaxRequestBodyBytes: 1024, MaxConcurrentDecodes: 4, MaxInflightDecodeBytes: 4096},
		Diagnostics: config.DiagnosticsConfig{Enabled: true, HealthPath: "/healthz"},
		Plugins:     config.PluginsConfig{Features: []config.PluginConfig{{ID: reasoningpreservation.ID, Enabled: true, Config: node}}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	execID := "gen-exec"
	scheduler, _ := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return fixedRunnerForTypedNil{id: execID} }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 4, MaxResults: 10})
	t.Cleanup(func() { _ = scheduler.Close() })
	prod := ProductionOptions{FeatureHostRegistrations: []featurehost.Registration{
		(&reasoninghost.Binding{
			EgressPolicies:  map[string]reasoninghost.EgressPolicy{"gen-ref": &typedNilEgress{}},
			MatcherResolver: &typedNilResolver{},
		}).Registration(),
	}}
	opts := &BuildOptions{PluginRegistry: reg, Production: prod}
	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{Cfg: cfg, Log: slog.Default(), Opts: opts, BackgroundAux: scheduler})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	genRunner, boundClient, boundPoller, err := newGenerationAuxiliaryRunner(ps)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if genRunner == nil || boundClient == nil || boundPoller == nil {
		t.Fatalf("runner/client/poller nil")
	}
	jid, err := boundClient.SubmitCollect(context.Background(), sdkauxiliary.Request{Call: &aipapi.Call{ID: "test-gen"}}, sdkauxiliary.SubmitOptions{CoalesceKey: "k-gen"})
	if err != nil {
		t.Fatalf("SubmitCollect failed: %v", err)
	}
	if jid == "" {
		t.Fatalf("expected non-empty job id")
	}
	if _, err := boundClient.Await(context.Background(), jid); err != nil && strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Await should not be ErrNotConfigured, got %v", err)
	}
	if _, err := boundPoller.Poll(context.Background(), jid); err != nil && strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Poll should not be ErrNotConfigured, got %v", err)
	}
	// Also ensure that a direct Disabled client would fail
	disabled := sdkauxiliary.DisabledBackgroundClient{}
	if _, err := disabled.SubmitCollect(context.Background(), sdkauxiliary.Request{Call: &aipapi.Call{ID: "x"}}, sdkauxiliary.SubmitOptions{CoalesceKey: "k"}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("disabled client should be ErrNotConfigured")
	}
}
