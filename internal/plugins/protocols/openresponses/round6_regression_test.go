package openresponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestRound6DecodeRequestRejectsTooManyItemReferences(t *testing.T) {
	var input strings.Builder
	input.WriteString(`{"model":"m","input":[`)
	for i := range 3 {
		if i > 0 {
			input.WriteByte(',')
		}
		fmt.Fprintf(&input, `{"type":"item_reference","id":"ref_%d"}`, i)
	}
	input.WriteString(`]}`)

	limits := DefaultLimits()
	limits.MaxContinuationRefCount = 2
	if _, _, err := DecodeRequest([]byte(input.String()), limits); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("DecodeRequest error = %v, want continuation reference limit", err)
	}
}

func TestRound6EncodeRequestPreservesExtensionNumberBytes(t *testing.T) {
	const exact = "9223372036854775807"
	encoded, err := EncodeRequest(aipapi.Call{
		Items:      []aipapi.Item{{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}}}},
		Extensions: map[string]json.RawMessage{"vendor:token": json.RawMessage(exact)},
	})
	if err != nil {
		t.Fatalf("EncodeRequest failed: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"vendor:token":`+exact)) {
		t.Fatalf("encoded extension lost exact integer bytes: %s", encoded)
	}
}

func TestRound6StateMachineEnforcesSerializedResourceLimit(t *testing.T) {
	sm := NewStateMachine(EnvelopeMetadata{ResponseID: "resp_round6"}, aipapi.GenerationOptions{})
	if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventMessageStarted}); err != nil {
		t.Fatal(err)
	}
	_, before, err := sm.AccumulateResource()
	if err != nil {
		t.Fatal(err)
	}
	sm.limits.MaxResourceSizeBytes = len(before) + 1
	resourceBytesBefore := sm.resourceBytes
	_, err = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "resource growth"})
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("ProcessCanonicalEvent error = %v, want resource limit", err)
	}
	if got := sm.Trajectory()[0].Content[0].Text; got != "" {
		t.Fatalf("failed event mutated trajectory with %q", got)
	}
	if got := sm.resourceBytes; got != resourceBytesBefore {
		t.Fatalf("failed event changed resource accounting: got %d, want %d", got, resourceBytesBefore)
	}
}

func TestRound6StateMachineBoundsReasoningAndToolArgumentDeltas(t *testing.T) {
	t.Run("reasoning", func(t *testing.T) {
		sm := NewStateMachine(EnvelopeMetadata{ResponseID: "resp_reasoning"}, aipapi.GenerationOptions{})
		if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
			t.Fatal(err)
		}
		sm.limits.MaxResourceSizeBytes = sm.resourceBytes
		if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "reasoning"}); !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("reasoning delta error = %v, want resource limit", err)
		}
		if got := sm.Trajectory(); len(got) != 0 || sm.resourceBytes == 0 {
			t.Fatalf("rejected reasoning event did not roll back trajectory/counter: items=%d bytes=%d", len(got), sm.resourceBytes)
		}
	})

	t.Run("tool arguments", func(t *testing.T) {
		sm := NewStateMachine(EnvelopeMetadata{ResponseID: "resp_tool_limit"}, aipapi.GenerationOptions{})
		if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
			t.Fatal(err)
		}
		if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_limit", ToolName: "fn"}); err != nil {
			t.Fatal(err)
		}
		sm.limits.MaxResourceSizeBytes = sm.resourceBytes
		if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_limit", Delta: `{"x":1}`}); !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("tool argument delta error = %v, want resource limit", err)
		}
		if got := sm.Trajectory()[0].ToolCall.Arguments; len(got) != 0 {
			t.Fatalf("rejected tool argument event mutated arguments: %q", got)
		}
	})
}

func TestRound6StateMachineRollbackAfterMaterializationCheckpointFailure(t *testing.T) {
	sm := NewStateMachine(EnvelopeMetadata{ResponseID: "resp_materialize_rollback"}, aipapi.GenerationOptions{})
	for _, event := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
	} {
		if _, err := sm.ProcessCanonicalEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	builder := sm.textBuilders[0]
	if builder == nil || builder.String() != "hello" {
		t.Fatalf("expected text builder hello, got %#v", builder)
	}
	beforeLength := len(builder.data)
	beforePart := sm.textPartIndexes[0]
	_, serializedBefore, err := sm.AccumulateResource()
	if err != nil {
		t.Fatalf("snapshot before finish: %v", err)
	}
	sm.limits.MaxResourceSizeBytes = len(serializedBefore) - 1
	if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseFinished}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("finish error = %v, want resource limit", err)
	}
	if got := sm.textBuilders[0].String(); got != "hello" || len(sm.textBuilders[0].data) != beforeLength {
		t.Fatalf("builder was not rolled back: %q (%d bytes)", got, len(sm.textBuilders[0].data))
	}
	if sm.textPartIndexes[0] != beforePart {
		t.Fatalf("text part index changed: got %d, want %d", sm.textPartIndexes[0], beforePart)
	}
	if got := sm.Trajectory()[0].Content[0].Text; got != "hello" {
		t.Fatalf("materialized text changed after rollback: %q", got)
	}
	if sm.activeItem == nil || sm.activeContentPart == nil {
		t.Fatalf("active pointers were not restored: item=%p part=%p", sm.activeItem, sm.activeContentPart)
	}
}

func TestRound6StateMachineRollbackRestoresMaterializedToolState(t *testing.T) {
	sm := NewStateMachine(EnvelopeMetadata{ResponseID: "resp_tool_materialize_rollback"}, aipapi.GenerationOptions{})
	for _, event := range []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_materialize", ToolName: "fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_materialize", Delta: `{"x":1}`},
	} {
		if _, err := sm.ProcessCanonicalEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	builder := sm.toolArgBuilders[0]
	if builder == nil || builder.String() != `{"x":1}` {
		t.Fatalf("expected tool argument builder, got %#v", builder)
	}
	beforeLength := len(builder.data)
	if got := sm.activeToolCalls["call_materialize"]; got != 0 {
		t.Fatalf("active tool call index = %d, want 1", got)
	}

	// ResponseFinished materializes and closes the tool call before its final
	// resource checkpoint. Force that checkpoint to fail and verify every lazy
	// and materialized representation returns to its pre-event state.
	sm.limits.MaxResourceSizeBytes = sm.resourceBytes - 1
	if _, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseFinished}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("finish error = %v, want resource limit", err)
	}
	if got := sm.toolArgBuilders[0].String(); got != `{"x":1}` || len(sm.toolArgBuilders[0].data) != beforeLength {
		t.Fatalf("tool argument builder was not rolled back: %q (%d bytes)", got, len(sm.toolArgBuilders[1].data))
	}
	if got := sm.Trajectory()[0].ToolCall.Arguments; string(got) != `{"x":1}` {
		t.Fatalf("materialized tool arguments changed after rollback: %q", got)
	}
	if got := sm.activeToolCalls["call_materialize"]; got != 0 {
		t.Fatalf("active tool call mapping was not restored: got %d, want 1", got)
	}
	if sm.activeItem == nil || sm.activeItemIdx != 0 {
		t.Fatalf("active tool item was not restored: item=%p index=%d", sm.activeItem, sm.activeItemIdx)
	}
}

func TestRound6DecodeItemRejectsPrimitiveAndNullUnionValues(t *testing.T) {
	for _, raw := range []string{`true`, `false`, `123`, `null`} {
		t.Run("reasoning_content_"+raw, func(t *testing.T) {
			_, err := DecodeItem(WireItem{Type: "reasoning", Content: []byte(raw)}, DefaultLimits())
			if err == nil {
				t.Fatalf("DecodeItem accepted reasoning content %s", raw)
			}
		})
		t.Run("reasoning_field_"+raw, func(t *testing.T) {
			_, err := DecodeItem(WireItem{Type: "reasoning", Reasoning: []byte(raw)}, DefaultLimits())
			if err == nil {
				t.Fatalf("DecodeItem accepted reasoning field %s", raw)
			}
		})
		t.Run("tool_output_"+raw, func(t *testing.T) {
			_, err := DecodeItem(WireItem{Type: "function_call_output", Output: []byte(raw)}, DefaultLimits())
			if err == nil {
				t.Fatalf("DecodeItem accepted tool output %s", raw)
			}
		})
	}
}
