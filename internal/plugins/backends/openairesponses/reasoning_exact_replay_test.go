package openairesponses_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func exactCall(t *testing.T, opaque json.RawMessage, extraAssistant ...aipapi.Part) aipapi.Call {
	t.Helper()
	parts := []aipapi.Part{{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  append(json.RawMessage(nil), opaque...),
		},
	}}
	parts = append(parts, extraAssistant...)
	return aipapi.Call{
		ID: "exact-replay",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleAssistant, Parts: parts},
		},
	}
}

func marshalParams(t *testing.T, call aipapi.Call) string {
	t.Helper()
	p, err := backend.ParamsForCall(&call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
	if err != nil {
		t.Fatalf("ParamsForCall: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func firstReasoningItem(t *testing.T, paramsJSON string) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal([]byte(paramsJSON), &root); err != nil {
		t.Fatal(err)
	}
	input, ok := root["input"].([]any)
	if !ok {
		t.Fatalf("input missing: %s", paramsJSON)
	}
	for _, it := range input {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "reasoning" {
			return m
		}
	}
	t.Fatalf("no reasoning item in %s", paramsJSON)
	return nil
}

func assertContentSafeErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, f := range []string{"SECRET", "enc-secret", "rs_leak", `"summary_text"`, "prior-chat"} {
		if strings.Contains(msg, f) {
			t.Fatalf("content-safe error required, leaked %q: %v", f, err)
		}
	}
}

func TestParamsForCall_exactReasoning_presenceVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		in    string
		check func(t *testing.T, item map[string]any, raw string)
	}{
		{
			name: "minimal",
			in:   `{"id":"rs_1","summary":[]}`,
			check: func(t *testing.T, item map[string]any, raw string) {
				t.Helper()
				if item["id"] != "rs_1" || item["type"] != "reasoning" {
					t.Fatalf("item=%v", item)
				}
				if _, ok := item["encrypted_content"]; ok {
					t.Fatal("encrypted_content must be absent")
				}
				if _, ok := item["content"]; ok {
					t.Fatal("content must be absent")
				}
				if _, ok := item["status"]; ok {
					t.Fatal("status must be absent when not stored")
				}
			},
		},
		{
			name: "encrypted_null",
			in:   `{"id":"rs_1","summary":[],"encrypted_content":null}`,
			check: func(t *testing.T, item map[string]any, raw string) {
				t.Helper()
				if !strings.Contains(raw, `"encrypted_content":null`) {
					t.Fatalf("null must survive marshal: %s", raw)
				}
				if item["encrypted_content"] != nil {
					t.Fatalf("encrypted_content=%v want null", item["encrypted_content"])
				}
			},
		},
		{
			name: "encrypted_value",
			in:   `{"id":"rs_1","summary":[],"encrypted_content":"enc"}`,
			check: func(t *testing.T, item map[string]any, raw string) {
				t.Helper()
				if item["encrypted_content"] != "enc" {
					t.Fatalf("encrypted_content=%v", item["encrypted_content"])
				}
			},
		},
		{
			name: "encrypted_empty_string",
			in:   `{"id":"rs_1","summary":[],"encrypted_content":""}`,
			check: func(t *testing.T, item map[string]any, raw string) {
				t.Helper()
				if !strings.Contains(raw, `"encrypted_content":""`) {
					t.Fatalf("empty string must survive: %s", raw)
				}
				if item["encrypted_content"] != "" {
					t.Fatalf("encrypted_content=%v", item["encrypted_content"])
				}
			},
		},
		{
			name: "content_empty",
			in:   `{"id":"rs_1","summary":[],"content":[]}`,
			check: func(t *testing.T, item map[string]any, raw string) {
				t.Helper()
				c, ok := item["content"].([]any)
				if !ok || len(c) != 0 {
					t.Fatalf("content empty array required, got %v", item["content"])
				}
			},
		},
		{
			name: "content_value",
			in:   `{"id":"rs_1","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"c"}]}`,
			check: func(t *testing.T, item map[string]any, raw string) {
				t.Helper()
				c, ok := item["content"].([]any)
				if !ok || len(c) != 1 {
					t.Fatalf("content=%v", item["content"])
				}
			},
		},
		{
			name: "status_completed",
			in:   `{"id":"rs_1","summary":[],"status":"completed"}`,
			check: func(t *testing.T, item map[string]any, raw string) {
				t.Helper()
				if item["status"] != "completed" {
					t.Fatalf("status=%v", item["status"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := marshalParams(t, exactCall(t, json.RawMessage(tc.in), aipapi.TextPart("ans")))
			item := firstReasoningItem(t, raw)
			tc.check(t, item, raw)
		})
	}
}

func TestParamsForCall_exactReasoning_orderAmongTextAndTool(t *testing.T) {
	t.Parallel()
	opaqueA := json.RawMessage(`{"id":"rs_a","summary":[]}`)
	opaqueB := json.RawMessage(`{"id":"rs_b","summary":[{"type":"summary_text","text":"b"}]}`)
	fc := aipapi.Part{
		Kind:    aipapi.PartJSON,
		Content: json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"n","arguments":"{}"}`),
	}
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: opaqueA}},
				aipapi.TextPart("mid"),
				fc,
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: opaqueB}},
				aipapi.TextPart("end"),
			}},
		},
	}
	raw := marshalParams(t, call)
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		t.Fatal(err)
	}
	input, ok := root["input"].([]any)
	if !ok {
		t.Fatalf("input missing: %s", raw)
	}
	var seq []string
	for _, it := range input {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("non-object input item: %s", raw)
		}
		typ, _ := m["type"].(string)
		switch typ {
		case "reasoning":
			id, _ := m["id"].(string)
			seq = append(seq, "reasoning:"+id)
		case "function_call":
			seq = append(seq, "function_call")
		case "message", "":
			role, _ := m["role"].(string)
			seq = append(seq, role)
		default:
			t.Fatalf("unexpected input type %q in %s", typ, raw)
		}
	}
	want := []string{"user", "reasoning:rs_a", "assistant", "function_call", "reasoning:rs_b", "assistant"}
	if len(seq) != len(want) {
		t.Fatalf("seq=%v want %v raw=%s", seq, want, raw)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("seq=%v want %v raw=%s", seq, want, raw)
		}
	}
}

func TestParamsForCall_exactReasoning_noTextOrSummaryFallback(t *testing.T) {
	t.Parallel()
	t.Run("text_only", func(t *testing.T) {
		t.Parallel()
		call := aipapi.Call{Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
					Text:    "SECRET_SUM",
				}},
				aipapi.TextPart("a"),
			}},
		}}
		_, err := backend.ParamsForCall(&call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
		assertContentSafeErr(t, err)
	})
	t.Run("opaque_missing_summary_text_fallback_forbidden", func(t *testing.T) {
		t.Parallel()
		call := exactCall(t, json.RawMessage(`{"id":"rs_1"}`))
		call.Messages[1].Parts[0].Reasoning.Text = "SECRET_SUM"
		_, err := backend.ParamsForCall(&call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
		assertContentSafeErr(t, err)
	})
}

func TestParamsForCall_exactReasoning_rejectsInvalidNestedElements(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{name: "summary_wrong_type", in: `{"id":"rs_1","summary":[{"type":"reasoning_text","text":"SECRET_SUM"}]}`},
		{name: "summary_unknown_field", in: `{"id":"rs_1","summary":[{"type":"summary_text","text":"SECRET_SUM","extra":1}]}`},
		{name: "content_wrong_type", in: `{"id":"rs_1","summary":[],"content":[{"type":"summary_text","text":"SECRET_BODY"}]}`},
		{name: "content_unknown_field", in: `{"id":"rs_1","summary":[],"content":[{"type":"reasoning_text","text":"SECRET_BODY","x":1}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := exactCall(t, json.RawMessage(tc.in), aipapi.TextPart("a"))
			_, err := backend.ParamsForCall(&call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
			assertContentSafeErr(t, err)
		})
	}
}

func TestParamsForCall_exactReasoning_nestedElementTypesPreserved(t *testing.T) {
	t.Parallel()
	raw := marshalParams(t, exactCall(t, json.RawMessage(`{"id":"rs_1","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"c"}]}`), aipapi.TextPart("ans")))
	item := firstReasoningItem(t, raw)
	sum, _ := item["summary"].([]any)
	if len(sum) != 1 {
		t.Fatalf("summary=%v", item["summary"])
	}
	sm, _ := sum[0].(map[string]any)
	if sm["type"] != "summary_text" || sm["text"] != "s" {
		t.Fatalf("summary element=%v", sm)
	}
	content, _ := item["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content=%v", item["content"])
	}
	cm, _ := content[0].(map[string]any)
	if cm["type"] != "reasoning_text" || cm["text"] != "c" {
		t.Fatalf("content element=%v", cm)
	}
}

func TestParamsForCall_exactReasoning_rejectsInvalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		part aipapi.Part
	}{
		{name: "missing_id", part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"summary":[]}`),
		}}},
		{name: "missing_summary", part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_1"}`),
		}}},
		{name: "content_null", part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_1","summary":[],"content":null}`),
		}}},
		{name: "bad_status", part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_1","summary":[],"status":"failed"}`),
		}}},
		{name: "unknown_field", part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_1","summary":[],"extra":1}`),
		}}},
		{name: "duplicate_key", part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_1","summary":[],"id":"rs_2"}`),
		}}},
		{name: "trailing", part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_1","summary":[]}garbage`),
		}}},
		{name: "oversize", part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(`{"id":"rs_1","summary":[],"encrypted_content":"` + strings.Repeat("S", aipapi.MaxReasoningOpaqueBytes) + `"}`),
		}}},
		{name: "wrong_dialect", part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
			Text:    "SECRET_SUM",
			Opaque:  json.RawMessage(`{"id":"rs_1","summary":[]}`),
		}}},
		{name: "empty_opaque", part: aipapi.Part{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  nil,
			Text:    "SECRET_SUM",
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := aipapi.Call{Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{tc.part, aipapi.TextPart("a")}},
			}}
			_, err := backend.ParamsForCall(&call, routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-4o-mini"}})
			assertContentSafeErr(t, err)
		})
	}
}
