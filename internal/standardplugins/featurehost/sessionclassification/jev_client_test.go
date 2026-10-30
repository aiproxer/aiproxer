package sessionclassification

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
)

// jevTestToken is distinctive on purpose: every redaction assertion searches for
// it, so an accidental copy into an error or diagnostic cannot hide (requirement
// 7.1, 7.5).
const jevTestToken = "jev-test-token-must-never-be-echoed-8f41c2ab"

const jevTestCredentialEnv = "AIP_TEST_JEV_CREDENTIAL"

// jevTestPolicy builds a validated remote posture for one generation. Every
// operational value is inside the bounds NewRemotePolicy enforces, so the only
// posture failures a test can produce are the ones it means to produce.
func jevTestPolicy(t *testing.T, timeout time.Duration) featurestate.RemotePolicy {
	t.Helper()
	policy, err := featurestate.NewRemotePolicy(featurestate.Config{
		Mode: featurestate.ModeJev,
		Remote: &featurestate.RemoteConfig{
			Provider:              "jev",
			APIKeyEnv:             jevTestCredentialEnv,
			Timeout:               timeout,
			MaxAttemptsPerSession: 2,
			LeaseTTL:              timeout + featurestate.RemoteLeaseSafetyMargin + time.Second,
			PositiveThreshold:     0.9,
		},
	})
	if err != nil {
		t.Fatalf("build a validated jev policy: %v", err)
	}
	return policy
}

// jevTestDecider builds the adapter against a test endpoint with the credential
// already present in the environment.
// jevTestDecider publishes one decider with a resolvable credential, which
// requirement 6.10 now demands at construction time.
func jevTestDecider(t *testing.T, endpoint string, timeout time.Duration) featurestate.RemoteDecider {
	t.Helper()
	t.Setenv(jevTestCredentialEnv, jevTestToken)
	decider, err := NewJevDecider(jevTestPolicy(t, timeout), withJevEndpoint(endpoint))
	if err != nil {
		t.Fatalf("NewJevDecider: %v", err)
	}
	return decider
}

// jevAnswerBody renders a documented successful response for one probability.
func jevAnswerBody(probability string) string {
	return `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
		`":{"type":"noul","noul":` + probability + `}},"usage":{"input_tokens":296,"output_tokens":20}}`
}

// jevServer starts a hermetic endpoint. No test in this package reaches the
// network (requirement 12.9).
func jevServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// jevReleaseHandler returns a channel closed when the test ends, so a handler
// parked on a stalled endpoint never holds server shutdown open. The test context
// is canceled before cleanup functions run, so a handler released this way always
// exits before its server is closed. Tests assert on the adapter's return value,
// not on the handler's exit.
func jevReleaseHandler(t *testing.T) <-chan struct{} {
	t.Helper()
	return t.Context().Done()
}

func jevSuccessHandler(t *testing.T, calls *atomic.Int32, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}
}

// TestJevDeciderMapsTheDocumentedSuccessResponse is the happy path against the
// reconfirmed schema: one noul answer becomes one bounded RemoteDecision, no
// confidence is invented, and the caller's threshold decides promotion
// (requirements 6.8, 6.9).
func TestJevDeciderMapsTheDocumentedSuccessResponse(t *testing.T) {
	var calls atomic.Int32
	server := jevServer(t, jevSuccessHandler(t, &calls, jevAnswerBody("0.95")))
	decider := jevTestDecider(t, server.URL, 2*time.Second)

	decision, err := decider.Decide(context.Background(), jevMaximalRemoteInput())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("remote attempts = %d, want exactly one", calls.Load())
	}
	if decision.CodingProbability != 0.95 {
		t.Errorf("CodingProbability = %v, want 0.95", decision.CodingProbability)
	}
	if decision.Confidence != 0 {
		t.Errorf("Confidence = %v, want the unreported zero value", decision.Confidence)
	}
	if err := featurestate.ValidateRemoteDecision(decision); err != nil {
		t.Errorf("mapped decision must satisfy the port validator: %v", err)
	}
	if !decision.Positive(0.9) {
		t.Error("0.95 must promote at a 0.9 threshold")
	}
	if decision.Positive(0.96) {
		t.Error("0.95 must not promote above its own probability")
	}
}

// TestJevDeciderHonorsTheThresholdBoundaries covers the exact threshold and both
// ends of the vendor's documented noul scale (requirements 6.8, 6.9).
func TestJevDeciderHonorsTheThresholdBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		probability  string
		threshold    float64
		wantPositive bool
	}{
		{name: "no at any threshold", probability: "0", threshold: 0.9, wantPositive: false},
		{name: "midpoint below threshold", probability: "0.5", threshold: 0.9, wantPositive: false},
		{name: "just below threshold", probability: "0.899999", threshold: 0.9, wantPositive: false},
		{name: "exactly at threshold", probability: "0.9", threshold: 0.9, wantPositive: true},
		{name: "above threshold", probability: "0.91", threshold: 0.9, wantPositive: true},
		{name: "certain yes", probability: "1", threshold: 0.9, wantPositive: true},
		{name: "certain yes at a certain threshold", probability: "1", threshold: 1, wantPositive: true},
		{name: "threshold above the reported probability", probability: "0.9", threshold: 1, wantPositive: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := jevServer(t, jevSuccessHandler(t, nil, jevAnswerBody(tc.probability)))
			decider := jevTestDecider(t, server.URL, 2*time.Second)

			decision, err := decider.Decide(context.Background(), jevMaximalRemoteInput())
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if got := decision.Positive(tc.threshold); got != tc.wantPositive {
				t.Fatalf("probability %v at threshold %v promoted = %v, want %v",
					tc.probability, tc.threshold, got, tc.wantPositive)
			}
		})
	}
}

// TestJevDeciderSendsTheDocumentedRequest proves the wire request matches the
// documented endpoint, method, and headers, and that the egress body is the
// derived state the port allows (requirements 7.1, 7.2, 8.3).
func TestJevDeciderSendsTheDocumentedRequest(t *testing.T) {
	type captured struct {
		method string
		path   string
		header http.Header
		body   []byte
	}
	requests := make(chan captured, 1)
	server := jevServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, jevMaxRequestBytes+1)
		read, _ := r.Body.Read(body)
		requests <- captured{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body[:read]}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(jevAnswerBody("0.95"))); err != nil {
			t.Errorf("write response: %v", err)
		}
	})
	decider := jevTestDecider(t, server.URL+"/v1/systemone", 2*time.Second)

	if _, err := decider.Decide(context.Background(), jevMaximalRemoteInput()); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	got := <-requests
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.path != "/v1/systemone" {
		t.Errorf("path = %q, want the documented evaluation path", got.path)
	}
	if want := "application/json"; got.header.Get("Content-Type") != want {
		t.Errorf("Content-Type = %q, want %q", got.header.Get("Content-Type"), want)
	}
	if want := "Bearer " + jevTestToken; got.header.Get("Authorization") != want {
		t.Errorf("Authorization = %q, want the bearer form of the referenced credential", got.header.Get("Authorization"))
	}
	// Only the two documented adapter-set headers may travel. Go's transport adds
	// Accept-Encoding, Content-Length, and its own User-Agent; nothing else, and in
	// particular no client identity, may appear (requirements 7.1, 7.2).
	for _, forbidden := range []string{
		"Cookie", "Proxy-Authorization", "X-Api-Key", "X-Forwarded-For", "X-Real-Ip",
		"X-Request-Id", "X-Session-Id", "Openai-Organization", "Anthropic-Version",
		"X-Goog-Api-Client", "Openai-Beta", "Referer",
	} {
		if value := got.header.Get(forbidden); value != "" {
			t.Errorf("request carried the %s header %q", forbidden, value)
		}
	}
	for _, forbidden := range []string{"codex", "opencode", "anthropic", "context.compaction"} {
		if strings.Contains(strings.ToLower(got.header.Get("User-Agent")), forbidden) {
			t.Errorf("User-Agent %q reveals derived client evidence", got.header.Get("User-Agent"))
		}
	}
	for name := range got.header {
		switch name {
		case "Accept-Encoding", "Authorization", "Content-Length", "Content-Type", "User-Agent":
		default:
			t.Errorf("unexpected request header %q: %q", name, got.header.Get(name))
		}
	}
	want, err := jevMarshalRequest(jevMaximalRemoteInput(), jevModelLatest)
	if err != nil {
		t.Fatalf("jevMarshalRequest: %v", err)
	}
	if string(got.body) != string(want) {
		t.Fatalf("egress body\n got: %s\nwant: %s", got.body, want)
	}
}

// TestJevDeciderSendsOnlyDerivedFactsForEveryRemoteInputShape walks the closed
// vocabularies and proves the egress document never grows content: only the
// bounded members change (requirements 7.1, 7.2, 7.3).
func TestJevDeciderSendsOnlyDerivedFactsForEveryRemoteInputShape(t *testing.T) {
	bodies := make(chan string, 1)
	server := jevServer(t, func(w http.ResponseWriter, r *http.Request) {
		buffer := make([]byte, jevMaxRequestBytes+1)
		read, _ := r.Body.Read(buffer)
		bodies <- string(buffer[:read])
		if _, err := w.Write([]byte(jevAnswerBody("0.95"))); err != nil {
			t.Errorf("write response: %v", err)
		}
	})
	decider := jevTestDecider(t, server.URL, 2*time.Second)

	inputs := []featurestate.RemoteInput{
		{Operation: "anthropic.messages"},
		{
			Operation:          "openai.chat_completions",
			ClientFamily:       "codex",
			HasAmbiguousClient: false,
			ToolCategories:     0x2,
			WorkspaceClass:     "",
			LocalEvidenceCode:  "",
		},
		{
			Operation:          "openresponses.create",
			HasAmbiguousClient: true,
			ToolCategories:     0x7f,
			WorkspaceClass:     "project_marker",
			LocalEvidenceCode:  "client_family.codex",
		},
	}
	for _, input := range inputs {
		if _, err := decider.Decide(context.Background(), input); err != nil {
			t.Fatalf("Decide(%+v): %v", input, err)
		}
		body := <-bodies
		document := decodeJSONObject(t, []byte(body))
		state, ok := document["state"].(map[string]any)
		if !ok {
			t.Fatalf("state is %T in %s", document["state"], body)
		}
		if keys := sortedKeys(state); len(keys) != len(jevStateMembers) {
			t.Fatalf("state keys = %v, want the %d derived members", keys, len(jevStateMembers))
		}
		if _, ok := document["model"].(string); !ok {
			t.Fatalf("model is %T in %s", document["model"], body)
		}
		questions, ok := document["questions"].(map[string]any)
		if !ok || len(questions) != 1 {
			t.Fatalf("questions = %v, want exactly the single noul question", document["questions"])
		}
		if len(body) > jevMaxRequestBytes {
			t.Fatalf("egress body grew to %d bytes", len(body))
		}
	}
}

// TestJevDeciderRefusesAnUnusableRemoteInputBeforeEgress proves the port's input
// gate runs before any request exists: an operation outside the closed
// vocabulary never reaches the network (requirements 6.7, 7.1, 7.2).
func TestJevDeciderRefusesAnUnusableRemoteInputBeforeEgress(t *testing.T) {
	var calls atomic.Int32
	server := jevServer(t, jevSuccessHandler(t, &calls, jevAnswerBody("0.95")))
	decider := jevTestDecider(t, server.URL, 2*time.Second)

	for _, input := range []featurestate.RemoteInput{
		{},
		{Operation: "openai.chat_completions", ClientFamily: "cursor"},
		{Operation: "openai.chat_completions", HasAmbiguousClient: true, ClientFamily: "codex"},
		{Operation: "openai.chat_completions", ToolCategories: 1 << 15},
		{Operation: "openai.chat_completions", WorkspaceClass: "/srv/work"},
		{Operation: "openai.chat_completions", LocalEvidenceCode: "EvidenceCodeRemoteAboveThreshold"},
	} {
		decision, err := decider.Decide(context.Background(), input)
		if err == nil {
			t.Fatalf("input %+v produced %+v", input, decision)
		}
		if !errors.Is(err, featurestate.ErrInvalidRemoteInput) {
			t.Fatalf("input %+v error %v does not chain to ErrInvalidRemoteInput", input, err)
		}
		if got := JevFailureKindOf(err); got != JevFailureInputRefused {
			t.Fatalf("input %+v failure kind = %q, want %q", input, got, JevFailureInputRefused)
		}
		if decision != (featurestate.RemoteDecision{}) {
			t.Fatalf("a refused input must yield the zero decision, got %+v", decision)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("remote attempts = %d, want none before egress", calls.Load())
	}
}

// TestJevDeciderFailsOpenOnEveryVendorStatus is the status matrix: every
// documented failure and every refusal degrades to a bounded remote error after
// exactly one attempt, never a retry storm and never a user-visible failure
// (requirements 6.7, 6.9, 12.8).
func TestJevDeciderFailsOpenOnEveryVendorStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantKind JevFailure
	}{
		{name: "missing or invalid api key", status: http.StatusUnauthorized, wantKind: JevFailureCredentialMissing},
		{name: "forbidden", status: http.StatusForbidden, wantKind: JevFailureCredentialMissing},
		{name: "unprocessable entity", status: http.StatusUnprocessableEntity, wantKind: JevFailureRequestRejected},
		{name: "rate limited", status: http.StatusTooManyRequests, wantKind: JevFailureRateLimited},
		{name: "internal server error", status: http.StatusInternalServerError, wantKind: JevFailureServerUnavailable},
		{name: "bad gateway", status: http.StatusBadGateway, wantKind: JevFailureServerUnavailable},
		{name: "service unavailable", status: http.StatusServiceUnavailable, wantKind: JevFailureServerUnavailable},
		{name: "gateway timeout", status: http.StatusGatewayTimeout, wantKind: JevFailureServerUnavailable},
		{name: "overloaded", status: jevStatusOverloaded, wantKind: JevFailureServerUnavailable},
		{name: "permanent redirect", status: http.StatusMovedPermanently, wantKind: JevFailureRedirectRefused},
		{name: "found", status: http.StatusFound, wantKind: JevFailureRedirectRefused},
		{name: "see other", status: http.StatusSeeOther, wantKind: JevFailureRedirectRefused},
		{name: "temporary redirect", status: http.StatusTemporaryRedirect, wantKind: JevFailureRedirectRefused},
		{name: "permanent redirect alias", status: http.StatusPermanentRedirect, wantKind: JevFailureRedirectRefused},
		{name: "multiple choices", status: http.StatusMultipleChoices, wantKind: JevFailureRedirectRefused},
		{name: "unknown informational 3xx", status: 305, wantKind: JevFailureRedirectRefused},
		{name: "no content", status: http.StatusNoContent, wantKind: JevFailureRequestRejected},
		{name: "created", status: http.StatusCreated, wantKind: JevFailureRequestRejected},
		{name: "not found", status: http.StatusNotFound, wantKind: JevFailureRequestRejected},
		{name: "teapot", status: http.StatusTeapot, wantKind: JevFailureRequestRejected},
		{name: "not modified", status: http.StatusNotModified, wantKind: JevFailureRequestRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := jevServer(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				if tc.status == http.StatusNoContent || tc.status == http.StatusNotModified {
					return
				}
				// A vendor error body must never reach the caller: it is written
				// with a credential-shaped string so a leak is observable.
				if _, err := w.Write([]byte(`{"error":{"message":"` + jevTestToken + `"}}`)); err != nil {
					t.Errorf("write error body: %v", err)
				}
			})
			decider := jevTestDecider(t, server.URL, 2*time.Second)

			decision, err := decider.Decide(context.Background(), jevMaximalRemoteInput())
			if err == nil {
				t.Fatalf("status %d produced %+v", tc.status, decision)
			}
			if got := JevFailureKindOf(err); got != tc.wantKind {
				t.Fatalf("status %d failure kind = %q, want %q", tc.status, got, tc.wantKind)
			}
			if decision != (featurestate.RemoteDecision{}) {
				t.Fatalf("status %d yielded %+v, want the zero decision", tc.status, decision)
			}
			if calls.Load() != 1 {
				t.Fatalf("status %d produced %d attempts, want exactly one", tc.status, calls.Load())
			}
			assertNoCredentialLeak(t, err)
			if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Errorf("error text does not name the refused status %d: %v", tc.status, err)
			}
			if strings.Contains(err.Error(), "message") {
				t.Errorf("error text carried the vendor body: %v", err)
			}
		})
	}
}

// TestJevDeciderFailsOpenOnAMalformedSuccess proves a 200 that cannot be mapped
// is a remote error rather than a silent default (requirements 6.9, 12.8).
func TestJevDeciderFailsOpenOnAMalformedSuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "html error page", body: "<html>gateway timeout</html>"},
		{name: "truncated json", body: `{"model":"jev-1.13.0","answers":{`},
		{name: "trailing garbage", body: jevAnswerBody("0.95") + " oops"},
		{name: "missing answers", body: `{"model":"jev-1.13.0","usage":{"input_tokens":1,"output_tokens":1}}`},
		{name: "wrong answer type", body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
			`":{"type":"choice","choice":"yes","confidence":0.9}},"usage":{"input_tokens":1,"output_tokens":1}}`},
		{name: "missing noul", body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
			`":{"type":"noul"}},"usage":{"input_tokens":1,"output_tokens":1}}`},
		{name: "probability above one", body: jevAnswerBody("1.5")},
		{name: "probability below zero", body: jevAnswerBody("-0.1")},
		{name: "empty body", body: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := jevServer(t, jevSuccessHandler(t, &calls, tc.body))
			decider := jevTestDecider(t, server.URL, 2*time.Second)

			decision, err := decider.Decide(context.Background(), jevMaximalRemoteInput())
			if err == nil {
				t.Fatalf("body %q produced %+v", tc.body, decision)
			}
			if got := JevFailureKindOf(err); got != JevFailureMalformedResponse {
				t.Fatalf("failure kind = %q, want %q", got, JevFailureMalformedResponse)
			}
			if !errors.Is(err, featurestate.ErrInvalidRemoteDecision) {
				t.Fatalf("error %v does not chain to ErrInvalidRemoteDecision", err)
			}
			if decision != (featurestate.RemoteDecision{}) {
				t.Fatalf("a malformed response yielded %+v", decision)
			}
			if calls.Load() != 1 {
				t.Fatalf("malformed body produced %d attempts, want exactly one", calls.Load())
			}
			assertNoCredentialLeak(t, err)
		})
	}
}

// TestJevDeciderRefusesAnOversizedResponse proves the response bound is enforced
// end to end against a server that would send far more than the adapter accepts.
func TestJevDeciderRefusesAnOversizedResponse(t *testing.T) {
	var written atomic.Int64
	server := jevServer(t, func(w http.ResponseWriter, _ *http.Request) {
		chunk := make([]byte, 8<<10)
		for i := range chunk {
			chunk[i] = 'x'
		}
		for written.Load() < int64(jevMaxResponseBytes*8) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			written.Add(int64(len(chunk)))
		}
	})
	decider := jevTestDecider(t, server.URL, 10*time.Second)

	decision, err := decider.Decide(context.Background(), jevMaximalRemoteInput())
	if err == nil {
		t.Fatalf("an oversized body produced %+v", decision)
	}
	if got := JevFailureKindOf(err); got != JevFailureResponseOversized {
		t.Fatalf("failure kind = %q, want %q", got, JevFailureResponseOversized)
	}
	if decision != (featurestate.RemoteDecision{}) {
		t.Fatalf("an oversized body yielded %+v", decision)
	}
	if written.Load() < int64(jevMaxResponseBytes) {
		t.Fatalf("the test server wrote only %d bytes; it must exceed the %d byte bound",
			written.Load(), jevMaxResponseBytes)
	}
	assertNoCredentialLeak(t, err)
}

// TestJevDeciderEnforcesTheConfiguredHardTimeout proves the generation timeout
// bounds the whole exchange, including a body that never arrives
// (requirements 6.7, 6.9, 12.8).
func TestJevDeciderEnforcesTheConfiguredHardTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		stall   time.Duration
	}{
		{name: "at the configured floor", timeout: featurestate.MinRemoteTimeout, stall: 5 * time.Second},
		{name: "mid range", timeout: 100 * time.Millisecond, stall: 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := jevReleaseHandler(t)
			server := jevServer(t, func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-release:
				case <-time.After(tc.stall):
					w.WriteHeader(http.StatusOK)
				}
			})
			decider := jevTestDecider(t, server.URL, tc.timeout)

			started := time.Now()
			decision, err := decider.Decide(context.Background(), jevMaximalRemoteInput())
			elapsed := time.Since(started)
			if err == nil {
				t.Fatalf("a stalled endpoint produced %+v", decision)
			}
			if got := JevFailureKindOf(err); got != JevFailureTimeout {
				t.Fatalf("failure kind = %q, want %q", got, JevFailureTimeout)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error %v does not chain to context.DeadlineExceeded", err)
			}
			if decision != (featurestate.RemoteDecision{}) {
				t.Fatalf("a timeout yielded %+v", decision)
			}
			if elapsed > tc.timeout+2*time.Second {
				t.Fatalf("timeout of %v took %v to surface", tc.timeout, elapsed)
			}
			assertNoCredentialLeak(t, err)
		})
	}
}

// TestJevDeciderClassifiesATimeoutOrCancellationWhileTheBodyArrives covers the
// body phase specifically. The header-phase case above stalls before any status
// line, which is the easy path. A slow vendor answers 200 and flushes headers,
// then streams the body, so a stall there is the common real failure. If the
// read error were discarded the deadline would surface as a generic transport
// failure and requirements 9.2/9.3 would label the dominant vendor timeout
// remote_error instead of remote_timeout.
func TestJevDeciderClassifiesATimeoutOrCancellationWhileTheBodyArrives(t *testing.T) {
	// headersArrived proves the handler got past WriteHeader and Flush, so the
	// timeout can only fire while the body is being read.
	for _, tc := range []struct {
		name       string
		wantKind   JevFailure
		wantIs     error
		stallAfter func(chan struct{})
	}{
		{
			name:     "hard timeout while the body streams",
			wantKind: JevFailureTimeout,
			wantIs:   context.DeadlineExceeded,
		},
		{
			name:     "caller cancellation while the body streams",
			wantKind: JevFailureCanceled,
			wantIs:   context.Canceled,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headersArrived := make(chan struct{})
			release := jevReleaseHandler(t)
			server := jevServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				close(headersArrived)
				select {
				case <-r.Context().Done():
				case <-release:
				case <-time.After(30 * time.Second):
				}
			})

			timeout := 150 * time.Millisecond
			decider := jevTestDecider(t, server.URL, timeout)
			ctx := context.Background()
			// An explicit cancel rather than a parent deadline: a parent deadline
			// would be reported as the adapter's own timeout and could not
			// discriminate the two. The cancel is triggered only after the headers
			// are observed, so it lands inside the adapter's timeout window while
			// the body is still stalled.
			var cancel context.CancelFunc
			if tc.wantKind == JevFailureCanceled {
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
			}

			type outcome struct {
				decision featurestate.RemoteDecision
				err      error
				elapsed  time.Duration
			}
			done := make(chan outcome, 1)
			go func() {
				started := time.Now()
				decision, err := decider.Decide(ctx, jevMaximalRemoteInput())
				done <- outcome{decision: decision, err: err, elapsed: time.Since(started)}
			}()

			select {
			case <-headersArrived:
			case got := <-done:
				t.Fatalf("the call returned before the headers arrived: %v", got.err)
			case <-time.After(10 * time.Second):
				t.Fatal("the handler never reached the body phase")
			}

			if cancel != nil {
				cancel()
			}

			got := <-done
			if got.err == nil {
				t.Fatalf("a stalled body produced %+v", got.decision)
			}
			if kind := JevFailureKindOf(got.err); kind != tc.wantKind {
				t.Fatalf("failure kind = %q, want %q (err %v)", kind, tc.wantKind, got.err)
			}
			if !errors.Is(got.err, tc.wantIs) {
				t.Fatalf("error %v does not chain to %v", got.err, tc.wantIs)
			}
			if got.decision != (featurestate.RemoteDecision{}) {
				t.Fatalf("a stalled body yielded %+v", got.decision)
			}
			if got.elapsed > 10*time.Second {
				t.Fatalf("a body-phase stall took %v to surface", got.elapsed)
			}
			assertNoCredentialLeak(t, got.err)
		})
	}
}

// TestJevDeciderStopsPromptlyOnCallerCancellation proves caller cancellation is
// authoritative and surfaces promptly (requirements 6.7, 6.9, 12.8).
func TestJevDeciderStopsPromptlyOnCallerCancellation(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := jevReleaseHandler(t)
	server := jevServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		case <-time.After(30 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	})
	decider := jevTestDecider(t, server.URL, featurestate.MaxRemoteTimeout)

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan error, 1)
	go func() {
		_, err := decider.Decide(ctx, jevMaximalRemoteInput())
		results <- err
	}()
	<-entered
	cancel()

	select {
	case err := <-results:
		if err == nil {
			t.Fatal("a canceled call produced a decision")
		}
		if got := JevFailureKindOf(err); got != JevFailureCanceled {
			t.Fatalf("failure kind = %q, want %q", got, JevFailureCanceled)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error %v does not chain to context.Canceled", err)
		}
		assertNoCredentialLeak(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not surface promptly")
	}
}

// TestJevDeciderRefusesARedirect proves no redirect is followed, so the bearer
// token can never be forwarded to another origin the adapter was not configured
// against (design.md "Jev adapter"; requirements 6.7, 7.1).
func TestJevDeciderRefusesARedirect(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		buildTarget func(origin string) string
	}{
		{
			name:        "temporary redirect to another origin",
			status:      http.StatusTemporaryRedirect,
			buildTarget: func(origin string) string { return origin + "/v1/systemone" },
		},
		{
			name:        "permanent redirect to another origin",
			status:      http.StatusMovedPermanently,
			buildTarget: func(origin string) string { return origin + "/v1/systemone" },
		},
		{
			name:        "found redirect to another origin",
			status:      http.StatusFound,
			buildTarget: func(origin string) string { return origin + "/elsewhere" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var secondOriginRequests atomic.Int32
			second := jevServer(t, func(w http.ResponseWriter, r *http.Request) {
				secondOriginRequests.Add(1)
				if strings.Contains(r.Header.Get("Authorization"), jevTestToken) {
					t.Errorf("the bearer token was forwarded to a second origin")
				}
				if _, err := w.Write([]byte(jevAnswerBody("0.99"))); err != nil {
					t.Errorf("write response: %v", err)
				}
			})
			var firstOriginRequests atomic.Int32
			first := jevServer(t, func(w http.ResponseWriter, _ *http.Request) {
				firstOriginRequests.Add(1)
				w.Header().Set("Location", tc.buildTarget(second.URL))
				w.WriteHeader(tc.status)
			})
			decider := jevTestDecider(t, first.URL, 2*time.Second)

			decision, err := decider.Decide(context.Background(), jevMaximalRemoteInput())
			if err == nil {
				t.Fatalf("a redirect produced %+v", decision)
			}
			if got := JevFailureKindOf(err); got != JevFailureRedirectRefused {
				t.Fatalf("failure kind = %q, want %q", got, JevFailureRedirectRefused)
			}
			if decision != (featurestate.RemoteDecision{}) {
				t.Fatalf("a redirect yielded %+v", decision)
			}
			if secondOriginRequests.Load() != 0 {
				t.Fatalf("the redirect was followed to a second origin %d times", secondOriginRequests.Load())
			}
			if firstOriginRequests.Load() != 1 {
				t.Fatalf("the configured origin saw %d requests, want exactly one", firstOriginRequests.Load())
			}
			assertNoCredentialLeak(t, err)
		})
	}
}

// TestJevDeciderReportsAnUnreachableEndpoint keeps a dial failure a bounded
// remote error rather than a panic or a hang.
func TestJevDeciderReportsAnUnreachableEndpoint(t *testing.T) {
	server := jevServer(t, jevSuccessHandler(t, nil, jevAnswerBody("0.95")))
	endpoint := server.URL
	server.Close()
	decider := jevTestDecider(t, endpoint, 2*time.Second)

	decision, err := decider.Decide(context.Background(), jevMaximalRemoteInput())
	if err == nil {
		t.Fatalf("an unreachable endpoint produced %+v", decision)
	}
	if got := JevFailureKindOf(err); got != JevFailureTransport {
		t.Fatalf("failure kind = %q, want %q", got, JevFailureTransport)
	}
	if decision != (featurestate.RemoteDecision{}) {
		t.Fatalf("an unreachable endpoint yielded %+v", decision)
	}
	assertNoCredentialLeak(t, err)
}

// TestJevDeciderRedactsTheCredentialEverywhere walks the whole failure matrix
// with a distinctive credential and proves it appears in no error, no diagnostic,
// and no refusal (requirements 7.1, 7.5, 8.3).
func TestJevDeciderRedactsTheCredentialEverywhere(t *testing.T) {
	failures := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "unauthorized",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"bad key ` + jevTestToken + `"}}`))
			},
		},
		{
			name: "server error with an echoed request",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"authorization":"` + r.Header.Get("Authorization") + `"}`))
			},
		},
		{
			name: "malformed success",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("not json at all"))
			},
		},
		{
			name: "oversized success",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				chunk := make([]byte, 8<<10)
				for written := 0; written < jevMaxResponseBytes*4; written += len(chunk) {
					if _, err := w.Write(chunk); err != nil {
						return
					}
				}
			},
		},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			server := jevServer(t, failure.handler)
			decider := jevTestDecider(t, server.URL, 5*time.Second)

			_, err := decider.Decide(context.Background(), jevMaximalRemoteInput())
			if err == nil {
				t.Fatal("expected a remote failure")
			}
			assertNoCredentialLeak(t, err)
		})
	}
}

// TestJevDeciderReportsAMissingCredential proves the credential contract has two
// halves. An absent or blank credential rejects the CANDIDATE GENERATION before
// publication (requirement 6.10), and a credential that becomes unusable after
// publication is refused per call without any request (requirements 7.1, 8.3).
func TestJevDeciderReportsAMissingCredential(t *testing.T) {
	t.Run("absent at publication", func(t *testing.T) {
		var calls atomic.Int32
		server := jevServer(t, jevSuccessHandler(t, &calls, jevAnswerBody("0.95")))
		t.Setenv(jevTestCredentialEnv, "")

		decider, err := NewJevDecider(jevTestPolicy(t, 2*time.Second), withJevEndpoint(server.URL))
		if err == nil {
			t.Fatalf("a generation with an absent credential published %T", decider)
		}
		if !errors.Is(err, featurestate.ErrRemoteNotConfigured) {
			t.Fatalf("error = %v, want it to wrap ErrRemoteNotConfigured", err)
		}
		if decider != nil {
			t.Fatal("a refused generation must not hand back a decider")
		}
		if calls.Load() != 0 {
			t.Fatalf("a refused generation still produced %d requests", calls.Load())
		}
	})

	t.Run("whitespace only at publication", func(t *testing.T) {
		server := jevServer(t, jevSuccessHandler(t, nil, jevAnswerBody("0.95")))
		t.Setenv(jevTestCredentialEnv, "   ")

		if _, err := NewJevDecider(jevTestPolicy(t, 2*time.Second), withJevEndpoint(server.URL)); err == nil {
			t.Fatal("a generation with a whitespace credential was published")
		}
	})

	t.Run("unset after publication", func(t *testing.T) {
		var calls atomic.Int32
		server := jevServer(t, jevSuccessHandler(t, &calls, jevAnswerBody("0.95")))
		t.Setenv(jevTestCredentialEnv, jevTestToken)
		decider := jevTestDecider(t, server.URL, 2*time.Second)

		t.Setenv(jevTestCredentialEnv, "")
		_, unsetErr := decider.Decide(context.Background(), jevMaximalRemoteInput())
		if unsetErr == nil {
			t.Fatal("expected a credential unset after publication to be refused")
		}
		if got := JevFailureKindOf(unsetErr); got != JevFailureCredentialMissing {
			t.Fatalf("failure kind = %q, want %q", got, JevFailureCredentialMissing)
		}
		if calls.Load() != 0 {
			t.Fatalf("an unset credential still produced %d requests", calls.Load())
		}

		t.Setenv(jevTestCredentialEnv, jevTestToken)
		decision, err := decider.Decide(context.Background(), jevMaximalRemoteInput())
		if err != nil {
			t.Fatalf("a credential restored after publication was not resolved: %v", err)
		}
		if decision.CodingProbability != 0.95 {
			t.Fatalf("CodingProbability = %v, want 0.95", decision.CodingProbability)
		}
	})
}

// TestNewJevDeciderUsesTheDocumentedVendorPosture pins the adapter's defaults:
// the documented endpoint, the documented model alias, the referenced credential
// name, the validated generation timeout, and a client that is neither the
// package-global client nor a redirect follower (requirements 6.1, 6.7, 8.3;
// design.md "Jev adapter").
func TestNewJevDeciderUsesTheDocumentedVendorPosture(t *testing.T) {
	t.Setenv(jevTestCredentialEnv, jevTestToken)

	policy := jevTestPolicy(t, 1500*time.Millisecond)
	decider, err := NewJevDecider(policy)
	if err != nil {
		t.Fatalf("NewJevDecider: %v", err)
	}
	concrete, ok := decider.(*jevDecider)
	if !ok {
		t.Fatalf("NewJevDecider returned %T, want the adapter", decider)
	}
	if concrete.endpoint != jevEndpointDefault {
		t.Errorf("endpoint = %q, want the documented vendor endpoint", concrete.endpoint)
	}
	if concrete.endpoint != "https://api.typesafe.ai/v1/systemone" {
		t.Errorf("endpoint %q drifted from the documented vendor endpoint", concrete.endpoint)
	}
	if concrete.model != jevModelLatest {
		t.Errorf("model = %q, want the documented alias", concrete.model)
	}
	if concrete.credentialEnv != policy.CredentialReference() {
		t.Errorf("credential reference = %q, want %q", concrete.credentialEnv, policy.CredentialReference())
	}
	if concrete.credentialEnv != jevTestCredentialEnv {
		t.Errorf("credential reference = %q, want the configured environment name", concrete.credentialEnv)
	}
	if concrete.timeout != 1500*time.Millisecond {
		t.Errorf("timeout = %v, want the configured generation timeout", concrete.timeout)
	}
	if concrete.client == nil {
		t.Fatal("the adapter must own an explicit client")
	}
	if concrete.client.Transport == nil {
		t.Error("the adapter must own an explicit transport, not the default one")
	}
	if concrete.client.CheckRedirect == nil {
		t.Error("the adapter must refuse redirects")
	}
}

// TestNewJevDeciderAppliesTheConfiguredTimeoutBounds proves the adapter serves
// whatever the validated posture allows and clamps nothing: both published bounds
// are honoured exactly as configured (requirements 6.7, 8.4).
func TestNewJevDeciderAppliesTheConfiguredTimeoutBounds(t *testing.T) {
	t.Setenv(jevTestCredentialEnv, jevTestToken)

	for _, timeout := range []time.Duration{
		featurestate.MinRemoteTimeout,
		featurestate.MaxRemoteTimeout,
		750 * time.Millisecond,
	} {
		decider, err := NewJevDecider(jevTestPolicy(t, timeout))
		if err != nil {
			t.Fatalf("NewJevDecider(timeout=%v): %v", timeout, err)
		}
		concrete, ok := decider.(*jevDecider)
		if !ok {
			t.Fatalf("NewJevDecider returned %T, want the adapter", decider)
		}
		if concrete.timeout != timeout {
			t.Fatalf("timeout = %v, want the configured %v", concrete.timeout, timeout)
		}
	}
}

// TestNewJevDeciderRefusesUnusablePostures proves no adapter is constructed for a
// posture that must never reach the network or that carries an unusable origin
// (requirements 6.1, 6.2, 6.10, 8.4).
func TestNewJevDeciderRefusesUnusablePostures(t *testing.T) {
	t.Setenv(jevTestCredentialEnv, jevTestToken)

	for _, tc := range []struct {
		name    string
		policy  featurestate.RemotePolicy
		options []JevOption
		wantErr bool
	}{
		{name: "the zero posture", policy: featurestate.RemotePolicy{}, wantErr: true},
		{
			name:    "a relative endpoint",
			policy:  jevTestPolicy(t, time.Second),
			options: []JevOption{withJevEndpoint("/v1/systemone")},
			wantErr: true,
		},
		{
			name:    "a plaintext non-loopback endpoint",
			policy:  jevTestPolicy(t, time.Second),
			options: []JevOption{withJevEndpoint("http://api.typesafe.ai/v1/systemone")},
			wantErr: true,
		},
		{
			name:    "an endpoint carrying a credential",
			policy:  jevTestPolicy(t, time.Second),
			options: []JevOption{withJevEndpoint("https://key@api.typesafe.ai/v1/systemone")},
			wantErr: true,
		},
		{
			name:    "an empty endpoint",
			policy:  jevTestPolicy(t, time.Second),
			options: []JevOption{withJevEndpoint("")},
			wantErr: true,
		},
		{
			name:   "the hybrid posture",
			policy: jevHybridPolicy(t, time.Second),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decider, err := NewJevDecider(tc.policy, tc.options...)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("posture %+v produced an adapter", tc.policy)
				}
				if decider != nil {
					t.Fatal("a refused posture must not yield an adapter")
				}
				if !errors.Is(err, featurestate.ErrRemoteNotConfigured) {
					t.Fatalf("error %v does not chain to ErrRemoteNotConfigured", err)
				}
				if strings.Contains(err.Error(), jevTestToken) {
					t.Fatalf("construction error leaked the credential: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("posture %+v was refused: %v", tc.policy, err)
			}
			if decider == nil {
				t.Fatal("an accepted posture produced no adapter")
			}
		})
	}
}

// TestJevAdapterEmitsNoDiagnostic proves the adapter has no logging path at all, so
// requirement 7.5 holds by construction rather than by inspection: neither vendor
// payloads nor credentials can reach an ordinary log because no logger is imported.
func TestJevAdapterEmitsNoDiagnostic(t *testing.T) {
	t.Parallel()

	forbidden := map[string]bool{"log": true, "log/slog": true, "log/syslog": true}
	for _, name := range []string{"jev_client.go", "jev_wire.go"} {
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			if forbidden[path] {
				t.Errorf("%s imports %q; the adapter must have no diagnostic path", name, path)
			}
		}
	}
}

// TestJevDeciderRefusesAnOversizedEgressBeforeAnyRequest proves the refusal built
// by jevMarshalRequest actually stops the call: no request reaches the endpoint and
// no decision is returned (requirements 6.7, 7.2).
func TestJevDeciderRefusesAnOversizedEgressBeforeAnyRequest(t *testing.T) {
	var calls atomic.Int32
	server := jevServer(t, jevSuccessHandler(t, &calls, jevAnswerBody("0.99")))
	t.Setenv(jevTestCredentialEnv, jevTestToken)
	decider := &jevDecider{
		endpoint:      server.URL,
		model:         strings.Repeat("m", jevMaxRequestBytes),
		credentialEnv: jevTestCredentialEnv,
		timeout:       2 * time.Second,
		client:        jevNewClient(),
	}

	decision, err := decider.Decide(context.Background(), jevMaximalRemoteInput())
	if err == nil {
		t.Fatalf("an oversized egress produced %+v", decision)
	}
	if got := JevFailureKindOf(err); got != JevFailureRequestRefused {
		t.Fatalf("failure kind = %q, want %q", got, JevFailureRequestRefused)
	}
	if decision != (featurestate.RemoteDecision{}) {
		t.Fatalf("a refused egress yielded %+v", decision)
	}
	if calls.Load() != 0 {
		t.Fatalf("an oversized egress still produced %d requests", calls.Load())
	}
	assertNoCredentialLeak(t, err)
}

// TestJevDeciderClosesTheVendorResponseBody proves the adapter always releases the
// vendor response, including on the refusal paths where the body is deliberately
// never read, so a slow or endless vendor body cannot pin a connection or a
// goroutine (requirements 6.7, 6.9).
func TestJevDeciderClosesTheVendorResponseBody(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response func(body *trackedBody) *http.Response
	}{
		{
			name: "decoded success",
			response: func(body *trackedBody) *http.Response {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       body.withContent(jevAnswerBody("0.95")),
					Header:     make(http.Header),
				}
			},
		},
		{
			name: "refused status with an unread body",
			response: func(body *trackedBody) *http.Response {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       body.withContent(`{"error":"` + jevTestToken + `"}`),
					Header:     make(http.Header),
				}
			},
		},
		{
			name: "unreadable body",
			response: func(body *trackedBody) *http.Response {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       body,
					Header:     make(http.Header),
				}
			},
		},
		{
			name: "oversized body",
			response: func(body *trackedBody) *http.Response {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       body.withContent(strings.Repeat("x", jevMaxResponseBytes+16)),
					Header:     make(http.Header),
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{}
			t.Setenv(jevTestCredentialEnv, jevTestToken)
			decider := &jevDecider{
				endpoint:      "https://api.typesafe.ai/v1/systemone",
				model:         jevModelLatest,
				credentialEnv: jevTestCredentialEnv,
				timeout:       time.Second,
				client:        &http.Client{Transport: staticRoundTripper{response: tc.response(body)}},
			}
			_, _ = decider.Decide(context.Background(), jevMaximalRemoteInput())
			if body.closes != 1 {
				t.Fatalf("response body closed %d times, want exactly once", body.closes)
			}
		})
	}
}

// staticRoundTripper answers every request with one prepared response, so a test can
// observe the adapter's own body handling without a socket.
type staticRoundTripper struct {
	response *http.Response
}

func (t staticRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return t.response, nil
}

// trackedBody records how often the adapter closed the vendor response body.
type trackedBody struct {
	content string
	reads   int
	closes  int
}

func (b *trackedBody) withContent(content string) *trackedBody {
	b.content = content
	return b
}

func (b *trackedBody) Read(p []byte) (int, error) {
	b.reads++
	if b.content == "" {
		return 0, errors.New("connection reset by the vendor")
	}
	n := copy(p, b.content)
	b.content = b.content[n:]
	return n, nil
}

func (b *trackedBody) Close() error {
	b.closes++
	return nil
}

// assertNoCredentialLeak fails when the credential appears anywhere in an error
// chain, message, or unwrapped cause (requirements 7.1, 7.5).
func assertNoCredentialLeak(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for range 8 {
		if strings.Contains(err.Error(), jevTestToken) {
			t.Fatalf("error leaked the credential: %v", err)
		}
		text := string(mustMarshalError(t, err))
		if strings.Contains(text, jevTestToken) {
			t.Fatalf("marshalled error leaked the credential: %s", text)
		}
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return
		}
		err = unwrapped
	}
}

// mustMarshalError renders an error value through JSON so a token hidden in a
// marshalled struct field is also observable.
func mustMarshalError(t *testing.T, err error) []byte {
	t.Helper()
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		return []byte{}
	}
	return encoded
}

func jevHybridPolicy(t *testing.T, timeout time.Duration) featurestate.RemotePolicy {
	t.Helper()
	policy, err := featurestate.NewRemotePolicy(featurestate.Config{
		Mode: featurestate.ModeHybrid,
		Remote: &featurestate.RemoteConfig{
			Provider:              "jev",
			APIKeyEnv:             jevTestCredentialEnv,
			Timeout:               timeout,
			MaxAttemptsPerSession: 1,
			LeaseTTL:              timeout + featurestate.RemoteLeaseSafetyMargin + time.Second,
			PositiveThreshold:     0.9,
		},
	})
	if err != nil {
		t.Fatalf("build a validated hybrid policy: %v", err)
	}
	return policy
}
