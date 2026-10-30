package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/option"
)

func TestNewBackend_configErrorsReturnErrorBackendWithHostedCaps(t *testing.T) {
	t.Parallel()

	be := NewBackend(BackendSpec{ID: "test", APIKey: "sk-test"})
	assertHostedBackend(t, be)
	_, err := be.Open(context.Background(), invokeTestCall(), invokeTestCandidate("gpt-test"))
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("Open error = %v", err)
	}
}

func TestNewBackend_invalidCredentialConfigReturnsErrorBackend(t *testing.T) {
	t.Parallel()

	be := NewBackend(BackendSpec{
		ID:          "test",
		BaseURL:     "http://127.0.0.1",
		Credentials: []credpool.Credential{{ID: "dup", Secret: "sk-a"}, {ID: "dup", Secret: "sk-b"}},
	})
	assertHostedBackend(t, be)
	_, err := be.Open(context.Background(), invokeTestCall(), invokeTestCandidate("gpt-test"))
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("Open error = %v", err)
	}
}

func TestNewBackend_transportCapsIncludeChatAndResponses(t *testing.T) {
	t.Parallel()

	be := NewBackend(validBackendSpec("http://127.0.0.1"))
	if !be.TransportCaps.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeStreaming) {
		t.Fatal("chat streaming support missing")
	}
	if !be.TransportCaps.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeNonStreaming) {
		t.Fatal("chat non-streaming support missing")
	}
	if !be.TransportCaps.Supports(aipapi.OperationOpenAIResponses, aipapi.TransportModeStreaming) {
		t.Fatal("responses streaming support missing")
	}
	if !be.TransportCaps.Supports(aipapi.OperationOpenAIResponses, aipapi.TransportModeNonStreaming) {
		t.Fatal("responses non-streaming support missing")
	}
}

func TestNewBackend_resolveCapsUsesModelResolver(t *testing.T) {
	t.Parallel()

	be := NewBackend(validBackendSpec("http://127.0.0.1"))
	caps := be.ResolveCaps(context.Background(), invokeTestCall(), invokeTestCandidate("gpt-3.5-test"))
	if _, ok := caps[aipapi.CapabilityVision]; ok {
		t.Fatalf("expected hosted model narrowing to remove vision: %+v", caps)
	}
}

func TestNewBackend_routesByFlavor(t *testing.T) {
	t.Parallel()
	rec := newInvokeRecorder()
	srv := newInvokeServer(t, rec)

	be := NewBackend(validBackendSpec(srv.URL))
	chatCall := invokeTestCall()
	chatCall.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	es, err := be.Open(context.Background(), chatCall, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasTextDelta(drainInvokeEvents(t, es), "chat-ns-ok") {
		t.Fatal("expected chat route")
	}

	respCall := invokeTestCall()
	respCall.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	respCall.Extensions = map[string]json.RawMessage{"flavor": json.RawMessage(`"responses"`)}
	es, err = be.Open(context.Background(), respCall, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasTextDelta(drainInvokeEvents(t, es), "responses-ns-ok") {
		t.Fatal("expected responses route")
	}
}

func TestNewBackend_nilContextReturnsProviderError(t *testing.T) {
	t.Parallel()

	be := NewBackend(validBackendSpec("http://127.0.0.1"))
	_, err := be.Open(nil, invokeTestCall(), invokeTestCandidate("gpt-test"))
	if err == nil {
		t.Fatal("expected nil context error")
	}
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("expected ErrNilContext, got %v", err)
	}
	if !strings.Contains(err.Error(), "test") {
		t.Fatalf("expected provider id in error, got %v", err)
	}
}

func TestNewBackend_forwardsRequestAndClientOptions(t *testing.T) {
	t.Parallel()
	rec := newInvokeRecorder()
	var mu sync.Mutex
	gotHeader := ""
	srv := newInvokeServerWithHook(t, rec, func(r *http.Request) {
		mu.Lock()
		gotHeader = r.Header.Get("X-Test-Client")
		mu.Unlock()
	})

	spec := validBackendSpec(srv.URL)
	var gotCand routing.AttemptCandidate
	spec.ClientOptions = func(_ aipapi.Call, cand routing.AttemptCandidate) []option.RequestOption {
		gotCand = cand
		return []option.RequestOption{option.WithHeader("X-Test-Client", "yes")}
	}
	spec.RequestOptions = func(aipapi.Call) []option.RequestOption {
		return []option.RequestOption{option.WithJSONSet("max_tokens", 19), option.WithJSONDel("stream_options")}
	}
	be := NewBackend(spec)
	call := invokeTestCall()
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	_, err := be.Open(context.Background(), call, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	header := gotHeader
	mu.Unlock()
	if header != "yes" {
		t.Fatalf("client header = %q", header)
	}
	if gotCand.Primary.Model != "gpt-test" {
		t.Fatalf("candidate model = %q, want gpt-test", gotCand.Primary.Model)
	}
	body := rec.lastBody(t)
	if !strings.Contains(string(body), `"max_tokens":19`) {
		t.Fatalf("request option not applied body=%s", string(body))
	}
	if strings.Contains(string(body), "stream_options") {
		t.Fatalf("stream_options should be removed body=%s", string(body))
	}
}

func TestNewBackend_authInvalidRetriesNextCredentialBeforeOutput(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	secrets := []string{}
	srv := newCredentialRetryServer(t, http.StatusUnauthorized, "", &mu, &secrets)
	spec := validBackendSpec(srv.URL)
	spec.APIKey = ""
	spec.Credentials = []credpool.Credential{{ID: "bad", Secret: "bad-key"}, {ID: "good", Secret: "good-key"}}
	be := NewBackend(spec)

	call := invokeTestCall()
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	es, err := be.Open(context.Background(), call, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasTextDelta(drainInvokeEvents(t, es), "chat-ns-ok") {
		t.Fatal("expected retry success text")
	}

	mu.Lock()
	got := append([]string(nil), secrets...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "bad-key" || got[1] != "good-key" {
		t.Fatalf("credential attempts = %v", got)
	}
}

func TestNewBackend_rateLimitRetriesNextCredentialBeforeOutput(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	secrets := []string{}
	srv := newCredentialRetryServer(t, http.StatusTooManyRequests, "7200", &mu, &secrets)
	spec := validBackendSpec(srv.URL)
	spec.APIKey = ""
	spec.Credentials = []credpool.Credential{{ID: "slow", Secret: "slow-key"}, {ID: "good", Secret: "good-key"}}
	spec.RateLimitFallback = time.Millisecond
	be := NewBackend(spec)

	call := invokeTestCall()
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	es, err := be.Open(context.Background(), call, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasTextDelta(drainInvokeEvents(t, es), "chat-ns-ok") {
		t.Fatal("expected retry success text")
	}

	mu.Lock()
	got := append([]string(nil), secrets...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "slow-key" || got[1] != "good-key" {
		t.Fatalf("credential attempts = %v", got)
	}
}

func TestNewBackend_noUsableCredentialIsRecoverablePreOutput(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	secrets := []string{}
	srv := newCredentialRetryServer(t, http.StatusUnauthorized, "", &mu, &secrets)
	spec := validBackendSpec(srv.URL)
	spec.APIKey = ""
	spec.Credentials = []credpool.Credential{{ID: "bad", Secret: "bad-key"}}
	be := NewBackend(spec)
	call := invokeTestCall()
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming

	_, err := be.Open(context.Background(), call, invokeTestCandidate("gpt-test"))
	if !errors.Is(err, aipapi.ErrRecoverablePreOutput) {
		t.Fatalf("expected recoverable pre-output error, got %v", err)
	}
}

func alwaysStatusServer(t *testing.T, status int, attempts *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"message":"forced","type":"server_error","code":"forced"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNewBackend_internalServerErrorIsRecoverablePreOutput(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	srv := alwaysStatusServer(t, http.StatusInternalServerError, &attempts)
	be := NewBackend(validBackendSpec(srv.URL))
	call := invokeTestCall()
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming

	_, err := be.Open(context.Background(), call, invokeTestCandidate("gpt-test"))
	if err == nil {
		t.Fatal("expected error")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected recoverable pre-output for 500, got %v", err)
	}
	if !strings.Contains(err.Error(), "test: ") {
		t.Fatalf("expected backend ID prefix in error, got %v", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts: %d want 1 (no credential rotation on 500)", n)
	}
}

func TestNewBackend_unclassifiedErrorCarriesBackendID(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	srv := alwaysStatusServer(t, http.StatusBadRequest, &attempts)
	be := NewBackend(validBackendSpec(srv.URL))
	call := invokeTestCall()
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming

	_, err := be.Open(context.Background(), call, invokeTestCandidate("gpt-test"))
	if err == nil {
		t.Fatal("expected error")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("did not expect recoverable pre-output for 400: %v", err)
	}
	if !strings.Contains(err.Error(), "test: ") {
		t.Fatalf("expected backend ID prefix in error, got %v", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts: %d want 1 (no credential rotation on 400)", n)
	}
}

func validBackendSpec(baseURL string) BackendSpec {
	return BackendSpec{
		ID:                "test",
		BaseURL:           baseURL,
		APIKey:            "sk-test",
		RateLimitFallback: time.Millisecond,
		SDKMaxRetries:     new(int),
		ResolveModel: func(cand routing.AttemptCandidate, _ aipapi.Call) string {
			return cand.Primary.Model
		},
		ResolveFlavor: func(call aipapi.Call) Flavor {
			if string(call.Extensions["flavor"]) == `"responses"` {
				return FlavorResponses
			}
			return FlavorChat
		},
	}
}

func assertHostedBackend(t *testing.T, be execbackend.Backend) {
	t.Helper()
	if be.Open == nil {
		t.Fatal("Open is nil")
	}
	if len(be.Caps) == 0 {
		t.Fatal("Caps are empty")
	}
	if be.ResolveCaps == nil {
		t.Fatal("ResolveCaps is nil")
	}
}

func newCredentialRetryServer(t *testing.T, status int, retryAfter string, mu *sync.Mutex, secrets *[]string) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		secret := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		*secrets = append(*secrets, secret)
		attempt := len(*secrets)
		mu.Unlock()
		if attempt == 1 {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"retryable","type":"invalid_request_error","code":"retryable"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat-ns","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"chat-ns-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestNewBackend_ReportsCacheTargetIdentityOnCommittedOpen(t *testing.T) {
	t.Parallel()
	rec := newInvokeRecorder()
	srv := newInvokeServer(t, rec)
	be := NewBackend(validBackendSpec(srv.URL))
	ctx, collector := execbackend.WithAttemptIdentityCollector(context.Background())
	call := invokeTestCall()
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	es, err := be.Open(ctx, call, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	if got := collector.Identity().Cache; got == "" {
		t.Fatal("cache target identity was not reported for a committed open")
	}
	_ = es.Close()
}

func TestNewBackend_ReportsOnlyTheSuccessfulCredentialCacheTarget(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	secrets := []string{}
	srv := newCredentialRetryServer(t, http.StatusUnauthorized, "", &mu, &secrets)
	spec := validBackendSpec(srv.URL)
	spec.APIKey = ""
	spec.Credentials = []credpool.Credential{
		{ID: "bad", Secret: "bad-key", RemoteRegion: "us-east-1"},
		{ID: "good", Secret: "good-key", RemoteRegion: "eu-west-1"},
	}
	be := NewBackend(spec)
	ctx, collector := execbackend.WithAttemptIdentityCollector(context.Background())
	call := invokeTestCall()
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	es, err := be.Open(ctx, call, invokeTestCandidate("gpt-test"))
	if err != nil {
		t.Fatal(err)
	}
	_ = es.Close()
	want := attemptCacheTargetFingerprint(srv.URL, string(FlavorChat), "eu-west-1")
	if got := collector.Identity().Cache; got != want {
		t.Fatalf("cache target = %q, want the successful credential's target %q", got, want)
	}
}

func TestNewBackend_ReportsOnlyTheSuccessfulCredentialIdentity(t *testing.T) {
	t.Parallel()
	openIdentity := func(t *testing.T, be execbackend.Backend) string {
		t.Helper()
		ctx, collector := execbackend.WithAttemptIdentityCollector(context.Background())
		call := invokeTestCall()
		call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
		es, err := be.Open(ctx, call, invokeTestCandidate("gpt-test"))
		if err != nil {
			t.Fatal(err)
		}
		_ = es.Close()
		return collector.Identity().Credential
	}

	good := credpool.Credential{ID: "good", Secret: "good-key", RemoteRegion: "eu-west-1"}
	successRec := newInvokeRecorder()
	successSrv := newInvokeServer(t, successRec)
	successSpec := validBackendSpec(successSrv.URL)
	successSpec.APIKey = ""
	successSpec.Credentials = []credpool.Credential{good}
	want := openIdentity(t, NewBackend(successSpec))
	if want == "" {
		t.Fatal("successful credential identity was not reported")
	}

	var mu sync.Mutex
	secrets := []string{}
	rotationSrv := newCredentialRetryServer(t, http.StatusUnauthorized, "", &mu, &secrets)
	rotationSpec := validBackendSpec(rotationSrv.URL)
	rotationSpec.APIKey = ""
	rotationSpec.Credentials = []credpool.Credential{
		{ID: "bad", Secret: "bad-key", RemoteRegion: "us-east-1"},
		good,
	}
	if got := openIdentity(t, NewBackend(rotationSpec)); got != want {
		t.Fatalf("rotation reported credential %q, want the successful credential %q", got, want)
	}
}
