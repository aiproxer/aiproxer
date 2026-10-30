package openaicompat_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

const customChatSuccessJSON = `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"custom-ok"},"finish_reason":"stop"}]}`

// customKeyEmulator fails scripted bearer keys with a 429 carrying retryAfter
// and serves a completed chat completion to every other key.
type customKeyEmulator struct {
	mu         sync.Mutex
	seen       []string
	failKey    string
	retryAfter string
}

func (e *customKeyEmulator) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		e.mu.Lock()
		e.seen = append(e.seen, key)
		e.mu.Unlock()
		if key == e.failKey {
			if e.retryAfter != "" {
				w.Header().Set("Retry-After", e.retryAfter)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limit","type":"rate_limit"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, customChatSuccessJSON)
	})
}

func (e *customKeyEmulator) seenKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

func customLegacyNode(t *testing.T, base, root string) yaml.Node {
	t.Helper()
	var n yaml.Node
	raw := "backend_prefix: custom-legacy\nbase_url: " + base + "/v1\napi_key_env_var_root: " + root + "\n"
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func customChatCall() aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIChatCompletions,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
}

func openCustomCollect(t *testing.T, be execbackend.Backend) string {
	t.Helper()
	es, err := be.Open(context.Background(), customChatCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), es)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return collected.Text.String()
}

// TestCustomSnapshot_candidateKeysPreferredOverProcessEnv proves the custom
// OpenAI family resolves its per-instance root from the authorized candidate
// snapshot instead of the process environment at factory build.
func TestCustomSnapshot_candidateKeysPreferredOverProcessEnv(t *testing.T) {
	root := "CUSTOM_OAI_SNAP_API_KEY"
	t.Setenv(root, "env-key")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if gotAuth != "Bearer snap-k1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, customChatSuccessJSON)
	}))
	t.Cleanup(srv.Close)

	snap := pluginreg.HostCredentialSnapshot{
		ByRoot:      map[string][]string{root: {"snap-k1"}},
		MaxEnvIndex: 20,
	}
	res, err := openaicompat.LifecycleOpenAILegacyCompatible("custom-legacy", customLegacyNode(t, srv.URL, root), srv.Client(), pluginreg.BackendFactoryDeps{Credentials: snap})
	if err != nil {
		t.Fatalf("lifecycle build: %v", err)
	}
	if text := openCustomCollect(t, res.Backend); text != "custom-ok" {
		t.Fatalf("text = %q, want custom-ok", text)
	}
	if gotAuth != "Bearer snap-k1" {
		t.Fatalf("serving auth = %q, want admitted snapshot key", gotAuth)
	}
}

// TestCustomSnapshot_thresholdFromSnapshotSelectsRotation proves the
// snapshot-bound quota_switch_retry_after reaches the custom OpenAI
// coordinator: a 45-minute hint rotates under a 30-minute policy.
func TestCustomSnapshot_thresholdFromSnapshotSelectsRotation(t *testing.T) {
	root := "CUSTOM_OAI_THRESH_API_KEY"
	t.Setenv(root, "k1")
	t.Setenv(root+"_2", "k2")

	emu := &customKeyEmulator{failKey: "k1", retryAfter: "2700"}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	snap := pluginreg.HostCredentialSnapshot{
		ByRoot:                map[string][]string{root: {"k1", "k2"}},
		MaxEnvIndex:           20,
		QuotaSwitchRetryAfter: 30 * time.Minute,
	}
	res, err := openaicompat.LifecycleOpenAILegacyCompatible("custom-legacy", customLegacyNode(t, srv.URL, root), srv.Client(), pluginreg.BackendFactoryDeps{Credentials: snap})
	if err != nil {
		t.Fatalf("lifecycle build: %v", err)
	}
	if text := openCustomCollect(t, res.Backend); text != "custom-ok" {
		t.Fatalf("text = %q, want custom-ok", text)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "k1" || seen[1] != "k2" {
		t.Fatalf("credential order = %v, want [k1 k2]", seen)
	}
}
