// IN-FLIGHT half of the generation pin for the private control-tool contract
// (spec: .kiro/specs/agent-loop-explicit-completion-protocol, design Concurrency
// and Lifecycle "Reload can switch strategies for new requests without changing
// in-flight strategy" and "Withdrawal leaves no durable protocol row or stale
// overlay to clean"; requirements 10.3 and 10.6).
//
// Why this cell sits next to
// TestControlToolProjection_reloadPinsActivationToItsOwnGeneration: that cell is
// purely sequential (request 1 finishes, then the snapshot is replaced, then
// request 2 is admitted), so it certifies only the newly-admitted half of
// requirement 10.3. This cell certifies the half that the conformance cell
// TestPreferredProtocolTransportE2E_reloadSwitchesStrategyForNewlyAdmittedTurns
// explicitly declines to assert: a request that is ALREADY admitted when a newer
// generation is published keeps the strategy and control-tool provenance of its
// original generation for every later stage of its own lifetime.
//
// The reload seam here is the PRODUCTION one, not a harness field write.
// Executor.RuntimeSnapshot is a plain field production never mutates after
// composition: a reload publishes a NEW immutable generation object carrying its
// own executor through runtimehost.Manager.Publish (an atomic pointer swap) and
// retires the previous generation without touching it. That is the only shape in
// which "a newer generation was published while this request is in flight" is
// expressible at all, and it is why this cell never swaps RuntimeSnapshot under
// a live request — that is a harness-side data race, not a production reload.
// The publication below happens on the in-flight request's own goroutine, so the
// cell is race free by construction rather than by timing.
//
// Generic runtime only. Generation 1 contributes the same anonymous
// `proxy_control` model control tool any feature generation would contribute
// through feature.PlaneControlToolProvider and generation 2 withdraws it, which
// is exactly the shape of a real Agent Loop Guard strategy reload from the
// preferred attempt_completion protocol to the legacy semantic-verifier
// strategy: same request, same wire, same projection stage, different strategy.
package runtime_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// algReloadReplacementBackendID is the failover candidate the in-flight request
// reaches AFTER the newer generation has been published. Its projection is the
// in-flight half of requirement 10.3.
const algReloadReplacementBackendID = "openai-reload-replacement"

// algGenerationPlane is one immutable published request plane carrying a real
// executor, exactly as runtimebundle's generation bundle co-locates Handler() and
// ExecutorView() in one immutable object that is never rebound after publication.
// It is the production reload unit, not a test-only reload switch.
type algGenerationPlane struct {
	exec *runtime.Executor
}

func (p *algGenerationPlane) ExecutorView() aipsdk.ExecutorView { return p.exec }

func (p *algGenerationPlane) Handler() http.Handler { return http.NotFoundHandler() }

// Quiesce admits no new generation-owned work and closes nothing: the plane owns
// no closer here, so retiring generation 1 must leave its executor untouched,
// which is what lets the in-flight request keep running on it.
func (p *algGenerationPlane) Quiesce(context.Context) error { return nil }

func (p *algGenerationPlane) Close() error { return nil }

var (
	_ runtimehost.PublishedRequestPlane = (*algGenerationPlane)(nil)
	_ runtimehost.ExecutorProvider      = (*algGenerationPlane)(nil)
)

// algReloadControlProvider is generation 1's anonymous control provider. It
// records every handoff with the frozen attempt provenance the pinned
// generation handed over, so the response side of the in-flight pin is provable
// and not merely inferred from the projected tool definition.
type algReloadControlProvider struct {
	mu      sync.Mutex
	handled int
	metas   []controltool.Meta
}

func (p *algReloadControlProvider) ID() string             { return algControlProviderID }
func (p *algReloadControlProvider) Spec() controltool.Spec { return algControlTestSpec() }

func (p *algReloadControlProvider) Handle(_ context.Context, _ controltool.CompletedCall, meta controltool.Meta) (controltool.Outcome, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handled++
	p.metas = append(p.metas, meta)
	return controltool.Outcome{
		Kind:       controltool.OutcomeComplete,
		ResultText: "generation 1 answered the pinned control call",
		ReasonCode: "control_complete",
	}, nil
}

func (p *algReloadControlProvider) handoffs() (int, []controltool.Meta) {
	p.mu.Lock()
	defer p.mu.Unlock()
	metas := make([]controltool.Meta, len(p.metas))
	copy(metas, p.metas)
	return p.handled, metas
}

// algReloadExecutor builds one real executor for one generation. withControl
// selects whether that generation admits the anonymous control tool at all.
func algReloadExecutor(t *testing.T, log *algOrderLog, backends map[string]execbackend.Backend, withControl bool) (*runtime.Executor, *algReloadControlProvider) {
	t.Helper()
	cs := aipfeature.NewContributionSet()
	var provider *algReloadControlProvider
	if withControl {
		concrete := &algReloadControlProvider{}
		var admitted controltool.Provider = concrete
		provider = concrete
		require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneControlToolProvider, algControlProviderID, admitted),
			"generation composition must accept the generic control spec")
	}
	ex := algSeamExecutor(t, backends, cs.Freeze(), log)
	if withControl {
		require.NotNil(t, ex.RuntimeSnapshot.ControlToolProvider(),
			"generation 1 must admit the generic control provider")
	} else {
		require.Nil(t, ex.RuntimeSnapshot.ControlToolProvider(),
			"generation 2 must admit no control provider at all, exactly like a strategy withdrawal")
	}
	return ex, provider
}

// TestControlToolProjection_inFlightRequestKeepsAdmittedGenerationAcrossReload is
// the in-flight half of requirement 10.3.
//
// The request is admitted on generation 1, which admits the generic control
// tool. Its first candidate then fails with a recoverable pre-output transport
// error, and that same failure publishes generation 2 (the strategy withdrawal)
// through the real Manager while the request is still in flight. The request's
// replacement attempt — a strictly later stage of the same in-flight request —
// must still project generation 1's control tool and instruction, and
// generation 1's provider must still handle the control call that attempt
// completes. The next request, admitted on the newly published generation, must
// receive no control provenance at all.
//
// Every provenance claim is an exact-tool-name and exact-instruction-text
// assertion against the call the production path itself built, and the temporal
// precondition (generation 2 already active when the replacement attempt opens)
// is asserted, so an implementation that resolved the provider from the CURRENT
// generation rather than the admitted one fails this cell instead of passing it
// quietly.
func TestControlToolProjection_inFlightRequestKeepsAdmittedGenerationAcrossReload(t *testing.T) {
	t.Parallel()

	m := runtimehost.NewManager(4, nil)
	gen1Log, gen2Log := &algOrderLog{}, &algOrderLog{}
	inFlightLog := &algOrderLog{}
	replacementLog := &algOrderLog{}

	var reloadPublished atomic.Bool
	var reloadErr atomic.Value
	var replacementCall atomic.Pointer[aipapi.Call]
	var activeAtReplacement atomic.Int64
	var newlyAdmittedCall atomic.Pointer[aipapi.Call]

	// The replacement attempt completes generation 1's control call, so the
	// response side of the pin is observable too and never reaches the client.
	replacementStream := &controlLifecycleFailoverStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventToolCallStarted, ToolCallID: "reload-control-1", ToolName: algControlToolName},
			{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "reload-control-1", Delta: `{"note":"generation-1"}`},
			{Kind: aipapi.EventToolCallFinished, ToolCallID: "reload-control-1", ToolName: algControlToolName},
			{Kind: aipapi.EventResponseFinished},
		},
	}

	// gen2Exec is the candidate generation a reload compiles ahead of its
	// publication. It is declared first because generation 1's first candidate
	// open is where the in-flight reload publishes it.
	var gen2Exec *runtime.Executor

	gen1Backends := map[string]execbackend.Backend{
		algControlBackendID: algSeamBackend(inFlightLog, algOrderedToolsCaps(), algControlBackendID,
			func(aipapi.Call) (aipapi.ManagedEventStream, error) {
				if reloadPublished.CompareAndSwap(false, true) {
					// The real production reload, executed at the exact moment the
					// in-flight request has lost its first attempt. Manager.Publish
					// returns a nil error on a clean publication, so the outcome is
					// recorded ONLY when it is non-nil: storing a nil error into an
					// atomic.Value panics, and the executor's safety.CallValue would
					// swallow that panic, converting a clean reload into a recovered
					// failure that aborts every remaining statement of this backend
					// open and makes this cell's own reload assertion unreachable.
					if perr := m.Publish(m.PrepareRequestPlane("gen-2", &algGenerationPlane{exec: gen2Exec})); perr != nil {
						reloadErr.Store(perr)
					}
				}
				return nil, aipapi.ErrRecoverablePreOutput
			}),
		algReloadReplacementBackendID: algSeamBackend(replacementLog, algOrderedToolsCaps(), algReloadReplacementBackendID,
			func(call aipapi.Call) (aipapi.ManagedEventStream, error) {
				cloned := aipapi.CloneCall(call)
				replacementCall.Store(&cloned)
				if active := m.Active(); active != nil {
					activeAtReplacement.Store(active.ID())
				}
				return replacementStream, nil
			}),
	}
	gen2Backends := map[string]execbackend.Backend{
		algControlBackendID: algSeamBackend(gen2Log, algOrderedToolsCaps(), algControlBackendID,
			func(call aipapi.Call) (aipapi.ManagedEventStream, error) {
				cloned := aipapi.CloneCall(call)
				newlyAdmittedCall.Store(&cloned)
				return algSeamFinishStream(), nil
			}),
		algReloadReplacementBackendID: algSeamBackend(gen2Log, algOrderedToolsCaps(), algReloadReplacementBackendID,
			func(aipapi.Call) (aipapi.ManagedEventStream, error) { return algSeamFinishStream(), nil }),
	}

	gen1Exec, gen1Provider := algReloadExecutor(t, gen1Log, gen1Backends, true)
	gen2Exec, _ = algReloadExecutor(t, gen2Log, gen2Backends, false)

	gen1 := m.PrepareRequestPlane("gen-1", &algGenerationPlane{exec: gen1Exec})
	require.NoError(t, m.Publish(gen1))
	require.Equal(t, int64(1), m.Active().ID(), "generation 1 must be the active generation at admission")
	t.Cleanup(func() {
		if active := m.Active(); active != nil {
			_, _ = m.RetireGeneration(context.Background(), active)
		}
		_, _ = m.RetireGeneration(context.Background(), gen1)
	})

	// --- the in-flight request ------------------------------------------------
	selector := algControlBackendID + ":" + algControlBackendModel + "|" + algReloadReplacementBackendID + ":" + algControlBackendModel
	stream, err := runtimehost.NewGenerationExecutor(m).Execute(principalCtx("control-inflight-reload"),
		algSeamCall(selector, "control-inflight-reload"))
	require.NoError(t, err, "a recoverable pre-output failure must still reach a replacement candidate")
	t.Cleanup(func() { _ = stream.Close() })
	released := algCollectEOF(t, stream)

	if err, _ := reloadErr.Load().(error); err != nil {
		t.Fatalf("the in-flight reload must publish cleanly: %v", err)
	}
	require.True(t, reloadPublished.Load(), "the reload must have been published while the request was in flight")
	require.Equal(t, int64(2), m.Active().ID(),
		"generation 2 must be the active generation once the in-flight request's replacement attempt opens")
	require.Equal(t, int64(2), activeAtReplacement.Load(),
		"the in-flight replacement attempt must have been opened with generation 2 already active")

	// The in-flight half: the replacement attempt of the request admitted on
	// generation 1 still carries generation 1's own control contract.
	got := replacementCall.Load()
	require.NotNil(t, got, "the in-flight request must reach its replacement candidate")
	require.NoError(t, got.Validate(), "the replacement backend-effective call must stay canonically valid")
	require.Equal(t, 1, algCountToolDef(*got, algControlToolName),
		"an in-flight attempt must keep the control tool of the generation it was admitted with")
	require.Equal(t, 1, algCountPlainText(*got, algControlInstruction),
		"an in-flight attempt must keep the instruction of the generation it was admitted with")
	assert.Len(t, got.Extensions, 0,
		"the pinned activation must stay request-scoped and never serialize control provenance into canonical extensions")
	assert.Zero(t, controlLifecycleCountContaining(controlLifecycleEventLabels(released), algControlToolName),
		"the in-flight control call must never reach the client; released=%+v", released)

	// The response side of the same in-flight request is pinned too: the control
	// call it completed is handled by generation 1's provider, with generation 1's
	// attempt provenance.
	handled, metas := gen1Provider.handoffs()
	require.Equal(t, 1, handled,
		"the pinned generation's provider must handle the in-flight attempt's control call exactly once")
	require.Len(t, metas, 1, "exactly one control handoff belongs to the in-flight request")
	assert.Equal(t, algReloadReplacementBackendID+":"+algControlBackendModel, metas[0].CandidateKey,
		"the handoff must carry the in-flight replacement attempt's own candidate")

	// Every ATTEMPT of the in-flight request projected generation 1's contract,
	// including the attempt whose own open FAILED. A failed open records only the
	// candidate identity, so attempt 1 cannot be proven through its open; it is
	// proven through the capability resolution the production path performed for
	// that same candidate. Asserting only successful opens would leave attempt 1
	// entirely unasserted while claiming both attempts were checked.
	//
	// Each attempt log also carries the control provider's own PRE-ACTIVATION
	// eligibility lookup, which correctly renders control_tools=0 because no
	// contract has been projected yet. Only the attempt's OWN resolution (the last
	// resolution in that log) is asserted, so the eligibility lookup is never
	// mistaken for the attempt's own projection.
	failedOpens, succeededOpens := 0, 0
	for _, steps := range [][]string{inFlightLog.snapshot(), replacementLog.snapshot()} {
		require.NotEmpty(t, steps, "expected candidate-open ordering probes")
		own := lastProjectionStep(steps)
		require.NotEmpty(t, own,
			"every attempt must resolve its own candidate capabilities; steps=%v", steps)
		assert.Contains(t, own, "control_tools=1",
			"every attempt of the in-flight request must project generation 1's control tool; steps=%v", steps)
		assert.Contains(t, own, "control_text=1",
			"every attempt of the in-flight request must project generation 1's control instruction; steps=%v", steps)
		for _, step := range steps {
			switch {
			case isSuccessfulOpen(step):
				succeededOpens++
				assert.Contains(t, step, "control_tools=1",
					"a successful in-flight attempt must open with generation 1's control tool; step=%q", step)
				assert.Contains(t, step, "control_text=1",
					"a successful in-flight attempt must open with generation 1's control instruction; step=%q", step)
			case isFailedOpen(step):
				failedOpens++
			}
		}
	}
	require.Equal(t, 1, failedOpens,
		"exactly the in-flight request's first attempt must have failed at open; logs=%v/%v",
		inFlightLog.snapshot(), replacementLog.snapshot())
	require.Equal(t, 1, succeededOpens,
		"exactly the in-flight request's replacement attempt must have opened; logs=%v/%v",
		inFlightLog.snapshot(), replacementLog.snapshot())

	// The newly-admitted half, through the very same reload: the next request on
	// the active generation receives no control provenance whatsoever.
	second, err := runtimehost.NewGenerationExecutor(m).Execute(principalCtx("control-after-reload"),
		algSeamCall(algControlBackendID+":"+algControlBackendModel, "control-after-reload"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	_ = algCollectEOF(t, second)

	afterReload := newlyAdmittedCall.Load()
	require.NotNil(t, afterReload, "the newly admitted request must reach the active generation's backend")
	assert.Zero(t, algCountToolDef(*afterReload, algControlToolName),
		"a newly admitted request must use the newly published generation and never inherit the previous one's control activation")
	assert.Zero(t, algCountPlainText(*afterReload, algControlInstruction),
		"a newly admitted request must never inherit the previous generation's control instruction")
	handled, _ = gen1Provider.handoffs()
	assert.Equal(t, 1, handled,
		"the newly admitted request must never reach the retired generation's provider")
}

// lastProjectionStep returns the LAST capability-resolution step in one attempt's
// order log, which is that attempt's OWN candidate resolution: the activation
// stage projects the control contract immediately before the open, so the
// attempt's resolution is the last resolution to run. Earlier resolutions in the
// same log are the control provider's pre-activation eligibility lookups and are
// deliberately not treated as the attempt's own projection.
func lastProjectionStep(steps []string) string {
	for _, step := range slices.Backward(steps) {
		if strings.HasPrefix(step, "resolve_caps:") {
			return step
		}
	}
	return ""
}

// isSuccessfulOpen reports whether a step is a candidate open that produced a
// stream, which therefore also rendered the call the production path built.
func isSuccessfulOpen(step string) bool {
	return strings.HasPrefix(step, "open:")
}

// isFailedOpen reports whether a step is a candidate open that failed. A failed
// open carries the candidate identity only, which is exactly why the attempt that
// failed must be proven through its own capability resolution instead.
func isFailedOpen(step string) bool {
	return strings.HasPrefix(step, "open_failed:")
}
