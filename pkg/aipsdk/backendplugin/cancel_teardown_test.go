package backendplugin

import (
	"context"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"go.uber.org/goleak"
)

// The control reader has already entered Recv when EOF wins. Close releases
// that Recv with a CANCEL only after the coordinator's initial start check.
type teardownCancelStream struct {
	ctx           context.Context
	recvEntered   chan struct{}
	closeReceived chan struct{}
	closeOnce     sync.Once
	terminals     atomic.Int32
}

func (s *teardownCancelStream) Context() context.Context { return s.ctx }

func (s *teardownCancelStream) Recv() (ClientFrame, error) {
	close(s.recvEntered)
	<-s.closeReceived
	return ClientFrame{Kind: ClientFrameCancel, CancelReason: CancelReasonClient}, nil
}

func (s *teardownCancelStream) Send(frame ServerFrame) error {
	if frame.Kind == ServerFrameTerminal {
		s.terminals.Add(1)
	}
	return nil
}

func (s *teardownCancelStream) Close() error {
	s.closeOnce.Do(func() { close(s.closeReceived) })
	return nil
}

type teardownEOFManaged struct {
	recvEntered <-chan struct{}
	cancels     atomic.Int32
	closes      atomic.Int32
}

func (m *teardownEOFManaged) Recv(context.Context) (aipapi.Event, error) {
	<-m.recvEntered
	return aipapi.Event{}, io.EOF
}
func (m *teardownEOFManaged) Close() error { m.closes.Add(1); return nil }
func (m *teardownEOFManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	m.cancels.Add(1)
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

//nolint:paralleltest // controls scheduler and checks for goroutine leaks
func TestForwardExecute_CancelReceivedDuringReaderJoin(t *testing.T) {
	// With one P, the closer releases Recv after the coordinator blocks joining
	// it. This forces receipt after the initial cancellation-start check.
	previous := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
	defer goleak.VerifyNone(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	stream := &teardownCancelStream{ctx: ctx, recvEntered: make(chan struct{}), closeReceived: make(chan struct{})}
	managed := &teardownEOFManaged{recvEntered: stream.recvEntered}
	done := make(chan error, 1)
	go func() {
		done <- forwardActiveExecute(stream, newFrameSequencer(stream), managed, Negotiation{})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("teardown waited on a cancellation worker that was never started")
	}
	if managed.cancels.Load() != 1 || managed.closes.Load() != 1 || stream.terminals.Load() != 1 {
		t.Fatalf("cancel=%d close=%d terminal=%d; want one of each", managed.cancels.Load(), managed.closes.Load(), stream.terminals.Load())
	}
}
