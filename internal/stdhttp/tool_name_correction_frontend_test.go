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

	var attempts atomic.Int32
	var mu sync.Mutex
	var requestBodies [][]byte
	backend := httptest.NewServer(openaichat.NewHandler(openaichat.Config{
		AllowMissingBearer: true,
		OnRequestBody: func(body []byte) {
			mu.Lock()
			requestBodies = append(requestBodies, append([]byte(nil), body...))
			mu.Unlock()
		},
		Responder: func(openaichat.Request) openaichat.Response {
			if attempts.Add(1) == 1 {
				return openaichat.Response{SSE: chatToolCallSSE("b1", "call_b1", inventedName, `{"q":"x"}`)}
			}
			return openaichat.Response{SSE: chatToolCallSSE("b2", "call_b2", validName, `{"location":"NYC"}`)}
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
	h := startDogfoodHarness(t, configPath)

	body := `{"model":"invented:model-a","stream":true,"messages":[{"role":"user","content":"weather?"}],` +
		`"tools":[{"type":"function","function":{"name":"get_weather","description":"weather lookup",` +
		`"parameters":{"type":"object","properties":{"location":{"type":"string"}}}}}]}`
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
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("client status = %d, body = %s", resp.StatusCode, raw)
	}

	// The scripted backend answered B1 and then the hidden correction request,
	// which must carry the correction message that quotes the invented name.
	if got := attempts.Load(); got != 2 {
		t.Fatalf("backend attempts = %d, want 2; wire = %s", got, raw)
	}
	mu.Lock()
	bodies := requestBodies
	mu.Unlock()
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
