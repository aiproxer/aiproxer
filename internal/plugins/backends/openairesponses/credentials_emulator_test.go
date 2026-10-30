package openairesponses_test

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
	backend "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// keyFailingEmulator fails failKey with a scripted status/Retry-After/body and
// delegates every other credential to the reference chat backend, which serves
// a valid completion. It records the bearer secret of every attempt in order.
type keyFailingEmulator struct {
	mu         sync.Mutex
	seen       []string
	success    http.Handler
	failKey    string
	status     int
	retryAfter string
	errJSON    string
	attempts   atomic.Int32
}

func (e *keyFailingEmulator) handler() http.Handler {
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
			w.WriteHeader(e.status)
			_, _ = w.Write([]byte(e.errJSON))
			return
		}
		e.success.ServeHTTP(w, r)
	})
}

func (e *keyFailingEmulator) seenKeys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

func openNonStreaming(t *testing.T, srv *httptest.Server, cfg backend.Config) (string, error) {
	t.Helper()
	be := backend.New(cfg)
	call := aipapi.Call{
		ID:         "cred-emu",
		Invocation: aipapi.Invocation{TransportMode: aipapi.TransportModeNonStreaming},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
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

func emulatorServer(t *testing.T, emu *keyFailingEmulator) *httptest.Server {
	t.Helper()
	emu.success = refbackend.NewHandler(refbackend.Config{})
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)
	return srv
}

func emulatorConfig(srv *httptest.Server, threshold time.Duration) backend.Config {
	zero := 0
	return backend.Config{
		BaseURL:              srv.URL + "/v1",
		APIKey:               "k1",
		APIKeys:              []string{"k1", "k2"},
		HTTPClient:           srv.Client(),
		SDKMaxRetries:        &zero,
		QuotaSwitchThreshold: threshold,
	}
}

const (
	openAIUnauthorizedJSON = `{"error":{"message":"incorrect api key","type":"invalid_request_error","code":"invalid_api_key"}}`
	openAIRateLimitJSON    = `{"error":{"message":"rate limit exceeded","type":"requests","code":"rate_limit_exceeded"}}`
)

func TestEmulator_unauthorizedAdvancesToSecondKey(t *testing.T) {
	t.Parallel()
	emu := &keyFailingEmulator{failKey: "k1", status: http.StatusUnauthorized, errJSON: openAIUnauthorizedJSON}
	srv := emulatorServer(t, emu)

	text, err := openNonStreaming(t, srv, emulatorConfig(srv, 0))
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

func TestEmulator_longThrottleAdvancesToSecondKey(t *testing.T) {
	t.Parallel()
	emu := &keyFailingEmulator{failKey: "k1", status: http.StatusTooManyRequests, retryAfter: "7200", errJSON: openAIRateLimitJSON}
	srv := emulatorServer(t, emu)

	text, err := openNonStreaming(t, srv, emulatorConfig(srv, 0))
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

func TestEmulator_shortThrottleDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyFailingEmulator{failKey: "k1", status: http.StatusTooManyRequests, retryAfter: "30", errJSON: openAIRateLimitJSON}
	srv := emulatorServer(t, emu)

	_, err := openNonStreaming(t, srv, emulatorConfig(srv, 0))
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

func TestEmulator_missingRetryAfterDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyFailingEmulator{failKey: "k1", status: http.StatusTooManyRequests, errJSON: openAIRateLimitJSON}
	srv := emulatorServer(t, emu)

	_, err := openNonStreaming(t, srv, emulatorConfig(srv, 0))
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

func TestEmulator_forbiddenDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyFailingEmulator{failKey: "k1", status: http.StatusForbidden, errJSON: `{"error":{"message":"forbidden","type":"invalid_request_error"}}`}
	srv := emulatorServer(t, emu)

	_, err := openNonStreaming(t, srv, emulatorConfig(srv, 0))
	if err == nil {
		t.Fatal("expected 403 error")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("403 must not be a rotation signal: %v", err)
	}
	if n := emu.attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts = %d, want 1 (no rotation on 403)", n)
	}
}

func TestEmulator_paymentRequiredDoesNotRotate(t *testing.T) {
	t.Parallel()
	emu := &keyFailingEmulator{failKey: "k1", status: http.StatusPaymentRequired, errJSON: `{"error":{"message":"billing","type":"invalid_request_error"}}`}
	srv := emulatorServer(t, emu)

	_, err := openNonStreaming(t, srv, emulatorConfig(srv, 0))
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

func TestEmulator_configuredThresholdHonored(t *testing.T) {
	t.Parallel()
	emu := &keyFailingEmulator{failKey: "k1", status: http.StatusTooManyRequests, retryAfter: "2700", errJSON: openAIRateLimitJSON}
	srv := emulatorServer(t, emu)

	// A 45-minute hint exceeds a configured 30-minute threshold but not the
	// default 1-hour threshold.
	text, err := openNonStreaming(t, srv, emulatorConfig(srv, 30*time.Minute))
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
