// Bounded, attempt-local control-call capture state machine for task 4.1 of
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, requirements 3.2, 3.5,
// 5.1-5.6, 8.5, 10.3; design Response Interception / Capture State).
//
// Generic runtime only. Nothing here names a concrete feature or a concrete
// control tool: the fixture is the same anonymous proxy-owned control tool any
// feature generation would contribute, and the capture is driven straight from
// the trusted activation frozen by the request path. No response pipeline, tool
// assembler, policy, reactor, frontend, terminal, or provider handler is
// involved, because task 4.2 owns that wiring.
package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
)

// The generic control contract every case below projects. Name, schema,
// instruction text, and args budget are provider-owned plain bytes; the capture
// must read them from the frozen projection and never rewrite them.
const (
	captureProviderID    = "capture-control-test"
	captureToolName      = "proxy_control_capture"
	captureToolDesc      = "Call this only when the assigned proxy-local control action is complete."
	captureInstruction   = "Call proxy_control_capture only when the assigned work is complete."
	captureSchema        = `{"type":"object","properties":{"note":{"type":"string"}},"required":["note"],"additionalProperties":false}`
	captureOrdinaryTool  = "get_weather"
	captureOrdinaryInput = `{"city":"krakow"}`
)

// captureTripwireProvider is the generation-admitted provider behind the frozen
// activation. Every live method panics: a capture that re-reads the spec, re-reads
// the live identity, or reaches the handler instead of using the frozen
// projection fails its own test loudly.
type captureTripwireProvider struct{}

func (captureTripwireProvider) ID() string { panic("capture must use the frozen provider identity") }

func (captureTripwireProvider) Spec() controltool.Spec {
	panic("capture must use the frozen projection, never a live spec read")
}

func (captureTripwireProvider) Handle(context.Context, controltool.CompletedCall, controltool.Meta) (controltool.Outcome, error) {
	panic("capture must not invoke the control handler")
}

func captureSpec(maxArgs int) controltool.Spec {
	return controltool.Spec{
		Tool: aipapi.ToolDef{
			Name:        captureToolName,
			Description: captureToolDesc,
			Parameters:  json.RawMessage(captureSchema),
		},
		Instruction:  controltool.Instruction{Role: aipapi.RoleSystem, Text: captureInstruction},
		MaxArgsBytes: maxArgs,
	}
}

// captureActivation freezes a real, eligible projection the way the request path
// does, so these tests bind to the task 3.2 activation owner and cannot be
// satisfied by re-deriving eligibility or re-reading a spec.
func captureActivation(t *testing.T, maxArgs int) *controlToolActivation {
	t.Helper()
	spec := captureSpec(maxArgs)
	require.NoError(t, controltool.ValidateSpec(spec), "the capture fixture must be a valid control spec")
	call := aipapi.Call{
		ID: "capture-attempt",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("do the work")},
		}},
		Tools: []aipapi.ToolDef{{
			Name:       captureOrdinaryTool,
			Parameters: json.RawMessage(`{"type":"object"}`),
		}},
	}
	_, projection, err := controltool.Project(call, spec, aipapi.NewBackendCaps(aipapi.CapabilityTools))
	require.NoError(t, err)
	require.True(t, projection.Active(), "the capture fixture must be eligible for the control spec")
	require.Equal(t, maxArgs, projection.MaxArgsBytes(), "the frozen args budget is the only budget the capture may use")
	return &controlToolActivation{
		providerID: captureProviderID,
		provider:   captureTripwireProvider{},
		projection: projection,
	}
}

func captureStart(id, name string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: name}
}

func captureArgs(id, delta string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, Delta: delta}
}

func captureFinish(id string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: id}
}

// captureOrdinaryStream is one complete ordinary response: text, reasoning, an
// ordinary client tool lifecycle, usage, an unrelated item, and the terminal
// event. It also carries a control-named start, which an inactive capture must
// still leave alone because there is no trusted activation to claim it.
func captureOrdinaryStream() []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventReasoningDelta, Delta: "thinking"},
		captureStart("ordinary-1", captureOrdinaryTool),
		captureArgs("ordinary-1", captureOrdinaryInput),
		captureFinish("ordinary-1"),
		{Kind: aipapi.EventTextDelta, Delta: " world"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 3, OutputTokens: 4},
		{Kind: aipapi.EventItem, Item: &aipapi.Item{
			Kind:    aipapi.ItemKindMessage,
			ID:      "item-1",
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "note"}},
		}},
		captureStart("control-shaped-1", captureToolName),
		captureArgs("control-shaped-1", `{"note":"x"}`),
		captureFinish("control-shaped-1"),
		{Kind: aipapi.EventResponseFinished},
	}
}

func assertUntouched(t *testing.T, obs controlCallObservation) {
	t.Helper()
	assert.False(t, obs.Claimed(), "the event must not be claimed")
	assert.False(t, obs.Completed(), "no control call may complete")
	assert.False(t, obs.Invalid(), "an ordinary event must not invalidate the protocol")
	assert.NoError(t, obs.Fatal(), "an ordinary event must not raise a protocol error")
	assert.Equal(t, controltool.CompletedCall{}, obs.Call(), "no completed call may be produced")
}

// TestControlCallCapture_inactiveActivationDoesNoWork pins requirement 5.1 and the
// design rule that an absent or inactive activation costs nothing: the capture is
// nil, so an ordinary response and even a control-named tool name pass through
// untouched with no hashing, no buffer, and no correlation state.
func TestControlCallCapture_inactiveActivationDoesNoWork(t *testing.T) {
	t.Parallel()

	require.Nil(t, newControlCallCapture(nil), "an absent activation must build no capture")

	inactive := &controlToolActivation{}
	require.False(t, inactive.active())
	require.Nil(t, newControlCallCapture(inactive), "an inactive projection must build no capture")

	spec := captureSpec(controltool.DefaultMaxArgsBytes)
	_, inactiveProjection, err := controltool.Project(aipapi.Call{ID: "no-tools"}, spec, aipapi.NewBackendCaps(aipapi.CapabilityStreaming))
	require.NoError(t, err)
	require.False(t, inactiveProjection.Active(), "a backend without tools must be ineligible")

	captures := []*controlCallCapture{
		nil,
		newControlCallCapture(inactive),
		newControlCallCapture(&controlToolActivation{providerID: captureProviderID, projection: inactiveProjection}),
	}
	for _, c := range captures {
		require.Nil(t, c)
		for _, ev := range captureOrdinaryStream() {
			assertUntouched(t, c.observe(ev))
		}
		assertUntouched(t, c.closeResponse())
		assert.False(t, c.invalid())
		assert.Empty(t, c.reasonCode())
	}
}

// TestControlCallCapture_ordinaryEventsAreUnchangedAndUndelayed pins requirements
// 5.1 and 5.3: while no proxy-owned call is active, ordinary text, reasoning,
// usage, items, and the complete ordinary tool lifecycle reach the caller
// unchanged, and the capture retains nothing about them.
func TestControlCallCapture_ordinaryEventsAreUnchangedAndUndelayed(t *testing.T) {
	t.Parallel()

	c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
	require.NotNil(t, c)

	events := captureOrdinaryStream()
	for i, ev := range events {
		obs := c.observe(ev)
		if ev.ToolCallID == "control-shaped-1" {
			// The only control-shaped lifecycle in the fixture. It is claimed and
			// completed because the activation is trusted, so it is excluded from
			// the pass-through expectation below.
			assert.True(t, obs.Claimed(), "event[%d] control-shaped fragment must be claimed", i)
			continue
		}
		assertUntouched(t, obs)
	}

	assert.False(t, c.invalid(), "an ordinary response must not invalidate the control protocol")
	assert.Nil(t, c.args, "an ordinary tool lifecycle must not fill the control args buffer")
	assert.Empty(t, c.callID, "an ordinary tool ID must never become a control call ID")
}

// TestControlCallCapture_validFragmentedCallCompletesOnce pins the happy path and
// requirements 3.2 and 5.2: a started event whose name matches the prepared tool
// establishes control ownership, later name-less args and finish fragments
// correlate only by the exact claimed call ID, and the completed invocation owns
// its bytes after the raw buffer is released.
func TestControlCallCapture_validFragmentedCallCompletesOnce(t *testing.T) {
	t.Parallel()

	const maxArgs = 512
	c := newControlCallCapture(captureActivation(t, maxArgs))
	require.NotNil(t, c)

	start := c.observe(captureStart("call-1", captureToolName))
	assert.True(t, start.Claimed(), "a prepared-name start must be claimed")
	assert.False(t, start.Completed())
	assert.False(t, start.Invalid())
	assert.Equal(t, "call-1", c.callID, "the exact opaque call ID must be retained verbatim")

	for _, fragment := range []string{`{"note":`, `"done"}`} {
		obs := c.observe(captureArgs("call-1", fragment))
		assert.True(t, obs.Claimed(), "a name-less fragment of the claimed ID must be claimed")
		assert.False(t, obs.Completed(), "partial arguments must never complete the call")
		assert.False(t, obs.Invalid())
	}

	done := c.observe(captureFinish("call-1"))
	assert.True(t, done.Claimed(), "a name-less finish of the claimed ID must be claimed")
	require.True(t, done.Completed(), "start plus bounded complete args plus finish is one complete invocation")
	assert.False(t, done.Invalid())
	assert.Equal(t, controltool.CompletedCall{
		ToolCallID: "call-1",
		ToolName:   captureToolName,
		ArgsJSON:   []byte(`{"note":"done"}`),
	}, done.Call())
	require.NoError(t, controltool.ValidateCompletedCall(done.Call(), maxArgs),
		"the capture must hand off a call that satisfies the SDK contract")

	assert.Nil(t, c.args, "the raw args buffer must be released at handoff")
	assert.Empty(t, c.callID, "the exact raw ID must be released at handoff")

	// A completion is handed off at most once.
	after := c.observe(captureArgs("call-1", "tail"))
	assert.True(t, after.Claimed(), "a post-completion fragment is still a claimed control fragment")
	assert.False(t, after.Completed(), "the completed invocation must never be handed off twice")
	assert.True(t, after.Invalid(), "a fragment after finish must invalidate the protocol")
}

// TestControlCallCapture_unrelatedInterleavingPreservesOrdinaryStreaming pins
// requirement 5.3 with the control lifecycle split by ordinary traffic: an
// ordinary tool call that starts, streams, and finishes around the control call
// must be untouched while the control fragments are still claimed.
func TestControlCallCapture_unrelatedInterleavingPreservesOrdinaryStreaming(t *testing.T) {
	t.Parallel()

	c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
	require.NotNil(t, c)

	stream := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "let me check"},
		captureStart("ordinary-1", captureOrdinaryTool),
		captureStart("call-1", captureToolName),
		captureArgs("ordinary-1", captureOrdinaryInput),
		{Kind: aipapi.EventTextDelta, Delta: " something"},
		captureArgs("call-1", `{"note":`),
		{Kind: aipapi.EventUsageDelta, InputTokens: 1, OutputTokens: 1},
		captureArgs("call-1", `"ok"}`),
		captureFinish("ordinary-1"),
		captureFinish("call-1"),
		{Kind: aipapi.EventTextDelta, Delta: " done"},
	}
	wantCompleted := 0
	for i, ev := range stream {
		obs := c.observe(ev)
		if ev.ToolCallID == "call-1" {
			assert.True(t, obs.Claimed(), "event[%d] control fragment must be claimed", i)
			if obs.Completed() {
				wantCompleted++
			}
			continue
		}
		assertUntouched(t, obs)
	}
	assert.Equal(t, 1, wantCompleted, "exactly one control invocation may complete")
	assert.False(t, c.invalid(), "interleaved ordinary traffic must not invalidate the protocol")
}

// TestControlCallCapture_argsBudgetBoundary pins requirement 5.2: exactly the
// frozen budget is accepted and one byte more is a sticky overflow that never
// becomes a client tool execution.
func TestControlCallCapture_argsBudgetBoundary(t *testing.T) {
	t.Parallel()

	payload := `{"note":"abcdefghij"}`
	maxArgs := len(payload)

	exact := newControlCallCapture(captureActivation(t, maxArgs))
	require.NotNil(t, exact)
	exact.observe(captureStart("call-1", captureToolName))
	first := exact.observe(captureArgs("call-1", payload[:maxArgs-1]))
	assert.True(t, first.Claimed())
	assert.False(t, first.Invalid(), "arguments up to the budget stay valid")
	second := exact.observe(captureArgs("call-1", payload[maxArgs-1:]))
	assert.True(t, second.Claimed())
	assert.False(t, second.Invalid(), "exactly the frozen budget must be accepted")
	done := exact.observe(captureFinish("call-1"))
	require.True(t, done.Completed(), "a payload of exactly the budget must complete")
	assert.Equal(t, payload, string(done.Call().ArgsJSON))

	over := newControlCallCapture(captureActivation(t, maxArgs))
	require.NotNil(t, over)
	over.observe(captureStart("call-1", captureToolName))
	over.observe(captureArgs("call-1", payload[:maxArgs-1]))
	overflow := over.observe(captureArgs("call-1", payload[maxArgs-1:]+"x"))
	assert.True(t, overflow.Claimed(), "an overflowing fragment stays inside the protocol")
	assert.True(t, overflow.Invalid())
	assert.Equal(t, controlReasonArgsOverflow, overflow.Reason())
	assert.False(t, overflow.Completed(), "overflow must never produce a completed invocation")
	assert.Nil(t, over.args, "overflow must release the buffer instead of retaining it")
	late := over.observe(captureFinish("call-1"))
	assert.True(t, late.Claimed())
	assert.False(t, late.Completed(), "a finish after overflow must never complete")
	assert.Equal(t, controlReasonArgsOverflow, late.Reason(), "the invalid reason must be sticky")
}

// TestControlCallCapture_hugeArgsChunkIsRejectedWithoutBuffering pins the
// bounded-buffer rule: the budget is checked before any copy or append, so a
// single oversized chunk neither completes the call nor retains a buffer, and a
// valid stream never allocates beyond the frozen budget.
func TestControlCallCapture_hugeArgsChunkIsRejectedWithoutBuffering(t *testing.T) {
	t.Parallel()

	const maxArgs = 4 * 1024
	huge := strings.Repeat("x", 4<<20)

	c := newControlCallCapture(captureActivation(t, maxArgs))
	require.NotNil(t, c)
	c.observe(captureStart("call-1", captureToolName))
	obs := c.observe(captureArgs("call-1", huge))
	assert.True(t, obs.Claimed())
	assert.True(t, obs.Invalid())
	assert.Equal(t, controlReasonArgsOverflow, obs.Reason())
	assert.Nil(t, c.args, "an oversized chunk must be refused before any byte is buffered")
	assert.Zero(t, cap(c.args), "an oversized chunk must never grow the buffer")
	assert.False(t, c.observe(captureFinish("call-1")).Completed())

	// The valid path never exceeds the frozen budget either.
	ok := newControlCallCapture(captureActivation(t, maxArgs))
	require.NotNil(t, ok)
	ok.observe(captureStart("call-1", captureToolName))
	head, tail := `{"note":"`, `"}`
	payload := head + strings.Repeat("y", maxArgs-len(head)-len(tail)) + tail
	require.Len(t, payload, maxArgs, "the valid fixture must fill the frozen budget exactly")
	for sent := 0; sent < len(payload); {
		end := min(sent+997, len(payload))
		obs := ok.observe(captureArgs("call-1", payload[sent:end]))
		require.False(t, obs.Invalid(), "a valid stream must stay within the budget")
		assert.LessOrEqual(t, cap(ok.args), maxArgs, "the buffer must never exceed the frozen budget")
		sent = end
	}
	done := ok.observe(captureFinish("call-1"))
	require.True(t, done.Completed())
	assert.Equal(t, maxArgs, len(done.Call().ArgsJSON), "the completed args must be exactly the frozen budget")
	assert.LessOrEqual(t, cap(ok.args), maxArgs, "the handed-off buffer must not exceed the frozen budget")

	// A byte-at-a-time stream is the shape where a plain append rounds its
	// capacity well past the last needed byte.
	tiny := newControlCallCapture(captureActivation(t, maxArgs))
	require.NotNil(t, tiny)
	tiny.observe(captureStart("call-1", captureToolName))
	for i := range maxArgs {
		delta := "y"
		if i == 0 {
			delta = "{"
		}
		if i == maxArgs-1 {
			delta = "}"
		}
		require.False(t, tiny.observe(captureArgs("call-1", delta)).Invalid(),
			"a byte-at-a-time stream must stay within the budget")
		assert.LessOrEqual(t, cap(tiny.args), maxArgs, "growth must never round past the frozen budget")
	}

	// A growth step whose next power-of-two lands past the budget is the exact
	// shape a plain append would overshoot: 600 then 400 against a 1000-byte
	// budget rounds a naive buffer to 1200.
	const tightBudget, firstChunk, secondChunk = 1000, 600, 400
	tight := newControlCallCapture(captureActivation(t, tightBudget))
	require.NotNil(t, tight)
	tight.observe(captureStart("call-1", captureToolName))
	require.False(t, tight.observe(captureArgs("call-1", strings.Repeat("z", firstChunk))).Invalid())
	assert.LessOrEqual(t, cap(tight.args), tightBudget, "the first chunk must not overshoot the budget")
	require.False(t, tight.observe(captureArgs("call-1", strings.Repeat("z", secondChunk))).Invalid(),
		"a second chunk that exactly fills the budget stays valid")
	assert.Equal(t, tightBudget, cap(tight.args), "the buffer must be clamped to the frozen budget, never rounded past it")
}

// TestControlCallCapture_malformedLifecyclesAreStickyInvalid pins requirement 5.5
// over the whole malformed matrix: duplicate start, duplicate finish, arguments
// after finish, and a claimed ID reused under a different name. Every one is
// swallowed, sticky-invalid, and never a completed invocation.
func TestControlCallCapture_malformedLifecyclesAreStickyInvalid(t *testing.T) {
	t.Parallel()

	t.Run("named_fragment_before_start_is_swallowed", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, c)

		obs := c.observe(aipapi.Event{
			Kind:       aipapi.EventToolCallArgsDelta,
			ToolCallID: "call-9",
			ToolName:   captureToolName,
			Delta:      `{"note":"x"}`,
		})
		assert.True(t, obs.Claimed(), "a directly named control fragment must never reach ordinary tool processing")
		assert.True(t, obs.Invalid())
		assert.Equal(t, controlReasonBeforeStart, obs.Reason())
		assert.False(t, c.observe(captureFinish("call-9")).Completed(),
			"a named control fragment with no start must never complete")
		assert.True(t, c.observe(captureArgs("call-9", "tail")).Claimed(),
			"its later name-less fragments stay swallowed")
	})

	t.Run("duplicate_start", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		c.observe(captureStart("call-1", captureToolName))
		obs := c.observe(captureStart("call-1", captureToolName))
		assert.True(t, obs.Claimed())
		assert.True(t, obs.Invalid())
		assert.Equal(t, controlReasonDuplicateStart, obs.Reason())
		assert.False(t, c.observe(captureArgs("call-1", `{"note":"a"}`)).Completed())
	})

	t.Run("second_distinct_control_call", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		c.observe(captureStart("call-1", captureToolName))
		obs := c.observe(captureStart("call-2", captureToolName))
		assert.True(t, obs.Claimed())
		assert.True(t, obs.Invalid())
		assert.Equal(t, controlReasonMultipleCalls, obs.Reason())
		assert.False(t, c.observe(captureFinish("call-2")).Completed())
	})

	t.Run("duplicate_finish", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		c.observe(captureStart("call-1", captureToolName))
		c.observe(captureArgs("call-1", `{"note":"a"}`))
		require.True(t, c.observe(captureFinish("call-1")).Completed())
		obs := c.observe(captureFinish("call-1"))
		assert.True(t, obs.Claimed())
		assert.True(t, obs.Invalid(), "a duplicate finish must invalidate the earlier validity")
		assert.Equal(t, controlReasonDuplicateFinish, obs.Reason())
		assert.False(t, obs.Completed(), "a duplicate finish must never hand off a second invocation")
	})

	t.Run("args_after_finish", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		c.observe(captureStart("call-1", captureToolName))
		c.observe(captureArgs("call-1", `{"note":"a"}`))
		require.True(t, c.observe(captureFinish("call-1")).Completed())
		obs := c.observe(captureArgs("call-1", `{"note":"b"}`))
		assert.True(t, obs.Claimed())
		assert.True(t, obs.Invalid())
		assert.Equal(t, controlReasonArgsAfterFinish, obs.Reason())
	})

	t.Run("claimed_id_reused_under_another_name", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		c.observe(captureStart("call-1", captureToolName))
		obs := c.observe(captureStart("call-1", captureOrdinaryTool))
		assert.True(t, obs.Claimed(), "a claimed control ID may not be re-declared as an ordinary tool")
		assert.True(t, obs.Invalid())
		assert.Equal(t, controlReasonNameConflict, obs.Reason())
		assert.False(t, c.observe(captureArgs("call-1", `{"note":"a"}`)).Completed())
	})

	t.Run("malformed_args_never_complete", func(t *testing.T) {
		t.Parallel()
		for name, payload := range map[string]string{
			"empty":            "",
			"not_json":         `note`,
			"truncated_object": `{"note":`,
			"trailing_value":   `{"note":"a"} {}`,
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
				c.observe(captureStart("call-1", captureToolName))
				if payload != "" {
					assert.True(t, c.observe(captureArgs("call-1", payload)).Claimed())
				}
				obs := c.observe(captureFinish("call-1"))
				assert.True(t, obs.Claimed())
				assert.False(t, obs.Completed(), "malformed arguments must never become a completed invocation")
				assert.True(t, obs.Invalid())
				assert.Equal(t, controlReasonArgsMalformed, obs.Reason())
			})
		}
	})
}

// TestControlCallCapture_extraClaimedIDsStaySwallowed pins the correlation-privacy
// rule: after a second control call is rejected, its later name-less fragments
// are still swallowed, while a genuinely unrelated ordinary ID still passes
// through.
func TestControlCallCapture_extraClaimedIDsStaySwallowed(t *testing.T) {
	t.Parallel()

	c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
	require.NotNil(t, c)

	first := c.observe(captureStart("call-1", captureToolName))
	require.True(t, first.Claimed())
	require.True(t, c.observe(captureArgs("call-1", `{"note":"a"}`)).Claimed())
	require.True(t, c.observe(captureFinish("call-1")).Completed())

	second := c.observe(captureStart("call-2", captureToolName))
	assert.True(t, second.Claimed())
	assert.True(t, second.Invalid())

	for _, ev := range []aipapi.Event{
		captureArgs("call-2", "garbage"),
		captureFinish("call-2"),
		captureArgs("call-1", "garbage"),
	} {
		obs := c.observe(ev)
		assert.True(t, obs.Claimed(), "name-less fragments of any claimed control ID stay swallowed")
		assert.False(t, obs.Completed())
		assert.NoError(t, obs.Fatal())
	}

	unrelated := c.observe(captureStart("ordinary-9", captureOrdinaryTool))
	assert.False(t, unrelated.Claimed(), "an unrelated ordinary tool must still pass through")
	assertUntouched(t, c.observe(captureArgs("ordinary-9", captureOrdinaryInput)))
	assertUntouched(t, c.observe(captureFinish("ordinary-9")))
}

// TestControlCallCapture_unusableCallIDsStaySwallowed pins the oversized-ID
// correlation rule: a canonical call ID may be up to 8 KiB while the SDK accepts
// 256 bytes, so an unusable ID is still recorded as a correlation key and its
// later name-less fragments stay swallowed, while no oversized raw ID is retained
// and the invocation never completes.
func TestControlCallCapture_unusableCallIDsStaySwallowed(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":         "",
		"oversized":     strings.Repeat("i", controltool.MaxIdentifierBytes+1),
		"invalid_utf8":  "\xff\xfe-control",
		"exact_max_ok":  strings.Repeat("k", controltool.MaxIdentifierBytes),
		"canonical_8k":  strings.Repeat("j", aipapi.MaxRefStringBytes),
		"whitespace_id": " ",
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
			require.NotNil(t, c)

			start := c.observe(captureStart(id, captureToolName))
			assert.True(t, start.Claimed(), "a prepared-name start is always proxy-owned")
			if name == "exact_max_ok" {
				assert.False(t, start.Invalid(), "a maximum-length valid ID is usable")
				assert.Equal(t, id, c.callID, "a usable ID must be retained verbatim")
			} else {
				assert.True(t, start.Invalid(), "an unusable ID can never form a completed invocation")
				assert.Equal(t, controlReasonIDInvalid, start.Reason())
				assert.Empty(t, c.callID, "no unusable raw ID may be retained")
			}

			fragment := c.observe(captureArgs(id, `{"note":"a"}`))
			assert.True(t, fragment.Claimed(), "a name-less fragment of a claimed ID must be swallowed")
			finish := c.observe(captureFinish(id))
			assert.True(t, finish.Claimed())
			if name == "exact_max_ok" {
				assert.True(t, finish.Completed(), "a maximum-length valid ID completes normally")
				assert.Equal(t, id, finish.Call().ToolCallID)
			} else {
				assert.False(t, finish.Completed(), "an unusable ID must never produce a completed invocation")
				assert.True(t, finish.Invalid())
			}
			assert.Nil(t, c.args, "no raw args may be retained")
		})
	}
}

// TestControlCallCapture_itemBypassCarriesOneCompleteCall pins the EventItem
// bypass: a canonical item that already carries the prepared control tool call is
// one complete invocation, a tool result for a claimed control ID is private
// input, and every other item is unrelated.
func TestControlCallCapture_itemBypassCarriesOneCompleteCall(t *testing.T) {
	t.Parallel()

	t.Run("complete_item_call", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, c)

		item := aipapi.Item{
			Kind:     aipapi.ItemKindToolCall,
			ID:       "item-1",
			ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Name: captureToolName, Arguments: json.RawMessage(`{"note":"done"}`)},
		}
		obs := c.observe(aipapi.Event{Kind: aipapi.EventItem, Item: &item})
		assert.True(t, obs.Claimed(), "a complete prepared-name item call is proxy-owned")
		require.True(t, obs.Completed(), "a complete item call is one complete bounded invocation")
		assert.Equal(t, controltool.CompletedCall{
			ToolCallID: "call-1",
			ToolName:   captureToolName,
			ArgsJSON:   []byte(`{"note":"done"}`),
		}, obs.Call())
		require.NoError(t, controltool.ValidateCompletedCall(obs.Call(), controltool.DefaultMaxArgsBytes))

		// The completed call owns its bytes and never aliases the canonical item.
		item.ToolCall.Arguments[2] = 'X'
		assert.Equal(t, `{"note":"done"}`, string(obs.Call().ArgsJSON), "the completed call must own its arguments")

		again := c.observe(aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
			Kind:     aipapi.ItemKindToolCall,
			ToolCall: &aipapi.ToolCallItem{CallID: "call-2", Name: captureToolName, Arguments: json.RawMessage(`{"note":"again"}`)},
		}})
		assert.True(t, again.Claimed())
		assert.False(t, again.Completed(), "a second complete control item call must never hand off again")
		assert.True(t, again.Invalid())
	})

	t.Run("item_call_owns_exactly_bounded_arguments", func(t *testing.T) {
		t.Parallel()
		// A budget that is not a power of two is where an allocator is free to round
		// a copy up past the frozen bound, so the item path must hand off a buffer
		// sized to exactly what it validated rather than whatever a growth-based copy
		// happened to allocate.
		const budget = 1000
		require.NotZero(t, budget&(budget-1), "the fixture budget must not be a power of two")
		head, tail := `{"note":"`, `"}`
		payload := head + strings.Repeat("x", budget-len(head)-len(tail)) + tail
		require.Len(t, payload, budget, "the fixture must fill the frozen budget exactly")
		require.True(t, json.Valid([]byte(payload)), "the fixture must be valid JSON")

		c := newControlCallCapture(captureActivation(t, budget))
		require.NotNil(t, c)
		item := aipapi.Item{
			Kind:     aipapi.ItemKindToolCall,
			ID:       "item-boundary",
			ToolCall: &aipapi.ToolCallItem{CallID: "item-boundary", Name: captureToolName, Arguments: json.RawMessage(payload)},
		}
		obs := c.observe(aipapi.Event{Kind: aipapi.EventItem, Item: &item})
		require.True(t, obs.Completed(), "a payload of exactly the frozen budget is one complete invocation")
		assert.False(t, obs.Invalid())
		assert.NoError(t, obs.Fatal())
		require.NoError(t, controltool.ValidateCompletedCall(obs.Call(), budget),
			"the capture must hand off a call that satisfies the SDK contract")

		args := obs.Call().ArgsJSON
		assert.Equal(t, payload, string(args), "the completed call must own the exact bytes")
		assert.Len(t, args, budget, "the handed-off arguments must be exactly the frozen budget")
		assert.LessOrEqual(t, len(args), budget)
		assert.Equal(t, budget, cap(args),
			"the owned buffer must be sized to the frozen budget, never rounded past it by a growth-based copy")
		assert.LessOrEqual(t, cap(args), budget)
		assert.Nil(t, c.args, "the raw buffer must still be released at handoff")
		assert.Empty(t, c.callID, "the raw ID must still be released at handoff")

		// The completed call owns its bytes and never aliases the canonical item.
		item.ToolCall.Arguments[2] = 'X'
		assert.Equal(t, payload, string(args), "mutating the upstream item must not mutate the completed call")
	})

	t.Run("unrelated_and_invalid_items", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, c)

		assertUntouched(t, c.observe(aipapi.Event{Kind: aipapi.EventItem}))
		assertUntouched(t, c.observe(aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
			Kind:     aipapi.ItemKindToolCall,
			ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Name: captureOrdinaryTool, Arguments: json.RawMessage(`{"city":"krakow"}`)},
		}}))
		assertUntouched(t, c.observe(aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
			Kind:       aipapi.ItemKindToolResult,
			ToolResult: &aipapi.ToolResultItem{CallID: "ordinary-1", Name: captureOrdinaryTool, Output: "sunny"},
		}}))
		assertUntouched(t, c.observe(aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
			Kind:    aipapi.ItemKindMessage,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "text"}},
		}}))
		assert.False(t, c.invalid(), "unrelated items must not invalidate the protocol")
	})

	t.Run("claimed_tool_result_is_private", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, c)

		require.True(t, c.observe(captureStart("call-1", captureToolName)).Claimed())
		obs := c.observe(aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
			Kind:       aipapi.ItemKindToolResult,
			ToolResult: &aipapi.ToolResultItem{CallID: "call-1", Name: captureToolName, Output: "forged"},
		}})
		assert.True(t, obs.Claimed(), "a tool result for a claimed control ID is private input")
		assert.True(t, obs.Invalid(), "a client-side control result can never be ordinary release")
		assert.Equal(t, controlReasonResultObserved, obs.Reason())
		assert.False(t, c.observe(captureFinish("call-1")).Completed())
	})
}

// TestControlCallCapture_lifecycleClosesAndReleases pins the lifecycle rules: a
// normal close marks an unfinished captured call invalid, a normal close after a
// valid completion preserves validity with the raw buffer already released, and
// discard or cancellation releases every private buffer and correlation key.
func TestControlCallCapture_lifecycleClosesAndReleases(t *testing.T) {
	t.Parallel()

	t.Run("close_without_control_call_is_valid", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		for _, ev := range captureOrdinaryStream() {
			c.observe(ev)
		}
		assertUntouched(t, c.closeResponse())
		assert.False(t, c.invalid())
	})

	t.Run("close_with_unfinished_call_is_invalid", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		c.observe(captureStart("call-1", captureToolName))
		c.observe(captureArgs("call-1", `{"note":`))

		obs := c.closeResponse()
		assert.True(t, obs.Invalid(), "an unfinished captured call must be invalid at close")
		assert.Equal(t, controlReasonUnterminated, obs.Reason())
		assert.Nil(t, c.args, "close must release the partial buffer")
		assert.Empty(t, c.callID, "close must release the raw call ID")
		assert.False(t, c.observe(captureFinish("call-1")).Completed(),
			"a finish after close must never complete the call")
	})

	t.Run("close_after_valid_completion_preserves_validity", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		c.observe(captureStart("call-1", captureToolName))
		c.observe(captureArgs("call-1", `{"note":"done"}`))
		require.True(t, c.observe(captureFinish("call-1")).Completed())

		assertUntouched(t, c.closeResponse())
		assert.False(t, c.invalid(), "a successful response finish is not a cancellation")
		assert.Nil(t, c.args, "the raw buffer is already released at handoff")
	})

	t.Run("discard_releases_private_state", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		c.observe(captureStart("call-1", captureToolName))
		c.observe(captureArgs("call-1", `{"note":"a"}`))
		require.NotNil(t, c.args)

		c.discard()
		assert.Nil(t, c.args, "discard must release the args buffer")
		assert.Empty(t, c.callID, "discard must release the raw call ID")
		assert.False(t, c.correlates("call-1"), "discard must drop every correlation key")
		assert.False(t, c.invalid())
		assertUntouched(t, c.closeResponse())

		late := c.observe(captureArgs("call-1", "leftover"))
		assert.False(t, late.Claimed(), "a discarded call must not keep swallowing fragments")
		assert.False(t, c.observe(captureFinish("call-1")).Completed())
	})
}

// TestControlCallCapture_replacementOwnerIsIsolated pins requirement 10.3 and the
// replacement-owner rule: a replacement attempt starts from the same trusted
// activation with no inherited call ID or args buffer, so the same opaque ID can
// be claimed again and a stale fragment from the old owner cannot be adopted.
func TestControlCallCapture_replacementOwnerIsIsolated(t *testing.T) {
	t.Parallel()

	activation := captureActivation(t, controltool.DefaultMaxArgsBytes)

	old := newControlCallCapture(activation)
	require.NotNil(t, old)
	old.observe(captureStart("call-1", captureToolName))
	old.observe(captureArgs("call-1", `{"note":"stale"}`))
	old.discard()

	fresh := newControlCallCapture(activation)
	require.NotNil(t, fresh)
	assert.False(t, fresh.correlates("call-1"), "a replacement owner must inherit no correlation key")
	assert.Nil(t, fresh.args)
	assert.False(t, fresh.invalid())
	assert.False(t, fresh.completed)

	// A stale fragment from the discarded owner is ordinary again under the new
	// owner, and the new owner can still claim the same opaque ID.
	stale := fresh.observe(captureArgs("call-1", "stale"))
	assert.False(t, stale.Claimed(), "a replacement owner must not adopt the old owner's fragments")

	start := fresh.observe(captureStart("call-1", captureToolName))
	require.True(t, start.Claimed())
	require.True(t, fresh.observe(captureArgs("call-1", `{"note":"fresh"}`)).Claimed())
	done := fresh.observe(captureFinish("call-1"))
	require.True(t, done.Completed())
	assert.Equal(t, `{"note":"fresh"}`, string(done.Call().ArgsJSON))
}

// TestControlCallCapture_correlationCapacityIsBounded pins the bounded
// correlation window: a stream that keeps inventing control call IDs exhausts a
// fixed-capacity key set and yields one static, content-free protocol error
// instead of forgetting an ID, while ordinary IDs keep passing through.
func TestControlCallCapture_correlationCapacityIsBounded(t *testing.T) {
	t.Parallel()

	c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
	require.NotNil(t, c)

	for i := range controlClaimedIDCapacity {
		obs := c.observe(captureStart(captureOverflowID(i), captureToolName))
		require.True(t, obs.Claimed(), "claim[%d] must be claimed", i)
		require.NoError(t, obs.Fatal(), "claim[%d] must stay inside the correlation window", i)
	}
	assert.Equal(t, controlClaimedIDCapacity, c.claimedN)

	overflow := c.observe(captureStart(captureOverflowID(controlClaimedIDCapacity), captureToolName))
	assert.True(t, overflow.Claimed(), "the exhausting event stays inside the protocol")
	require.ErrorIs(t, overflow.Fatal(), errControlCallCorrelationExhausted)
	assert.NotContains(t, overflow.Fatal().Error(), captureOverflowID(controlClaimedIDCapacity),
		"the protocol error must carry no call ID")
	assert.NotContains(t, overflow.Fatal().Error(), captureToolName,
		"the protocol error must carry no tool name")

	sticky := c.observe(captureArgs(captureOverflowID(0), "late"))
	assert.True(t, sticky.Claimed())
	require.ErrorIs(t, sticky.Fatal(), errControlCallCorrelationExhausted,
		"the protocol error must stay sticky so the owner can abort safely")

	unrelated := c.observe(captureStart("ordinary-1", captureOrdinaryTool))
	assert.False(t, unrelated.Claimed(), "an unrelated ordinary tool must still pass through")
	assertUntouched(t, c.observe(captureArgs("ordinary-1", captureOrdinaryInput)))
}

func captureOverflowID(i int) string {
	return "capacity-probe-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
}

// TestControlCallCapture_validityIsRevokedByLaterMalformedInput pins the handoff
// contract task 4.2 depends on: an already completed invocation stays handed off
// at most once, but a later malformed sequence revokes the capture's validity so
// the terminal owner can still clear any pending outcome.
func TestControlCallCapture_validityIsRevokedByLaterMalformedInput(t *testing.T) {
	t.Parallel()

	for name, poison := range map[string]aipapi.Event{
		"duplicate_finish":  captureFinish("call-1"),
		"args_after_finish": captureArgs("call-1", "tail"),
		"second_control_id": captureStart("call-2", captureToolName),
		"claimed_tool_result": {Kind: aipapi.EventItem, Item: &aipapi.Item{
			Kind:       aipapi.ItemKindToolResult,
			ToolResult: &aipapi.ToolResultItem{CallID: "call-1", Name: captureToolName, Output: "forged"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
			require.NotNil(t, c)

			require.True(t, c.observe(captureStart("call-1", captureToolName)).Claimed())
			require.True(t, c.observe(captureArgs("call-1", `{"note":"done"}`)).Claimed())
			done := c.observe(captureFinish("call-1"))
			require.True(t, done.Completed(), "the one legitimate invocation must complete")
			require.False(t, c.invalid())

			poisoned := c.observe(poison)
			assert.True(t, poisoned.Claimed(), "a malformed control sequence stays inside the protocol")
			assert.False(t, poisoned.Completed(), "a malformed sequence must never hand off a second invocation")
			assert.True(t, poisoned.Invalid(), "a later malformed sequence must revoke the earlier validity")
			assert.NotEmpty(t, poisoned.Reason(), "the revoked reason must be bounded and content-free")
			assert.True(t, c.invalid())
			assert.Equal(t, c.reasonCode(), poisoned.Reason())
			assert.True(t, c.closeResponse().Invalid(), "the terminal owner must observe the revoked validity")
			assert.Nil(t, c.args, "the raw buffer stays released")
		})
	}
}

// captureNamedArgs and captureNamedFinish are provider shapes that repeat the
// tool name on fragments the canonical contract leaves name-less. They stay
// control-owned on the same terms as a name-less fragment.
func captureNamedArgs(id, delta string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, ToolName: captureToolName, Delta: delta}
}

func captureNamedFinish(id string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: id, ToolName: captureToolName}
}

func captureItemCall(id, name, args string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
		Kind:     aipapi.ItemKindToolCall,
		ID:       "item-" + id,
		ToolCall: &aipapi.ToolCallItem{CallID: id, Name: name, Arguments: json.RawMessage(args)},
	}}
}

// TestControlCallCapture_claimedItemCallIDIsNeverOrdinary pins the item identity
// rule: a ToolCallItem is judged by both of its identity fields. The prepared
// name makes it control-owned, and an already claimed call ID makes it ours even
// under an ordinary name, which is an identity conflict rather than ordinary
// client data that may be released.
func TestControlCallCapture_claimedItemCallIDIsNeverOrdinary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, c *controlCallCapture)
	}{
		{
			name: "before_valid_completion",
			prepare: func(t *testing.T, c *controlCallCapture) {
				t.Helper()
				require.True(t, c.observe(captureStart("claimed-1", captureToolName)).Claimed())
				require.False(t, c.invalid())
			},
		},
		{
			name: "after_valid_completion",
			prepare: func(t *testing.T, c *controlCallCapture) {
				t.Helper()
				require.True(t, c.observe(captureStart("claimed-1", captureToolName)).Claimed())
				require.True(t, c.observe(captureArgs("claimed-1", `{"note":"done"}`)).Claimed())
				require.True(t, c.observe(captureFinish("claimed-1")).Completed())
				require.False(t, c.invalid())
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
			require.NotNil(t, c)
			tc.prepare(t, c)

			conflict := c.observe(captureItemCall("claimed-1", captureOrdinaryTool, captureOrdinaryInput))
			assert.True(t, conflict.Claimed(), "a claimed control call ID may never be released as ordinary item data")
			assert.True(t, conflict.Invalid(), "reusing a claimed control call ID under another name is an identity conflict")
			assert.Equal(t, controlReasonNameConflict, conflict.Reason())
			assert.False(t, conflict.Completed(), "an identity conflict never hands off an invocation")
			assert.Nil(t, c.args, "a conflicting item must not buffer arguments")

			// The rejected item's own later name-less fragments stay private.
			for _, ev := range []aipapi.Event{captureArgs("claimed-1", "tail"), captureFinish("claimed-1")} {
				late := c.observe(ev)
				assert.True(t, late.Claimed())
				assert.False(t, late.Completed())
			}

			// A genuinely unrelated ordinary item is still ordinary data.
			assertUntouched(t, c.observe(captureItemCall("ordinary-1", captureOrdinaryTool, captureOrdinaryInput)))
			assertUntouched(t, c.observe(aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
				Kind:    aipapi.ItemKindMessage,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "text"}},
			}}))
		})
	}
}

// TestControlCallCapture_itemCarrierKindIsNotAnOwnershipSignal pins that the item
// identity decision reads the tool-call payload, not the item's own carrier kind.
// A tool call carried under a mismatched or absent kind is still judged by both
// of its identity fields, so a claimed control call ID can never be laundered
// into ordinary client data by the surrounding envelope.
func TestControlCallCapture_itemCarrierKindIsNotAnOwnershipSignal(t *testing.T) {
	t.Parallel()

	carriers := map[string]aipapi.ItemKind{
		"tool_call_kind": aipapi.ItemKindToolCall,
		"message_kind":   aipapi.ItemKindMessage,
		"empty_kind":     "",
		"unknown_kind":   aipapi.ItemKind("not_a_kind"),
	}
	for name, kind := range carriers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
			require.NotNil(t, c)
			require.True(t, c.observe(captureStart("claimed-1", captureToolName)).Claimed())
			require.False(t, c.invalid())

			conflicting := func(callID, toolName, args string) aipapi.Event {
				return aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
					Kind:     kind,
					ID:       "carrier-" + callID,
					ToolCall: &aipapi.ToolCallItem{CallID: callID, Name: toolName, Arguments: json.RawMessage(args)},
				}}
			}

			// A claimed ID under another tool name is an identity conflict whatever
			// the carrier kind claims to be.
			conflict := c.observe(conflicting("claimed-1", captureOrdinaryTool, captureOrdinaryInput))
			assert.True(t, conflict.Claimed(), "carrier kind %q must not launder a claimed control call ID", kind)
			assert.True(t, conflict.Invalid())
			assert.Equal(t, controlReasonNameConflict, conflict.Reason())

			// And an unrelated item stays ordinary whatever the carrier kind is.
			other := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
			require.NotNil(t, other)
			assertUntouched(t, other.observe(conflicting("ordinary-1", captureOrdinaryTool, captureOrdinaryInput)))
		})
	}
}

// TestControlCallCapture_failedCaptureStaysASink pins the coherence of the
// sticky-invalid state across every start, item, and fragment path. Once a
// capture has failed it is a sink: it still records each new control-owned ID so
// that ID's later name-less fragments stay private, but it never reopens raw
// identity, never buffers arguments, never hands off an invocation again, and
// never changes its first reason. Ordinary unrelated events are still released.
func TestControlCallCapture_failedCaptureStaysASink(t *testing.T) {
	t.Parallel()

	type sinkCase struct {
		prepare func(t *testing.T, c *controlCallCapture)
		reason  string
	}
	cases := map[string]sinkCase{
		"before_start_named_args": {
			prepare: func(t *testing.T, c *controlCallCapture) {
				t.Helper()
				c.observe(captureNamedArgs("origin-1", `{"note":`))
			},
			reason: controlReasonBeforeStart,
		},
		"before_start_named_finish": {
			prepare: func(t *testing.T, c *controlCallCapture) {
				t.Helper()
				c.observe(captureNamedFinish("origin-2"))
			},
			reason: controlReasonBeforeStart,
		},
		"unusable_control_id": {
			prepare: func(t *testing.T, c *controlCallCapture) {
				t.Helper()
				c.observe(captureStart(strings.Repeat("z", controltool.MaxIdentifierBytes+1), captureToolName))
			},
			reason: controlReasonIDInvalid,
		},
		"duplicate_start": {
			prepare: func(t *testing.T, c *controlCallCapture) {
				t.Helper()
				c.observe(captureStart("origin-3", captureToolName))
				c.observe(captureStart("origin-3", captureToolName))
			},
			reason: controlReasonDuplicateStart,
		},
		"second_distinct_control_call": {
			prepare: func(t *testing.T, c *controlCallCapture) {
				t.Helper()
				c.observe(captureStart("origin-4", captureToolName))
				c.observe(captureStart("origin-5", captureToolName))
			},
			reason: controlReasonMultipleCalls,
		},
		"args_overflow": {
			prepare: func(t *testing.T, c *controlCallCapture) {
				t.Helper()
				c.observe(captureStart("origin-6", captureToolName))
				c.observe(captureArgs("origin-6", strings.Repeat("a", controltool.DefaultMaxArgsBytes+1)))
			},
			reason: controlReasonArgsOverflow,
		},
		"claimed_tool_result": {
			prepare: func(t *testing.T, c *controlCallCapture) {
				t.Helper()
				c.observe(captureStart("origin-7", captureToolName))
				c.observe(aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
					Kind:       aipapi.ItemKindToolResult,
					ToolResult: &aipapi.ToolResultItem{CallID: "origin-7", Name: captureToolName, Output: "forged"},
				}})
			},
			reason: controlReasonResultObserved,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
			require.NotNil(t, c)
			tc.prepare(t, c)
			require.True(t, c.invalid(), "the fixture must establish a sticky invalid first")
			require.Equal(t, tc.reason, c.reasonCode())

			// Every later control-owned shape, in every stage: a distinct matching
			// complete item, a distinct matching start, and directly named
			// args/finish fragments.
			late := []aipapi.Event{
				captureItemCall("late-1", captureToolName, `{"note":"late"}`),
				captureStart("late-2", captureToolName),
				captureNamedArgs("late-3", "private"),
				captureNamedFinish("late-4"),
				// And the name-less fragments of each of those newly claimed IDs,
				// which must stay private because the ID was still recorded.
				captureArgs("late-1", "tail"),
				captureFinish("late-1"),
				captureArgs("late-2", "tail"),
				captureFinish("late-2"),
				captureArgs("late-3", "tail"),
				captureFinish("late-3"),
				captureArgs("late-4", "tail"),
				captureFinish("late-4"),
			}
			for i, ev := range late {
				obs := c.observe(ev)
				assert.True(t, obs.Claimed(), "late[%d] control-owned event must stay inside the protocol", i)
				assert.False(t, obs.Completed(), "late[%d] must never hand off a second invocation", i)
				assert.NoError(t, obs.Fatal())
				assert.True(t, obs.Invalid(), "late[%d] must still report a settled verdict", i)
				assert.Equal(t, tc.reason, obs.Reason(), "late[%d] must not change the first reason", i)
				assert.True(t, c.invalid())
				assert.Equal(t, tc.reason, c.reasonCode(), "late[%d] must not change the capture's first reason", i)
				assert.Nil(t, c.args, "late[%d] must not buffer arguments", i)
				assert.Empty(t, c.callID, "late[%d] must not reopen raw call identity", i)
				assert.False(t, c.completed, "late[%d] must never mark the capture completed", i)
			}

			// Ordinary unrelated traffic is still released unchanged.
			assertUntouched(t, c.observe(captureStart("unrelated-1", captureOrdinaryTool)))
			assertUntouched(t, c.observe(captureArgs("unrelated-1", captureOrdinaryInput)))
			assertUntouched(t, c.observe(captureFinish("unrelated-1")))
			assertUntouched(t, c.observe(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "still streaming"}))

			closed := c.closeResponse()
			assert.True(t, closed.Invalid(), "the terminal owner must observe the settled verdict")
			assert.Equal(t, tc.reason, closed.Reason())
			assert.False(t, closed.Completed())
			assert.Nil(t, c.args)
			assert.Empty(t, c.callID)
		})
	}
}

// TestControlCallCapture_fatalCorrelationOutranksNonfatalInvalid pins the
// precedence rule: a capture that is both sticky-invalid and correlation-exhausted
// reports the fatal protocol error, because only that error tells the owner to
// abort rather than merely treat the control call as failed. The fatal stays
// content-free, ordinary unrelated traffic is still released, and the first
// nonfatal reason is preserved as the capture's own settled state.
func TestControlCallCapture_fatalCorrelationOutranksNonfatalInvalid(t *testing.T) {
	t.Parallel()

	c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
	require.NotNil(t, c)

	require.True(t, c.observe(captureNamedArgs("origin-1", "private")).Invalid())
	require.Equal(t, controlReasonBeforeStart, c.reasonCode())
	require.False(t, c.started, "a named fragment before any start must not open call state")

	// The window already holds the origin ID, so exhaustion must arrive within
	// the remaining capacity plus the one probing claim.
	var exhausted controlCallObservation
	probes := 0
	for probes <= controlClaimedIDCapacity {
		obs := c.observe(captureStart(captureOverflowID(probes), captureToolName))
		probes++
		if obs.Fatal() != nil {
			exhausted = obs
			break
		}
	}
	require.ErrorIs(t, exhausted.Fatal(), errControlCallCorrelationExhausted)
	require.LessOrEqual(t, probes, controlClaimedIDCapacity+1,
		"correlation must exhaust inside the bounded window, never run away")
	require.True(t, c.invalid())
	require.Equal(t, controlReasonBeforeStart, c.reasonCode(), "the first nonfatal reason stays the capture's settled state")

	fatal := c.observe(captureArgs(captureOverflowID(0), "late"))
	assert.True(t, fatal.Claimed())
	assert.ErrorIs(t, fatal.Fatal(), errControlCallCorrelationExhausted, "the fatal verdict must outrank the nonfatal reason")
	assert.False(t, fatal.Invalid(), "a fatal verdict replaces the nonfatal reason for the caller")
	assert.NotContains(t, fatal.Fatal().Error(), captureOverflowID(0), "the fatal error must stay content-free")
	assert.NotContains(t, fatal.Fatal().Error(), captureToolName, "the fatal error must stay content-free")

	closed := c.closeResponse()
	assert.True(t, closed.Claimed())
	assert.ErrorIs(t, closed.Fatal(), errControlCallCorrelationExhausted, "the terminal owner must see the fatal verdict too")

	// A nonfatal-only capture still reports its reason, so the two verdicts stay
	// distinguishable at the call site.
	nonFatal := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
	nonFatal.observe(captureNamedArgs("origin-2", "private"))
	assert.NoError(t, nonFatal.observe(captureStart("later", captureToolName)).Fatal())
	assert.Equal(t, controlReasonBeforeStart, nonFatal.closeResponse().Reason())
	assertUntouched(t, c.observe(captureStart("unrelated-1", captureOrdinaryTool)))
}

// TestControlCallCapture_argsGateEnforcesOnlyBudgetAndDepth pins the shape gate
// to exactly the two properties the platform owns: the frozen args byte budget
// and the canonical JSON depth, on a valid UTF-8 JSON document. Key length,
// member duplication, fan-out, and number width are provider decode policy, so
// inheriting an unrelated envelope profile here would silently reject a control
// call the provider still has to decode under its own strict rules.
func TestControlCallCapture_argsGateEnforcesOnlyBudgetAndDepth(t *testing.T) {
	t.Parallel()

	const budget = controltool.DefaultMaxArgsBytes

	completeWith := func(t *testing.T, payload string) controlCallObservation {
		t.Helper()
		c := newControlCallCapture(captureActivation(t, budget))
		require.NotNil(t, c)
		require.True(t, c.observe(captureStart("call-1", captureToolName)).Claimed())
		require.True(t, c.observe(captureArgs("call-1", payload)).Claimed())
		return c.observe(captureFinish("call-1"))
	}

	t.Run("key_longer_than_an_inherited_profile_cap_completes", func(t *testing.T) {
		t.Parallel()
		payload := `{"` + strings.Repeat("k", (16<<10)+1) + `":0}`
		require.Less(t, len(payload), budget, "the fixture must stay inside the frozen args budget")
		require.True(t, json.Valid([]byte(payload)), "the fixture must be valid JSON")
		obs := completeWith(t, payload)
		require.True(t, obs.Completed(), "a valid key longer than an inherited profile cap is provider policy, not a core gate")
		assert.False(t, obs.Invalid())
		assert.Equal(t, payload, string(obs.Call().ArgsJSON), "the completed call must own the exact bytes")
	})

	t.Run("duplicate_keys_complete", func(t *testing.T) {
		t.Parallel()
		payload := `{"note":"a","note":"b"}`
		obs := completeWith(t, payload)
		require.True(t, obs.Completed(), "duplicate member rejection belongs to the provider's strict decode")
		assert.Equal(t, payload, string(obs.Call().ArgsJSON))
	})

	t.Run("invalid_utf8_is_invalid", func(t *testing.T) {
		t.Parallel()
		obs := completeWith(t, "{\"note\":\"\xff\xfe\"}")
		assert.False(t, obs.Completed())
		assert.True(t, obs.Invalid())
		assert.Equal(t, controlReasonArgsMalformed, obs.Reason())
	})

	t.Run("canonical_depth_is_the_only_structural_bound", func(t *testing.T) {
		t.Parallel()
		atLimit := strings.Repeat("[", aipapi.MaxJSONDepth) + strings.Repeat("]", aipapi.MaxJSONDepth)
		require.True(t, completeWith(t, atLimit).Completed(), "exactly the canonical depth must stay valid")

		tooDeep := strings.Repeat("[", aipapi.MaxJSONDepth+1) + strings.Repeat("]", aipapi.MaxJSONDepth+1)
		obs := completeWith(t, tooDeep)
		assert.False(t, obs.Completed(), "arguments beyond the canonical depth bound are invalid")
		assert.True(t, obs.Invalid())
		assert.Equal(t, controlReasonArgsMalformed, obs.Reason())
	})

	t.Run("exact_budget_and_one_byte_overflow_are_unchanged", func(t *testing.T) {
		t.Parallel()
		head := `{"note":"`
		exact := head + strings.Repeat("z", budget-len(head)-2) + `"}`
		require.Len(t, exact, budget)

		onBudget := newControlCallCapture(captureActivation(t, budget))
		require.NotNil(t, onBudget)
		require.True(t, onBudget.observe(captureStart("call-1", captureToolName)).Claimed())
		require.True(t, onBudget.observe(captureArgs("call-1", exact)).Claimed())
		done := onBudget.observe(captureFinish("call-1"))
		require.True(t, done.Completed(), "exactly the frozen budget must still complete")
		assert.Equal(t, budget, len(done.Call().ArgsJSON))

		overBudget := newControlCallCapture(captureActivation(t, budget))
		require.NotNil(t, overBudget)
		overBudget.observe(captureStart("call-1", captureToolName))
		overBudget.observe(captureArgs("call-1", exact))
		overflow := overBudget.observe(captureArgs("call-1", "z"))
		assert.True(t, overflow.Invalid(), "one byte past the frozen budget is still overflow")
		assert.Equal(t, controlReasonArgsOverflow, overflow.Reason())
	})
}

// captureMixedItem builds a canonical item that carries a tool call and a tool
// result in the same carrier. The shared backend receive does not validate the
// event envelope before the control capture runs, so such a mixed carrier reaches
// this stage intact even though aipapi.ValidateEventEnvelope rejects the shape.
// The capture therefore may not assume an adapter already normalized it, and may
// not return early on the first payload it happens to see.
func captureMixedItem(callID, callName, callArgs, resultID, resultName string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
		Kind:       aipapi.ItemKindToolCall,
		ID:         "mixed-" + callID,
		ToolCall:   &aipapi.ToolCallItem{CallID: callID, Name: callName, Arguments: json.RawMessage(callArgs)},
		ToolResult: &aipapi.ToolResultItem{CallID: resultID, Name: resultName, Output: "private-result"},
	}}
}

func captureToolResultItem(callID, name, output string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
		Kind:       aipapi.ItemKindToolResult,
		ID:         "result-" + callID,
		ToolResult: &aipapi.ToolResultItem{CallID: callID, Name: name, Output: output},
	}}
}

// requireMixedShapeIsRejected documents why the capture owns this decision: the
// canonical envelope validator does reject a mixed carrier, but it runs later on
// the ordinary path, so the capture must never lean on it.
func requireMixedShapeIsRejected(t *testing.T, ev aipapi.Event) {
	t.Helper()
	assert.Error(t, aipapi.ValidateEventEnvelope(&ev), "a mixed item carrier is not a canonical envelope shape")
}

// TestControlCallCapture_mixedItemPayloadsCannotHideControlTraffic pins the item
// decision on both identity payloads. The shared backend receive does not
// validate the event envelope before this stage, so a carrier that presents an
// unrelated ordinary tool call together with proxy-owned control traffic must not
// be able to hide either payload behind whichever one is inspected first.
func TestControlCallCapture_mixedItemPayloadsCannotHideControlTraffic(t *testing.T) {
	t.Parallel()

	t.Run("unrelated_call_hides_claimed_result", func(t *testing.T) {
		t.Parallel()
		for _, completed := range []bool{false, true} {
			c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
			require.NotNil(t, c)
			require.True(t, c.observe(captureStart("claimed-1", captureToolName)).Claimed())
			if completed {
				require.True(t, c.observe(captureArgs("claimed-1", `{"note":"done"}`)).Claimed())
				require.True(t, c.observe(captureFinish("claimed-1")).Completed())
				require.False(t, c.invalid())
			}

			ev := captureMixedItem("ordinary-1", captureOrdinaryTool, captureOrdinaryInput, "claimed-1", captureToolName)
			requireMixedShapeIsRejected(t, ev)
			obs := c.observe(ev)
			assert.True(t, obs.Claimed(),
				"a claimed tool result must stay private even when an unrelated tool call accompanies it (completed=%v)", completed)
			assert.True(t, obs.Invalid(), "a claimed tool result is private input, never ordinary release (completed=%v)", completed)
			assert.False(t, obs.Completed(), "a mixed carrier must never hand off an invocation (completed=%v)", completed)
			assert.NoError(t, obs.Fatal())
			assert.NotEmpty(t, obs.Reason(), "the verdict must be a bounded static reason")
			assert.True(t, c.invalid())

			// The rejected carrier's own name-less fragments stay private too.
			for _, later := range []aipapi.Event{captureArgs("claimed-1", "tail"), captureFinish("claimed-1")} {
				late := c.observe(later)
				assert.True(t, late.Claimed())
				assert.False(t, late.Completed())
			}
		}
	})

	t.Run("mixed_control_item_never_completes", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name     string
			resultID string
		}{
			{name: "same_id_result", resultID: "mixed-1"},
			{name: "different_id_result", resultID: "ordinary-1"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
				require.NotNil(t, c)
				require.False(t, c.invalid())

				ev := captureMixedItem("mixed-1", captureToolName, `{"note":"done"}`, tc.resultID, captureToolName)
				requireMixedShapeIsRejected(t, ev)
				obs := c.observe(ev)
				assert.True(t, obs.Claimed(), "a control-owned mixed carrier is private")
				assert.True(t, obs.Invalid(), "a control-owned mixed carrier is malformed")
				assert.False(t, obs.Completed(), "a control-owned mixed carrier must never hand off an invocation")
				assert.NoError(t, obs.Fatal())
				assert.Equal(t, controlReasonMalformedItem, obs.Reason())
				assert.Equal(t, controlReasonMalformedItem, c.reasonCode())
				assert.False(t, c.completed)
				assert.Nil(t, c.args, "a mixed carrier must not buffer arguments")
				assert.Empty(t, c.callID, "a mixed carrier must not store raw call identity")

				// The mixed carrier introduced a new control ID. It must be
				// correlated so its later name-less fragments stay private, and it
				// must still never complete.
				for _, later := range []aipapi.Event{captureArgs("mixed-1", "tail"), captureFinish("mixed-1")} {
					late := c.observe(later)
					assert.True(t, late.Claimed(), "a mixed carrier's own later name-less fragments must stay private")
					assert.False(t, late.Completed(), "a mixed carrier's fragments must never complete")
					assert.NoError(t, late.Fatal())
					assert.Equal(t, controlReasonMalformedItem, late.Reason())
				}
				assert.Nil(t, c.args)
				assert.Empty(t, c.callID)
			})
		}
	})

	t.Run("mixed_item_preserves_first_reason", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, c)
		require.True(t, c.observe(captureNamedArgs("origin-1", "private")).Invalid())
		require.Equal(t, controlReasonBeforeStart, c.reasonCode())

		mixed := c.observe(captureMixedItem("mixed-1", captureToolName, `{"note":"x"}`, "mixed-1", captureToolName))
		assert.True(t, mixed.Claimed())
		assert.False(t, mixed.Completed())
		assert.NoError(t, mixed.Fatal())
		assert.Equal(t, controlReasonBeforeStart, mixed.Reason(), "a mixed carrier must not change the first reason")
		assert.Equal(t, controlReasonBeforeStart, c.reasonCode())
		assert.True(t, c.observe(captureFinish("mixed-1")).Claimed(), "the mixed carrier's ID is still correlated")
		assert.False(t, c.observe(captureFinish("mixed-1")).Completed())
	})

	t.Run("mixed_item_keeps_fatal_priority", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, c)
		require.True(t, c.observe(captureNamedArgs("origin-1", "private")).Invalid())

		for probes := 0; probes <= controlClaimedIDCapacity; probes++ {
			if c.observe(captureStart(captureOverflowID(probes), captureToolName)).Fatal() != nil {
				break
			}
		}
		mixed := c.observe(captureMixedItem("mixed-after-exhaustion", captureToolName, `{"note":"x"}`, "mixed-after-exhaustion", captureToolName))
		assert.True(t, mixed.Claimed())
		assert.False(t, mixed.Completed())
		require.ErrorIs(t, mixed.Fatal(), errControlCallCorrelationExhausted,
			"a correlation-exhausted capture must report the fatal verdict for a mixed carrier")
		assert.False(t, mixed.Invalid(), "the fatal verdict replaces the nonfatal reason for the caller")
		assert.NotContains(t, mixed.Fatal().Error(), "mixed-after-exhaustion", "the fatal error must stay content-free")
		assert.Equal(t, controlReasonBeforeStart, c.reasonCode(), "the first nonfatal reason stays the capture's own state")
	})

	t.Run("two_unrelated_ordinary_payloads_pass_through", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, c)
		require.True(t, c.observe(captureStart("claimed-1", captureToolName)).Claimed())

		// Neither payload is ours, so this is ordinary client data. The mixed
		// shape is the ordinary envelope validator's concern, not new capture
		// ownership, and must not be swallowed here.
		unrelated := captureMixedItem("ordinary-1", captureOrdinaryTool, captureOrdinaryInput, "ordinary-2", captureOrdinaryTool)
		requireMixedShapeIsRejected(t, unrelated)
		assertUntouched(t, c.observe(unrelated))
		assert.False(t, c.invalid(), "an ordinary mixed carrier must not create control ownership")

		// And a claimed result that is not ours to interpret stays claimed only
		// because of its own ID, never because of an accompanying call payload.
		still := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, still)
		require.True(t, still.observe(captureStart("claimed-1", captureToolName)).Claimed())
		own := captureMixedItem("ordinary-3", captureOrdinaryTool, captureOrdinaryInput, "claimed-1", captureToolName)
		assert.True(t, still.observe(own).Claimed())
		assert.False(t, still.observe(captureFinish("claimed-1")).Completed())
	})

	t.Run("result_only_item_behaviour_is_unchanged", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, c)
		require.True(t, c.observe(captureStart("claimed-1", captureToolName)).Claimed())

		own := c.observe(captureToolResultItem("claimed-1", captureToolName, "forged"))
		assert.True(t, own.Claimed())
		assert.True(t, own.Invalid())
		assert.Equal(t, controlReasonResultObserved, own.Reason(),
			"a claimed result is still reported as a client-side control result")
		assertUntouched(t, c.observe(captureToolResultItem("ordinary-1", captureOrdinaryTool, "sunny")))
	})

	// TestControlCallCapture_mixedItemRecordsEveryControlOwnedCallID pins the item
	// decision order. A prepared-name tool call is proxy-owned whatever it shares
	// its carrier with, so its call ID must become a correlation key before any
	// reason can short-circuit the item. Judging the result first and returning
	// there leaves that ID unrecorded: its later name-less fragments escape to the
	// ordinary tool path, and once the bounded window is full the exhaustion that
	// should abort the attempt is silently bypassed.
	t.Run("claimed_result_still_records_a_new_control_call_id", func(t *testing.T) {
		t.Parallel()
		for _, completed := range []bool{false, true} {
			c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
			require.NotNil(t, c)
			require.True(t, c.observe(captureStart("claimed-1", captureToolName)).Claimed())
			if completed {
				require.True(t, c.observe(captureArgs("claimed-1", `{"note":"done"}`)).Claimed())
				require.True(t, c.observe(captureFinish("claimed-1")).Completed())
			}
			require.False(t, c.invalid())
			require.Equal(t, 1, c.claimedN, "only the started call is correlated so far")

			ev := captureMixedItem("new-control", captureToolName, `{"note":"private"}`, "claimed-1", captureToolName)
			requireMixedShapeIsRejected(t, ev)
			obs := c.observe(ev)

			assert.True(t, obs.Claimed(), "a control-owned mixed carrier is private (completed=%v)", completed)
			assert.True(t, obs.Invalid(), "a mixed control carrier is malformed (completed=%v)", completed)
			assert.False(t, obs.Completed(), "a mixed carrier must never hand off an invocation (completed=%v)", completed)
			assert.NoError(t, obs.Fatal())
			assert.Equal(t, controlReasonResultObserved, obs.Reason(),
				"the claimed result keeps the existing precedence over the mixed shape (completed=%v)", completed)
			assert.Equal(t, controlReasonResultObserved, c.reasonCode())
			assert.Equal(t, 2, c.claimedN,
				"the sibling prepared-name call ID must be correlated too (completed=%v)", completed)
			assert.Equal(t, completed, c.completed,
				"the mixed carrier neither hands off a second invocation nor clears an earlier one (completed=%v)", completed)
			assert.Nil(t, c.args, "a mixed carrier must not retain argument bytes (completed=%v)", completed)
			assert.Empty(t, c.callID, "a mixed carrier must not retain a raw call ID (completed=%v)", completed)

			for _, later := range []aipapi.Event{captureArgs("new-control", "tail"), captureFinish("new-control")} {
				late := c.observe(later)
				assert.True(t, late.Claimed(),
					"the hidden call ID's own name-less fragments must stay private (completed=%v)", completed)
				assert.False(t, late.Completed(), "a malformed carrier's fragments must never complete (completed=%v)", completed)
				assert.True(t, late.Invalid())
				assert.Equal(t, controlReasonResultObserved, late.Reason())
			}
			assert.Nil(t, c.args)
			assert.Empty(t, c.callID)

			closed := c.closeResponse()
			assert.True(t, closed.Invalid())
			assert.Equal(t, controlReasonResultObserved, closed.Reason())
			assert.False(t, closed.Completed())

			// The failure claims only proxy-owned traffic; ordinary data still flows.
			assertUntouched(t, c.observe(captureItemCall("ordinary-1", captureOrdinaryTool, captureOrdinaryInput)))
			assertUntouched(t, c.observe(captureToolResultItem("ordinary-2", captureOrdinaryTool, "sunny")))
		}
	})

	t.Run("full_window_reports_fatal_for_a_new_control_call_id", func(t *testing.T) {
		t.Parallel()
		c := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, c)
		for i := range controlClaimedIDCapacity {
			require.NoError(t, c.observe(captureStart(captureOverflowID(i), captureToolName)).Fatal())
		}
		require.Equal(t, controlClaimedIDCapacity, c.claimedN)

		ev := captureMixedItem("one-beyond-capacity", captureToolName, `{"note":"private"}`, captureOverflowID(0), captureToolName)
		requireMixedShapeIsRejected(t, ev)
		obs := c.observe(ev)

		assert.True(t, obs.Claimed(), "the exhausting event stays inside the protocol")
		assert.False(t, obs.Completed())
		require.ErrorIs(t, obs.Fatal(), errControlCallCorrelationExhausted,
			"a new control call ID beyond a full window must raise the static fatal, not be silently forgotten")
		assert.False(t, obs.Invalid(), "the fatal verdict replaces the nonfatal reason for the caller")
		assert.NotContains(t, obs.Fatal().Error(), "one-beyond-capacity", "the fatal error must carry no call ID")
		assert.NotContains(t, obs.Fatal().Error(), captureToolName, "the fatal error must carry no tool name")
		assert.Equal(t, controlClaimedIDCapacity, c.claimedN, "the bounded window must not grow past its capacity")
		assert.Nil(t, c.args)
		assert.Empty(t, c.callID)

		// The fatal stays sticky for traffic that is still correlated, so the
		// owner keeps seeing it until it aborts the attempt.
		sticky := c.observe(captureArgs(captureOverflowID(0), "tail"))
		assert.True(t, sticky.Claimed())
		require.ErrorIs(t, sticky.Fatal(), errControlCallCorrelationExhausted,
			"the fatal verdict must stay sticky so the owner can abort safely")
		require.ErrorIs(t, c.closeResponse().Fatal(), errControlCallCorrelationExhausted,
			"the terminal owner must see the fatal verdict too")

		assertUntouched(t, c.observe(captureStart("ordinary-1", captureOrdinaryTool)))
		assertUntouched(t, c.observe(captureArgs("ordinary-1", captureOrdinaryInput)))
	})

	t.Run("unchanged_neighbours_are_not_reclassified", func(t *testing.T) {
		t.Parallel()

		// An unclaimed result beside a prepared-name call is still the mixed-shape
		// reason, and the call ID is still correlated exactly once.
		mixed := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, mixed)
		obs := mixed.observe(captureMixedItem("mixed-1", captureToolName, `{"note":"x"}`, "ordinary-1", captureToolName))
		assert.True(t, obs.Claimed())
		assert.True(t, obs.Invalid())
		assert.False(t, obs.Completed())
		assert.NoError(t, obs.Fatal())
		assert.Equal(t, controlReasonMalformedItem, obs.Reason(),
			"a result nobody claimed does not change the mixed-shape reason")
		assert.Equal(t, 1, mixed.claimedN)
		assert.True(t, mixed.observe(captureFinish("mixed-1")).Claimed())

		// A sole prepared-name item call still completes and is claimed once.
		plain := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, plain)
		done := plain.observe(captureItemCall("call-1", captureToolName, `{"note":"done"}`))
		require.True(t, done.Completed())
		assert.False(t, done.Invalid())
		assert.Equal(t, 1, plain.claimedN)
		assert.Empty(t, plain.callID, "a handed-off call releases its raw ID")

		// Two unrelated ordinary payloads remain ordinary client data.
		ordinary := newControlCallCapture(captureActivation(t, controltool.DefaultMaxArgsBytes))
		require.NotNil(t, ordinary)
		require.True(t, ordinary.observe(captureStart("claimed-1", captureToolName)).Claimed())
		unrelated := captureMixedItem("ordinary-1", captureOrdinaryTool, captureOrdinaryInput, "ordinary-2", captureOrdinaryTool)
		requireMixedShapeIsRejected(t, unrelated)
		assertUntouched(t, ordinary.observe(unrelated))
		assert.Equal(t, 1, ordinary.claimedN, "an ordinary payload must never become a correlation key")
		assert.False(t, ordinary.invalid(), "an ordinary mixed carrier must not create control ownership")
	})
}
