// Task 5.1 fixtures for the additive terminal-decision completion expectation
// (spec .kiro/specs/agent-loop-explicit-completion-protocol, requirements 3.2-3.4,
// 6.1-6.2, 7.1, 7.6, 9.3; design Completion Evidence and Pending Result).
//
// Every case binds to the real trusted activation, the real private capture, and
// a real captured Outcome driven through the response seam, so no case can pass
// by writing a field directly. The projection under test stays generic: it reads
// only the attempt's frozen activation and its own private validated outcome, and
// it never names a feature, a tool, or a provider.
package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	expectToolName    = "attempt_completion"
	expectToolDesc    = "Call this only when the work the user requested for this task is complete."
	expectInstruction = "Call attempt_completion only when the requested work is complete."
	expectSchema      = `{"type":"object","properties":{"result":{"type":"string"}},"required":["result"],"additionalProperties":false}`
	expectArgs        = `{"result":"migration complete"}`
	expectResultText  = "the migration is complete"
)

// expectSpec is the documented completion protocol contract of design Handler
// Semantics: exactly one required string `result`, no `command`, additional
// properties rejected.
func expectSpec(maxArgs int) controltool.Spec {
	return controltool.Spec{
		Tool: aipapi.ToolDef{
			Name:        expectToolName,
			Description: expectToolDesc,
			Parameters:  json.RawMessage(expectSchema),
		},
		Instruction:  controltool.Instruction{Role: aipapi.RoleSystem, Text: expectInstruction},
		MaxArgsBytes: maxArgs,
	}
}

// expectClientCall is one ordinary client candidate call the completion
// protocol may legitimately be projected onto.
func expectClientCall() aipapi.Call {
	return aipapi.Call{
		ID:       "expect-attempt",
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("finish the migration")}}},
		Tools:    []aipapi.ToolDef{{Name: interceptOrdinary, Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
}

// expectActiveCompletionActivation freezes the real completion contract onto an
// eligible candidate, exactly as the request path does, so the active cases
// drive the real proxy-owned tool name.
func expectActiveCompletionActivation(t *testing.T, provider controltool.Provider) *controlToolActivation {
	t.Helper()
	spec := expectSpec(controltool.DefaultMaxArgsBytes)
	require.NoError(t, controltool.ValidateSpec(spec), "the evidence fixture must be a valid control spec")
	_, projection, err := controltool.Project(expectClientCall(), spec, aipapi.NewBackendCaps(aipapi.CapabilityTools))
	require.NoError(t, err)
	require.True(t, projection.Active(), "the active evidence fixture must be eligible for the completion protocol")
	require.Equal(t, expectToolName, projection.ToolName(),
		"the frozen projection is the only source of the proxy-owned tool name")
	return &controlToolActivation{
		providerID: interceptProviderID,
		provider:   provider,
		projection: projection,
		meta: controltool.Meta{
			TraceID:      "trace-expect-1",
			ALegID:       "aleg-expect-1",
			BLegID:       "bleg-expect-1",
			CandidateKey: "openai:gpt-4",
			AttemptSeq:   1,
		},
	}
}

// expectIneligibleActivation freezes a real, ineligible projection exactly like
// the request path does, so the eligibility matrix binds to real projection
// semantics instead of a hand-made inactive activation. wantReason must be the
// bounded reason the projection actually reports, so a silently eligible case
// fails here instead of passing vacuously.
func expectIneligibleActivation(t *testing.T, provider controltool.Provider, call aipapi.Call, caps aipapi.BackendCaps, wantReason string) *controlToolActivation {
	t.Helper()
	spec := expectSpec(controltool.DefaultMaxArgsBytes)
	require.NoError(t, controltool.ValidateSpec(spec), "the evidence fixture must be a valid control spec")
	_, projection, err := controltool.Project(call, spec, caps)
	require.NoError(t, err)
	require.False(t, projection.Active(), "this fixture must freeze an ineligible projection")
	require.Equal(t, wantReason, projection.Reason(), "the fixture must freeze the intended ineligibility reason")
	return &controlToolActivation{
		providerID: interceptProviderID,
		provider:   provider,
		projection: projection,
		meta: controltool.Meta{
			TraceID:      "trace-expect-1",
			ALegID:       "aleg-expect-1",
			BLegID:       "bleg-expect-1",
			CandidateKey: "openai:gpt-4",
			AttemptSeq:   1,
		},
	}
}

// expectRequest is the canonical request the terminal owner projects. Native
// completion items are passed in explicitly so the native and proxy facts stay
// separable.
func expectRequest(items ...aipapi.Item) requestTerminalFacts {
	return requestTerminalFacts{
		call:    aipapi.Call{ID: "request-expect", Items: items},
		traceID: "trace-expect-1",
		aLegID:  "aleg-expect-1",
	}
}

// expectRequestWithUnresolvedOrdinaryTool adds an ordinary client tool call that
// is still in progress on the request trajectory.
//
// That unresolved state is exactly what a later terminal policy must be able to
// see and gate on, so task 5.1 must project it unchanged and must not let a
// private proxy completion hide it or substitute for it.
func expectRequestWithUnresolvedOrdinaryTool() requestTerminalFacts {
	return expectRequest(
		expectUserItem(),
		aipapi.Item{
			Kind:     aipapi.ItemKindToolCall,
			ID:       "item-ordinary-open",
			Status:   aipapi.ItemStatusInProgress,
			ToolCall: &aipapi.ToolCallItem{CallID: "ordinary-open", Name: interceptOrdinary},
		},
	)
}

// expectUserItem is the ordinary objective carrier every case shares.
func expectUserItem() aipapi.Item {
	return aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		ID:      "user-expect",
		Status:  aipapi.ItemStatusCompleted,
		Role:    aipapi.RoleUser,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "finish the migration"}},
	}
}

// expectNativeCompletionItems is a client/harness-owned completion that already
// ran through the ordinary client tool-result path (requirements 3.3, 3.4, 9.3).
func expectNativeCompletionItems() []aipapi.Item {
	return []aipapi.Item{
		expectUserItem(),
		{
			Kind:     aipapi.ItemKindToolCall,
			ID:       "item-native-call",
			Status:   aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{CallID: "call-native", Name: expectToolName, Arguments: json.RawMessage(expectArgs)},
		},
		{
			Kind:       aipapi.ItemKindToolResult,
			ID:         "item-native-result",
			Status:     aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{CallID: "call-native", Name: expectToolName, Output: "native harness output"},
		},
	}
}

func expectCompleteOutcome() controltool.Outcome {
	return controltool.Outcome{Kind: controltool.OutcomeComplete, ResultText: expectResultText, ReasonCode: "control_complete"}
}

// driveExpectStream runs one real event stream through the response seam and
// then through the same client-facing observation the receive loop performs,
// returning the events actually released to the client.
//
// Reproducing that observation is what makes the pipeline's released seen-event
// copy real: the terminal evidence projection later reads that copy, so a
// swallowed private control lifecycle is provably absent from it while ordinary
// traffic is provably present.
func driveExpectStream(t *testing.T, rig *interceptRig, stream ...aipapi.Event) []aipapi.Event {
	t.Helper()
	ctx := context.Background()
	var released []aipapi.Event
	for i, ev := range stream {
		got, err := rig.drive(ctx, ev)
		require.NoError(t, err, "stream event[%d] (%s) must not fail the response", i, ev.Kind)
		released = append(released, got...)
		drained, err := rig.drain(ctx)
		require.NoError(t, err, "draining after stream event[%d] must not fail the response", i)
		released = append(released, drained...)
		for _, out := range got {
			rig.p.rememberClientEvent(out)
		}
		for _, out := range drained {
			rig.p.rememberClientEvent(out)
		}
	}
	return released
}

// driveExpectCompletion runs one real proxy-owned completion lifecycle and
// asserts the pinned provider was invoked exactly once for it.
func driveExpectCompletion(t *testing.T, rig *interceptRig, provider *interceptProvider, stream ...aipapi.Event) []aipapi.Event {
	t.Helper()
	released := driveExpectStream(t, rig, stream...)
	require.Equal(t, 1, provider.handleCount(),
		"the pinned provider must be invoked exactly once for the one completion")
	return released
}

// releasedToolNames lists every tool name that actually reached the client, so a
// case can assert about a real release rather than about event labels that carry
// only a call identity.
func releasedToolNames(released []aipapi.Event) []string {
	var names []string
	for _, ev := range released {
		if ev.ToolName != "" {
			names = append(names, ev.ToolName)
		}
		if ev.Item != nil && ev.Item.ToolCall != nil && ev.Item.ToolCall.Name != "" {
			names = append(names, ev.Item.ToolCall.Name)
		}
		if ev.Item != nil && ev.Item.ToolResult != nil && ev.Item.ToolResult.Name != "" {
			names = append(names, ev.Item.ToolResult.Name)
		}
	}
	return names
}

// evidenceActionNames projects the bounded action names the provider may read.
func evidenceActionNames(evidence terminaldecision.Evidence) []string {
	names := make([]string, 0, evidence.ActionCount)
	for _, action := range evidence.Actions[:evidence.ActionCount] {
		if action.Name != "" {
			names = append(names, action.Name)
		}
	}
	return names
}

// TestProjectTerminalDecisionEvidence_ExpectationRequiresSuccessfulActivation pins
// that the expectation is derived only from a successfully active proxy-owned
// control protocol (requirements 3.2, 4.1-4.4, 7.6).
//
// The ineligibility rows are frozen through real projections, so a case cannot
// pass by asserting against an activation that was never approved: an absent
// attempt, an attempt with no activation, a hand-made inactive activation, and
// the backend-tools, tool-choice, allowed-tools, and tool-name-collision
// projections all project neither an expectation nor an observation.
func TestProjectTerminalDecisionEvidence_ExpectationRequiresSuccessfulActivation(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{outcome: expectCompleteOutcome()}

	t.Run("no_attempt", func(t *testing.T) {
		t.Parallel()
		pipeline := newResponsePipeline()
		pipeline.rememberClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "candidate output"})

		evidence := projectTerminalDecisionEvidence(expectRequest(expectUserItem()), nil, pipeline)
		assert.False(t, evidence.ExplicitCompletionExpected,
			"a response with no attempt cannot have had an active proxy-owned control protocol")
		assert.False(t, evidence.ExplicitCompletion, "no attempt and no native signal must observe nothing")
	})

	t.Run("activation_absent", func(t *testing.T) {
		t.Parallel()
		attempt := &attemptSession{}
		require.Nil(t, attempt.controlCapture, "an attempt with no activation owns no capture")

		evidence := projectTerminalDecisionEvidence(expectRequest(expectUserItem()), attempt, newResponsePipeline())
		assert.False(t, evidence.ExplicitCompletionExpected,
			"an attempt with no activation cannot expect an explicit completion")
		assert.False(t, evidence.ExplicitCompletion, "an attempt with no activation must observe nothing")
	})

	t.Run("activation_inactive", func(t *testing.T) {
		t.Parallel()
		rig := newInterceptRigWithActivation(t, &controlToolActivation{})
		require.Nil(t, rig.attempt.controlCapture, "an inactive activation owns no capture")

		evidence := projectTerminalDecisionEvidence(expectRequest(expectUserItem()), rig.attempt, rig.p)
		assert.False(t, evidence.ExplicitCompletionExpected,
			"an inactive activation must not expect an explicit completion")
		assert.False(t, evidence.ExplicitCompletion, "an inactive activation must not observe a completion")
	})

	for _, tc := range []struct {
		name       string
		call       aipapi.Call
		caps       aipapi.BackendCaps
		wantReason string
	}{
		{
			name:       "backend_tools_unsupported",
			call:       expectClientCall(),
			caps:       aipapi.NewBackendCaps(),
			wantReason: controltool.ReasonBackendToolsUnsupported,
		},
		{
			name: "tool_choice_none",
			call: func() aipapi.Call {
				call := expectClientCall()
				call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}
				return call
			}(),
			caps:       aipapi.NewBackendCaps(aipapi.CapabilityTools),
			wantReason: controltool.ReasonToolChoiceNone,
		},
		{
			name: "tool_choice_required_named_client_tool",
			call: func() aipapi.Call {
				call := expectClientCall()
				call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: interceptOrdinary}
				return call
			}(),
			caps:       aipapi.NewBackendCaps(aipapi.CapabilityTools),
			wantReason: controltool.ReasonToolChoiceRequired,
		},
		{
			name: "allowed_tools_constrained",
			call: func() aipapi.Call {
				call := expectClientCall()
				call.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto, AllowedTools: []string{interceptOrdinary}}
				return call
			}(),
			caps:       aipapi.NewBackendCaps(aipapi.CapabilityTools),
			wantReason: controltool.ReasonAllowedToolsConstrained,
		},
		{
			name: "tool_name_collision",
			call: func() aipapi.Call {
				call := expectClientCall()
				call.Tools = append(call.Tools, aipapi.ToolDef{
					Name:       expectToolName,
					Parameters: json.RawMessage(expectSchema),
				})
				return call
			}(),
			caps:       aipapi.NewBackendCaps(aipapi.CapabilityTools),
			wantReason: controltool.ReasonToolNameCollision,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			activation := expectIneligibleActivation(t, provider, tc.call, tc.caps, tc.wantReason)
			rig := newInterceptRigWithActivation(t, activation)
			require.Nil(t, rig.attempt.controlCapture,
				"an ineligible projection must never obtain a private capture")

			evidence := projectTerminalDecisionEvidence(expectRequest(expectUserItem()), rig.attempt, rig.p)
			assert.False(t, evidence.ExplicitCompletionExpected,
				"an ineligible protocol activation must not expect an explicit completion (reason %q)", tc.wantReason)
			assert.False(t, evidence.ExplicitCompletion,
				"an ineligible protocol activation must not observe a proxy completion (reason %q)", tc.wantReason)
		})
	}
}

// TestProjectTerminalDecisionEvidence_ActiveWithoutSignalExpectsButDoesNotObserve
// pins requirement 7.1's precondition in isolation: the protocol was active, so
// the expectation is reported, and no completion was observed, so the observation
// stays false. It proves the two flags are independent facts rather than one
// derived from the other.
func TestProjectTerminalDecisionEvidence_ActiveWithoutSignalExpectsButDoesNotObserve(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{outcome: expectCompleteOutcome()}
	rig := newInterceptRig(t, provider, expectActiveCompletionActivation(t, provider))

	// Ordinary traffic and an ordinary client tool only; the proxy-owned
	// completion tool is never called.
	driveExpectStream(t, rig,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "inspecting the repository"},
		interceptStart("ordinary-1", interceptOrdinary),
		interceptArgsDelta("ordinary-1", interceptOrdinaryArg),
		interceptFinish("ordinary-1"),
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)
	require.Zero(t, provider.handleCount(), "an unclaimed response must never invoke the control provider")

	evidence := projectTerminalDecisionEvidence(expectRequest(expectUserItem()), rig.attempt, rig.p)
	assert.True(t, evidence.ExplicitCompletionExpected,
		"an active proxy-owned control protocol must be reported as expected")
	assert.False(t, evidence.ExplicitCompletion,
		"an active protocol with no completion signal must not report an observation")
}

// TestProjectTerminalDecisionEvidence_ValidProxyCompletionIsObserved pins
// requirements 6.1, 6.2, and 9.3 for the proxy path: a real captured, validated
// OutcomeComplete becomes a trusted observed fact alongside the expectation.
//
// It also pins that the completion stays private. The result text and the control
// call identity never reach the client action facts or the candidate text, and
// the swallowed lifecycle never enters the released seen-event copy the
// projection reads (requirements 3.5, 5.4).
func TestProjectTerminalDecisionEvidence_ValidProxyCompletionIsObserved(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{outcome: expectCompleteOutcome()}
	rig := newInterceptRig(t, provider, expectActiveCompletionActivation(t, provider))

	released := driveExpectCompletion(t, rig, provider,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "inspecting the repository"},
		interceptStart("ordinary-1", interceptOrdinary),
		interceptArgsDelta("ordinary-1", interceptOrdinaryArg),
		interceptFinish("ordinary-1"),
		interceptStart("control-1", expectToolName),
		interceptArgsDelta("control-1", expectArgs),
		interceptFinish("control-1"),
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)

	require.NotNil(t, rig.attempt.controlOutcome, "the fixture must first produce a real validated completion")
	require.Equal(t, controltool.OutcomeComplete, rig.attempt.controlOutcome.Kind)

	evidence := projectTerminalDecisionEvidence(expectRequest(expectUserItem()), rig.attempt, rig.p)
	assert.True(t, evidence.ExplicitCompletionExpected,
		"an active proxy-owned control protocol must be reported as expected")
	assert.True(t, evidence.ExplicitCompletion,
		"a valid proxy control completion must be reported as an observed trusted signal")

	// The completion is evidence only: it publishes no synthetic client
	// ToolCall/ToolResult pair and no result text (requirement 6.2; task 5.2
	// owns any later publication).
	for _, name := range evidenceActionNames(evidence) {
		assert.NotEqual(t, expectToolName, name,
			"the private proxy control call must not appear in ordinary client action facts")
		assert.NotEqual(t, "control-1", name,
			"the private proxy control call ID must not appear in ordinary client action facts")
	}
	for i, action := range evidence.Actions[:evidence.ActionCount] {
		assert.NotEqual(t, "control-1", action.CallID,
			"the private proxy control call ID must not appear in ordinary client action facts; action[%d]=%+v", i, action)
	}
	assert.NotContains(t, evidence.CandidateText, expectResultText,
		"the private completion result must not reach candidate text")
	assert.NotContains(t, evidence.RecentText, expectResultText,
		"the private completion result must not reach canonical trajectory text")
	assert.NotContains(t, evidence.Objective, expectResultText,
		"the private completion result must not reach the objective")
	assert.NotContains(t, evidence.CandidateText, expectArgs,
		"the private control arguments must not reach candidate text")

	assert.Zero(t, countContaining(releasedToolNames(released), expectToolName),
		"the private control tool name must never reach the client; names=%v", releasedToolNames(released))
	for _, ev := range rig.p.seenEventsCopy() {
		assert.NotEqual(t, expectToolName, ev.ToolName,
			"a swallowed control lifecycle must not enter the released seen-event copy")
		if ev.Item != nil && ev.Item.ToolCall != nil {
			assert.NotEqual(t, expectToolName, ev.Item.ToolCall.Name,
				"a swallowed control call must not enter the released seen-event copy")
		}
	}
}

// TestProjectTerminalDecisionEvidence_OrdinaryToolsStayOrdinaryEvidence pins that
// an ordinary client tool remains ordinary action evidence next to a private
// proxy completion, including an unresolved ordinary call that stays visible to
// the provider.
//
// Task 5.1 deliberately implements no policy here: whether an unresolved ordinary
// action blocks terminal authority belongs to the preferred terminal policy, and
// this case proves only that the projection neither hides the unresolved action
// nor invents completion from it.
func TestProjectTerminalDecisionEvidence_OrdinaryToolsStayOrdinaryEvidence(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{outcome: expectCompleteOutcome()}
	rig := newInterceptRig(t, provider, expectActiveCompletionActivation(t, provider))

	released := driveExpectCompletion(t, rig, provider,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "inspecting the repository"},
		// A completed ordinary client tool in the same response as the private
		// control completion.
		interceptStart("ordinary-1", interceptOrdinary),
		interceptArgsDelta("ordinary-1", interceptOrdinaryArg),
		interceptFinish("ordinary-1"),
		interceptStart("control-1", expectToolName),
		interceptArgsDelta("control-1", expectArgs),
		interceptFinish("control-1"),
	)

	require.NotEmpty(t, released, "ordinary traffic must still be released to the client")
	assert.Positive(t, countContaining(interceptReleasedLabels(released), "ordinary-1"),
		"the ordinary client tool lifecycle must still reach the client; released=%v", interceptReleasedLabels(released))
	assert.Zero(t, countContaining(interceptReleasedLabels(released), "control-1"),
		"the private control call must still never reach the client; released=%v", interceptReleasedLabels(released))
	assert.Zero(t, countContaining(releasedToolNames(released), expectToolName),
		"the private control tool name must still never reach the client; names=%v", releasedToolNames(released))
	assert.Contains(t, releasedToolNames(released), interceptOrdinary,
		"the ordinary client tool name must still reach the client; names=%v", releasedToolNames(released))

	evidence := projectTerminalDecisionEvidence(expectRequestWithUnresolvedOrdinaryTool(), rig.attempt, rig.p)
	assert.True(t, evidence.ExplicitCompletion, "the private valid completion is still the observed fact")

	names := evidenceActionNames(evidence)
	assert.Contains(t, names, interceptOrdinary,
		"an ordinary client tool must remain ordinary action evidence")
	assert.NotContains(t, names, expectToolName,
		"the private control call must not appear among ordinary client action facts")

	unresolved := 0
	for _, action := range evidence.Actions[:evidence.ActionCount] {
		if action.CallID == "ordinary-open" && action.Status == aipapi.ItemStatusInProgress {
			unresolved++
		}
	}
	assert.Equal(t, 1, unresolved,
		"an unresolved ordinary client tool must remain visible evidence for later policy; actions=%+v",
		evidence.Actions[:evidence.ActionCount])
}

// TestProjectTerminalDecisionEvidence_NonCompletionOutcomesDoNotObserve walks the
// negative completion matrix of design Handler Semantics and Capture State.
//
// Only a validated OutcomeComplete observes. An invalid outcome, a provider
// error, an isolated handler panic, an unknown outcome kind, an unfinished call,
// a duplicate start, and a malformed sequence that revokes an earlier valid
// completion all leave the observation false while the expectation stays true, so
// a provider can never mistake a failure for a completion.
func TestProjectTerminalDecisionEvidence_NonCompletionOutcomesDoNotObserve(t *testing.T) {
	t.Parallel()

	controlLifecycle := func(name string, args string, tail ...aipapi.Event) []aipapi.Event {
		stream := []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			interceptStart("control-1", name),
		}
		if args != "" {
			stream = append(stream, interceptArgsDelta("control-1", args))
		}
		return append(stream, tail...)
	}

	for _, tc := range []struct {
		name    string
		arm     func(p *interceptProvider)
		stream  []aipapi.Event
		wantErr bool
	}{
		{
			name: "invalid_outcome",
			arm: func(p *interceptProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeInvalid, ReasonCode: "model_mistake"}
			},
			stream: controlLifecycle(expectToolName, expectArgs, interceptFinish("control-1")),
		},
		{
			name:    "provider_error",
			arm:     func(p *interceptProvider) { p.err = errWithSecret(interceptReasonSecret) },
			stream:  controlLifecycle(expectToolName, expectArgs, interceptFinish("control-1")),
			wantErr: true,
		},
		{
			name:    "handler_panic",
			arm:     func(p *interceptProvider) { p.panicV = "panic payload " + interceptResultSecret },
			stream:  controlLifecycle(expectToolName, expectArgs, interceptFinish("control-1")),
			wantErr: true,
		},
		{
			name: "unknown_outcome_kind",
			arm: func(p *interceptProvider) {
				p.outcome = controltool.Outcome{Kind: controltool.OutcomeKind(11), ReasonCode: "weird"}
			},
			stream:  controlLifecycle(expectToolName, expectArgs, interceptFinish("control-1")),
			wantErr: true,
		},
		{
			name:   "incomplete_call",
			arm:    func(p *interceptProvider) { p.outcome = expectCompleteOutcome() },
			stream: controlLifecycle(expectToolName, `{"result":`, aipapi.Event{Kind: aipapi.EventResponseFinished}),
		},
		{
			name: "duplicate_start",
			arm:  func(p *interceptProvider) { p.outcome = expectCompleteOutcome() },
			stream: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				interceptStart("control-1", expectToolName),
				interceptStart("control-2", expectToolName),
				interceptArgsDelta("control-1", expectArgs),
				interceptFinish("control-1"),
			},
		},
		{
			name: "malformed_sequence_revokes_valid_completion",
			arm:  func(p *interceptProvider) { p.outcome = expectCompleteOutcome() },
			stream: controlLifecycle(expectToolName, expectArgs,
				interceptFinish("control-1"),
				interceptFinish("control-1"),
			),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			provider := &interceptProvider{}
			tc.arm(provider)
			rig := newInterceptRig(t, provider, expectActiveCompletionActivation(t, provider))

			var gotErr error
			for _, ev := range tc.stream {
				if _, err := rig.drive(context.Background(), ev); err != nil {
					gotErr = err
					break
				}
			}
			if tc.wantErr {
				require.Error(t, gotErr, "this case must fail the response with the bounded control-handle sentinel")
				assert.ErrorIs(t, gotErr, extensions.ErrControlHandleFailed,
					"the response must fail with the static, content-free sentinel; err=%v", gotErr)
			} else {
				require.NoError(t, gotErr, "a swallowed malformed control lifecycle must not fail the response")
			}

			evidence := projectTerminalDecisionEvidence(expectRequest(expectUserItem()), rig.attempt, rig.p)
			assert.True(t, evidence.ExplicitCompletionExpected,
				"an active proxy-owned control protocol is still expected whatever the completion outcome was")
			assert.False(t, evidence.ExplicitCompletion,
				"%s must not invent an observed completion", tc.name)
		})
	}
}

// TestProjectTerminalDecisionEvidence_LifecycleOwnershipDropsObservation pins that
// the observation belongs to the attempt that owns the protocol state.
//
// A valid completion is observed while the attempt owns it, and disappears once
// that attempt's private state is disposed by cancellation, Close, attempt loss,
// or replacement, so a losing attempt cannot contribute completion evidence into
// the winning logical response. The expectation survives disposal, because the
// immutable activation is request/attempt provenance that disposal deliberately
// retains.
func TestProjectTerminalDecisionEvidence_LifecycleOwnershipDropsObservation(t *testing.T) {
	t.Parallel()

	loserProvider := &interceptProvider{outcome: expectCompleteOutcome()}
	winnerProvider := &interceptProvider{outcome: expectCompleteOutcome()}
	loser := newInterceptRig(t, loserProvider, expectActiveCompletionActivation(t, loserProvider))
	winner := newInterceptRig(t, winnerProvider, expectActiveCompletionActivation(t, winnerProvider))

	driveExpectCompletion(t, loser, loserProvider,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		interceptStart("control-1", expectToolName),
		interceptArgsDelta("control-1", expectArgs),
		interceptFinish("control-1"),
	)
	driveExpectCompletion(t, winner, winnerProvider,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		interceptStart("control-2", expectToolName),
		interceptArgsDelta("control-2", expectArgs),
		interceptFinish("control-2"),
	)

	request := expectRequest(expectUserItem())
	beforeDiscard := projectTerminalDecisionEvidence(request, loser.attempt, loser.p)
	require.True(t, beforeDiscard.ExplicitCompletion,
		"the owning attempt must observe its own valid completion before cleanup")

	loser.attempt.discardControlState()
	winner.attempt.discardControlState()

	for _, rig := range []*interceptRig{loser, winner} {
		evidence := projectTerminalDecisionEvidence(request, rig.attempt, rig.p)
		assert.False(t, evidence.ExplicitCompletion,
			"a disposed attempt must not contribute an observed completion into any logical response")
		assert.True(t, evidence.ExplicitCompletionExpected,
			"disposal must not erase the immutable activation that proves an active protocol")
	}
}

// TestProjectTerminalDecisionEvidence_NativeCompletionObservesWithoutProxy pins
// requirements 3.4 and 9.3 and the native half of design Completion Evidence: the
// existing correlated harness-owned completion fact keeps working unchanged, with
// no proxy expectation at all, and the OR with a proxy expectation is preserved
// when both facts exist.
func TestProjectTerminalDecisionEvidence_NativeCompletionObservesWithoutProxy(t *testing.T) {
	t.Parallel()

	t.Run("native_only", func(t *testing.T) {
		t.Parallel()
		rig := inertInterceptRig(t)
		require.Nil(t, rig.attempt.controlCapture, "the no-provider case owns no capture")

		evidence := projectTerminalDecisionEvidence(expectRequest(expectNativeCompletionItems()...), rig.attempt, rig.p)
		assert.True(t, evidence.ExplicitCompletion,
			"a correlated native completion call and result must remain an observed trusted signal")
		assert.False(t, evidence.ExplicitCompletionExpected,
			"a native completion must not invent a proxy expectation")
	})

	t.Run("native_with_active_protocol_preserves_or", func(t *testing.T) {
		t.Parallel()
		provider := &interceptProvider{outcome: expectCompleteOutcome()}
		rig := newInterceptRig(t, provider, expectActiveCompletionActivation(t, provider))

		evidence := projectTerminalDecisionEvidence(expectRequest(expectNativeCompletionItems()...), rig.attempt, rig.p)
		assert.True(t, evidence.ExplicitCompletion,
			"the native observation must survive an active proxy-owned protocol")
		assert.True(t, evidence.ExplicitCompletionExpected,
			"an active proxy-owned protocol must report the expectation alongside a native observation")
	})
}

// TestProjectTerminalDecisionEvidence_ClientOwnedCompletionStaysClientOwned pins
// requirements 3.3 and 4.4 together.
//
// The client independently declares the completion tool name, so the proxy-owned
// protocol activation fails conservatively: no expectation is projected and no
// private capture is opened. The client's own completed execution still observes
// through the ordinary normalized evidence path, with its ownership unchanged and
// no interception.
func TestProjectTerminalDecisionEvidence_ClientOwnedCompletionStaysClientOwned(t *testing.T) {
	t.Parallel()

	provider := &interceptProvider{outcome: expectCompleteOutcome()}
	colliding := expectClientCall()
	colliding.Tools = append(colliding.Tools, aipapi.ToolDef{Name: expectToolName, Parameters: json.RawMessage(expectSchema)})
	activation := expectIneligibleActivation(t, provider, colliding,
		aipapi.NewBackendCaps(aipapi.CapabilityTools), controltool.ReasonToolNameCollision)
	rig := newInterceptRigWithActivation(t, activation)
	require.Nil(t, rig.attempt.controlCapture, "a name collision must never open a private capture")

	evidence := projectTerminalDecisionEvidence(expectRequest(expectNativeCompletionItems()...), rig.attempt, rig.p)
	assert.False(t, evidence.ExplicitCompletionExpected,
		"a tool-name collision must disable the proxy-owned protocol, so no completion is expected")
	assert.True(t, evidence.ExplicitCompletion,
		"the client-owned completed execution must still be usable as existing normalized explicit-completion evidence")
	assert.Zero(t, provider.handleCount(),
		"a client-owned completion tool must never be intercepted or re-labeled as proxy-owned")

	// Client ownership is unchanged: the ordinary action facts still carry the
	// client's own call and result, not a synthetic proxy pair.
	assert.Contains(t, evidenceActionNames(evidence), expectToolName,
		"the client-owned completion tool must remain ordinary client action evidence")
}

// TestTerminalDecisionInput_ProjectsCompletionExpectationFlags pins that the
// public provider input carries both booleans and still satisfies the generic SDK
// contract, so a provider observes the expectation and the observation with no
// platform rule relating them.
func TestTerminalDecisionInput_ProjectsCompletionExpectationFlags(t *testing.T) {
	t.Parallel()

	executor := &Executor{ExtensionRuntime: ExtensionRuntime{
		RuntimeSnapshot: extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{}),
	}}
	owner := newTurnTerminal()
	bindTurnTerminalRuntime(owner, executor)

	provider := &interceptProvider{outcome: expectCompleteOutcome()}
	rig := newInterceptRig(t, provider, expectActiveCompletionActivation(t, provider))
	driveExpectCompletion(t, rig, provider,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "inspecting the repository"},
		interceptStart("control-1", expectToolName),
		interceptArgsDelta("control-1", expectArgs),
		interceptFinish("control-1"),
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	)

	request := expectRequest(expectUserItem())
	input := owner.terminalDecisionInput(terminal.CommandNormalFinish, request, rig.attempt, rig.p,
		coreterm.NewAccumulatorSnapshot(nil, false))
	assert.True(t, input.Evidence.ExplicitCompletionExpected,
		"the public provider input must carry the active-protocol expectation")
	assert.True(t, input.Evidence.ExplicitCompletion,
		"the public provider input must carry the observed completion")
	require.NoError(t, input.Validate(),
		"the projected input must satisfy the generic SDK contract with both flags set")

	inertAttempt := &attemptSession{}
	require.Nil(t, inertAttempt.controlCapture, "an attempt with no activation owns no capture")
	inertInput := owner.terminalDecisionInput(terminal.CommandNormalFinish, request, inertAttempt, rig.p,
		coreterm.NewAccumulatorSnapshot(nil, false))
	assert.False(t, inertInput.Evidence.ExplicitCompletionExpected,
		"a response with no active protocol must project the zero expectation")
	require.NoError(t, inertInput.Validate(),
		"the no-provider projection must satisfy the generic SDK contract")
}
