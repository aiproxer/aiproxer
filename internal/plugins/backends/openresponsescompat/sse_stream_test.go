package openresponsescompat

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type bodyRecorder struct {
	io.Reader
	mu     sync.Mutex
	closes int
}

func (b *bodyRecorder) Close() error {
	b.mu.Lock()
	b.closes++
	b.mu.Unlock()
	return nil
}

func (b *bodyRecorder) closeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closes
}

func textSSEBody() string {
	var b strings.Builder
	for _, r := range textRecords() {
		b.WriteString("event: " + r.eventType + "\n")
		b.WriteString("data: " + string(r.data) + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func TestSSEStream_DrainsFullLifecycleToEOF(t *testing.T) {
	t.Parallel()
	body := &bodyRecorder{Reader: strings.NewReader(textSSEBody())}
	es := newSSEStream("my-or", body, defaultResponseTestLimits(), 0)
	events, err := drainStream(es)
	if err != nil {
		t.Fatal(err)
	}
	if err := es.Close(); err != nil {
		t.Fatal(err)
	}
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventTextDelta,
		aipapi.EventUsageDelta,
		aipapi.EventResponseFinished,
	}
	assertKinds(t, kindsOf(events), want)
	if events[2].Delta != "Hello" || events[3].Delta != " world" {
		t.Fatalf("deltas = %q %q", events[2].Delta, events[3].Delta)
	}
	if body.closeCount() != 1 {
		t.Fatalf("body closed %d times, want 1", body.closeCount())
	}
}

func TestSSEStream_CloseIdempotentAndCancel(t *testing.T) {
	t.Parallel()
	body := &bodyRecorder{Reader: strings.NewReader(textSSEBody())}
	es := newSSEStream("my-or", body, defaultResponseTestLimits(), 0)
	if err := es.Close(); err != nil {
		t.Fatal(err)
	}
	if err := es.Close(); err != nil {
		t.Fatal(err)
	}
	res := es.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeTransport {
		t.Fatalf("cancel mode = %q", res.Mode)
	}
	if body.closeCount() != 1 {
		t.Fatalf("body closed %d times, want exactly 1", body.closeCount())
	}
	if _, err := es.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv after close = %v, want io.EOF", err)
	}
}

func TestSSEStream_ProviderDisconnectAfterOutputIsStreamError(t *testing.T) {
	t.Parallel()
	// chunk1 carries response.created (first canonical event); the provider then
	// disconnects with a transport-style error before the rest of the stream.
	body := &bodyRecorder{Reader: &chunkErrorReader{
		chunks: [][]byte{[]byte(sseEvent("response.created", `{"type":"response.created","sequence_number":0}`) + "\n")},
		err:    io.ErrUnexpectedEOF,
	}}
	es := newSSEStream("my-or", body, defaultResponseTestLimits(), 0)
	defer func() { _ = es.Close() }()

	ev, err := es.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("first event = %+v", ev)
	}
	_, err = es.Recv(context.Background())
	if err == nil {
		t.Fatal("expected provider disconnect to surface as a stream error")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("post-output disconnect must not be recoverable: %v", err)
	}
	if err := es.Close(); err != nil {
		t.Fatal(err)
	}
	if body.closeCount() != 1 {
		t.Fatalf("body closed %d times, want 1", body.closeCount())
	}
}

func TestSSEStream_MissingDONEIsError(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for _, r := range textRecords() {
		b.WriteString("event: " + r.eventType + "\n")
		b.WriteString("data: " + string(r.data) + "\n\n")
	}
	es := newSSEStream("my-or", &bodyRecorder{Reader: strings.NewReader(b.String())}, defaultResponseTestLimits(), 0)
	defer func() { _ = es.Close() }()

	for i := range 6 {
		if _, err := es.Recv(context.Background()); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	_, err := es.Recv(context.Background())
	if err == nil || !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
	if !strings.Contains(err.Error(), "[DONE]") {
		t.Fatalf("error = %v, want [DONE] mention", err)
	}
}

func TestSSEStream_EventAfterTerminalIsError(t *testing.T) {
	t.Parallel()
	records := append([]sseRecord(nil), textRecords()...)
	records = append(records, rec("response.output_item.added", `{"type":"response.output_item.added","sequence_number":9,"item":{"id":"m2","type":"message","role":"assistant"}}`))
	var b strings.Builder
	for _, r := range records {
		b.WriteString("event: " + r.eventType + "\n")
		b.WriteString("data: " + string(r.data) + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	es := newSSEStream("my-or", &bodyRecorder{Reader: strings.NewReader(b.String())}, defaultResponseTestLimits(), 0)
	defer func() { _ = es.Close() }()

	for i := range 6 {
		if _, err := es.Recv(context.Background()); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
	}
	_, err := es.Recv(context.Background())
	if err == nil || !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
	if !strings.Contains(err.Error(), "after the terminal") {
		t.Fatalf("error = %v", err)
	}
}

func TestSSEStream_DONEBeforeTerminalIsError(t *testing.T) {
	t.Parallel()
	body := sseEvent("response.created", `{"type":"response.created","sequence_number":0}`) + "data: [DONE]\n\n"
	es := newSSEStream("my-or", &bodyRecorder{Reader: strings.NewReader(body)}, defaultResponseTestLimits(), 0)
	defer func() { _ = es.Close() }()
	if ev, err := es.Recv(context.Background()); err != nil || ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("first recv = %+v, %v", ev, err)
	}
	_, err := es.Recv(context.Background())
	if err == nil || !strings.Contains(err.Error(), "[DONE] received before a terminal") {
		t.Fatalf("error = %v", err)
	}
}

func TestSSEStream_EmptyStreamRejected(t *testing.T) {
	t.Parallel()
	es := newSSEStream("my-or", &bodyRecorder{Reader: strings.NewReader("")}, defaultResponseTestLimits(), 0)
	defer func() { _ = es.Close() }()
	_, err := es.Recv(context.Background())
	if err == nil || !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
}

func TestSSEStream_RecvNilContextRejected(t *testing.T) {
	t.Parallel()
	es := newSSEStream("my-or", &bodyRecorder{Reader: strings.NewReader(textSSEBody())}, defaultResponseTestLimits(), 0)
	defer func() { _ = es.Close() }()
	var nilContext context.Context
	if _, err := es.Recv(nilContext); !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("error = %v, want ErrNilContext", err)
	}
}

func TestSSEStream_BlockedRecvUnblockedByClose(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	es := newSSEStream("my-or", pr, defaultResponseTestLimits(), 0)
	defer func() { _ = es.Close() }()

	go func() {
		_, _ = io.WriteString(pw, sseEvent("response.created", `{"type":"response.created","sequence_number":0}`))
	}()
	if ev, err := es.Recv(context.Background()); err != nil || ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("first recv = %+v, %v", ev, err)
	}

	done := make(chan struct{})
	var recvErr error
	go func() {
		defer close(done)
		_, recvErr = es.Recv(context.Background())
	}()
	// The second Recv must block until Close closes the body.
	select {
	case <-done:
		t.Fatal("second Recv returned before Close")
	case <-time.After(50 * time.Millisecond):
	}
	if err := es.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second Recv did not unblock after Close")
	}
	if !errors.Is(recvErr, io.EOF) {
		t.Fatalf("recv after close = %v, want io.EOF", recvErr)
	}
}

func TestSSEStream_PullDrivenReadsDoNotRunAhead(t *testing.T) {
	t.Parallel()
	// chunk1 contains only the first record; stepReader panics if the stream
	// tries to read chunk2 during the first Recv.
	chunk1 := sseEvent("response.created", `{"type":"response.created","sequence_number":0}`)
	chunk2 := sseEvent("response.completed", `{"type":"response.completed","sequence_number":1}`) + "data: [DONE]\n\n"
	sr := &stepReader{chunks: [][]byte{[]byte(chunk1), []byte(chunk2)}}
	es := newSSEStream("my-or", &bodyRecorder{Reader: sr}, defaultResponseTestLimits(), 0)
	defer func() { _ = es.Close() }()

	if ev, err := es.Recv(context.Background()); err != nil || ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("first recv = %+v, %v", ev, err)
	}
	sr.allow(1)
	if ev, err := es.Recv(context.Background()); err != nil || ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("second recv = %+v, %v", ev, err)
	}
	if _, err := es.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("final recv = %v, want io.EOF", err)
	}
}

func TestSSEStream_PendingBoundRejectsBatchAtomicallyAndDrains(t *testing.T) {
	t.Parallel()
	// A terminal record maps to two canonical events (usage + finished); a
	// maxPending of 1 cannot hold the whole batch. The bound must reject the
	// full batch before appending so no partial batch is buffered after the
	// error, and the stream must drain cleanly to EOF afterwards.
	records := append([]sseRecord(nil), textRecords()...)
	records = append(records, rec("", "[DONE]"))
	var b strings.Builder
	for _, r := range records {
		if r.eventType == "" {
			b.WriteString("data: [DONE]\n\n")
			continue
		}
		b.WriteString("event: " + r.eventType + "\n")
		b.WriteString("data: " + string(r.data) + "\n\n")
	}
	es := newSSEStream("my-or", &bodyRecorder{Reader: strings.NewReader(b.String())}, defaultResponseTestLimits(), 1)
	defer func() { _ = es.Close() }()

	var got []aipapi.Event
	for i := range 4 {
		ev, err := es.Recv(context.Background())
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		got = append(got, ev)
	}
	if _, err := es.Recv(context.Background()); !errors.Is(err, stream.ErrPendingQueueFull) {
		t.Fatalf("pending bound error = %v, want ErrPendingQueueFull", err)
	}
	// No partial batch may surface after the rejection: the rejected usage and
	// finished events must not be delivered out of order after the error.
	for i := range 2 {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("drain recv %d: %v", i, err)
		}
		if ev.Kind == aipapi.EventUsageDelta || ev.Kind == aipapi.EventResponseFinished {
			t.Fatalf("rejected batch event %q delivered after the error", ev.Kind)
		}
		got = append(got, ev)
	}
	// The stream ends cleanly once [DONE] is consumed.
	if _, err := es.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("final recv = %v, want io.EOF", err)
	}
	assertKinds(t, kindsOf(got), []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventTextDelta,
	})
}

func TestSSEStream_PendingBoundAcceptsWholeBatchWithinCap(t *testing.T) {
	t.Parallel()
	// A terminal record maps to usage + finished (2 events); a maxPending of 2
	// must buffer the whole batch in order with zero loss.
	records := append([]sseRecord(nil), textRecords()...)
	records = append(records, rec("", "[DONE]"))
	var b strings.Builder
	for _, r := range records {
		if r.eventType == "" {
			b.WriteString("data: [DONE]\n\n")
			continue
		}
		b.WriteString("event: " + r.eventType + "\n")
		b.WriteString("data: " + string(r.data) + "\n\n")
	}
	es := newSSEStream("my-or", &bodyRecorder{Reader: strings.NewReader(b.String())}, defaultResponseTestLimits(), 2)
	defer func() { _ = es.Close() }()
	events, err := drainStream(es)
	if err != nil {
		t.Fatal(err)
	}
	assertKinds(t, kindsOf(events), []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventTextDelta,
		aipapi.EventUsageDelta,
		aipapi.EventResponseFinished,
	})
}

type chunkErrorReader struct {
	chunks [][]byte
	idx    int
	err    error
}

func (r *chunkErrorReader) Read(p []byte) (int, error) {
	if r.idx < len(r.chunks) {
		chunk := r.chunks[r.idx]
		n := copy(p, chunk)
		r.chunks[r.idx] = chunk[n:]
		if len(chunk) == n {
			r.idx++
		}
		return n, nil
	}
	return 0, r.err
}

type stepReader struct {
	mu      sync.Mutex
	allowed int
	chunks  [][]byte
	idx     int
	off     int
}

func (r *stepReader) allow(n int) {
	r.mu.Lock()
	r.allowed = n
	r.mu.Unlock()
}

func (r *stepReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.idx >= len(r.chunks) {
		return 0, io.EOF
	}
	if r.idx > r.allowed {
		panic("stepReader: read ahead of allowance")
	}
	chunk := r.chunks[r.idx]
	n := copy(p, chunk[r.off:])
	r.off += n
	if r.off >= len(chunk) {
		r.idx++
		r.off = 0
	}
	return n, nil
}

func drainStream(es aipapi.ManagedEventStream) ([]aipapi.Event, error) {
	var events []aipapi.Event
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			return events, nil
		}
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
}
