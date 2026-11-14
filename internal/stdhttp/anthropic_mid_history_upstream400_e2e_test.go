package stdhttp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	upstreamRejectSecretMarker = "UPSTREAM-BODY-MARKER-7f3a"
	midHistoryRejectReason     = "upstream rejected the mid-history system message"
	// nonTextImageData is placeholder base64 for an image block; the request is
	// refused before decoding, so the bytes need not be a real image.
	nonTextImageData = "AAAAAAAA"
)

// upstreamCounts records what the scripted upstream saw. attempts counts every
// provider request; refused counts the ones answered with the scripted 400.
type upstreamCounts struct {
	attempts atomic.Int32
	refused  atomic.Int32
}

// scriptedUpstream400 makes the emulator answer 400 invalid_request_error for
// every request whose body satisfies rejectWhen, the way a provider refuses a
// model that does not accept inline system placement. Other requests reach the
// reference emulator unchanged.
func scriptedUpstream400(counts *upstreamCounts, rejectWhen func(body []byte) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if r.Method == http.MethodPost && r.URL.Path == "/v1/messages" {
				counts.attempts.Add(1)
			}
			if rejectWhen(body) {
				if r.URL.Path == "/v1/messages" {
					counts.refused.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"model rejects inline system ` + upstreamRejectSecretMarker + `"}}`))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
}

// withSecondFailoverArm widens the harness route to two Anthropic failover arms
// with an attempt budget of two, so a failure the core treats as retryable would
// reach the provider a second time. Without it the route has one candidate and a
// single-attempt assertion proves nothing.
func withSecondFailoverArm(t *testing.T) func(string) string {
	t.Helper()
	return func(cfg string) string {
		const (
			budget = "max_attempts: 1"
			route  = `default_route: "anthropic:` + rpE2EAnthModel + `"`
		)
		if !strings.Contains(cfg, budget) || !strings.Contains(cfg, route) {
			t.Fatalf("harness config no longer has %q and %q; update withSecondFailoverArm", budget, route)
		}
		cfg = strings.Replace(cfg, budget, "max_attempts: 2", 1)
		return strings.Replace(cfg, route, `default_route: "anthropic:`+rpE2EAnthModel+`|anthropic:claude-opus-5-5"`, 1)
	}
}

// hasSystemMessage reports whether the provider-bound body carries a
// mid-history system-role message.
func hasSystemMessage(body []byte) bool {
	var req struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}
	for _, m := range req.Messages {
		if m.Role == "system" {
			return true
		}
	}
	return false
}

// Claim: a provider 400 for a valid mid-history system message reaches the
// client as a 400 invalid_request_error with the fixed reason, in both streaming
// and non-streaming modes, never as an opaque 500, and no upstream body or
// request content leaks into the client error. The typed reject is terminal:
// the provider sees exactly one attempt, and that attempt is the refused
// inline-system request, even though the route has a second failover arm.
// Mutants: drop classifyInlineSystemReject from the backend return path; make
// the reject recoverable pre-output so the core retries it.
func TestAnthropicFrontendMidHistorySystem_UpstreamRejectionIsClientError(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "non_stream"}[stream], func(t *testing.T) {
			t.Parallel()
			var counts upstreamCounts
			stack, _ := startMidHistorySystemStackWithConfig(t, scriptedUpstream400(&counts, hasSystemMessage), withSecondFailoverArm(t))
			status, body := postMidHistoryMessages(t, stack, map[string]any{
				"model": rpE2EAnthModel, "max_tokens": 64, "stream": stream,
				"messages": []any{
					map[string]any{"role": "user", "content": "probe"},
					map[string]any{"role": "system", "content": claudeEnvironmentBlock},
				},
			})
			if status != http.StatusBadRequest {
				t.Fatalf("status %d, want 400; body=%s", status, body)
			}
			var wire struct {
				Type  string `json:"type"`
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatalf("error body: %v; body=%s", err, body)
			}
			if wire.Type != "error" || wire.Error.Type != "invalid_request_error" {
				t.Fatalf("error body %s, want invalid_request_error", body)
			}
			if wire.Error.Message != midHistoryRejectReason {
				t.Fatalf("error message %q, want fixed reason %q", wire.Error.Message, midHistoryRejectReason)
			}
			// rpE2EAnthKey is both the client key and the upstream credential
			// the harness installs (main_test.go), so this covers both.
			for _, leak := range []string{upstreamRejectSecretMarker, "internal error", claudeEnvironmentBlock, rpE2EAnthKey, "probe"} {
				if strings.Contains(string(body), leak) {
					t.Fatalf("client error leaked %q: %s", leak, body)
				}
			}
			if got := counts.attempts.Load(); got != 1 {
				t.Fatalf("provider attempts %d, want exactly 1 (a typed reject must not be retried)", got)
			}
			if got := counts.refused.Load(); got != 1 {
				t.Fatalf("provider refused %d attempts, want 1 (the attempt must carry the inline system message)", got)
			}
		})
	}
}

// Claim: a provider 400 for a request without an inline system message keeps its
// existing behavior: one provider attempt and the opaque internal error, never
// the mid-history reason.
// Mutant: classify every upstream 400 as a reject.
func TestAnthropicFrontendUpstream400WithoutInlineSystem_Unchanged(t *testing.T) {
	t.Parallel()
	var counts upstreamCounts
	stack, _ := startMidHistorySystemStackWithUpstream(t, scriptedUpstream400(&counts, func([]byte) bool { return true }))
	status, body := postMidHistoryMessages(t, stack, map[string]any{
		"model": rpE2EAnthModel, "max_tokens": 64, "stream": false,
		"messages": []any{map[string]any{"role": "user", "content": "probe"}},
	})
	if status != http.StatusInternalServerError || !strings.Contains(string(body), "internal error") {
		t.Fatalf("status %d, want the existing opaque 500; body=%s", status, body)
	}
	if strings.Contains(string(body), midHistoryRejectReason) || strings.Contains(string(body), upstreamRejectSecretMarker) {
		t.Fatalf("client error leaked upstream detail: %s", body)
	}
	if got := counts.attempts.Load(); got != 1 {
		t.Fatalf("provider attempts %d, want 1", got)
	}
}

// Claim: a mid-history system message carrying a non-text block is refused
// locally as 400 invalid_request_error, in both client modes, and the provider
// is never contacted.
// Mutant: let systemMessageParam pass non-text parts through.
func TestAnthropicFrontendMidHistorySystem_NonTextRefusedLocally(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{true, false} {
		t.Run(map[bool]string{true: "stream", false: "non_stream"}[stream], func(t *testing.T) {
			t.Parallel()
			var counts upstreamCounts
			stack, _ := startMidHistorySystemStackWithUpstream(t, scriptedUpstream400(&counts, func([]byte) bool { return false }))
			status, body := postMidHistoryMessages(t, stack, map[string]any{
				"model": rpE2EAnthModel, "max_tokens": 64, "stream": stream,
				"messages": []any{
					map[string]any{"role": "user", "content": "probe"},
					map[string]any{"role": "system", "content": []any{
						map[string]any{"type": "text", "text": claudeEnvironmentBlock},
						map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": nonTextImageData}},
					}},
				},
			})
			if status != http.StatusBadRequest {
				t.Fatalf("status %d, want 400; body=%s", status, body)
			}
			var wire struct {
				Type  string `json:"type"`
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatalf("error body: %v; body=%s", err, body)
			}
			if wire.Type != "error" || wire.Error.Type != "invalid_request_error" {
				t.Fatalf("error body %s, want invalid_request_error", body)
			}
			if wire.Error.Message != "system message supports text parts only" {
				t.Fatalf("error message %q, want the local non-text refusal", wire.Error.Message)
			}
			for _, leak := range []string{claudeEnvironmentBlock, rpE2EAnthKey, nonTextImageData} {
				if strings.Contains(string(body), leak) {
					t.Fatalf("client error leaked %q: %s", leak, body)
				}
			}
			if got := counts.attempts.Load(); got != 0 {
				t.Fatalf("provider attempts %d, want 0 (refusal must happen before upstream I/O)", got)
			}
		})
	}
}
