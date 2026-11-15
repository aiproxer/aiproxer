package openailegacy_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// marshalAssistantWire builds params for a canonical call and returns the
// marshaled assistant message at index 1 (after the leading user turn), plus
// its raw JSON, decoded into a generic map.
func marshalAssistantWire(t *testing.T, assistant aipapi.Message) (string, map[string]any) {
	t.Helper()
	call := aipapi.Call{
		ID: "assistant-null-content",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			assistant,
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 2 {
		t.Fatalf("messages: got %d want 2", len(p.Messages))
	}
	raw, err := json.Marshal(p.Messages[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return string(raw), m
}

func lookupToolCallPart() aipapi.Part {
	return aipapi.Part{
		Kind:       aipapi.PartJSON,
		ToolCallID: "call_1",
		ToolName:   "lookup",
		Content:    []byte(`{"q":"x"}`),
	}
}

func wantLookupToolCalls() []any {
	return []any{map[string]any{
		"id":   "call_1",
		"type": "function",
		"function": map[string]any{
			"name":      "lookup",
			"arguments": `{"q":"x"}`,
		},
	}}
}

func TestParamsForCall_assistantToolCallOnlyEncodesNullContent(t *testing.T) {
	t.Parallel()
	raw, m := marshalAssistantWire(t, aipapi.Message{
		Role:  aipapi.RoleAssistant,
		Parts: []aipapi.Part{lookupToolCallPart()},
	})
	if got, ok := m["content"]; !ok || got != nil {
		t.Fatalf("tool-call-only assistant must send content:null, got present=%v value=%#v wire=%s", ok, got, raw)
	}
	if !strings.Contains(raw, `"content":null`) {
		t.Fatalf("wire JSON must contain \"content\":null: %s", raw)
	}
	if !reflect.DeepEqual(m["tool_calls"], wantLookupToolCalls()) {
		t.Fatalf("tool_calls changed: %#v wire=%s", m["tool_calls"], raw)
	}
}

func TestParamsForCall_assistantTextWithToolCallKeepsText(t *testing.T) {
	t.Parallel()
	raw, m := marshalAssistantWire(t, aipapi.Message{
		Role: aipapi.RoleAssistant,
		Parts: []aipapi.Part{
			aipapi.TextPart("checking"),
			lookupToolCallPart(),
		},
	})
	if m["content"] != "checking" {
		t.Fatalf("text alongside tool calls must be kept: %s", raw)
	}
	if !reflect.DeepEqual(m["tool_calls"], wantLookupToolCalls()) {
		t.Fatalf("tool_calls changed: %#v wire=%s", m["tool_calls"], raw)
	}
}

func TestParamsForCall_assistantTextOnlyUnchanged(t *testing.T) {
	t.Parallel()
	raw, m := marshalAssistantWire(t, aipapi.Message{
		Role:  aipapi.RoleAssistant,
		Parts: []aipapi.Part{aipapi.TextPart("hello back")},
	})
	if m["content"] != "hello back" {
		t.Fatalf("text-only assistant content changed: %s", raw)
	}
	if _, ok := m["tool_calls"]; ok {
		t.Fatalf("text-only assistant must not carry tool_calls: %s", raw)
	}
}

func TestParamsForCall_assistantReasoningOnlyKeepsEmptyStringContent(t *testing.T) {
	t.Parallel()
	raw, m := marshalAssistantWire(t, aipapi.Message{
		Role: aipapi.RoleAssistant,
		Parts: []aipapi.Part{{
			Kind: aipapi.PartReasoning,
			Reasoning: &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
				Text:    "think",
			},
		}},
	})
	if m["content"] != "" {
		t.Fatalf("reasoning-only assistant must keep content \"\" (current behavior): %s", raw)
	}
	if m["reasoning_content"] != "think" {
		t.Fatalf("reasoning_content missing: %s", raw)
	}
}

// A turn that carries only a deprecated function_call follows the same rule
// as a tool_calls-only turn: content:null, not "".
func TestParamsForCall_assistantLegacyFunctionCallOnlyEncodesNullContent(t *testing.T) {
	t.Parallel()
	raw, m := marshalAssistantWire(t, aipapi.Message{
		Role: aipapi.RoleAssistant,
		Parts: []aipapi.Part{{
			Kind:     aipapi.PartJSON,
			ToolName: "legacy_fn",
			Content:  json.RawMessage(`{"name":"legacy_fn","arguments":"{\"q\":\"x\"}"}`),
		}},
	})
	if got, ok := m["content"]; !ok || got != nil {
		t.Fatalf("function_call-only assistant must send content:null, got present=%v value=%#v wire=%s", ok, got, raw)
	}
	if _, ok := m["function_call"]; !ok {
		t.Fatalf("function_call missing: %s", raw)
	}
}
