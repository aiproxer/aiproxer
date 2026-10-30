package openresponses

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPresenceHelpers(t *testing.T) {
	t.Parallel()

	if b := cloneBytes(nil); b != nil {
		t.Fatalf("expected nil for cloneBytes(nil)")
	}

	if j := ensureNonNullJSON(nil); string(j) != "null" {
		t.Fatalf("expected 'null' for ensureNonNullJSON(nil)")
	}
	if j := ensureNonNullJSON([]byte("null")); string(j) != "null" {
		t.Fatalf("expected 'null' for ensureNonNullJSON('null')")
	}
	if j := ensureNonNullJSON([]byte(`{"a":1}`)); string(j) != `{"a":1}` {
		t.Fatalf("expected JSON byte string, got %s", string(j))
	}

	if m := ensureNonNilMap[string, int](nil); m == nil {
		t.Fatalf("expected non-nil map")
	}
}

func TestEncode_AllItemKinds(t *testing.T) {
	t.Parallel()

	items := []aipapi.Item{
		{
			Kind:   aipapi.ItemKindMessage,
			Role:   aipapi.RoleAssistant,
			Status: aipapi.ItemStatusCompleted,
			Content: []aipapi.ContentPart{
				{Kind: aipapi.ContentPartText, Text: "Assistant response"},
				{Kind: aipapi.ContentPartImageRef, ImageRef: "https://example.com/img.png"},
				{Kind: aipapi.ContentPartRefusal, Refusal: "Refused"},
			},
		},
		{
			Kind:   aipapi.ItemKindItemReference,
			ID:     "msg_ref",
			Status: aipapi.ItemStatusCompleted,
			Reference: &aipapi.ItemReference{
				ID: "msg_ref",
			},
		},
		{
			Kind:   aipapi.ItemKindToolCall,
			ID:     "call_item_1",
			Status: aipapi.ItemStatusCompleted,
			ToolCall: &aipapi.ToolCallItem{
				CallID:    "call_123",
				Name:      "calc",
				Arguments: []byte(`{"expr":"1+1"}`),
			},
		},
		{
			Kind:   aipapi.ItemKindToolResult,
			ID:     "result_item_1",
			Status: aipapi.ItemStatusCompleted,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "call_123",
				Name:   "calc",
				Parts: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "2"},
				},
			},
		},
		{
			Kind:   aipapi.ItemKindReasoning,
			ID:     "reas_item_1",
			Status: aipapi.ItemStatusCompleted,
			Reasoning: &aipapi.ReasoningItem{
				Reasoning: &aipapi.ReasoningPart{
					Text: "Reasoning text",
				},
			},
		},
		{
			Kind:   aipapi.ItemKindCompaction,
			ID:     "comp_item_1",
			Status: aipapi.ItemStatusCompleted,
			Compaction: &aipapi.CompactionItem{
				EncapsulatedID: "enc_1",
				Dialect:        "standard",
				Implementor:    "proxy",
				Opaque:         []byte(`{"key":"val"}`),
			},
		},
		{
			Kind:   aipapi.ItemKindExtension,
			ID:     "ext_item_1",
			Status: aipapi.ItemStatusCompleted,
			Extension: &aipapi.OpaqueExtension{
				Namespace:   "custom",
				Type:        "acme:telemetry",
				Implementor: "acme",
				Direction:   "in",
				Data:        []byte(`{"metric":42}`),
			},
		},
	}

	for i, item := range items {
		wItem, err := EncodeItem(item)
		if err != nil {
			t.Fatalf("EncodeItem failed for item[%d] (%s): %v", i, item.Kind, err)
		}
		if wItem.Type == "" {
			t.Fatalf("expected non-empty type for item[%d]", i)
		}
	}
}

func TestEncode_ToolChoiceModes(t *testing.T) {
	t.Parallel()

	modes := []struct {
		tc      aipapi.ToolChoice
		wantStr string
	}{
		{tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, wantStr: `"auto"`},
		{tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, wantStr: `"none"`},
		{tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, wantStr: `"required"`},
		{tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "my_fn"}, wantStr: `{"type":"function","name":"my_fn"}`},
		{tc: aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired}, wantStr: `"required"`},
	}

	for _, tt := range modes {
		call := aipapi.Call{
			Items: []aipapi.Item{
				{
					Kind:    aipapi.ItemKindMessage,
					Role:    aipapi.RoleUser,
					Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
				},
			},
			ToolChoice: tt.tc,
		}

		encoded, err := EncodeRequest(call)
		if err != nil {
			t.Fatalf("EncodeRequest failed for tool_choice %v: %v", tt.tc, err)
		}
		if !strings.Contains(string(encoded), tt.wantStr) {
			t.Fatalf("encoded request missing %s: got %s", tt.wantStr, string(encoded))
		}
	}
}

func TestEncode_TopLevelExtensions(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
			},
		},
		Extensions: map[string]json.RawMessage{
			"acme:extension_key": json.RawMessage(`{"custom":true}`),
		},
	}

	encoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest failed with extensions: %v", err)
	}

	if !strings.Contains(string(encoded), `"acme:extension_key":{"custom":true}`) {
		t.Fatalf("encoded JSON missing extension key: %s", string(encoded))
	}
}

func TestDecode_ToolChoiceErrors(t *testing.T) {
	t.Parallel()

	invalidToolChoices := []string{
		`{"model": "gpt-4o", "input": "hi", "tool_choice": "invalid_mode"}`,
		`{"model": "gpt-4o", "input": "hi", "tool_choice": {"type": "unknown_type"}}`,
		`{"model": "gpt-4o", "input": "hi", "tool_choice": {"type": "function"}}`, // missing function name
		`{"model": "gpt-4o", "input": "hi", "tool_choice": 12345}`,
	}

	for _, tcJSON := range invalidToolChoices {
		_, _, err := DecodeRequest([]byte(tcJSON))
		if err == nil {
			t.Fatalf("expected error for invalid tool_choice %s, got nil", tcJSON)
		}
	}
}

func TestBuildResponseResource_WithError(t *testing.T) {
	t.Parallel()

	env := EnvelopeMetadata{
		ResponseID: "resp_err",
		CreatedAt:  time.Now(),
		Model:      "gpt-4o",
		Status:     "failed",
	}

	streamErr := &aipapi.StreamError{
		Code:    "model_error",
		Message: "Model failed to process request",
	}

	wireRes, jsonBytes, err := BuildResponseResource(env, nil, UsageStats{}, aipapi.GenerationOptions{}, streamErr)
	if err != nil {
		t.Fatalf("BuildResponseResource failed with streamErr: %v", err)
	}

	if wireRes.Error == nil {
		t.Fatalf("expected non-nil Error in resource")
	}

	if !strings.Contains(string(jsonBytes), `"code":"model_error"`) {
		t.Fatalf("built JSON missing error code: %s", string(jsonBytes))
	}
}

func TestEncodeItem_UnknownKindError(t *testing.T) {
	t.Parallel()

	badItem := aipapi.Item{
		Kind: aipapi.ItemKind("unknown_kind"),
	}

	_, err := EncodeItem(badItem)
	if err == nil {
		t.Fatalf("expected error for unknown item kind, got nil")
	}
}

func TestDecode_AdditionalEdgeCases(t *testing.T) {
	t.Parallel()

	// Unexpected closing braces / brackets in strict JSON
	badJSONs := []string{
		`}`,
		`]`,
		`{"key": }`,
		`{"key": [1, 2}}`,
	}
	for _, bj := range badJSONs {
		_, _, err := DecodeRequest([]byte(bj))
		if err == nil {
			t.Fatalf("expected error for bad JSON %q, got nil", bj)
		}
	}

	// Unknown unprefixed top-level field
	_, _, err := DecodeRequest([]byte(`{"model": "gpt-4o", "input": "hi", "invalid_top_level": 123}`))
	if err == nil {
		t.Fatalf("expected error for unknown unprefixed top-level field, got nil")
	}

	// Tool missing name
	_, _, err = DecodeRequest([]byte(`{"model": "gpt-4o", "input": "hi", "tools": [{"type": "function"}]}`))
	if err == nil {
		t.Fatalf("expected error for tool missing name, got nil")
	}

	// ensureNonNilMap non-nil branch
	m := map[string]int{"a": 1}
	if res := ensureNonNilMap(m); res["a"] != 1 {
		t.Fatalf("ensureNonNilMap modified non-nil map")
	}
}

func TestStateMachine_EventErrorAndIncomplete(t *testing.T) {
	t.Parallel()

	envelope := EnvelopeMetadata{
		ResponseID: "resp_failed",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	// 1. EventError
	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	evs, err := sm.ProcessCanonicalEvent(aipapi.Event{
		Kind:         aipapi.EventError,
		ErrorCode:    "rate_limit_exceeded",
		ErrorMessage: "Rate limit exceeded",
	})
	if err != nil {
		t.Fatalf("EventError failed: %v", err)
	}
	if len(evs) == 0 || evs[len(evs)-1].Type != "response.failed" {
		t.Fatalf("expected response.failed as the terminal event, got %v", evs)
	}
	if sm.Status() != "failed" || sm.State() != StateTerminal {
		t.Fatalf("expected failed terminal state, got status=%s state=%s", sm.Status(), sm.State())
	}

	// 2. EventResponseFinished with max_tokens (incomplete)
	sm2 := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm2.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	evs, err = sm2.ProcessCanonicalEvent(aipapi.Event{
		Kind:         aipapi.EventResponseFinished,
		FinishReason: "length",
	})
	if err != nil {
		t.Fatalf("EventResponseFinished length failed: %v", err)
	}
	if len(evs) == 0 || evs[len(evs)-1].Type != "response.incomplete" {
		t.Fatalf("expected response.incomplete as the terminal event, got %v", evs)
	}
	if sm2.Status() != "incomplete" {
		t.Fatalf("expected status incomplete, got %s", sm2.Status())
	}
}

func TestStateMachine_DeepCloningAndSnapshotRestore(t *testing.T) {
	t.Parallel()

	envelope := EnvelopeMetadata{
		ResponseID: "resp_deep",
		CreatedAt:  time.Unix(1715620000, 0),
		Model:      "gpt-4o",
	}

	sm := NewStateMachine(envelope, aipapi.GenerationOptions{})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventResponseStarted})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventMessageStarted})
	_, _ = sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "active text"})

	// Force an error while activeItem and activeContentPart are non-nil
	_, err := sm.ProcessCanonicalEvent(aipapi.Event{Kind: aipapi.EventKind("invalid_trigger_error")})
	if err == nil {
		t.Fatalf("expected error on invalid event")
	}

	// Verify state and active pointers are intact
	if sm.Trajectory()[0].Content[0].Text != "active text" {
		t.Fatalf("expected active text to be restored after error")
	}

	// Test cloneItems with all item kinds
	items := []aipapi.Item{
		{
			Kind: aipapi.ItemKindToolCall,
			ToolCall: &aipapi.ToolCallItem{
				CallID:    "c1",
				Name:      "fn",
				Arguments: []byte(`{"a":1}`),
			},
		},
		{
			Kind: aipapi.ItemKindToolResult,
			ToolResult: &aipapi.ToolResultItem{
				CallID: "c1",
				Name:   "fn",
				Parts:  []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "res"}},
			},
		},
		{
			Kind: aipapi.ItemKindReasoning,
			Reasoning: &aipapi.ReasoningItem{
				Reasoning: &aipapi.ReasoningPart{Text: "reasoning"},
			},
		},
		{
			Kind: aipapi.ItemKindCompaction,
			Compaction: &aipapi.CompactionItem{
				EncapsulatedID: "enc1",
				Opaque:         []byte(`{"o":1}`),
			},
		},
		{
			Kind: aipapi.ItemKindExtension,
			Extension: &aipapi.OpaqueExtension{
				Namespace: "ns",
				Type:      "ext:type",
				Data:      []byte(`{"d":1}`),
			},
		},
		{
			Kind: aipapi.ItemKindItemReference,
			Reference: &aipapi.ItemReference{
				ID: "ref1",
			},
		},
	}

	cloned := cloneItems(items)
	if len(cloned) != len(items) {
		t.Fatalf("expected %d cloned items, got %d", len(items), len(cloned))
	}
	if string(cloned[0].ToolCall.Arguments) != `{"a":1}` {
		t.Fatalf("cloned tool call args mismatch")
	}
	if cloned[1].ToolResult.Parts[0].Text != "res" {
		t.Fatalf("cloned tool result text mismatch")
	}
	if cloned[2].Reasoning.Reasoning.Text != "reasoning" {
		t.Fatalf("cloned reasoning text mismatch")
	}
	if string(cloned[3].Compaction.Opaque) != `{"o":1}` {
		t.Fatalf("cloned compaction opaque mismatch")
	}
	if string(cloned[4].Extension.Data) != `{"d":1}` {
		t.Fatalf("cloned extension data mismatch")
	}
	if cloned[5].Reference.ID != "ref1" {
		t.Fatalf("cloned reference ID mismatch")
	}
}

func TestDecode_ItemKindsAndContentParts(t *testing.T) {
	t.Parallel()

	// Item decode tests for all item wire types
	wireItemsJSON := []string{
		`{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Hello"}]}`,
		`{"type": "message", "role": "user", "content": [{"type": "input_image", "image_url": "https://img.jpg"}]}`,
		`{"type": "message", "role": "user", "content": [{"type": "acme:input_file", "file_url": "https://file.pdf"}]}`,
		`{"type": "message", "role": "assistant", "content": [{"type": "refusal", "refusal": "Cannot process"}]}`,
		`{"type": "function_call", "call_id": "c1", "name": "fn1", "arguments": "{\"x\":1}"}`,
		`{"type": "function_call_output", "call_id": "c1", "output": "output string"}`,
		`{"type": "reasoning", "summary": [{"type": "summary_text", "text": "sum"}], "content": [{"type": "output_text", "text": "reas"}]}`,
		`{"type": "item_reference", "id": "ref_1"}`,
		`{"type": "acme:custom_item", "data": {"a": 1}}`,
	}

	for _, wj := range wireItemsJSON {
		var wi WireItem
		if err := json.Unmarshal([]byte(wj), &wi); err != nil {
			t.Fatalf("unmarshal WireItem failed for %s: %v", wj, err)
		}
		item, err := DecodeItem(wi, DefaultLimits())
		if err != nil {
			t.Fatalf("DecodeItem failed for %s: %v", wj, err)
		}
		if string(item.Kind) == "" {
			t.Fatalf("expected non-empty item kind for %s", wj)
		}
	}
}
