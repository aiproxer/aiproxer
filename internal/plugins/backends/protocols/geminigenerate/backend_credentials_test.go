package geminigenerate

import (
	"context"
	"fmt"
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

const credentialRotationSuccessSSE = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"rotated-ok\"}]}}]}\n\n"

// keyedEmulator fails requests bearing failKey with a Google-style error body
// and serves a success SSE chunk to every other key. It records every observed
// credential (header or query) in order so tests prove key selection.
type keyedEmulator struct {
	mu       sync.Mutex
	seen     []string
	failKey  string
	status   int
	body     string
	attempts atomic.Int32
}

func (e *keyedEmulator) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.attempts.Add(1)
		key := r.Header.Get("x-goog-api-key")
		if key == "" {
			key = r.URL.Query().Get("key")
		}
		e.mu.Lock()
		e.seen = append(e.seen, key)
		e.mu.Unlock()
		if key == e.failKey {
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

func googleErrorBody(code int, message, status string, retryDelay string) string {
	details := ""
	if retryDelay != "" {
		details = fmt.Sprintf(`,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":%q}]`, retryDelay)
	}
	return fmt.Sprintf(`{"error":{"code":%d,"message":%q,"status":%q%s}}`, code, message, status, details)
}

func openWithKeys(t *testing.T, srv *httptest.Server, keys ...string) (aipapi.ManagedEventStream, error) {
	t.Helper()
	var apiKey string
	var apiKeys []string
	if len(keys) > 0 {
		apiKey = keys[0]
		apiKeys = keys
	}
	be := NewBackend(Config{
		BackendID:  "gemini-cred-test",
		BaseURL:    srv.URL,
		APIKey:     apiKey,
		APIKeys:    apiKeys,
		HTTPClient: srv.Client(),
	})
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	return be.Open(context.Background(), call, cand)
}

func TestCredentialPolicy_unauthorizedAdvancesToSecondKey(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusUnauthorized,
		body:    googleErrorBody(http.StatusUnauthorized, "API key not valid.", "UNAUTHENTICATED", ""),
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

func TestCredentialPolicy_longRetryInfoAdvancesToSecondKey(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusTooManyRequests,
		body:    googleErrorBody(http.StatusTooManyRequests, "Resource exhausted.", "RESOURCE_EXHAUSTED", "7200s"),
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
		t.Fatalf("credential order = %v, want [k1 k2] (RetryInfo 7200s is long exhaustion)", seen)
	}
}

func TestCredentialPolicy_shortRetryInfoDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusTooManyRequests,
		body:    googleErrorBody(http.StatusTooManyRequests, "Resource exhausted.", "RESOURCE_EXHAUSTED", "30s"),
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	_, err := openWithKeys(t, srv, "k1", "k2")
	if err == nil {
		t.Fatal("expected short-throttle error")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("short RetryInfo throttle must be recoverable pre-output, got: %v", err)
	}
	if n := emu.attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (no rotation on 30s RetryInfo)", n)
	}
	if seen := emu.seenKeys(); len(seen) != 1 || seen[0] != "k1" {
		t.Fatalf("credential order = %v, want [k1] (secondary unconsumed)", seen)
	}
}

func TestCredentialPolicy_missingRetryInfoDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusTooManyRequests,
		body:    googleErrorBody(http.StatusTooManyRequests, "Resource exhausted.", "RESOURCE_EXHAUSTED", ""),
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
		t.Fatalf("upstream attempts = %d, want 1 (no rotation without RetryInfo)", n)
	}
}

func TestCredentialPolicy_forbiddenDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusForbidden,
		body:    googleErrorBody(http.StatusForbidden, "Permission denied.", "PERMISSION_DENIED", ""),
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
	if !strings.Contains(err.Error(), "gemini-cred-test: ") {
		t.Fatalf("expected backend ID prefix, got: %v", err)
	}
}

func TestCredentialPolicy_paymentRequiredDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyedEmulator{
		failKey: "k1",
		status:  http.StatusPaymentRequired,
		body:    googleErrorBody(http.StatusPaymentRequired, "Billing disabled.", "FAILED_PRECONDITION", ""),
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
		body:    googleErrorBody(http.StatusTooManyRequests, "Resource exhausted.", "RESOURCE_EXHAUSTED", "2700s"),
	}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	// A 45-minute RetryInfo exceeds a configured 30-minute threshold
	// (rotation) but not the 1-hour default (transient throttle).
	be := NewBackend(Config{
		BackendID:            "gemini-cred-test",
		BaseURL:              srv.URL,
		APIKey:               "k1",
		APIKeys:              []string{"k1", "k2"},
		HTTPClient:           srv.Client(),
		QuotaSwitchThreshold: 30 * time.Minute,
	})
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	stream, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if seen := emu.seenKeys(); len(seen) != 2 || seen[0] != "k1" || seen[1] != "k2" {
		t.Fatalf("credential order = %v, want [k1 k2] (45m RetryInfo > 30m threshold)", seen)
	}
}
