package anthropicmessages

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
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const credentialRotationSuccessSSE = "event: message_start\ndata: " +
	`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude-3-5-haiku-20241022","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":0,"output_tokens":0}}}` + "\n\n" +
	"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"rotated-ok"}}` + "\n\n" +
	"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"

// keyedEmulator fails requests bearing failKey with status/body/headers and
// serves a success SSE stream to every other key. It records every observed
// x-api-key in order so tests prove which credential served each attempt.
type keyedEmulator struct {
	mu       sync.Mutex
	seen     []string
	failKey  string
	status   int
	body     string
	headers  map[string]string
	attempts atomic.Int32
}

func (e *keyedEmulator) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.attempts.Add(1)
		key := r.Header.Get("x-api-key")
		e.mu.Lock()
		e.seen = append(e.seen, key)
		e.mu.Unlock()
		if key == e.failKey {
			for k, v := range e.headers {
				w.Header().Set(k, v)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(e.status)
			_, _ = w.Write([]byte(e.body))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(credentialRotationSuccessSSE))
	})
}

func (e *keyedEmulator) seenKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

func rotationTestCall() (aipapi.Call, routing.AttemptCandidate) {
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
	return call, cand
}

func openWithKeys(t *testing.T, srv *httptest.Server, keys ...string) (aipapi.ManagedEventStream, error) {
	t.Helper()
	zero := 0
	var apiKey string
	var apiKeys []string
	if len(keys) > 0 {
		apiKey = keys[0]
		apiKeys = keys
	}
	be := NewBackend(Config{
		BackendID:     "anthropic-cred-test",
		BaseURL:       srv.URL,
		APIKey:        apiKey,
		APIKeys:       apiKeys,
		HTTPClient:    srv.Client(),
		SDKMaxRetries: &zero,
	})
	call, cand := rotationTestCall()
	return be.Open(context.Background(), call, cand)
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

	stream, err := openWithKeys(t, srv, "k1", "k2")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := collected.Text.String(); got != "rotated-ok" {
		t.Fatalf("text = %q, want rotated-ok", got)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "k1" || seen[1] != "k2" {
		t.Fatalf("credential order = %v, want [k1 k2]", seen)
	}
}

func TestCredentialPolicy_longThrottleAdvancesToSecondKey(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusTooManyRequests,
		body:    `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
		headers: map[string]string{"Retry-After": "7200"},
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	stream, err := openWithKeys(t, srv, "k1", "k2")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "k1" || seen[1] != "k2" {
		t.Fatalf("credential order = %v, want [k1 k2]", seen)
	}
}

func TestCredentialPolicy_shortThrottleDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusTooManyRequests,
		body:    `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
		headers: map[string]string{"Retry-After": "30"},
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	_, err := openWithKeys(t, srv, "k1", "k2")
	if err == nil {
		t.Fatal("expected short-throttle error")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("short throttle must be recoverable pre-output, got: %v", err)
	}
	if n := emu.attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (no rotation on 30s throttle)", n)
	}
	if seen := emu.seenKeys(); len(seen) != 1 || seen[0] != "k1" {
		t.Fatalf("credential order = %v, want [k1] (secondary unconsumed)", seen)
	}
	// The primary stays the priority owner: a follow-up request still uses k1.
	_, err = openWithKeys(t, srv, "k1", "k2")
	if err == nil {
		t.Fatal("expected short-throttle error on follow-up")
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[1] != "k1" {
		t.Fatalf("follow-up credential order = %v, want [k1 k1]", seen)
	}
}

func TestCredentialPolicy_missingRetryAfterDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusTooManyRequests,
		body:    `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	_, err := openWithKeys(t, srv, "k1", "k2")
	if err == nil {
		t.Fatal("expected throttle error")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("unknown-hint throttle must be recoverable pre-output, got: %v", err)
	}
	if n := emu.attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (no rotation without Retry-After)", n)
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

	_, err := openWithKeys(t, srv, "k1", "k2")
	if err == nil {
		t.Fatal("expected 403 error")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("403 must not be a rotation signal: %v", err)
	}
	if n := emu.attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (no rotation on 403)", n)
	}
	if !strings.Contains(err.Error(), "anthropic-cred-test: ") {
		t.Fatalf("expected backend ID prefix, got: %v", err)
	}
}

func TestCredentialPolicy_paymentRequiredDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusPaymentRequired,
		body:    `{"type":"error","error":{"type":"billing_error","message":"quota"}}`,
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	_, err := openWithKeys(t, srv, "k1", "k2")
	if err == nil {
		t.Fatal("expected 402 error")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("402 must not be a rotation signal: %v", err)
	}
	if n := emu.attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (no rotation on 402)", n)
	}
}

func TestCredentialPolicy_configuredThresholdHonored(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusTooManyRequests,
		body:    `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
		headers: map[string]string{"Retry-After": "2700"},
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	// A 45-minute hint exceeds a configured 30-minute threshold (rotation)
	// but not the 1-hour default (transient throttle): the adapter must honor
	// the configured upstream_credentials.quota_switch_retry_after.
	zero := 0
	be := NewBackend(Config{
		BackendID:            "anthropic-cred-test",
		BaseURL:              srv.URL,
		APIKey:               "k1",
		APIKeys:              []string{"k1", "k2"},
		HTTPClient:           srv.Client(),
		SDKMaxRetries:        &zero,
		QuotaSwitchThreshold: 30 * time.Minute,
	})
	call, cand := rotationTestCall()
	stream, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "k1" || seen[1] != "k2" {
		t.Fatalf("credential order = %v, want [k1 k2] (45m hint > 30m threshold)", seen)
	}
}
