package siwc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The loopback callback of one attempt.
//
// The listener is the only place where the provider's return path enters
// the connector, so its rules are proven here: it serves one path, it
// validates the state before anything else, and it turns an OAuth error
// into a stopped attempt rather than into a result.

// TestCallbackListener_ServesOnlyTheCallbackPath proves that a request for
// another path is not a callback.
func TestCallbackListener_ServesOnlyTheCallbackPath(t *testing.T) {
	listener, err := newCallbackListener("state-1", time.Minute)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	defer func() { _ = listener.close() }()

	response, err := http.Get("http://" + listener.listener.Addr().String() + "/other")
	if err != nil {
		t.Fatalf("GET the other path: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("the other path answered %d, want 404", response.StatusCode)
	}
	select {
	case outcome := <-listener.outcomes:
		t.Fatalf("the other path produced an outcome: %v", outcome)
	default:
	}
}

// TestCallbackListener_RefusesACallbackWithoutTheState proves that a
// callback that cannot prove it belongs to the attempt is refused and
// produces no result.
func TestCallbackListener_RefusesACallbackWithoutTheState(t *testing.T) {
	listener, err := newCallbackListener("state-1", time.Minute)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	defer func() { _ = listener.close() }()

	target := listener.redirectURI() + "?" + url.Values{"code": {"code-1"}}.Encode()
	response, err := http.Get(target)
	if err != nil {
		t.Fatalf("GET the callback: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("the stateless callback answered %d, want 400", response.StatusCode)
	}
	outcome := <-listener.outcomes
	if !errors.Is(outcome.err, ErrStateMismatch) {
		t.Errorf("the callback produced the error %v, want a state mismatch", outcome.err)
	}
	if outcome.result != nil {
		t.Error("the callback produced a result")
	}
}

// TestCallbackListener_ReportsTheOAuthError proves that an OAuth error ends
// the attempt and carries the provider's own reason.
func TestCallbackListener_ReportsTheOAuthError(t *testing.T) {
	listener, err := newCallbackListener("state-1", time.Minute)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	defer func() { _ = listener.close() }()

	values := url.Values{"state": {"state-1"}, "error": {"access_denied"}}
	response, err := http.Get(listener.redirectURI() + "?" + values.Encode())
	if err != nil {
		t.Fatalf("GET the callback: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Errorf("the denied callback answered %d, want 200", response.StatusCode)
	}
	outcome := <-listener.outcomes
	var callbackErr *CallbackError
	if !errors.As(outcome.err, &callbackErr) {
		t.Fatalf("the callback produced the error %v, want an OAuth error", outcome.err)
	}
	if callbackErr.Code != "access_denied" {
		t.Errorf("the callback error carried the code %q, want access_denied", callbackErr.Code)
	}
	if outcome.result != nil {
		t.Error("the denied callback produced a result")
	}
}

// TestCallbackListener_CarriesTheIssuedClientIDAndScope proves that the
// callback's own values are delivered unchanged.
func TestCallbackListener_CarriesTheIssuedClientIDAndScope(t *testing.T) {
	listener, err := newCallbackListener("state-1", time.Minute)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	defer func() { _ = listener.close() }()

	values := url.Values{
		"state":     {"state-1"},
		"code":      {"code-1"},
		"client_id": {"oaiapp_issued_1"},
		"scope":     {"openid email"},
	}
	response, err := http.Get(listener.redirectURI() + "?" + values.Encode())
	if err != nil {
		t.Fatalf("GET the callback: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	outcome := <-listener.outcomes
	if outcome.err != nil {
		t.Fatalf("the callback produced the error %v", outcome.err)
	}
	if outcome.result.Code != "code-1" {
		t.Errorf("the callback carried the code %q, want code-1", outcome.result.Code)
	}
	if outcome.result.IssuedClientID != "oaiapp_issued_1" {
		t.Errorf("the callback carried the client id %q, want oaiapp_issued_1", outcome.result.IssuedClientID)
	}
	if outcome.result.Scope != "openid email" {
		t.Errorf("the callback carried the scope %q, want %q", outcome.result.Scope, "openid email")
	}
}

// TestCallbackListener_Expires proves that an attempt that is never
// answered closes its listener and fails.
func TestCallbackListener_Expires(t *testing.T) {
	listener, err := newCallbackListener("state-1", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	defer func() { _ = listener.close() }()

	select {
	case <-listener.expired:
	case <-time.After(2 * time.Second):
		t.Fatal("the listener did not expire")
	}
}

// TestCallbackListener_RedirectURIIsTheLoopbackURI proves the shape of the
// selected callback URI: the literal loopback address, a port, and the
// fixed path.
func TestCallbackListener_RedirectURIIsTheLoopbackURI(t *testing.T) {
	listener, err := newCallbackListener("state-1", time.Minute)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	defer func() { _ = listener.close() }()

	uri := listener.redirectURI()
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse the callback URI: %v", err)
	}
	if parsed.Scheme != "http" {
		t.Errorf("the callback URI %q is not an http URI", uri)
	}
	if parsed.Hostname() != LoopbackHost {
		t.Errorf("the callback URI %q is not on the loopback address", uri)
	}
	if parsed.Port() == "" {
		t.Errorf("the callback URI %q carries no port", uri)
	}
	if parsed.Path != CallbackPath {
		t.Errorf("the callback URI %q carries the path %q, want %q", uri, parsed.Path, CallbackPath)
	}
}

// flushedWriter is a response writer that records whether the handler
// flushed its answer.
type flushedWriter struct {
	header  http.Header
	body    bytes.Buffer
	status  int
	flushed bool
}

func (w *flushedWriter) Header() http.Header { return w.header }

func (w *flushedWriter) Write(data []byte) (int, error) { return w.body.Write(data) }

func (w *flushedWriter) WriteHeader(status int) { w.status = status }

func (w *flushedWriter) Flush() { w.flushed = true }

// TestCallbackListener_FlushesTheAnswerBeforeTheOutcomeIsDelivered proves
// that the answer is on the wire before the attempt delivers its result.
//
// The delivery ends the attempt, and the attempt closes its listener. An
// answer that was written but not flushed would be lost when the listener
// closes, and the browser would read a truncated response.
func TestCallbackListener_FlushesTheAnswerBeforeTheOutcomeIsDelivered(t *testing.T) {
	listener, err := newCallbackListener("state-1", time.Minute)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	defer func() { _ = listener.close() }()

	writer := &flushedWriter{header: http.Header{}}
	request := httptest.NewRequest(http.MethodGet, CallbackPath+"?state=state-1&code=code-1", nil)
	listener.ServeHTTP(writer, request)
	if !writer.flushed {
		t.Error("the callback did not flush its answer; a closed attempt truncates the browser's response")
	}
	outcome := <-listener.outcomes
	if outcome.err != nil {
		t.Fatalf("the callback produced the error %v", outcome.err)
	}
}

// TestCallbackListener_FlushesTheRefusalBeforeTheOutcomeIsDelivered proves
// the same for a refused callback.
func TestCallbackListener_FlushesTheRefusalBeforeTheOutcomeIsDelivered(t *testing.T) {
	listener, err := newCallbackListener("state-1", time.Minute)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	defer func() { _ = listener.close() }()

	writer := &flushedWriter{header: http.Header{}}
	request := httptest.NewRequest(http.MethodGet, CallbackPath+"?state=another&code=code-1", nil)
	listener.ServeHTTP(writer, request)
	if !writer.flushed {
		t.Error("the refused callback did not flush its answer")
	}
	outcome := <-listener.outcomes
	if !errors.Is(outcome.err, ErrStateMismatch) {
		t.Fatalf("the callback produced the error %v, want a state mismatch", outcome.err)
	}
}

// TestCallbackListener_AnswersTheBrowserBeforeTheAttemptEnds proves that
// the browser receives a complete answer.
//
// The attempt is closed the moment its result is delivered, and the
// delivery happens while the answer is still being written. A listener
// that did not flush its answer first would truncate the response the
// browser is reading, and the browser would see a failed load.
func TestCallbackListener_AnswersTheBrowserBeforeTheAttemptEnds(t *testing.T) {
	listener, err := newCallbackListener("state-1", time.Minute)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	defer func() { _ = listener.close() }()

	go func() {
		outcome := <-listener.outcomes
		if outcome.err == nil {
			_ = listener.close()
		}
	}()

	values := url.Values{"state": {"state-1"}, "code": {"code-1"}}
	response, err := http.Get(listener.redirectURI() + "?" + values.Encode())
	if err != nil {
		t.Fatalf("the browser did not receive the answer: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Errorf("the callback answered %d, want 200", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("the answer was truncated: %v", err)
	}
	if len(body) == 0 {
		t.Error("the answer carried no body")
	}
}

// TestCallbackListener_ResponseSurvivesTheShutdownOfTheAttempt proves that
// the browser reads a COMPLETE answer when the attempt closes its listener
// while the handler is still finishing.
//
// The handler is held at its return boundary on a channel, so the shutdown
// is a controlled schedule rather than a race. The response has to be
// length-delimited: a chunked response is framed after the handler returns,
// and the shutdown would truncate it.
func TestCallbackListener_ResponseSurvivesTheShutdownOfTheAttempt(t *testing.T) {
	listener, err := newCallbackListener("state-1", time.Minute)
	if err != nil {
		t.Fatalf("newCallbackListener: %v", err)
	}
	defer func() { _ = listener.close() }()

	returned := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listener.ServeHTTP(w, r)
		close(returned)
		<-release
	}))
	server.Config.SetKeepAlivesEnabled(false)
	server.Start()
	// The release runs first: the test server waits for its outstanding
	// handler, and the handler waits for the release.
	defer server.Close()
	defer close(release)

	response, err := server.Client().Get(server.URL + CallbackPath + "?state=state-1&code=code-1")
	if err != nil {
		t.Fatalf("GET the callback: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	<-returned

	if length := response.Header.Get("Content-Length"); length == "" {
		t.Fatal("the callback response is not length-delimited")
	}
	if len(response.TransferEncoding) != 0 {
		t.Fatalf("the callback response is framed as %v, want a length-delimited body", response.TransferEncoding)
	}

	// The attempt closes its listener while the handler is still held.
	_ = server.Config.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("the browser read a truncated answer: %v", err)
	}
	if !strings.Contains(string(body), "Sign-in completed") {
		t.Errorf("the answer body is %q", body)
	}
}

// TestCallbackListener_ResponseSurvivesTheProductionClose proves the same
// property on the production close path: the attempt closes its own
// listener the moment the result is delivered.
func TestCallbackListener_ResponseSurvivesTheProductionClose(t *testing.T) {
	h := newHarness(t)
	h.setDelivery(callbackDelivery{code: "auth-code-1", clientID: "oaiapp_issued_1"})

	record, err := h.authorizer.Register(context.Background(), RegisterRequest{ProfileID: "acct-test"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if record.IssuedClientID != "oaiapp_issued_1" {
		t.Errorf("the registration recorded the issued client id %q", record.IssuedClientID)
	}
	// The simulated browser reports a delivery error when the answer is
	// truncated, so a complete answer keeps the delivery clean.
	if h.browserDeliveryError() != "" {
		t.Errorf("the browser did not receive a complete answer: %s", h.browserDeliveryError())
	}
}

// TestAttempt_AwaitFailsWhenTheAttemptIsClosed proves that a closed attempt
// fails instead of waiting forever.
func TestAttempt_AwaitFailsWhenTheAttemptIsClosed(t *testing.T) {
	h := newHarness(t)
	attempt, err := h.authorizer.BeginRegistration(context.Background(), h.hostID(t))
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	_ = attempt.Close()
	if _, err := attempt.Await(context.Background()); !errors.Is(err, ErrAttemptClosed) && !errors.Is(err, ErrAttemptExpired) {
		t.Fatalf("Await error = %v, want a closed or expired attempt", err)
	}
}
