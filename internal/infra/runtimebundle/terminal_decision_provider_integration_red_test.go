package runtimebundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/internal/core/config"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

type terminalDecisionIntegrationProvider struct {
	id       string
	decideFn func(int32, terminaldecision.Input) terminaldecision.Decision
	calls    atomic.Int32
	inputs   chan terminaldecision.Input
}

func (p *terminalDecisionIntegrationProvider) ID() string { return p.id }

func (p *terminalDecisionIntegrationProvider) Decide(_ context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	n := p.calls.Add(1)
	if p.inputs != nil {
		p.inputs <- in
	}
	return p.decideFn(n, in), nil
}

type sealingIntegrationProvider struct {
	id          string
	idCalls     atomic.Int32
	decideCalls atomic.Int32
	sealed      atomic.Bool
}

func (p *sealingIntegrationProvider) ID() string {
	if p.sealed.Load() {
		panic("provider ID() invoked after sealing / on request hot path")
	}
	p.idCalls.Add(1)
	return p.id
}

func (p *sealingIntegrationProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	p.decideCalls.Add(1)
	return terminaldecision.Decision{
		Kind:       terminaldecision.DecisionAllowStop,
		ReasonCode: "test-stop",
	}, nil
}

func TestTerminalDecisionProviderFeatureIntegrationAndRemoval(t *testing.T) {
	t.Parallel()

	t.Run("allow-stop reaches core terminal finalization", func(t *testing.T) {
		provider := &terminalDecisionIntegrationProvider{
			id: "terminal-integration.allow-stop",
			decideFn: func(_ int32, _ terminaldecision.Input) terminaldecision.Decision {
				return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "test-allow"}
			},
		}
		bundle := compileIntegrationProviderGeneration(t, provider)
		body := postResponses(t, bundle.Handler(), "stub-default")
		if got := provider.calls.Load(); got != 1 {
			t.Fatalf("provider calls = %d, want one terminal evaluation", got)
		}
		if body == "" {
			t.Fatal("allow-stop request returned an empty response")
		}
	})

	t.Run("continue invokes core continuation transaction and publishes B2", func(t *testing.T) {
		provider := &terminalDecisionIntegrationProvider{
			id:     "terminal-integration.continue",
			inputs: make(chan terminaldecision.Input, 2),
			decideFn: func(n int32, _ terminaldecision.Input) terminaldecision.Decision {
				if n == 1 {
					return terminaldecision.Decision{
						Kind:       terminaldecision.DecisionContinue,
						ReasonCode: "test-continue",
						Continue: &terminaldecision.ContinuationIntent{
							TrajectoryRef: "test-trajectory",
							ControlRef:    "test-control",
							Instruction:   "continue the in-scope test task",
							Provenance:    "internal-control",
							ReasonCode:    "test-continue",
						},
					}
				}
				return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "test-allow"}
			},
		}
		bundle := compileIntegrationProviderGeneration(t, provider)
		body := postResponses(t, bundle.Handler(), "stub-default")
		if got := provider.calls.Load(); got != 2 {
			t.Fatalf("provider calls = %d, want B1 and B2 terminal evaluations", got)
		}
		first, second := <-provider.inputs, <-provider.inputs
		if first.Request.BLegID == "" || first.Request.BLegID == second.Request.BLegID {
			t.Fatalf("continuation did not publish a distinct B2 leg: B1=%q B2=%q", first.Request.BLegID, second.Request.BLegID)
		}
		if body == "" {
			t.Fatal("continued request returned an empty response")
		}
	})

	t.Run("removing registration preserves no-provider behavior", func(t *testing.T) {
		provider := &terminalDecisionIntegrationProvider{
			id: "terminal-integration.removed",
			decideFn: func(_ int32, _ terminaldecision.Input) terminaldecision.Decision {
				return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop}
			},
		}
		// 1. Compile enabled provider generation and execute one request; Decide count becomes one.
		registry := stdFactoryCatalog(t)
		factoryID := "terminal-decision-removal-test"
		require.NoError(t, registry.RegisterFeature(factoryID, func(yaml.Node) (aipfeature.FeatureBundle, error) {
			return testkit.FeatureBundle(t, factoryID, func(cs *aipfeature.ContributionSet) error {
				return aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, factoryID, terminaldecision.Provider(provider))
			}, nil), nil
		}))
		process, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
			Cfg:  processBaseConfig(),
			Log:  testkit.DiscardLogger(),
			Opts: &runtimebundle.BuildOptions{PluginRegistry: registry},
			Tracing: runtimebundle.ProcessTracing{
				Shutdown: func(context.Context) error { return nil },
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = process.Close() })

		var capturedEnabled httpcontract.TerminalDecisionPolicyInput
		bundle1, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
			Process:   process,
			Candidate: terminalDecisionCandidate(t, factoryID, true),
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				capturedEnabled = in.Operations.TerminalDecisionPolicy
				return stdhttp.ComposeStandardHTTP(ctx, cfg, log, in)
			},
		})
		require.NoError(t, err)

		ctx := context.Background()
		known1, avail1, err1 := capturedEnabled.FeatureStatus(ctx, "terminal-decision")
		require.NoError(t, err1)
		assert.True(t, known1)
		assert.True(t, avail1)
		assert.True(t, capturedEnabled.GenerationDefault("terminal-decision"))

		body1 := postResponses(t, bundle1.Handler(), "stub-default")
		require.NotEmpty(t, body1)
		require.Equal(t, int32(1), provider.calls.Load())
		require.NoError(t, bundle1.Close())

		// 2. Compile replacement generation using same process with feature absent/disabled.
		var capturedReplacement httpcontract.TerminalDecisionPolicyInput
		bundle2, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
			Process:   process,
			Candidate: terminalDecisionCandidate(t, "", false),
			Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
				capturedReplacement = in.Operations.TerminalDecisionPolicy
				return stdhttp.ComposeStandardHTTP(ctx, cfg, log, in)
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = bundle2.Close() })

		known2, avail2, err2 := capturedReplacement.FeatureStatus(ctx, "terminal-decision")
		require.NoError(t, err2)
		assert.True(t, known2)
		assert.False(t, avail2)
		assert.False(t, capturedReplacement.GenerationDefault("terminal-decision"))

		known2Unk, avail2Unk, err2Unk := capturedReplacement.FeatureStatus(ctx, "unknown-feature")
		require.NoError(t, err2Unk)
		assert.False(t, known2Unk)
		assert.False(t, avail2Unk)
		assert.False(t, capturedReplacement.GenerationDefault("unknown-feature"))

		assert.Same(t, process.StandardFeatures.TerminalDecisionPolicy(), capturedEnabled.Store)
		assert.Same(t, process.StandardFeatures.TerminalDecisionPolicy(), capturedReplacement.Store)

		// 3. Assert replacement TerminalDecisionProvider() is nil.
		genBundle, ok := bundle2.(*runtimebundle.GenerationBundle)
		require.True(t, ok)
		assert.Nil(t, genBundle.TerminalDecisionProvider())

		// 5. Execute replacement request and assert baseline response contains terminal-decision-text.
		body2 := postResponses(t, bundle2.Handler(), "stub-default")
		if !strings.Contains(body2, "terminal-decision-text") {
			t.Fatalf("no-provider behavior changed: response missing baseline text: %s", body2)
		}
		// 6. Assert removed provider Decide count remains one.
		if got := provider.calls.Load(); got != 1 {
			t.Fatalf("removed provider calls = %d, want exactly one", got)
		}
	})
}

func TestTerminalDecisionProvider_RequestPathUsesFrozenIdentityWithoutCallingID(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	provider := &sealingIntegrationProvider{
		id: "sealed-hot-path-provider",
	}

	registry := stdFactoryCatalog(t)
	factoryID := "terminal-decision-sealed-provider"
	require.NoError(t, registry.RegisterFeature(factoryID, func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, factoryID, func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, factoryID, terminaldecision.Provider(provider))
		}, nil), nil
	}))
	process, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  processBaseConfig(),
		Log:  logger,
		Opts: &runtimebundle.BuildOptions{PluginRegistry: registry},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = process.Close() })

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   process,
		Candidate: terminalDecisionCandidate(t, factoryID, true),
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Close() })

	idCallsBefore := provider.idCalls.Load()
	require.Greater(t, idCallsBefore, int32(0), "ID() must be called during composition")

	// Set sealed=true so any later ID call panics.
	provider.sealed.Store(true)

	// Execute real request through compiled generation
	body := postResponses(t, bundle.Handler(), "stub-default")
	require.NotEmpty(t, body)

	// Assert ID call count did not increase
	assert.Equal(t, idCallsBefore, provider.idCalls.Load(), "ID() must not be called during request execution")
	// Assert Decide count is exactly one
	assert.Equal(t, int32(1), provider.decideCalls.Load(), "Decide() must be called exactly once")

	// Parse each JSON log line into map[string]any to find terminal_decision_evaluation record
	var evalRecords []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logBuf.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record map[string]any
		err := json.Unmarshal([]byte(line), &record)
		require.NoError(t, err, "log line should be valid JSON: %s", line)
		if record["msg"] == "terminal_decision_evaluation" {
			evalRecords = append(evalRecords, record)
		}
	}
	require.Len(t, evalRecords, 1, "exactly one terminal_decision_evaluation log record expected")
	assert.Equal(t, "sealed-hot-path-provider", evalRecords[0]["provider_id"])
}

func TestTerminalDecisionGeneratedCandidateSuccessThroughCompileGeneration(t *testing.T) {
	t.Parallel()

	// 1. Process/base config has no terminal provider.
	ps := newProcessForGeneration(t)

	// 2. Build a generated candidate frozen set using normal feature.NewContributionSet, feature.Contribute, and Freeze.
	// 3. Candidate provider has ID generated-candidate-provider and Decide counter.
	provider := &sealingIntegrationProvider{
		id: "generated-candidate-provider",
	}
	candSrc := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(candSrc, aipfeature.PlaneTerminalDecisionProvider, "cand-plugin", terminaldecision.Provider(provider)))
	candFrozen := candSrc.Freeze()

	// 7. Candidate frozen identity before compile is exactly generated-candidate-provider.
	candID, hasID := aipfeature.FrozenIdentity(candFrozen, aipfeature.PlaneTerminalDecisionProvider)
	require.True(t, hasID)
	require.Equal(t, "generated-candidate-provider", candID)

	// 4. Pass frozen set through CandidateOpts.FeaturePlanes, wrapping Compose to capture httpcontract.StandardHTTPInput.
	var capturedInput httpcontract.StandardHTTPInput
	cfg := terminalDecisionCandidate(t, "", false)
	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes: candFrozen,
		},
		Compose: func(ctx context.Context, cfg *config.Config, log *slog.Logger, in httpcontract.StandardHTTPInput) (http.Handler, error) {
			capturedInput = in
			return stdhttp.ComposeStandardHTTP(ctx, cfg, log, in)
		},
	})
	// 5. Compile succeeds.
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Close() })

	// Inspect the compiled executor snapshot behind the composer input's narrow
	// ExecutorView.
	require.NotNil(t, capturedInput.Core.Executor)
	composerExec, ok := capturedInput.Core.Executor.(*coreruntime.Executor)
	require.True(t, ok, "composer input must carry the concrete executor")
	snapshot := composerExec.RuntimeSnapshot
	require.NotNil(t, snapshot)

	id, ok := snapshot.TerminalDecisionProviderIdentity()
	assert.True(t, ok)
	assert.Equal(t, "generated-candidate-provider", id)
	assert.Equal(t, terminaldecision.Provider(provider), snapshot.TerminalDecisionProvider())

	// 6. generation.TerminalDecisionProvider() is the same provider instance.
	genProj, ok := bundle.(*runtimebundle.GenerationBundle)
	require.True(t, ok)
	require.Equal(t, terminaldecision.Provider(provider), genProj.TerminalDecisionProvider())

	// 8. Decide count is zero after compile.
	require.Equal(t, int32(0), provider.decideCalls.Load())

	// Seal provider so any future ID() calls will panic on request path.
	idCallsBefore := provider.idCalls.Load()
	provider.sealed.Store(true)

	// 9. Execute a request and assert Decide count becomes one and response completes.
	body := postResponses(t, bundle.Handler(), "stub-default")
	require.NotEmpty(t, body)
	assert.Equal(t, idCallsBefore, provider.idCalls.Load(), "ID() must not be called during request execution")
	require.Equal(t, int32(1), provider.decideCalls.Load())

	// 10. Compile a malformed/conflicting candidate afterward and assert the successful prior generation remains usable.
	conflictCandSrc := aipfeature.NewContributionSet()
	provConflict1 := &sealingIntegrationProvider{id: "conflicting-provider-1"}
	provConflict2 := &sealingIntegrationProvider{id: "conflicting-provider-2"}
	require.NoError(t, aipfeature.Contribute(conflictCandSrc, aipfeature.PlaneTerminalDecisionProvider, "conflict-1", terminaldecision.Provider(provConflict1)))
	require.Error(t, aipfeature.Contribute(conflictCandSrc, aipfeature.PlaneTerminalDecisionProvider, "conflict-2", terminaldecision.Provider(provConflict2)))

	// Now try compiling with candidate planes that conflict with an enabled feature
	registry := stdFactoryCatalog(t)
	featFactory := "feature-conflict-factory"
	require.NoError(t, registry.RegisterFeature(featFactory, func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, featFactory, func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, featFactory, terminaldecision.Provider(provConflict1))
		}, nil), nil
	}))
	psConflict, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  processBaseConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: registry},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = psConflict.Close() })

	cfgConflict := terminalDecisionCandidate(t, featFactory, true) // has provConflict1
	_, errConflict := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   psConflict,
		Candidate: cfgConflict,
		CandidateOpts: &runtimebundle.BuildOptions{
			FeaturePlanes: candFrozen, // has provider -> conflict
		},
		Compose: stdhttp.ComposeStandardHTTP,
	})
	require.Error(t, errConflict)
	require.ErrorIs(t, errConflict, aipfeature.ErrExclusiveConflict)

	// Prior generation remains usable
	bodyAfter := postResponses(t, bundle.Handler(), "stub-default")
	require.NotEmpty(t, bodyAfter)
	assert.Equal(t, idCallsBefore, provider.idCalls.Load(), "ID() must not be called during second request execution")
	require.Equal(t, int32(2), provider.decideCalls.Load())
}

func compileIntegrationProviderGeneration(t *testing.T, provider terminaldecision.Provider) runtimebundle.GenerationRuntime {
	t.Helper()
	registry := stdFactoryCatalog(t)
	factoryID := "terminal-decision-integration"
	if err := registry.RegisterFeature(factoryID, func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, factoryID, func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneTerminalDecisionProvider, factoryID, provider)
		}, nil), nil
	}); err != nil {
		t.Fatalf("RegisterFeature(%q): %v", factoryID, err)
	}
	process, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  processBaseConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: registry},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	bundle := compileTerminalDecisionGeneration(t, process, terminalDecisionCandidate(t, factoryID, true))
	t.Cleanup(func() { _ = bundle.Close() })
	return bundle
}
