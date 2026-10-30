package openaicompat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// EmulatorConfig tunes the deterministic HTTP emulator used by support and
// connector tests. It never contacts live providers.
type EmulatorConfig struct {
	RequireBearer          bool
	OnRequestBody          func([]byte)
	OnRequestHeaders       func(http.Header)
	ChatStreamSSE          string
	ChatNonStreamJSON      string
	ResponsesStreamSSE     string
	ResponsesNonStreamJSON string
	ModelsJSON             string
	ForcedStatus           int
	ForcedBody             string
}

// NewEmulator returns an http.Handler that emulates OpenAI-compatible endpoints.
func NewEmulator(cfg EmulatorConfig) http.Handler {
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.RequireBearer && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, `{"error":{"message":"missing bearer","type":"auth"}}`, http.StatusUnauthorized)
			return
		}
		if cfg.OnRequestHeaders != nil {
			cfg.OnRequestHeaders(r.Header.Clone())
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, defaultMaxBodyBytes))
		if cfg.OnRequestBody != nil {
			mu.Lock()
			cfg.OnRequestBody(body)
			mu.Unlock()
		}
		if cfg.ForcedStatus != 0 {
			w.WriteHeader(cfg.ForcedStatus)
			if cfg.ForcedBody != "" {
				_, _ = w.Write([]byte(cfg.ForcedBody))
			} else {
				_, _ = w.Write([]byte(`{"error":{"message":"forced","type":"server"}}`))
			}
			return
		}
		path := r.URL.Path
		if r.Method == http.MethodGet && strings.HasSuffix(path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			if cfg.ModelsJSON != "" {
				_, _ = w.Write([]byte(cfg.ModelsJSON))
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"emu-model","owned_by":"emu"}]}`))
			return
		}
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		stream := bytes.Contains(body, []byte(`"stream":true`))
		isChat := strings.HasSuffix(path, "/chat/completions")
		isResp := strings.HasSuffix(path, "/responses")
		if !isChat && !isResp {
			http.NotFound(w, r)
			return
		}
		if isChat {
			if stream {
				writeSSE(w, cfg.ChatStreamSSE, defaultChatSSE())
			} else {
				writeJSON(w, cfg.ChatNonStreamJSON, defaultChatJSON())
			}
			return
		}
		if stream {
			writeSSE(w, cfg.ResponsesStreamSSE, defaultResponsesSSE())
		} else {
			writeJSON(w, cfg.ResponsesNonStreamJSON, defaultResponsesJSON())
		}
	})
}

func writeJSON(w http.ResponseWriter, override, def string) {
	w.Header().Set("Content-Type", "application/json")
	if override != "" {
		_, _ = w.Write([]byte(override))
		return
	}
	_, _ = w.Write([]byte(def))
}

func writeSSE(w http.ResponseWriter, override, def string) {
	w.Header().Set("Content-Type", "text/event-stream")
	payload := def
	if override != "" {
		payload = override
	}
	_, _ = w.Write([]byte(payload))
}

func defaultChatJSON() string {
	return `{"id":"chatcmpl-emu","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
}

func defaultChatSSE() string {
	return strings.Join([]string{
		`data: {"id":"chatcmpl-emu","choices":[{"delta":{"content":"hel"}}]}`,
		`data: {"id":"chatcmpl-emu","choices":[{"delta":{"content":"lo"}}]}`,
		`data: {"id":"chatcmpl-emu","choices":[{}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`data: [DONE]`,
		"",
	}, "\n")
}

func defaultResponsesJSON() string {
	return `{"id":"resp-emu","output":[{"content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`
}

func defaultResponsesSSE() string {
	b, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "delta": "hello"})
	return "data: " + string(b) + "\n\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
}
