package frontend

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/systemone"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// recordingHandler captures the log records a mounted frontend emits so the
// test can assert on what the operator would actually see.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// hasError reports whether the handler captured an error-level record with the
// given message.
func (h *recordingHandler) hasError(message string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level == slog.LevelError && r.Message == message {
			return true
		}
	}
	return false
}

// TestBundledFrontends_ReportInternalFailuresThroughHostLogger proves every
// mounted frontend reports its own internal failures through the host logger.
// A frontend mounted without a logger dropped every one of those records, so
// an operator never saw why a request failed.
func TestBundledFrontends_ReportInternalFailuresThroughHostLogger(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mount funcFrontendMount
		path  string
	}{
		{"openai-responses", openairesponses.Mount, "/v1/responses"},
		{"openresponses", openresponses.Mount, "/openresponses/v1/responses"},
		{"openai-legacy", openailegacy.Mount, "/v1/chat/completions"},
		{"anthropic", anthropic.Mount, "/v1/messages"},
		{"gemini", gemini.Mount, "/v1beta/models/m:generateContent"},
		{"systemone", systemone.Mount, "/v1/systemone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handler := &recordingHandler{}
			mux := http.NewServeMux()
			if err := tc.mount(mux, aipsdk.FrontendMountOptions{
				Exec:                 &CapturingExecutor{},
				DefaultRoute:         "stub",
				AllowUnauthenticated: true,
				ContinuationStore:    aipcont.NewMemoryStore(),
				Log:                  slog.New(handler),
			}); err != nil {
				t.Fatalf("mount: %v", err)
			}
			// A body that is valid JSON but violates the protocol fails inside
			// the frontend, which is the failure class an operator must see.
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code < 400 {
				t.Fatalf("malformed request was not rejected: status=%d", rec.Code)
			}
			if !handler.hasError("decode request failed") {
				t.Fatal("frontend did not report its internal failure through the host logger")
			}
		})
	}
}
