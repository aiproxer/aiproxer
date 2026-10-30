package runtimebundle_test

// The composed enforcement proof of task 2.2 (design §2 and §3;
// Requirements 1.4 and 1.5): the production composition root binds the
// feature-owned target compiler from the generation's executor, the
// proxy-memory feature checks its per-phase policy against the final compiled
// actual target, and the phase is dispatched only when that target is
// covered.
//
// The proof runs the real path: the production candidate compile, the real
// feature worker, the real process background scheduler bound to the real
// generation executor runner, the real generic-core runtime executor and a
// scripted provider. Only the provider's content is stubbed. A phase that
// the policy refuses never reaches the provider, so the provider's open
// count is the direct evidence that the refusal happens before the dispatch.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"github.com/uptrace/bun"
)

// proxyMemoryPermittedInventory is the allowed inventory of a phase whose
// operator's route resolves to the "openai:openai/test-model" target: the
// inventory names the target the alias resolves to, so the compiled actual
// target is covered.
const proxyMemoryPermittedInventory = `
      backends: [openai]
      models: [openai/test-model]
      targets:
        - backend: openai
          model: openai/test-model
          reasoning_efforts: [low, medium, high]`

// proxyMemoryUncoveredInventory is the allowed inventory of a phase whose
// operator named the alias instead of the target it resolves to: the
// compiled actual target is outside the inventory, so the phase is refused.
const proxyMemoryUncoveredInventory = `
      backends: [openai]
      models: [gpt5]
      targets:
        - backend: openai
          model: gpt5
          reasoning_efforts: [low, medium, high]`

// proxyMemoryMultiLeafInventory is the allowed inventory of a phase whose
// operator's alias resolves to two leaves: both leaves are the operator's
// own targets, so the phase's policy covers the whole reference set.
const proxyMemoryMultiLeafInventory = `
      backends: [openai]
      models: [openai/test-model, openai/alternate]
      targets:
        - backend: openai
          model: openai/test-model
          reasoning_efforts: [low, medium, high]
        - backend: openai
          model: openai/alternate
          reasoning_efforts: [low, medium, high]`

// proxyMemoryCountingResolver counts the capability resolutions of one
// generation.
//
// The capability resolution is the call an executable connector serves with
// its ResolveProfile, so the count is the operation count at the process
// boundary.
type proxyMemoryCountingResolver struct {
	inner capabilities.Resolver
	mu    sync.Mutex
	calls int
	// withoutOptions counts the resolutions made for a call that carries no
	// reasoning effort. The operator's locked reference is compiled with a
	// content-free synthetic call, so a resolution for such a call would be
	// the reference resolving a capability.
	withoutOptions int
}

func (r *proxyMemoryCountingResolver) DescribeCandidate(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
	r.mu.Lock()
	r.calls++
	if call.Options.ReasoningEffort == "" {
		r.withoutOptions++
	}
	r.mu.Unlock()
	if r.inner == nil {
		return aipapi.BackendCaps{}
	}
	return r.inner.DescribeCandidate(ctx, cand, call)
}

func (r *proxyMemoryCountingResolver) counts() (calls, withoutOptions int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.withoutOptions
}

// proxyMemoryPolicySink records the terminal call and leg records of one
// composed run: the recorded usage names the target that served the call.
type proxyMemoryPolicySink struct {
	mu    sync.Mutex
	calls []usageevidence.CallUsageRecord
	legs  []usageevidence.CallLegUsageRecord
}

func (s *proxyMemoryPolicySink) AppendCall(_ context.Context, record usageevidence.CallUsageRecord) error {
	s.mu.Lock()
	s.calls = append(s.calls, record)
	s.mu.Unlock()
	return nil
}

func (s *proxyMemoryPolicySink) AppendLeg(_ context.Context, record usageevidence.CallLegUsageRecord) error {
	s.mu.Lock()
	s.legs = append(s.legs, record)
	s.mu.Unlock()
	return nil
}

func (s *proxyMemoryPolicySink) legsSnapshot() []usageevidence.CallLegUsageRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]usageevidence.CallLegUsageRecord(nil), s.legs...)
}

// proxyMemoryPolicyConfig builds the configuration of one generation with a
// declared model policy: the phase routes, the durable database and the
// allowed inventory the test names.
func proxyMemoryPolicyConfig(t *testing.T, memoryPath, continuityPath, inventory string) *config.Config {
	t.Helper()
	return proxyMemoryJ5Config(t, memoryPath, continuityPath, proxyMemoryJ5FirstGeneration(), inventory)
}

// proxyMemoryPolicyScript wires the scripted provider and the billing seams
// into one generation's executor. The scripted provider declares the
// capabilities it actually serves, and the terminal sink records the usage
// the served call produced.
func proxyMemoryPolicyScript(exec *runtime.Executor, backend *proxyMemoryPhaseBackend, instanceID string, sink *proxyMemoryPolicySink, reasoning bool) {
	exec.Backends = map[string]execbackend.Backend{instanceID: backend.Backend()}
	phaseCaps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityStructuredOutputs)
	if reasoning {
		phaseCaps = aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityStructuredOutputs, aipapi.CapabilityReasoning)
	}
	exec.CapsResolver = capabilities.MapResolver{instanceID: func(context.Context, routing.AttemptCandidate, aipapi.Call) aipapi.BackendCaps {
		return phaseCaps
	}}
	exec.BillingIdentity = runtime.BillingIdentity{
		AccountID: func(context.Context, aipapi.Call) string { return "acct:proxy-memory" },
		CustomerPricingRef: func(context.Context, aipapi.Call) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "pricing:proxy-memory", Version: "1"}
		},
		ChargePolicyRef: func(context.Context, aipapi.Call) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "policy:proxy-memory", Version: "1"}
		},
		OperatorRateRef: func(context.Context, string, string) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "operator:proxy-memory", Version: "1"}
		},
	}
	exec.BillingCreditGate = proxyMemoryBillingSeams{}
	exec.BillingExposureAdmission = proxyMemoryBillingSeams{}
	exec.TerminalUsageSink = sink
}

// proxyMemoryPolicyRun composes one generation with the given inventory,
// seeds one claimable session and drives the worker's tick. It returns the
// scripted provider, the terminal sink and the memory database handle.
func proxyMemoryPolicyRun(t *testing.T, inventory string, reasoning bool) (*proxyMemoryPhaseBackend, *proxyMemoryPolicySink, *bun.DB) {
	t.Helper()
	ctx := context.Background()
	memoryPath := filepath.Join(t.TempDir(), "memory.db")
	continuityPath := filepath.Join(t.TempDir(), "continuity.db")

	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))

	ps, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg:  proxyMemoryPolicyConfig(t, memoryPath, continuityPath, inventory),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	backend := &proxyMemoryPhaseBackend{}
	sink := &proxyMemoryPolicySink{}
	worker, exec, dispose, err := runtimebundle.ProxyMemoryGenerationForTest(ctx, runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: proxyMemoryPolicyConfig(t, memoryPath, continuityPath, inventory),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, dispose())
		if loops := proxyMemoryJ5LiveLoops(); loops != 0 {
			t.Errorf("the candidate generation left %d background loop(s) behind", loops)
		}
	})
	proxyMemoryPolicyScript(exec, backend, "openai", sink, reasoning)

	memory, err := db.OpenSQLiteBun(ctx, memoryPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memory.Close() })

	proxyMemoryJ5SeedSession(t, memory, "aleg-policy", time.Now().Add(-24*time.Hour).UnixMilli())
	worker.Tick(ctx)
	return backend, sink, memory
}

// proxyMemorySessionNextAttempt reads the next attempt of the seeded session.
// A refused phase keeps the session and schedules its retry with the bounded
// backoff; a served phase consumes the session, so the row is gone.
func proxyMemorySessionNextAttempt(t *testing.T, memory *bun.DB) (bool, int64) {
	t.Helper()
	var nextAttemptMS int64
	err := memory.NewRaw(
		"SELECT next_attempt_ms FROM proxy_memory_sessions WHERE a_leg_id = ?", "aleg-policy",
	).Scan(context.Background(), &nextAttemptMS)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0
	}
	require.NoError(t, err)
	return true, nextAttemptMS
}

// TestProxyMemoryModelPolicyDispatchesThePermittedTarget is the composed
// allowed case of task 2.2: the compiled actual target is covered by the
// phase's policy, so the phase reaches the provider with the effort
// unchanged, and the recorded usage describes the target that served the
// call.
func TestProxyMemoryModelPolicyDispatchesThePermittedTarget(t *testing.T) {
	// These composed tests stay sequential: the accepted composed test's
	// cleanup observes the process-wide proxy-memory goroutines, so a
	// concurrent generation would read as a leaked loop.
	backend, sink, memory := proxyMemoryPolicyRun(t, proxyMemoryPermittedInventory, true)

	observed := backend.extractions("gpt5")
	require.Len(t, observed, 1, "the permitted phase must reach the provider")
	require.Equal(t, "openai", observed[0].backend)
	require.Equal(t, "openai/test-model", observed[0].model,
		"the served call must carry the alias-resolved model")
	require.Equal(t, "low", observed[0].options.ReasoningEffort,
		"a provider-supported effort must reach the provider unchanged")
	kept, nextAttemptMS := proxyMemorySessionNextAttempt(t, memory)
	require.False(t, kept, "a served phase must consume the session, not schedule a retry")
	require.Zero(t, nextAttemptMS)
	legs := sink.legsSnapshot()
	require.NotEmpty(t, legs, "the served call must record its leg usage")
	require.Equal(t, "openai", legs[0].BackendID,
		"the recorded usage must describe the target that served the call")
	require.Equal(t, "openai/test-model", legs[0].ModelID,
		"the recorded usage must describe the target that served the call")
}

// TestProxyMemoryModelPolicyRefusesAnUncoveredTarget is the composed refused
// case of task 2.2: the alias resolves to a target the phase's inventory
// does not cover, so the phase is refused before the dispatch and never
// reaches the provider.
func TestProxyMemoryModelPolicyRefusesAnUncoveredTarget(t *testing.T) {
	backend, sink, memory := proxyMemoryPolicyRun(t, proxyMemoryUncoveredInventory, true)

	require.Empty(t, backend.observed(), "a refused phase must not reach the provider")
	require.Empty(t, sink.legsSnapshot(), "a refused phase must not record usage")
	kept, nextAttemptMS := proxyMemorySessionNextAttempt(t, memory)
	require.True(t, kept, "a refused phase must keep the session for its retry")
	require.Positive(t, nextAttemptMS,
		"a refused phase must schedule its retry with the bounded backoff")
}

// TestProxyMemoryModelPolicyRefusesAnUnsupportedEffort is the composed
// refused case of design §2: the actual target does not carry the reasoning
// capability, so the phase is refused before the dispatch and the effort is
// never silently downgraded on the provider.
func TestProxyMemoryModelPolicyRefusesAnUnsupportedEffort(t *testing.T) {
	backend, sink, memory := proxyMemoryPolicyRun(t, proxyMemoryPermittedInventory, false)

	require.Empty(t, backend.observed(), "a refused phase must not reach the provider")
	require.Empty(t, sink.legsSnapshot(), "a refused phase must not record usage")
	kept, nextAttemptMS := proxyMemorySessionNextAttempt(t, memory)
	require.True(t, kept, "a refused phase must keep the session for its retry")
	require.Positive(t, nextAttemptMS,
		"a refused phase must schedule its retry with the bounded backoff")
}

// proxyMemoryPolicyFixture is one composed generation a user-control case
// drives itself: the scripted provider, the terminal sink, the memory handle,
// the production feature worker and the generation's runtime executor.
type proxyMemoryPolicyFixture struct {
	backend *proxyMemoryPhaseBackend
	sink    *proxyMemoryPolicySink
	memory  *bun.DB
	worker  *proxymemory.Worker
	exec    *runtime.Executor
	// metrics is the process metrics bundle of the composed run. It is nil
	// unless the case enabled metrics; a case that asserts a bounded refusal
	// count enables them and reads the proxy-memory events from it.
	metrics *metrics.Bundle
}

// proxyMemoryPolicyFixtureFor composes one generation with the given allowed
// inventory and the given configuration and executor edits, without seeding a
// session or ticking the worker. The caller seeds its own owners and drives
// the tick, so one composed run can carry several users or several preference
// changes.
//
// The edits let a case install a policy lock, a request-part hook or a
// call-sensitive capability resolver on the *production* path, so the case
// observes the real worker, scheduler, executor and provider open.
func proxyMemoryPolicyFixtureFor(
	t *testing.T,
	inventory string,
	reasoning bool,
	editConfig func(*config.Config),
	editExec func(*runtime.Executor),
) *proxyMemoryPolicyFixture {
	t.Helper()
	ctx := context.Background()
	memoryPath := filepath.Join(t.TempDir(), "memory.db")
	continuityPath := filepath.Join(t.TempDir(), "continuity.db")

	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))

	cfg := proxyMemoryPolicyConfig(t, memoryPath, continuityPath, inventory)
	if editConfig != nil {
		editConfig(cfg)
	}
	ps, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	backend := &proxyMemoryPhaseBackend{}
	sink := &proxyMemoryPolicySink{}
	worker, exec, dispose, err := runtimebundle.ProxyMemoryGenerationForTest(ctx, runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, dispose())
		if loops := proxyMemoryJ5LiveLoops(); loops != 0 {
			t.Errorf("the candidate generation left %d background loop(s) behind", loops)
		}
	})
	proxyMemoryPolicyScript(exec, backend, "openai", sink, reasoning)
	if editExec != nil {
		editExec(exec)
	}

	memory, err := db.OpenSQLiteBun(ctx, memoryPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = memory.Close() })

	return &proxyMemoryPolicyFixture{
		backend: backend,
		sink:    sink,
		memory:  memory,
		worker:  worker,
		exec:    exec,
		metrics: ps.Metrics,
	}
}

// proxyMemoryPolicyRunWith composes one generation with the given allowed
// inventory and the given configuration and executor edits, seeds one
// claimable session for the fixture owner and drives the worker's tick.
func proxyMemoryPolicyRunWith(
	t *testing.T,
	inventory string,
	reasoning bool,
	editConfig func(*config.Config),
	editExec func(*runtime.Executor),
) (*proxyMemoryPhaseBackend, *proxyMemoryPolicySink, *bun.DB) {
	t.Helper()
	fixture := proxyMemoryPolicyFixtureFor(t, inventory, reasoning, editConfig, editExec)
	proxyMemoryJ5SeedSession(t, fixture.memory, "aleg-policy", time.Now().Add(-24*time.Hour).UnixMilli())
	fixture.worker.Tick(context.Background())
	return fixture.backend, fixture.sink, fixture.memory
}

// proxyMemoryPolicyEditFeature replaces one string of the feature's
// configuration subtree, so a case can declare a policy lock without
// restating the whole configuration.
func proxyMemoryPolicyEditFeature(t *testing.T, cfg *config.Config, old, replacement string) {
	t.Helper()
	raw, err := yaml.Marshal(cfg.Plugins.Features[0].Config)
	require.NoError(t, err)
	require.Contains(t, string(raw), old)
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(strings.ReplaceAll(string(raw), old, replacement)), &doc))
	cfg.Plugins.Features[0].Config = *doc.Content[0]
}

// proxyMemoryEffortHook is a request-part hook that rewrites the reasoning
// effort of the attempt. It uses the existing SDK hook interface and the
// ordinary core bus, so the mutation is an ordinary one.
type proxyMemoryEffortHook struct{}

func (proxyMemoryEffortHook) ID() string { return "proxy-memory-effort-mutation" }

func (proxyMemoryEffortHook) Order() int { return 1 }

func (proxyMemoryEffortHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (proxyMemoryEffortHook) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	call.Options.ReasoningEffort = "medium"
	return nil
}

// TestProxyMemoryModelPolicyKeepsTheOperatorsLockedAlias pins the compiled
// lock of design §3: locking the operator's own, unchanged alias must not
// refuse its own compiled target, so the phase reaches the provider.
func TestProxyMemoryModelPolicyKeepsTheOperatorsLockedAlias(t *testing.T) {
	backend, _, _ := proxyMemoryPolicyRunWith(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		proxyMemoryPolicyEditFeature(t, cfg, "locks: {}", "locks: {model: true}")
	}, nil)

	observed := backend.extractions("gpt5")
	require.Len(t, observed, 1,
		"locking the operator's unchanged alias must not reject its own compiled target")
	require.Equal(t, "openai/test-model", observed[0].model)
}

// TestProxyMemoryModelPolicyRefusesANotInstalledTarget pins the installation
// fact of design §3: an allowed model the deployment does not carry is
// refused before the provider I/O, in both phases.
func TestProxyMemoryModelPolicyRefusesANotInstalledTarget(t *testing.T) {
	inventory := strings.ReplaceAll(proxyMemoryPermittedInventory, "openai/test-model", "openai/not-installed")
	backend, _, _ := proxyMemoryPolicyRunWith(t, inventory, true, func(cfg *config.Config) {
		for i := range cfg.ModelAliases {
			cfg.ModelAliases[i].Replacement = "openai:openai/not-installed"
		}
	}, nil)

	require.Empty(t, backend.observed(), "an allowed but absent model must be refused before provider I/O")
}

// TestProxyMemoryModelPolicyRefusesADivergedEffort pins the pre-open
// invariant of design §2: a request transform that rewrites the locked
// reasoning effort must not slip past the policy between the dispatch and
// the provider open.
func TestProxyMemoryModelPolicyRefusesADivergedEffort(t *testing.T) {
	backend, _, _ := proxyMemoryPolicyRunWith(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		proxyMemoryPolicyEditFeature(t, cfg, "locks: {}", "locks: {reasoning_effort: true}")
	}, func(exec *runtime.Executor) {
		exec.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{proxyMemoryEffortHook{}}})
		// The resolver is call-sensitive: it carries the reasoning capability
		// for the frozen effort and not for the rewritten one, so the
		// runtime would negotiate the effort away.
		exec.CapsResolver = capabilities.MapResolver{"openai": func(_ context.Context, _ routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
			caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityStructuredOutputs)
			if call.Options.ReasoningEffort == "low" {
				caps[aipapi.CapabilityReasoning] = struct{}{}
			}
			return caps
		}}
	})

	require.Empty(t, backend.observed(),
		"a diverged reasoning effort must be refused before the provider open")
}

// TestProxyMemoryModelPolicyKeepsTheUnchangedEffort pins the positive
// control of the same invariant: an attempt that still carries the frozen
// effort reaches the provider with that effort unchanged.
func TestProxyMemoryModelPolicyKeepsTheUnchangedEffort(t *testing.T) {
	backend, _, _ := proxyMemoryPolicyRunWith(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		proxyMemoryPolicyEditFeature(t, cfg, "locks: {}", "locks: {reasoning_effort: true}")
	}, func(exec *runtime.Executor) {
		exec.CapsResolver = capabilities.MapResolver{"openai": func(_ context.Context, _ routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
			caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityStructuredOutputs)
			if call.Options.ReasoningEffort == "low" {
				caps[aipapi.CapabilityReasoning] = struct{}{}
			}
			return caps
		}}
	})

	observed := backend.extractions("gpt5")
	require.Len(t, observed, 1, "the unchanged effort must reach the provider")
	require.Equal(t, "low", observed[0].options.ReasoningEffort,
		"the provider must receive the frozen reasoning effort unchanged")
}

// TestProxyMemoryModelPolicyRefusesALaterUncoveredCandidate pins the
// candidate coverage of Requirement 1.5: a later candidate the policy does
// not cover refuses the phase before any provider I/O.
func TestProxyMemoryModelPolicyRefusesALaterUncoveredCandidate(t *testing.T) {
	backend, _, _ := proxyMemoryPolicyRunWith(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		cfg.ModelAliases[0].Replacement = "openai:openai/test-model | openai:openai/uncovered"
	}, nil)

	require.Empty(t, backend.observed(), "a later uncovered candidate must refuse the phase before any open")
}

// proxyMemoryExtractionPolicy replaces the declared default target and the
// lock set of the extraction phase only, so a case can declare a policy
// without restating the whole configuration and without touching the
// consolidation phase.
func proxyMemoryExtractionPolicy(t *testing.T, cfg *config.Config, defaults, locks string) {
	t.Helper()
	proxyMemoryPolicyEditFeature(t, cfg,
		"    extraction:\n        defaults: {}\n        locks: {}",
		"    extraction:\n        defaults: {"+defaults+"}\n        locks: {"+locks+"}")
}

// TestProxyMemoryModelPolicyServesTheOperatorsMultiLeafAlias pins the full
// reference set of design §3 on the composed path: the operator's own
// unchanged alias resolves to two leaves, and a lock on the model must not
// refuse the operator's own second leaf.
//
// Before the repair the reference kept only its first candidate, so the
// operator's own unchanged alias was refused and the phase never reached the
// provider.
func TestProxyMemoryModelPolicyServesTheOperatorsMultiLeafAlias(t *testing.T) {
	backend, _, _ := proxyMemoryPolicyRunWith(t, proxyMemoryMultiLeafInventory, true, func(cfg *config.Config) {
		cfg.ModelAliases[0].Replacement = "openai:openai/test-model | openai:openai/alternate"
		proxyMemoryExtractionPolicy(t, cfg, "model: gpt5", "model: true")
	}, nil)

	observed := backend.extractions("gpt5")
	require.Len(t, observed, 1,
		"locking the operator's own multi-leaf alias must not refuse its own compiled target")
	require.Equal(t, "openai/test-model", observed[0].model)
}

// TestProxyMemoryModelPolicyKeepsTheInheritedLockedModel pins the effective
// reference of design §3 on the composed path: the operator named no model
// default, so the operator's default route supplies the model, and the lock
// pins that inherited value. The operator's own route is still served.
func TestProxyMemoryModelPolicyKeepsTheInheritedLockedModel(t *testing.T) {
	backend, _, _ := proxyMemoryPolicyRunWith(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		proxyMemoryExtractionPolicy(t, cfg, "", "model: true")
	}, nil)

	observed := backend.extractions("gpt5")
	require.Len(t, observed, 1,
		"a lock on the model's inherited value must not refuse the operator's own route")
	require.Equal(t, "openai/test-model", observed[0].model)
}

// proxyMemoryConsolidationAllowedModels is the marshalled allowed block of
// the consolidation phase when both phases carry the same inventory.
const proxyMemoryConsolidationAllowedModels = "    consolidation:\n        defaults: {}\n        locks: {}\n        allowed:\n            backends: [openai]\n            models: [openai/test-model]\n"

// proxyMemoryConsolidationDistinctModels is the same block with a
// consolidation phase whose policy names another target: the two phases then
// carry distinct policy triples.
const proxyMemoryConsolidationDistinctModels = "    consolidation:\n        defaults: {}\n        locks: {}\n        allowed:\n            backends: [openai]\n            models: [openai/other-model]\n"

// TestProxyMemoryModelPolicyAppliesDistinctTriplesPerPhase pins the per-phase
// independence of design §3 on the composed path: the two phases carry
// distinct policy triples, the extraction phase's policy covers its target
// and the consolidation phase's policy does not, so the consolidation phase
// is refused before any provider I/O while the extraction phase is served.
//
// The composed fixture dispatches both phases: the extraction phase serves
// the call and records its leg usage, and the refused consolidation phase
// records none.
func TestProxyMemoryModelPolicyAppliesDistinctTriplesPerPhase(t *testing.T) {
	backend, sink, _ := proxyMemoryPolicyRunWith(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		proxyMemoryPolicyEditFeature(t, cfg, proxyMemoryConsolidationAllowedModels, proxyMemoryConsolidationDistinctModels)
	}, nil)

	require.Len(t, backend.extractions("gpt5"), 1, "the extraction phase must be served")
	require.Empty(t, backend.extractions("gpt5b"),
		"the consolidation phase must be refused before any provider open")
	legs := sink.legsSnapshot()
	require.Len(t, legs, 1, "only the served phase may record its leg usage")
	require.Equal(t, "openai/test-model", legs[0].ModelID)
}

// proxyMemoryServedOpen is one provider open the scripted provider observed.
//
// It carries the attempt identity the runtime published to the adapter
// together with the target and the options the open carried. The B-leg
// identity comes from the runtime's own observation lineage, the seam a
// provider adapter uses to associate a foreground observation with its
// B-leg: it lets the test correlate one actual open with the leg row the
// runtime recorded for it, instead of guessing from the recorded values.
type proxyMemoryServedOpen struct {
	aleg    string
	bleg    string
	backend string
	model   string
	route   string
	effort  string
}

// The usage each served arm records. The extraction phase's second arm and
// the consolidation phase record a different usage, so the leg row of one
// phase can never satisfy the other's evidence.
const (
	proxyMemoryExtractionUsageIn    = 11
	proxyMemoryExtractionUsageOut   = 7
	proxyMemoryExtractionUsageTotal = 18
	proxyMemoryOtherUsageIn         = 31
	proxyMemoryOtherUsageOut        = 17
	proxyMemoryOtherUsageTotal      = 48
)

// candidate coverage of Requirement 1.5 on the composed path: two failover
// arms address the same backend and model and differ only in their route
// parameters, the first arm fails recoverably, and the second arm serves the
// call. The recorded usage describes the arm that served.
//
// The arms are not ambiguous: the guard selects the arm by its candidate key,
// so the phase is served instead of refused.
//
// The evidence is a correlation, not a presence check. The served arm is
// identified by the B-leg the runtime published to the adapter for its
// actual open, and the leg row the runtime recorded for that B-leg must
// describe that arm: its target, its outcome and the usage it incurred. The
// failed first arm and the consolidation phase record their own rows with a
// different identity and a different usage, so neither can satisfy the
// served arm's evidence.
func TestProxyMemoryModelPolicyServesTheRecoverableSecondArm(t *testing.T) {
	var mu sync.Mutex
	var served []proxyMemoryPhaseCall
	var opens []proxyMemoryServedOpen
	opensByRoute := map[string]int{}
	scripted := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(
			aipapi.CapabilityStreaming,
			aipapi.CapabilityStructuredOutputs,
			aipapi.CapabilityReasoning,
		),
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			mu.Lock()
			opensByRoute[call.Route.Selector]++
			mu.Unlock()
			if call.Options.ReasoningEffort == "low" {
				// The first arm fails recoverably: the runtime fails
				// over to the second arm.
				return nil, aipapi.RecoverablePreOutputError(errors.New("the first arm is unavailable"))
			}
			lineage, _ := promptcache.ObservationLineageFromContext(ctx)
			mu.Lock()
			opens = append(opens, proxyMemoryServedOpen{
				aleg:    lineage.ALegID,
				bleg:    lineage.BLegID,
				backend: cand.Primary.Backend,
				model:   cand.Primary.Model,
				route:   call.Route.Selector,
				effort:  call.Options.ReasoningEffort,
			})
			served = append(served, proxyMemoryPhaseCall{
				backend: cand.Primary.Backend,
				model:   cand.Primary.Model,
				route:   call.Route.Selector,
				options: call.Options,
			})
			mu.Unlock()
			in, out, total := proxyMemoryExtractionUsageIn, proxyMemoryExtractionUsageOut, proxyMemoryExtractionUsageTotal
			if call.Options.ReasoningEffort != "high" {
				in, out, total = proxyMemoryOtherUsageIn, proxyMemoryOtherUsageOut, proxyMemoryOtherUsageTotal
			}
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: proxyMemoryPhaseReply},
				{
					Kind: aipapi.EventUsageDelta, InputTokens: in, OutputTokens: out, TotalTokens: total,
					UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
				},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	_, sink, _ := proxyMemoryPolicyRunWith(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		cfg.ModelAliases[0].Replacement = "openai:openai/test-model?reasoning_effort=low | openai:openai/test-model?reasoning_effort=high"
	}, func(exec *runtime.Executor) {
		exec.Backends = map[string]execbackend.Backend{"openai": scripted}
	})

	mu.Lock()
	defer mu.Unlock()
	var phaseOne []proxyMemoryPhaseCall
	for _, call := range served {
		if call.route == "gpt5" {
			phaseOne = append(phaseOne, call)
		}
	}
	require.Len(t, phaseOne, 1, "the second arm must serve the extraction phase")
	require.Equal(t, "high", phaseOne[0].options.ReasoningEffort,
		"the served arm must be the one the runtime failed over to")
	require.Equal(t, 2, opensByRoute["gpt5"], "both arms must be attempted once")

	// The served arm is the extraction phase's actual open with the high
	// effort. Its identity is the B-leg the runtime published for it.
	var servedOpen *proxyMemoryServedOpen
	for i := range opens {
		if opens[i].route != "gpt5" {
			continue
		}
		require.Nil(t, servedOpen, "the extraction phase must serve exactly one arm")
		servedOpen = &opens[i]
	}
	require.NotNil(t, servedOpen, "the extraction phase must serve its second arm")
	require.Equal(t, "high", servedOpen.effort,
		"the served arm must be the one the runtime failed over to")
	require.Equal(t, "openai", servedOpen.backend)
	require.Equal(t, "openai/test-model", servedOpen.model)
	require.NotEmpty(t, servedOpen.bleg, "the served open must carry its attempt identity")

	// The leg row of the served arm is the row of that B-leg, and it must
	// describe the arm that served the call.
	legs := sink.legsSnapshot()
	var servedLeg *usageevidence.CallLegUsageRecord
	for i := range legs {
		if legs[i].BLegID != servedOpen.bleg {
			continue
		}
		require.Nil(t, servedLeg, "the served arm must record exactly one leg row")
		servedLeg = &legs[i]
	}
	require.NotNil(t, servedLeg, "the served arm must record its own leg usage")
	require.Equal(t, servedOpen.aleg, servedLeg.ALegID,
		"the recorded leg must belong to the served attempt's a-leg")
	require.Equal(t, 2, servedLeg.AttemptSeq,
		"the served arm is the second attempt of the extraction call")
	require.Equal(t, "openai", servedLeg.BackendID,
		"the recorded usage must describe the target that served the call")
	require.Equal(t, "openai/test-model", servedLeg.ModelID,
		"the recorded usage must describe the target that served the call")
	require.Equal(t, usageevidence.LegOutcomeWinner, servedLeg.Outcome,
		"the served leg must be recorded as the winner")
	require.Equal(t, usageevidence.SurfacedYes, servedLeg.Surfaced,
		"the served leg must be recorded as surfaced")
	require.Equal(t, int64(proxyMemoryExtractionUsageIn), servedLeg.Evidence.InputTokens.Value,
		"the recorded usage must be the usage the served arm incurred")
	require.True(t, servedLeg.Evidence.InputTokens.Present)
	require.Equal(t, int64(proxyMemoryExtractionUsageOut), servedLeg.Evidence.OutputTokens.Value)
	require.True(t, servedLeg.Evidence.OutputTokens.Present)
	require.Equal(t, int64(proxyMemoryExtractionUsageTotal), servedLeg.Evidence.TotalTokens.Value)
	require.True(t, servedLeg.Evidence.TotalTokens.Present)

	// The failed first arm is recorded in the same call as never started,
	// with no usage: it can never satisfy the served arm's evidence.
	var failedLeg *usageevidence.CallLegUsageRecord
	for i := range legs {
		if legs[i].ALegID == servedOpen.aleg && legs[i].BLegID != servedOpen.bleg {
			require.Nil(t, failedLeg, "the failed first arm must record exactly one leg row")
			failedLeg = &legs[i]
		}
	}
	require.NotNil(t, failedLeg, "the failed first arm must record its own leg row")
	require.Equal(t, servedLeg.CallID, failedLeg.CallID,
		"both arms belong to the same call")
	require.Equal(t, 1, failedLeg.AttemptSeq)
	require.Equal(t, usageevidence.LegOutcomeNeverStarted, failedLeg.Outcome,
		"the failed first arm must be recorded as never started")
	require.False(t, failedLeg.Evidence.InputTokens.Present,
		"the failed first arm must record no usage")

	// The consolidation phase is another call with another usage: no other
	// row can satisfy the served arm's evidence.
	var otherLegs []usageevidence.CallLegUsageRecord
	for i := range legs {
		if legs[i].CallID != servedLeg.CallID {
			otherLegs = append(otherLegs, legs[i])
		}
	}
	require.Len(t, otherLegs, 1, "the consolidation phase must record its own leg row")
	require.Equal(t, int64(proxyMemoryOtherUsageIn), otherLegs[0].Evidence.InputTokens.Value,
		"the consolidation phase records a different usage")
}

// identity-only reference compile of design §1: the operator's locked
// reference is compiled without resolving a capability, so it performs no
// provider I/O and adds no capability resolution to the phase dispatch.
//
// The count is taken at the capability-resolution seam, which is the call an
// executable connector serves with its ResolveProfile. The reference is the
// only compile that carries a content-free call, so a resolution for such a
// call would be the reference resolving a capability: the count of those is
// zero, while the phase's own compile still resolves the capabilities of its
// candidates.
func TestProxyMemoryModelPolicyReferenceAddsNoCapabilityResolution(t *testing.T) {
	var counter *proxyMemoryCountingResolver
	backend, _, _ := proxyMemoryPolicyRunWith(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		proxyMemoryExtractionPolicy(t, cfg, "model: gpt5", "model: true")
	}, func(exec *runtime.Executor) {
		counter = &proxyMemoryCountingResolver{inner: exec.CapsResolver}
		exec.CapsResolver = counter
	})

	require.Len(t, backend.extractions("gpt5"), 1, "the phase must reach the provider")
	calls, withoutOptions := counter.counts()
	require.Positive(t, calls,
		"the phase dispatch must resolve the capabilities of its candidates")
	require.Zero(t, withoutOptions,
		"the identity-only reference compile must resolve no capability")
}

// proxyMemoryFactsCatalog states the model catalog's per-model reasoning fact
// for the composed capability cases. The entry is matched, so the fact is the
// deployment's answer for the compiled model and not a backend declaration.
type proxyMemoryFactsCatalog struct {
	reasoning modelcatalog.CapabilityTriState
	matched   bool
}

func (c proxyMemoryFactsCatalog) Resolve(_ context.Context, _ routing.AttemptCandidate, _ aipapi.Call, backend aipapi.BackendCaps) modelcatalog.EffectiveFacts {
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchExact,
			Reasoning: c.reasoning,
		},
		BackendCaps:   backend,
		EffectiveCaps: modelcatalog.CloneBackendCaps(backend),
		Matched:       c.matched,
	}
}

// proxyMemoryEventCount returns the value of one bounded proxy-memory event
// series from the process metrics registry.
func proxyMemoryEventCount(t *testing.T, bundle *metrics.Bundle, event string) float64 {
	t.Helper()
	require.NotNil(t, bundle, "the case must enable process metrics to count the refusal")
	require.NotNil(t, bundle.Registry, "the process metrics bundle must carry a registry")
	families, err := bundle.Registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "aip_proxy_memory_events_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, pair := range metric.GetLabel() {
				if pair.GetName() == "event" && pair.GetValue() == event {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestProxyMemoryModelPolicyRefusesAnUnknownReasoningCapability pins the
// tri-state of the model catalog on the composed path: the backend declares
// the reasoning capability, but the model catalog does not establish it for
// the compiled model. The unknown state is not support: the phase is refused
// before the provider open and the refusal is counted.
func TestProxyMemoryModelPolicyRefusesAnUnknownReasoningCapability(t *testing.T) {
	fixture := proxyMemoryPolicyFixtureFor(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		cfg.Observability.Metrics.Enabled = true
	}, func(exec *runtime.Executor) {
		exec.CatalogResolver = proxyMemoryFactsCatalog{
			reasoning: modelcatalog.CapabilityUnknown,
			matched:   true,
		}
	})
	proxyMemoryJ5SeedSession(t, fixture.memory, "aleg-policy", time.Now().Add(-24*time.Hour).UnixMilli())
	fixture.worker.Tick(context.Background())

	require.Empty(t, fixture.backend.observed(),
		"an unproven reasoning capability must refuse the phase before the provider open")
	kept, nextAttemptMS := proxyMemorySessionNextAttempt(t, fixture.memory)
	require.True(t, kept, "a refused phase must keep the session for its retry")
	require.Positive(t, nextAttemptMS)
	require.Equal(t, float64(1), proxyMemoryEventCount(t, fixture.metrics, proxymemory.EventModelPolicyRefusedReasoning),
		"the capability refusal must be counted once")
}

// TestProxyMemoryModelPolicyServesAProvenReasoningCapability is the positive
// control of the same case: a model catalog entry that establishes the
// capability serves the phase with the effort unchanged.
func TestProxyMemoryModelPolicyServesAProvenReasoningCapability(t *testing.T) {
	fixture := proxyMemoryPolicyFixtureFor(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		cfg.Observability.Metrics.Enabled = true
	}, func(exec *runtime.Executor) {
		exec.CatalogResolver = proxyMemoryFactsCatalog{
			reasoning: modelcatalog.CapabilitySupported,
			matched:   true,
		}
	})
	proxyMemoryJ5SeedSession(t, fixture.memory, "aleg-policy", time.Now().Add(-24*time.Hour).UnixMilli())
	fixture.worker.Tick(context.Background())

	observed := fixture.backend.extractions("gpt5")
	require.Len(t, observed, 1, "a proven reasoning capability must serve the phase")
	require.Equal(t, "low", observed[0].options.ReasoningEffort,
		"the proven effort must reach the provider unchanged")
	require.Zero(t, proxyMemoryEventCount(t, fixture.metrics, proxymemory.EventModelPolicyRefusedReasoning))
}

// TestProxyMemoryModelPolicyRefusesAPreAdmissionNegotiatedStrip pins the first
// negotiated-downgrade site: the dispatch-time compile proves the phase's
// effort, and the runtime's pre-open candidate admission asks the same
// call-sensitive resolver again and no longer gets the capability. The strip
// is converted to an explicit, counted refusal instead of a downgrade.
//
// The refusal must come from the strip site and not from the later open guard:
// only the strip-site refusal increments the negotiated-strip event.
func TestProxyMemoryModelPolicyRefusesAPreAdmissionNegotiatedStrip(t *testing.T) {
	var resolutions atomic.Int32
	fixture := proxyMemoryPolicyFixtureFor(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		cfg.Observability.Metrics.Enabled = true
	}, func(exec *runtime.Executor) {
		exec.CapsResolver = capabilities.MapResolver{"openai": func(_ context.Context, _ routing.AttemptCandidate, _ aipapi.Call) aipapi.BackendCaps {
			caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityStructuredOutputs)
			// The dispatch compile is the first resolution and proves the
			// capability; the runtime's pre-open admission is the second and
			// no longer does, modelling a dynamic resolver.
			if resolutions.Add(1) == 1 {
				caps[aipapi.CapabilityReasoning] = struct{}{}
			}
			return caps
		}}
	})
	proxyMemoryJ5SeedSession(t, fixture.memory, "aleg-policy", time.Now().Add(-24*time.Hour).UnixMilli())
	fixture.worker.Tick(context.Background())

	require.Empty(t, fixture.backend.observed(),
		"a negotiated effort strip must refuse the phase before the provider open")
	require.Equal(t, float64(1), proxyMemoryEventCount(t, fixture.metrics, proxymemory.EventModelPolicyRefusedNegotiatedStrip),
		"the pre-open admission strip must be refused and counted once")
}

// TestProxyMemoryModelPolicyRefusesAPostHookNegotiatedStrip pins the second
// negotiated-downgrade site: the dispatch compile and the initial pre-open
// admission both prove the phase's effort, and the post-request re-derive asks
// the same call-sensitive resolver again and no longer gets the capability.
// The strip is refused and counted at that site, before the provider open.
func TestProxyMemoryModelPolicyRefusesAPostHookNegotiatedStrip(t *testing.T) {
	var resolutions atomic.Int32
	fixture := proxyMemoryPolicyFixtureFor(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		cfg.Observability.Metrics.Enabled = true
	}, func(exec *runtime.Executor) {
		exec.CapsResolver = capabilities.MapResolver{"openai": func(_ context.Context, _ routing.AttemptCandidate, _ aipapi.Call) aipapi.BackendCaps {
			caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityStructuredOutputs)
			// The dispatch compile and the initial pre-open admission prove
			// the capability; the post-request re-derive is the third
			// resolution and no longer does.
			if resolutions.Add(1) <= 2 {
				caps[aipapi.CapabilityReasoning] = struct{}{}
			}
			return caps
		}}
	})
	proxyMemoryJ5SeedSession(t, fixture.memory, "aleg-policy", time.Now().Add(-24*time.Hour).UnixMilli())
	fixture.worker.Tick(context.Background())

	require.Empty(t, fixture.backend.observed(),
		"a post-request negotiated effort strip must refuse the phase before the provider open")
	require.Equal(t, float64(1), proxyMemoryEventCount(t, fixture.metrics, proxymemory.EventModelPolicyRefusedNegotiatedStrip),
		"the post-request strip must be refused and counted once")
}

// TestProxyMemoryModelPolicyServesTheRouteParameterEffortTheTargetProves pins
// the scope of the negotiated-strip refusal: the operator's route parameter
// replaces the phase's own effort with a proven one, so stripping the
// superseded baseline effort does not degrade the served call. The phase is
// served with the route parameter's effort, and the strip refusal is not
// counted.
func TestProxyMemoryModelPolicyServesTheRouteParameterEffortTheTargetProves(t *testing.T) {
	fixture := proxyMemoryPolicyFixtureFor(t, proxyMemoryPermittedInventory, true, func(cfg *config.Config) {
		cfg.Observability.Metrics.Enabled = true
		cfg.ModelAliases[0].Replacement = "openai:openai/test-model?reasoning_effort=high"
	}, func(exec *runtime.Executor) {
		exec.CapsResolver = capabilities.MapResolver{"openai": func(_ context.Context, _ routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
			caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityStructuredOutputs)
			if call.Options.ReasoningEffort == "high" {
				caps[aipapi.CapabilityReasoning] = struct{}{}
			}
			return caps
		}}
	})
	proxyMemoryJ5SeedSession(t, fixture.memory, "aleg-policy", time.Now().Add(-24*time.Hour).UnixMilli())
	fixture.worker.Tick(context.Background())

	observed := fixture.backend.extractions("gpt5")
	require.Len(t, observed, 1, "the route parameter's proven effort must serve the phase")
	require.Equal(t, "high", observed[0].options.ReasoningEffort,
		"the served call must carry the compiled route parameter's effort")
	require.Zero(t, proxyMemoryEventCount(t, fixture.metrics, proxymemory.EventModelPolicyRefusedNegotiatedStrip),
		"a strip of an effort the route parameters replace must not be refused")
}
