package backendplugin_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type activeCancelTrackingManaged struct {
	recvEntered  atomic.Bool
	cancelCalled atomic.Bool
	cancelCause  aipapi.CancelCause
	unblocked    chan struct{}
	closeOnce    sync.Once
}

func (m *activeCancelTrackingManaged) Recv(ctx context.Context) (aipapi.Event, error) {
	m.recvEntered.Store(true)
	select {
	case <-m.unblocked:
		return aipapi.Event{}, context.Canceled
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (m *activeCancelTrackingManaged) Close() error { return nil }

func (m *activeCancelTrackingManaged) Cancel(_ context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	m.cancelCause = cause
	m.cancelCalled.Store(true)
	m.closeOnce.Do(func() { close(m.unblocked) })
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

type channelExecuteStream struct {
	ctx       context.Context
	inFrames  chan backendplugin.ClientFrame
	done      chan struct{}
	mu        sync.Mutex
	sent      []backendplugin.ServerFrame
	closeOnce sync.Once
}

func newChannelExecuteStream(ctx context.Context) *channelExecuteStream {
	return &channelExecuteStream{
		ctx:      ctx,
		inFrames: make(chan backendplugin.ClientFrame, 8),
		done:     make(chan struct{}),
	}
}

func (c *channelExecuteStream) Context() context.Context { return c.ctx }

// Close signals termination without closing the frame channel: closing
// inFrames while the test goroutine sends and the control reader receives is
// a close-vs-send race (and risks "send on closed channel"). Receivers observe
// Close via done instead, mirroring the production bridgeExecuteStream
// pattern of a separate close channel that is never sent on.
func (c *channelExecuteStream) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
	})
	return nil
}

func (c *channelExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	select {
	case fr, ok := <-c.inFrames:
		if !ok {
			return backendplugin.ClientFrame{}, context.Canceled
		}
		return fr, nil
	case <-c.done:
		return backendplugin.ClientFrame{}, context.Canceled
	case <-c.ctx.Done():
		return backendplugin.ClientFrame{}, c.ctx.Err()
	}
}

func (c *channelExecuteStream) Send(frame backendplugin.ServerFrame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, frame)
	return nil
}

type negotiatedChannelWrapper struct {
	*channelExecuteStream
	neg backendplugin.Negotiation
}

func (n *negotiatedChannelWrapper) Negotiation() backendplugin.Negotiation { return n.neg }

func newNegotiatedChannelExecuteStream(ctx context.Context) *negotiatedChannelWrapper {
	return &negotiatedChannelWrapper{
		channelExecuteStream: newChannelExecuteStream(ctx),
		neg: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}
}

func TestRED_ForwardExecute_PostStartCancelFrameNotConsumed(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := newNegotiatedChannelExecuteStream(ctx)
	stream.inFrames <- validStartFrame(t)

	ms := &activeCancelTrackingManaged{
		unblocked: make(chan struct{}),
	}

	execDone := make(chan error, 1)
	go func() {
		execDone <- backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	// Wait until Recv is entered (stream is active)
	waitUntil(t, time.Second, func() bool { return ms.recvEntered.Load() })

	// Send in-band CANCEL frame on active Execute stream
	stream.inFrames <- backendplugin.ClientFrame{
		Kind:         backendplugin.ClientFrameCancel,
		CancelReason: backendplugin.CancelReasonHost,
	}

	// Wait to see if ForwardExecute consumes the in-band CANCEL frame and cancels ms
	select {
	case <-ms.unblocked:
		// unblocked by Cancel
	case <-time.After(time.Second):
		t.Fatal("ForwardExecute failed to consume post-START in-band ClientFrameCancel while Execute was active")
	}

	if !ms.cancelCalled.Load() {
		t.Fatal("upstream ManagedEventStream.Cancel was not called upon receiving in-band cancel frame")
	}

	// Join the worker so no ForwardExecute goroutine outlives the test; an
	// unjoined worker racing test teardown is what the race detector flagged.
	select {
	case err := <-execDone:
		if err != nil {
			t.Fatalf("ForwardExecute returned unexpected error after in-band cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ForwardExecute did not return after in-band cancel")
	}
}
