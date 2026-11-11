package openairesponses_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	front "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const namespaceRoundTripBody = `{
	"model":"gpt-4o-mini",
	"stream":%s,
	"tools":[
		{"type":"namespace","name":"multi_agent_v1","description":"NS-DESC","tools":[
			{"type":"function","name":"close_agent","description":"CLOSE-DESC","parameters":{"type":"object","properties":{"target":{"type":"string"}}}}
		]},
		{"type":"function","name":"exec_command","description":"RUN","parameters":{"type":"object"}}
	],
	"tool_choice":"auto",
	"input":[
		{"type":"message","role":"user","content":"hi"},
		{"type":"function_call","id":"fc_1","call_id":"c1","name":"close_agent","namespace":"multi_agent_v1","arguments":"{\"target\":\"nope\"}"},
		{"type":"function_call_output","call_id":"c1","output":"err"},
		{"type":"message","role":"user","content":"again"}
	]
}`

func postNamespaceResponses(t *testing.T, srv *httptest.Server, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func capturedCall(t *testing.T, capture *sync.Map) aipapi.Call {
	t.Helper()
	v, ok := capture.Load("last")
	if !ok {
		t.Fatal("the stub executor did not capture a canonical call")
	}
	call, ok := v.(aipapi.Call)
	if !ok {
		t.Fatalf("captured value type %T", v)
	}
	return call
}

func requireNamespaceWireItem(t *testing.T, item map[string]any, wantArguments bool) {
	t.Helper()
	if item["type"] != "function_call" {
		t.Fatalf("item type = %v, want function_call (%+v)", item["type"], item)
	}
	if got := item["name"]; got != "close_agent" {
		t.Fatalf("wire function name = %v, want original bare member close_agent", got)
	}
	if got := item["namespace"]; got != "multi_agent_v1" {
		t.Fatalf("wire namespace = %v, want multi_agent_v1", got)
	}
	if got := item["call_id"]; got != "call_stub1" {
		t.Fatalf("wire call_id = %v, want call_stub1", got)
	}
	if wantArguments {
		if got := item["arguments"]; got != `{"q":"ok"}` {
			t.Fatalf("wire arguments = %v, want the model-authored bytes", got)
		}
	}
}

func TestIntegration_NamespaceToolsRoundTrip(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var capture sync.Map
			caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
			ex := testkit.NewStubExecutor(t, caps, "unused", &capture)
			h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
			mux := http.NewServeMux()
			mux.Handle("/v1/responses", h)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			status, body := postNamespaceResponses(t, srv, strings.Replace(namespaceRoundTripBody, "%s", boolLiteral(stream), 1))
			if status != http.StatusOK {
				t.Fatalf("status = %d, body = %s", status, body)
			}

			call := capturedCall(t, &capture)
			if len(call.Tools) != 2 || call.Tools[0].Name != "multi_agent_v1__close_agent" || call.Tools[1].Name != "exec_command" {
				t.Fatalf("canonical tools = %+v, want the projected namespace member first", call.Tools)
			}
			if call.Tools[0].Description != "NS-DESC\n\nCLOSE-DESC" {
				t.Fatalf("canonical description = %q", call.Tools[0].Description)
			}
			if len(call.Messages) != 4 {
				t.Fatalf("canonical history = %d messages, want 4", len(call.Messages))
			}
			callPart := call.Messages[1].Parts[0]
			if callPart.ToolCallID != "c1" || callPart.ToolName != "multi_agent_v1__close_agent" || string(callPart.Content) != `{"target":"nope"}` {
				t.Fatalf("canonical history call = %+v", callPart)
			}
			resultPart := call.Messages[2].Parts[0]
			if resultPart.ToolCallID != "c1" || string(resultPart.Content) != `"err"` {
				t.Fatalf("canonical history result = %+v", resultPart)
			}

			if !stream {
				var resp struct {
					Output []map[string]any `json:"output"`
				}
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Fatalf("nonstream response JSON: %v (%s)", err, body)
				}
				var fc map[string]any
				for _, item := range resp.Output {
					if item["type"] == "function_call" {
						fc = item
						break
					}
				}
				if fc == nil {
					t.Fatalf("nonstream output has no function_call: %+v", resp.Output)
				}
				requireNamespaceWireItem(t, fc, true)
				return
			}

			var added, done, completed map[string]any
			for _, frame := range testkit.ParseSSEBody(body) {
				if frame.Data == "" || frame.Data == "[DONE]" {
					continue
				}
				var ev map[string]any
				if err := json.Unmarshal([]byte(frame.Data), &ev); err != nil {
					t.Fatalf("SSE JSON: %v (%s)", err, frame.Data)
				}
				typ, _ := ev["type"].(string)
				switch typ {
				case "response.output_item.added":
					item, _ := ev["item"].(map[string]any)
					if item != nil && item["type"] == "function_call" {
						added = item
					}
				case "response.output_item.done":
					item, _ := ev["item"].(map[string]any)
					if item != nil && item["type"] == "function_call" {
						done = item
					}
				case "response.completed":
					resp, _ := ev["response"].(map[string]any)
					out, _ := resp["output"].([]any)
					for _, rawItem := range out {
						item, _ := rawItem.(map[string]any)
						if item != nil && item["type"] == "function_call" {
							completed = item
							break
						}
					}
				}
			}
			if added == nil || done == nil || completed == nil {
				t.Fatalf("streaming function_call output items missing: added=%+v done=%+v completed=%+v", added, done, completed)
			}
			requireNamespaceWireItem(t, added, false)
			requireNamespaceWireItem(t, done, true)
			requireNamespaceWireItem(t, completed, true)
		})
	}
}

func boolLiteral(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// TestIntegration_NamespaceStrictRefusesAtHandler pins the config path: the
// operator setting reaches decode and a refused request never opens a backend.
func TestIntegration_NamespaceStrictRefusesAtHandler(t *testing.T) {
	t.Parallel()
	var capture sync.Map
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	ex := testkit.NewStubExecutor(t, caps, "unused", &capture)
	h := &front.Handler{
		Exec:                 ex,
		DefaultRouteSelector: "stub:gpt-4o-mini",
		Config:               front.Config{NamespaceTools: "reject"},
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	const body = `{"model":"gpt-4o-mini","tools":[{"type":"namespace","name":"multi_agent_v1","tools":[{"type":"function","name":"close_agent","parameters":{"type":"object"}}]}],"input":"hi"}`
	status, resp := postNamespaceResponses(t, srv, body)
	if status != http.StatusBadRequest {
		t.Fatalf("strict namespace refusal status = %d, body = %s", status, resp)
	}
	if _, ok := capture.Load("last"); ok {
		t.Fatal("a refused strict request must never reach a backend")
	}
}
