package responses

// Framing and acquisition proofs for the raw Responses SSE stream (task
// 5.1).
//
// This file is in the package so it can read the unexported budgets. The
// bound proofs are ACQUISITION proofs: a generated body counts every byte the
// transport actually read, and the assertions fail if the stream is read to
// the end before it is refused.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
)

// generatedBody is an endless body that counts what was actually read.
type generatedBody struct {
	pattern []byte
	offset  int
	read    int64
	closed  bool
	closes  int
}

// Read fills the buffer from the repeating pattern.
func (g *generatedBody) Read(p []byte) (int, error) {
	if len(g.pattern) == 0 {
		return 0, io.EOF
	}
	for index := range p {
		p[index] = g.pattern[g.offset]
		g.offset = (g.offset + 1) % len(g.pattern)
	}
	g.read += int64(len(p))
	return len(p), nil
}

// Close records that the body was released.
func (g *generatedBody) Close() error {
	g.closed = true
	g.closes++
	return nil
}

// countingBody is a finite body that counts reads and records its close.
type countingBody struct {
	reader io.Reader
	read   int64
	closed bool
}

// Read counts the bytes the transport consumed.
func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += int64(n)
	return n, err
}

// Close records that the body was released.
func (b *countingBody) Close() error {
	b.closed = true
	return nil
}

// failingBody is a body whose every read fails with a prepared diagnostic.
type failingBody struct {
	err    error
	reads  int
	closed bool
}

// Read fails with the prepared diagnostic without returning any bytes.
func (b *failingBody) Read([]byte) (int, error) {
	b.reads++
	return 0, b.err
}

// Close records that the body was released.
func (b *failingBody) Close() error {
	b.closed = true
	return nil
}

// stubTransport answers with a prepared response without a network.
type stubTransport struct {
	status      int
	contentType string
	header      http.Header
	body        io.ReadCloser
}

// RoundTrip returns the prepared response.
func (s *stubTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	header := s.header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	if s.contentType != "" {
		header.Set("Content-Type", s.contentType)
	}
	return &http.Response{
		StatusCode: s.status,
		Status:     fmt.Sprintf("%d %s", s.status, http.StatusText(s.status)),
		Header:     header,
		Body:       s.body,
		Request:    request,
	}, nil
}

// stubAuthority is a usable selected registration.
type stubAuthority struct{}

// Selected answers with one registration.
func (stubAuthority) Selected(context.Context) (string, error) { return "acct-one", nil }

// Token answers with one access token.
func (stubAuthority) Token(context.Context, string) (siwc.TokenGrant, error) {
	return siwc.TokenGrant{AccessToken: "siwc-access-token-for-acct-one"}, nil
}

// loopbackRequest is a minimal request the transport guard accepts. The
// loopback address is the local test endpoint; the transport never connects
// because the test supplies its own round tripper.
func loopbackRequest() WireRequest {
	return WireRequest{
		Method: http.MethodPost,
		URL:    "http://127.0.0.1:9/v1/responses",
		Headers: http.Header{
			"Accept":       {"text/event-stream"},
			"Content-Type": {"application/json"},
			"User-Agent":   {UserAgent},
		},
		Body: []byte(`{"model":"gpt-6.1-codex","stream":true,"store":false,"input":[]}`),
	}
}

// newStubClient returns the transport under test over a prepared round
// tripper.
func newStubClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		HTTPClient: &http.Client{Transport: transport},
		Tokens:     stubAuthority{},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// readAllEvents collects every event of a stream.
func readAllEvents(t *testing.T, stream *Stream) []Event {
	t.Helper()
	var events []Event
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

// TestStream_DeliversTheRawProviderFraming proves the SSE framing.
//
// The stream delivers what the provider sent, in the provider's order, and
// ignores only the framing that carries no event.
func TestStream_DeliversTheRawProviderFraming(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want []Event
	}{
		{
			name: "comments and keep-alives are ignored",
			body: ": ping\n\n: ping\n\n",
			want: nil,
		},
		{
			name: "one event with a name and a payload",
			body: "event: response.created\ndata: {\"type\":\"response.created\"}\n\n",
			want: []Event{{Name: "response.created", Data: []byte(`{"type":"response.created"}`)}},
		},
		{
			name: "multi-line data is joined with a newline",
			body: "event: response.output_text.delta\ndata: {\"type\":\ndata: \"delta\":\"hi\"}\n\n",
			want: []Event{{
				Name: "response.output_text.delta",
				Data: []byte("{\"type\":\n\"delta\":\"hi\"}"),
			}},
		},
		{
			name: "non-data fields are ignored",
			body: "id: 7\nretry: 1000\ndata: x\n\n",
			want: []Event{{Name: "message", Data: []byte("x")}},
		},
		{
			name: "an event with no data is not dispatched",
			body: "event: response.in_progress\n\n",
			want: nil,
		},
		{
			name: "the event name defaults to message",
			body: "data: x\n\n",
			want: []Event{{Name: "message", Data: []byte("x")}},
		},
		{
			name: "an empty data payload is an event",
			body: "data:\n\n",
			want: []Event{{Name: "message", Data: nil}},
		},
		{
			name: "a bare data field is an empty data field",
			body: "data\n\n",
			want: []Event{{Name: "message", Data: nil}},
		},
		{
			name: "an empty data frame does not contaminate the next frame",
			body: "data:\n\ndata: x\n\n",
			want: []Event{
				{Name: "message", Data: nil},
				{Name: "message", Data: []byte("x")},
			},
		},
		{
			name: "a bare data field still contributes its newline",
			body: "data: a\ndata\n\n",
			want: []Event{{Name: "message", Data: []byte("a\n")}},
		},
		{
			name: "an empty data frame does not carry its name into the next frame",
			body: "event: first\ndata:\n\nevent: second\ndata: y\n\n",
			want: []Event{
				{Name: "first", Data: nil},
				{Name: "second", Data: []byte("y")},
			},
		},
		{
			name: "a final empty data frame without a blank line is not salvaged",
			body: "data:\n",
			want: nil,
		},
		{
			name: "a final event without a blank line is delivered",
			body: "event: response.completed\ndata: {\"type\":\"response.completed\"}\n",
			want: []Event{{
				Name: "response.completed",
				Data: []byte(`{"type":"response.completed"}`),
			}},
		},
		{
			name: "a final partial line is discarded",
			body: "event: response.completed\ndata: {\"type\":",
			want: nil,
		},
		{
			name: "CRLF and CR terminators",
			body: "event: x\r\ndata: a\r\ndata: b\r\r",
			want: []Event{{Name: "x", Data: []byte("a\nb")}},
		},
		{
			name: "an unknown field without a colon is ignored",
			body: "hello\n\ndata: x\n\n",
			want: []Event{{Name: "message", Data: []byte("x")}},
		},
		{
			name: "a comment between data lines preserves the payload",
			body: "data: a\n: keep-alive\ndata: b\n\n",
			want: []Event{{Name: "message", Data: []byte("a\nb")}},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			stream := newStream(
				context.Background(),
				io.NopCloser(strings.NewReader(testCase.body)),
				"acct-one",
				[32]byte{},
			)
			defer func() { _ = stream.Close() }()
			got := readAllEvents(t, stream)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("the events = %#v, want %#v", got, testCase.want)
			}
			if _, err := stream.Next(); !errors.Is(err, io.EOF) {
				t.Fatalf("the stream after its end returned %v, want io.EOF", err)
			}
		})
	}
}

// TestStream_ClassifiesAGenericReadFailureWithoutItsText proves a failing body
// read becomes the constant ErrStreamUnreadable.
//
// The standard HTTP body reader reports provider-controlled text (a malformed
// chunk trailer, for example) in its read error. Returning that error would put
// untrusted bytes, and a provider that echoed the request header would put the
// bearer credential, into every log that prints the stream failure. The
// terminal error is the constant, value-free sentinel; the cause is
// deliberately not retained.
func TestStream_ClassifiesAGenericReadFailureWithoutItsText(t *testing.T) {
	t.Parallel()
	const token = "siwc-access-token-for-acct-one"
	diagnostic := "the provider echoed " + token + " " + strings.Repeat("detail ", 3000)
	body := &failingBody{err: errors.New(diagnostic)}
	client := newStubClient(t, &stubTransport{
		status:      http.StatusOK,
		contentType: "text/event-stream",
		body:        body,
	})
	stream, err := client.OpenStream(context.Background(), loopbackRequest())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	_, err = stream.Next()
	if err == nil {
		t.Fatal("the failing body returned an event")
	}
	if !errors.Is(err, ErrStreamUnreadable) {
		t.Fatalf("the read failure = %v, want ErrStreamUnreadable", err)
	}
	if err != ErrStreamUnreadable {
		t.Fatalf("the read failure = %q (%T), want the constant ErrStreamUnreadable without a retained cause", err, err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "detail") {
		t.Fatalf("bearer_leaked=true: the read failure repeated provider text: %q", err.Error())
	}
	if got, want := err.Error(), ErrStreamUnreadable.Error(); got != want {
		t.Fatalf("the read failure text = %q, want the bounded sentinel %q", got, want)
	}
	if !body.closed {
		t.Fatal("the failed stream did not close its body")
	}
	if _, again := stream.Next(); again != ErrStreamUnreadable {
		t.Fatalf("the terminal read failure changed to %v, want the same ErrStreamUnreadable", again)
	}
}

// TestStream_RealMalformedTrailerCannotLeakTheBearer proves the real standard
// HTTP reader cannot put the request's credential into a stream error.
//
// The test server answers a chunked SSE body whose trailer block contains a
// malformed line carrying the Authorization value the request sent. Go's
// net/http body reader reports that line in its read error; the transport must
// turn it into the constant ErrStreamUnreadable instead of returning it.
func TestStream_RealMalformedTrailerCannotLeakTheBearer(t *testing.T) {
	t.Parallel()
	const token = "siwc-access-token-for-acct-one"
	const event = "data: x\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("the test server cannot hijack its connection")
			return
		}
		connection, buffer, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = connection.Close() }()
		_, _ = io.WriteString(buffer,
			"HTTP/1.1 200 OK\r\n"+
				"Content-Type: text/event-stream\r\n"+
				"Transfer-Encoding: chunked\r\n"+
				"Trailer: X-Echo\r\n\r\n")
		_, _ = fmt.Fprintf(buffer, "%x\r\n%s\r\n", len(event), event)
		// The trailer line has no colon, so the reader reports it as a
		// malformed header; it carries the Authorization value the request
		// sent.
		_, _ = io.WriteString(buffer, "0\r\n"+request.Header.Get("Authorization")+"\r\n\r\n")
		if err := buffer.Flush(); err != nil {
			t.Errorf("flush the malformed-trailer answer: %v", err)
		}
	}))
	defer server.Close()

	request := loopbackRequest()
	request.URL = server.URL + ResponsesPath
	client, err := NewClient(ClientConfig{HTTPClient: server.Client(), Tokens: stubAuthority{}})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	stream, err := client.OpenStream(context.Background(), request)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if delivered, err := stream.Next(); err != nil || string(delivered.Data) != "x" {
		t.Fatalf("the event before the trailer = (%#v, %v), want data %q", delivered, err, "x")
	}
	_, err = stream.Next()
	if err == nil {
		t.Fatal("the malformed trailer returned an event")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("the malformed trailer was reported as a normal end of stream: %v", err)
	}
	if !errors.Is(err, ErrStreamUnreadable) {
		t.Fatalf("bearer_leaked=%t classified=false: the malformed trailer is %v, want ErrStreamUnreadable",
			strings.Contains(err.Error(), token), err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("bearer_leaked=true: the malformed trailer error repeated the credential: %q", err.Error())
	}
}

// TestStream_KeepAliveFramesHaveSeparateBudgets proves the per-event budget is
// per FRAME, not accumulated across data-less keep-alives.
//
// The whole answer stays under maxStreamBytes, so only a per-frame reset can
// let this stream reach its one real event.
func TestStream_KeepAliveFramesHaveSeparateBudgets(t *testing.T) {
	t.Parallel()
	const keepAlives = 131073
	var body strings.Builder
	for range keepAlives {
		body.WriteString(": ping\n\n")
	}
	body.WriteString("data: x\n\n")
	if body.Len() > maxStreamBytes {
		t.Fatalf("the fixture is %d bytes, above the whole-stream bound %d", body.Len(), maxStreamBytes)
	}
	stream := newStream(context.Background(), io.NopCloser(strings.NewReader(body.String())), "acct-one", [32]byte{})
	defer func() { _ = stream.Close() }()
	events := readAllEvents(t, stream)
	want := []Event{{Name: "message", Data: []byte("x")}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("the events = %#v, want %#v", events, want)
	}
}

// TestClient_PreservesTheRequestIDOfANonSSESuccess proves the transport keeps
// the provider's correlation metadata on a successful answer it cannot use.
func TestClient_PreservesTheRequestIDOfANonSSESuccess(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		header string
		value  string
	}{
		{name: "x-request-id", header: "X-Request-Id", value: "req_public_123"},
		{name: "openai-request-id", header: "Openai-Request-Id", value: "req_openai_456"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			header := make(http.Header)
			header.Set(testCase.header, testCase.value)
			body := &countingBody{reader: strings.NewReader(`{"id":"resp_1"}`)}
			client := newStubClient(t, &stubTransport{
				status:      http.StatusOK,
				contentType: "application/json",
				header:      header,
				body:        body,
			})
			stream, err := client.OpenStream(context.Background(), loopbackRequest())
			if stream != nil {
				_ = stream.Close()
			}
			if !errors.Is(err, ErrUnexpectedContentType) {
				t.Fatalf("the non-SSE success returned %v, want ErrUnexpectedContentType", err)
			}
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("the refusal is %T, want an HTTPError: %v", err, err)
			}
			if httpErr.Status != http.StatusOK {
				t.Errorf("the preserved status = %d, want %d", httpErr.Status, http.StatusOK)
			}
			if httpErr.RequestID != testCase.value {
				t.Fatalf("the preserved request id = %q, want %q", httpErr.RequestID, testCase.value)
			}
			if body.read != 0 {
				t.Fatalf("the transport read %d bytes of an unusable answer, want 0", body.read)
			}
			if !body.closed {
				t.Fatal("the unusable answer was not closed")
			}
		})
	}
}

// TestClient_RedactsTheRequestIDOfANonSSESuccess proves the captured request id
// passes the same redact-before-bound path as the non-200 error path.
func TestClient_RedactsTheRequestIDOfANonSSESuccess(t *testing.T) {
	t.Parallel()
	const token = "siwc-access-token-for-acct-one"
	header := make(http.Header)
	header.Set("X-Request-Id", "req_"+token+"_"+strings.Repeat("r", 4096))
	body := &countingBody{reader: strings.NewReader(`{"id":"resp_1"}`)}
	client := newStubClient(t, &stubTransport{
		status:      http.StatusOK,
		contentType: "application/json",
		header:      header,
		body:        body,
	})
	stream, err := client.OpenStream(context.Background(), loopbackRequest())
	if stream != nil {
		_ = stream.Close()
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("the refusal is %T, want an HTTPError: %v", err, err)
	}
	if strings.Contains(httpErr.RequestID, token) {
		t.Fatalf("bearer_leaked=true: the preserved request id repeated the token: %q", httpErr.RequestID)
	}
	if !strings.Contains(httpErr.RequestID, "[REDACTED]") {
		t.Fatalf("the echoed token was not redacted from the request id: %q", httpErr.RequestID)
	}
	if runes := utf8.RuneCountInString(httpErr.RequestID); runes > maxRequestIDRunes+3 {
		t.Fatalf("the request id is %d runes, want at most %d", runes, maxRequestIDRunes+3)
	}
	if body.read != 0 {
		t.Fatalf("the transport read %d bytes of an unusable answer, want 0", body.read)
	}
	if !body.closed {
		t.Fatal("the unusable answer was not closed")
	}
}

// countingTransport counts the requests that reached it.
type countingTransport struct {
	calls int
}

// RoundTrip must never be reached by a refused request.
func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls++
	return nil, errors.New("the transport must not be reached")
}

// TestClient_RefusesAnOversizeRequestBeforeSending proves the request bound.
//
// The bound exists so a caller cannot hand the transport an unbounded
// document; the refusal happens before any credential is acquired or any
// network request is made.
func TestClient_RefusesAnOversizeRequestBeforeSending(t *testing.T) {
	t.Parallel()
	transport := &countingTransport{}
	client := newStubClient(t, transport)
	request := loopbackRequest()
	request.Body = make([]byte, maxRequestBytes+1)

	stream, err := client.OpenStream(context.Background(), request)
	if stream != nil {
		_ = stream.Close()
	}
	if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("the oversize request returned %v, want ErrRequestTooLarge", err)
	}
	if transport.calls != 0 {
		t.Fatalf("the oversize request reached the transport %d time(s)", transport.calls)
	}
}

// TestStream_CutsAnOversizeStreamAtTheBound proves the whole-stream bound is
// an acquisition bound.
//
// The body is endless, so a transport that read it to the end would never
// return. The stream must stop at its budget, return the constant failure,
// and leave the unread remainder unread.
func TestStream_CutsAnOversizeStreamAtTheBound(t *testing.T) {
	t.Parallel()
	body := &generatedBody{pattern: []byte("data: " + strings.Repeat("x", 32<<10) + "\n\n")}
	client := newStubClient(t, &stubTransport{
		status:      http.StatusOK,
		contentType: "text/event-stream",
		body:        body,
	})
	stream, err := client.OpenStream(context.Background(), loopbackRequest())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var terminal error
	for {
		if _, err := stream.Next(); err != nil {
			terminal = err
			break
		}
	}
	if !errors.Is(terminal, ErrStreamTooLarge) {
		t.Fatalf("the oversize stream ended with %v, want ErrStreamTooLarge", terminal)
	}
	if body.read > int64(maxStreamBytes)+1 {
		t.Fatalf("the transport read %d bytes, want at most the budget %d", body.read, int64(maxStreamBytes)+1)
	}
	if body.read < int64(maxStreamBytes) {
		t.Fatalf("the transport stopped after %d bytes, before the budget %d", body.read, int64(maxStreamBytes))
	}
	if !body.closed {
		t.Fatal("the refused stream did not close its body")
	}
}

// TestStream_CutsAnOversizeEventAtTheBound proves the per-event bound is an
// acquisition bound.
func TestStream_CutsAnOversizeEventAtTheBound(t *testing.T) {
	t.Parallel()
	body := &generatedBody{pattern: []byte("data: " + strings.Repeat("x", 1<<10))}
	client := newStubClient(t, &stubTransport{
		status:      http.StatusOK,
		contentType: "text/event-stream",
		body:        body,
	})
	stream, err := client.OpenStream(context.Background(), loopbackRequest())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	_, terminal := stream.Next()
	if !errors.Is(terminal, ErrStreamEventTooLarge) {
		t.Fatalf("the oversize event ended with %v, want ErrStreamEventTooLarge", terminal)
	}
	ceiling := int64(maxStreamEventBytes) + int64(sseReadBufferBytes) + 1
	if body.read > ceiling {
		t.Fatalf("the transport read %d bytes for one event, want at most %d", body.read, ceiling)
	}
	if body.read < int64(maxStreamEventBytes) {
		t.Fatalf("the transport stopped after %d bytes, before the event budget %d", body.read, int64(maxStreamEventBytes))
	}
	if !body.closed {
		t.Fatal("the refused event did not close its body")
	}
}

// TestClient_CutsAnOversizeErrorBodyAtTheBound proves the provider error
// document is bounded at acquisition and that an oversize answer is still an
// ARRIVED answer, not an absent one.
func TestClient_CutsAnOversizeErrorBodyAtTheBound(t *testing.T) {
	t.Parallel()
	body := &generatedBody{pattern: []byte("x")}
	client := newStubClient(t, &stubTransport{
		status:      http.StatusInternalServerError,
		contentType: "application/json",
		body:        body,
	})
	stream, err := client.OpenStream(context.Background(), loopbackRequest())
	if stream != nil {
		_ = stream.Close()
	}
	if !errors.Is(err, ErrErrorBodyTooLarge) {
		t.Fatalf("the oversize error body ended with %v, want ErrErrorBodyTooLarge", err)
	}
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("the oversize error body lost the status class: %v", err)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusInternalServerError {
		t.Fatalf("the oversize error body lost the status: %v", err)
	}
	if body.read != int64(maxErrorBodyBytes)+1 {
		t.Fatalf("the transport read %d error-body bytes, want exactly the budget %d", body.read, int64(maxErrorBodyBytes)+1)
	}
	if !body.closed {
		t.Fatal("the refused error body was not closed")
	}
}

// TestClient_ReportsAnUnreadableErrorBodyAsArrived proves that an error
// document this build cannot read is not reported as an absent one.
func TestClient_ReportsAnUnreadableErrorBodyAsArrived(t *testing.T) {
	t.Parallel()
	body := &countingBody{reader: strings.NewReader("<html>bad gateway</html>")}
	client := newStubClient(t, &stubTransport{
		status:      http.StatusBadGateway,
		contentType: "text/html",
		body:        body,
	})
	stream, err := client.OpenStream(context.Background(), loopbackRequest())
	if stream != nil {
		_ = stream.Close()
	}
	if !errors.Is(err, ErrErrorBodyUnreadable) {
		t.Fatalf("the unreadable error body ended with %v, want ErrErrorBodyUnreadable", err)
	}
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("the unreadable error body lost the status class: %v", err)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadGateway {
		t.Fatalf("the unreadable error body lost the status: %v", err)
	}
	if strings.Contains(err.Error(), "bad gateway") {
		t.Fatalf("the error repeated the provider body: %q", err.Error())
	}
	if !body.closed {
		t.Fatal("the unreadable error body was not closed")
	}
}

// TestClient_BoundsAndRedactsTheProviderDiagnostic proves the diagnostic
// bound and the redaction order.
//
// Redaction runs BEFORE the bound: an echoed token at the end of a long
// message must not survive as a visible prefix of the truncated text.
func TestClient_BoundsAndRedactsTheProviderDiagnostic(t *testing.T) {
	t.Parallel()
	const token = "siwc-access-token-for-acct-one"
	// The token starts just inside the bound, so a redaction that ran AFTER
	// the length bound would keep a visible prefix of it.
	message := strings.Repeat("a", maxDiagnosticRunes-12) + token + " " + strings.Repeat("tail ", 1000)
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal the provider message: %v", err)
	}
	body := &countingBody{reader: strings.NewReader(
		`{"error":{"message":` + string(encoded) + `,"type":"invalid_request_error","code":"invalid_api_key"}}`,
	)}
	client := newStubClient(t, &stubTransport{
		status:      http.StatusUnauthorized,
		contentType: "application/json",
		body:        body,
	})
	stream, err := client.OpenStream(context.Background(), loopbackRequest())
	if stream != nil {
		_ = stream.Close()
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("the refusal is %T, want an HTTPError: %v", err, err)
	}
	if strings.Contains(httpErr.Diagnostic, token) || strings.Contains(err.Error(), token) {
		t.Fatalf("the provider diagnostic leaked the token: %q", httpErr.Diagnostic)
	}
	if !strings.Contains(httpErr.Diagnostic, "[REDACTED]") {
		t.Fatalf("the echoed token was not redacted: %q", httpErr.Diagnostic)
	}
	if runes := utf8.RuneCountInString(httpErr.Diagnostic); runes > maxDiagnosticRunes+3 {
		t.Fatalf("the diagnostic is %d runes, want at most %d", runes, maxDiagnosticRunes+3)
	}
}

// TestClient_RefusesANonSSESuccessWithoutReadingIt proves that a successful
// answer with the wrong content type is refused before it is consumed.
func TestClient_RefusesANonSSESuccessWithoutReadingIt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		contentType string
	}{
		{name: "a JSON answer", contentType: "application/json"},
		{name: "no content type", contentType: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			body := &countingBody{reader: strings.NewReader(`{"id":"resp_1"}`)}
			client := newStubClient(t, &stubTransport{
				status:      http.StatusOK,
				contentType: testCase.contentType,
				body:        body,
			})
			stream, err := client.OpenStream(context.Background(), loopbackRequest())
			if stream != nil {
				_ = stream.Close()
			}
			if !errors.Is(err, ErrUnexpectedContentType) {
				t.Fatalf("the transport returned %v, want ErrUnexpectedContentType", err)
			}
			var httpErr *HTTPError
			if !errors.As(err, &httpErr) || httpErr.Status != http.StatusOK {
				t.Fatalf("the refusal lost the successful status: %v", err)
			}
			if body.read != 0 {
				t.Fatalf("the transport read %d bytes of an unusable answer, want 0", body.read)
			}
			if !body.closed {
				t.Fatal("the unusable answer was not closed")
			}
		})
	}
}

// TestClient_AcceptsAnEventStreamWithParameters proves the content-type check
// reads the media type and not a byte prefix.
func TestClient_AcceptsAnEventStreamWithParameters(t *testing.T) {
	t.Parallel()
	client := newStubClient(t, &stubTransport{
		status:      http.StatusOK,
		contentType: "text/event-stream; charset=utf-8",
		body:        io.NopCloser(strings.NewReader("data: x\n\n")),
	})
	stream, err := client.OpenStream(context.Background(), loopbackRequest())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer func() { _ = stream.Close() }()
	events := readAllEvents(t, stream)
	want := []Event{{Name: "message", Data: []byte("x")}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("the events = %#v, want %#v", events, want)
	}
}

// TestClient_RequiresTheRegistrationAuthority proves the constructor refuses
// a transport that cannot acquire a credential.
func TestClient_RequiresTheRegistrationAuthority(t *testing.T) {
	t.Parallel()
	if _, err := NewClient(ClientConfig{}); err == nil {
		t.Fatal("NewClient accepted a transport with no registration authority")
	}
}
