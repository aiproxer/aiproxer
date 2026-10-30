package runtimebundle_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// TestDefaultRoute_LargePayloadCandidateKeepsTheDefaultModelBinding proves the
// large-payload lane keeps the generation's default route binding.
//
// The large-payload candidate resolves the route in the profile proof, so the
// route's origin travels with the proof. A candidate that declines to the
// canonical lane then replays the same route, and the request still addresses
// the model the backend published: an inventory refresh that withdrew the model
// must not make the proxy send the canonical id as the wire model.
func TestDefaultRoute_LargePayloadCandidateKeepsTheDefaultModelBinding(t *testing.T) {
	t.Parallel()

	var (
		chatCalls     atomic.Int32
		lastWireModel atomic.Pointer[string]
		published     atomic.Pointer[string]
	)
	published.Store(new("good-a"))

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			text := string(body)
			lastWireModel.Store(&text)
		}
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":%q}]}`, *published.Load())
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			chatCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-route\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	spoolDir := t.TempDir()
	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
  large_payload_fast_path:
    enabled: true
    threshold_bytes: 4096
    memory_spool_bytes: 32768
    max_inflight_spool_bytes: 1048576
    max_semantic_fact_bytes: 65536
    spool_dir: %q
routing:
  max_attempts: 1
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
plugins:
  frontends:
    - id: openai-legacy
      enabled: true
      config: {}
  backends:
    - kind: custom-openai-legacy-compatible
      id: wire-backend
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
`, spoolDir, upstream.URL+"/v1")

	cfgPath := filepath.Join(t.TempDir(), "large-payload-default-route-e2e.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	// The upstream withdraws the model the generation's default route names.
	published.Store(new("good-b"))
	registry := runtimebundle.HostModelRegistryRuntime(host)
	if registry == nil {
		t.Fatal("expected the active generation to own a model registry runtime")
	}
	registry.RunRefresh(t.Context())

	// The body exceeds the large-payload threshold, so the request runs the
	// candidate lane and replays it on the canonical lane.
	body := `{"model":"plain-model","stream":true,"messages":[{"role":"user","content":"` +
		strings.Repeat("a", 8<<10) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	host.HTTPHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if chatCalls.Load() == 0 {
		t.Fatal("the derived default route served no request")
	}
	sent := lastWireModel.Load()
	if sent == nil {
		t.Fatal("the backend received no request body")
	}
	if !strings.Contains(*sent, `"model":"good-a"`) {
		t.Fatalf("upstream body = %s want the model id the backend published", *sent)
	}
	if strings.Contains(*sent, "wire-backend/good-a") {
		t.Fatalf("upstream body = %s: the canonical id must not be sent as the wire model", *sent)
	}
}
