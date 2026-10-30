// Failure and ownership seams for task 3.2 of
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, requirements 3.2, 3.3,
// 4.1-4.7, 10.3-10.6, 12.1-12.2).
//
// Generic runtime only. Everything here names no concrete feature: the provider
// is the same anonymous `proxy_control` model control tool task 3.1 contributed
// through feature.PlaneControlToolProvider, re-armed here so each test can prove
// which provider methods the request path may and may not call.
package runtime_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// algSeamControlProvider is the generic control provider used by the task 3.2
// seam tests. It counts each provider method so a test can prove which calls the
// request path makes, and can be armed after composition to fail closed or to
// trip a live-identity wire.
type algSeamControlProvider struct {
	id   string
	spec controltool.Spec

	mu           sync.Mutex
	idPanic      bool
	specPanic    bool
	specCalls    int
	handledCalls int
}

func (p *algSeamControlProvider) ID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idPanic {
		panic("live control provider identity read on the request path")
	}
	return p.id
}

func (p *algSeamControlProvider) Spec() controltool.Spec {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.specPanic {
		panic("control spec unavailable")
	}
	p.specCalls++
	return p.spec
}

func (p *algSeamControlProvider) Handle(context.Context, controltool.CompletedCall, controltool.Meta) (controltool.Outcome, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handledCalls++
	return controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: "control_handler_reached"}, nil
}

// arm resets the request-time counters after composition validated identity and
// spec, and optionally installs the live-identity tripwire that a
// generation-pinned request path must never pull.
func (p *algSeamControlProvider) arm(liveIDPanic bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.idPanic = liveIDPanic
	p.specCalls = 0
	p.handledCalls = 0
}

func (p *algSeamControlProvider) failSpec() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.specPanic = true
}

func (p *algSeamControlProvider) counts() (specs, handled int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.specCalls, p.handledCalls
}

// algSeamGeneration composes the provider the way a feature generation does
// (validate identity plus frozen spec, through the exclusive plane) and then
// re-arms it for request-time observation.
func algSeamGeneration(t *testing.T, liveIDPanic bool) (*algSeamControlProvider, aipfeature.FrozenPlaneSet) {
	t.Helper()
	provider := &algSeamControlProvider{id: algControlProviderID, spec: algControlTestSpec()}
	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneControlToolProvider, algControlProviderID, controltool.Provider(provider)),
		"generation composition must accept the generic control spec")
	provider.arm(liveIDPanic)
	return provider, cs.Freeze()
}

// algCountSteps counts logged steps carrying prefix. It turns the shared
// candidate-open order log into an exact capability-resolution count.
func algCountSteps(steps []string, prefix string) int {
	count := 0
	for _, step := range steps {
		if strings.HasPrefix(step, prefix) {
			count++
		}
	}
	return count
}

// algSeamBackend is one candidate backend whose observed call is owned by the
// test. caps is returned for every candidate-specific resolution so per-candidate
// activation is provable.
func algSeamBackend(log *algOrderLog, caps aipapi.BackendCaps, backendID string, open func(aipapi.Call) (aipapi.ManagedEventStream, error)) execbackend.Backend {
	return execbackend.Backend{
		Caps:                    caps,
		TransportCaps:           algStreamingTransport(),
		EnforcesMaxOutputTokens: true,
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			log.add("resolve_caps:" + strings.TrimSpace(cand.Primary.Backend) + ":" + algControlMeta(call))
			return caps
		},
		Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			stream, err := open(call)
			if err != nil {
				log.add("open_failed:" + strings.TrimSpace(cand.Primary.Backend))
				return nil, err
			}
			log.add("open:" + strings.TrimSpace(cand.Primary.Backend) + ":" + algControlMeta(call))
			return stream, nil
		},
	}
}

func algSeamFinishStream() aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventResponseFinished},
	})
}

// algSeamExecutor builds the shared secure executor with the live
// conversation-view reader/observer and clamp preview, so the final
// conversation-view reassertion and therefore the control reassertion both run.
func algSeamExecutor(t *testing.T, backends map[string]execbackend.Backend, planes aipfeature.FrozenPlaneSet, log *algOrderLog) *runtime.Executor {
	t.Helper()
	ex := algSecureExecutor(t, backends, hooks.New(hooks.Config{}), extensions.SnapshotOptions{FeaturePlanes: planes})
	algWireOpenOrderProbes(t, ex, log, algLiveConversationSnapshot(t))
	return ex
}

func algSeamCall(selector, id string) *aipapi.Call {
	call := pdBaseCall(selector)
	call.ID = id
	call.Messages = append(call.Messages, algNeverBackendMessage())
	return call
}

// TestControlToolProjection_suppressedProviderPerformsNoControlWork pins the
// generic suppression bypass: a suppressed control-tool provider must be
// skipped without resolving its spec, without an extra candidate capability
// lookup, and without any projection, exactly like an absent provider. The
// suppressed identity is the generation-frozen one, so no provider-name or
// feature-name branch exists.
func TestControlToolProjection_suppressedProviderPerformsNoControlWork(t *testing.T) {
	t.Parallel()

	log := &algOrderLog{}
	provider, planes := algSeamGeneration(t, false)
	opened := &atomic.Int32{}
	openCall := &atomic.Pointer[aipapi.Call]{}

	ex := algSeamExecutor(t, map[string]execbackend.Backend{algControlBackendID: algSeamBackend(log, algOrderedToolsCaps(), algControlBackendID,
		func(call aipapi.Call) (aipapi.ManagedEventStream, error) {
			cloned := aipapi.CloneCall(call)
			openCall.Store(&cloned)
			opened.Add(1)
			return algSeamFinishStream(), nil
		})}, planes, log)
	require.NotNil(t, ex.RuntimeSnapshot.ControlToolProvider(), "the generation still admits the generic control provider")

	baseline := algSeamBaselineResolverCount(t)
	require.Equal(t, 2, baseline, "the shared baseline resolves candidate capabilities exactly twice; the suppression seam is measured against it")

	ctx := execctx.WithSuppressedPluginIDs(principalCtx("control-suppressed"), []string{algControlProviderID})
	call := algSeamCall(algControlBackendID+":"+algControlBackendModel, "control-suppressed")
	clientChoice := call.ToolChoice
	stream, err := ex.Execute(ctx, call)
	require.NoError(t, err, "a suppressed control provider must leave the candidate usable")
	t.Cleanup(func() { _ = stream.Close() })
	_ = algCollectEOF(t, stream)

	require.Equal(t, int32(1), opened.Load(), "exactly one candidate must reach Backend.Open")
	got := openCall.Load()
	require.NotNil(t, got, "Backend.Open must receive the backend-effective call")
	require.NoError(t, got.Validate(), "the backend-effective call must stay canonically valid")
	assert.Zero(t, algCountToolDef(*got, algControlToolName), "a suppressed control provider must append no control tool")
	assert.Zero(t, algCountPlainText(*got, algControlInstruction), "a suppressed control provider must append no control instruction")
	assert.Empty(t, got.Tools, "the client catalog must stay untouched")
	assert.Equal(t, clientChoice, got.ToolChoice, "the client tool choice must never be rewritten")
	assert.Len(t, got.Extensions, 0, "suppression must not serialize control provenance")
	assert.Zero(t, algCountPlainText(*got, algNeverBackendText), "the conversation-view reassertion must still run")

	specs, handled := provider.counts()
	assert.Zero(t, specs, "a suppressed control provider must never be asked for its spec")
	assert.Zero(t, handled, "the control handler must not run for a suppressed provider")

	steps := log.snapshot()
	assert.Equal(t, baseline, algCountSteps(steps, "resolve_caps:"),
		"a suppressed control provider must not add a candidate capability resolution; steps=%v", steps)
	for i, step := range steps {
		assert.NotContains(t, step, "control_tools=1", "no probe may observe a control tool for a suppressed provider; step[%d]=%q", i, step)
		assert.NotContains(t, step, "control_text=1", "no probe may observe a control instruction for a suppressed provider; step[%d]=%q", i, step)
	}
}

// algSeamBaselineResolverCount measures the shared candidate-open baseline
// capability resolutions for a generation that admits no control provider, so a
// present-provider test can state its one extra preliminary lookup exactly.
func algSeamBaselineResolverCount(t *testing.T) int {
	t.Helper()
	baselineLog := &algOrderLog{}
	opened := &atomic.Int32{}
	ex := algSeamExecutor(t, map[string]execbackend.Backend{algControlBackendID: algSeamBackend(baselineLog, algOrderedToolsCaps(), algControlBackendID,
		func(aipapi.Call) (aipapi.ManagedEventStream, error) {
			opened.Add(1)
			return algSeamFinishStream(), nil
		})}, aipfeature.NewContributionSet().Freeze(), baselineLog)
	require.Nil(t, ex.RuntimeSnapshot.ControlToolProvider(), "the baseline generation must admit no control provider")

	stream, err := ex.Execute(principalCtx("control-baseline"), algSeamCall(algControlBackendID+":"+algControlBackendModel, "control-baseline"))
	require.NoError(t, err, "a generation without a control provider must stay usable")
	t.Cleanup(func() { _ = stream.Close() })
	_ = algCollectEOF(t, stream)
	require.Equal(t, int32(1), opened.Load())
	return algCountSteps(baselineLog.snapshot(), "resolve_caps:")
}

// TestControlToolProjection_presentProviderAddsExactlyOnePreliminaryLookup
// characterizes the cost the new stage is allowed to add: an unsuppressed
// provider costs one extra, purely preliminary candidate capability resolution
// between the request hooks and the authoritative post-hook rederive. It proves
// the stage is not free, so the absent and suppressed paths stay honest.
func TestControlToolProjection_presentProviderAddsExactlyOnePreliminaryLookup(t *testing.T) {
	t.Parallel()

	log := &algOrderLog{}
	_, planes := algSeamGeneration(t, false)
	opened := &atomic.Int32{}
	ex := algSeamExecutor(t, map[string]execbackend.Backend{algControlBackendID: algSeamBackend(log, algOrderedToolsCaps(), algControlBackendID,
		func(aipapi.Call) (aipapi.ManagedEventStream, error) {
			opened.Add(1)
			return algSeamFinishStream(), nil
		})}, planes, log)

	baseline := algSeamBaselineResolverCount(t)
	stream, err := ex.Execute(principalCtx("control-lookup-cost"), algSeamCall(algControlBackendID+":"+algControlBackendModel, "control-lookup-cost"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	_ = algCollectEOF(t, stream)
	require.Equal(t, int32(1), opened.Load())

	steps := log.snapshot()
	require.NotEmpty(t, steps, "expected candidate-open ordering probes")
	// The eligible harness is the only place a request hook adds a client tool.
	// Measure the delta against the shared baseline, which runs the same
	// pipeline without a control provider.
	assert.Equal(t, baseline+1, algCountSteps(steps, "resolve_caps:"),
		"an unsuppressed control provider costs exactly one preliminary capability resolution; steps=%v", steps)
}

// TestControlToolProjection_providerSpecPanicFailsClosed pins the extension
// safety boundary: a provider that panics while producing its spec must fail the
// candidate open closed with a bounded, content-free error, must not open the
// backend, and must not leak the panic payload into client-visible text.
func TestControlToolProjection_providerSpecPanicFailsClosed(t *testing.T) {
	t.Parallel()

	log := &algOrderLog{}
	provider, planes := algSeamGeneration(t, false)
	provider.failSpec()
	opened := &atomic.Int32{}
	ex := algSeamExecutor(t, map[string]execbackend.Backend{algControlBackendID: algSeamBackend(log, algOrderedToolsCaps(), algControlBackendID,
		func(aipapi.Call) (aipapi.ManagedEventStream, error) {
			opened.Add(1)
			return algSeamFinishStream(), nil
		})}, planes, log)

	stream, err := ex.Execute(principalCtx("control-spec-panic"), algSeamCall(algControlBackendID+":"+algControlBackendModel, "control-spec-panic"))
	if stream != nil {
		t.Cleanup(func() { _ = stream.Close() })
	}
	require.Error(t, err, "a provider spec panic must fail the candidate closed")
	assert.Zero(t, opened.Load(), "a failed projection must fail before Backend.Open")
	assert.Contains(t, err.Error(), "control tool projection", "the error must name the failed stage; err=%v", err)
	assert.Contains(t, err.Error(), "boundary=extension_execution",
		"the spec call must run behind the extension safety boundary; err=%v", err)
	assert.NotContains(t, err.Error(), "control spec unavailable",
		"a provider panic payload must never reach the error text; err=%v", err)
	_, handled := provider.counts()
	assert.Zero(t, handled, "the control handler must not run when the spec is unavailable")
}

// TestControlToolProjection_candidateCapabilityPanicFailsClosed pins the
// backend safety boundary around the preliminary eligibility resolution. The
// second candidate capability resolution is the projection stage's own lookup, so
// a panic there is isolated to that boundary and reported with the backend
// boundary instead of reaching Backend.Open.
func TestControlToolProjection_candidateCapabilityPanicFailsClosed(t *testing.T) {
	t.Parallel()

	log := &algOrderLog{}
	_, planes := algSeamGeneration(t, false)
	opened := &atomic.Int32{}
	var lookups atomic.Int32
	caps := algOrderedToolsCaps()
	backend := execbackend.Backend{
		Caps:                    caps,
		TransportCaps:           algStreamingTransport(),
		EnforcesMaxOutputTokens: true,
		ResolveCaps: func(context.Context, aipapi.Call, routing.AttemptCandidate) aipapi.BackendCaps {
			if lookups.Add(1) == 2 {
				log.add("resolve_caps_panic")
				panic("capability resolution exploded")
			}
			log.add("resolve_caps:" + algControlBackendID)
			return caps
		},
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opened.Add(1)
			return algSeamFinishStream(), nil
		},
	}
	ex := algSeamExecutor(t, map[string]execbackend.Backend{algControlBackendID: backend}, planes, log)

	stream, err := ex.Execute(principalCtx("control-caps-panic"), algSeamCall(algControlBackendID+":"+algControlBackendModel, "control-caps-panic"))
	if stream != nil {
		t.Cleanup(func() { _ = stream.Close() })
	}
	require.Error(t, err, "a preliminary capability resolution panic must fail the candidate closed")
	assert.Zero(t, opened.Load(), "a failed projection must fail before Backend.Open")
	assert.Equal(t, int32(2), lookups.Load(), "the failure must be isolated to the projection stage's own lookup")
	assert.Contains(t, err.Error(), "control tool candidate capabilities", "the error must name the failed resolution; err=%v", err)
	assert.Contains(t, err.Error(), "boundary=backend_attempt", "the lookup must run behind the backend safety boundary; err=%v", err)
	assert.NotContains(t, err.Error(), "exploded", "a capability panic payload must never reach the error text; err=%v", err)
}

// TestControlToolProjection_activationPinsFrozenGenerationIdentity proves the
// request path never reads the provider's live identity: after composition
// installs the tripwire, an armed generation still projects its control contract
// from the cached validated identity alone.
func TestControlToolProjection_activationPinsFrozenGenerationIdentity(t *testing.T) {
	t.Parallel()

	log := &algOrderLog{}
	_, planes := algSeamGeneration(t, true)
	require.NotNil(t, planes, "the frozen plane set must exist")
	opened := &atomic.Int32{}
	openCall := &atomic.Pointer[aipapi.Call]{}
	ex := algSeamExecutor(t, map[string]execbackend.Backend{algControlBackendID: algSeamBackend(log, algOrderedToolsCaps(), algControlBackendID,
		func(call aipapi.Call) (aipapi.ManagedEventStream, error) {
			cloned := aipapi.CloneCall(call)
			openCall.Store(&cloned)
			opened.Add(1)
			return algSeamFinishStream(), nil
		})}, planes, log)

	stream, err := ex.Execute(principalCtx("control-frozen-id"), algSeamCall(algControlBackendID+":"+algControlBackendModel, "control-frozen-id"))
	require.NoError(t, err, "a frozen generation identity must be sufficient to activate")
	t.Cleanup(func() { _ = stream.Close() })
	_ = algCollectEOF(t, stream)

	require.Equal(t, int32(1), opened.Load())
	got := openCall.Load()
	require.NotNil(t, got, "Backend.Open must receive the backend-effective call")
	assert.Equal(t, 1, algCountToolDef(*got, algControlToolName), "the approved control tool must be projected")
	assert.Equal(t, 1, algCountPlainText(*got, algControlInstruction), "the approved control instruction must be projected")
}

// TestControlToolProjection_reloadPinsActivationToItsOwnGeneration pins the
// generation pin (requirement 10.3): a request admitted by a generation with a
// control provider keeps its own control activation, while the next request on a
// generation without one receives no control provenance at all.
func TestControlToolProjection_reloadPinsActivationToItsOwnGeneration(t *testing.T) {
	t.Parallel()

	log := &algOrderLog{}
	_, planes := algSeamGeneration(t, false)
	opened := &atomic.Int32{}
	openCall := &atomic.Pointer[aipapi.Call]{}
	ex := algSeamExecutor(t, map[string]execbackend.Backend{algControlBackendID: algSeamBackend(log, algOrderedToolsCaps(), algControlBackendID,
		func(call aipapi.Call) (aipapi.ManagedEventStream, error) {
			cloned := aipapi.CloneCall(call)
			openCall.Store(&cloned)
			opened.Add(1)
			return algSeamFinishStream(), nil
		})}, planes, log)

	first, err := ex.Execute(principalCtx("control-generation-1"), algSeamCall(algControlBackendID+":"+algControlBackendModel, "control-generation-1"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	_ = algCollectEOF(t, first)
	require.Equal(t, int32(1), opened.Load())
	pinned := openCall.Load()
	require.NotNil(t, pinned)
	require.Equal(t, 1, algCountToolDef(*pinned, algControlToolName), "the first generation projects its control tool")

	// Publish a new generation without a control-tool provider, exactly as a
	// reload that withdraws the feature does.
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: aipfeature.NewContributionSet().Freeze(),
	})
	require.Nil(t, ex.RuntimeSnapshot.ControlToolProvider(), "the new generation must admit no control provider")

	second, err := ex.Execute(principalCtx("control-generation-2"), algSeamCall(algControlBackendID+":"+algControlBackendModel, "control-generation-2"))
	require.NoError(t, err, "a generation without a control provider must stay usable")
	t.Cleanup(func() { _ = second.Close() })
	_ = algCollectEOF(t, second)
	require.Equal(t, int32(2), opened.Load())

	afterReload := openCall.Load()
	require.NotNil(t, afterReload)
	assert.Zero(t, algCountToolDef(*afterReload, algControlToolName),
		"a newly admitted request must not inherit the previous generation's control activation")
	assert.Zero(t, algCountPlainText(*afterReload, algControlInstruction),
		"a newly admitted request must not inherit the previous generation's control instruction")
}

// TestControlToolProjection_replacementAttemptRecomputesActivationFromItsOwnCandidate
// pins per-candidate recomputation and attempt independence (requirement 4.7):
// the losing candidate's ineligible activation is discarded with it, and the
// replacement attempt projects from its own capabilities rather than inheriting
// or sharing the first attempt's state.
func TestControlToolProjection_replacementAttemptRecomputesActivationFromItsOwnCandidate(t *testing.T) {
	t.Parallel()

	const secondBackendID = "openai-control-b"
	log := &algOrderLog{}
	_, planes := algSeamGeneration(t, false)
	loserOpens := &atomic.Int32{}
	winnerOpens := &atomic.Int32{}
	winnerCall := &atomic.Pointer[aipapi.Call]{}

	ex := algSeamExecutor(t, map[string]execbackend.Backend{
		algControlBackendID: algSeamBackend(log, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), algControlBackendID,
			func(aipapi.Call) (aipapi.ManagedEventStream, error) {
				loserOpens.Add(1)
				return nil, aipapi.ErrRecoverablePreOutput
			}),
		secondBackendID: algSeamBackend(log, algOrderedToolsCaps(), secondBackendID,
			func(call aipapi.Call) (aipapi.ManagedEventStream, error) {
				cloned := aipapi.CloneCall(call)
				winnerCall.Store(&cloned)
				winnerOpens.Add(1)
				return algSeamFinishStream(), nil
			}),
	}, planes, log)

	selector := algControlBackendID + ":" + algControlBackendModel + "|" + secondBackendID + ":" + algControlBackendModel
	stream, err := ex.Execute(principalCtx("control-replacement"), algSeamCall(selector, "control-replacement"))
	require.NoError(t, err, "a recoverable pre-output failure must still reach a replacement candidate")
	t.Cleanup(func() { _ = stream.Close() })
	_ = algCollectEOF(t, stream)

	require.Equal(t, int32(1), loserOpens.Load(), "the ineligible candidate must be opened once and then replaced")
	require.Equal(t, int32(1), winnerOpens.Load(), "the replacement candidate must open exactly once")
	got := winnerCall.Load()
	require.NotNil(t, got, "the replacement candidate must reach Backend.Open")
	require.NoError(t, got.Validate(), "the replacement backend-effective call must stay canonically valid")
	assert.Equal(t, 1, algCountToolDef(*got, algControlToolName),
		"the replacement attempt projects from its own tools-capable candidate")
	assert.Equal(t, 1, algCountPlainText(*got, algControlInstruction),
		"the replacement attempt projects its own control instruction")
	assert.Empty(t, got.Tools[:len(got.Tools)-1], "no client tool may be invented by the projection")
	assert.Len(t, got.Extensions, 0, "control activation must not serialize provenance into canonical extensions")

	// The replaced attempt resolved capabilities for a tools-less backend, so its
	// own eligibility lookup never projected a control contract, while the
	// replacement attempt projected from its own tools-capable candidate.
	steps := log.snapshot()
	loserResolveIdx := algIndexOf(steps, "resolve_caps:"+algControlBackendID)
	loserOpenIdx := algIndexOf(steps, "open_failed:"+algControlBackendID)
	winnerResolveIdx := algLastIndexOf(steps, "resolve_caps:"+secondBackendID)
	require.GreaterOrEqual(t, loserResolveIdx, 0, "the first candidate must resolve its own capabilities; steps=%v", steps)
	require.GreaterOrEqual(t, loserOpenIdx, loserResolveIdx, "the first candidate must fail at open; steps=%v", steps)
	require.Greater(t, winnerResolveIdx, loserOpenIdx, "the replacement candidate must resolve its own capabilities; steps=%v", steps)
	require.Contains(t, steps[loserResolveIdx], "control_tools=0",
		"the replaced attempt's own capability resolution must never see a control tool; steps=%v", steps)
}
