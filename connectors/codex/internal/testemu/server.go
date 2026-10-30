package testemu

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

type Config struct {
	Token      string
	OutputText string
}

type CapturedRequest struct {
	Authorization string
}

type Server struct {
	cfg    Config
	mu     sync.Mutex
	latest CapturedRequest
}

func New(cfg Config) *Server { return &Server{cfg: cfg} }

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		s.mu.Lock()
		s.latest = CapturedRequest{Authorization: auth}
		s.mu.Unlock()
		if s.cfg.Token != "" && auth != "Bearer "+s.cfg.Token {
			http.Error(w, "missing or invalid bearer", http.StatusUnauthorized)
			return
		}
		_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 10<<20))
		text := s.cfg.OutputText
		if text == "" {
			text = "ok"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`{"type":"response.created","sequence_number":0,"response":{"id":"resp_codex_ref","object":"response","created_at":1715620000,"status":"in_progress","model":"gpt-5.3-codex-spark"}}`,
			fmt.Sprintf(`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_codex_ref","output_index":0,"content_index":0,"delta":%q}`, text),
			fmt.Sprintf(`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_codex_ref","object":"response","created_at":1715620000,"status":"completed","model":"gpt-5.3-codex-spark","output":[{"type":"message","id":"msg_codex_ref","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}}`, text),
		}
		for _, raw := range frames {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", jsonType(raw), raw)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
}

func (s *Server) LatestRequest() CapturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

func jsonType(raw string) string {
	const key = `"type":"`
	_, after, ok := strings.Cut(raw, key)
	if !ok {
		return "message"
	}
	rest := after
	before, _, ok := strings.Cut(rest, "\"")
	if !ok {
		return "message"
	}
	return before
}
