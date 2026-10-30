package openresponses

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func mustReadScenario(tb testing.TB, name string) []byte {
	tb.Helper()
	b, err := os.ReadFile("testdata/scenarios/" + name)
	if err != nil {
		tb.Fatalf("read scenario %s: %v", name, err)
	}
	return b
}

// scenarioCase pins a declarative scenario ID to expected semantic observations.
type scenarioCase struct {
	id          string
	kind        ScenarioKind
	fixture     string
	parse       func(t *testing.T, data []byte)
	run         func(t *testing.T)
	description string
}

func responseScenarioCases() []scenarioCase {
	return []scenarioCase{
		{
			id:          "scenario-response-text",
			kind:        ScenarioJSONText,
			fixture:     "response_text.json",
			description: "Pinned text response resource parses with required presence and one assistant message.",
			parse: func(t *testing.T, data []byte) {
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				res := parseResponseOK(t, data)
				if res.Status != "completed" || res.ID != "resp_5a3e04d550c84a63a1d4fc4e3e206abb" {
					t.Fatalf("status/id: %q/%q", res.Status, res.ID)
				}
				if len(res.Output) != 1 || res.Output[0].Type != ItemMessage {
					t.Fatalf("output: %+v", res.Output)
				}
				if got := res.OutputText(); got != "Here is an example of a response object in the specified format." {
					t.Fatalf("OutputText: %q", got)
				}
				if res.Usage.InputTokens != 25 || res.Usage.OutputTokens != 42 {
					t.Fatalf("usage: %+v", res.Usage)
				}
			},
		},
		{
			id:          "scenario-response-tools",
			kind:        ScenarioTools,
			fixture:     "response_tools.json",
			description: "Pinned function_call and function_call_output items parse with call identity preserved.",
			parse: func(t *testing.T, data []byte) {
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				res := parseResponseOK(t, data)
				if len(res.Output) != 2 {
					t.Fatalf("output len: %d", len(res.Output))
				}
				call := res.Output[0]
				if call.Type != ItemFunctionCall || call.Name != "get_weather" || call.CallID != "call_987zyx654wvu321" {
					t.Fatalf("function call item: %+v", call)
				}
				if !strings.Contains(call.Arguments, "San Francisco") {
					t.Fatalf("arguments: %q", call.Arguments)
				}
				out := res.Output[1]
				if out.Type != ItemFunctionCallOutput || out.CallID != call.CallID {
					t.Fatalf("function call output item: %+v", out)
				}
				if len(res.Tools) != 1 || res.Tools[0].Name != "get_weather" {
					t.Fatalf("tools: %+v", res.Tools)
				}
				if res.ParallelToolCalls != true {
					t.Fatalf("parallel_tool_calls: %v", res.ParallelToolCalls)
				}
			},
		},
		{
			id:          "scenario-response-reasoning",
			kind:        ScenarioReasoning,
			fixture:     "response_reasoning.json",
			description: "Reasoning item preserves summary/content and encrypted_content null presence.",
			parse: func(t *testing.T, data []byte) {
				t.Helper()
				t.Helper()
				t.Helper()
				res := parseResponseOK(t, data)
				if len(res.Output) != 2 {
					t.Fatalf("output len: %d", len(res.Output))
				}
				rs := res.Output[0]
				if rs.Type != ItemReasoning || rs.Status != "completed" {
					t.Fatalf("reasoning item: %+v", rs)
				}
				if rs.Reasoning == nil {
					t.Fatal("reasoning payload missing")
				}
				if len(rs.Reasoning.Summary) != 1 || !strings.Contains(rs.Reasoning.Summary[0].Text, "code word") {
					t.Fatalf("reasoning summary: %+v", rs.Reasoning.Summary)
				}
				if len(rs.Reasoning.Content) != 1 {
					t.Fatalf("reasoning content: %+v", rs.Reasoning.Content)
				}
				if !rs.Reasoning.EncryptedContentSet {
					t.Fatal("encrypted_content null presence must be preserved")
				}
				if rs.Reasoning.EncryptedContent != "" {
					t.Fatalf("encrypted_content should be empty, got %q", rs.Reasoning.EncryptedContent)
				}
				if res.Usage.ReasoningTokens != 6 {
					t.Fatalf("reasoning tokens: %d", res.Usage.ReasoningTokens)
				}
			},
		},
		{
			id:          "scenario-response-phase",
			kind:        ScenarioPhase,
			fixture:     "response_phase.json",
			description: "Assistant phase commentary/final_answer preserved on output messages.",
			parse: func(t *testing.T, data []byte) {
				t.Helper()
				res := parseResponseOK(t, data)
				if len(res.Output) != 2 {
					t.Fatalf("output len: %d", len(res.Output))
				}
				if res.Output[0].Phase != "commentary" || res.Output[1].Phase != "final_answer" {
					t.Fatalf("phases: %q/%q", res.Output[0].Phase, res.Output[1].Phase)
				}
				for i, it := range res.Output {
					if it.Role != "assistant" || it.Status != "completed" {
						t.Fatalf("item %d: %+v", i, it)
					}
				}
				if res.CompletedAt == nil {
					t.Fatal("completed_at must be present")
				}
			},
		},
		{
			id:          "scenario-response-extensions",
			kind:        ScenarioExtensions,
			fixture:     "response_extensions.json",
			description: "Unknown prefixed extension items parse as opaque without loss.",
			parse: func(t *testing.T, data []byte) {
				t.Helper()
				res := parseResponseOK(t, data)
				if len(res.Output) != 2 {
					t.Fatalf("output len: %d", len(res.Output))
				}
				ext := res.Output[0]
				if ext.Type != "acme:telemetry_chunk" || ext.ID != "tc_123" || ext.Status != "completed" {
					t.Fatalf("extension item: %+v", ext)
				}
				if len(ext.Opaque) == 0 {
					t.Fatal("opaque payload must be preserved")
				}
				var m map[string]any
				if err := json.Unmarshal(ext.Opaque, &m); err != nil {
					t.Fatalf("opaque json: %v", err)
				}
				latency, latencyOK := m["latency_ms"].(float64)
				cacheHit, cacheHitOK := m["cache_hit"].(bool)
				if !latencyOK || !cacheHitOK || latency != 72 || !cacheHit {
					t.Fatalf("opaque fields lost: %v", m)
				}
				if !ext.IsExtension() {
					t.Fatal("extension item must be flagged as extension")
				}
				if ext.OpaqueItem() == nil {
					t.Fatal("OpaqueItem must expose raw bytes")
				}
			},
		},
		{
			id:          "scenario-response-error",
			kind:        ScenarioNegative,
			fixture:     "response_error.json",
			description: "Failed response resource carries structured error with code/type/param.",
			parse: func(t *testing.T, data []byte) {
				t.Helper()
				res := parseResponseOK(t, data)
				if res.Status != "failed" {
					t.Fatalf("status: %q", res.Status)
				}
				if res.Error == nil || res.Error.Code != "model_not_found" || res.Error.Type != "invalid_request" || res.Error.Param != "model" {
					t.Fatalf("error: %+v", res.Error)
				}
				if !res.Failed() {
					t.Fatal("Failed() must be true")
				}
			},
		},
	}
}

func TestResponseScenario_Cases(t *testing.T) {
	t.Parallel()
	for _, tc := range responseScenarioCases() {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			data := mustReadScenario(t, tc.fixture)
			tc.parse(t, data)
		})
	}
}

func TestCompactResourceScenario_Parses(t *testing.T) {
	t.Parallel()
	data := mustReadScenario(t, "compact_resource.json")
	res, err := ParseCompactResource(data, DefaultParseOptions())
	if err != nil {
		t.Fatalf("ParseCompactResource: %v", err)
	}
	if res.Object != "response.compaction" {
		t.Fatalf("object: %q", res.Object)
	}
	if len(res.Output) != 1 || res.Output[0].Type != ItemCompaction {
		t.Fatalf("output: %+v", res.Output)
	}
	if res.Output[0].ID != "cmp_1" {
		t.Fatalf("compaction item id: %q", res.Output[0].ID)
	}
	if res.Usage.TotalTokens != 25 {
		t.Fatalf("usage: %+v", res.Usage)
	}
	if !res.IsCompact() {
		t.Fatal("IsCompact must be true")
	}
}

func TestCompactResource_CompactionItemCarriesEncryptedContent(t *testing.T) {
	t.Parallel()
	raw := `{
	  "id": "resp_compact_rt",
	  "object": "response.compaction",
	  "created_at": 1719900400,
	  "status": "completed",
	  "model": "m",
	  "output": [
	    {"type": "compaction", "id": "cmp_rt", "status": "completed", "encrypted_content": "gAAAAABpayload"}
	  ],
	  "usage": {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	}`
	res, err := ParseCompactResource([]byte(raw), DefaultParseOptions())
	if err != nil {
		t.Fatalf("ParseCompactResource: %v", err)
	}
	if len(res.Output) != 1 || res.Output[0].Type != ItemCompaction {
		t.Fatalf("output: %+v", res.Output)
	}
	if res.Output[0].EncryptedContent != "gAAAAABpayload" {
		t.Fatalf("encrypted_content = %q", res.Output[0].EncryptedContent)
	}
	back, err := json.Marshal(res.Output[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(back), `"encrypted_content":"gAAAAABpayload"`) {
		t.Fatalf("re-marshaled compaction item lost encrypted_content: %s", back)
	}
}

func parseResponseOK(t *testing.T, data []byte) *ResponseResource {
	t.Helper()
	res, err := ParseResponseResource(data, DefaultParseOptions())
	if err != nil {
		t.Fatalf("ParseResponseResource: %v", err)
	}
	return res
}

// TestRequiredPresence_DetectsMissingFields pins the required-presence contract:
// every non-optional ResponseResource field must be present on the wire.
func TestRequiredPresence_DetectsMissingFields(t *testing.T) {
	t.Parallel()
	base := mustReadScenario(t, "response_text.json")

	for _, missing := range []string{"id", "object", "created_at", "status", "model", "output", "parallel_tool_calls", "reasoning", "store", "background", "temperature", "text", "tool_choice", "tools", "top_p", "truncation", "usage", "metadata", "service_tier", "max_output_tokens", "max_tool_calls", "instructions", "previous_response_id", "error", "incomplete_details"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			t.Parallel()
			var m map[string]json.RawMessage
			if err := json.Unmarshal(base, &m); err != nil {
				t.Fatal(err)
			}
			delete(m, missing)
			reduced, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseResponseResource(reduced, DefaultParseOptions()); err == nil {
				t.Fatalf("expected required-presence error when %q is missing", missing)
			} else if !strings.Contains(err.Error(), "missing required field") {
				t.Fatalf("unexpected error for missing %q: %v", missing, err)
			}
		})
	}
}

// TestRequiredPresence_AcceptsNullWhereSpecified asserts null-valued nullable fields
// still satisfy required presence (they are present, just null).
func TestRequiredPresence_AcceptsNullWhereSpecified(t *testing.T) {
	t.Parallel()
	// response_text.json sets error/instructions/previous_response_id/incomplete_details
	// to null; ParseResponseResource must accept it (proven by fixture parse).
	parseResponseOK(t, mustReadScenario(t, "response_text.json"))

	// A resource that replaces those with absent-but-required must fail.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(mustReadScenario(t, "response_text.json"), &m); err != nil {
		t.Fatal(err)
	}
	m["error"] = json.RawMessage("null")
	m["instructions"] = json.RawMessage("null")
	m["previous_response_id"] = json.RawMessage("null")
	m["incomplete_details"] = json.RawMessage("null")
	ok, _ := json.Marshal(m)
	if _, err := ParseResponseResource(ok, DefaultParseOptions()); err != nil {
		t.Fatalf("null-presence resource must parse: %v", err)
	}
}

// TestResponse_RejectsMalformed ensures malformed resources fail without panic.
func TestResponse_RejectsMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data string
	}{
		{"empty", ""},
		{"not_json", "not json"},
		{"array_root", "[1,2,3]"},
		{"output_not_array", `{"id":"r","object":"response","created_at":1,"status":"completed","model":"m","output":"o","parallel_tool_calls":false,"reasoning":null,"store":true,"background":false,"temperature":1,"text":{},"tool_choice":"auto","tools":[],"top_p":1,"truncation":"disabled","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0},"metadata":{},"service_tier":"default","max_output_tokens":null,"max_tool_calls":null,"instructions":null,"previous_response_id":null,"error":null,"incomplete_details":null}`},
		{"usage_missing_tokens", `{"id":"r","object":"response","created_at":1,"status":"completed","model":"m","output":[],"parallel_tool_calls":false,"reasoning":null,"store":true,"background":false,"temperature":1,"text":{},"tool_choice":"auto","tools":[],"top_p":1,"truncation":"disabled","usage":{},"metadata":{},"service_tier":"default","max_output_tokens":null,"max_tool_calls":null,"instructions":null,"previous_response_id":null,"error":null,"incomplete_details":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseResponseResource([]byte(tc.data), DefaultParseOptions()); err == nil {
				t.Fatal("expected error for malformed response")
			}
		})
	}
}
