package openairesponsestream

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func newTestMapper() (*Mapper, *stream.PendingEventQueue) {
	q := stream.NewPendingEventQueue(0)
	m := New(&q)
	return m, &q
}

func drainKinds(q *stream.PendingEventQueue) []aipapi.EventKind {
	var kinds []aipapi.EventKind
	for _, ev := range stream.DrainPending(q) {
		kinds = append(kinds, ev.Kind)
	}
	return kinds
}

func TestToolCallID_prefersPrimary(t *testing.T) {
	t.Parallel()
	if got := ToolCallID("fc_1", "call_1"); got != "fc_1" {
		t.Fatalf("ToolCallID = %q", got)
	}
}

func TestToolCallID_fallsBackWhenPrimaryEmpty(t *testing.T) {
	t.Parallel()
	if got := ToolCallID("", "call_1"); got != "call_1" {
		t.Fatalf("ToolCallID = %q", got)
	}
}

func TestCallIDFromRawJSON_extractsCallID(t *testing.T) {
	t.Parallel()
	raw := `{"type":"response.function_call_arguments.delta","call_id":"call_only","delta":"{}"}`
	if got := CallIDFromRawJSON(raw); got != "call_only" {
		t.Fatalf("CallIDFromRawJSON = %q", got)
	}
}

func TestToolCallIDFromRaw_prefersItemID(t *testing.T) {
	t.Parallel()
	raw := `{"item_id":"fc_1","call_id":"call_1"}`
	if got := ToolCallIDFromRaw("fc_1", raw); got != "fc_1" {
		t.Fatalf("ToolCallIDFromRaw = %q", got)
	}
}

func TestToolCallIDFromRaw_fallsBackToCallIDFromRaw(t *testing.T) {
	t.Parallel()
	raw := `{"call_id":"call_only","delta":"{}"}`
	if got := ToolCallIDFromRaw("", raw); got != "call_only" {
		t.Fatalf("ToolCallIDFromRaw = %q", got)
	}
}

func TestMapper_sawResponseStarted_tracksLifecycle(t *testing.T) {
	t.Parallel()
	m, _ := newTestMapper()
	if m.SawResponseStarted() {
		t.Fatal("expected false before response started")
	}
	if err := m.ResponseCreated(); err != nil {
		t.Fatal(err)
	}
	if !m.SawResponseStarted() {
		t.Fatal("expected true after response created")
	}
}

func TestMapper_outputTextDelta_emitsLifecycleAndText(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.OutputTextDelta("hel"); err != nil {
		t.Fatal(err)
	}
	if err := m.OutputTextDelta("lo"); err != nil {
		t.Fatal(err)
	}
	kinds := drainKinds(q)
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventTextDelta,
	}
	if len(kinds) != len(want) {
		t.Fatalf("kinds: %v", kinds)
	}
	for i, kind := range want {
		if kinds[i] != kind {
			t.Fatalf("kinds[%d] = %v, want %v", i, kinds[i], kind)
		}
	}
}

func TestMapper_completed_emitsLifecycleUsageAndFinished(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.BeginCompleted(); err != nil {
		t.Fatal(err)
	}
	usage := &aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  1,
		OutputTokens: 2,
		TotalTokens:  3,
	}
	if err := m.PushUsage(usage); err != nil {
		t.Fatal(err)
	}
	if err := m.ResponseFinished(); err != nil {
		t.Fatal(err)
	}
	events := stream.DrainPending(q)
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventUsageDelta,
		aipapi.EventResponseFinished,
	}
	if len(events) != len(want) {
		t.Fatalf("events: %+v", events)
	}
	for i, kind := range want {
		if events[i].Kind != kind {
			t.Fatalf("events[%d] = %v, want %v", i, events[i].Kind, kind)
		}
	}
	if u := events[2]; u.InputTokens != 1 || u.OutputTokens != 2 || u.TotalTokens != 3 {
		t.Fatalf("usage: %+v", u)
	}
}

func TestMapper_streamError_defaultMessageWhenEmpty(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.StreamError("server_error", "", "stream error"); err != nil {
		t.Fatal(err)
	}
	events := stream.DrainPending(q)
	if len(events) != 2 {
		t.Fatalf("events: %+v", events)
	}
	if events[0].Kind != aipapi.EventResponseStarted {
		t.Fatalf("first event: %v", events[0].Kind)
	}
	if events[1].Kind != aipapi.EventError {
		t.Fatalf("second event: %v", events[1].Kind)
	}
	if events[1].ErrorCode != "server_error" || events[1].ErrorMessage != "stream error" {
		t.Fatalf("error event: %+v", events[1])
	}
}

func TestMapper_toolCallStream_mapsToCanonicalToolEvents(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.ToolCallAdded("fc_1", "get_weather"); err != nil {
		t.Fatal(err)
	}
	if err := m.ToolCallArgsDelta("fc_1", `{"city":`); err != nil {
		t.Fatal(err)
	}
	if err := m.ToolCallArgsDelta("fc_1", `"NYC"}`); err != nil {
		t.Fatal(err)
	}
	if err := m.FinishToolCallArguments("fc_1", "get_weather", `{"city":"NYC"}`); err != nil {
		t.Fatal(err)
	}

	var kinds []aipapi.EventKind
	var args strings.Builder
	for _, ev := range stream.DrainPending(q) {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			args.WriteString(ev.Delta)
		}
	}
	if kinds[0] != aipapi.EventResponseStarted || kinds[1] != aipapi.EventMessageStarted || kinds[2] != aipapi.EventToolCallStarted {
		t.Fatalf("opening events: %v", kinds)
	}
	if got := args.String(); got != `{"city":"NYC"}` {
		t.Fatalf("combined args: %q", got)
	}
	if kinds[len(kinds)-1] != aipapi.EventToolCallFinished {
		t.Fatalf("last event: %v", kinds)
	}
}

func TestMapper_finishToolCallArguments_emitsArgsOnlyWhenNoDeltasSeen(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.ToolCallAdded("fc_1", "get_weather"); err != nil {
		t.Fatal(err)
	}
	if err := m.FinishToolCallArguments("fc_1", "get_weather", `{"city":"NYC"}`); err != nil {
		t.Fatal(err)
	}
	var argDeltas int
	for _, ev := range stream.DrainPending(q) {
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			argDeltas++
			if ev.Delta != `{"city":"NYC"}` {
				t.Fatalf("delta: %q", ev.Delta)
			}
		}
	}
	if argDeltas != 1 {
		t.Fatalf("arg deltas: %d", argDeltas)
	}
}

func TestMapper_finishToolCallArguments_skipsArgsWhenDeltasSeen(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.ToolCallAdded("fc_1", "get_weather"); err != nil {
		t.Fatal(err)
	}
	if err := m.ToolCallArgsDelta("fc_1", `{"city":"NYC"}`); err != nil {
		t.Fatal(err)
	}
	if err := m.FinishToolCallArguments("fc_1", "get_weather", `{"city":"NYC"}`); err != nil {
		t.Fatal(err)
	}
	var argDeltas int
	for _, ev := range stream.DrainPending(q) {
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			argDeltas++
		}
	}
	if argDeltas != 1 {
		t.Fatalf("arg deltas: %d", argDeltas)
	}
}

func TestMapper_emitToolCallFinished_emptyIDIsNoOp(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.EmitToolCallFinished(""); err != nil {
		t.Fatal(err)
	}
	if kinds := drainKinds(q); len(kinds) != 0 {
		t.Fatalf("events: %v", kinds)
	}
}

func TestMapper_emitToolCallFinished_suppressesDuplicates(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.ToolCallAdded("fc_1", "get_weather"); err != nil {
		t.Fatal(err)
	}
	if err := m.EmitToolCallFinished("fc_1"); err != nil {
		t.Fatal(err)
	}
	if err := m.EmitToolCallFinished("fc_1"); err != nil {
		t.Fatal(err)
	}
	var finished int
	for _, ev := range stream.DrainPending(q) {
		if ev.Kind == aipapi.EventToolCallFinished {
			finished++
		}
	}
	if finished != 1 {
		t.Fatalf("finished events: %d", finished)
	}
}

func TestMapper_completedTextFallback_onlyWhenNoTextDeltas(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.BeginCompleted(); err != nil {
		t.Fatal(err)
	}
	if err := m.CompletedTextFallback("done"); err != nil {
		t.Fatal(err)
	}
	if err := m.ResponseFinished(); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, ev := range stream.DrainPending(q) {
		if ev.Kind == aipapi.EventTextDelta {
			texts = append(texts, ev.Delta)
		}
	}
	if len(texts) != 1 || texts[0] != "done" {
		t.Fatalf("texts: %v", texts)
	}

	m2, q2 := newTestMapper()
	if err := m2.OutputTextDelta("hel"); err != nil {
		t.Fatal(err)
	}
	if err := m2.BeginCompleted(); err != nil {
		t.Fatal(err)
	}
	if err := m2.CompletedTextFallback("hello"); err != nil {
		t.Fatal(err)
	}
	texts = nil
	for _, ev := range stream.DrainPending(q2) {
		if ev.Kind == aipapi.EventTextDelta {
			texts = append(texts, ev.Delta)
		}
	}
	if len(texts) != 1 || texts[0] != "hel" {
		t.Fatalf("texts after delta: %v", texts)
	}
}

func TestMapper_remapToolCallID_movesBufferedArgsOntoCanonicalID(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	// Args arrive before the tool call is added, so they buffer under the
	// provisional item-only ID.
	if err := m.ToolCallArgsDelta("fc_late", `{"filePath":`); err != nil {
		t.Fatal(err)
	}
	// Learning the real call_id remaps the buffered args onto it.
	m.RemapToolCallID("fc_late", "call_late")
	if err := m.ToolCallAdded("call_late", "read"); err != nil {
		t.Fatal(err)
	}
	if err := m.FinishToolCallArguments("call_late", "read", `{"filePath":"x"}`); err != nil {
		t.Fatal(err)
	}

	var startedCount, argDeltaCount, finishedCount int
	var startedID string
	var args strings.Builder
	for _, ev := range stream.DrainPending(q) {
		switch ev.Kind {
		case aipapi.EventToolCallStarted:
			startedCount++
			startedID = ev.ToolCallID
		case aipapi.EventToolCallArgsDelta:
			argDeltaCount++
			args.WriteString(ev.Delta)
			if ev.ToolCallID != "call_late" {
				t.Fatalf("args delta under id %q, want call_late", ev.ToolCallID)
			}
		case aipapi.EventToolCallFinished:
			finishedCount++
			if ev.ToolCallID != "call_late" {
				t.Fatalf("finished under id %q, want call_late", ev.ToolCallID)
			}
		}
	}
	if startedCount != 1 || startedID != "call_late" {
		t.Fatalf("started = %d / %q, want 1 / call_late", startedCount, startedID)
	}
	if argDeltaCount != 1 || args.String() != `{"filePath":` {
		t.Fatalf("args = %d / %q, want 1 / {\"filePath\": (incremental preserved, full args suppressed)", argDeltaCount, args.String())
	}
	if finishedCount != 1 {
		t.Fatalf("finished = %d, want 1", finishedCount)
	}
}

func TestMapper_reasoningDelta_emitsLifecycleAndReasoning(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.ReasoningDelta("thin"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReasoningDelta("king"); err != nil {
		t.Fatal(err)
	}
	events := stream.DrainPending(q)
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventReasoningDelta,
		aipapi.EventReasoningDelta,
	}
	if len(events) != len(want) {
		t.Fatalf("events: %+v", events)
	}
	for i, kind := range want {
		if events[i].Kind != kind {
			t.Fatalf("event[%d] = %v, want %v", i, events[i].Kind, kind)
		}
	}
	if events[2].Delta != "thin" || events[3].Delta != "king" {
		t.Fatalf("deltas: %q %q", events[2].Delta, events[3].Delta)
	}
}

func TestMapper_reasoningDelta_emptyIsNoOp(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.ReasoningDelta(""); err != nil {
		t.Fatal(err)
	}
	for _, ev := range stream.DrainPending(q) {
		if ev.Kind == aipapi.EventReasoningDelta {
			t.Fatalf("expected no reasoning delta for empty input, got %+v", ev)
		}
	}
}

func TestMapper_remapToolCallID_noOpWhenIDsEqualOrEmpty(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	if err := m.ToolCallArgsDelta("fc_1", `{"x":1}`); err != nil {
		t.Fatal(err)
	}
	m.RemapToolCallID("fc_1", "fc_1") // equal -> no-op
	m.RemapToolCallID("", "call_1")   // empty old -> no-op
	m.RemapToolCallID("fc_1", "")     // empty new -> no-op
	if err := m.ToolCallAdded("fc_1", "get"); err != nil {
		t.Fatal(err)
	}
	var args strings.Builder
	for _, ev := range stream.DrainPending(q) {
		if ev.Kind == aipapi.EventToolCallArgsDelta {
			args.WriteString(ev.Delta)
		}
	}
	if args.String() != `{"x":1}` {
		t.Fatalf("args = %q, want original buffered delta preserved after no-op remaps", args.String())
	}
}

func TestMapper_doneOnlyDualFinalNotifications_emitFallbackArgsOnce(t *testing.T) {
	t.Parallel()
	m, q := newTestMapper()
	// A done-only tool call with no incremental deltas: the Responses adapter
	// routes both response.function_call_arguments.done and the final
	// response.output_item.done (function_call) through FinishToolCallArguments.
	// The full-argument fallback must be emitted exactly once; a second
	// argument delta after the finish would invalidate the private control
	// capture that already completed.
	if err := m.FinishToolCallArguments("fc_1", "attempt_completion", `{"result":"Done."}`); err != nil {
		t.Fatal(err)
	}
	if err := m.FinishToolCallArguments("fc_1", "attempt_completion", `{"result":"Done."}`); err != nil {
		t.Fatal(err)
	}
	events := stream.DrainPending(q)
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventToolCallStarted,
		aipapi.EventToolCallArgsDelta,
		aipapi.EventToolCallFinished,
	}
	if len(events) != len(want) {
		t.Fatalf("events = %+v, want %d events %v", events, len(want), want)
	}
	for i, kind := range want {
		if events[i].Kind != kind {
			t.Fatalf("events[%d] = %v, want %v (full sequence %+v)", i, events[i].Kind, kind, events)
		}
	}
	if got := events[3].Delta; got != `{"result":"Done."}` {
		t.Fatalf("fallback args delta = %q, want complete arguments", got)
	}
}
