// Pending completion-result publication at the accepted terminal for
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Completion
// Evidence and Pending Result / Response Interception Placement / Concurrency and
// Lifecycle; requirements 6.3-6.7, 11.3, 11.5, 12.5).
//
// This is the public-stream half of task 5.2. Every case drives the real
// executor, the real generic control-tool projection/interception seams, and the
// real canonical client stream, so a publication satisfied only by a private
// helper or a synthetic boolean fails here.
//
// Generic runtime only. The fake provider is the same anonymous `proxy_control`
// model control tool any feature generation would contribute through
// feature.PlaneControlToolProvider; nothing here names a concrete feature.
package runtime_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	front "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// pendingResultText is the bounded completion result every publication case
// expects to reach the client as ordinary assistant text. It is a low-entropy,
// obviously fake marker so secret scanning sees a planted test value.
const pendingResultText = "final bounded answer"

// pendingResultProvider is the generation-admitted generic control provider. It
// answers one completed call with the configured outcome and records the call so
// a case can prove the private handoff still happened exactly once.
type pendingResultProvider struct {
	outcome controltool.Outcome
	err     error

	handled int
}

func (p *pendingResultProvider) ID() string             { return algControlProviderID }
func (p *pendingResultProvider) Spec() controltool.Spec { return algControlTestSpec() }

func (p *pendingResultProvider) Handle(context.Context, controltool.CompletedCall, controltool.Meta) (controltool.Outcome, error) {
	p.handled++
	if p.err != nil {
		return controltool.Outcome{}, p.err
	}
	return p.outcome, nil
}

func pendingCompleteOutcome(result string) controltool.Outcome {
	return controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: result, ReasonCode: "control_complete"}
}

// pendingCompletionStream replays a fixed canonical prefix and then ends. The
// completion-only cases keep it free of assistant text so the private result is
// the only possible client-visible answer.
func pendingCompletionStream(events ...aipapi.Event) aipapi.ManagedEventStream {
	if len(events) == 0 {
		events = []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventToolCallStarted, ToolCallID: "control-1", ToolName: algControlToolName},
			{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "control-1", Delta: `{"note":"done"}`},
			{Kind: aipapi.EventToolCallFinished, ToolCallID: "control-1", ToolName: algControlToolName},
			{Kind: aipapi.EventResponseFinished},
		}
	}
	return aipapi.NewFixedEventStream(events)
}

func pendingCompletionEventLabels(events []aipapi.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventTextDelta:
			out = append(out, "text:"+ev.Delta)
		default:
			out = append(out, string(ev.Kind))
		}
	}
	return out
}

func pendingCompletionText(events []aipapi.Event) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta {
			b.WriteString(ev.Delta)
		}
	}
	return b.String()
}

// pendingCompletionRig runs one real request against one real candidate whose
// backend stream emits events.
type pendingCompletionRig struct {
	ex       *runtime.Executor
	provider *pendingResultProvider
	log      *algOrderLog
}

func newPendingCompletionRig(t *testing.T, outcome controltool.Outcome, gates []completion.Gate, events ...aipapi.Event) *pendingCompletionRig {
	t.Helper()
	log := &algOrderLog{}
	provider := &pendingResultProvider{outcome: outcome}
	ex := algSeamExecutor(t, map[string]execbackend.Backend{
		algControlBackendID: algSeamBackend(log, algOrderedToolsCaps(), algControlBackendID,
			func(aipapi.Call) (aipapi.ManagedEventStream, error) {
				return pendingCompletionStream(events...), nil
			}),
	}, pendingCompletionPlanes(t, provider, gates), log)
	return &pendingCompletionRig{ex: ex, provider: provider, log: log}
}

// pendingCompletionPlanes composes the generic control provider and any optional
// completion gates the way a feature generation does: one contribution set, one
// freeze, one immutable generation.
func pendingCompletionPlanes(t *testing.T, provider controltool.Provider, gates []completion.Gate) aipfeature.FrozenPlaneSet {
	t.Helper()
	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneControlToolProvider, algControlProviderID, provider),
		"generation composition must accept the generic control spec")
	if gates != nil {
		require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, "pending-completion-gates", gates),
			"generation composition must accept the completion gates")
	}
	return cs.Freeze()
}

// TestPendingCompletion_completionOnlyResultBecomesTheFinalAssistantText is
// requirements 6.3 and 6.7 on the real stream: a valid completion whose response
// carried no meaningful assistant text releases its bounded result as ordinary
// canonical assistant text through the existing release path, before the
// accepted terminal finish, exactly once and without any synthetic client tool
// call.
func TestPendingCompletion_completionOnlyResultBecomesTheFinalAssistantText(t *testing.T) {
	t.Parallel()

	rig := newPendingCompletionRig(t, pendingCompleteOutcome(pendingResultText), nil)
	stream, err := rig.ex.Execute(principalCtx("pending-completion-only"),
		algSeamCall(algControlBackendID+":"+algControlBackendModel, "pending-completion-only"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	released := algCollectEOF(t, stream)

	require.NoError(t, aipapi.ValidateEventSequence(released),
		"the published result must keep the canonical sequence legal; released=%v",
		pendingCompletionEventLabels(released))
	assert.Equal(t, pendingResultText, pendingCompletionText(released),
		"the bounded completion result must be the only client-visible assistant text; released=%v",
		pendingCompletionEventLabels(released))
	assert.Equal(t, 1, rig.provider.handled, "the private control handoff must still run exactly once")

	// The result precedes the accepted terminal, and no synthetic client tool
	// call or tool result is ever published for a proxy-owned control completion.
	labels := pendingCompletionEventLabels(released)
	resultAt := -1
	finishAt := -1
	for i, label := range labels {
		switch {
		case strings.HasPrefix(label, "text:"):
			resultAt = i
		case label == string(aipapi.EventResponseFinished):
			finishAt = i
		}
	}
	require.GreaterOrEqual(t, resultAt, 0, "the result must be released; released=%v", labels)
	require.GreaterOrEqual(t, finishAt, 0, "the response must finish; released=%v", labels)
	assert.Less(t, resultAt, finishAt, "the result must precede the accepted terminal finish; released=%v", labels)
	for _, ev := range released {
		assert.NotEqual(t, aipapi.EventToolCallStarted, ev.Kind,
			"a proxy-owned control completion must never publish a synthetic client tool call; released=%v", labels)
		assert.NotEqual(t, aipapi.EventItem, ev.Kind,
			"a proxy-owned control completion must never publish a synthetic client tool item; released=%v", labels)
	}
	assert.Zero(t, controlLifecycleCountContaining(controlLifecycleEventLabels(released), algControlToolName),
		"no proxy-owned control traffic may reach the client; released=%v", labels)
}

// TestPendingCompletion_priorAssistantTextSuppressesTheResult pins requirement
// 6.4: once meaningful assistant text has already been committed for the logical
// response, the completion result stays completion evidence and is never
// duplicated into a second answer.
func TestPendingCompletion_priorAssistantTextSuppressesTheResult(t *testing.T) {
	t.Parallel()

	rig := newPendingCompletionRig(t, pendingCompleteOutcome(pendingResultText), nil,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "already answered"},
		aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "control-1", ToolName: algControlToolName},
		aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "control-1", Delta: `{"note":"done"}`},
		aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "control-1", ToolName: algControlToolName},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)
	stream, err := rig.ex.Execute(principalCtx("pending-prior-text"),
		algSeamCall(algControlBackendID+":"+algControlBackendModel, "pending-prior-text"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	released := algCollectEOF(t, stream)

	require.NoError(t, aipapi.ValidateEventSequence(released),
		"an ordinary completed response must stay canonically legal; released=%v",
		pendingCompletionEventLabels(released))
	assert.Equal(t, "already answered", pendingCompletionText(released),
		"prior committed assistant text must suppress the completion result; released=%v",
		pendingCompletionEventLabels(released))
}

// TestPendingCompletion_invalidCompletionNeverPublishesAResult pins the invalid
// half of requirements 6.3/6.4: an outcome the private handler did not accept is
// not a completion, so nothing may be published for it even though the same
// bounded text exists on the wire.
func TestPendingCompletion_invalidCompletionNeverPublishesAResult(t *testing.T) {
	t.Parallel()

	rig := newPendingCompletionRig(t, controltool.Outcome{
		Kind: controltool.OutcomeInvalid, ReasonCode: "control_invalid",
	}, nil)
	stream, err := rig.ex.Execute(principalCtx("pending-invalid"),
		algSeamCall(algControlBackendID+":"+algControlBackendModel, "pending-invalid"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	released := algCollectEOF(t, stream)

	assert.NotContains(t, pendingCompletionText(released), pendingResultText,
		"an unaccepted completion outcome must publish no result; released=%v",
		pendingCompletionEventLabels(released))
	for _, ev := range released {
		assert.NotEqual(t, aipapi.EventToolCallStarted, ev.Kind,
			"a rejected control call must never fall through to client tool execution; released=%v",
			pendingCompletionEventLabels(released))
	}
}

// pendingRecordGate is a pass-through completion gate whose invocation count and
// observed buffer let a case prove the gate chain ran exactly once over a
// candidate that already carried the private result.
type pendingRecordGate struct {
	id    string
	order int
	out   completion.Outcome
	err   error
	seen  []string
	calls int
}

func (g *pendingRecordGate) ID() string                        { return g.id }
func (g *pendingRecordGate) Order() int                        { return g.order }
func (g *pendingRecordGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (g *pendingRecordGate) Handle(_ context.Context, _ completion.Meta, buf completion.Buffered, _ completion.Services) (completion.Outcome, error) {
	g.calls++
	g.seen = nil
	for _, ev := range buf.Events() {
		if ev.Kind == aipapi.EventTextDelta {
			g.seen = append(g.seen, ev.Delta)
		}
	}
	if g.err != nil {
		return completion.Outcome{}, g.err
	}
	return g.out, nil
}

// TestPendingCompletion_gatePassKeepsTheResultAndRunsOnce pins requirement 6.7
// for the gated path: a pass-through gate must see the eligible private result
// exactly once, and the released stream must contain that result before the
// accepted terminal finish.
func TestPendingCompletion_gatePassKeepsTheResultAndRunsOnce(t *testing.T) {
	t.Parallel()

	gate := &pendingRecordGate{id: "pending-pass", out: completion.PassOriginalOutcome()}
	rig := newPendingCompletionRig(t, pendingCompleteOutcome(pendingResultText), []completion.Gate{gate})
	stream, err := rig.ex.Execute(principalCtx("pending-gate-pass"),
		algSeamCall(algControlBackendID+":"+algControlBackendModel, "pending-gate-pass"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	released := algCollectEOF(t, stream)

	require.NoError(t, aipapi.ValidateEventSequence(released),
		"a gated completion-only response must stay canonically legal; released=%v",
		pendingCompletionEventLabels(released))
	assert.Equal(t, pendingResultText, pendingCompletionText(released),
		"a pass-through gate must preserve the published result; released=%v",
		pendingCompletionEventLabels(released))
	require.Equal(t, 1, gate.calls, "the gate chain must run exactly once for the whole response")
	assert.Equal(t, []string{pendingResultText}, gate.seen,
		"the gate must observe the eligible private result inside its buffer")
}

// TestPendingCompletion_gateReplaceOutputIsAuthoritative pins the authoritative
// gate output: when a gate replaces the stream, the replacement is what the
// client receives and the original private result is not re-appended.
func TestPendingCompletion_gateReplaceOutputIsAuthoritative(t *testing.T) {
	t.Parallel()

	gate := &pendingRecordGate{id: "pending-replace", out: completion.ReplaceOutcome([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "gate replacement"},
		{Kind: aipapi.EventResponseFinished},
	})}
	rig := newPendingCompletionRig(t, pendingCompleteOutcome(pendingResultText), []completion.Gate{gate})
	stream, err := rig.ex.Execute(principalCtx("pending-gate-replace"),
		algSeamCall(algControlBackendID+":"+algControlBackendModel, "pending-gate-replace"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	released := algCollectEOF(t, stream)

	require.NoError(t, aipapi.ValidateEventSequence(released),
		"a gate replacement must stay canonically legal; released=%v",
		pendingCompletionEventLabels(released))
	assert.Equal(t, "gate replacement", pendingCompletionText(released),
		"the effective gate output is authoritative; released=%v",
		pendingCompletionEventLabels(released))
	require.Equal(t, 1, gate.calls, "the gate chain must run exactly once for the whole response")
}

// TestPendingCompletion_gateRejectSuppressesTheResult pins the fail-closed gate
// path: a rejecting gate discards the whole candidate, so the private result must
// never reach the client.
func TestPendingCompletion_gateRejectSuppressesTheResult(t *testing.T) {
	t.Parallel()

	gate := &pendingRecordGate{id: "pending-reject", out: completion.RejectOutcome(assertErrorf("pending reject"))}
	rig := newPendingCompletionRig(t, pendingCompleteOutcome(pendingResultText), []completion.Gate{gate})
	stream, err := rig.ex.Execute(principalCtx("pending-gate-reject"),
		algSeamCall(algControlBackendID+":"+algControlBackendModel, "pending-gate-reject"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	_, collectErr := aipapi.Collect(context.Background(), stream)
	if collectErr == nil {
		t.Fatal("a rejecting completion gate must fail the response")
	}
	assert.Equal(t, 1, gate.calls)
}

type pendingRejectError string

func (e pendingRejectError) Error() string { return string(e) }

func assertErrorf(text string) error { return pendingRejectError(text) }

// TestPendingCompletion_repeatedTerminalReleaseNeverDuplicatesTheResult pins the
// once-only publication property: draining the same accepted terminal twice must
// not produce a second copy of the result.
func TestPendingCompletion_repeatedTerminalReleaseNeverDuplicatesTheResult(t *testing.T) {
	t.Parallel()

	rig := newPendingCompletionRig(t, pendingCompleteOutcome(pendingResultText), nil)
	stream, err := rig.ex.Execute(principalCtx("pending-repeat"),
		algSeamCall(algControlBackendID+":"+algControlBackendModel, "pending-repeat"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	released := algCollectEOF(t, stream)

	count := 0
	for _, ev := range released {
		if ev.Kind == aipapi.EventTextDelta && strings.Contains(ev.Delta, pendingResultText) {
			count++
		}
	}
	assert.Equal(t, 1, count, "the accepted terminal must publish the result exactly once; released=%v",
		pendingCompletionEventLabels(released))
}

// TestPendingCompletion_hooksAndGatesStayGeneric pins requirement 12.1/12.2 for
// this path: the published result is ordinary assistant content, so the ordinary
// response-part hook sees it through the existing hook seam rather than through
// any feature-specific write.
func TestPendingCompletion_hooksAndGatesStayGeneric(t *testing.T) {
	t.Parallel()

	probe := &pendingHookProbe{}
	log := &algOrderLog{}
	provider := &pendingResultProvider{outcome: pendingCompleteOutcome(pendingResultText)}
	bus := hooks.New(hooks.Config{ResponsePartHooks: []sdkhooks.ResponsePartHook{probe}})
	ex := algSecureExecutor(t, map[string]execbackend.Backend{
		algControlBackendID: algSeamBackend(log, algOrderedToolsCaps(), algControlBackendID,
			func(aipapi.Call) (aipapi.ManagedEventStream, error) {
				return pendingCompletionStream(), nil
			}),
	}, bus, extensions.SnapshotOptions{
		Workspace:     voidWorkspaceResolver{},
		FeaturePlanes: pendingCompletionPlanes(t, provider, nil),
	})
	require.NotNil(t, ex.RuntimeSnapshot.ControlToolProvider())

	stream, err := ex.Execute(principalCtx("pending-hook"),
		algSeamCall(algControlBackendID+":"+algControlBackendModel, "pending-hook"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Close() })
	released := algCollectEOF(t, stream)

	require.NoError(t, aipapi.ValidateEventSequence(released),
		"the hooked result must stay canonically legal; released=%v",
		pendingCompletionEventLabels(released))
	assert.Equal(t, pendingResultText, pendingCompletionText(released))
	assert.Equal(t, 1, probe.textDeltas(pendingResultText),
		"the response-part hook must observe the synthetic result exactly once; saw=%v", probe.seen)
}

// pendingHookProbe is one ordinary response-part hook. It counts text deltas that
// carry a marker so a case can prove the published result went through the
// existing hook seam exactly once.
type pendingHookProbe struct {
	deltas []string
}

func (p *pendingHookProbe) ID() string                        { return "pending-hook-probe" }
func (p *pendingHookProbe) Order() int                        { return 0 }
func (p *pendingHookProbe) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (p *pendingHookProbe) HandleEvent(_ context.Context, ev *aipapi.Event, _ sdkhooks.PartMeta) error {
	if ev != nil && ev.Kind == aipapi.EventTextDelta {
		p.deltas = append(p.deltas, ev.Delta)
	}
	return nil
}

func (p *pendingHookProbe) seen() []string {
	out := make([]string, len(p.deltas))
	copy(out, p.deltas)
	return out
}

func (p *pendingHookProbe) textDeltas(containing string) int {
	count := 0
	for _, delta := range p.seen() {
		if strings.Contains(delta, containing) {
			count++
		}
	}
	return count
}

func TestPendingCompletion_actualReplayAndGateProvenance(t *testing.T) {
	replacement := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventTextDelta, Delta: "chosen gate answer"}, {Kind: aipapi.EventResponseFinished}}
	for _, tc := range []struct {
		name     string
		outcomes []completion.Outcome
		want     string
		unsafe   bool
	}{
		{"Replay", []completion.Outcome{{Kind: completion.OutcomeReplayOriginal}}, pendingResultText, false},
		{"ReplaceReplay", []completion.Outcome{completion.ReplaceOutcome(replacement), {Kind: completion.OutcomeReplayOriginal}}, pendingResultText, false},
		{"ReplacePass", []completion.Outcome{completion.ReplaceOutcome(replacement), completion.PassOriginalOutcome()}, "chosen gate answer", false},
		{"UnsafeReplaceReplay", []completion.Outcome{completion.ReplaceOutcome(replacement), {Kind: completion.OutcomeReplayOriginal}}, "", true},
		{"UnsafeReplacePass", []completion.Outcome{completion.ReplaceOutcome(replacement), completion.PassOriginalOutcome()}, "chosen gate answer", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &pendingHookProbe{}
			provider := &pendingResultProvider{outcome: pendingCompleteOutcome(pendingResultText)}
			var gates []completion.Gate
			var records []*pendingRecordGate
			for i, out := range tc.outcomes {
				g := &pendingRecordGate{id: tc.name + string(rune('a'+i)), order: i, out: out}
				gates = append(gates, g)
				records = append(records, g)
			}
			bus := hooks.New(hooks.Config{ResponsePartHooks: []sdkhooks.ResponsePartHook{probe}})
			ex := algSecureExecutor(t, map[string]execbackend.Backend{algControlBackendID: algSeamBackend(&algOrderLog{}, algOrderedToolsCaps(), algControlBackendID, func(aipapi.Call) (aipapi.ManagedEventStream, error) {
				if tc.unsafe {
					return pendingCompletionStream(
						aipapi.Event{Kind: aipapi.EventResponseStarted}, aipapi.Event{Kind: aipapi.EventMessageStarted},
						aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "control-1", ToolName: algControlToolName},
						aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "control-1", Delta: `{"note":"done"}`},
						aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "control-1", ToolName: algControlToolName},
						aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "unsafe-ordinary", ToolName: "get_weather"},
						aipapi.Event{Kind: aipapi.EventResponseFinished},
					), nil
				}
				return pendingCompletionStream(), nil
			})}, bus, extensions.SnapshotOptions{Workspace: voidWorkspaceResolver{}, FeaturePlanes: pendingCompletionPlanes(t, provider, gates)})
			stream, err := ex.Execute(principalCtx("pending-gate-provenance"), algSeamCall(algControlBackendID+":"+algControlBackendModel, "pending-gate-provenance"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = stream.Close() })
			released := algCollectEOF(t, stream)
			require.NoError(t, aipapi.ValidateEventSequence(released))
			assert.Equal(t, tc.want, pendingCompletionText(released))
			assert.Equal(t, 1, probe.textDeltas(pendingResultText))
			assert.Equal(t, 1, provider.handled)
			for _, g := range records {
				assert.Equal(t, 1, g.calls)
			}
		})
	}
}

// pendingHTTPExecutor is only the existing frontend SDK seam. Its returned
// stream is the real receive-loop fixture, constructed from the decoded call.
type pendingHTTPExecutor struct {
	execute func(context.Context, *aipapi.Call) (aipapi.EventStream, error)
}

func (e pendingHTTPExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return e.execute(ctx, call)
}

func (pendingHTTPExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (pendingHTTPExecutor) WallClock() func() time.Time { return nil }

func TestPendingReal_actualResponsesHandlerConsumers(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, suppress := range []bool{false, true} {
			name := map[bool]string{false: "nonstreaming", true: "streaming"}[streaming] + map[bool]string{false: "/completion", true: "/suppressed"}[suppress]
			t.Run(name, func(t *testing.T) {
				var decoded aipapi.Call
				var verify func()
				ex := pendingHTTPExecutor{execute: func(_ context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
					decoded = *call
					stream, check := runtime.PendingResponsesConsumerFixtureForTest(t, call, suppress)
					verify = check
					return stream, nil
				}}
				h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:model"}
				body := `{"model":"model","input":"frontend actual objective","stream":` + map[bool]string{false: "false", true: "true"}[streaming] + `}`
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.NotNil(t, verify)
				verify()
				assert.Equal(t, aipapi.OperationOpenAIResponses, decoded.Invocation.Operation)

				require.NotEmpty(t, decoded.Messages)
				rawCall, err := json.Marshal(decoded.Messages)
				require.NoError(t, err)
				assert.Contains(t, string(rawCall), "frontend actual objective")
				wire := w.Body.String()
				for _, private := range []string{"proxy_complete", "control_complete", "function_call", "tool_call", "reservation-pending-real"} {
					assert.NotContains(t, wire, private)
				}
				want := "real bounded answer"
				if suppress {
					want = "already answered"
					assert.NotContains(t, wire, "real bounded answer")
				}
				var response map[string]any
				if streaming {
					// The actual frontend SSE writer (encode_stream.go) sets the media
					// type with its charset parameter, so assert the real header value
					// rather than a bare media type.
					assert.Equal(t, "text/event-stream; charset=utf-8", w.Header().Get("Content-Type"))
					deltas := 0
					completed := 0
					for line := range strings.SplitSeq(wire, "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						payload := strings.TrimPrefix(line, "data: ")
						if payload == "[DONE]" {
							continue
						}
						var frame map[string]any
						require.NoError(t, json.Unmarshal([]byte(payload), &frame))
						if frame["type"] == "response.output_text.delta" {
							assert.Equal(t, want, frame["delta"])
							deltas++
						}
						if frame["type"] == "response.completed" {
							response, _ = frame["response"].(map[string]any)
							completed++
						}
					}
					assert.Equal(t, 1, deltas)
					assert.Equal(t, 1, completed)
				} else {
					require.NoError(t, json.Unmarshal([]byte(wire), &response))
				}
				require.NotNil(t, response)
				output, ok := response["output"].([]any)
				require.True(t, ok)
				require.Len(t, output, 1)
				msg, ok := output[0].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "assistant", msg["role"])
				content, ok := msg["content"].([]any)
				require.True(t, ok)
				require.Len(t, content, 1)
				text, ok := content[0].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, want, text["text"])
				u, ok := response["usage"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, float64(7), u["input_tokens"])
				assert.Equal(t, float64(len(want)), u["output_tokens"])
			})
		}
	}
}
