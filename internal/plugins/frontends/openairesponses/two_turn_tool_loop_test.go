package openairesponses_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

// loopBackendExecutor is the narrow executor view the frontend needs: every
// decoded call is opened on the real OpenResponses-compatible backend.
type loopBackendExecutor struct {
	backend execbackend.Backend
}

func (e *loopBackendExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	return e.backend.Open(ctx, *call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "loop", Model: "model-x"}})
}

func (e *loopBackendExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error {
	return nil
}

func (e *loopBackendExecutor) WallClock() func() time.Time { return nil }

var _ aipsdk.ExecutorView = (*loopBackendExecutor)(nil)

type loopUpstream struct {
	mu       sync.Mutex
	requests [][]byte
}

func (u *loopUpstream) last(t *testing.T) []byte {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.requests) == 0 {
		t.Fatal("upstream received no request")
	}
	return u.requests[len(u.requests)-1]
}

const loopFunctionCallSSE = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress","model":"model-x"}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"close_agent","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc_1","output_index":0,"delta":"{\"target\":\"nope\"}"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc_1","output_index":0,"name":"close_agent","arguments":"{\"target\":\"nope\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"close_agent","arguments":"{\"target\":\"nope\"}"}}

event: response.completed
data: {"type":"response.completed","sequence_number":5,"response":{"id":"resp_1","status":"completed","model":"model-x","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"close_agent","arguments":"{\"target\":\"nope\"}"}],"usage":{"input_tokens":5,"output_tokens":7,"total_tokens":12}}}

`

const loopMessageSSE = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_2","status":"in_progress","model":"model-x"}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_2","type":"message","status":"in_progress","role":"assistant","content":[]}}

event: response.content_part.added
data: {"type":"response.content_part.added","sequence_number":2,"item_id":"msg_2","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_2","output_index":0,"content_index":0,"delta":"loop done"}

event: response.output_text.done
data: {"type":"response.output_text.done","sequence_number":4,"item_id":"msg_2","output_index":0,"content_index":0,"text":"loop done"}

event: response.content_part.done
data: {"type":"response.content_part.done","sequence_number":5,"item_id":"msg_2","output_index":0,"content_index":0,"part":{"type":"output_text","text":"loop done"}}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"loop done"}]}}

event: response.completed
data: {"type":"response.completed","sequence_number":7,"response":{"id":"resp_2","status":"completed","model":"model-x","output":[{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"loop done"}]}],"usage":{"input_tokens":6,"output_tokens":2,"total_tokens":8}}}

`

// TestTwoTurnToolLoop_FunctionCallHistoryReachesOpenResponsesBackend drives two
// real streaming HTTP turns through the shipped frontend and the real
// OpenResponses-compatible backend. Turn two replays the exact function_call
// item the first turn returned plus its function_call_output; the assertion is
// the request the backend actually sent upstream, so a decoder that loses the
// tool identity or rewrites the argument document fails the loop.
func TestTwoTurnToolLoop_FunctionCallHistoryReachesOpenResponsesBackend(t *testing.T) {
	t.Parallel()

	upstream := &loopUpstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request: %v", err)
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		upstream.mu.Lock()
		upstream.requests = append(upstream.requests, body)
		turn := len(upstream.requests)
		upstream.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if turn == 1 {
			_, _ = io.WriteString(w, loopFunctionCallSSE)
			return
		}
		_, _ = io.WriteString(w, loopMessageSSE)
	}))
	t.Cleanup(srv.Close)

	var node yaml.Node
	if err := yaml.Unmarshal([]byte("backend_prefix: loop\nbase_url: "+srv.URL+"\ncapabilities: [streaming, tools, ordered_items]\n"), &node); err != nil {
		t.Fatal(err)
	}
	backend, err := openresponsescompat.Build("loop", node, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	h := &openairesponses.Handler{
		Exec:                 &loopBackendExecutor{backend: backend},
		DefaultRouteSelector: "loop:model-x",
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"loop"}),
	}
	front := httptest.NewServer(h)
	t.Cleanup(front.Close)

	tools := json.RawMessage(`[{"type":"function","name":"close_agent","parameters":{"type":"object","properties":{"target":{"type":"string"}}}}]`)
	turn1Body, err := json.Marshal(map[string]any{
		"model":  "loop:model-x",
		"stream": true,
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "close nope"}}},
		},
		"tools": tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn1 := postResponses(t, front.URL+"/v1/responses", turn1Body)
	if turn1.StatusCode != http.StatusOK {
		t.Fatalf("turn 1 status = %d body=%s", turn1.StatusCode, turn1.Body)
	}
	firstOutput := completedOutput(t, turn1.Body)
	if len(firstOutput) != 1 {
		t.Fatalf("turn 1 output = %s", turn1.Body)
	}
	var firstCall struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(firstOutput[0], &firstCall); err != nil {
		t.Fatal(err)
	}
	if firstCall.Type != "function_call" || firstCall.CallID != "call_1" || firstCall.Name != "close_agent" || firstCall.Arguments != `{"target":"nope"}` {
		t.Fatalf("turn 1 function_call = %+v", firstCall)
	}

	turn2Body, err := json.Marshal(map[string]any{
		"model":  "loop:model-x",
		"stream": true,
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "close nope"}}},
			json.RawMessage(firstOutput[0]),
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "err"},
		},
		"tools": tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn2 := postResponses(t, front.URL+"/v1/responses", turn2Body)
	if turn2.StatusCode != http.StatusOK {
		t.Fatalf("turn 2 status = %d body=%s", turn2.StatusCode, turn2.Body)
	}
	if !strings.Contains(string(turn2.Body), "loop done") {
		t.Fatalf("turn 2 body does not carry the backend text: %s", turn2.Body)
	}

	var providerReq struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(upstream.last(t), &providerReq); err != nil {
		t.Fatalf("upstream request: %v body=%s", err, upstream.last(t))
	}
	if len(providerReq.Input) != 3 {
		t.Fatalf("upstream input items = %d, want 3: %s", len(providerReq.Input), upstream.last(t))
	}
	if got := string(providerReq.Input[1]["type"]); got != `"function_call"` {
		t.Fatalf("input[1] type = %s", got)
	}
	if got := string(providerReq.Input[1]["call_id"]); got != `"call_1"` {
		t.Fatalf("input[1] call_id = %s", got)
	}
	if got := string(providerReq.Input[1]["name"]); got != `"close_agent"` {
		t.Fatalf("input[1] name = %s", got)
	}
	if got := string(providerReq.Input[1]["arguments"]); got != `"{\"target\":\"nope\"}"` {
		t.Fatalf("input[1] arguments = %s, want the client's exact argument document", got)
	}
	if got := string(providerReq.Input[2]["type"]); got != `"function_call_output"` {
		t.Fatalf("input[2] type = %s", got)
	}
	if got := string(providerReq.Input[2]["call_id"]); got != `"call_1"` {
		t.Fatalf("input[2] call_id = %s", got)
	}
	if got := string(providerReq.Input[2]["output"]); got != `"err"` {
		t.Fatalf("input[2] output = %s", got)
	}
}

// completedOutput extracts the output items of the terminal response.completed
// event from the frontend SSE body.
func completedOutput(t *testing.T, body []byte) []json.RawMessage {
	t.Helper()
	for frame := range strings.SplitSeq(string(body), "\n\n") {
		for line := range strings.SplitSeq(frame, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event struct {
				Type     string `json:"type"`
				Response struct {
					Output []json.RawMessage `json:"output"`
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
				t.Fatalf("SSE data is not JSON: %v", err)
			}
			if event.Type == "response.completed" {
				return event.Response.Output
			}
		}
	}
	t.Fatalf("SSE body has no response.completed event: %s", body)
	return nil
}

type responseHTTPResult struct {
	StatusCode int
	Body       []byte
}

func postResponses(t *testing.T, url string, body []byte) responseHTTPResult {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return responseHTTPResult{StatusCode: resp.StatusCode, Body: raw}
}
