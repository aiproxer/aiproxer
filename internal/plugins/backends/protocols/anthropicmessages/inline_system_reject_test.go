package anthropicmessages

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	asdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const upstreamSecretMarker = "UPSTREAM-BODY-MARKER-7f3a"

const upstreamInvalidRequestBody = `{"type":"error","error":{"type":"invalid_request_error","message":"model rejects inline system ` + upstreamSecretMarker + `"}}`

// inlineSystemCall is a valid placement: a user turn followed by a mid-history
// system message that ends the request.
func inlineSystemCall() aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("U1")}},
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("environment block")}},
		},
	}
}

func plainCall() aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
		},
	}
}

// openWithFakeUpstream opens one attempt against a fake upstream that answers
// every request with the given status and body. noAuth selects the no-key
// compatible path.
func openWithFakeUpstream(t *testing.T, call aipapi.Call, status int, body string, noAuth bool, attempts *atomic.Int32) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	cfg := Config{
		BackendID:     "anthropic-test",
		BaseURL:       srv.URL,
		APIKey:        "sk-ant-test",
		HTTPClient:    srv.Client(),
		SDKMaxRetries: new(int),
	}
	if noAuth {
		cfg.APIKey = ""
		cfg.CompatibleModeAuth = true
	}
	be := NewBackend(cfg)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-opus-5-5"}}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected Open error")
	}
	return err
}

// Claim: a provider 400 for a request carrying a mid-history system message is
// a typed client reject, with the fixed reason as its only text, and the upstream
// error stays in the chain for diagnostics.
// Mutant: drop classifyInlineSystemReject at the credentialed return.
func TestInlineSystemUpstream400_IsTypedRejectWithoutLeak(t *testing.T) {
	t.Parallel()
	for _, noAuth := range []bool{false, true} {
		name := "credentialed"
		if noAuth {
			name = "no_auth"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var attempts atomic.Int32
			err := openWithFakeUpstream(t, inlineSystemCall(), http.StatusBadRequest, upstreamInvalidRequestBody, noAuth, &attempts)

			rej, ok := errors.AsType[*aipapi.RejectError](err)
			if !ok {
				t.Fatalf("want *aipapi.RejectError, got %T: %v", err, err)
			}
			if rej.Reason != inlineSystemRejectReason {
				t.Fatalf("reason %q, want fixed %q", rej.Reason, inlineSystemRejectReason)
			}
			if got := err.Error(); got != inlineSystemRejectReason {
				t.Fatalf("client-visible text %q must be the fixed reason only", got)
			}
			if strings.Contains(err.Error(), upstreamSecretMarker) {
				t.Fatalf("upstream body leaked into error text: %v", err)
			}
			if _, ok := errors.AsType[*asdk.Error](err); !ok {
				t.Fatalf("upstream SDK error must stay in the chain for diagnostics: %v", err)
			}
			if aipapi.IsRecoverablePreOutput(err) {
				t.Fatal("a rejected request must not be retried as recoverable")
			}
			if n := attempts.Load(); n != 1 {
				t.Fatalf("upstream attempts %d, want 1 (no rotation on 400)", n)
			}
		})
	}
}

// Claim: a provider 400 for a request without an inline system message keeps its
// existing untyped behavior.
// Mutant: classify every upstream 400 as a reject.
func TestUpstream400WithoutInlineSystem_Unchanged(t *testing.T) {
	t.Parallel()
	for _, noAuth := range []bool{false, true} {
		var attempts atomic.Int32
		err := openWithFakeUpstream(t, plainCall(), http.StatusBadRequest, upstreamInvalidRequestBody, noAuth, &attempts)
		if aipapi.IsReject(err) {
			t.Fatalf("noAuth=%v: a 400 without an inline system message must not become a reject: %v", noAuth, err)
		}
		if aipapi.IsRecoverablePreOutput(err) {
			t.Fatalf("noAuth=%v: 400 must not be recoverable: %v", noAuth, err)
		}
	}
}

// Claim: a provider 500 with an inline system message stays recoverable
// pre-output, and is not converted into a client reject.
// Mutant: classify any upstream error with an inline system as a reject.
func TestInlineSystemUpstream500_StaysRecoverable(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	err := openWithFakeUpstream(t, inlineSystemCall(), http.StatusInternalServerError,
		`{"type":"error","error":{"type":"api_error","message":"boom"}}`, false, &attempts)
	if aipapi.IsReject(err) {
		t.Fatalf("500 must not become a reject: %v", err)
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("500 must stay recoverable pre-output: %v", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts %d, want 1 (SDK retries disabled)", n)
	}
}

// Claim: for every non-400 upstream failure, a request carrying an inline
// system message gets exactly the credential handling a plain request gets:
// the same keys tried in the same order, the same success or failure, and the
// same recoverability. Only the 400 path is reclassified.
// Mutant: classify by request shape before checking the status, or reclassify
// a non-400 status for inline-system requests.
func TestInlineSystemCredentialHandlingMatchesPlainRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		status  int
		body    string
		headers map[string]string
	}{
		{"401 auth invalid", http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`, nil},
		{"402 payment", http.StatusPaymentRequired, `{"type":"error","error":{"type":"billing_error","message":"pay"}}`, nil},
		{"403 forbidden", http.StatusForbidden, `{"type":"error","error":{"type":"permission_error","message":"no"}}`, nil},
		{"429 long retry-after", http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, map[string]string{"Retry-After": "7200"}},
		{"429 short retry-after", http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, map[string]string{"Retry-After": "1"}},
		{"500", http.StatusInternalServerError, `{"type":"error","error":{"type":"api_error","message":"boom"}}`, nil},
		{"529 overloaded", 529, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, nil},
	}
	type outcome struct {
		keys        []string
		ok          bool
		recoverable bool
		reject      bool
	}
	run := func(t *testing.T, call aipapi.Call, status int, body string, headers map[string]string) outcome {
		t.Helper()
		emu := &keyedEmulator{failKey: "k1", status: status, body: body, headers: headers}
		srv := httptest.NewServer(emu.handler())
		t.Cleanup(srv.Close)
		be := NewBackend(Config{
			BackendID:     "anthropic-cred-test",
			BaseURL:       srv.URL,
			APIKey:        "k1",
			APIKeys:       []string{"k1", "k2"},
			HTTPClient:    srv.Client(),
			SDKMaxRetries: new(int),
		})
		cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-opus-5-5"}}
		stream, err := be.Open(context.Background(), call, cand)
		if err == nil {
			if _, err = aipapi.Collect(context.Background(), stream); err != nil {
				t.Fatalf("Collect: %v", err)
			}
		}
		return outcome{
			keys:        emu.seenKeys(),
			ok:          err == nil,
			recoverable: aipapi.IsRecoverablePreOutput(err),
			reject:      aipapi.IsReject(err),
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plain := run(t, plainCall(), tc.status, tc.body, tc.headers)
			inline := run(t, inlineSystemCall(), tc.status, tc.body, tc.headers)
			if inline.reject {
				t.Fatalf("status %d with an inline system message became a client reject", tc.status)
			}
			if !slices.Equal(plain.keys, inline.keys) || plain.ok != inline.ok || plain.recoverable != inline.recoverable {
				t.Fatalf("inline-system handling %+v differs from plain request %+v", inline, plain)
			}
		})
	}
	// Guard against a vacuous parity: the table must exercise both rotation and
	// non-rotation, or matching outcomes would prove nothing about the pool.
	if got := run(t, inlineSystemCall(), http.StatusUnauthorized, cases[0].body, nil).keys; !slices.Equal(got, []string{"k1", "k2"}) {
		t.Fatalf("401 with inline system: keys %v, want rotation [k1 k2]", got)
	}
	if got := run(t, inlineSystemCall(), http.StatusForbidden, cases[2].body, nil).keys; !slices.Equal(got, []string{"k1"}) {
		t.Fatalf("403 with inline system: keys %v, want no rotation [k1]", got)
	}
}
