package compatibleparity

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

// WireServer is a deterministic httptest for one family/scenario cell.
type WireServer struct {
	URL    string
	Server *httptest.Server
	mu     sync.Mutex
	auths  []string
	paths  []string
	bodies [][]byte
	reqN   int
}

// NewWireServer serves fixture provider payloads for family/scenario.
func NewWireServer(t *testing.T, family Family, scenario Scenario) *WireServer {
	t.Helper()
	ws := &WireServer{}
	ws.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ws.mu.Lock()
		ws.reqN++
		reqNum := ws.reqN
		ws.auths = append(ws.auths, r.Header.Get("Authorization"))
		if x := r.Header.Get("X-Api-Key"); x != "" {
			ws.auths[len(ws.auths)-1] = "x-api-key:" + x
		}
		ws.paths = append(ws.paths, r.URL.Path)
		ws.bodies = append(ws.bodies, append([]byte(nil), body...))
		ws.mu.Unlock()

		if scenario == ScenarioNoRetryAfterOutput && reqNum > 1 {
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, `{"error":{"message":"unexpected retry after output","type":"invalid_request_error"}}`)
			return
		}

		if scenario == ScenarioCancellation {
			<-r.Context().Done()
			return
		}
		if scenario == ScenarioError {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"parity-unauthorized","type":"invalid_request_error"}}`)
			return
		}
		writeParityPayload(w, r, family, scenario, body)
	}))
	t.Cleanup(ws.Server.Close)
	ws.URL = ws.Server.URL
	return ws
}

func (ws *WireServer) Auths() []string {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	out := make([]string, len(ws.auths))
	copy(out, ws.auths)
	return out
}

func (ws *WireServer) Paths() []string {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	out := make([]string, len(ws.paths))
	copy(out, ws.paths)
	return out
}

func (ws *WireServer) Bodies() [][]byte {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	out := make([][]byte, len(ws.bodies))
	for i, b := range ws.bodies {
		out[i] = append([]byte(nil), b...)
	}
	return out
}

func (ws *WireServer) RequestCount() int {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.reqN
}

func writeParityPayload(w http.ResponseWriter, r *http.Request, family Family, scenario Scenario, body []byte) {
	streaming := strings.Contains(string(body), `"stream":true`)
	if scenario == ScenarioPostOutputError || scenario == ScenarioNoRetryAfterOutput {
		writePostOutputFailure(w, family, streaming)
		return
	}
	switch family {
	case FamilyAnthropic:
		writeAnthropic(w, scenario, streaming)
	case FamilyOpenAIResponses:
		writeOpenAIResponses(w, scenario, streaming)
	default:
		writeOpenAIChat(w, scenario, streaming)
	}
}

func writePostOutputFailure(w http.ResponseWriter, family Family, streaming bool) {
	if !streaming {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"parity-post-output","type":"server_error"}}`)
		return
	}
	switch family {
	case FamilyAnthropic:
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"parity-partial-ok\"}}\n\n")
		_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"parity-post-output\"}}\n\n")
	case FamilyOpenAIResponses:
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"parity-partial-ok\"}\n\n")
		_, _ = io.WriteString(w, "event: error\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"parity-post-output\"}\n\n")
	default:
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"parity-partial-ok\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"parity-post-output\",\"type\":\"server_error\"}}\n\n")
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func writeOpenAIChat(w http.ResponseWriter, scenario Scenario, streaming bool) {
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		switch scenario {
		case ScenarioReasoning:
			_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning\":\"parity-think\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"parity-reasoned-ok\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n")
		case ScenarioTools:
			_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_parity\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\"}}]},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\\\"NYC\\\"}\"}}]},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		case ScenarioMultimodal:
			_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"parity-vision-ok\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		case ScenarioUsage, ScenarioTerminalOrdering:
			text := "parity-usage-ok"
			if scenario == ScenarioTerminalOrdering {
				text = "parity-terminal-ok"
			}
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", text)
			_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3,\"completion_tokens_details\":{\"reasoning_tokens\":4}}}\n\n")
		default:
			_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"parity-text-ok\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"parity\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-parity-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch scenario {
	case ScenarioReasoning:
		_, _ = io.WriteString(w, `{"id":"parity","object":"chat.completion","created":1,"model":"gpt-parity-test","choices":[{"index":0,"message":{"role":"assistant","content":"parity-reasoned-ok","reasoning":"parity-think"},"finish_reason":"stop"}]}`)
	case ScenarioTools:
		_, _ = io.WriteString(w, `{"id":"parity","object":"chat.completion","created":1,"model":"gpt-parity-test","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_parity","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]},"finish_reason":"tool_calls"}]}`)
	case ScenarioMultimodal:
		_, _ = io.WriteString(w, `{"id":"parity","object":"chat.completion","created":1,"model":"gpt-parity-test","choices":[{"index":0,"message":{"role":"assistant","content":"parity-vision-ok"},"finish_reason":"stop"}]}`)
	case ScenarioUsage, ScenarioTerminalOrdering:
		text := "parity-usage-ok"
		if scenario == ScenarioTerminalOrdering {
			text = "parity-terminal-ok"
		}
		_, _ = fmt.Fprintf(w, `{"id":"parity","object":"chat.completion","created":1,"model":"gpt-parity-test","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"completion_tokens_details":{"reasoning_tokens":4}}}`, text)
	default:
		_, _ = io.WriteString(w, `{"id":"parity","object":"chat.completion","created":1,"model":"gpt-parity-test","choices":[{"index":0,"message":{"role":"assistant","content":"parity-text-ok"},"finish_reason":"stop"}]}`)
	}
}

func writeOpenAIResponses(w http.ResponseWriter, scenario Scenario, streaming bool) {
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		switch scenario {
		case ScenarioReasoning:
			_, _ = io.WriteString(w, "event: response.completed\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_parity\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"gpt-parity-test\",\"output\":[{\"type\":\"reasoning\",\"id\":\"rs\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"parity-think\"}]},{\"type\":\"message\",\"id\":\"msg\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"parity-reasoned-ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n")
		case ScenarioTools:
			_, _ = io.WriteString(w, "event: response.completed\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_parity\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"gpt-parity-test\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc\",\"call_id\":\"call_parity\",\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"NYC\\\"}\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n")
		case ScenarioMultimodal:
			_, _ = io.WriteString(w, "event: response.completed\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_parity\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"gpt-parity-test\",\"output\":[{\"type\":\"message\",\"id\":\"msg\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"parity-vision-ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n")
		case ScenarioUsage, ScenarioTerminalOrdering:
			text := "parity-usage-ok"
			if scenario == ScenarioTerminalOrdering {
				text = "parity-terminal-ok"
			}
			_, _ = io.WriteString(w, "event: response.completed\n")
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_parity\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"gpt-parity-test\",\"output\":[{\"type\":\"message\",\"id\":\"msg\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n", text)
		default:
			_, _ = io.WriteString(w, "event: response.completed\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"resp_parity\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"gpt-parity-test\",\"output\":[{\"type\":\"message\",\"id\":\"msg\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"parity-text-ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch scenario {
	case ScenarioReasoning:
		_, _ = io.WriteString(w, `{"id":"resp_parity","object":"response","created_at":1,"status":"completed","model":"gpt-parity-test","output":[{"type":"reasoning","id":"rs","summary":[{"type":"summary_text","text":"parity-think"}]},{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"parity-reasoned-ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
	case ScenarioTools:
		_, _ = io.WriteString(w, `{"id":"resp_parity","object":"response","created_at":1,"status":"completed","model":"gpt-parity-test","output":[{"type":"function_call","id":"fc","call_id":"call_parity","name":"get_weather","arguments":"{\"city\":\"NYC\"}"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
	case ScenarioMultimodal:
		_, _ = io.WriteString(w, `{"id":"resp_parity","object":"response","created_at":1,"status":"completed","model":"gpt-parity-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"parity-vision-ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
	case ScenarioUsage, ScenarioTerminalOrdering:
		text := "parity-usage-ok"
		if scenario == ScenarioTerminalOrdering {
			text = "parity-terminal-ok"
		}
		_, _ = fmt.Fprintf(w, `{"id":"resp_parity","object":"response","created_at":1,"status":"completed","model":"gpt-parity-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`, text)
	default:
		_, _ = io.WriteString(w, `{"id":"resp_parity","object":"response","created_at":1,"status":"completed","model":"gpt-parity-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"parity-text-ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
	}
}

func writeAnthropic(w http.ResponseWriter, scenario Scenario, streaming bool) {
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_parity\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-parity-test\",\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		switch scenario {
		case ScenarioReasoning:
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"parity-think\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"parity-reasoned-ok\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		case ScenarioTools:
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_parity\",\"name\":\"get_weather\",\"input\":{}}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"NYC\\\"}\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		case ScenarioMultimodal:
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"parity-vision-ok\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		case ScenarioUsage, ScenarioTerminalOrdering:
			text := "parity-usage-ok"
			if scenario == ScenarioTerminalOrdering {
				text = "parity-terminal-ok"
			}
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			_, _ = fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", text)
			_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		default:
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"parity-text-ok\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		}
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch scenario {
	case ScenarioReasoning:
		_, _ = io.WriteString(w, `{"id":"msg_parity","type":"message","role":"assistant","model":"claude-parity-test","content":[{"type":"thinking","thinking":"parity-think","signature":"sig"},{"type":"text","text":"parity-reasoned-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
	case ScenarioTools:
		_, _ = io.WriteString(w, `{"id":"msg_parity","type":"message","role":"assistant","model":"claude-parity-test","content":[{"type":"tool_use","id":"call_parity","name":"get_weather","input":{"city":"NYC"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":2}}`)
	case ScenarioMultimodal:
		_, _ = io.WriteString(w, `{"id":"msg_parity","type":"message","role":"assistant","model":"claude-parity-test","content":[{"type":"text","text":"parity-vision-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
	case ScenarioUsage, ScenarioTerminalOrdering:
		text := "parity-usage-ok"
		if scenario == ScenarioTerminalOrdering {
			text = "parity-terminal-ok"
		}
		_, _ = fmt.Fprintf(w, `{"id":"msg_parity","type":"message","role":"assistant","model":"claude-parity-test","content":[{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`, text)
	default:
		_, _ = io.WriteString(w, `{"id":"msg_parity","type":"message","role":"assistant","model":"claude-parity-test","content":[{"type":"text","text":"parity-text-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}
}

func mustYAML(raw string) yaml.Node {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		panic(err)
	}
	return doc
}
