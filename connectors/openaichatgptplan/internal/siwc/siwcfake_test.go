package siwc

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// The fake provider of the refresh and the sign-out.
//
// The fake stands in for the three provider surfaces this slice uses: the
// token endpoint, the revocation endpoint and the OIDC configuration that
// names them.
//
// It RECORDS what it received, because two properties of these operations
// are properties of the traffic: the refresh request must carry the issued
// client id of the registration it renews, and the revocation request must
// carry the refresh token that was saved with that registration.

// fakeProvider is the fake provider.
type fakeProvider struct {
	server *httptest.Server

	mu sync.Mutex
	// discoveryRequests counts the requests the discovery endpoint
	// received.
	discoveryRequests int
	// tokenRequests are the forms the token endpoint received.
	tokenRequests []url.Values
	// revocations are the forms the revocation endpoint received.
	revocations []url.Values
	// tokenStatus is the status the token endpoint answers with. Zero
	// means 200.
	tokenStatus int
	// tokenBody is the raw body the token endpoint answers with. Empty
	// means the documented success body.
	tokenBody string
	// revocationStatus is the status the revocation endpoint answers with.
	// Zero means 200 with an empty body, which is the documented success
	// response.
	revocationStatus int
	// revocationBody is the raw body the revocation endpoint answers
	// with. Empty means the documented empty body.
	revocationBody string
	// revocationBroken drops the connection of the revocation endpoint
	// without an answer, so that the client sees a transport failure.
	revocationBroken bool
	// discoveryStatus is the status the discovery endpoint answers with.
	// Zero means 200.
	discoveryStatus int
	// discoveryBody overrides the discovery document. Empty means the
	// documented one.
	discoveryBody string
	// tokenGate blocks the token handler until it is closed. Nil means
	// the handler answers at once.
	tokenGate chan struct{}
	// tokenGateUsed records that the gate already held one request.
	tokenGateUsed bool
	// revocationGate blocks the revocation handler until it is closed.
	// Nil means the handler answers at once.
	revocationGate chan struct{}
	// onRevocation is called with the revocation form before the
	// endpoint answers. It lets a proof observe the state of the store at
	// the moment the revocation arrives.
	onRevocation func(url.Values)
}

// newFakeProvider starts the fake provider.
func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	fake := &fakeProvider{}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.route))
	t.Cleanup(fake.server.Close)
	return fake
}

// route dispatches the three provider surfaces.
func (f *fakeProvider) route(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		f.serveDiscovery(w)
	case "/api/accounts/oauth/token":
		f.serveToken(w, r)
	case "/api/accounts/oauth/revoke":
		f.serveRevocation(w, r)
	default:
		http.NotFound(w, r)
	}
}

// serveDiscovery answers the OIDC configuration.
func (f *fakeProvider) serveDiscovery(w http.ResponseWriter) {
	f.mu.Lock()
	f.discoveryRequests++
	status := f.discoveryStatus
	body := f.discoveryBody
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	if body == "" {
		body = f.discoveryDocument()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// discoveryRequestCount returns how often the discovery endpoint was
// called.
func (f *fakeProvider) discoveryRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.discoveryRequests
}

// discoveryDocument is the OIDC configuration the fake publishes.
func (f *fakeProvider) discoveryDocument() string {
	return `{"issuer":"` + f.server.URL + `",` +
		`"authorization_endpoint":"` + f.server.URL + `/api/accounts/authorize",` +
		`"token_endpoint":"` + f.server.URL + `/api/accounts/oauth/token",` +
		`"jwks_uri":"` + f.server.URL + `/.well-known/jwks.json",` +
		`"revocation_endpoint":"` + f.server.URL + `/api/accounts/oauth/revoke"}`
}

// serveToken answers the token endpoint.
func (f *fakeProvider) serveToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "unreadable form", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.tokenRequests = append(f.tokenRequests, r.PostForm)
	status := f.tokenStatus
	body := f.tokenBody
	gate := f.tokenGate
	blocked := gate != nil && !f.tokenGateUsed
	if blocked {
		f.tokenGateUsed = true
	}
	f.mu.Unlock()
	// The gate holds ONE request: the one that reaches the provider
	// first. A proof that a second request must NOT reach the provider
	// needs the second request to be answered, or the proof would wait
	// for a response that the gate never sends.
	if blocked {
		<-gate
	}
	if status == 0 {
		status = http.StatusOK
	}
	if body == "" {
		body = f.refreshSuccessBody()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// refreshSuccessBody is the token set a successful refresh answers with.
//
// The replacement refresh token is a different value from the one the
// registration was saved with: a rotation that returned the same token
// would not prove that the replacement was stored.
func (f *fakeProvider) refreshSuccessBody() string {
	return `{"access_token":"refreshed-access-token",` +
		`"refresh_token":"rotated-refresh-token",` +
		`"token_type":"bearer",` +
		`"expires_in":3600,` +
		`"scope":"openid offline_access chatgpt.tokens.use.direct"}`
}

// serveRevocation answers the revocation endpoint.
func (f *fakeProvider) serveRevocation(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "unreadable form", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.revocations = append(f.revocations, r.PostForm)
	observe := f.onRevocation
	broken := f.revocationBroken
	gate := f.revocationGate
	f.mu.Unlock()
	// The observer runs BEFORE the answer is composed, so that a proof
	// can decide what the provider answers.
	if observe != nil {
		observe(r.PostForm)
	}
	if gate != nil {
		<-gate
	}
	if broken {
		// The connection is dropped without an answer: the client sees a
		// transport failure, not a status.
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "unreadable form", http.StatusInternalServerError)
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		_ = connection.Close()
		return
	}
	f.mu.Lock()
	status := f.revocationStatus
	body := f.revocationBody
	f.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if body != "" {
		_, _ = w.Write([]byte(body))
	}
}

// tokenRequestCount returns how often the token endpoint was called.
func (f *fakeProvider) tokenRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tokenRequests)
}

// lastTokenRequest returns the form of the last token request.
func (f *fakeProvider) lastTokenRequest(t *testing.T) url.Values {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tokenRequests) == 0 {
		t.Fatal("the token endpoint was never called")
	}
	return f.tokenRequests[len(f.tokenRequests)-1]
}

// revocationCount returns how often the revocation endpoint was called.
func (f *fakeProvider) revocationCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.revocations)
}

// lastRevocation returns the form of the last revocation request.
func (f *fakeProvider) lastRevocation(t *testing.T) url.Values {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.revocations) == 0 {
		t.Fatal("the revocation endpoint was never called")
	}
	return f.revocations[len(f.revocations)-1]
}

// setTokenResponse replaces the answer of the token endpoint.
func (f *fakeProvider) setTokenResponse(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenStatus = status
	f.tokenBody = body
}

// setRevocationResponse replaces the answer of the revocation endpoint.
//
// A status of zero and an empty body is the documented success response.
func (f *fakeProvider) setRevocationResponse(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revocationStatus = status
	f.revocationBody = body
}

// breakRevocation drops the connection of the revocation endpoint.
func (f *fakeProvider) breakRevocation() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revocationBroken = true
}

// setDiscoveryResponse replaces the answer of the discovery endpoint.
func (f *fakeProvider) setDiscoveryResponse(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discoveryStatus = status
	f.discoveryBody = body
}

// gateToken blocks the token endpoint until the returned function is
// called.
//
// The returned function is IDEMPOTENT: a proof that releases the gate on
// the normal path and again in a deferred cleanup must not panic.
func (f *fakeProvider) gateToken() func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenGate = make(chan struct{})
	gate := f.tokenGate
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// waitForTokenRequest waits until the token endpoint received a request.
//
// The wait is bounded: a proof that waits forever is a proof that hangs.
func (f *fakeProvider) waitForTokenRequest(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f.tokenRequestCount() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the token endpoint was never called")
}

// gateRevocation blocks the revocation endpoint until the returned
// function is called.
//
// The returned function is IDEMPOTENT: a proof that releases the gate on
// the normal path and again in a deferred cleanup must not panic.
func (f *fakeProvider) gateRevocation() func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revocationGate = make(chan struct{})
	gate := f.revocationGate
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// waitForRevocation waits until the revocation endpoint received a request.
//
// The wait is bounded: a proof that waits forever is a proof that hangs.
func (f *fakeProvider) waitForRevocation(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f.revocationCount() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the revocation endpoint was never called")
}

// observeRevocation registers the observer of the revocation request.
func (f *fakeProvider) observeRevocation(observe func(url.Values)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onRevocation = observe
}

// client returns the HTTP client of the fake.
func (f *fakeProvider) client() *http.Client { return f.server.Client() }

// endpoint returns the URL of one of the fake's surfaces.
func (f *fakeProvider) endpoint(path string) string { return f.server.URL + path }
