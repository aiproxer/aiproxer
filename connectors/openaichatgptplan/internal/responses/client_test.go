package responses_test

// Behavior proofs for the public Responses HTTP/SSE transport (task 5.1).
//
// The proofs live at the wire. A reference server records the request the
// connector actually makes, and the source of every answer is the server, not
// a mock of the connector's own helpers:
//
//   - the request reaches ONLY the documented public endpoint, as POST, with
//     the selected registration's standard bearer credential;
//   - the upstream request always streams, for both downstream delivery
//     modes, and the two modes produce the same wire request;
//   - the raw provider events arrive in the provider's order, byte for byte,
//     with comments and keep-alives ignored;
//   - an unusable selection is an explicit error and never an anonymous or
//     cross-profile request;
//   - cancellation reaches the request and releases the connection;
//   - a provider error preserves status, code and request id, and its
//     diagnostic is redacted and bounded.
//
// The bounds themselves (per event, whole stream, error body) are acquisition
// proofs and live in stream_test.go, where the unexported constants and the
// counting body are visible.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	// testProfileID is the local, non-secret key of the selected
	// registration.
	testProfileID = "acct-one"

	// testAccessToken is the credential of the selected registration. It is
	// a recognizable value so that a leak would be visible.
	testAccessToken = "siwc-access-token-for-acct-one"
)

// referenceSSE is the answer of the reference server.
//
// It contains every framing form the transport must handle: comments and
// keep-alives, an event name, a multi-line data payload, non-data fields, an
// event with no data, and an event with no name.
const referenceSSE = ": keep-alive\n\n" +
	"event: response.created\n" +
	"data: {\"type\":\"response.created\",\"sequence_number\":0}\n\n" +
	": keep-alive\n" +
	"event: response.output_text.delta\n" +
	"data: {\"type\":\"response.output_text.delta\",\n" +
	"data: \"delta\":\"hello\"}\n\n" +
	"id: 7\n" +
	"retry: 1000\n" +
	"data: {\"type\":\"response.output_item.added\"}\n\n" +
	"event: response.in_progress\n\n" +
	"event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n"

// referenceEvents is what the reference server sent, in its order.
var referenceEvents = []responses.Event{
	{
		Name: "response.created",
		Data: []byte(`{"type":"response.created","sequence_number":0}`),
	},
	{
		Name: "response.output_text.delta",
		Data: []byte("{\"type\":\"response.output_text.delta\",\n\"delta\":\"hello\"}"),
	},
	{
		Name: "message",
		Data: []byte(`{"type":"response.output_item.added"}`),
	},
	{
		Name: "response.completed",
		Data: []byte(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`),
	},
}

// observedRequest is what the reference server saw.
type observedRequest struct {
	method string
	path   string
	host   string
	header http.Header
	body   []byte
}

// referenceServer is the reference provider of the public Responses API.
type referenceServer struct {
	server *httptest.Server

	mu          sync.Mutex
	requests    []observedRequest
	status      int
	contentType string
	location    string
	requestID   string
	body        string
	hold        bool
	released    chan struct{}
	releaseOnce sync.Once
	stop        chan struct{}
	stopOnce    sync.Once
}

// newReferenceServer starts the reference server.
func newReferenceServer(t *testing.T) *referenceServer {
	t.Helper()
	reference := &referenceServer{
		released: make(chan struct{}),
		stop:     make(chan struct{}),
	}
	reference.server = httptest.NewServer(http.HandlerFunc(reference.handle))
	t.Cleanup(func() {
		reference.stopOnce.Do(func() { close(reference.stop) })
		reference.server.Close()
	})
	return reference
}

// handle answers one request and records it.
func (r *referenceServer) handle(w http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	r.mu.Lock()
	r.requests = append(r.requests, observedRequest{
		method: request.Method,
		path:   request.URL.Path,
		host:   request.Host,
		header: request.Header.Clone(),
		body:   body,
	})
	status := r.status
	contentType := r.contentType
	location := r.location
	requestID := r.requestID
	answer := r.body
	hold := r.hold
	r.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	if requestID != "" {
		w.Header().Set("x-request-id", requestID)
	}
	if location != "" {
		w.Header().Set("Location", location)
	}
	if contentType == "" && status == http.StatusOK {
		contentType = "text/event-stream"
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	if status != http.StatusOK {
		_, _ = io.WriteString(w, answer)
		return
	}
	flusher, _ := w.(http.Flusher)
	if hold {
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-request.Context().Done():
		case <-r.stop:
		}
		r.releaseOnce.Do(func() { close(r.released) })
		return
	}
	if answer == "" {
		answer = referenceSSE
	}
	_, _ = io.WriteString(w, answer)
	if flusher != nil {
		flusher.Flush()
	}
}

// answer replaces the answer of the reference server.
func (r *referenceServer) answer(status int, contentType, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
	r.contentType = contentType
	r.body = body
}

// requestIDHeader sets the provider request id of the answer.
func (r *referenceServer) requestIDHeader(value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requestID = value
}

// redirect makes the server answer with a redirect.
func (r *referenceServer) redirect(target string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = http.StatusFound
	r.contentType = "text/plain"
	r.location = target
	r.body = "moved"
}

// hold makes the server answer one event and wait for the client to leave.
func (r *referenceServer) holdOpen() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hold = true
}

// requestCount returns how many requests the reference server received.
func (r *referenceServer) requestCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// lastRequest returns the request the reference server received last.
func (r *referenceServer) lastRequest(t *testing.T) observedRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		t.Fatal("the reference server received no request")
	}
	return r.requests[len(r.requests)-1]
}

// publicEndpointTransport proves the target and forwards the request to the
// reference server.
//
// The client under test has no endpoint setting: it sends the projected
// request to the public endpoint it carries. This transport therefore checks
// the target the client addressed BEFORE it forwards the request to the
// loopback reference server, so a private route is a named failure instead of
// a surprising connection attempt.
type publicEndpointTransport struct {
	target *url.URL
	base   http.RoundTripper
}

// RoundTrip checks the public target and forwards to the reference server.
func (p *publicEndpointTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	want := responses.PublicBaseURL + responses.ResponsesPath
	if request.URL.Scheme != "https" ||
		request.URL.Host != "api.openai.com" ||
		request.URL.Path != responses.ResponsesPath ||
		request.URL.RawQuery != "" ||
		request.URL.Fragment != "" ||
		request.URL.User != nil {
		return nil, fmt.Errorf("the connector addressed %q, want the public Responses endpoint %q",
			request.URL.Redacted(), want)
	}
	forwarded := request.Clone(request.Context())
	forwarded.URL.Scheme = p.target.Scheme
	forwarded.URL.Host = p.target.Host
	// The reference server observes the public Host the client addressed,
	// not the loopback address of the test.
	forwarded.Host = request.URL.Host
	base := p.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(forwarded)
}

// recordingAuthority is the registration authority of the tests.
type recordingAuthority struct {
	selected     string
	selectedErr  error
	grants       map[string]siwc.TokenGrant
	grantErr     map[string]error
	askedProfile []string
}

// Selected answers with the prepared selection.
func (a *recordingAuthority) Selected(context.Context) (string, error) {
	return a.selected, a.selectedErr
}

// Token answers with the grant of the named registration and records the
// question.
func (a *recordingAuthority) Token(_ context.Context, profileID string) (siwc.TokenGrant, error) {
	a.askedProfile = append(a.askedProfile, profileID)
	if err := a.grantErr[profileID]; err != nil {
		return siwc.TokenGrant{}, err
	}
	grant, ok := a.grants[profileID]
	if !ok {
		return siwc.TokenGrant{}, fmt.Errorf("no grant for %q", profileID)
	}
	return grant, nil
}

// newSelectedAuthority returns an authority with the selected registration
// and its access token.
func newSelectedAuthority() *recordingAuthority {
	return &recordingAuthority{
		selected: testProfileID,
		grants: map[string]siwc.TokenGrant{
			testProfileID: {
				AccessToken: testAccessToken,
				Generation:  [sha256.Size]byte{0xAA},
			},
		},
	}
}

// newReferenceClient returns the transport under test, aimed at the reference
// server through the public-endpoint guard.
func newReferenceClient(t *testing.T, reference *referenceServer, authority siwc.SelectedTokenProvider) *responses.Client {
	t.Helper()
	target, err := url.Parse(reference.server.URL)
	if err != nil {
		t.Fatalf("the reference server URL could not be read: %v", err)
	}
	client, err := responses.NewClient(responses.ClientConfig{
		HTTPClient: &http.Client{Transport: &publicEndpointTransport{
			target: target,
			base:   reference.server.Client().Transport,
		}},
		Tokens: authority,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// projectedStreamRequest returns the exact request the projector/policy seam
// produces for one downstream delivery mode.
func projectedStreamRequest(t *testing.T, mode aipapi.DeliveryMode) responses.WireRequest {
	t.Helper()
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("Say hello.")}},
		},
		Invocation: aipapi.Invocation{DeliveryMode: mode},
	}
	admission, err := responses.AdmitCall(
		responses.ProjectInput{Call: call, Model: "gpt-6.1-codex"},
		responses.PolicyModeInteroperable,
	)
	if err != nil {
		t.Fatalf("AdmitCall: %v", err)
	}
	return admission.Request
}

// collectEvents reads a stream to its end and returns every event.
func collectEvents(t *testing.T, stream *responses.Stream) []responses.Event {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var events []responses.Event
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return events
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		events = append(events, event)
	}
}

// wireDocument decodes the request body the reference server observed.
func wireDocument(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	document := make(map[string]json.RawMessage)
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("the wire body is not a JSON object: %v", err)
	}
	return document
}

// requireStreamingWireBody asserts the mandatory upstream transport posture.
func requireStreamingWireBody(t *testing.T, body []byte) {
	t.Helper()
	document := wireDocument(t, body)
	stream, ok := document["stream"]
	if !ok || string(stream) != "true" {
		t.Fatalf("the wire body does not ask the provider to stream: stream=%s", stream)
	}
	store, ok := document["store"]
	if !ok || string(store) != "false" {
		t.Fatalf("the wire body does not forbid storage: store=%s", store)
	}
	if _, ok := document["previous_response_id"]; ok {
		t.Fatal("the wire body carries previous_response_id")
	}
}

// TestClient_RequestsThePublicStreamingEndpointWithTheSelectedProfile proves
// the request the transport makes.
//
// The request must reach the documented public endpoint as POST, carry
// exactly the selected registration's standard bearer credential, always
// stream, and the answer must arrive as the raw events the provider sent.
func TestClient_RequestsThePublicStreamingEndpointWithTheSelectedProfile(t *testing.T) {
	t.Parallel()
	reference := newReferenceServer(t)
	authority := newSelectedAuthority()
	client := newReferenceClient(t, reference, authority)

	stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if got := stream.ProfileID(); got != testProfileID {
		t.Errorf("the stream names the registration %q, want %q", got, testProfileID)
	}
	if got := stream.CredentialGeneration(); got != authority.grants[testProfileID].Generation {
		t.Errorf("the stream carries the credential generation %x, want %x", got, authority.grants[testProfileID].Generation)
	}

	events := collectEvents(t, stream)
	if !reflect.DeepEqual(events, referenceEvents) {
		t.Fatalf("the raw events = %#v, want %#v", events, referenceEvents)
	}
	if reference.requestCount() != 1 {
		t.Fatalf("the reference server received %d requests, want 1", reference.requestCount())
	}
	request := reference.lastRequest(t)
	if request.method != http.MethodPost {
		t.Errorf("the request method = %q, want %q", request.method, http.MethodPost)
	}
	if request.path != responses.ResponsesPath {
		t.Errorf("the request path = %q, want the documented public %q", request.path, responses.ResponsesPath)
	}
	if request.host != "api.openai.com" {
		t.Errorf("the request host = %q, want the public host %q", request.host, "api.openai.com")
	}
	if request.header.Get("Accept") != "text/event-stream" {
		t.Errorf("the request accept header = %q, want %q", request.header.Get("Accept"), "text/event-stream")
	}
	values := request.header.Values("Authorization")
	if len(values) != 1 || values[0] != "Bearer "+testAccessToken {
		t.Errorf("the authorization header = %q, want exactly one %q", values, "Bearer "+testAccessToken)
	}
	if userAgent := request.header.Get("User-Agent"); !strings.Contains(userAgent, "AIProxer") {
		t.Errorf("the user agent = %q, want the truthful product identity", userAgent)
	}
	// The request carries no Codex identity and no private header.
	for _, name := range []string{
		"originator", "chatgpt-account-id", "conversation_id", "session_id",
		"Codex-Task-Type", "version", "OpenAI-Beta",
	} {
		if carried := request.header.Values(name); len(carried) != 0 {
			t.Errorf("the request carries the private header %q: %q", name, carried)
		}
	}
	requireStreamingWireBody(t, request.body)
	if len(authority.askedProfile) != 1 || authority.askedProfile[0] != testProfileID {
		t.Errorf("the transport asked for %v, want exactly [%q]", authority.askedProfile, testProfileID)
	}
}

// TestClient_OpenStreamForAttemptUsesTheBoundCredential proves a bound attempt
// presents exactly the credential the service authorized and never asks the
// authority for another credential. The authority names ANOTHER registration
// and no token lookup may occur: a selection that moves after admission cannot
// supply this attempt's credential. (Selection reads are out of scope here;
// the observation is the absence of token acquisition.)
func TestClient_OpenStreamForAttemptUsesTheBoundCredential(t *testing.T) {
	t.Parallel()
	const otherProfileID = "acct-other"
	reference := newReferenceServer(t)
	authority := newSelectedAuthority()
	authority.selected = otherProfileID
	client := newReferenceClient(t, reference, authority)

	bound := responses.BoundCredential{
		ProfileID:   testProfileID,
		AccessToken: testAccessToken,
		Generation:  [sha256.Size]byte{0xAA},
	}
	stream, err := client.OpenStreamForAttempt(
		context.Background(),
		projectedStreamRequest(t, aipapi.DeliveryModeStreaming),
		bound,
	)
	if err != nil {
		t.Fatalf("OpenStreamForAttempt: %v", err)
	}
	if got := stream.ProfileID(); got != bound.ProfileID {
		t.Errorf("the stream names the registration %q, want the bound %q", got, bound.ProfileID)
	}
	if got := stream.CredentialGeneration(); got != bound.Generation {
		t.Errorf("the stream carries the credential generation %x, want the bound %x", got, bound.Generation)
	}
	if events := collectEvents(t, stream); !reflect.DeepEqual(events, referenceEvents) {
		t.Fatalf("the raw events = %#v, want %#v", events, referenceEvents)
	}
	if len(authority.askedProfile) != 0 {
		t.Fatalf("a bound attempt asked the authority for a credential %v, want no token lookup", authority.askedProfile)
	}
	if reference.requestCount() != 1 {
		t.Fatalf("the reference server received %d requests, want 1", reference.requestCount())
	}
	request := reference.lastRequest(t)
	if values := request.header.Values("Authorization"); len(values) != 1 || values[0] != "Bearer "+bound.AccessToken {
		t.Fatalf("the authorization header = %q, want exactly the bound credential", values)
	}
}

// TestClient_ServesBothDownstreamDeliveryModesFromOneStreamingRequest proves
// that the downstream delivery mode changes nothing upstream.
//
// Requirement 4.3: every HTTP inference request streams regardless of the
// downstream mode. The client has no provider non-stream path, and the two
// canonical calls below differ only in their delivery mode.
func TestClient_ServesBothDownstreamDeliveryModesFromOneStreamingRequest(t *testing.T) {
	t.Parallel()
	reference := newReferenceServer(t)
	client := newReferenceClient(t, reference, newSelectedAuthority())

	var bodies [][]byte
	for _, mode := range []aipapi.DeliveryMode{
		aipapi.DeliveryModeStreaming,
		aipapi.DeliveryModeNonStreaming,
	} {
		stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, mode))
		if err != nil {
			t.Fatalf("OpenStream(%s): %v", mode, err)
		}
		if events := collectEvents(t, stream); !reflect.DeepEqual(events, referenceEvents) {
			t.Fatalf("OpenStream(%s): the raw events = %#v, want %#v", mode, events, referenceEvents)
		}
		bodies = append(bodies, reference.lastRequest(t).body)
	}
	if reference.requestCount() != 2 {
		t.Fatalf("the reference server received %d requests, want 2", reference.requestCount())
	}
	for index, body := range bodies {
		requireStreamingWireBody(t, body)
		if index > 0 && !bytes.Equal(bodies[0], body) {
			t.Fatalf("the %s request body differs from the streaming one:\n%s\n---\n%s",
				"non-streaming", bodies[0], body)
		}
	}
}

// TestClient_RefusesAnUnusableSelection proves that the selected registration
// is checked every time.
//
// An unusable selection is an explicit error and never an anonymous request,
// never another registration's credential, and never a silently empty stream.
func TestClient_RefusesAnUnusableSelection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		authority *recordingAuthority
		want      error
	}{
		{
			name:      "no registration is selected",
			authority: &recordingAuthority{grants: map[string]siwc.TokenGrant{testProfileID: {AccessToken: testAccessToken}}},
			want:      responses.ErrNoSelectedProfile,
		},
		{
			name: "the selection could not be read",
			authority: &recordingAuthority{
				selectedErr: errors.New("the registration store is unavailable"),
				grants:      map[string]siwc.TokenGrant{testProfileID: {AccessToken: testAccessToken}},
			},
			want: responses.ErrNoSelectedProfile,
		},
		{
			name: "the selected registration has an empty access token",
			authority: &recordingAuthority{
				selected: testProfileID,
				grants:   map[string]siwc.TokenGrant{testProfileID: {}},
			},
			want: responses.ErrRegistrationUnusable,
		},
		{
			name: "the selected registration's token could not be acquired",
			authority: &recordingAuthority{
				selected: testProfileID,
				grantErr: map[string]error{testProfileID: errors.New("the credential is expired")},
			},
			want: responses.ErrRegistrationUnusable,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			reference := newReferenceServer(t)
			client := newReferenceClient(t, reference, testCase.authority)
			stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
			if stream != nil {
				_ = stream.Close()
			}
			if !errors.Is(err, testCase.want) {
				t.Fatalf("the transport returned %v, want an error classified as %v", err, testCase.want)
			}
			if reference.requestCount() != 0 {
				t.Fatalf("the transport sent %d request(s) without a usable selection", reference.requestCount())
			}
			for _, asked := range testCase.authority.askedProfile {
				if asked != testProfileID {
					t.Fatalf("the transport asked for the registration %q, want only the selected %q", asked, testProfileID)
				}
			}
		})
	}
}

// TestClient_RefusesARequestItMustNotSend proves the transport-side guards.
//
// The policy and the projector own the wire contract; the transport still
// refuses a request that is not the public streaming route, so a future
// caller cannot turn the transport into a private or non-streaming path.
func TestClient_RefusesARequestItMustNotSend(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*responses.WireRequest)
		want   error
	}{
		{
			name: "a non-streaming body",
			mutate: func(request *responses.WireRequest) {
				request.Body = bytes.Replace(request.Body, []byte(`"stream":true`), []byte(`"stream":false`), 1)
			},
			want: responses.ErrRequestNotStreaming,
		},
		{
			name: "a request that carries its own credential",
			mutate: func(request *responses.WireRequest) {
				request.Headers.Set("Authorization", "Bearer another-registration")
			},
			want: responses.ErrInvalidWireRequest,
		},
		{
			name: "a non-POST method",
			mutate: func(request *responses.WireRequest) {
				request.Method = http.MethodGet
			},
			want: responses.ErrInvalidWireRequest,
		},
		{
			name: "a private backend-api path",
			mutate: func(request *responses.WireRequest) {
				request.URL = "https://chatgpt.com/backend-api/codex/responses"
			},
			want: responses.ErrNotPublicEndpoint,
		},
		{
			name: "another public path",
			mutate: func(request *responses.WireRequest) {
				request.URL = "https://api.openai.com/v1/chat/completions"
			},
			want: responses.ErrNotPublicEndpoint,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			reference := newReferenceServer(t)
			client := newReferenceClient(t, reference, newSelectedAuthority())
			request := projectedStreamRequest(t, aipapi.DeliveryModeStreaming)
			testCase.mutate(&request)
			stream, err := client.OpenStream(context.Background(), request)
			if stream != nil {
				_ = stream.Close()
			}
			if !errors.Is(err, testCase.want) {
				t.Fatalf("the transport returned %v, want an error classified as %v", err, testCase.want)
			}
			if reference.requestCount() != 0 {
				t.Fatalf("the transport sent %d request(s) it must refuse", reference.requestCount())
			}
		})
	}
}

// TestClient_ChecksTheSelectedRegistrationEveryTime proves that the selection
// is read per request, not cached.
//
// A cached or assumed selection would keep presenting the previous
// registration's credential after the operator switched profiles.
func TestClient_ChecksTheSelectedRegistrationEveryTime(t *testing.T) {
	t.Parallel()
	const secondProfileID = "acct-two"
	const secondToken = "siwc-access-token-for-acct-two"
	reference := newReferenceServer(t)
	authority := newSelectedAuthority()
	authority.grants[secondProfileID] = siwc.TokenGrant{
		AccessToken: secondToken,
		Generation:  [sha256.Size]byte{0xBB},
	}
	client := newReferenceClient(t, reference, authority)

	first, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if err != nil {
		t.Fatalf("the first OpenStream: %v", err)
	}
	_ = collectEvents(t, first)

	authority.selected = secondProfileID
	second, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if err != nil {
		t.Fatalf("the second OpenStream: %v", err)
	}
	if got := second.ProfileID(); got != secondProfileID {
		t.Errorf("the second stream names the registration %q, want %q", got, secondProfileID)
	}
	_ = collectEvents(t, second)

	if reference.requestCount() != 2 {
		t.Fatalf("the reference server received %d requests, want 2", reference.requestCount())
	}
	if got := reference.requests[0].header.Get("Authorization"); got != "Bearer "+testAccessToken {
		t.Errorf("the first request presented %q, want the first registration's token", got)
	}
	if got := reference.requests[1].header.Get("Authorization"); got != "Bearer "+secondToken {
		t.Errorf("the second request presented %q, want the second registration's token", got)
	}
	if len(authority.askedProfile) != 2 ||
		authority.askedProfile[0] != testProfileID ||
		authority.askedProfile[1] != secondProfileID {
		t.Errorf("the transport asked for %v, want [%q %q]", authority.askedProfile, testProfileID, secondProfileID)
	}
}

// TestClient_ReportsTheProviderErrorWithoutLeakingTheCredential proves the
// error surface of a refused request.
//
// Requirement 8.1: status, machine-readable code and request id are
// preserved. The provider's message is untrusted input: it is redacted
// against the token that was sent and bounded, and no raw body reaches the
// error.
func TestClient_ReportsTheProviderErrorWithoutLeakingTheCredential(t *testing.T) {
	t.Parallel()
	const requestID = "req_public_123"
	message := "the access token " + testAccessToken + " was rejected; " + strings.Repeat("detail ", 200)
	reference := newReferenceServer(t)
	reference.requestIDHeader(requestID)
	reference.answer(http.StatusTooManyRequests, "application/json",
		`{"error":{"message":`+jsonString(message)+`,"type":"invalid_request_error","code":"usage_limit_exceeded"}}`)
	client := newReferenceClient(t, reference, newSelectedAuthority())

	stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if stream != nil {
		_ = stream.Close()
	}
	if err == nil {
		t.Fatal("the transport reported a successful stream for a refused request")
	}
	var httpErr *responses.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("the refusal is %T, want a *responses.HTTPError: %v", err, err)
	}
	if httpErr.Status != http.StatusTooManyRequests {
		t.Errorf("the preserved status = %d, want %d", httpErr.Status, http.StatusTooManyRequests)
	}
	if httpErr.Code != "usage_limit_exceeded" {
		t.Errorf("the preserved code = %q, want %q", httpErr.Code, "usage_limit_exceeded")
	}
	if httpErr.Type != "invalid_request_error" {
		t.Errorf("the preserved type = %q, want %q", httpErr.Type, "invalid_request_error")
	}
	if httpErr.RequestID != requestID {
		t.Errorf("the preserved request id = %q, want %q", httpErr.RequestID, requestID)
	}
	if !errors.Is(err, responses.ErrUnexpectedStatus) {
		t.Errorf("the refusal does not classify as ErrUnexpectedStatus: %v", err)
	}
	if strings.Contains(httpErr.Diagnostic, testAccessToken) || strings.Contains(err.Error(), testAccessToken) {
		t.Fatalf("the provider error leaked the access token: %q", err.Error())
	}
	if !strings.Contains(httpErr.Diagnostic, "[REDACTED]") {
		t.Errorf("the echoed token was not redacted: %q", httpErr.Diagnostic)
	}
	if len(httpErr.Diagnostic) > 1024 {
		t.Errorf("the diagnostic is %d bytes, want it bounded", len(httpErr.Diagnostic))
	}
	if len(err.Error()) > 2048 {
		t.Errorf("the rendered error is %d bytes, want it bounded", len(err.Error()))
	}
}

// TestClient_RefusesANonSSESuccessAnswer proves that a successful status
// alone is not a stream.
//
// Reading a JSON or HTML answer as an empty SSE stream would turn a
// provider-side protocol change into a normal end of stream.
func TestClient_RefusesANonSSESuccessAnswer(t *testing.T) {
	t.Parallel()
	reference := newReferenceServer(t)
	reference.answer(http.StatusOK, "application/json", `{"id":"resp_1"}`)
	client := newReferenceClient(t, reference, newSelectedAuthority())

	stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if stream != nil {
		_ = stream.Close()
	}
	if !errors.Is(err, responses.ErrUnexpectedContentType) {
		t.Fatalf("the transport returned %v, want ErrUnexpectedContentType", err)
	}
	var httpErr *responses.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusOK {
		t.Fatalf("the refusal must preserve the successful status, got %v", err)
	}
}

// TestClient_DoesNotFollowARedirect proves that the bearer credential is not
// replayed to a destination the provider chose.
func TestClient_DoesNotFollowARedirect(t *testing.T) {
	t.Parallel()
	reference := newReferenceServer(t)
	reference.redirect("https://elsewhere.example/v1/responses")
	client := newReferenceClient(t, reference, newSelectedAuthority())

	stream, err := client.OpenStream(context.Background(), projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if stream != nil {
		_ = stream.Close()
	}
	if reference.requestCount() != 1 {
		t.Fatalf("the transport made %d requests, want exactly the one initial request", reference.requestCount())
	}
	var httpErr *responses.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusFound {
		t.Fatalf("the redirect must be refused as an HTTP error with status %d, got %v", http.StatusFound, err)
	}
}

// TestClient_PreservesCancellation proves that the request context reaches
// the wire and a cancelled stream releases the connection.
func TestClient_PreservesCancellation(t *testing.T) {
	t.Parallel()
	reference := newReferenceServer(t)
	reference.holdOpen()
	client := newReferenceClient(t, reference, newSelectedAuthority())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := client.OpenStream(ctx, projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer func() { _ = stream.Close() }()
	first, err := stream.Next()
	if err != nil {
		t.Fatalf("the first event must arrive before cancellation: %v", err)
	}
	if first.Name != "response.created" {
		t.Fatalf("the first event = %q, want %q", first.Name, "response.created")
	}

	cancel()
	_, err = stream.Next()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled stream returned %v, want context.Canceled", err)
	}
	select {
	case <-reference.released:
	case <-time.After(5 * time.Second):
		t.Fatal("the provider did not observe the cancelled connection")
	}
}

// TestClient_DoesNotStartACancelledRequest proves that a context cancelled
// before the call sends nothing.
func TestClient_DoesNotStartACancelledRequest(t *testing.T) {
	t.Parallel()
	reference := newReferenceServer(t)
	client := newReferenceClient(t, reference, newSelectedAuthority())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stream, err := client.OpenStream(ctx, projectedStreamRequest(t, aipapi.DeliveryModeStreaming))
	if stream != nil {
		_ = stream.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the transport returned %v, want context.Canceled", err)
	}
	if reference.requestCount() != 0 {
		t.Fatalf("the transport sent %d request(s) for a cancelled caller", reference.requestCount())
	}
}

// jsonString encodes one string as a JSON string literal.
func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
