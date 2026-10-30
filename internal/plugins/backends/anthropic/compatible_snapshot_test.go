package anthropic_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

const customAnthropicSuccessSSE = "event: message_start\ndata: " +
	`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"custom-anthropic-ok"}}` + "\n\n" +
	"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"

// customAnthropicEmulator fails scripted x-api-key values with status/headers
// and serves a success SSE stream to every other key.
type customAnthropicEmulator struct {
	mu         sync.Mutex
	seen       []string
	fail       map[string]int
	retryAfter map[string]string
	attempts   atomic.Int32
}

func (e *customAnthropicEmulator) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.attempts.Add(1)
		key := r.Header.Get("x-api-key")
		e.mu.Lock()
		e.seen = append(e.seen, key)
		e.mu.Unlock()
		if status, ok := e.fail[key]; ok {
			if ra := e.retryAfter[key]; ra != "" {
				w.Header().Set("Retry-After", ra)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(customAnthropicSuccessSSE))
	})
}

func (e *customAnthropicEmulator) seenKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

func (e *customAnthropicEmulator) count(key string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, k := range e.seen {
		if k == key {
			n++
		}
	}
	return n
}

func customAnthropicNode(t *testing.T, base, root string) yaml.Node {
	t.Helper()
	var n yaml.Node
	raw := "backend_prefix: custom-anthropic\nbase_url: " + base + "\napi_key_env_var_root: " + root + "\n"
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func openCustomAnthropic(t *testing.T, be execbackend.Backend) string {
	t.Helper()
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	es, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return collected.Text.String()
}

// TestCustomAnthropicSnapshot_candidateKeysPreferredOverProcessEnv proves the
// custom Anthropic family resolves its per-instance root from the authorized
// candidate snapshot instead of the process environment at factory build.
func TestCustomAnthropicSnapshot_candidateKeysPreferredOverProcessEnv(t *testing.T) {
	root := "CUSTOM_ANTHROPIC_SNAP_API_KEY"
	t.Setenv(root, "env-key")

	emu := &customAnthropicEmulator{fail: map[string]int{"env-key": http.StatusUnauthorized}}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	snap := pluginreg.HostCredentialSnapshot{
		ByRoot:      map[string][]string{root: {"snap-k1"}},
		MaxEnvIndex: 20,
	}
	res, err := backend.LifecycleAnthropicCompatible("custom-anthropic", customAnthropicNode(t, srv.URL, root), srv.Client(), pluginreg.BackendFactoryDeps{Credentials: snap})
	if err != nil {
		t.Fatalf("lifecycle build: %v", err)
	}
	if text := openCustomAnthropic(t, res.Backend); text != "custom-anthropic-ok" {
		t.Fatalf("text = %q, want custom-anthropic-ok", text)
	}
	if seen := emu.seenKeys(); len(seen) != 1 || seen[0] != "snap-k1" {
		t.Fatalf("credential order = %v, want [snap-k1]", seen)
	}
}

// TestCustomAnthropicSnapshot_thresholdFromSnapshotSelectsRotation proves the
// snapshot-bound quota_switch_retry_after reaches the custom Anthropic
// coordinator: a 45-minute hint rotates under a 30-minute policy.
func TestCustomAnthropicSnapshot_thresholdFromSnapshotSelectsRotation(t *testing.T) {
	root := "CUSTOM_ANTHROPIC_THRESH_API_KEY"
	t.Setenv(root, "k1")
	t.Setenv(root+"_2", "k2")

	emu := &customAnthropicEmulator{
		fail:       map[string]int{"k1": http.StatusTooManyRequests},
		retryAfter: map[string]string{"k1": "2700"},
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	snap := pluginreg.HostCredentialSnapshot{
		ByRoot:                map[string][]string{root: {"k1", "k2"}},
		MaxEnvIndex:           20,
		QuotaSwitchRetryAfter: 30 * time.Minute,
	}
	res, err := backend.LifecycleAnthropicCompatible("custom-anthropic", customAnthropicNode(t, srv.URL, root), srv.Client(), pluginreg.BackendFactoryDeps{Credentials: snap})
	if err != nil {
		t.Fatalf("lifecycle build: %v", err)
	}
	if text := openCustomAnthropic(t, res.Backend); text != "custom-anthropic-ok" {
		t.Fatalf("text = %q, want custom-anthropic-ok", text)
	}
	seen := emu.seenKeys()
	if len(seen) == 0 || seen[0] != "k1" || seen[len(seen)-1] != "k2" {
		t.Fatalf("credential order = %v, want first k1 and last k2", seen)
	}
	if emu.count("k2") != 1 {
		t.Fatalf("k2 attempts = %d, want exactly 1", emu.count("k2"))
	}
	if !strings.Contains(strings.Join(seen, ","), "k1") {
		t.Fatalf("k1 never attempted: %v", seen)
	}
}
