package anthropic

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

type EmulatorConfig struct {
	RequireAuth      bool
	ModelsJSON       string
	ForcedStatus     int
	ForcedBody       string
	MessagesSSE      string
	MessagesJSON     string
	OnRequestHeaders func(http.Header)
	OnRequestBody    func([]byte)
}

type Emulator struct {
	cfg EmulatorConfig
}

func NewEmulator(cfg EmulatorConfig) *Emulator {
	return &Emulator{cfg: cfg}
}

func (e *Emulator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if e.cfg.OnRequestHeaders != nil {
		e.cfg.OnRequestHeaders(r.Header)
	}

	body, _ := io.ReadAll(r.Body)
	if e.cfg.OnRequestBody != nil {
		e.cfg.OnRequestBody(body)
	}

	if e.cfg.RequireAuth {
		key := r.Header.Get("x-api-key")
		if key == "" {
			bearer := r.Header.Get("Authorization")
			key = strings.TrimPrefix(bearer, "Bearer ")
		}
		if strings.TrimSpace(key) == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid api key"}}`))
			return
		}
	}

	if e.cfg.ForcedStatus > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(e.cfg.ForcedStatus)
		if e.cfg.ForcedBody != "" {
			_, _ = w.Write([]byte(e.cfg.ForcedBody))
		} else {
			_, _ = w.Write(fmt.Appendf(nil, `{"type":"error","error":{"type":"api_error","message":"error %d"}}`, e.cfg.ForcedStatus))
		}
		return
	}

	path := r.URL.Path
	if strings.HasSuffix(path, "/models") {
		w.Header().Set("Content-Type", "application/json")
		if e.cfg.ModelsJSON != "" {
			_, _ = w.Write([]byte(e.cfg.ModelsJSON))
		} else {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-3-5-sonnet-20241022"},{"id":"claude-haiku-4-5-20251001"}]}`))
		}
		return
	}

	if strings.HasSuffix(path, "/messages") {
		isStreaming := strings.Contains(string(body), `"stream":true`)
		if isStreaming {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if e.cfg.MessagesSSE != "" {
				_, _ = w.Write([]byte(e.cfg.MessagesSSE))
			} else {
				defaultSSE := strings.Join([]string{
					`event: message_start`,
					`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
					``,
					`event: content_block_start`,
					`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
					``,
					`event: content_block_delta`,
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
					``,
					`event: content_block_stop`,
					`data: {"type":"content_block_stop","index":0}`,
					``,
					`event: message_delta`,
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`,
					``,
					`event: message_stop`,
					`data: {"type":"message_stop"}`,
					``,
					``,
				}, "\n")
				_, _ = w.Write([]byte(defaultSSE))
			}
			if ok {
				flusher.Flush()
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if e.cfg.MessagesJSON != "" {
			_, _ = w.Write([]byte(e.cfg.MessagesJSON))
		} else {
			defaultJSON := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
			_, _ = w.Write([]byte(defaultJSON))
		}
		return
	}

	http.NotFound(w, r)
}
