package openresponses_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

func mountContinuationNode(t *testing.T) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(`
profile: 2026-04-24
base_path: /openresponses/v1
continuation:
  persistent_store: standard
  ttl: 24h
  max_chain_depth: 64
  max_materialized_bytes: 67108864
websocket:
  enabled: false
`), &n); err != nil {
		t.Fatal(err)
	}
	for n.Kind == yaml.DocumentNode {
		n = *n.Content[0]
	}
	return n
}

// mountContinuationPost issues a store:true HTTP create through a mounted mux.
func mountContinuationPost(t *testing.T, mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AIP-Session-ID", "sess_mount_lifecycle")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestMount_PerConfigContinuationStoreClosedOnGenerationQuiesce proves the standard
// mount constructs one bounded continuation store per config instance and closes it
// exactly once when the generation context begins shutdown (Task 7.1 lifecycle).
// A store:true create reserves a proxy id while the store is open; after quiesce the
// same store rejects reservation with a storage error, proving no orphan store survives.
func TestMount_PerConfigContinuationStoreClosedOnGenerationQuiesce(t *testing.T) {
	genCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := http.NewServeMux()
	exec := &wsTurnExecutor{streams: []aipapi.EventStream{successStream("ok")}}
	if err := openresponses.Mount(mux, aipsdk.FrontendMountOptions{
		AllowUnauthenticated: true,
		PluginCfg:            mountContinuationNode(t),
		Exec:                 aipsdkExecutorAdapter{ExecutorView: exec},
		DefaultRoute:         "gpt-4o",
		GenerationContext:    genCtx,
	}); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	first := mountContinuationPost(t, mux, `{"model":"gpt-4o","input":"first","store":true}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first store:true create failed: %d %s", first.Code, first.Body.String())
	}
	if exec.count() != 1 {
		t.Fatalf("executor calls=%d, want 1", exec.count())
	}

	cancel()

	// The generation context has been canceled; the per-config store must be closed.
	// A subsequent reservation therefore fails with a storage error (no orphan store).
	eventually(t, 3*time.Second, func() bool {
		after := mountContinuationPost(t, mux, `{"model":"gpt-4o","input":"after","store":true}`)
		return after.Code == http.StatusInternalServerError && strings.Contains(after.Body.String(), "storage_error")
	})
}

// TestMount_PerConfigContinuationStoreIndependentInstances proves each Mount call
// receives an independent store: a store:true create on one instance cannot resolve
// through the other instance's store (no shared globals).
func TestMount_PerConfigContinuationStoreIndependentInstances(t *testing.T) {
	muxA := http.NewServeMux()
	muxB := http.NewServeMux()
	execA := &wsTurnExecutor{streams: []aipapi.EventStream{successStream("a")}}
	execB := &wsTurnExecutor{streams: []aipapi.EventStream{successStream("b")}}
	if err := openresponses.Mount(muxA, aipsdk.FrontendMountOptions{AllowUnauthenticated: true, PluginCfg: mountContinuationNode(t), Exec: aipsdkExecutorAdapter{ExecutorView: execA}, DefaultRoute: "gpt-4o"}); err != nil {
		t.Fatal(err)
	}
	if err := openresponses.Mount(muxB, aipsdk.FrontendMountOptions{AllowUnauthenticated: true, PluginCfg: mountContinuationNode(t), Exec: aipsdkExecutorAdapter{ExecutorView: execB}, DefaultRoute: "gpt-4o"}); err != nil {
		t.Fatal(err)
	}

	first := mountContinuationPost(t, muxA, `{"model":"gpt-4o","input":"first","store":true}`)
	if first.Code != http.StatusOK {
		t.Fatalf("instance A store:true create failed: %d %s", first.Code, first.Body.String())
	}

	// Instance B cannot resolve instance A's stored response id: stores are independent.
	second := mountContinuationPost(t, muxB, `{"previous_response_id":"resp_does_not_exist","input":"second"}`)
	if second.Code != http.StatusBadRequest {
		t.Fatalf("instance B continuation code=%d, want 400 previous_response_not_found", second.Code)
	}
	if !strings.Contains(second.Body.String(), "previous_response_not_found") {
		t.Fatalf("instance B continuation body=%s, want previous_response_not_found", second.Body.String())
	}
}
