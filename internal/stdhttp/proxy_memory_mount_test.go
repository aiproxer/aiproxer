package stdhttp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

// proxyMemoryRecordingHandler records every path that reaches the projected
// proxy-memory HTTP handler, so the mount test can prove the mux forwarded the
// request to the projected handler instead of answering with a fallback.
type proxyMemoryRecordingHandler struct {
	mu    sync.Mutex
	paths []string
}

func (h *proxyMemoryRecordingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.paths = append(h.paths, r.URL.Path)
	h.mu.Unlock()
	_, _ = w.Write([]byte("proxy-memory-handler\n"))
}

func (h *proxyMemoryRecordingHandler) recordedPaths() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.paths...)
}

func TestComposeStandardHTTP_ProxyMemoryRoutesReachProjectedHandler(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneMountConfig(false)
	recording := &proxyMemoryRecordingHandler{}
	ex := runtime.TestExecutor()
	in := StandardHTTPInput{
		Core:       HTTPCoreInput{Executor: ex},
		Frontends:  frontendInputForTest(cfg, ex, pluginreg.NewRegistry()),
		Operations: HTTPOperationsInput{ProxyMemory: recording},
	}
	h, err := ComposeStandardHTTP(context.Background(), cfg, slog.Default(), in)
	if err != nil {
		t.Fatalf("ComposeStandardHTTP: %v", err)
	}

	for _, path := range []string{"/v1/aip/memory", "/v1/aip/memory/x"} {
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, path, nil))
		if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "proxy-memory-handler") {
			t.Fatalf("GET %s did not reach the projected handler: status=%d body=%q", path, rw.Code, rw.Body.String())
		}
	}
	want := []string{"/v1/aip/memory", "/v1/aip/memory/x"}
	if got := recording.recordedPaths(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("recorded paths = %v, want %v", got, want)
	}
}

func TestComposeStandardHTTP_ProxyMemoryUnmountedWithoutHandler(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneMountConfig(false)
	ex := runtime.TestExecutor()
	in := StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, pluginreg.NewRegistry()),
	}
	h, err := ComposeStandardHTTP(context.Background(), cfg, slog.Default(), in)
	if err != nil {
		t.Fatalf("ComposeStandardHTTP: %v", err)
	}

	for _, path := range []string{"/v1/aip/memory", "/v1/aip/memory/x"} {
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, path, nil))
		if rw.Code != http.StatusNotFound || strings.TrimSpace(rw.Body.String()) != "404 page not found" {
			t.Fatalf("GET %s without a projected handler: status=%d body=%q, want the mux 404", path, rw.Code, rw.Body.String())
		}
	}
}
