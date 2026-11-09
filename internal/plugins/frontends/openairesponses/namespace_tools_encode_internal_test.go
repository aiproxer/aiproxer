package openairesponses

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestWriteStreamSSE_NamespaceToolCallsKeepOrderAndIdentity pins that response
// restoration is per call, not per request: a projected member and an ordinary
// function in one response keep their announcement order, call ids, arguments,
// and only the member carries the namespace.
func TestWriteStreamSSE_NamespaceToolCallsKeepOrderAndIdentity(t *testing.T) {
	t.Parallel()
	const body = `{
		"model":"gpt-4o-mini",
		"tools":[
			{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"close_agent","parameters":{"type":"object"}}]},
			{"type":"function","name":"exec_command","parameters":{"type":"object"}}
		],
		"input":"hi"
	}`
	decoded, err := DecodeCreateRequest([]byte(body), DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "multi_agent_v1__close_agent"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"target":"a"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_2", ToolName: "exec_command"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_2", Delta: `{"cmd":"ls"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_2"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := WriteStreamSSE(t.Context(), rec, decoded.Call, es, EncodeOptions{
		ResponseID:     "resp_order",
		CreatedAt:      1715620000,
		namespaceTools: decoded.namespaceTools,
	}); err != nil {
		t.Fatal(err)
	}

	var done []map[string]any
	var completed []map[string]any
	for _, frame := range testkit.ParseRecorderSSE(rec) {
		if frame.Data == "" || frame.Data == "[DONE]" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(frame.Data), &ev); err != nil {
			t.Fatal(err)
		}
		switch ev["type"] {
		case "response.output_item.done":
			item, _ := ev["item"].(map[string]any)
			if item != nil && item["type"] == "function_call" {
				done = append(done, item)
			}
		case "response.completed":
			resp, _ := ev["response"].(map[string]any)
			out, _ := resp["output"].([]any)
			for _, raw := range out {
				item, _ := raw.(map[string]any)
				if item != nil && item["type"] == "function_call" {
					completed = append(completed, item)
				}
			}
		}
	}
	if len(done) != 2 || len(completed) != 2 {
		t.Fatalf("done=%d completed=%d, want 2 each", len(done), len(completed))
	}
	want := []struct {
		name, namespace, callID, args string
	}{
		{name: "close_agent", namespace: "multi_agent_v1", callID: "call_1", args: `{"target":"a"}`},
		{name: "exec_command", namespace: "", callID: "call_2", args: `{"cmd":"ls"}`},
	}
	for i, w := range want {
		for _, got := range []map[string]any{done[i], completed[i]} {
			if got["name"] != w.name || got["call_id"] != w.callID || got["arguments"] != w.args {
				t.Fatalf("item %d = %+v, want name=%q call_id=%q arguments=%q", i, got, w.name, w.callID, w.args)
			}
			namespace, _ := got["namespace"].(string)
			if namespace != w.namespace {
				t.Fatalf("item %d namespace = %q, want %q", i, namespace, w.namespace)
			}
		}
	}
}

// TestWriteNonStreamJSON_HistoryOnlyNamespaceProjectionRestoresIdentity pins the
// continuity case: a namespaced history item whose declaration is absent from
// this request still gets a reversible mapping, so a model call to that name is
// restored instead of surfacing the flat projection to the client.
func TestWriteNonStreamJSON_HistoryOnlyNamespaceProjectionRestoresIdentity(t *testing.T) {
	t.Parallel()
	const body = `{
		"model":"gpt-4o-mini",
		"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}}],
		"input":[
			{"type":"function_call","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":"ok"}
		]
	}`
	decoded, err := DecodeCreateRequest([]byte(body), DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	es := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_h", ToolName: "multi_agent_v1__close_agent"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_h", Delta: `{"target":"x"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_h"},
		{Kind: aipapi.EventResponseFinished},
	})
	rec := httptest.NewRecorder()
	if err := WriteNonStreamJSON(t.Context(), rec, decoded.Call, es, EncodeOptions{
		ResponseID:     "resp_history_only",
		CreatedAt:      1715620000,
		namespaceTools: decoded.namespaceTools,
	}); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, item := range resp.Output {
		if item["type"] != "function_call" {
			continue
		}
		if item["name"] != "close_agent" || item["namespace"] != "multi_agent_v1" || item["call_id"] != "call_h" || item["arguments"] != `{"target":"x"}` {
			t.Fatalf("history-only projected call was not restored: %+v", item)
		}
		return
	}
	t.Fatalf("no function_call output: %+v", resp.Output)
}
