package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// nonIdempotentBackendStream enforces strictly at-most-one Cancel and at-most-one Close.
// Any double Close or double Cancel panics immediately, proving that attemptSession is
// the sole physical owner and never issues duplicate cleanup calls.
type nonIdempotentBackendStream struct {
	mu          sync.Mutex
	cancelCount atomic.Int32
	closeCount  atomic.Int32
	closed      bool
	canceled    bool
	calls       []string
	blockCh     chan struct{}
	readyCh     chan struct{}
	onceReady   sync.Once
}

func newNonIdempotentBackendStream() *nonIdempotentBackendStream {
	return &nonIdempotentBackendStream{
		blockCh: make(chan struct{}),
		readyCh: make(chan struct{}),
	}
}

func (s *nonIdempotentBackendStream) signalReady() {
	s.onceReady.Do(func() {
		close(s.readyCh)
	})
}

func (s *nonIdempotentBackendStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.signalReady()
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-s.blockCh:
		return aipapi.Event{}, io.EOF
	}
}

func (s *nonIdempotentBackendStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		panic("nonIdempotentBackendStream: Cancel called after Close")
	}
	if s.canceled {
		panic("nonIdempotentBackendStream: double Cancel detected")
	}
	s.canceled = true
	s.cancelCount.Add(1)
	s.calls = append(s.calls, fmt.Sprintf("cancel:%s", cause.Kind))
	select {
	case <-s.blockCh:
	default:
		close(s.blockCh)
	}
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (s *nonIdempotentBackendStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		panic("nonIdempotentBackendStream: double Close detected")
	}
	s.closed = true
	s.closeCount.Add(1)
	s.calls = append(s.calls, "close")
	select {
	case <-s.blockCh:
	default:
		close(s.blockCh)
	}
	return nil
}

func (s *nonIdempotentBackendStream) getCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func TestBlocker3_PublishedClose_SingleCancelAndClose(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	backendStream := newNonIdempotentBackendStream()

	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.ALegLifecycle = coord
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return backendStream, nil
			},
		},
	}

	call := &aipapi.Call{
		Session:  aipapi.SessionRef{ContinuityKey: "blocker3-close"},
		Route:    aipapi.RouteIntent{Selector: "backend:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for stream to be ready
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		_, _ = stream.Recv(context.Background())
	}()

	select {
	case <-backendStream.readyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for backendStream.readyCh")
	}

	// Client closes the stream.
	if err := stream.Close(); err != nil {
		t.Fatalf("stream.Close: %v", err)
	}

	select {
	case <-recvDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream.Recv to exit")
	}

	// Repeated Close on published stream must be safe and idempotent.
	if err := stream.Close(); err != nil {
		t.Fatalf("second stream.Close: %v", err)
	}

	calls := backendStream.getCalls()
	if backendStream.cancelCount.Load() != 1 || backendStream.closeCount.Load() != 1 {
		t.Fatalf("backendStream cancelCount=%d closeCount=%d, want 1/1, calls: %v", backendStream.cancelCount.Load(), backendStream.closeCount.Load(), calls)
	}
	if len(calls) != 2 || calls[0] != "cancel:client_gone" || calls[1] != "close" {
		t.Fatalf("backend calls = %v, want [cancel:client_gone close]", calls)
	}
}

func TestBlocker3_ALegCancellation_SingleCancelAndClose(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	backendStream := newNonIdempotentBackendStream()

	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.ALegLifecycle = coord
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return backendStream, nil
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	call := &aipapi.Call{
		Session:  aipapi.SessionRef{ContinuityKey: "blocker3-aleg-cancel"},
		Route:    aipapi.RouteIntent{Selector: "backend:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}

	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}

	recvDone := make(chan error, 1)
	go func() {
		_, err := stream.Recv(ctx)
		recvDone <- err
	}()

	select {
	case <-backendStream.readyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for backendStream.readyCh")
	}

	// Cancel context / A-leg
	cancel()
	var errRecv error
	select {
	case errRecv = <-recvDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for recvDone after cancel")
	}
	if !errors.Is(errRecv, context.Canceled) && !errors.Is(errRecv, leglifecycle.ErrALegCanceled) {
		t.Fatalf("Recv error = %v, want context.Canceled or ErrALegCanceled", errRecv)
	}

	// Subsequent stream.Close must not double-close backend
	if err := stream.Close(); err != nil {
		t.Fatalf("stream.Close after cancel: %v", err)
	}

	calls := backendStream.getCalls()
	if backendStream.cancelCount.Load() != 1 || backendStream.closeCount.Load() != 1 {
		t.Fatalf("backendStream cancelCount=%d closeCount=%d, want 1/1, calls: %v", backendStream.cancelCount.Load(), backendStream.closeCount.Load(), calls)
	}
	if len(calls) != 2 || calls[0] != "cancel:context_done" || calls[1] != "close" {
		t.Fatalf("backend calls = %v, want [cancel:context_done close]", calls)
	}
}

func TestBlocker3_ConcurrentCloseAndRecv_SingleTerminalWinner(t *testing.T) {
	t.Parallel()
	for iter := range 10 {
		st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		if err != nil {
			t.Fatal(err)
		}
		coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
		backendStream := newNonIdempotentBackendStream()

		ex := TestExecutor()
		ex.Store = st
		ex.Bus = hooks.New(hooks.Config{})
		ex.Rand = routing.NewSeededRng(int64(iter))
		ex.ALegLifecycle = coord
		ex.Backends = map[string]execbackend.Backend{
			"backend": {
				Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return backendStream, nil
				},
			},
		}

		call := &aipapi.Call{
			Session:  aipapi.SessionRef{ContinuityKey: fmt.Sprintf("blocker3-concurrent-%d", iter)},
			Route:    aipapi.RouteIntent{Selector: "backend:m"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
		}

		stream, err := ex.Execute(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}

		// aipapi.EventStream and retryRecvStream allow exactly one concurrent
		// Recv; Close may run concurrently with a blocked Recv. Keep one
		// receiver blocked on the inner stream (readyCh proves it arrived)
		// while two closers race.
		var wg sync.WaitGroup
		wg.Add(3)

		go func() {
			defer wg.Done()
			_, _ = stream.Recv(context.Background())
		}()
		select {
		case <-backendStream.readyCh:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: timed out waiting for backendStream.readyCh", iter)
		}
		go func() {
			defer wg.Done()
			_ = stream.Close()
		}()
		go func() {
			defer wg.Done()
			_ = stream.Close()
		}()

		doneCh := make(chan struct{})
		go func() {
			wg.Wait()
			close(doneCh)
		}()

		select {
		case <-doneCh:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: timed out waiting for concurrent Recv/Close goroutines", iter)
		}

		if backendStream.cancelCount.Load() > 1 || backendStream.closeCount.Load() != 1 {
			t.Fatalf("iter %d: backendStream cancelCount=%d closeCount=%d, want at most 1 cancel and exactly 1 close; calls: %v",
				iter, backendStream.cancelCount.Load(), backendStream.closeCount.Load(), backendStream.getCalls())
		}
	}
}

func TestBlocker3_IdleFinish_SingleCancelAndClose(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	backendStream := newNonIdempotentBackendStream()

	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.ALegLifecycle = coord
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return backendStream, nil
			},
		},
	}

	ex.StreamRecovery = streamrecovery.Config{
		Enabled:     true,
		IdleTimeout: 10 * time.Millisecond,
		EmitWarning: true,
	}

	call := &aipapi.Call{
		Session:  aipapi.SessionRef{ContinuityKey: "blocker3-idle-finish"},
		Route:    aipapi.RouteIntent{Selector: "backend:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}

	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		// Recv triggers backendStream.Recv, idle decision, and drain
		for {
			ev, err := stream.Recv(context.Background())
			if err != nil {
				break
			}
			if ev.Kind != aipapi.EventResponseFinished {
				// While receiving pre-terminal events (such as synthetic warning),
				// physical teardown must not have occurred yet.
				if backendStream.cancelCount.Load() != 0 || backendStream.closeCount.Load() != 0 {
					panic(fmt.Sprintf("backend stream prematurely cancelled/closed before response_finished: %v", backendStream.getCalls()))
				}
			}
			if ev.Kind == aipapi.EventResponseFinished {
				break
			}
		}
	}()

	// Wait for stream to be ready
	select {
	case <-backendStream.readyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for backendStream.readyCh")
	}

	// Wait for Recv loop to complete after idle timeout triggers recovery/exhaustion
	select {
	case <-recvDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Recv loop to complete after idle timeout")
	}

	// Close stream
	if err := stream.Close(); err != nil {
		t.Fatalf("stream.Close: %v", err)
	}

	calls := backendStream.getCalls()
	if backendStream.cancelCount.Load() != 1 || backendStream.closeCount.Load() != 1 {
		t.Fatalf("backendStream cancelCount=%d closeCount=%d, want 1/1, calls: %v", backendStream.cancelCount.Load(), backendStream.closeCount.Load(), calls)
	}
	if len(calls) != 2 || calls[0] != "cancel:context_done" || calls[1] != "close" {
		t.Fatalf("backend calls = %v, want [cancel:context_done close]", calls)
	}
}

func TestBlocker3_RegistrationError_SingleCleanupAndLogicalSettle(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	backendStream := newNonIdempotentBackendStream()

	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.ALegLifecycle = coord
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				// Cancel ALeg right during Open, causing RegisterBLeg to fail
				aLegID := strings.TrimSpace(call.Session.ALegID)
				_ = coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit})
				return backendStream, nil
			},
		},
	}

	call := &aipapi.Call{
		Session:  aipapi.SessionRef{ContinuityKey: "blocker3-reg-err"},
		Route:    aipapi.RouteIntent{Selector: "backend:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}

	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected Execute to fail on canceled ALeg during RegisterBLeg")
	}

	calls := backendStream.getCalls()
	if backendStream.cancelCount.Load() != 1 || backendStream.closeCount.Load() != 1 {
		t.Fatalf("backendStream cancelCount=%d closeCount=%d, want 1/1, calls: %v", backendStream.cancelCount.Load(), backendStream.closeCount.Load(), calls)
	}
}
