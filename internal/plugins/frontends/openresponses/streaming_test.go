package openresponses_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type streamingExecutor struct {
	stream aipapi.EventStream
	err    error
	calls  int
}

func (e *streamingExecutor) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	e.calls++
	return e.stream, e.err
}

type streamingEventStream struct {
	mu       sync.Mutex
	events   []aipapi.Event
	err      error
	pos      int
	closes   int
	recvSeen chan int
	wait     <-chan struct{}
}

func (s *streamingEventStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	s.pos++
	pos := s.pos
	s.mu.Unlock()
	if s.recvSeen != nil {
		s.recvSeen <- pos
	}
	if s.wait != nil && pos > 2 {
		select {
		case <-s.wait:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	if pos <= len(s.events) {
		return s.events[pos-1], nil
	}
	if s.err != nil {
		return aipapi.Event{}, s.err
	}
	return aipapi.Event{}, io.EOF
}

func (s *streamingEventStream) Close() error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	return nil
}

func (s *streamingEventStream) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

type streamingResponseWriter struct {
	header           http.Header
	mu               sync.Mutex
	body             bytes.Buffer
	status           int
	writes           int
	flushes          int
	failAt           int
	failBeforeCommit bool
	partialFail      bool
	blockAt          int
	release          <-chan struct{}
}

func newStreamingResponseWriter() *streamingResponseWriter {
	return &streamingResponseWriter{header: make(http.Header)}
}

func (w *streamingResponseWriter) Header() http.Header { return w.header }

func (w *streamingResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}

func (w *streamingResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	writes := w.writes
	fail := writes == w.failAt
	block := writes == w.blockAt
	w.mu.Unlock()
	if fail {
		if w.partialFail {
			// Simulate net/http committing the response (status 200, headers
			// flushed) and accepting some body bytes before the connection
			// fails. The stream seam must treat this as committed.
			half := len(p) / 2
			if half == 0 {
				return 0, errors.New("writer failed mid-write")
			}
			w.mu.Lock()
			if w.status == 0 {
				w.status = http.StatusOK
			}
			_, _ = w.body.Write(p[:half])
			w.mu.Unlock()
			return half, errors.New("writer failed mid-write")
		}
		if w.failBeforeCommit {
			return 0, errors.New("writer failed before commit")
		}
		return 0, errors.New("writer failed")
	}
	w.mu.Lock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.mu.Unlock()
	if block {
		<-w.release
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.body.Write(p)
	return len(p), nil
}

func (w *streamingResponseWriter) Flush() {
	w.mu.Lock()
	w.flushes++
	w.mu.Unlock()
}

func (w *streamingResponseWriter) snapshot() (string, int, int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String(), w.status, w.writes, w.flushes
}

func newStreamingHandler(executor openresponses.ExecutorView) *openresponses.Handler {
	return openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Executor:             executor,
		ResponseIDSource:     deterministicResponseMetadata{id: "resp_stream", now: time.Unix(1_700_000_100, 0)},
		ResponseClock:        deterministicResponseMetadata{id: "resp_stream", now: time.Unix(1_700_000_100, 0)},
	})
}

func streamingRequest(ctx context.Context) *http.Request {
	req := httptestNewRequest(ctx, `{"model":"gpt-4o","input":"hello","stream":true}`)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func httptestNewRequest(ctx context.Context, body string) *http.Request {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	if err != nil {
		panic(err)
	}
	return req
}

func TestStreamingSSEIsIncrementalOrderedFlushedAndExecutorRunsOnce(t *testing.T) {
	release := make(chan struct{})
	stream := &streamingEventStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "hello"},
			{Kind: aipapi.EventResponseFinished},
		},
		wait: release,
	}
	executor := &streamingExecutor{stream: stream}
	handler := newStreamingHandler(executor)
	w := newStreamingResponseWriter()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(w, streamingRequest(context.Background()))
		close(done)
	}()

	deadline := time.After(time.Second)
	for {
		body, _, _, _ := w.snapshot()
		if bytes.Contains([]byte(body), []byte("response.output_item.added")) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first SSE output item was buffered")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if body, _, _, _ := w.snapshot(); bytes.Contains([]byte(body), []byte("response.output_text.delta")) {
		t.Fatal("SSE handler buffered events beyond the first downstream event")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream handler did not finish")
	}

	body, status, _, flushes := w.snapshot()
	if status != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected stream response: status=%d content-type=%q", status, w.Header().Get("Content-Type"))
	}
	for _, want := range []string{"response.created", "response.output_item.added", "response.output_text.delta", "response.completed", "data: [DONE]"} {
		if !bytes.Contains([]byte(body), []byte(want)) {
			t.Fatalf("stream missing %q: %s", want, body)
		}
	}
	if bytes.Count([]byte(body), []byte("event: response.completed")) != 1 || bytes.Count([]byte(body), []byte("data: [DONE]")) != 1 {
		t.Fatalf("expected exactly one terminal and DONE: %s", body)
	}
	if flushes < 2 {
		t.Fatalf("expected flush after incremental frames, got %d", flushes)
	}
	if executor.calls != 1 || stream.closeCount() != 1 {
		t.Fatalf("expected one execution and one close, calls=%d closes=%d", executor.calls, stream.closeCount())
	}
}

func TestStreamingErrorsBeforeAndAfterCommitment(t *testing.T) {
	tests := []struct {
		name       string
		stream     *streamingEventStream
		wantStatus int
		wantSSE    bool
	}{
		{name: "before output", stream: &streamingEventStream{err: errors.New("native secret")}, wantStatus: http.StatusInternalServerError},
		{name: "after output", stream: &streamingEventStream{events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}}, err: errors.New("native secret")}, wantStatus: http.StatusOK, wantSSE: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			executor := &streamingExecutor{stream: tc.stream}
			w := newStreamingResponseWriter()
			newStreamingHandler(executor).ServeHTTP(w, streamingRequest(context.Background()))
			body, status, _, _ := w.snapshot()
			if status != tc.wantStatus {
				t.Fatalf("expected status %d, got %d; body=%s", tc.wantStatus, status, body)
			}
			if !tc.wantSSE && w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("pre-commit failure did not retain JSON error semantics: %q", w.Header().Get("Content-Type"))
			}
			if tc.wantSSE {
				if !bytes.Contains([]byte(body), []byte("event: response.failed")) {
					t.Fatalf("expected failed terminal: %s", body)
				}
				if got := bytes.Count([]byte(body), []byte("data: [DONE]")); got != 1 {
					t.Fatalf("failed terminal must be followed by exactly one DONE, got %d: %s", got, body)
				}
				if !strings.HasSuffix(body, "data: [DONE]\n\n") {
					t.Fatalf("expected stream to end with data: [DONE]\\n\\n: %q", body)
				}
			} else if bytes.Contains([]byte(body), []byte("event:")) || bytes.Contains([]byte(body), []byte("native secret")) {
				t.Fatalf("pre-commit error was not normal bounded JSON: %s", body)
			}
			if executor.calls != 1 || tc.stream.closeCount() != 1 {
				t.Fatalf("expected no retry and one close, calls=%d closes=%d", executor.calls, tc.stream.closeCount())
			}
		})
	}
}

func TestStreamingCanonicalEventErrorEmitsFailedThenDONE(t *testing.T) {
	stream := &streamingEventStream{events: []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventError, ErrorCode: "backend_error", ErrorMessage: "boom"},
	}}
	executor := &streamingExecutor{stream: stream}
	w := newStreamingResponseWriter()
	newStreamingHandler(executor).ServeHTTP(w, streamingRequest(context.Background()))

	body, status, _, _ := w.snapshot()
	if status != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected stream response: status=%d content-type=%q", status, w.Header().Get("Content-Type"))
	}
	failedIdx := bytes.Index([]byte(body), []byte("event: response.failed"))
	doneIdx := bytes.Index([]byte(body), []byte("data: [DONE]"))
	if failedIdx < 0 {
		t.Fatalf("expected response.failed terminal: %s", body)
	}
	if doneIdx < 0 || doneIdx < failedIdx {
		t.Fatalf("expected response.failed before DONE: %s", body)
	}
	if got := bytes.Count([]byte(body), []byte("data: [DONE]")); got != 1 {
		t.Fatalf("expected exactly one DONE sentinel, got %d: %s", got, body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("expected stream to end with data: [DONE]\\n\\n: %q", body)
	}
	if executor.calls != 1 || stream.closeCount() != 1 {
		t.Fatalf("expected one execution and one close, calls=%d closes=%d", executor.calls, stream.closeCount())
	}
}

func TestStreamingPrecommitWriterFailureCanReturnJSONError(t *testing.T) {
	stream := &streamingEventStream{events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}}}
	w := newStreamingResponseWriter()
	w.failAt = 1
	w.failBeforeCommit = true
	newStreamingHandler(&streamingExecutor{stream: stream}).ServeHTTP(w, streamingRequest(context.Background()))
	_, status, _, _ := w.snapshot()
	if status != http.StatusBadGateway || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected uncommitted JSON error, status=%d content-type=%q", status, w.Header().Get("Content-Type"))
	}
}

func TestStreamingWriterFailureDoesNotRewriteCommittedHTTPStatus(t *testing.T) {
	stream := &streamingEventStream{events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}}}
	executor := &streamingExecutor{stream: stream}
	w := newStreamingResponseWriter()
	w.failAt = 2
	newStreamingHandler(executor).ServeHTTP(w, streamingRequest(context.Background()))
	_, status, _, _ := w.snapshot()
	if status != http.StatusOK {
		t.Fatalf("writer failure after first frame rewrote HTTP status to %d", status)
	}
	if executor.calls != 1 || stream.closeCount() != 1 {
		t.Fatalf("expected one execution and one close, calls=%d closes=%d", executor.calls, stream.closeCount())
	}
}

func TestStreamingPartialWriteFailureIsTreatedAsCommitted(t *testing.T) {
	stream := &streamingEventStream{events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}}}
	executor := &streamingExecutor{stream: stream}
	w := newStreamingResponseWriter()
	w.failAt = 1
	w.partialFail = true
	newStreamingHandler(executor).ServeHTTP(w, streamingRequest(context.Background()))

	body, status, _, _ := w.snapshot()
	if status != http.StatusOK {
		t.Fatalf("partial write after bytes committed rewrote status to %d; body=%s", status, body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("partial write after commit mutated headers, content-type=%q", ct)
	}
	if bytes.Contains([]byte(body), []byte(`"code"`)) || bytes.Contains([]byte(body), []byte(`{"error"`)) {
		t.Fatalf("partial write after commit appended a JSON error after SSE bytes: %s", body)
	}
	if executor.calls != 1 || stream.closeCount() != 1 {
		t.Fatalf("expected one execution and one close, calls=%d closes=%d", executor.calls, stream.closeCount())
	}
}

func TestStreamingWriterFailureBeforeCommitmentClosesWithoutRetry(t *testing.T) {
	stream := &streamingEventStream{events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}}}
	executor := &streamingExecutor{stream: stream}
	w := newStreamingResponseWriter()
	w.failAt = 1
	newStreamingHandler(executor).ServeHTTP(w, streamingRequest(context.Background()))
	body, _, _, _ := w.snapshot()
	if bytes.Contains([]byte(body), []byte("event:")) {
		t.Fatalf("failed first write unexpectedly produced an SSE frame: %s", body)
	}
	if executor.calls != 1 || stream.closeCount() != 1 {
		t.Fatalf("expected one execution and one close, calls=%d closes=%d", executor.calls, stream.closeCount())
	}
}

func TestStreamingBackpressureStopsReadsUntilWriterContinues(t *testing.T) {
	release := make(chan struct{})
	seen := make(chan int, 8)
	stream := &streamingEventStream{
		events: []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "hello"},
			{Kind: aipapi.EventResponseFinished},
		},
		recvSeen: seen,
	}
	executor := &streamingExecutor{stream: stream}
	w := newStreamingResponseWriter()
	w.blockAt = 2
	w.release = release
	done := make(chan struct{})
	go func() {
		newStreamingHandler(executor).ServeHTTP(w, streamingRequest(context.Background()))
		close(done)
	}()

	deadline := time.After(time.Second)
	for {
		if _, _, writes, _ := w.snapshot(); writes >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("writer did not reach backpressure point")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case got := <-seen:
		if got != 1 {
			t.Fatalf("expected first Recv before blocked write, got %d", got)
		}
	case <-time.After(time.Second):
		t.Fatal("missing first Recv")
	}
	select {
	case got := <-seen:
		if got != 2 {
			t.Fatalf("expected second Recv before blocked write, got %d", got)
		}
	case <-time.After(time.Second):
		t.Fatal("missing second Recv")
	}
	select {
	case got := <-seen:
		t.Fatalf("read event %d while writer was blocked", got)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not finish after writer resumed")
	}
}

func TestStreamingCancellationClosesBackendOnceAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := &streamingEventStream{events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}}, wait: make(chan struct{})}
	executor := &streamingExecutor{stream: stream}
	w := newStreamingResponseWriter()
	done := make(chan struct{})
	go func() {
		newStreamingHandler(executor).ServeHTTP(w, streamingRequest(ctx))
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled stream did not stop promptly")
	}
	// The streaming work runs in its own goroutine, so the backend close can
	// land just after the handler returned. Wait for the observable outcome
	// instead of assuming the ordering: a close that never arrives still
	// fails, and a second close is still rejected.
	deadline := time.Now().Add(time.Second)
	for stream.closeCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stream.closeCount() != 1 {
		t.Fatalf("expected exactly one backend close, got %d", stream.closeCount())
	}
}

func TestStreamingDeadlineExceededBeforeCommitReturnsGatewayTimeout(t *testing.T) {
	stream := &streamingEventStream{err: context.DeadlineExceeded}
	handler := newStreamingHandler(&streamingExecutor{stream: stream})
	w := newStreamingResponseWriter()

	handler.ServeHTTP(w, streamingRequest(context.Background()))

	body, status, _, _ := w.snapshot()
	if status != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504; body=%s", status, body)
	}
	if !bytes.Contains([]byte(body), []byte(`"code":"timeout"`)) {
		t.Fatalf("body = %q, want canonical timeout error", body)
	}
	if stream.closeCount() != 1 {
		t.Fatalf("stream close count = %d, want 1", stream.closeCount())
	}
}

func TestStreamingTerminalOwnershipStopsDuplicateBackendTerminal(t *testing.T) {
	stream := &streamingEventStream{events: []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished},
		{Kind: aipapi.EventResponseFinished},
	}}
	executor := &streamingExecutor{stream: stream}
	w := newStreamingResponseWriter()
	newStreamingHandler(executor).ServeHTTP(w, streamingRequest(context.Background()))
	body, status, _, _ := w.snapshot()
	if status != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", status, body)
	}
	if bytes.Count([]byte(body), []byte("event: response.completed")) != 1 || bytes.Count([]byte(body), []byte("data: [DONE]")) != 1 {
		t.Fatalf("duplicate backend terminal escaped frontend ownership: %s", body)
	}
	if stream.closeCount() != 1 {
		t.Fatalf("expected one backend close, got %d", stream.closeCount())
	}
}
