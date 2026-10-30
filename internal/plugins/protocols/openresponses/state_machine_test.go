package openresponses

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestStateMachine_ValidLifecycle(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_test123",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	if sm.Status() != "in_progress" {
		t.Fatalf("expected initial status in_progress, got %q", sm.Status())
	}

	// 1. Response started reserves the response but emits no wire event. The
	// pinned profile requires response.output_item.added to be first.
	evs, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	if err != nil {
		t.Fatalf("ProcessCanonicalEvent ResponseStarted failed: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("expected no wire event before the first output item, got %v", evs)
	}

	// 2. Message started emits output_item.added first, followed by the
	// compatibility response.created envelope and content-part lifecycle.
	evs, err = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventMessageStarted})
	if err != nil {
		t.Fatalf("ProcessCanonicalEvent MessageStarted failed: %v", err)
	}
	if len(evs) != 3 || evs[0].Type != "response.output_item.added" || evs[1].Type != "response.created" || evs[2].Type != "response.content_part.added" {
		t.Fatalf("expected item.added, response.created, and content_part.added, got %v", evs)
	}
	if evs[0].SequenceNumber != 0 || evs[1].SequenceNumber != 1 {
		t.Fatalf("expected first sequence numbers 0 and 1, got %d and %d", evs[0].SequenceNumber, evs[1].SequenceNumber)
	}

	// 3. Text deltas
	evs, err = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "Hello"})
	if err != nil {
		t.Fatalf("ProcessCanonicalEvent TextDelta failed: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "response.output_text.delta" || evs[0].Delta != "Hello" {
		t.Fatalf("expected text.delta Hello, got %v", evs)
	}

	evs, err = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: " World"})
	if err != nil {
		t.Fatalf("ProcessCanonicalEvent TextDelta 2 failed: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "response.output_text.delta" || evs[0].Delta != " World" {
		t.Fatalf("expected text.delta World, got %v", evs)
	}

	// 4. Usage delta
	_, err = sm.ProcessCanonicalEvent(aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
	})
	if err != nil {
		t.Fatalf("ProcessCanonicalEvent UsageDelta failed: %v", err)
	}

	// 5. Response finished
	evs, err = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("ProcessCanonicalEvent ResponseFinished failed: %v", err)
	}
	// Expect text.done, content_part.done, item.done, response.completed
	if len(evs) < 1 {
		t.Fatalf("expected completed events, got empty")
	}
	lastEv := evs[len(evs)-1]
	if lastEv.Type != "response.completed" {
		t.Fatalf("expected terminal response.completed, got %s", lastEv.Type)
	}

	// Verify accumulation equivalence with BuildResponseResource
	res, resBytes, err := sm.AccumulateResource()
	if err != nil {
		t.Fatalf("AccumulateResource failed: %v", err)
	}
	if res.ID != "resp_test123" {
		t.Fatalf("expected ID resp_test123, got %s", res.ID)
	}
	if len(res.Output) != 1 || res.Output[0].Type != "message" {
		t.Fatalf("expected 1 message output item, got %v", res.Output)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 5 {
		t.Fatalf("unexpected usage stats: %+v", res.Usage)
	}
	if len(resBytes) == 0 {
		t.Fatalf("expected non-empty JSON bytes")
	}
}

func TestStateMachine_ToolCallsAndArgumentDeltas(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_tool123",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})

	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	evs, err := sm.ProcessCanonicalEvent(aipapi.Event{
		Kind:       aipapi.EventToolCallStarted,
		ToolCallID: "call_abc",
		ToolName:   "get_weather",
	})
	if err != nil {
		t.Fatalf("ToolCallStarted failed: %v", err)
	}
	if len(evs) != 2 || evs[0].Type != "response.output_item.added" || evs[1].Type != "response.created" {
		t.Fatalf("expected output_item.added followed by response.created, got %v", evs)
	}

	evs, err = sm.ProcessCanonicalEvent(aipapi.Event{
		Kind:       aipapi.EventToolCallArgsDelta,
		ToolCallID: "call_abc",
		Delta:      `{"location": "`,
	})
	if err != nil {
		t.Fatalf("ToolCallArgsDelta 1 failed: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "response.function_call_arguments.delta" {
		t.Fatalf("expected function_call_arguments.delta, got %v", evs)
	}

	_, err = sm.ProcessCanonicalEvent(aipapi.Event{
		Kind:       aipapi.EventToolCallArgsDelta,
		ToolCallID: "call_abc",
		Delta:      `Paris"}`,
	})
	if err != nil {
		t.Fatalf("ToolCallArgsDelta 2 failed: %v", err)
	}

	evs, err = sm.ProcessCanonicalEvent(aipapi.Event{
		Kind:       aipapi.EventToolCallFinished,
		ToolCallID: "call_abc",
		ToolName:   "get_weather",
	})
	if err != nil {
		t.Fatalf("ToolCallFinished failed: %v", err)
	}
	if len(evs) != 2 || evs[0].Type != "response.function_call_arguments.done" || evs[1].Type != "response.output_item.done" {
		t.Fatalf("expected args.done and item.done, got %v", evs)
	}

	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseFinished})
	res, _, err := sm.AccumulateResource()
	if err != nil {
		t.Fatalf("AccumulateResource failed: %v", err)
	}
	if len(res.Output) != 1 || res.Output[0].Type != "function_call" {
		t.Fatalf("expected function_call output item, got %v", res.Output)
	}
	if string(res.Output[0].Arguments) != `"{\"location\": \"Paris\"}"` {
		t.Fatalf("expected arguments `\"{\\\"location\\\": \\\"Paris\\\"}\"`, got %s", string(res.Output[0].Arguments))
	}
}

func TestStateMachine_InterleavedParallelToolCallsKeepArgumentsByCallID(t *testing.T) {
	envelope := EnvelopeMetadata{ResponseID: "resp_parallel", CreatedAt: time.Unix(1715620000, 0), Model: "gpt-4o"}
	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_a", ToolName: "first"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_b", ToolName: "second"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_a", Delta: `{"a":`},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_b", Delta: `{"b":`},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_a", Delta: `1}`},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_b", Delta: `2}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_a"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_b"},
		{Kind: aipapi.EventResponseFinished},
	} {
		if _, err := sm.ProcessCanonicalEvent(ev); err != nil {
			t.Fatalf("processing %s failed: %v", ev.Kind, err)
		}
	}
	res, _, err := sm.AccumulateResource()
	if err != nil {
		t.Fatalf("AccumulateResource failed: %v", err)
	}
	if len(res.Output) != 2 {
		t.Fatalf("output items=%d, want 2: %+v", len(res.Output), res.Output)
	}
	if got := string(res.Output[0].Arguments); got != `"{\"a\":1}"` {
		t.Fatalf("call_a arguments=%q, want %q", got, `"{\"a\":1}"`)
	}
	if got := string(res.Output[1].Arguments); got != `"{\"b\":2}"` {
		t.Fatalf("call_b arguments=%q, want %q", got, `"{\"b\":2}"`)
	}
}

func TestStateMachine_ImplicitToolClosureEmitsArgumentsDoneFirst(t *testing.T) {
	envelope := EnvelopeMetadata{ResponseID: "resp_implicit_tool", CreatedAt: time.Unix(1715620000, 0), Model: "gpt-4o"}
	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_open", ToolName: "fn"})
	evs, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("ResponseFinished failed: %v", err)
	}
	if len(evs) != 3 || evs[0].Type != "response.function_call_arguments.done" || evs[1].Type != "response.output_item.done" || evs[2].Type != "response.completed" {
		t.Fatalf("implicit closure events=%v, want arguments.done, item.done, completed", evs)
	}
}

func TestStateMachine_RefusalAndReasoning(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_refusal",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})

	// Reasoning delta
	evs, err := sm.ProcessCanonicalEvent(aipapi.Event{
		Kind:  aipapi.EventReasoningDelta,
		Delta: "Thinking deeply...",
	})
	if err != nil {
		t.Fatalf("ReasoningDelta failed: %v", err)
	}
	if len(evs) == 0 {
		t.Fatalf("expected reasoning events")
	}

	// Response finished
	evs, err = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseFinished})
	if err != nil {
		t.Fatalf("ResponseFinished failed: %v", err)
	}
	if len(evs) == 0 || evs[len(evs)-1].Type != "response.completed" {
		t.Fatalf("expected response.completed")
	}
}

func TestStateMachine_IncompleteExplicitStatusNonLengthReason(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_inc_explicit",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	for _, reason := range []string{"content_filter", "interruption", "unknown", "provider_cutoff", ""} {
		t.Run("reason_"+reason, func(t *testing.T) {
			sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
			if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
				t.Fatalf("ResponseStarted failed: %v", err)
			}
			evs, err := sm.ProcessCanonicalEvent(aipapi.Event{
				Kind:           aipapi.EventResponseFinished,
				ResponseStatus: "incomplete",
				FinishReason:   reason,
			})
			if err != nil {
				t.Fatalf("EventResponseFinished failed: %v", err)
			}
			last := evs[len(evs)-1]
			if last.Type != "response.incomplete" {
				t.Fatalf("expected terminal response.incomplete, got %s", last.Type)
			}
			if last.Response == nil || last.Response.Status != "incomplete" {
				t.Fatalf("expected incomplete response on terminal event, got %+v", last.Response)
			}
			if sm.Status() != "incomplete" {
				t.Fatalf("expected status incomplete, got %s", sm.Status())
			}
			res, _, err := sm.AccumulateResource()
			if err != nil {
				t.Fatalf("AccumulateResource failed: %v", err)
			}
			if res.Status != "incomplete" {
				t.Fatalf("expected accumulated resource status incomplete, got %s", res.Status)
			}
			if reason != "content_filter" && string(res.IncompleteDetails) != "null" {
				t.Fatalf("non-refusal reason %q must leave incomplete_details null, got %s", reason, res.IncompleteDetails)
			}
		})
	}
}

func TestStateMachine_ExplicitCompletedStatusWinsOverContentFilterReason(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_completed_cf",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	evs, err := sm.ProcessCanonicalEvent(aipapi.Event{
		Kind:           aipapi.EventResponseFinished,
		ResponseStatus: "completed",
		FinishReason:   "content_filter",
	})
	if err != nil {
		t.Fatalf("EventResponseFinished failed: %v", err)
	}
	if last := evs[len(evs)-1]; last.Type != "response.completed" {
		t.Fatalf("expected terminal response.completed, got %s", last.Type)
	}
	if sm.Status() != "completed" {
		t.Fatalf("expected status completed, got %s", sm.Status())
	}
	res, _, err := sm.AccumulateResource()
	if err != nil {
		t.Fatalf("AccumulateResource failed: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected accumulated resource status completed, got %s", res.Status)
	}
	if !strings.Contains(string(res.IncompleteDetails), `"reason":"content_filter"`) {
		t.Fatalf("expected incomplete_details reason content_filter, got %s", res.IncompleteDetails)
	}
}

func TestStateMachine_CompletedContentFilterWithoutExplicitStatusStaysCompleted(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_legacy_cf",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	// A legacy provider (e.g. chat completions) reports a completed response
	// whose finish_reason is content_filter. Without an explicit status the
	// shared state machine must keep the response completed.
	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	evs, err := sm.ProcessCanonicalEvent(aipapi.Event{
		Kind:         aipapi.EventResponseFinished,
		FinishReason: "content_filter",
	})
	if err != nil {
		t.Fatalf("EventResponseFinished failed: %v", err)
	}
	if last := evs[len(evs)-1]; last.Type != "response.completed" {
		t.Fatalf("expected terminal response.completed, got %s", last.Type)
	}
	if sm.Status() != "completed" {
		t.Fatalf("expected status completed, got %s", sm.Status())
	}
	res, _, err := sm.AccumulateResource()
	if err != nil {
		t.Fatalf("AccumulateResource failed: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected accumulated resource status completed, got %s", res.Status)
	}
	if !strings.Contains(string(res.IncompleteDetails), `"reason":"content_filter"`) {
		t.Fatalf("expected incomplete_details reason content_filter, got %s", res.IncompleteDetails)
	}
}

func TestStateMachine_SequenceAndLifecycleErrors(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_err",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseFinished})

	// Sending event after terminal must fail
	_, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "after done"})
	if err == nil {
		t.Fatalf("expected error for event after terminal")
	}

	// Duplicate response started
	sm2 := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm2.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	_, err = sm2.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	if err == nil {
		t.Fatalf("expected error for duplicate response.created")
	}
}

func TestStateMachine_ConservativeLegacyNormalize(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_norm",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	canonicalEvents := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "Czesc! "},
		{Kind: aipapi.EventTextDelta, Delta: "Jak sie masz?"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 5, OutputTokens: 10, TotalTokens: 15},
		{Kind: aipapi.EventResponseFinished},
	}

	streamEvents, res, err := ConservativeLegacyNormalize(envelope, aipapi.GenerationOptions{}, canonicalEvents)
	if err != nil {
		t.Fatalf("ConservativeLegacyNormalize failed: %v", err)
	}

	if len(streamEvents) == 0 {
		t.Fatalf("expected stream events")
	}
	if res.ID != "resp_norm" || res.Status != "completed" {
		t.Fatalf("unexpected resource: %+v", res)
	}
	if len(res.Output) != 1 || res.Output[0].Phase != "" {
		t.Fatalf("expected conservative normalization without invented phase, got phase %q", res.Output[0].Phase)
	}
}

func TestStateMachine_ChunkedUTF8Deltas(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_utf8",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventMessageStarted})

	// Multibyte UTF-8 characters split across deltas
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "Zażółć "})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "gęślą "})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "jaźń 🚀"})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseFinished})

	res, _, err := sm.AccumulateResource()
	if err != nil {
		t.Fatalf("AccumulateResource failed: %v", err)
	}

	if len(res.Output) != 1 {
		t.Fatalf("expected 1 output item")
	}
	// Parse output text
	item := res.Output[0]
	if !strings.Contains(string(item.Content), "Zażółć gęślą jaźń 🚀") {
		t.Fatalf("expected output content to preserve UTF-8 text, got %s", string(item.Content))
	}
}

func TestStateMachine_ItemIDAndIndicesOnStreamEvents(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_item_id",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})

	// Message started
	msgEvs, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventMessageStarted})
	if err != nil {
		t.Fatalf("MessageStarted failed: %v", err)
	}
	if len(msgEvs) != 3 {
		t.Fatalf("expected 3 events, got %d", len(msgEvs))
	}
	contentPartAdded := msgEvs[2]
	if contentPartAdded.ItemID == "" {
		t.Fatalf("expected item_id on content_part.added event, got empty")
	}

	// Text delta
	deltaEvs, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"})
	if err != nil {
		t.Fatalf("TextDelta failed: %v", err)
	}
	if len(deltaEvs) != 1 || deltaEvs[0].ItemID == "" {
		t.Fatalf("expected non-empty item_id on output_text.delta event, got %v", deltaEvs)
	}
	if deltaEvs[0].ItemID != contentPartAdded.ItemID {
		t.Fatalf("expected matching item_id %s, got %s", contentPartAdded.ItemID, deltaEvs[0].ItemID)
	}

	// Reasoning delta
	sm2 := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm2.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	rEvs, err := sm2.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "thinking..."})
	if err != nil {
		t.Fatalf("ReasoningDelta failed: %v", err)
	}
	if len(rEvs) != 3 || rEvs[2].ItemID == "" {
		t.Fatalf("expected non-empty item_id on response.reasoning.delta, got %v", rEvs)
	}
}

func TestStateMachine_ExtensionEventsAndUnknownDiscriminators(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_ext",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})

	// Prefixed extension event should succeed
	extEvs, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventKind("acme:telemetry_chunk")})
	if err != nil {
		t.Fatalf("expected success for prefixed extension event, got %v", err)
	}
	if len(extEvs) != 1 || extEvs[0].Type != "acme:telemetry_chunk" {
		t.Fatalf("unexpected extension event: %v", extEvs)
	}

	// Unprefixed unknown event kind should be rejected
	_, err = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventKind("unknown_unprefixed_kind")})
	if err == nil {
		t.Fatalf("expected error for unprefixed unknown event kind, got nil")
	}
}

func TestStateMachine_TransactionalStateRollback(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_tx",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	initialSeq := sm.SequenceNumber()
	initialTrajectoryLen := len(sm.Trajectory())

	// Send an invalid event that will fail
	_, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventKind("invalid_unprefixed")})
	if err == nil {
		t.Fatalf("expected error on invalid event")
	}

	// Verify state remained completely unchanged
	if sm.SequenceNumber() != initialSeq {
		t.Fatalf("expected sequence_number %d after failed event, got %d", initialSeq, sm.SequenceNumber())
	}
	if len(sm.Trajectory()) != initialTrajectoryLen {
		t.Fatalf("expected trajectory length %d after failed event, got %d", initialTrajectoryLen, len(sm.Trajectory()))
	}
}

func TestStateMachine_DefensiveCopies(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_copy",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventMessageStarted})

	traj := sm.Trajectory()
	if len(traj) == 0 {
		t.Fatalf("expected non-empty trajectory")
	}

	// Mutate returned trajectory slice
	traj[0].Role = aipapi.RoleUser

	// Verify internal trajectory was not mutated
	traj2 := sm.Trajectory()
	if traj2[0].Role != aipapi.RoleAssistant {
		t.Fatalf("expected internal trajectory role to remain assistant, got %s", traj2[0].Role)
	}
}

func TestStateMachine_GettersAndErrorFormatting(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_getters",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	if sm.State() != StateInit {
		t.Fatalf("expected StateInit, got %s", sm.State())
	}

	seqErr := &SequenceError{
		Code:     "test_code",
		Event:    "test_event",
		ID:       "id_1",
		Sequence: 5,
		Message:  "test msg",
		Err:      ErrSequenceViolation,
	}
	errStr := seqErr.Error()
	if !strings.Contains(errStr, "test_event") || !strings.Contains(errStr, "test msg") {
		t.Fatalf("unexpected error string: %s", errStr)
	}
	if seqErr.Unwrap() != ErrSequenceViolation {
		t.Fatalf("unexpected unwrapped error: %v", seqErr.Unwrap())
	}

	seqErrNoSub := &SequenceError{
		Code:     "test_code",
		Event:    "test_event",
		ID:       "id_1",
		Sequence: 5,
		Message:  "test msg",
	}
	if !strings.Contains(seqErrNoSub.Error(), "test msg") {
		t.Fatalf("unexpected error string without sub-err: %s", seqErrNoSub.Error())
	}
}

func TestStateMachine_LegacyReasoningThenTextSynthesizesMessageBoundary(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_reason_text",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	// Legacy backends (ACP plan updates, OpenAI chat reasoning_content, Bedrock
	// reasoning, generic OpenAI-compatible chat reasoning) emit reasoning deltas
	// followed by text deltas in one assistant turn without an explicit message
	// boundary. The frontend state machine must conservatively synthesize the
	// message boundary (a message/text boundary, not phase/replay/compaction or
	// extensions) so the text lands in its own message item.
	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
		t.Fatalf("ResponseStarted failed: %v", err)
	}
	if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "thinking..."}); err != nil {
		t.Fatalf("ReasoningDelta failed: %v", err)
	}
	if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "answer"}); err != nil {
		t.Fatalf("TextDelta after reasoning must synthesize a message boundary: %v", err)
	}
	if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: " tail"}); err != nil {
		t.Fatalf("second TextDelta failed: %v", err)
	}
	if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseFinished}); err != nil {
		t.Fatalf("ResponseFinished failed: %v", err)
	}

	res, _, err := sm.AccumulateResource()
	if err != nil {
		t.Fatalf("AccumulateResource failed: %v", err)
	}
	if len(res.Output) != 2 {
		t.Fatalf("expected [reasoning, message] output items, got %d: %+v", len(res.Output), res.Output)
	}
	if res.Output[0].Type != "reasoning" {
		t.Fatalf("output[0] type = %q, want reasoning", res.Output[0].Type)
	}
	if res.Output[1].Type != "message" {
		t.Fatalf("output[1] type = %q, want message", res.Output[1].Type)
	}
	if !strings.Contains(string(res.Output[1].Content), "answer tail") {
		t.Fatalf("message text = %q, want concatenated 'answer tail'", string(res.Output[1].Content))
	}
}

func TestStateMachine_LegacyReasoningThenTextStreamEvents(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_reason_text_sse",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}
	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	var all []StreamEvent
	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "think"},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventResponseFinished},
	} {
		evs, err := sm.ProcessCanonicalEvent(ev)
		if err != nil {
			t.Fatalf("%s failed: %v", ev.Kind, err)
		}
		all = append(all, evs...)
	}
	var types []string
	for _, e := range all {
		types = append(types, e.Type)
	}
	if strings.Join(types, "|") != "response.output_item.added|response.created|response.reasoning.delta|response.reasoning.done|response.output_item.done|response.output_item.added|response.content_part.added|response.output_text.delta|response.output_text.done|response.content_part.done|response.output_item.done|response.completed" {
		t.Fatalf("unexpected event trajectory: %s", strings.Join(types, "|"))
	}
}

func TestStateMachine_ReasoningEventPinnedNames(t *testing.T) {
	envelope := EnvelopeMetadata{
		ResponseID: "resp_pinned_names",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}
	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	var all []StreamEvent
	for _, ev := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "think"},
		{Kind: aipapi.EventResponseFinished},
	} {
		evs, err := sm.ProcessCanonicalEvent(ev)
		if err != nil {
			t.Fatalf("%s failed: %v", ev.Kind, err)
		}
		all = append(all, evs...)
	}

	// Pinned official names (responseReasoningDeltaStreamingEventSchema /
	// responseReasoningDoneStreamingEventSchema) must be emitted; the legacy
	// reasoning_text variants must never appear.
	var delta, done *StreamEvent
	for i := range all {
		switch all[i].Type {
		case "response.reasoning.delta":
			delta = &all[i]
		case "response.reasoning.done":
			done = &all[i]
		case "response.reasoning_text.delta", "response.reasoning_text.done":
			t.Fatalf("legacy reasoning_text event name emitted: %s", all[i].Type)
		}
	}
	if delta == nil {
		t.Fatalf("missing response.reasoning.delta event")
	}
	if done == nil {
		t.Fatalf("missing response.reasoning.done event")
	}

	// Required fields per responseReasoningDeltaStreamingEventSchema:
	// item_id, output_index, content_index, delta.
	if delta.ItemID == "" {
		t.Fatalf("response.reasoning.delta missing item_id")
	}
	if delta.OutputIndex == nil || *delta.OutputIndex != 0 {
		t.Fatalf("response.reasoning.delta output_index = %v, want 0", delta.OutputIndex)
	}
	if delta.ContentIndex == nil || *delta.ContentIndex != 0 {
		t.Fatalf("response.reasoning.delta content_index = %v, want 0", delta.ContentIndex)
	}
	if delta.Delta != "think" {
		t.Fatalf("response.reasoning.delta delta = %q, want think", delta.Delta)
	}

	// Required fields per responseReasoningDoneStreamingEventSchema:
	// item_id, output_index, content_index, text.
	if done.ItemID != delta.ItemID {
		t.Fatalf("response.reasoning.done item_id %q != delta item_id %q", done.ItemID, delta.ItemID)
	}
	if done.OutputIndex == nil || *done.OutputIndex != 0 {
		t.Fatalf("response.reasoning.done output_index = %v, want 0", done.OutputIndex)
	}
	if done.ContentIndex == nil || *done.ContentIndex != 0 {
		t.Fatalf("response.reasoning.done content_index = %v, want 0", done.ContentIndex)
	}
	if done.Text != "think" {
		t.Fatalf("response.reasoning.done text = %q, want think", done.Text)
	}

	// SSE and the WebSocket text frame path share the same StreamEvent surface:
	// FormatSSEEvent drives the `event:` header from Type, and json.Marshal (the
	// WebSocket frame serializer) embeds Type as the JSON `type` discriminator.
	// Both must carry the pinned names and never the legacy reasoning_text ones.
	for _, ev := range []*StreamEvent{delta, done} {
		want := ev.Type
		sseBytes, err := FormatSSEEvent(*ev)
		if err != nil {
			t.Fatalf("FormatSSEEvent(%s) failed: %v", want, err)
		}
		if !strings.HasPrefix(string(sseBytes), "event: "+want+"\n") {
			t.Fatalf("SSE header = %q, want event: %s", sseBytes, want)
		}
		if strings.Contains(string(sseBytes), "reasoning_text") {
			t.Fatalf("SSE frame references legacy reasoning_text name: %s", sseBytes)
		}

		wsBytes, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("json.Marshal(%s) failed: %v", want, err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(wsBytes, &parsed); err != nil {
			t.Fatalf("WebSocket frame not valid JSON: %v", err)
		}
		if parsed["type"] != want {
			t.Fatalf("WebSocket frame type = %v, want %q (frame %s)", parsed["type"], want, wsBytes)
		}
	}
}
