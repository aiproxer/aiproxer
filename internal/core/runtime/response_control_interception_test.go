// Streaming-preserving private control-call interception for task 4.2 of
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Response
// Interception / Placement and Handler Semantics; requirements 3.5, 5.1-5.7,
// 11.1-11.3, 12.3-12.4).
//
// Generic runtime only. Nothing here names a concrete feature: the fixture is the
// same anonymous proxy-owned control tool any feature generation would project,
// driven through the real response seam. Every case goes through
// prepareRecvEvent, the exact boundary the Recv loop owns, so a capture that
// consumed events somewhere else, or one that let a claimed event reach the
// ordinary path, fails here rather than in production ordering.
//
// The privacy property under test is two-sided and that is why the fakes are
// numerous: every ordinary observer the client path owns must see the ordinary
// traffic and ZERO claimed control traffic, while the operator BTP leg and the
// provider accounting leg must still see the legitimate upstream control
// traffic. Both directions failing is the defect.
//
// This file reads the attempt's private control state sequentially, from the same
// goroutine that drives the response seam. The cases where lifecycle cleanup runs
// concurrently with a response handoff read that state through the locked
// snapshots in control_call_lifecycle_test.go instead, because those need an
// observation that is ordered against cleanup rather than merely sequential.
package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// The generic control contract projected here. Name, schema, instruction text,
// and args budget are provider-owned plain bytes; the runtime must read them from
// the frozen projection and never rewrite them.
const (
	interceptProviderID  = "intercept-control-test"
	interceptToolName    = "proxy_control_intercept"
	interceptToolDesc    = "Call this only when the assigned proxy-local control action is complete."
	interceptInstruction = "Call proxy_control_intercept only when the assigned control action is complete."
	interceptSchema      = `{"type":"object","properties":{"note":{"type":"string"}},"required":["note"],"additionalProperties":false}`
	interceptArgs        = `{"note":"done"}`
	interceptOrdinary    = "get_weather"
	interceptOrdinaryArg = `{"city":"krakow"}`
)

// Distinct secrets so a failing assertion can name which surface leaked.
const (
	interceptResultSecret = "intercept-result-secret-EEE555"
	interceptReasonSecret = "PRIVATE_PRIVATE_PRIVATE"
)

// --- fakes -------------------------------------------------------------------

// interceptOrderLog records ordered observations across every seam under test, so
// placement can be asserted from real call order instead of timing.
type interceptOrderLog struct {
	mu    sync.Mutex
	steps []string
}

func (l *interceptOrderLog) add(step string) {
	l.mu.Lock()
	l.steps = append(l.steps, step)
	l.mu.Unlock()
}

func (l *interceptOrderLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.steps))
	copy(out, l.steps)
	return out
}

func interceptEventLabel(ev aipapi.Event) string {
	if ev.ToolCallID != "" {
		return string(ev.Kind) + ":" + ev.ToolCallID
	}
	if ev.Kind == aipapi.EventItem && ev.Item != nil && ev.Item.ToolCall != nil {
		return string(ev.Kind) + ":" + ev.Item.ToolCall.CallID
	}
	return string(ev.Kind)
}

// interceptTrafficProbe is the operator BTP/PTC observation. BTP must see the
// upstream control traffic (operator capture of the legitimate B-leg stream); PTC
// must never see it (client-facing truth).
type interceptTrafficProbe struct {
	log *interceptOrderLog

	mu  sync.Mutex
	btp []string
	ptc []string
}

func (p *interceptTrafficProbe) OnObservation(_ context.Context, obs sdktraffic.Observation) error {
	var ev aipapi.Event
	if err := json.Unmarshal(obs.Body, &ev); err != nil {
		return nil
	}
	label := interceptEventLabel(ev)
	p.mu.Lock()
	switch obs.Leg {
	case sdktraffic.LegBTP:
		p.btp = append(p.btp, label)
	case sdktraffic.LegPTC:
		p.ptc = append(p.ptc, label)
	}
	p.mu.Unlock()
	if p.log != nil {
		p.log.add("traffic:" + string(obs.Leg) + ":" + label)
	}
	return nil
}

func (p *interceptTrafficProbe) observed(leg sdktraffic.Leg) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch leg {
	case sdktraffic.LegBTP:
		out := make([]string, len(p.btp))
		copy(out, p.btp)
		return out
	default:
		out := make([]string, len(p.ptc))
		copy(out, p.ptc)
		return out
	}
}

// interceptUsageProbe is provider accounting on the upstream usage leg. It must
// keep seeing the real B-leg usage even while the control call is captured
// privately (requirement 11.3).
type interceptUsageProbe struct {
	log *interceptOrderLog

	mu   sync.Mutex
	seen []string
}

func (p *interceptUsageProbe) OnUsage(_ context.Context, ev usage.Event) error {
	p.mu.Lock()
	p.seen = append(p.seen, ev.BLegID)
	p.mu.Unlock()
	if p.log != nil {
		p.log.add("usage:" + ev.BLegID)
	}
	return nil
}

func (p *interceptUsageProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen)
}

// interceptFinalizer is the ordinary tool-call finalizer. It must see the
// ordinary client tool and must never see the proxy-owned control call.
type interceptFinalizer struct {
	log *interceptOrderLog

	mu    sync.Mutex
	calls []string
}

func (f *interceptFinalizer) ID() string { return "intercept-finalizer" }
func (f *interceptFinalizer) Order() int { return 0 }
func (f *interceptFinalizer) Finalize(_ context.Context, call toolcall.CompletedCall, _ aipapi.ToolDef, _ []aipapi.ToolDef, _ toolcall.Meta) (toolcall.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call.ToolName)
	f.mu.Unlock()
	if f.log != nil {
		f.log.add("finalizer:" + call.ToolName)
	}
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

func (f *interceptFinalizer) finalized() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// interceptReactor is the ordinary tool reactor.
type interceptReactor struct {
	log *interceptOrderLog

	mu   sync.Mutex
	seen []string
}

func (r *interceptReactor) ID() string { return "intercept-reactor" }
func (r *interceptReactor) Order() int { return 0 }
func (r *interceptReactor) HandleToolEvent(_ context.Context, te aipapi.ToolEvent, _ sdkhooks.ToolMeta) (sdkhooks.ToolDecision, aipapi.ToolEvent, error) {
	r.mu.Lock()
	r.seen = append(r.seen, string(te.Kind)+":"+te.ToolName)
	r.mu.Unlock()
	if r.log != nil {
		r.log.add("reactor:" + string(te.Kind) + ":" + te.ToolName)
	}
	return sdkhooks.ToolPass, aipapi.ToolEvent{}, nil
}

func (r *interceptReactor) observed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.seen))
	copy(out, r.seen)
	return out
}

// interceptPolicy is the ordinary client tool policy.
type interceptPolicy struct {
	log *interceptOrderLog

	mu   sync.Mutex
	seen []string
}

func (p *interceptPolicy) ID() string                        { return "intercept-policy" }
func (p *interceptPolicy) Order() int                        { return 0 }
func (p *interceptPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (p *interceptPolicy) Handle(_ context.Context, ev aipapi.ToolEvent, _ toolpolicy.Meta, _ toolpolicy.Services) (toolpolicy.Decision, error) {
	p.mu.Lock()
	p.seen = append(p.seen, string(ev.Kind)+":"+ev.ToolName)
	p.mu.Unlock()
	if p.log != nil {
		p.log.add("policy:" + string(ev.Kind) + ":" + ev.ToolName)
	}
	return toolpolicy.DecisionAllow, nil
}

func (p *interceptPolicy) observed() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.seen))
	copy(out, p.seen)
	return out
}

// interceptResponseHook is the ordinary response part hook. It sees ordinary
// text and ordinary tool lifecycle events and never the claimed control call.
type interceptResponseHook struct {
	log *interceptOrderLog

	mu   sync.Mutex
	seen []string
}

func (h *interceptResponseHook) ID() string                        { return "intercept-response-hook" }
func (h *interceptResponseHook) Order() int                        { return 0 }
func (h *interceptResponseHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h *interceptResponseHook) HandleEvent(_ context.Context, ev *aipapi.Event, _ sdkhooks.PartMeta) error {
	label := interceptEventLabel(*ev)
	h.mu.Lock()
	h.seen = append(h.seen, label)
	h.mu.Unlock()
	if h.log != nil {
		h.log.add("response_hook:" + label)
	}
	return nil
}

func (h *interceptResponseHook) observed() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.seen))
	copy(out, h.seen)
	return out
}

// interceptProvider is the pinned generation control provider. ID and Spec are
// tripwires: the response path must use the frozen projection and the frozen
// identity, so a live read fails this test loudly instead of passing quietly.
type interceptProvider struct {
	outcome controltool.Outcome
	err     error
	panicV  any
	log     *interceptOrderLog

	mu           sync.Mutex
	handled      int
	observedCall []controltool.CompletedCall
	observedMeta []controltool.Meta
}

func (p *interceptProvider) ID() string {
	panic("the response path must use the frozen provider identity")
}

func (p *interceptProvider) Spec() controltool.Spec {
	panic("the response path must use the frozen projection, never a live spec read")
}

func (p *interceptProvider) Handle(_ context.Context, call controltool.CompletedCall, meta controltool.Meta) (controltool.Outcome, error) {
	p.mu.Lock()
	p.handled++
	p.observedCall = append(p.observedCall, controltool.CompletedCall{
		ToolCallID: call.ToolCallID, ToolName: call.ToolName, ArgsJSON: append([]byte(nil), call.ArgsJSON...),
	})
	p.observedMeta = append(p.observedMeta, meta)
	p.mu.Unlock()
	if p.log != nil {
		p.log.add("handle:" + call.ToolCallID)
	}
	if p.panicV != nil {
		panic(p.panicV)
	}
	if p.err != nil {
		return controltool.Outcome{}, p.err
	}
	return p.outcome, nil
}

func (p *interceptProvider) handleCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handled
}

func (p *interceptProvider) calls() []controltool.CompletedCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]controltool.CompletedCall, len(p.observedCall))
	copy(out, p.observedCall)
	return out
}

func (p *interceptProvider) metas() []controltool.Meta {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]controltool.Meta, len(p.observedMeta))
	copy(out, p.observedMeta)
	return out
}

// --- fixtures ----------------------------------------------------------------

func interceptSpec(maxArgs int) controltool.Spec {
	return controltool.Spec{
		Tool: aipapi.ToolDef{
			Name:        interceptToolName,
			Description: interceptToolDesc,
			Parameters:  json.RawMessage(interceptSchema),
		},
		Instruction:  controltool.Instruction{Role: aipapi.RoleSystem, Text: interceptInstruction},
		MaxArgsBytes: maxArgs,
	}
}

// interceptActivation freezes a real, eligible projection exactly like the
// request path does, so these tests bind to the trusted activation rather than
// to any re-derived eligibility.
func interceptActivation(t *testing.T, provider controltool.Provider, maxArgs int) *controlToolActivation {
	t.Helper()
	spec := interceptSpec(maxArgs)
	require.NoError(t, controltool.ValidateSpec(spec), "the interception fixture must be a valid control spec")
	call := aipapi.Call{
		ID: "intercept-attempt",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("do the work")},
		}},
		Tools: []aipapi.ToolDef{{
			Name:       interceptOrdinary,
			Parameters: json.RawMessage(`{"type":"object"}`),
		}},
	}
	_, projection, err := controltool.Project(call, spec, aipapi.NewBackendCaps(aipapi.CapabilityTools))
	require.NoError(t, err)
	require.True(t, projection.Active(), "the interception fixture must be eligible for the control spec")
	require.Equal(t, maxArgs, projection.MaxArgsBytes(), "the frozen args budget is the only budget the runtime may use")

	meta := controltool.Meta{
		TraceID:      "trace-intercept-1",
		ALegID:       "aleg-intercept-1",
		BLegID:       "bleg-intercept-1",
		CandidateKey: "openai:gpt-4",
		AttemptSeq:   3,
	}
	return &controlToolActivation{
		providerID: interceptProviderID,
		provider:   provider,
		projection: projection,
		meta:       meta,
	}
}

// interceptRig is one wired response seam: a real response pipeline, a real
// attempt session derived from a trusted activation, and every ordinary observer
// the client path owns.
type interceptRig struct {
	log     *interceptOrderLog
	p       *responsePipeline
	attempt *attemptSession
	traffic *interceptTrafficProbe
	usage   *interceptUsageProbe

	finalizer *interceptFinalizer
	reactor   *interceptReactor
	policy    *interceptPolicy
	respHook  *interceptResponseHook
}

// newInterceptRig builds a seam around the activation the request path would
// have frozen for provider. It asserts the constructor invariant that an active
// activation always obtains a capture, so no case can pass with a missing one.
func newInterceptRig(t *testing.T, provider *interceptProvider, activation *controlToolActivation) *interceptRig {
	t.Helper()
	if activation == nil {
		activation = interceptActivation(t, provider, controltool.DefaultMaxArgsBytes)
	}
	rig := newInterceptRigWithActivation(t, activation)
	require.NotNil(t, rig.attempt.controlCapture,
		"an active activation must always obtain a capture at session construction")
	return rig
}

// newInterceptRigWithActivation builds a seam around an activation the test owns
// directly, for cases that must shape the frozen provenance themselves.
func newInterceptRigWithActivation(t *testing.T, activation *controlToolActivation) *interceptRig {
	t.Helper()
	return newInterceptRigOnLog(t, activation, &interceptOrderLog{})
}

// newInterceptRigOnLog is the shared-log variant, so a case that must compare
// positions across the traffic, usage, and handler seams records them into one
// deterministic sequence.
func newInterceptRigOnLog(t *testing.T, activation *controlToolActivation, log *interceptOrderLog) *interceptRig {
	t.Helper()
	traffic := &interceptTrafficProbe{log: log}
	usageProbe := &interceptUsageProbe{log: log}
	finalizer := &interceptFinalizer{log: log}
	reactor := &interceptReactor{log: log}
	policy := &interceptPolicy{log: log}
	respHook := &interceptResponseHook{log: log}

	bus := hooks.New(hooks.Config{
		ToolReactors:      []sdkhooks.ToolReactor{reactor},
		ResponsePartHooks: []sdkhooks.ResponsePartHook{respHook},
	})
	cs := aipfeature.NewContributionSet()
	require.NoError(t, aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, "intercept-test", []toolpolicy.Policy{policy}))
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		TrafficObserver: traffic,
		UsageObserver:   usageProbe,
		FeaturePlanes:   cs.Freeze(),
	})

	p := newResponsePipeline()
	p.bus = bus
	p.runtimeSnapshot = snap

	catalog := []aipapi.ToolDef{{Name: interceptOrdinary, Parameters: json.RawMessage(`{"type":"object"}`)}}
	attempt := newAttemptSession(attemptSessionInput{
		bleg:           b2bua.BLegRecord{ALegID: "aleg-intercept-1", BLegID: "bleg-intercept-1", Seq: 3},
		cand:           routing.AttemptCandidate{Key: "openai:gpt-4", Primary: routing.Primary{Backend: "openai", Model: "gpt-4"}},
		billingCallID:  usageevidence.BillingCallID("call-intercept-1"),
		controlTool:    activation,
		toolFinal:      newToolCallAssembler([]toolcall.Finalizer{finalizer}, 64*1024, catalog),
		finalStreamObs: &extensions.FinalStreamObservationSession{},
	})

	return &interceptRig{
		log: log, p: p, attempt: attempt,
		traffic: traffic, usage: usageProbe,
		finalizer: finalizer, reactor: reactor, policy: policy, respHook: respHook,
	}
}

// inertInterceptRig is the no-provider/inactive case: the session owns no
// capture at all, so the ordinary path must run unchanged.
func inertInterceptRig(t *testing.T) *interceptRig {
	t.Helper()
	return newInterceptRigWithActivation(t, &controlToolActivation{})
}

// drive runs one canonical event through the real response seam and then through
// the client-facing transformation, exactly as the Recv loop sequences them.
func (r *interceptRig) drive(ctx context.Context, ev aipapi.Event) (released []aipapi.Event, err error) {
	facts := recvTurnFacts{traceID: "trace-intercept-1", aLegID: "aleg-intercept-1"}
	prepared := r.p.prepareRecvEvent(ctx, facts, r.attempt, ev, nil)
	if prepared.err != nil {
		return nil, prepared.err
	}
	if prepared.swallowed {
		return nil, nil
	}
	transformed := r.p.transformClientEvent(ctx, facts, r.attempt, prepared.event, prepared)
	if transformed.err != nil {
		return nil, transformed.err
	}
	if transformed.swallowed {
		return nil, nil
	}
	return []aipapi.Event{transformed.event}, nil
}

// drain releases whatever the ordinary assembler finalized, so the ordinary tool
// lifecycle is observed by the client path rather than stranded in the buffer.
func (r *interceptRig) drain(ctx context.Context) (released []aipapi.Event, err error) {
	facts := recvTurnFacts{traceID: "trace-intercept-1", aLegID: "aleg-intercept-1"}
	asm := r.attempt.toolCallAssembler()
	if asm == nil {
		return nil, nil
	}
	for {
		ev, ok := asm.popDrain()
		if !ok {
			return released, nil
		}
		transformed := r.p.transformClientEvent(ctx, facts, r.attempt, ev, recvEventPreparation{event: ev})
		if transformed.err != nil {
			return released, transformed.err
		}
		if transformed.swallowed {
			continue
		}
		released = append(released, transformed.event)
	}
}

func interceptStart(id, name string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: name}
}

func interceptArgsDelta(id, delta string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, Delta: delta}
}

func interceptFinish(id string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: id}
}

func interceptItemCall(id, name, args string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
		Kind: aipapi.ItemKindMessage,
		ID:   "item-" + id,
		ToolCall: &aipapi.ToolCallItem{
			CallID: id, Name: name, Arguments: json.RawMessage(args),
		},
	}}
}

func countContaining(values []string, want string) int {
	count := 0
	for _, v := range values {
		if strings.Contains(v, want) {
			count++
		}
	}
	return count
}

// --- acceptance cases --------------------------------------------------------

// TestControlCallInterception_claimedLifecycleIsPrivateAndOrdinaryTrafficUnchanged
// is the placement acceptance case for requirements 3.5, 5.1-5.4, and 5.7.
//
// It pins both directions of design Response Interception / Placement in one
// deterministic stream:
//   - operator BTP capture and provider accounting still observe the legitimate
//     upstream control traffic and the real B-leg usage;
//   - the ordinary tool finalizer, tool policy, tool reactor, response part hook,
//     and the released client events see the ordinary tool and text and ZERO
//     claimed control lifecycle or item events;
//   - the pinned provider is invoked exactly once, and only after the full
//     bounded finish.
func TestControlCallInterception_claimedLifecycleIsPrivateAndOrdinaryTrafficUnchanged(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{
		outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "all done", ReasonCode: "control_complete"},
	}
	rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
	ctx := context.Background()

	stream := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "working"},
		interceptStart("ordinary-1", interceptOrdinary),
		interceptArgsDelta("ordinary-1", interceptOrdinaryArg),
		interceptFinish("ordinary-1"),
		{Kind: aipapi.EventTextDelta, Delta: " on it"},
		interceptStart("control-1", interceptToolName),
		{Kind: aipapi.EventUsageDelta, InputTokens: 11, OutputTokens: 5, TotalTokens: 16},
		interceptArgsDelta("control-1", `{"note":`),
		interceptArgsDelta("control-1", `"done"}`),
		interceptFinish("control-1"),
		{Kind: aipapi.EventTextDelta, Delta: " finished"},
		{Kind: aipapi.EventResponseFinished},
	}

	var released []aipapi.Event
	for i, ev := range stream {
		got, err := rig.drive(ctx, ev)
		require.NoError(t, err, "stream event[%d] (%s) must not fail the response", i, ev.Kind)
		released = append(released, got...)
		got, err = rig.drain(ctx)
		require.NoError(t, err, "draining after stream event[%d] must not fail the response", i)
		released = append(released, got...)
	}

	// The provider runs exactly once, after the full bounded finish.
	require.Equal(t, 1, provider.handleCount(), "the pinned provider must be invoked exactly once per response")
	calls := provider.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "control-1", calls[0].ToolCallID, "the handler must receive the captured call identity")
	assert.Equal(t, interceptToolName, calls[0].ToolName, "the handler must receive the frozen tool name")
	assert.Equal(t, interceptArgs, string(calls[0].ArgsJSON), "the handler must receive the owned bounded argument bytes")

	// The handler received the frozen provenance, not a re-derived one.
	metas := provider.metas()
	require.Len(t, metas, 1)
	assert.Equal(t, "bleg-intercept-1", metas[0].BLegID, "the handler must receive the actual B-leg")
	assert.Equal(t, 3, metas[0].AttemptSeq, "the handler must receive the actual attempt sequence")

	// Operator BTP capture keeps seeing the upstream control traffic; provider
	// accounting keeps seeing the real B-leg usage (requirement 11.3).
	btp := rig.traffic.observed(sdktraffic.LegBTP)
	for _, want := range []string{
		string(aipapi.EventToolCallStarted) + ":control-1",
		string(aipapi.EventToolCallFinished) + ":control-1",
	} {
		assert.Positive(t, countContaining(btp, want),
			"operator BTP capture must still observe legitimate upstream control traffic %q; btp=%v", want, btp)
	}
	assert.Positive(t, rig.usage.count(), "provider accounting must keep observing the real B-leg usage")

	// Every ordinary client-path observer sees the ordinary traffic and nothing
	// claimed.
	for name, seen := range map[string][]string{
		"finalizer":     rig.finalizer.finalized(),
		"tool_policy":   rig.policy.observed(),
		"reactor":       rig.reactor.observed(),
		"response_hook": rig.respHook.observed(),
		"client_events": interceptReleasedLabels(released),
		"ptc":           rig.traffic.observed(sdktraffic.LegPTC),
	} {
		assert.Zero(t, countContaining(seen, interceptToolName),
			"%s must see ZERO claimed control traffic; seen=%v", name, seen)
		assert.Zero(t, countContaining(seen, "control-1"),
			"%s must never see the claimed control call ID; seen=%v", name, seen)
	}

	// The ordinary traffic really did reach those same observers, so the case
	// above is not vacuous.
	assert.Contains(t, rig.finalizer.finalized(), interceptOrdinary,
		"the ordinary client tool must still be finalized")
	assert.Positive(t, countContaining(rig.policy.observed(), interceptOrdinary),
		"the ordinary client tool must still reach tool policy; seen=%v", rig.policy.observed())
	assert.Positive(t, countContaining(rig.reactor.observed(), interceptOrdinary),
		"the ordinary client tool must still reach the tool reactor; seen=%v", rig.reactor.observed())
	assert.Positive(t, countContaining(rig.respHook.observed(), string(aipapi.EventTextDelta)),
		"ordinary text must still reach the response hook; seen=%v", rig.respHook.observed())
	assert.Positive(t, countContaining(interceptReleasedLabels(released), string(aipapi.EventTextDelta)),
		"ordinary text must still be released to the client; released=%v", interceptReleasedLabels(released))
	assert.Positive(t, countContaining(interceptReleasedLabels(released), "ordinary-1"),
		"the ordinary client tool must still be released to the client; released=%v", interceptReleasedLabels(released))

	// No result text was published by this task; tasks 5.1/5.2 own that.
	assert.Zero(t, countContaining(interceptReleasedLabels(released), "all done"),
		"task 4.2 must not publish result text to the client; released=%v", interceptReleasedLabels(released))
}

func interceptReleasedLabels(released []aipapi.Event) []string {
	out := make([]string, 0, len(released))
	for _, ev := range released {
		out = append(out, interceptEventLabel(ev))
	}
	return out
}

// TestControlCallInterception_handlerRunsAfterBTPAndUsageObservation pins the
// binding order in design Response Interception / Placement: BTP and provider
// usage observation happen before the capture consumes the event and before the
// handler runs. It is asserted from real call order through a shared log, never
// from timing.
func TestControlCallInterception_handlerRunsAfterBTPAndUsageObservation(t *testing.T) {
	t.Parallel()

	ordered := &interceptOrderLog{}
	provider := &interceptProvider{
		outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "control_complete"},
		log:     ordered,
	}
	rig := newInterceptRigOnLog(t, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes), ordered)
	ctx := context.Background()

	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		interceptStart("control-1", interceptToolName),
		{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 1, TotalTokens: 4},
		interceptArgsDelta("control-1", interceptArgs),
		interceptFinish("control-1"),
	} {
		_, err := rig.drive(ctx, ev)
		require.NoError(t, err, "%s must not fail the response", ev.Kind)
	}

	seq := ordered.snapshot()
	startAt := interceptIndexOfPrefix(seq, "handle:")
	require.GreaterOrEqual(t, startAt, 0, "the handler must run; steps=%v", seq)
	require.Positive(t, rig.usage.count(), "provider accounting must observe the usage delta")

	// Every claimed event is observed on the operator BTP leg before the handler
	// runs, and the provider usage leg is observed too (requirement 11.3).
	for _, want := range []string{
		"traffic:" + string(sdktraffic.LegBTP) + ":" + string(aipapi.EventToolCallStarted) + ":control-1",
		"traffic:" + string(sdktraffic.LegBTP) + ":" + string(aipapi.EventToolCallFinished) + ":control-1",
		"usage:",
	} {
		idx := interceptIndexOfPrefix(seq, want)
		require.GreaterOrEqual(t, idx, 0, "BTP and usage observation must precede capture; missing %q; steps=%v", want, seq)
		assert.Less(t, idx, startAt,
			"operator BTP and provider usage observation must precede the handler invocation (%q); steps=%v", want, seq)
	}
}

func interceptIndexOfPrefix(steps []string, prefix string) int {
	for i, step := range steps {
		if strings.HasPrefix(step, prefix) {
			return i
		}
	}
	return -1
}

// TestControlCallInterception_ordinaryTrafficStreamsWithoutWholeResponseBuffering
// pins requirements 5.1 and 12.4: ordinary text around a claimed control call is
// released incrementally, event by event, with no whole-response buffering and no
// added delay. The assertion is deterministic event order, not timing.
func TestControlCallInterception_ordinaryTrafficStreamsWithoutWholeResponseBuffering(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{
		outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "control_complete"},
	}
	rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
	ctx := context.Background()

	// Ordinary text, control start, ordinary text again: the second ordinary
	// delta must be released before the control call has even finished.
	first, err := rig.drive(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "before"})
	require.NoError(t, err)
	require.Len(t, first, 1, "ordinary text before the control call must be released immediately")
	assert.Equal(t, "before", first[0].Delta, "ordinary text must be released unchanged")

	claimed, err := rig.drive(ctx, interceptStart("control-1", interceptToolName))
	require.NoError(t, err)
	assert.Empty(t, claimed, "a claimed control start must not be released to the client")

	second, err := rig.drive(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "during"})
	require.NoError(t, err)
	require.Len(t, second, 1,
		"ordinary text interleaved with an open control call must stream immediately, not wait for the response to finish")
	assert.Equal(t, "during", second[0].Delta, "interleaved ordinary text must be released unchanged")
	assert.Zero(t, provider.handleCount(), "an open control call must not invoke the handler")

	_, err = rig.drive(ctx, interceptArgsDelta("control-1", interceptArgs))
	require.NoError(t, err)
	_, err = rig.drive(ctx, interceptFinish("control-1"))
	require.NoError(t, err)
	assert.Equal(t, 1, provider.handleCount(), "the handler runs exactly once at the bounded finish")
}

// TestControlCallInterception_malformedAndMixedSequencesStayPrivate walks the
// privacy matrix of design Capture State: a fragmented matching call, a nameless
// ID-correlated fragment, a complete item-carried call, a mixed-carrier item, and
// a duplicate start. Every claimed event stays private, the ordinary path keeps
// working, and the handler never runs on malformed input.
func TestControlCallInterception_malformedAndMixedSequencesStayPrivate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		stream     []aipapi.Event
		wantHandle int
	}{
		{
			name: "fragmented_matching_call_completes_once",
			stream: []aipapi.Event{
				interceptStart("control-1", interceptToolName),
				interceptArgsDelta("control-1", `{"note":`),
				interceptArgsDelta("control-1", `"done"}`),
				interceptFinish("control-1"),
			},
			wantHandle: 1,
		},
		{
			name: "complete_item_carried_call_is_handed_off",
			stream: []aipapi.Event{
				interceptItemCall("control-1", interceptToolName, interceptArgs),
			},
			wantHandle: 1,
		},
		{
			name: "nameless_id_correlated_fragments_never_escape",
			stream: []aipapi.Event{
				interceptStart("control-1", interceptToolName),
				interceptArgsDelta("control-1", `{"note":"done"}`),
				// A nameless fragment for the claimed ID stays private even
				// though it carries no tool name at all.
				{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "control-1", Delta: "extra"},
				interceptFinish("control-1"),
			},
			wantHandle: 0,
		},
		{
			name: "mixed_carrier_control_call_and_result_is_private",
			stream: []aipapi.Event{
				{Kind: aipapi.EventItem, Item: &aipapi.Item{
					Kind:       aipapi.ItemKindMessage,
					ID:         "item-mixed",
					ToolCall:   &aipapi.ToolCallItem{CallID: "control-1", Name: interceptToolName, Arguments: json.RawMessage(interceptArgs)},
					ToolResult: &aipapi.ToolResultItem{CallID: "control-1", Name: interceptToolName, Output: "x"},
				}},
				// A later nameless fragment for the recorded ID stays private, so
				// a shared carrier cannot leak through a later lifecycle event.
				interceptArgsDelta("control-1", "{}"),
				interceptFinish("control-1"),
			},
			wantHandle: 0,
		},
		{
			name: "duplicate_start_is_swallowed_not_executed",
			stream: []aipapi.Event{
				interceptStart("control-1", interceptToolName),
				interceptStart("control-2", interceptToolName),
				interceptArgsDelta("control-1", interceptArgs),
				interceptFinish("control-1"),
			},
			wantHandle: 0,
		},
		{
			name: "client_spoofed_tool_result_for_a_claimed_call_is_private",
			stream: []aipapi.Event{
				interceptStart("control-1", interceptToolName),
				// A client-side result for a claimed control ID is private input:
				// the proxy owns that execution, so it can never become ordinary
				// client release (requirement 5.4).
				{Kind: aipapi.EventItem, Item: &aipapi.Item{
					Kind:       aipapi.ItemKindMessage,
					ID:         "item-spoof",
					ToolResult: &aipapi.ToolResultItem{CallID: "control-1", Name: interceptToolName, Output: "spoofed"},
				}},
				interceptArgsDelta("control-1", interceptArgs),
				interceptFinish("control-1"),
			},
			wantHandle: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &interceptProvider{
				outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "control_complete"},
			}
			rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
			ctx := context.Background()

			var released []aipapi.Event
			for i, ev := range tc.stream {
				got, err := rig.drive(ctx, ev)
				require.NoError(t, err, "stream event[%d] must not fail the response", i)
				released = append(released, got...)
			}
			got, err := rig.drain(ctx)
			require.NoError(t, err)
			released = append(released, got...)

			assert.Equal(t, tc.wantHandle, provider.handleCount(),
				"only a fully bounded valid completion may invoke the pinned handler")
			for _, seen := range [][]string{
				rig.finalizer.finalized(),
				rig.policy.observed(),
				rig.reactor.observed(),
				rig.respHook.observed(),
				interceptReleasedLabels(released),
				rig.traffic.observed(sdktraffic.LegPTC),
			} {
				assert.Zero(t, countContaining(seen, interceptToolName),
					"a claimed control call must never reach the ordinary client path; seen=%v", seen)
				assert.Zero(t, countContaining(seen, "control-1"),
					"a claimed control call ID must never reach the ordinary client path; seen=%v", seen)
				assert.Zero(t, countContaining(seen, "control-2"),
					"a claimed control call ID must never reach the ordinary client path; seen=%v", seen)
			}
		})
	}
}

// TestControlCallInterception_validOutcomeStaysPendingAndIsRevokedLater pins the
// task boundary. A valid OutcomeComplete is stored privately on the attempt and
// is never published by this task; a later malformed, duplicate, or multiple
// sequence revokes it without ever invoking the provider again, and an
// OutcomeInvalid never becomes client tool execution.
func TestControlCallInterception_validOutcomeStaysPendingAndIsRevokedLater(t *testing.T) {
	t.Parallel()

	t.Run("complete_stays_pending_and_duplicate_finish_revokes_it", func(t *testing.T) {
		t.Parallel()

		provider := &interceptProvider{
			outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "the answer", ReasonCode: "control_complete"},
		}
		rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
		ctx := context.Background()

		for _, ev := range []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			interceptStart("control-1", interceptToolName),
			interceptArgsDelta("control-1", interceptArgs),
			interceptFinish("control-1"),
		} {
			_, err := rig.drive(ctx, ev)
			require.NoError(t, err, "%s must not fail the response", ev.Kind)
		}

		require.Equal(t, 1, provider.handleCount(), "the handler must have run once")
		require.NotNil(t, rig.attempt.controlOutcome,
			"a valid outcome must stay pending and private for the later terminal owner")
		assert.Equal(t, controltool.OutcomeComplete, rig.attempt.controlOutcome.Kind)
		assert.Equal(t, "the answer", rig.attempt.controlOutcome.ResultText)

		// A duplicate finish after handoff is malformed input: it revokes the
		// valid outcome without a second invocation.
		_, err := rig.drive(ctx, interceptFinish("control-1"))
		require.NoError(t, err, "a duplicate finish is swallowed privately, not surfaced as a response failure")
		assert.Nil(t, rig.attempt.controlOutcome,
			"a malformed sequence after a valid completion must revoke the pending outcome")
		assert.Equal(t, 1, provider.handleCount(), "a revoked outcome must never re-invoke the provider")
	})

	t.Run("normal_finish_preserves_a_valid_completion", func(t *testing.T) {
		t.Parallel()

		provider := &interceptProvider{
			outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "the answer", ReasonCode: "control_complete"},
		}
		rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
		ctx := context.Background()

		for _, ev := range []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			interceptStart("control-1", interceptToolName),
			interceptArgsDelta("control-1", interceptArgs),
			interceptFinish("control-1"),
			{Kind: aipapi.EventResponseFinished},
		} {
			_, err := rig.drive(ctx, ev)
			require.NoError(t, err, "%s must not fail the response", ev.Kind)
		}

		require.NotNil(t, rig.attempt.controlOutcome,
			"a normal response finish must preserve an already valid completion for the terminal owner")
		assert.Equal(t, 1, provider.handleCount(), "the handler must have run exactly once")
	})

	t.Run("incomplete_finish_invokes_no_handler_and_marks_invalid", func(t *testing.T) {
		t.Parallel()

		provider := &interceptProvider{
			outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "the answer", ReasonCode: "control_complete"},
		}
		rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
		ctx := context.Background()

		for _, ev := range []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			interceptStart("control-1", interceptToolName),
			interceptArgsDelta("control-1", `{"note":`),
			{Kind: aipapi.EventResponseFinished},
		} {
			_, err := rig.drive(ctx, ev)
			require.NoError(t, err, "%s must not fail the response", ev.Kind)
		}

		assert.Zero(t, provider.handleCount(),
			"an unfinished control call must never invoke the handler; partial arguments are not a completion")
		assert.Nil(t, rig.attempt.controlOutcome, "an unfinished control call must leave no pending outcome")
		assert.True(t, rig.attempt.controlCapture.invalid(),
			"an unfinished control call must be recorded as invalid on the attempt")
		assert.Equal(t, controlReasonUnterminated, rig.attempt.controlCapture.reasonCode(),
			"the bounded static reason must classify the incomplete invocation")
	})

	t.Run("provider_invalid_outcome_is_private_and_not_client_execution", func(t *testing.T) {
		t.Parallel()

		provider := &interceptProvider{
			outcome: controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: "model_mistake"},
		}
		rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
		ctx := context.Background()

		var released []aipapi.Event
		for _, ev := range []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			interceptStart("control-1", interceptToolName),
			interceptArgsDelta("control-1", interceptArgs),
			interceptFinish("control-1"),
			{Kind: aipapi.EventResponseFinished},
		} {
			got, err := rig.drive(ctx, ev)
			require.NoError(t, err, "%s must not fail the response", ev.Kind)
			released = append(released, got...)
		}

		require.Equal(t, 1, provider.handleCount(), "the handler must have run once")
		require.NotNil(t, rig.attempt.controlOutcome, "an invalid outcome is still recorded privately")
		assert.Equal(t, controltool.OutcomeInvalid, rig.attempt.controlOutcome.Kind)
		for _, seen := range [][]string{
			rig.finalizer.finalized(),
			rig.policy.observed(),
			rig.reactor.observed(),
			rig.respHook.observed(),
			interceptReleasedLabels(released),
			rig.traffic.observed(sdktraffic.LegPTC),
		} {
			assert.Zero(t, countContaining(seen, interceptToolName),
				"an invalid control outcome must never become client tool execution; seen=%v", seen)
		}
	})
}

// TestControlCallInterception_handlerFailureIsBoundedAndFailsSafely pins the
// failure seam end to end (requirements 5.5, 5.6, 11.2): a provider error, a
// handler panic, and an unknown outcome kind all fail the response with the
// static, content-free sentinel, clear the pending outcome, and leak no planted
// secret anywhere.
func TestControlCallInterception_handlerFailureIsBoundedAndFailsSafely(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		arm    func(p *interceptProvider)
		secret string
	}{
		{
			name:   "provider_error",
			arm:    func(p *interceptProvider) { p.err = errWithSecret(interceptReasonSecret) },
			secret: interceptReasonSecret,
		},
		{
			name:   "provider_panic",
			arm:    func(p *interceptProvider) { p.panicV = "panic payload " + interceptResultSecret },
			secret: interceptResultSecret,
		},
		{
			name: "unknown_outcome_kind",
			arm: func(p *interceptProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeKind(11), ReasonCode: "weird"}
			},
			secret: "",
		},
		{
			name: "invalid_outcome_with_client_output",
			arm: func(p *interceptProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeInvalid, ResultText: interceptResultSecret, ReasonCode: "r"}
			},
			secret: interceptResultSecret,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &interceptProvider{}
			tc.arm(provider)
			rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
			ctx := context.Background()

			var released []aipapi.Event
			var gotErr error
			for _, ev := range []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				interceptStart("control-1", interceptToolName),
				interceptArgsDelta("control-1", interceptArgs),
				interceptFinish("control-1"),
			} {
				got, err := rig.drive(ctx, ev)
				released = append(released, got...)
				if err != nil {
					gotErr = err
					break
				}
			}

			require.Error(t, gotErr, "a failed control handler must fail the response safely")
			assert.ErrorIs(t, gotErr, extensions.ErrControlHandleFailed,
				"the response must fail with the static, content-free sentinel; err=%v", gotErr)
			if tc.secret != "" {
				assert.NotContains(t, gotErr.Error(), tc.secret,
					"no provider text may reach the receive error; err=%v", gotErr)
			}
			assert.Nil(t, rig.attempt.controlOutcome,
				"a failed handler must clear the pending outcome rather than leave partial state")
			for _, seen := range [][]string{
				rig.finalizer.finalized(),
				rig.policy.observed(),
				rig.reactor.observed(),
				rig.respHook.observed(),
				interceptReleasedLabels(released),
				rig.traffic.observed(sdktraffic.LegPTC),
			} {
				assert.Zero(t, countContaining(seen, interceptToolName),
					"a failed control call must never fall through to ordinary client tool execution; seen=%v", seen)
			}
		})
	}
}

// TestControlCallInterception_correlationFatalAbortsBeforeFurtherEvents pins the
// correlation bound (requirement 5.5). An excess correlation ID cannot be
// retained, so the owner must abort immediately: the fatal is reported on the
// event that could not be correlated, the pending outcome is cleared, and the
// response fails rather than continuing to leak later backend events.
func TestControlCallInterception_correlationFatalAbortsBeforeFurtherEvents(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{
		outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "control_complete"},
	}
	rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
	ctx := context.Background()

	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		interceptStart("control-1", interceptToolName),
		interceptArgsDelta("control-1", interceptArgs),
		interceptFinish("control-1"),
	} {
		_, err := rig.drive(ctx, ev)
		require.NoError(t, err, "%s must not fail the response", ev.Kind)
	}
	require.NotNil(t, rig.attempt.controlOutcome, "the fixture must first produce a valid completion")

	// Exhaust the bounded correlation window with fresh control-named starts.
	var fatal error
	for i := range controlClaimedIDCapacity + 2 {
		ev := interceptStart("overflow-"+interceptOrdinal(i), interceptToolName)
		_, err := rig.drive(ctx, ev)
		if err != nil {
			fatal = err
			break
		}
	}

	require.Error(t, fatal, "an exhausted correlation window must abort the response")
	assert.ErrorIs(t, fatal, errControlCallCorrelationExhausted,
		"the abort must carry the static bounded correlation error; err=%v", fatal)
	assert.Nil(t, rig.attempt.controlOutcome,
		"a fatal correlation failure must clear the pending outcome")

	// The fatal is sticky, so it keeps reporting itself for every further event
	// this response can still be asked about. The Recv loop already turns a
	// non-nil prepared error into a terminal partial failure and returns, so this
	// is the property that makes an abort safe rather than merely recorded.
	_, lateErr := rig.drive(ctx, interceptStart("control-late", interceptToolName))
	require.Error(t, lateErr, "a fatal correlation failure must keep aborting")
	assert.ErrorIs(t, lateErr, errControlCallCorrelationExhausted,
		"the sticky fatal must keep reporting itself; err=%v", lateErr)
	assert.Equal(t, 1, provider.handleCount(), "the provider must never be re-invoked after a fatal")
}

func interceptOrdinal(i int) string {
	// Small, deterministic, and bounded; the capture stores only a digest of it.
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if i < len(digits) {
		return string(digits[i])
	}
	return string(digits[i/len(digits)]) + string(digits[i%len(digits)])
}

func errWithSecret(secret string) error {
	return &interceptSecretError{secret: secret}
}

// interceptSecretError carries a planted secret in every text-bearing accessor so
// any normalization that propagates provider text fails loudly.
type interceptSecretError struct{ secret string }

func (e *interceptSecretError) Error() string { return "control handler rejected: " + e.secret }

// TestControlCallInterception_inactiveActivationPreservesOrdinaryBehavior pins the
// removal contract (requirements 4.5, 10.5, 12.3). With no trusted activation the
// capture is absent, every event including a control-shaped tool name is ordinary,
// the pinned provider does no work at all, and the ordinary client path is
// byte-for-byte unchanged.
func TestControlCallInterception_inactiveActivationPreservesOrdinaryBehavior(t *testing.T) {
	t.Parallel()

	rig := inertInterceptRig(t)
	require.Nil(t, rig.attempt.controlCapture,
		"an absent or inactive activation must build no capture at all")

	ctx := context.Background()
	stream := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		interceptStart("ordinary-1", interceptOrdinary),
		interceptArgsDelta("ordinary-1", interceptOrdinaryArg),
		interceptFinish("ordinary-1"),
		// A control-shaped tool name with no trusted activation stays ordinary
		// client traffic rather than being adopted.
		interceptStart("control-shaped-1", interceptToolName),
		interceptArgsDelta("control-shaped-1", interceptArgs),
		interceptFinish("control-shaped-1"),
		{Kind: aipapi.EventResponseFinished},
	}

	var released []aipapi.Event
	for i, ev := range stream {
		got, err := rig.drive(ctx, ev)
		require.NoError(t, err, "stream event[%d] must not fail the response", i)
		released = append(released, got...)
		got, err = rig.drain(ctx)
		require.NoError(t, err)
		released = append(released, got...)
	}

	assert.Contains(t, rig.finalizer.finalized(), interceptOrdinary,
		"the ordinary client tool must still be finalized")
	assert.Contains(t, rig.finalizer.finalized(), interceptToolName,
		"without a trusted activation a control-shaped tool stays ordinary client traffic")
	assert.Positive(t, countContaining(interceptReleasedLabels(released), string(aipapi.EventTextDelta)),
		"ordinary text must still be released")
	assert.Positive(t, countContaining(rig.policy.observed(), interceptToolName),
		"an untrusted control-shaped tool must still reach ordinary tool policy")
	assert.Zero(t, rig.attempt.controlHandled, "no control handoff may happen without a capture")
	assert.Nil(t, rig.attempt.controlOutcome, "no pending outcome may exist without a capture")
}

// TestControlCallInterception_snapshotReloadDoesNotChangeTheInvokedProvider pins
// the generation pin (requirement 10.3). A capture created from the activation
// frozen at open keeps addressing the same generation instance even after the
// live request snapshot is replaced with one that admits no control provider at
// all, and the handler still never reads a live identity or spec.
func TestControlCallInterception_snapshotReloadDoesNotChangeTheInvokedProvider(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{
		outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "control_complete"},
	}
	rig := newInterceptRig(t, provider, interceptActivation(t, provider, controltool.DefaultMaxArgsBytes))
	ctx := context.Background()

	// Open the control call against the pinned generation.
	_, err := rig.drive(ctx, interceptStart("control-1", interceptToolName))
	require.NoError(t, err)

	// Reload: the live snapshot now admits no control provider whatsoever. A
	// response path that re-resolved its provider from the current snapshot
	// would find nothing here; the frozen activation must not care.
	rig.p.runtimeSnapshot = extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		FeaturePlanes: aipfeature.NewContributionSet().Freeze(),
	})
	require.Nil(t, rig.p.runtimeSnapshot.ControlToolProvider(),
		"the reloaded generation must admit no control provider")

	_, err = rig.drive(ctx, interceptArgsDelta("control-1", interceptArgs))
	require.NoError(t, err, "a snapshot reload must not fail an in-flight control call")
	_, err = rig.drive(ctx, interceptFinish("control-1"))
	require.NoError(t, err, "a snapshot reload must not fail an in-flight control call")

	require.Equal(t, 1, provider.handleCount(),
		"the pinned generation provider must still handle the call after a reload")
	require.NotNil(t, rig.attempt.controlOutcome,
		"a reloaded snapshot must not revoke an in-flight valid completion")
}

// TestControlCallInterception_frozenProvenanceIsDeepOwned pins that the frozen
// Meta the provider receives cannot reach back into the activation's own views
// through a shared backing slice, and that a provider mutating what it was handed
// cannot alter the attempt's provenance.
func TestControlCallInterception_frozenProvenanceIsDeepOwned(t *testing.T) {
	t.Parallel()

	mutating := &interceptMutatingProvider{interceptProvider: &interceptProvider{
		outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "ok", ReasonCode: "control_complete"},
	}}
	activation := interceptActivation(t, mutating, controltool.DefaultMaxArgsBytes)
	// Give the frozen views real backing data, exactly as a request would.
	activation.meta.Scope.Roles = []string{"role-a"}
	activation.meta.Session.Labels = map[string]string{"label-a": "value-a"}
	activation.meta.Workspace.Markers = []string{"marker-a"}

	rig := newInterceptRigWithActivation(t, activation)
	ctx := context.Background()

	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		interceptStart("control-1", interceptToolName),
		interceptArgsDelta("control-1", interceptArgs),
		interceptFinish("control-1"),
	} {
		_, err := rig.drive(ctx, ev)
		require.NoError(t, err, "%s must not fail the response", ev.Kind)
	}

	require.Equal(t, 1, mutating.handleCount(), "the mutating provider must have handled the call once")
	assert.Equal(t, []string{"role-a"}, activation.meta.Scope.Roles,
		"a provider mutating the scope it was handed must not alter the activation's frozen provenance")
	assert.Equal(t, map[string]string{"label-a": "value-a"}, activation.meta.Session.Labels,
		"a provider mutating the session it was handed must not alter the activation's frozen provenance")
	assert.Equal(t, []string{"marker-a"}, activation.meta.Workspace.Markers,
		"a provider mutating the workspace it was handed must not alter the activation's frozen provenance")
}

// interceptMutatingProvider does its best to corrupt every view it is handed, so
// a shallow copy of the frozen provenance fails the ownership assertion.
type interceptMutatingProvider struct {
	*interceptProvider
}

func (p *interceptMutatingProvider) Handle(ctx context.Context, call controltool.CompletedCall, meta controltool.Meta) (controltool.Outcome, error) {
	if len(meta.Scope.Roles) > 0 {
		meta.Scope.Roles[0] = "corrupted"
	}
	if meta.Session.Labels != nil {
		meta.Session.Labels["label-a"] = "corrupted"
	}
	if len(meta.Workspace.Markers) > 0 {
		meta.Workspace.Markers[0] = "corrupted"
	}
	return p.interceptProvider.Handle(ctx, call, meta)
}

// interceptRecvCountingStream is the backend stream with a read counter, so a case
// can prove the receive loop stopped asking the backend rather than merely
// discarding an event after reading it.
type interceptRecvCountingStream struct {
	aipapi.ManagedEventStream
	calls int
}

func (s *interceptRecvCountingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.calls++
	return s.ManagedEventStream.Recv(ctx)
}

// TestControlCallInterception_publicRecvPreservesPTCPrivacyAndStopsOnFatal drives
// the real public entry point, retryRecvStream.Recv, rather than the internal
// preparation and transformation steps.
//
// The earlier seam cases stop at prepareRecvEvent/transformClientEvent, which never
// reaches observeClientFacing. That makes their "PTC saw nothing" assertion
// vacuous: an untouched PTC probe would satisfy it just as well. Here ordinary text
// must positively reach live PTC observation, so the negative control-traffic
// assertion below actually discriminates. The fatal row additionally proves the
// abort happens before the next backend read, not after it.
func TestControlCallInterception_publicRecvPreservesPTCPrivacyAndStopsOnFatal(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "fatal"}[fatal], func(t *testing.T) {
			provider := &interceptProvider{outcome: controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: "private result", ReasonCode: "complete"}}
			rig := newInterceptRig(t, provider, nil)
			stream := []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "before"}, interceptStart("control-1", interceptToolName), interceptArgsDelta("control-1", interceptArgs), interceptFinish("control-1")}
			if fatal {
				for i := range controlClaimedIDCapacity {
					stream = append(stream, interceptStart("overflow-"+interceptOrdinal(i), interceptToolName))
				}
			}
			stream = append(stream, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "after"})
			inner := &interceptRecvCountingStream{ManagedEventStream: aipapi.NewFixedEventStream(stream)}
			rig.attempt.inner = inner
			rig.p.policyEvidenceEmitter = func(*extensions.RequestRuntimeSnapshot) *extensions.EvidenceEmitter { return nil }
			rs := &retryRecvStream{facts: recvTurnFacts{traceID: "trace-intercept-1", aLegID: "aleg-intercept-1"}, responsePipeline: rig.p, attempt: attemptSlot{current: rig.attempt}, terminal: newTurnTerminalWithALeg(nil, aLegEndBase), recovery: &recoveryController{}}
			first, err := rs.Recv(context.Background())
			require.NoError(t, err)
			require.Equal(t, "before", first.Delta)
			second, err := rs.Recv(context.Background())
			if fatal {
				require.ErrorIs(t, err, errControlCallCorrelationExhausted)
				require.Less(t, inner.calls, len(stream))
				require.Nil(t, rig.attempt.controlOutcome)
			} else {
				require.NoError(t, err)
				require.Equal(t, "after", second.Delta)
				require.NotNil(t, rig.attempt.controlOutcome)
			}
			require.Equal(t, 1, provider.handleCount())
			ptc := rig.traffic.observed(sdktraffic.LegPTC)
			require.Positive(t, countContaining(ptc, "text_delta"), "PTC probe must actually run")
			require.Zero(t, countContaining(ptc, "control-1"))
		})
	}
}
