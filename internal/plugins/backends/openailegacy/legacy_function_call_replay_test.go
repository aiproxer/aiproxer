package openailegacy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The openai-legacy frontend stores a deprecated assistant function_call as a
// PartJSON with ToolName set, no ToolCallID, and the function_call object as
// Content. Replay must send it back as function_call, never as tool_calls, and
// must not invent a call id the legacy field does not carry.
func TestParamsForCall_legacyFunctionCallReplaysAsFunctionCall(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "legacy-fc",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("weather?")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:     aipapi.PartJSON,
				ToolName: "legacy_fn",
				Content:  json.RawMessage(`{"name":"legacy_fn","arguments":"{\"q\":\"x\"}"}`),
			}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("again?")}},
		},
		Route: aipapi.RouteIntent{Selector: "openai-legacy:gpt-4o-mini"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-legacy", Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p.Messages)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"function_call":{"arguments":"{\"q\":\"x\"}","name":"legacy_fn"}`) {
		t.Fatalf("legacy call must replay as function_call with its arguments: %s", s)
	}
	if strings.Contains(s, "tool_calls") {
		t.Fatalf("legacy function_call must never replay as tool_calls: %s", s)
	}
	if strings.Contains(s, `"id"`) {
		t.Fatalf("legacy function_call has no wire id, none may be sent: %s", s)
	}
}

// A tool_calls-shaped PartJSON (id and function inside Content) keeps the
// tool_calls replay path even when ToolCallID is empty.
func TestParamsForCall_toolCallShapedPartStaysToolCalls(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "tc-shaped",
		Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
			Kind:    aipapi.PartJSON,
			Content: json.RawMessage(`{"id":"call_h1","type":"function","function":{"name":"legacy_fn","arguments":"{}"}}`),
		}}}},
		Route: aipapi.RouteIntent{Selector: "openai-legacy:gpt-4o-mini"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-legacy", Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p.Messages)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"tool_calls":[{"id":"call_h1"`) || strings.Contains(s, "function_call") {
		t.Fatalf("tool_calls-shaped part must replay as tool_calls: %s", s)
	}
}

// A legacy function_call with no name is malformed and must fail explicitly
// rather than send an empty function_call.
func TestParamsForCall_legacyFunctionCallWithoutNameFails(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "legacy-noname",
		Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
			Kind:    aipapi.PartJSON,
			Content: json.RawMessage(`{"arguments":"{}"}`),
		}}}},
		Route: aipapi.RouteIntent{Selector: "openai-legacy:gpt-4o-mini"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-legacy", Model: "gpt-4o-mini"}}
	if _, err := backend.ParamsForCall(&call, cand); err == nil {
		t.Fatal("legacy function_call without a name must fail")
	}
}

// A Responses-shaped function_call envelope carries a call_id with no top-level
// id. It is not a legacy function_call, so it must not be sent with its identity
// silently dropped.
func TestParamsForCall_responsesEnvelopeIsNotLegacyFunctionCall(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "responses-envelope",
		Messages: []aipapi.Message{{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
			Kind:    aipapi.PartJSON,
			Content: json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{}"}`),
		}}}},
		Route: aipapi.RouteIntent{Selector: "openai-legacy:gpt-4o-mini"},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-legacy", Model: "gpt-4o-mini"}}
	p, err := backend.ParamsForCall(&call, cand)
	if err == nil {
		raw, _ := json.Marshal(p.Messages)
		t.Fatalf("responses envelope must not replay as a legacy function_call: %s", raw)
	}
}
