package alibabatokenplanintl_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/alibabatokenplanintl"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// keyedEmulator fails failKey with a scripted Anthropic-shaped error and serves
// a valid message stream to every other key, recording observed keys in order.
type keyedEmulator struct {
	mu         sync.Mutex
	seen       []string
	failKey    string
	status     int
	body       string
	retryAfter string
	attempts   atomic.Int32
}

func (e *keyedEmulator) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.attempts.Add(1)
		key := r.Header.Get("x-api-key")
		e.mu.Lock()
		e.seen = append(e.seen, key)
		e.mu.Unlock()
		if key == e.failKey {
			if e.retryAfter != "" {
				w.Header().Set("Retry-After", e.retryAfter)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(e.status)
			_, _ = w.Write([]byte(e.body))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(streamSSE))
	})
}

func (e *keyedEmulator) seenKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

func openAlibaba(t *testing.T, srv *httptest.Server, threshold time.Duration, keys ...string) (string, error) {
	t.Helper()
	zero := 0
	var apiKey string
	var apiKeys []string
	if len(keys) > 0 {
		apiKey = keys[0]
		apiKeys = keys
	}
	be := backend.New(backend.Config{
		BaseURL:              srv.URL,
		APIKey:               apiKey,
		APIKeys:              apiKeys,
		HTTPClient:           srv.Client(),
		SDKMaxRetries:        &zero,
		QuotaSwitchThreshold: threshold,
	})
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("question")}}}}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backend.ID, Model: "qwen3.7-plus"}}
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

func TestCredentialPolicy_unauthorizedAdvancesToSecondKey(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusUnauthorized,
		body:    `{"type":"error","error":{"type":"authentication_error","message":"invalid key"}}`,
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	text, err := openAlibaba(t, srv, 0, "k1", "k2")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if text != "ok" {
		t.Fatalf("text = %q, want ok", text)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "k1" || seen[1] != "k2" {
		t.Fatalf("credential order = %v, want [k1 k2]", seen)
	}
}

func TestCredentialPolicy_longThrottleAdvancesToSecondKey(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey:    "k1",
		status:     http.StatusTooManyRequests,
		body:       `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
		retryAfter: "7200",
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	text, err := openAlibaba(t, srv, 0, "k1", "k2")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if text != "ok" {
		t.Fatalf("text = %q, want ok", text)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "k1" || seen[1] != "k2" {
		t.Fatalf("credential order = %v, want [k1 k2]", seen)
	}
}

func TestCredentialPolicy_shortThrottleDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey:    "k1",
		status:     http.StatusTooManyRequests,
		body:       `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
		retryAfter: "30",
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	_, err := openAlibaba(t, srv, 0, "k1", "k2")
	if err == nil {
		t.Fatal("expected short-throttle error")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("short throttle must be recoverable pre-output, got: %v", err)
	}
	if n := emu.attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (no rotation on 30s throttle)", n)
	}
}

func TestCredentialPolicy_forbiddenDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusForbidden,
		body:    `{"type":"error","error":{"type":"permission_error","message":"denied"}}`,
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	_, err := openAlibaba(t, srv, 0, "k1", "k2")
	if err == nil {
		t.Fatal("expected 403 error")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("403 must not be a rotation signal: %v", err)
	}
	if n := emu.attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (no rotation on 403)", n)
	}
	if !strings.Contains(err.Error(), backend.ID+": ") {
		t.Fatalf("expected backend ID prefix, got: %v", err)
	}
}

func TestCredentialPolicy_configuredThresholdHonored(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey:    "k1",
		status:     http.StatusTooManyRequests,
		body:       `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
		retryAfter: "2700",
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	text, err := openAlibaba(t, srv, 30*time.Minute, "k1", "k2")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if text != "ok" {
		t.Fatalf("text = %q, want ok", text)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "k1" || seen[1] != "k2" {
		t.Fatalf("credential order = %v, want [k1 k2] (45m hint > 30m threshold)", seen)
	}
}
