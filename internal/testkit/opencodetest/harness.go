package opencodetest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	refanthropic "github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
	refgemini "github.com/aiproxer/aiproxer/internal/refbackend/gemini"
)

type RequestCapture struct {
	mu              sync.Mutex
	Path            string
	Authorization   string
	AnthropicAPIKey string
	GoogleAPIKey    string
	Body            []byte
}

func (c *RequestCapture) record(r *http.Request, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Path = r.URL.Path
	c.Authorization = r.Header.Get("Authorization")
	c.AnthropicAPIKey = r.Header.Get("x-api-key")
	c.GoogleAPIKey = r.Header.Get("x-goog-api-key")
	c.Body = append(c.Body[:0], body...)
}

func (c *RequestCapture) ModelField(t *testing.T) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(c.Body, &payload); err != nil {
		t.Fatalf("decode body: %v body=%s", err, string(c.Body))
	}
	raw, ok := payload["model"]
	if !ok {
		t.Fatalf("missing model field in body=%s", string(c.Body))
	}
	var model string
	if err := json.Unmarshal(raw, &model); err != nil {
		t.Fatalf("decode model: %v", err)
	}
	return model
}

func NewFlavorServer(t *testing.T, capture *RequestCapture) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.record(r, body)
		streaming := bytes.Contains(body, []byte(`"stream":true`))
		if streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chat-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"wire\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"chat-stream-ok\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat-ns","object":"chat.completion","created":1715620000,"model":"wire","choices":[{"index":0,"message":{"role":"assistant","content":"chat-ns-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
	})
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.record(r, body)
		streaming := bytes.Contains(body, []byte(`"stream":true`))
		if streaming {
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
	})
	mux.Handle("/v1/messages", refanthropic.NewHandler(refanthropic.Config{
		OnRequestBody: func(body []byte) {
			capture.mu.Lock()
			capture.Body = append(capture.Body[:0], body...)
			capture.mu.Unlock()
		},
	}))
	mux.Handle("/", refgemini.NewHandler(refgemini.Config{
		OnRequestBody: func(body []byte) {
			capture.mu.Lock()
			capture.Body = append(capture.Body[:0], body...)
			capture.mu.Unlock()
		},
	}))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "generateContent") || strings.Contains(r.URL.Path, "streamGenerateContent") {
			capture.mu.Lock()
			capture.Path = r.URL.Path
			capture.GoogleAPIKey = r.Header.Get("x-goog-api-key")
			capture.mu.Unlock()
		}
		if strings.HasSuffix(r.URL.Path, "/messages") {
			capture.mu.Lock()
			capture.Path = r.URL.Path
			capture.AnthropicAPIKey = r.Header.Get("x-api-key")
			capture.mu.Unlock()
		}
		mux.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}
