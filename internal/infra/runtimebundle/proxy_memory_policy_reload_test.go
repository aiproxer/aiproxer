package runtimebundle_test

import (
	"context"
	"fmt"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"github.com/uptrace/bun"
)

// proxyMemoryPhaseReply is the extraction phase's canned reply: the scripted
// provider answers the phase with a well-formed envelope, so the phase
// completes instead of only reaching the provider.
const proxyMemoryPhaseReply = `{"raw_memory":"phase memory","rollout_summary":"phase summary","rollout_slug":"phase-slug"}`

// proxyMemoryPhaseCall is one phase dispatch the scripted provider observed:
// the backend and the model the compiled route addressed, and the options the
// call carried.
type proxyMemoryPhaseCall struct {
	backend string
	model   string
	route   string
	options aipapi.GenerationOptions
	// aLegID and bLegID are the runtime's own observation lineage for this
	// actual provider open. They correlate the provider-facing arguments with
	// the terminal usage row of the same attempt.
	aLegID string
	bLegID string
}

// proxyMemoryPhaseBackend is the scripted provider the background phases reach.
// It records the candidate it was opened with, so the composed test observes
// the actual backend, model and options of the served call.
type proxyMemoryPhaseBackend struct {
	mu    sync.Mutex
	calls []proxyMemoryPhaseCall
}

func (b *proxyMemoryPhaseBackend) Backend() execbackend.Backend {
	return execbackend.Backend{
		// The phase calls ask for JSON output and a reasoning effort: the
		// scripted provider declares the capabilities it actually serves.
		Caps: aipapi.NewBackendCaps(
			aipapi.CapabilityStreaming,
			aipapi.CapabilityStructuredOutputs,
			aipapi.CapabilityReasoning,
		),
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			lineage, _ := promptcache.ObservationLineageFromContext(ctx)
			b.mu.Lock()
			b.calls = append(b.calls, proxyMemoryPhaseCall{
				backend: cand.Primary.Backend,
				model:   cand.Primary.Model,
				route:   call.Route.Selector,
				options: call.Options,
				aLegID:  lineage.ALegID,
				bLegID:  lineage.BLegID,
			})
			b.mu.Unlock()
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: proxyMemoryPhaseReply},
				{
					Kind: aipapi.EventUsageDelta, InputTokens: 11, OutputTokens: 7, TotalTokens: 18,
					UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
				},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}
}

func (b *proxyMemoryPhaseBackend) observed() []proxyMemoryPhaseCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]proxyMemoryPhaseCall(nil), b.calls...)
}

// extractions returns the observed calls of the extraction phase of one phase
// route only.
func (b *proxyMemoryPhaseBackend) extractions(route string) []proxyMemoryPhaseCall {
	var out []proxyMemoryPhaseCall
	for _, call := range b.observed() {
		if call.route == route {
			out = append(out, call)
		}
	}
	return out
}

// proxyMemoryJ5Backend is one backend instance a generation declares: the
// instance id the executor's backend map is keyed by, and whether the
// generation serves it.
type proxyMemoryJ5Backend struct {
	id      string
	enabled bool
}

// proxyMemoryJ5Generation is one generation's facts: the backend instances it
// declares, the aliases its phase routes resolve through, and the route
// parameters its aliases carry.
type proxyMemoryJ5Generation struct {
	// extractRoute is the phase 1 route the generation dispatches.
	extractRoute string
	// consolidateRoute is the phase 2 route the generation declares.
	consolidateRoute string
	// defaultRoute is the generation's runtime default route.
	defaultRoute string
	// aliases are the generation's model aliases, in order.
	aliases []config.ModelAliasConfig
	// backends are the generation's declared backend instances.
	backends []proxyMemoryJ5Backend
}

// proxyMemoryJ5FirstGeneration is the running generation: it serves the
// "openai" instance, and its phase 1 route resolves through the "gpt5" alias
// to the model that instance declares.
func proxyMemoryJ5FirstGeneration() proxyMemoryJ5Generation {
	return proxyMemoryJ5Generation{
		extractRoute:     "gpt5",
		consolidateRoute: "gpt5b",
		defaultRoute:     "openai:openai/test-model",
		aliases: []config.ModelAliasConfig{
			{Pattern: "^gpt5$", Replacement: "openai:openai/test-model"},
			{Pattern: "^gpt5b$", Replacement: "openai:openai/test-model"},
		},
		backends: []proxyMemoryJ5Backend{{id: "openai", enabled: true}},
	}
}

// proxyMemoryJ5SecondGeneration is the reloaded generation: the operator
// retired the "openai" instance and declared the "other" one, so the new
// generation's backend inventory no longer holds the backend the first
// generation's phase route resolves to. The alias table still maps the first
// generation's phase route to the retired instance, so that route is a valid
// target for the first generation and an unknown backend for this one.
//
// The second generation's own phase 1 route resolves to the instance it
// serves, with the route parameter that raises the effort.
func proxyMemoryJ5SecondGeneration() proxyMemoryJ5Generation {
	return proxyMemoryJ5Generation{
		extractRoute:     "gpt5c",
		consolidateRoute: "gpt5d",
		defaultRoute:     "other:other/test-model",
		aliases: []config.ModelAliasConfig{
			{Pattern: "^gpt5$", Replacement: "openai:openai/test-model"},
			{Pattern: "^gpt5c$", Replacement: "other:other/test-model?reasoning_effort=high"},
			{Pattern: "^gpt5d$", Replacement: "other:other/test-model?reasoning_effort=high"},
		},
		backends: []proxyMemoryJ5Backend{
			{id: "openai", enabled: false},
			{id: "other", enabled: true},
		},
	}
}

// proxyMemoryJ5BackendYAML is the model inventory of one backend instance: the
// canonical model id is namespaced by the instance id, so every instance
// declares its own model.
func proxyMemoryJ5BackendYAML(instanceID string) string {
	return strings.ReplaceAll(testOpenAIBackendYAML(), "openai/test-model", instanceID+"/test-model")
}

// proxyMemoryJ5Config builds the configuration of one generation: the
// proxy-memory feature with a declared model policy for both phases, the
// backend instances the generation declares, and the model aliases the phase
// routes resolve through.
//
// The optional inventory is the body of each phase's allowed inventory. An
// absent inventory declares an empty one, so the phase keeps the operator's
// own route.
func proxyMemoryJ5Config(t *testing.T, memoryPath, continuityPath string, gen proxyMemoryJ5Generation, inventory ...string) *config.Config {
	t.Helper()
	backends := make([]config.PluginConfig, 0, len(gen.backends))
	for _, b := range gen.backends {
		var backendNode yaml.Node
		require.NoError(t, yaml.Unmarshal([]byte(proxyMemoryJ5BackendYAML(b.id)), &backendNode))
		if len(backendNode.Content) > 0 {
			backendNode = *backendNode.Content[0]
		}
		backends = append(backends, config.PluginConfig{ID: b.id, Kind: "openai-responses", Enabled: b.enabled, Config: backendNode})
	}
	allowed := " {}"
	if len(inventory) > 0 {
		allowed = "\n" + inventory[0]
	}
	var featureNode yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(
		"principals:\n  - alice\n"+
			"extract_route: "+gen.extractRoute+"\n"+
			"consolidate_route: "+gen.consolidateRoute+"\n"+
			"sqlite_path: "+memoryPath+"\n"+
			// The loop's own tick source never fires during the test: the
			// manual tick is the only driver, so the background loop cannot
			// race it.
			"poll_interval: 1h\n"+
			// The generation binds stub billing seams so the phases reach
			// the provider without the money domain; a host with billing
			// seams needs an explicit funding mode, and these proofs are
			// usage-only.
			"funding_mode: operator_sponsored\n"+
			"models:\n"+
			"  extraction:\n"+
			"    defaults: {}\n"+
			"    locks: {}\n"+
			"    allowed:"+allowed+"\n"+
			"  consolidation:\n"+
			"    defaults: {}\n"+
			"    locks: {}\n"+
			"    allowed:"+allowed+"\n"), &featureNode))
	if len(featureNode.Content) > 0 {
		featureNode = *featureNode.Content[0]
	}
	return &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3, DefaultRoute: gen.defaultRoute},
		Continuity: config.ContinuityConfig{Store: "sqlite", SQLitePath: continuityPath},
		Plugins: config.PluginsConfig{
			Backends: backends,
			Features: []config.PluginConfig{{ID: "proxy-memory", Enabled: true, Config: featureNode}},
		},
		ModelAliases: gen.aliases,
	}
}

// proxyMemoryJ5SeedSession inserts one claimable session into the memory
// database, so the extraction phase has work to do.
func proxyMemoryJ5SeedSession(t *testing.T, memory *bun.DB, aLegID string, activityMS int64) {
	t.Helper()
	_, err := memory.NewRaw(`INSERT INTO proxy_memory_sessions
		(a_leg_id, tenant_id, principal_id, project_key, project_label, transcript, revision, last_activity_ms, source_seq)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, 1)`,
		aLegID, "tenant-j5", "alice", "project-j5", "/w/project", "[]", activityMS,
	).Exec(context.Background())
	require.NoError(t, err)
}

// proxyMemoryJ5Script wires the scripted provider and the billing seams into
// one generation's executor, so the phase reaches the provider without a
// network and without the money domain. The scripted provider answers for the
// generation's own backend instance: the generation's runtime backend
// inventory is exactly that instance.
func proxyMemoryJ5Script(exec *runtime.Executor, backend *proxyMemoryPhaseBackend, instanceID string) {
	exec.Backends = map[string]execbackend.Backend{instanceID: backend.Backend()}
	exec.CapsResolver = capabilities.MapResolver{instanceID: func(context.Context, routing.AttemptCandidate, aipapi.Call) aipapi.BackendCaps {
		return aipapi.NewBackendCaps(
			aipapi.CapabilityStreaming,
			aipapi.CapabilityStructuredOutputs,
			aipapi.CapabilityReasoning,
		)
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
}

// proxyMemoryJ5LiveLoops counts the proxy-memory background loops that are
// still running. It is the direct evidence that the candidate generations
// left no loop behind: the worker's and the capture's Stop return only after
// their loops exited, so a live loop names an unowned candidate resource.
func proxyMemoryJ5LiveLoops() int {
	buf := make([]byte, 4<<20)
	count := 0
	for range 1000 {
		n := goruntime.Stack(buf, true)
		stack := string(buf[:n])
		count = strings.Count(stack, "proxymemory.(*Worker).run") +
			strings.Count(stack, "proxymemory.(*Capture).run")
		if count == 0 {
			return 0
		}
		// A loop goroutine closes its done channel before it exits, so a dump
		// taken in that window can still name it. The scheduler gets a turn
		// and the dump is taken again; nothing sleeps.
		goruntime.Gosched()
	}
	return count
}

// proxyMemoryBillingSeams admits the background phases without the money
// domain: the composed proof is about the target compilation, not about the
// exposure of a phase.
type proxyMemoryBillingSeams struct{}

func (proxyMemoryBillingSeams) Check(context.Context, string) error { return nil }

func (proxyMemoryBillingSeams) Admit(_ context.Context, in runtime.BillingExposureAdmissionInput) (billingport.CallExposure, error) {
	return billingport.CallExposure{AccountID: "acct:proxy-memory", CallID: in.CallID, Status: billingport.ExposureOpen}, nil
}

// TestProxyMemoryTargetCompilerReachesTheFeatureAcrossGenerations is the
// composed proof of design §1: the production composition root binds the
// feature-owned target compiler from the generation's executor, and the
// proxy-memory feature consumes it before every phase dispatch.
//
// The two generations disagree on purpose. The first generation serves the
// "openai" instance and its phase route resolves through the "gpt5" alias.
// The second generation retired that instance, serves the "other" instance
// and dispatches the "gpt5b" route, whose alias carries the route parameter
// that raises the effort. The second generation's alias table still maps the
// first generation's phase route to the retired instance, so that route is a
// valid target for the first generation and an unknown backend for the
// second one.
//
// The first generation therefore keeps its own facts after the second
// generation compiled: it still compiles its target against its own backend
// inventory and still dispatches to the backend it serves. Nothing in this
// fixture publishes, pins, retires or quiesces a generation: both candidates
// are compiled and activated, and the first one is retained, not retired.
func TestProxyMemoryTargetCompilerReachesTheFeatureAcrossGenerations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	memoryPath := filepath.Join(t.TempDir(), "memory.db")

	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))

	continuityPath := filepath.Join(t.TempDir(), "continuity.db")
	first := proxyMemoryJ5Config(t, memoryPath, continuityPath, proxyMemoryJ5FirstGeneration())
	ps, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg:  first,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	backend := &proxyMemoryPhaseBackend{}
	firstWorker, firstExecutor, disposeFirst, err := runtimebundle.ProxyMemoryGenerationForTest(ctx, runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: first,
	})
	// The candidate generations own the worker and the capture they started,
	// and their resource ledger is the only owner of the stop actions. The
	// cleanup is registered as soon as a candidate exists, before any further
	// fallible step, and every release is attempted even when one of them
	// fails: neither a setup failure nor a disposal error may leave a
	// candidate's loops running.
	var disposeSecond func() error
	var memory *bun.DB
	if disposeFirst != nil {
		t.Cleanup(func() {
			var errs []error
			if disposeSecond != nil {
				errs = append(errs, disposeSecond())
			}
			errs = append(errs, disposeFirst())
			if memory != nil {
				errs = append(errs, memory.Close())
			}
			if loops := proxyMemoryJ5LiveLoops(); loops != 0 {
				errs = append(errs, fmt.Errorf("a candidate generation left %d background loop(s) behind", loops))
			}
			for _, err := range errs {
				if err != nil {
					t.Errorf("candidate cleanup: %v", err)
				}
			}
		})
	}
	require.NoError(t, err)
	proxyMemoryJ5Script(firstExecutor, backend, "openai")

	memory, err = db.OpenSQLiteBun(ctx, memoryPath)
	require.NoError(t, err)

	// The first generation dispatches with its own facts: the alias resolves
	// to the backend its own inventory holds.
	proxyMemoryJ5SeedSession(t, memory, "aleg-j5-before", time.Now().Add(-24*time.Hour).UnixMilli())
	firstWorker.Tick(ctx)
	observed := backend.extractions("gpt5")
	require.Len(t, observed, 1, "the first generation's phase must reach the provider")
	require.Equal(t, "openai", observed[0].backend)
	require.Equal(t, "openai/test-model", observed[0].model)
	require.Equal(t, "low", observed[0].options.ReasoningEffort)
	require.Equal(t, "application/json", observed[0].options.ResponseMIMEType)

	// The new generation is the reloaded one: it retired the "openai"
	// instance and serves the "other" one.
	second := proxyMemoryJ5Config(t, memoryPath, continuityPath, proxyMemoryJ5SecondGeneration())
	secondWorker, secondExecutor, dispose, err := runtimebundle.ProxyMemoryGenerationForTest(ctx, runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: second,
	})
	require.NoError(t, err)
	disposeSecond = dispose
	proxyMemoryJ5Script(secondExecutor, backend, "other")

	// The new generation dispatches with its own facts: its own route
	// resolves to the instance it serves, and its route parameter raises the
	// effort.
	proxyMemoryJ5SeedSession(t, memory, "aleg-j5-new", time.Now().Add(-24*time.Hour).UnixMilli())
	secondWorker.Tick(ctx)
	observed = backend.extractions("gpt5c")
	require.Len(t, observed, 1, "the new generation's phase must reach the provider with its own facts")
	require.Equal(t, "other", observed[0].backend)
	require.Equal(t, "other/test-model", observed[0].model)
	require.Equal(t, "high", observed[0].options.ReasoningEffort,
		"the new generation's route parameters must serve its own phase")

	// The retained generation keeps its own facts after the new generation
	// compiled: it still compiles its target against its own backend
	// inventory and still dispatches to the backend it serves. A compiler
	// shared with the new generation would refuse this target, because the
	// new generation's inventory no longer holds the retired instance.
	proxyMemoryJ5SeedSession(t, memory, "aleg-j5-after", time.Now().Add(-24*time.Hour).UnixMilli())
	firstWorker.Tick(ctx)
	observed = backend.extractions("gpt5")
	require.Len(t, observed, 2, "the retained generation must still dispatch after the new generation compiled")
	require.Equal(t, "openai", observed[1].backend)
	require.Equal(t, "openai/test-model", observed[1].model)
	require.Equal(t, "low", observed[1].options.ReasoningEffort,
		"the retained generation must keep its own facts")
}
