package runtime_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type ignoreCtxBlockingStream struct {
	release chan struct{}
}

func newIgnoreCtxBlockingStream() *ignoreCtxBlockingStream {
	return &ignoreCtxBlockingStream{release: make(chan struct{})}
}

func (s *ignoreCtxBlockingStream) Recv(context.Context) (aipapi.Event, error) {
	<-s.release
	return aipapi.Event{}, io.EOF
}

func (s *ignoreCtxBlockingStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

func (s *ignoreCtxBlockingStream) Close() error { return nil }

func blockingRecvBackend(track *[]*ignoreCtxBlockingStream, mu *sync.Mutex) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			s := newIgnoreCtxBlockingStream()
			mu.Lock()
			*track = append(*track, s)
			mu.Unlock()
			return s, nil
		},
	}
}

func TestParallelRace_ParentContextCancelReturnsAfterBlockedLegsRelease(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var (
		streamsMu sync.Mutex
		streams   []*ignoreCtxBlockingStream
	)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"a": blockingRecvBackend(&streams, &streamsMu),
		"b": blockingRecvBackend(&streams, &streamsMu),
	}
	ex.Rand = routing.NewSeededRng(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := ex.Execute(ctx, parallelCall("a:model!b:model"))
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	time.Sleep(50 * time.Millisecond)
	streamsMu.Lock()
	for _, s := range streams {
		close(s.release)
	}
	streamsMu.Unlock()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute blocked past deadline after blocked legs were released")
	}
	time.Sleep(100 * time.Millisecond)
}
