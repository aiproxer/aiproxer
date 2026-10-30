package standardplugins

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

const wiringSuccessJSON = `{
  "id": "resp_wire_1",
  "object": "response",
  "created_at": 1719900000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {"type": "message", "id": "msg_out_1", "status": "completed", "role": "assistant", "content": [{"type": "output_text", "text": "wired-ok"}]}
  ],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`

// thresholdWiringEmulator fails failKey with a 429 carrying retryAfter and
// serves a completed Responses object to every other key.
type thresholdWiringEmulator struct {
	mu         sync.Mutex
	seen       []string
	failKey    string
	retryAfter string
	attempts   atomic.Int32
}

func (e *thresholdWiringEmulator) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.attempts.Add(1)
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
			_, _ = io.WriteString(w, `{"error":{"message":"rate limit exceeded","type":"requests","code":"rate_limit_exceeded"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, wiringSuccessJSON)
	})
}

func (e *thresholdWiringEmulator) seenKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

// TestCredentialWiring_snapshotThresholdReachesAdapter proves requirement 3.2
// end to end: the configured upstream_credentials.quota_switch_retry_after
// captured in the candidate snapshot selects rotation behavior in the built
// OpenAI Responses backend. A 45-minute hint rotates under a 30-minute policy
// but stays a transient same-key throttle under the default 1-hour policy.
func TestCredentialWiring_snapshotThresholdReachesAdapter(t *testing.T) {
	t.Parallel()

	build := func(t *testing.T, emu *thresholdWiringEmulator, threshold string) (string, error) {
		t.Helper()
		srv := httptest.NewServer(emu.handler())
		t.Cleanup(srv.Close)

		env := snapshotMapEnv{"OPENAI_API_KEY": "k1", "OPENAI_API_KEY_2": "k2"}
		cfg := &config.Config{}
		if threshold != "" {
			cfg.UpstreamCredentials.QuotaSwitchRetryAfter = threshold
		}
		snap, err := SnapshotCandidateCredentials(env, cfg)
		if err != nil {
			t.Fatalf("SnapshotCandidateCredentials: %v", err)
		}
		factory := backendFactory(UpstreamAPIKeys{}, backendOpenAIResponses)
		var node yaml.Node
		if err := yaml.Unmarshal([]byte("base_url: "+srv.URL+"/v1\n"), &node); err != nil {
			t.Fatal(err)
		}
		be, err := factory(node, srv.Client(), pluginreg.BackendFactoryDeps{
			Identity:    identity.Config{},
			Credentials: snap,
		})
		if err != nil {
			t.Fatalf("factory: %v", err)
		}
		call := snapshotServingCall()
		cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-responses", Model: "gpt-4o-mini"}}
		stream, err := be.Open(context.Background(), call, cand)
		if err != nil {
			return "", err
		}
		collected, err := aipapi.Collect(context.Background(), stream)
		if err != nil {
			return "", err
		}
		return collected.Text.String(), nil
	}

	t.Run("configured_30m_rotates_on_45m_hint", func(t *testing.T) {
		t.Parallel()
		emu := &thresholdWiringEmulator{failKey: "k1", retryAfter: "2700"}
		text, err := build(t, emu, "30m")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if text != "wired-ok" {
			t.Fatalf("text = %q, want wired-ok", text)
		}
		if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "k1" || seen[1] != "k2" {
			t.Fatalf("credential order = %v, want [k1 k2]", seen)
		}
	})

	t.Run("default_1h_throttles_on_45m_hint", func(t *testing.T) {
		t.Parallel()
		emu := &thresholdWiringEmulator{failKey: "k1", retryAfter: "2700"}
		_, err := build(t, emu, "")
		if err == nil {
			t.Fatal("expected transient-throttle error under the default threshold")
		}
		if !aipapi.IsRecoverablePreOutput(err) {
			t.Fatalf("throttle must be recoverable pre-output, got: %v", err)
		}
		if n := emu.attempts.Load(); n != 1 {
			t.Fatalf("upstream attempts = %d, want 1 (no rotation under default 1h)", n)
		}
	})
}
