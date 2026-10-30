package opencode_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// SyntheticAnthropicAPIKey is a placeholder Anthropic-shaped API key for connector
// tests that talk to the local HTTP emulator. It is not a production or customer credential.
const SyntheticAnthropicAPIKey = "sk-ant-test" // #nosec G101 -- security: documented synthetic fixture only.

// RequestCapture records the last upstream request observed by NewFlavorServer.
// Access is concurrency-safe.
type RequestCapture struct {
	mu              sync.Mutex
	Path            string
	Authorization   string
	AnthropicAPIKey string
	GoogleAPIKey    string
	OpenCodeSession string
	Body            []byte
}

func (c *RequestCapture) record(r *http.Request, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Path = r.URL.Path
	c.Authorization = r.Header.Get("Authorization")
	c.AnthropicAPIKey = r.Header.Get("x-api-key")
	c.GoogleAPIKey = r.Header.Get("x-goog-api-key")
	c.OpenCodeSession = r.Header.Get("x-opencode-session")
	c.Body = append(c.Body[:0], body...)
}

// NewFlavorServer returns a test-only HTTP emulator with static Chat Completions,
// Responses, Anthropic Messages, and Gemini wire fixtures. It does not simulate
// OpenCode routing or catalog logic.
func NewFlavorServer(t *testing.T, capture *RequestCapture) *httptest.Server {
	t.Helper()
	if capture == nil {
		capture = &RequestCapture{}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.record(r, body)
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/chat/completions"):
			writeChatFixture(w, body)
		case strings.HasSuffix(path, "/responses"):
			writeResponsesFixture(w, body)
		case strings.HasSuffix(path, "/messages"):
			writeAnthropicFixture(w, body)
		case strings.Contains(path, "streamGenerateContent"):
			writeGeminiStreamFixture(w)
		case strings.Contains(path, "generateContent"):
			writeGeminiJSONFixture(w)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func writeChatFixture(w http.ResponseWriter, body []byte) {
	if bytes.Contains(body, []byte(`"stream":true`)) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"wire\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"chat-stream-ok\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"chat-ns","object":"chat.completion","created":1715620000,"model":"wire","choices":[{"index":0,"message":{"role":"assistant","content":"chat-ns-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
}

func writeResponsesFixture(w http.ResponseWriter, body []byte) {
	if bytes.Contains(body, []byte(`"stream":true`)) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"responses-stream-ok\"}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"created_at\":1715620000,\"status\":\"completed\",\"model\":\"wire\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"resp_ns","object":"response","created_at":1715620000,"status":"completed","model":"wire","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"responses-ns-ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
}

func writeAnthropicFixture(w http.ResponseWriter, body []byte) {
	if bytes.Contains(body, []byte(`"stream":true`)) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: content_block_delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"anthropic-stream-ok\"}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"id":"msg_ns","type":"message","role":"assistant","model":"wire","content":[{"type":"text","text":"anthropic-ns-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
}

func writeGeminiJSONFixture(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"gemini-ns-ok"}]}}]}`)
}

func writeGeminiStreamFixture(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"gemini-stream-ok\"}]}}]}\n\n")
}
