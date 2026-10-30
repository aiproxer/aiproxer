package frontendpipe_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// keepaliveComment is the exact SSE comment frame PumpSSE writes for a canonical
// keepalive event. Pinned byte-for-byte: it is wire-visible client framing.
const keepaliveComment = ": keepalive\n\n"

// scriptedStream replays a fixed event script and then reports io.EOF. Close is
// counted so tests can prove which layer owns stream lifetime.
type scriptedStream struct {
	mu       sync.Mutex
	evs      []aipapi.Event
	i        int
	closes   int
	closeErr error
}

func (s *scriptedStream) Recv(context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.evs) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.evs[s.i]
	s.i++
	return ev, nil
}

func (s *scriptedStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
	return s.closeErr
}

func (s *scriptedStream) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

// gatedStream blocks in Recv once its script is exhausted until release is closed
// (then io.EOF) or until the context it was handed ends. It exists to hold the
// pump inside Recv so recovery keepalives and cancellation are observable.
type gatedStream struct {
	mu       sync.Mutex
	evs      []aipapi.Event
	i        int
	closes   int
	closeErr error
	release  chan struct{}
	blocked  chan struct{}
}

func newGatedStream(evs ...aipapi.Event) *gatedStream {
	return &gatedStream{evs: evs, release: make(chan struct{}), blocked: make(chan struct{})}
}

func (s *gatedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	if s.i < len(s.evs) {
		ev := s.evs[s.i]
		s.i++
		s.mu.Unlock()
		return ev, nil
	}
	s.mu.Unlock()
	select {
	case <-s.blocked:
	default:
		close(s.blocked)
	}
	select {
	case <-s.release:
		return aipapi.Event{}, io.EOF
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (s *gatedStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
	return s.closeErr
}

func (s *gatedStream) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

// flushSpy is an http.ResponseWriter that also satisfies http.Flusher. It records
// the exact bytes written and counts flushes, and signals every flush so tests
// observe progress instead of sleeping.
type flushSpy struct {
	mu       sync.Mutex
	hdr      http.Header
	buf      bytes.Buffer
	flushes  int
	writeErr error
	flushed  chan struct{}
}

func newFlushSpy() *flushSpy {
	return &flushSpy{hdr: http.Header{}, flushed: make(chan struct{}, 1024)}
}

func (s *flushSpy) Header() http.Header { return s.hdr }

func (s *flushSpy) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.buf.Write(p)
}

func (s *flushSpy) WriteHeader(int) {}

func (s *flushSpy) Flush() {
	s.mu.Lock()
	s.flushes++
	s.mu.Unlock()
	select {
	case s.flushed <- struct{}{}:
	default:
	}
}

func (s *flushSpy) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *flushSpy) flushCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushes
}

// nonFlusher is an http.ResponseWriter that deliberately lacks http.Flusher.
type nonFlusher struct {
	body bytes.Buffer
}

func (w *nonFlusher) Header() http.Header { return http.Header{} }

func (w *nonFlusher) Write(p []byte) (int, error) { return w.body.Write(p) }

func (w *nonFlusher) WriteHeader(int) {}

// assertOnlyKeepaliveComments verifies got is a whole number of keepalive
// comment frames and returns that count. Any other byte fails the test.
func assertOnlyKeepaliveComments(t *testing.T, got string) int {
	t.Helper()
	n := 0
	for len(got) > 0 {
		if len(got) < len(keepaliveComment) || got[:len(keepaliveComment)] != keepaliveComment {
			t.Fatalf("after %d keepalive comments got unexpected bytes: %q", n, got)
		}
		got = got[len(keepaliveComment):]
		n++
	}
	return n
}

func neverHandle(t *testing.T) func(aipapi.Event) (bool, error) {
	t.Helper()
	return func(ev aipapi.Event) (bool, error) {
		t.Errorf("unexpected event reached handler: %+v", ev)
		return false, nil
	}
}

func TestPumpSSE_keepaliveWarningIsFramedAndNotHandled(t *testing.T) {
	t.Parallel()
	es := &scriptedStream{evs: []aipapi.Event{
		{Kind: aipapi.EventWarning, WarningCode: stream.KeepaliveEventCode},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
		{Kind: aipapi.EventResponseFinished},
	}}
	w := newFlushSpy()
	var deltas []string
	var handled []aipapi.Event
	err := frontendpipe.PumpSSE(context.Background(), w, es, errors.New("eof"), func(ev aipapi.Event) (bool, error) {
		handled = append(handled, ev)
		if ev.Kind == aipapi.EventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
		return ev.Kind == aipapi.EventResponseFinished, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(deltas) != 1 || deltas[0] != "hi" {
		t.Fatalf("deltas = %v", deltas)
	}
	// The keepalive event reaches the wire as exactly one SSE comment frame and
	// is never handed to handle: handle owns canonical event encoding only.
	if got := w.body(); got != keepaliveComment {
		t.Fatalf("body = %q want %q", got, keepaliveComment)
	}
	if len(handled) != 2 ||
		handled[0].Kind != aipapi.EventTextDelta || handled[0].Delta != "hi" ||
		handled[1].Kind != aipapi.EventResponseFinished {
		t.Fatalf("handle saw %d events %+v, want only the text delta and the terminal event", len(handled), handled)
	}
	// One flush for the keepalive frame; the terminal event stops the pump before
	// any further keepalive can be written.
	if got := w.flushCount(); got != 1 {
		t.Fatalf("flush count = %d want 1", got)
	}
	if got := es.closeCount(); got != 1 {
		t.Fatalf("close count = %d want 1", got)
	}
}

func TestPumpSSE_nonKeepaliveWarningReachesHandle(t *testing.T) {
	t.Parallel()
	es := &scriptedStream{evs: []aipapi.Event{
		{Kind: aipapi.EventWarning, WarningCode: "rate_limit", WarningMessage: "slow down"},
		{Kind: aipapi.EventResponseFinished},
	}}
	w := newFlushSpy()
	var seen []string
	err := frontendpipe.PumpSSE(context.Background(), w, es, errors.New("eof"), func(ev aipapi.Event) (bool, error) {
		if ev.Kind == aipapi.EventWarning {
			seen = append(seen, ev.WarningCode)
		}
		return ev.Kind == aipapi.EventResponseFinished, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "rate_limit" {
		t.Fatalf("warnings reaching handle = %v", seen)
	}
	if got := w.body(); got != "" {
		t.Fatalf("non-keepalive warning wrote %q, want empty body", got)
	}
	if got := w.flushCount(); got != 0 {
		t.Fatalf("flush count = %d want 0", got)
	}
}

func TestPumpSSE_appliesRecoveryKeepaliveFromContext(t *testing.T) {
	t.Parallel()
	es := newGatedStream()
	w := newFlushSpy()
	wantEOF := errors.New("ended without terminal event")
	errCh := make(chan error, 1)
	go func() {
		errCh <- frontendpipe.PumpSSE(
			stream.ContextWithKeepaliveInterval(context.Background(), 5*time.Millisecond),
			w, es, wantEOF, neverHandle(t),
		)
	}()
	// The pump must keep emitting the SSE comment frame while the canonical stream
	// is idle: proof it still wraps the stream with the context recovery keepalive.
	select {
	case <-w.flushed:
	case <-time.After(10 * time.Second):
		t.Fatalf("no recovery keepalive flush observed; body = %q", w.body())
	}
	if n := assertOnlyKeepaliveComments(t, w.body()); n < 1 {
		t.Fatalf("recovery keepalive comments = %d want >= 1", n)
	}
	close(es.release)
	if err := <-errCh; !errors.Is(err, wantEOF) {
		t.Fatalf("err = %v want %v", err, wantEOF)
	}
	if got := es.closeCount(); got != 1 {
		t.Fatalf("close count = %d want 1", got)
	}
}

func TestPumpSSE_eofWithoutTerminalEvent(t *testing.T) {
	t.Parallel()
	t.Run("custom eof error returned verbatim", func(t *testing.T) {
		t.Parallel()
		es := &scriptedStream{}
		want := errors.New("custom eof")
		err := frontendpipe.PumpSSE(context.Background(), newFlushSpy(), es, want, neverHandle(t))
		if err != want {
			t.Fatalf("err = %v want identity %v", err, want)
		}
		if !errors.Is(err, want) {
			t.Fatalf("errors.Is(err, want) = false for %v", err)
		}
		if got := es.closeCount(); got != 1 {
			t.Fatalf("close count = %d want 1", got)
		}
	})
	t.Run("nil eof error becomes canonical message", func(t *testing.T) {
		t.Parallel()
		es := &scriptedStream{}
		err := frontendpipe.PumpSSE(context.Background(), newFlushSpy(), es, nil, neverHandle(t))
		if err == nil || err.Error() != "stream: ended without terminal event" {
			t.Fatalf("err = %v want %q", err, "stream: ended without terminal event")
		}
		if errors.Is(err, io.EOF) {
			t.Fatalf("err = %v must not leak io.EOF identity", err)
		}
		if got := es.closeCount(); got != 1 {
			t.Fatalf("close count = %d want 1", got)
		}
	})
}

// failingStream reports one transport failure from every Recv.
type failingStream struct {
	scriptedStream
	err error
}

func (s *failingStream) Recv(context.Context) (aipapi.Event, error) { return aipapi.Event{}, s.err }

func TestPumpSSE_receiveErrorPropagatesAndCloses(t *testing.T) {
	t.Parallel()
	want := errors.New("upstream reset")
	es := &failingStream{err: want}
	err := frontendpipe.PumpSSE(context.Background(), newFlushSpy(), es, errors.New("unused eof"), neverHandle(t))
	if !errors.Is(err, want) {
		t.Fatalf("err = %v want %v", err, want)
	}
	if got := es.closeCount(); got != 1 {
		t.Fatalf("close count = %d want 1", got)
	}
}

func TestPumpSSE_terminalErrorEventMapsToStreamError(t *testing.T) {
	t.Parallel()
	es := &scriptedStream{evs: []aipapi.Event{
		{Kind: aipapi.EventError, ErrorCode: "upstream_overloaded", ErrorMessage: "backend busy"},
	}}
	err := frontendpipe.PumpSSE(context.Background(), newFlushSpy(), es, errors.New("unused eof"), neverHandle(t))
	var se *aipapi.StreamError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T) want *aipapi.StreamError", err, err)
	}
	if se.Code != "upstream_overloaded" || se.Message != "backend busy" {
		t.Fatalf("StreamError = %+v want code %q message %q", se, "upstream_overloaded", "backend busy")
	}
	if !errors.Is(err, aipapi.ErrStreamTerminal) {
		t.Fatalf("err = %v must unwrap to aipapi.ErrStreamTerminal", err)
	}
	if err.Error() != aipapi.ErrStreamTerminal.Error() {
		t.Fatalf("err.Error() = %q want %q", err.Error(), aipapi.ErrStreamTerminal.Error())
	}
	if got := es.closeCount(); got != 1 {
		t.Fatalf("close count = %d want 1", got)
	}
}

func TestPumpSSE_nonFlusherResponseWriterIsRejected(t *testing.T) {
	t.Parallel()
	es := &scriptedStream{evs: []aipapi.Event{
		{Kind: aipapi.EventWarning, WarningCode: stream.KeepaliveEventCode},
	}}
	w := &nonFlusher{}
	err := frontendpipe.PumpSSE(context.Background(), w, es, errors.New("unused eof"), neverHandle(t))
	if err == nil || err.Error() != "stream: ResponseWriter is not a Flusher" {
		t.Fatalf("err = %v want %q", err, "stream: ResponseWriter is not a Flusher")
	}
	// The pump owns the stream once wrapped: even this early return closes it,
	// and no keepalive byte is written before the flusher check.
	if got := w.body.String(); got != "" {
		t.Fatalf("body = %q want empty (flusher check must precede writes)", got)
	}
	if got := es.closeCount(); got != 1 {
		t.Fatalf("close count = %d want 1", got)
	}
}

func TestPumpSSE_handleDoneStopsAndClosesStream(t *testing.T) {
	t.Parallel()
	es := &scriptedStream{evs: []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "a"},
		{Kind: aipapi.EventTextDelta, Delta: "b"},
	}}
	var seen []string
	err := frontendpipe.PumpSSE(context.Background(), newFlushSpy(), es, errors.New("unused eof"), func(ev aipapi.Event) (bool, error) {
		seen = append(seen, ev.Delta)
		return true, nil
	})
	if err != nil {
		t.Fatalf("err = %v want nil", err)
	}
	if len(seen) != 1 || seen[0] != "a" {
		t.Fatalf("handle saw %v want exactly [a]", seen)
	}
	if got := es.closeCount(); got != 1 {
		t.Fatalf("close count = %d want 1", got)
	}
}

func TestPumpSSE_handleErrorIsReturnedUnchanged(t *testing.T) {
	t.Parallel()
	want := errors.New("encoder write failed")
	es := &scriptedStream{evs: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}}}
	err := frontendpipe.PumpSSE(context.Background(), newFlushSpy(), es, errors.New("unused eof"), func(aipapi.Event) (bool, error) {
		return false, want
	})
	if err != want {
		t.Fatalf("err = %v want identity %v", err, want)
	}
	if got := es.closeCount(); got != 1 {
		t.Fatalf("close count = %d want 1", got)
	}
}

func TestPumpSSE_handleDoneWithErrorStillReturnsError(t *testing.T) {
	t.Parallel()
	want := errors.New("terminal encode failed")
	es := &scriptedStream{evs: []aipapi.Event{{Kind: aipapi.EventResponseFinished}}}
	err := frontendpipe.PumpSSE(context.Background(), newFlushSpy(), es, errors.New("unused eof"), func(aipapi.Event) (bool, error) {
		return true, want
	})
	if err != want {
		t.Fatalf("err = %v want identity %v", err, want)
	}
}

type closeOrderStream struct {
	*scriptedStream
	order *[]string
}

func (s *closeOrderStream) Close() error {
	*s.order = append(*s.order, "close")
	return s.scriptedStream.Close()
}

func TestPumpSSE_closeErrorIsJoinedWithHandleError(t *testing.T) {
	t.Parallel()
	handleErr := errors.New("encoder write failed")
	closeErr := errors.New("close boom")
	var order []string
	es := &closeOrderStream{
		scriptedStream: &scriptedStream{
			evs:      []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}},
			closeErr: closeErr,
		},
		order: &order,
	}
	err := frontendpipe.PumpSSE(context.Background(), newFlushSpy(), es, errors.New("unused eof"), func(aipapi.Event) (bool, error) {
		order = append(order, "handle")
		return false, handleErr
	})
	if !errors.Is(err, handleErr) {
		t.Fatalf("err = %v must still match handle error %v", err, handleErr)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("err = %v must still match close error %v", err, closeErr)
	}
	if got, want := err.Error(), handleErr.Error()+"\nstream: close event stream: "+closeErr.Error(); got != want {
		t.Fatalf("joined message = %q want %q", got, want)
	}
	if len(order) != 2 || order[0] != "handle" || order[1] != "close" {
		t.Fatalf("operation order = %v want [handle close]", order)
	}
	if got := es.closeCount(); got != 1 {
		t.Fatalf("close count = %d want 1", got)
	}
}

func TestPumpSSE_closeErrorAloneIsReturned(t *testing.T) {
	t.Parallel()
	closeErr := errors.New("close boom")
	es := &scriptedStream{
		evs:      []aipapi.Event{{Kind: aipapi.EventResponseFinished}},
		closeErr: closeErr,
	}
	err := frontendpipe.PumpSSE(context.Background(), newFlushSpy(), es, errors.New("unused eof"), func(aipapi.Event) (bool, error) {
		return true, nil
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("err = %v want %v", err, closeErr)
	}
	if !strings.Contains(err.Error(), "stream: close event stream") {
		t.Fatalf("err = %v missing canonical close prefix", err)
	}
}

func TestPumpSSE_keepaliveWriteFailurePropagates(t *testing.T) {
	t.Parallel()
	writeErr := errors.New("broken pipe")
	w := newFlushSpy()
	w.writeErr = writeErr
	es := &scriptedStream{evs: []aipapi.Event{
		{Kind: aipapi.EventWarning, WarningCode: stream.KeepaliveEventCode},
		{Kind: aipapi.EventTextDelta, Delta: "a"},
	}}
	err := frontendpipe.PumpSSE(context.Background(), w, es, errors.New("unused eof"), neverHandle(t))
	if err != writeErr {
		t.Fatalf("err = %v want %v", err, writeErr)
	}
	if got := w.flushCount(); got != 0 {
		t.Fatalf("flush count = %d want 0 (write failed before flush)", got)
	}
	if got := es.closeCount(); got != 1 {
		t.Fatalf("close count = %d want 1", got)
	}
}

func TestPumpSSE_cancellationReturnsContextError(t *testing.T) {
	t.Parallel()
	es := newGatedStream()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- frontendpipe.PumpSSE(ctx, newFlushSpy(), es, errors.New("unused eof"), neverHandle(t))
	}()
	select {
	case <-es.blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("pump never entered Recv")
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pump did not return after cancellation")
	}
	if got := es.closeCount(); got != 1 {
		t.Fatalf("close count = %d want 1", got)
	}
}

func TestPumpSSE_nilEventStreamIsRejectedBeforeWriting(t *testing.T) {
	t.Parallel()
	w := newFlushSpy()
	err := frontendpipe.PumpSSE(context.Background(), w, nil, errors.New("unused eof"), neverHandle(t))
	if !errors.Is(err, aipapi.ErrNilEventStream) {
		t.Fatalf("err = %v want aipapi.ErrNilEventStream", err)
	}
	if got := w.body(); got != "" {
		t.Fatalf("body = %q want empty", got)
	}
}

// TestPumpSSE_recoveryKeepaliveCommentReachesARealHTTPClient proves a keepalive
// frame produced by the recovery keepalive wrapper reaches a connected HTTP
// client as exact bytes while the response is still open - the real net/http
// stack, not just the in-process flushSpy. The explicit post-frame Flush is
// pinned by TestPumpSSE_keepaliveWarningIsFramedAndNotHandled (flush count) and
// TestPumpSSE_appliesRecoveryKeepaliveFromContext (flush signal); this test
// cannot reject a dropped Flush on its own because net/http still delivers
// buffered bytes once its buffer fills.
func TestPumpSSE_recoveryKeepaliveCommentReachesARealHTTPClient(t *testing.T) {
	t.Parallel()
	es := newGatedStream()
	wantEOF := errors.New("ended without terminal event")
	errCh := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		ctx := stream.ContextWithKeepaliveInterval(context.Background(), 10*time.Millisecond)
		errCh <- frontendpipe.PumpSSE(ctx, w, es, wantEOF, neverHandle(t))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("close response body: %v", err)
		}
	}()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}

	got := make([]byte, len(keepaliveComment))
	if _, err := io.ReadFull(resp.Body, got); err != nil {
		t.Fatalf("read keepalive comment: %v", err)
	}
	if string(got) != keepaliveComment {
		t.Fatalf("first flushed bytes = %q want %q", got, keepaliveComment)
	}
	// A second frame proves the pump keeps flushing while the canonical stream is idle.
	got2 := make([]byte, len(keepaliveComment))
	if _, err := io.ReadFull(resp.Body, got2); err != nil {
		t.Fatalf("read second keepalive comment: %v", err)
	}
	if string(got2) != keepaliveComment {
		t.Fatalf("second flushed bytes = %q want %q", got2, keepaliveComment)
	}

	close(es.release)
	if err := <-errCh; !errors.Is(err, wantEOF) {
		t.Fatalf("err = %v want %v", err, wantEOF)
	}
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	// Anything the pump emitted between the second read and stream close must
	// still be whole keepalive comment frames and nothing else.
	assertOnlyKeepaliveComments(t, string(rest))
	if got := es.closeCount(); got != 1 {
		t.Fatalf("close count = %d want 1", got)
	}
}
