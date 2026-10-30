package stdhttp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// frontendLogRecorder captures the log records a mounted frontend emits.
type frontendLogRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *frontendLogRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *frontendLogRecorder) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *frontendLogRecorder) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *frontendLogRecorder) WithGroup(string) slog.Handler      { return h }

func (h *frontendLogRecorder) hasError(message string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level == slog.LevelError && r.Message == message {
			return true
		}
	}
	return false
}

// TestMountBundledFrontends_ReportsInternalFailuresThroughHostLogger proves
// the composition hands its host logger to the mounted frontend: a frontend
// without a logger dropped every internal failure, so an operator never saw
// why a request failed.
func TestMountBundledFrontends_ReportsInternalFailuresThroughHostLogger(t *testing.T) {
	t.Parallel()
	recorder := &frontendLogRecorder{}
	mux := http.NewServeMux()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", nil)
	if err := MountBundledFrontends(MountBundledFrontendsInput{
		Mux: mux,
		Frontends: HTTPFrontendInput{
			Executor:             ex,
			DefaultRouteSelector: "stub:claude",
			Plugins:              []config.PluginConfig{{ID: anthropic.ID, Enabled: true}},
			Registry:             reg,
			Log:                  slog.New(recorder),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// A body that is valid JSON but violates the protocol fails inside the
	// frontend, which is the failure class an operator must see.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Fatalf("malformed request was not rejected: status=%d", rec.Code)
	}
	if !recorder.hasError("decode request failed") {
		t.Fatal("mounted frontend did not report its internal failure through the host logger")
	}
}
