package codex

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestBuildInputItems_preservesActionTrajectoryOrder(t *testing.T) {
	t.Parallel()
	reasoning := func(id string) aipapi.Part {
		return aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"` + id + `","type":"reasoning","summary":[],"encrypted_content":"opaque-` + id + `"}`),
		}}
	}
	call := &aipapi.Call{
		Tools: []aipapi.ToolDef{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("use lookup")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				reasoning("r1"),
				{Kind: aipapi.PartJSON, ToolCallID: "call-1", ToolName: "lookup", Content: json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"x\"}"}`)},
			}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call-1", Content: json.RawMessage(`{"answer":"y"}`)}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{reasoning("r2"), aipapi.TextPart("done")}},
		},
	}
	items, err := buildInputItems(call)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 6 {
		t.Fatalf("input items = %d, want 6", len(items))
	}
	wantTypes := []string{"message", "reasoning", "function_call", "function_call_output", "reasoning", "message"}
	for i, want := range wantTypes {
		body, err := json.Marshal(items[i])
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Type != want {
			t.Fatalf("item[%d] type = %q, want %q; body=%s", i, got.Type, want, body)
		}
	}
}

func TestBuildInputItems_itemAuthorityPreservesExactTrajectory(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "use lookup"}}},
		{Kind: aipapi.ItemKindReasoning, Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"r1","type":"reasoning","summary":[],"encrypted_content":"opaque"}`),
		}}},
		{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"q":"x"}`)}},
		{Kind: aipapi.ItemKindToolResult, ToolResult: &aipapi.ToolResultItem{CallID: "call-1", Name: "lookup", Output: "y"}},
	}}
	items, err := buildInputItems(call)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("input items = %d, want 4", len(items))
	}
	for i, want := range []string{"message", "reasoning", "function_call", "function_call_output"} {
		body, err := json.Marshal(items[i])
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Type != want {
			t.Fatalf("item[%d] type = %q, want %q; body=%s", i, got.Type, want, body)
		}
	}
}

func TestBuildInputItems_itemAuthorityRejectsUnsupportedReasoningBeforeUpstream(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{Items: []aipapi.Item{{
		Kind: aipapi.ItemKindReasoning,
		Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
			Text:    "must not be synthesized",
		}},
	}}}
	if _, err := buildInputItems(call); err == nil {
		t.Fatal("unsupported reasoning dialect must fail before upstream")
	}
}

func TestBuildInputItems_itemAuthorityRejectsNilCompactionWithoutPanic(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{Items: []aipapi.Item{{Kind: aipapi.ItemKindCompaction}}}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("nil compaction must return a content-safe error, panicked: %v", recovered)
		}
	}()
	if _, err := buildInputItems(call); err == nil {
		t.Fatal("nil compaction must be rejected before upstream")
	}
}

func TestBuildInputItems_itemAuthorityRejectsMalformedCompaction(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{Items: []aipapi.Item{{
		Kind:       aipapi.ItemKindCompaction,
		Compaction: &aipapi.CompactionItem{Opaque: json.RawMessage(`{"type":"message"}`)},
	}}}
	if _, err := buildInputItems(call); err == nil {
		t.Fatal("non-compaction opaque item must be rejected before upstream")
	}
}

func TestBuildInputItems_itemAuthorityRejectsOrphanToolResult(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{Items: []aipapi.Item{{
		Kind:       aipapi.ItemKindToolResult,
		ToolResult: &aipapi.ToolResultItem{CallID: "missing", Name: "lookup", Output: "result"},
	}}}
	if _, err := buildInputItems(call); err == nil {
		t.Fatal("orphan tool output must be rejected before upstream")
	}
}

func TestBuildInputItems_itemAuthorityRejectsWireIllegalMessageRole(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{Items: []aipapi.Item{{
		Kind:    aipapi.ItemKindMessage,
		Role:    aipapi.RoleSystem,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "system"}},
	}}}
	if _, err := buildInputItems(call); err == nil {
		t.Fatal("system item must be handled as instructions or rejected before upstream")
	}
}
