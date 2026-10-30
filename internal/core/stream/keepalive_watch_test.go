package stream

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ctxRecordingStream records the detached context every read observed, so a test
// can prove the reader reuses one cancel context across reads instead of
// allocating one per streamed event.
type ctxRecordingStream struct {
	mu       sync.Mutex
	contexts []context.Context
	yield    int
}

func (s *ctxRecordingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	s.mu.Lock()
	s.contexts = append(s.contexts, ctx)
	remaining := s.yield
	s.yield--
	s.mu.Unlock()
	if remaining <= 0 {
		return aipapi.Event{}, errors.New("ctxRecordingStream: exhausted")
	}
	return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, nil
}

func (s *ctxRecordingStream) Close() error { return nil }

func (s *ctxRecordingStream) observed() []context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]context.Context(nil), s.contexts...)
}

// TestKeepalive_reusesOneCancelContextPerReader pins the reader-side cancel
// context reuse. A streamed response reads once per canonical event, so a cancel
// context allocated per read puts a context allocation on the per-event path; an
// aborted read always ends the reader loop, so reuse can never hand a cancelled
// context to a later read.
func TestKeepalive_reusesOneCancelContextPerReader(t *testing.T) {
	t.Parallel()

	inner := &ctxRecordingStream{yield: 3}
	ka, err := NewKeepalive(inner, KeepaliveConfig{Interval: time.Hour})
	if err != nil {
		t.Fatalf("NewKeepalive: %v", err)
	}
	defer func() { _ = ka.Close() }()

	ctx := context.Background()
	for call := 1; call <= 3; call++ {
		if _, err := ka.Recv(ctx); err != nil {
			t.Fatalf("Recv %d: %v", call, err)
		}
	}

	observed := inner.observed()
	if len(observed) < 2 {
		t.Fatalf("inner stream observed %d reads, want at least 2", len(observed))
	}
	first := observed[0]
	if first == nil {
		t.Fatal("inner stream observed a nil context")
	}
	for i, got := range observed {
		if got != first {
			t.Fatalf("read %d observed a new cancel context; want one reused context per reader", i)
		}
	}
}

// abortRecordingStream yields one ready event and then blocks in the next read
// until the detached read context ends, reporting that release on exited.
type abortRecordingStream struct {
	mu      sync.Mutex
	reads   int
	entered chan struct{}
	exited  chan struct{}
	once    sync.Once
}

func newAbortRecordingStream() *abortRecordingStream {
	return &abortRecordingStream{
		entered: make(chan struct{}),
		exited:  make(chan struct{}),
	}
}

func (s *abortRecordingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	s.mu.Lock()
	s.reads++
	first := s.reads == 1
	s.mu.Unlock()
	if first {
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, nil
	}
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	close(s.exited)
	return aipapi.Event{}, ctx.Err()
}

func (s *abortRecordingStream) Close() error { return nil }

// TestKeepalive_abortWatcherStaysArmedAcrossRecv pins that the abort watcher is
// kept for the whole stream instead of being registered per Recv call: the reader
// is one event ahead, so a caller context that ends between two Recv calls must
// still release the read the reader already started, which is the documented
// reason the abort path exists.
func TestKeepalive_abortWatcherStaysArmedAcrossRecv(t *testing.T) {
	t.Parallel()

	inner := newAbortRecordingStream()
	ka, err := NewKeepalive(inner, KeepaliveConfig{Interval: time.Hour})
	if err != nil {
		t.Fatalf("NewKeepalive: %v", err)
	}
	defer func() { _ = ka.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := ka.Recv(ctx); err != nil {
		cancel()
		t.Fatalf("first Recv: %v", err)
	}

	select {
	case <-inner.entered:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("reader never started the read after the delivered event")
	}

	// No Recv call is in flight at this point: the event was already returned.
	cancel()

	select {
	case <-inner.exited:
	case <-time.After(3 * time.Second):
		t.Fatal("caller cancellation between Recv calls did not abort the in-flight read")
	}
}
