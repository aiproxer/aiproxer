package stdhttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
)

// Spec: tool-name-correction Task 4 (requirements 1.1, 2.3): a frontend request
// whose backend invents a tool name must receive only the corrected call, on a
// single response, and never the invented name.
func TestToolNameCorrection_frontendReceivesOnlyCorrectedCall(t *testing.T) {
	t.Parallel()

	const (
		inventedName = "invented_tool"
		validName    = "get_weather"
	)

	s := startCorrectionStack(t, func(attempt int32) openaichat.Response {
		if attempt == 1 {
			return openaichat.Response{SSE: chatToolCallSSE("b1", "call_b1", inventedName, `{"q":"x"}`)}
		}
		return openaichat.Response{SSE: chatToolCallSSE("b2", "call_b2", validName, `{"location":"NYC"}`)}
	})

	body := `{"model":"invented:model-a","stream":true,"messages":[{"role":"user","content":"weather?"}],` +
		`"tools":[{"type":"function","function":{"name":"get_weather","description":"weather lookup",` +
		`"parameters":{"type":"object","properties":{"location":{"type":"string"}}}}}]}`
	status, raw := postChatCompletion(t, s.harness, body)
	if status != http.StatusOK {
		t.Fatalf("client status = %d, body = %s", status, raw)
	}

	// The scripted backend answered B1 and then the hidden correction request,
	// which must carry the correction message that quotes the invented name.
	if got := s.attempts.Load(); got != 2 {
		t.Fatalf("backend attempts = %d, want 2; wire = %s", got, raw)
	}
	bodies := s.requestBodies()
	if len(bodies) != 2 || !strings.Contains(string(bodies[1]), inventedName) {
		t.Fatalf("follow-up request must carry the correction message quoting %q; bodies = %d",
			inventedName, len(bodies))
	}

	calls, finishes := chatWireToolCalls(t, raw)
	if len(calls) != 1 || calls[0].name != validName || calls[0].args != `{"location":"NYC"}` {
		t.Fatalf("client tool calls = %+v, want only %s; wire = %s", calls, validName, raw)
	}
	if finishes != 1 {
		t.Fatalf("client finish reasons = %d, want 1; wire = %s", finishes, raw)
	}
	if strings.Contains(string(raw), inventedName) {
		t.Fatalf("client wire must never contain the invented name %q: %s", inventedName, raw)
	}
}

// Spec: tool-name-correction history rail for chat-completions clients. The
// client's history already called invented_tool, so a repeat is the only case
// the client has seen: the backend is called once and the call is delivered
// unchanged, never corrected.
func TestToolNameCorrection_frontendLegacyHistoryRepeatDeliversUnchanged(t *testing.T) {
	t.Parallel()

	const inventedName = "invented_tool"

	s := startCorrectionStack(t, func(int32) openaichat.Response {
		return openaichat.Response{SSE: chatToolCallSSE("b1", "call_b1", inventedName, `{"q":"y"}`)}
	})

	body := `{"model":"invented:model-a","stream":true,"messages":[` +
		`{"role":"user","content":"weather?"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_h1","type":"function","function":{"name":"invented_tool","arguments":"{\"q\":\"x\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_h1","content":"ok"},` +
		`{"role":"user","content":"again?"}],` +
		`"tools":[{"type":"function","function":{"name":"get_weather","description":"weather lookup",` +
		`"parameters":{"type":"object","properties":{"location":{"type":"string"}}}}}]}`
	status, raw := postChatCompletion(t, s.harness, body)
	if status != http.StatusOK {
		t.Fatalf("client status = %d, body = %s", status, raw)
	}
	if got := s.attempts.Load(); got != 1 {
		t.Fatalf("backend attempts = %d, want 1 (a repeated call is never corrected); wire = %s", got, raw)
	}
	calls, finishes := chatWireToolCalls(t, raw)
	if len(calls) != 1 || calls[0].name != inventedName || calls[0].args != `{"q":"y"}` {
		t.Fatalf("client tool calls = %+v, want %s delivered unchanged; wire = %s", calls, inventedName, raw)
	}
	if finishes != 1 {
		t.Fatalf("client finish reasons = %d, want 1; wire = %s", finishes, raw)
	}
}

// Spec: tool-name-correction history rail for the deprecated function_call
// field. The client's history already called invented_tool through function_call,
// so a repeat must be delivered unchanged, with the backend called once.
func TestToolNameCorrection_frontendLegacyFunctionCallHistoryRepeatDeliversUnchanged(t *testing.T) {
	t.Parallel()

	const inventedName = "invented_tool"

	s := startCorrectionStack(t, func(int32) openaichat.Response {
		return openaichat.Response{SSE: chatToolCallSSE("b1", "call_b1", inventedName, `{"q":"y"}`)}
	})

	body := `{"model":"invented:model-a","stream":true,"messages":[` +
		`{"role":"user","content":"weather?"},` +
		`{"role":"assistant","content":null,"function_call":{"name":"invented_tool","arguments":"{\"q\":\"x\"}"}},` +
		`{"role":"user","content":"again?"}],` +
		`"tools":[{"type":"function","function":{"name":"get_weather","description":"weather lookup",` +
		`"parameters":{"type":"object","properties":{"location":{"type":"string"}}}}}]}`
	status, raw := postChatCompletion(t, s.harness, body)
	if status != http.StatusOK {
		t.Fatalf("client status = %d, body = %s", status, raw)
	}
	if got := s.attempts.Load(); got != 1 {
		t.Fatalf("backend attempts = %d, want 1 (a repeated call is never corrected); wire = %s", got, raw)
	}
	calls, finishes := chatWireToolCalls(t, raw)
	if len(calls) != 1 || calls[0].name != inventedName || calls[0].args != `{"q":"y"}` {
		t.Fatalf("client tool calls = %+v, want %s delivered unchanged; wire = %s", calls, inventedName, raw)
	}
	if finishes != 1 {
		t.Fatalf("client finish reasons = %d, want 1; wire = %s", finishes, raw)
	}
}

// correctionStack is a dogfood frontend in front of a scripted chat-completions
// backend whose responder sees the 1-based attempt number.
type correctionStack struct {
	harness  dogfoodHarness
	attempts atomic.Int32
	mu       sync.Mutex
	bodies   [][]byte
}

func (s *correctionStack) requestBodies() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies
}

// startCorrectionStack starts the standard harness with the tool-name-correction
// feature enabled (one correction per turn) in front of a scripted backend.
func startCorrectionStack(t *testing.T, respond func(attempt int32) openaichat.Response) *correctionStack {
	t.Helper()
	s := &correctionStack{}
	backend := httptest.NewServer(openaichat.NewHandler(openaichat.Config{
		AllowMissingBearer: true,
		OnRequestBody: func(body []byte) {
			s.mu.Lock()
			s.bodies = append(s.bodies, append([]byte(nil), body...))
			s.mu.Unlock()
		},
		Responder: func(openaichat.Request) openaichat.Response {
			return respond(s.attempts.Add(1))
		},
	}))
	t.Cleanup(backend.Close)

	configPath := filepath.Join(t.TempDir(), "tool-name-correction.yaml")
	if err := os.WriteFile(configPath, fmt.Appendf(nil, `server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 2
  default_route: "invented:model-a"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - id: invented
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: invented
        base_url: %s
        models:
          source: inline
          items:
            - canonical_id: model-a
              native_id: model-a
  features:
    - id: tool-name-correction
      enabled: true
      config:
        max_corrections_per_turn: 1
`, backend.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	s.harness = startDogfoodHarness(t, configPath)
	return s
}

// postChatCompletion sends one streamed chat-completions request through the
// frontend and returns the status and the full client wire.
func postChatCompletion(t *testing.T, h dogfoodHarness, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.baseURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

// chatToolCallSSE scripts one streamed chat-completions tool call: the name
// arrives with the call framing and the arguments in a following chunk.
func chatToolCallSSE(id, callID, name, args string) string {
	return fmt.Sprintf(
		"data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":%q,\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n"+
			"data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":%q}}]},\"finish_reason\":null}]}\n\n"+
			"data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
			"data: [DONE]\n\n",
		id, callID, name, id, args, id)
}

type chatWireToolCall struct {
	name string
	args string
}

// chatWireToolCalls assembles the tool calls and counts the finish reasons the
// client wire carries.
func chatWireToolCalls(t *testing.T, raw []byte) ([]chatWireToolCall, int) {
	t.Helper()
	type toolCallDelta struct {
		Index    int `json:"index"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type chunk struct {
		Choices []struct {
			Delta struct {
				ToolCalls []toolCallDelta `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	byIndex := map[int]*chatWireToolCall{}
	var order []int
	finishes := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var c chunk
		if err := json.Unmarshal([]byte(payload), &c); err != nil {
			t.Fatalf("client wire chunk is not valid JSON: %v: %s", err, payload)
		}
		for _, choice := range c.Choices {
			if choice.FinishReason != nil {
				finishes++
			}
			for _, delta := range choice.Delta.ToolCalls {
				call := byIndex[delta.Index]
				if call == nil {
					call = &chatWireToolCall{}
					byIndex[delta.Index] = call
					order = append(order, delta.Index)
				}
				call.name += delta.Function.Name
				call.args += delta.Function.Arguments
			}
		}
	}
	sort.Ints(order)
	calls := make([]chatWireToolCall, 0, len(order))
	for _, index := range order {
		calls = append(calls, *byIndex[index])
	}
	return calls, finishes
}
