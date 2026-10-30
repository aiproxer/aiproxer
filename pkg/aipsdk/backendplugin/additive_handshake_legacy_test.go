package backendplugin_test

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// legacyChannelStream intentionally does NOT implement OptionalNegotiatedStream.
// It is channel-driven and deterministic. It records post-START Recv ownership
// via recvCount/secondRecvCh so tests can deterministically prove legacy does
// not perform a second Recv while negotiated does.
type legacyChannelStream struct {
	ctx          context.Context
	inFrames     chan backendplugin.ClientFrame
	mu           sync.Mutex
	sent         []backendplugin.ServerFrame
	closeOnce    sync.Once
	recvCount    atomic.Int32
	secondRecvCh chan struct{}
	secondOnce   sync.Once
}

func newLegacyChannelStream(ctx context.Context) *legacyChannelStream {
	return &legacyChannelStream{
		ctx:          ctx,
		inFrames:     make(chan backendplugin.ClientFrame, 8),
		secondRecvCh: make(chan struct{}),
	}
}

func (c *legacyChannelStream) Context() context.Context { return c.ctx }
func (c *legacyChannelStream) Close() error {
	c.closeOnce.Do(func() { close(c.inFrames) })
	return nil
}

func (c *legacyChannelStream) Recv() (backendplugin.ClientFrame, error) {
	if n := c.recvCount.Add(1); n == 2 {
		c.secondOnce.Do(func() { close(c.secondRecvCh) })
	}
	select {
	case fr, ok := <-c.inFrames:
		if !ok {
			return backendplugin.ClientFrame{}, context.Canceled
		}
		return fr, nil
	case <-c.ctx.Done():
		return backendplugin.ClientFrame{}, c.ctx.Err()
	}
}

func (c *legacyChannelStream) Send(frame backendplugin.ServerFrame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, frame)
	return nil
}

func (c *legacyChannelStream) sentFrames() []backendplugin.ServerFrame {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]backendplugin.ServerFrame, len(c.sent))
	copy(out, c.sent)
	return out
}
func (c *legacyChannelStream) secondRecvSignal() <-chan struct{} { return c.secondRecvCh }

// negotiatedChannelStream implements OptionalNegotiatedStream with minor 8 + handshake feature.
type negotiatedChannelStream struct {
	*legacyChannelStream
	neg backendplugin.Negotiation
}

func newNegotiatedChannelStream(ctx context.Context, neg backendplugin.Negotiation) *negotiatedChannelStream {
	return &negotiatedChannelStream{
		legacyChannelStream: newLegacyChannelStream(ctx),
		neg:                 neg,
	}
}

func (n *negotiatedChannelStream) Negotiation() backendplugin.Negotiation { return n.neg }
func (n *negotiatedChannelStream) Context() context.Context               { return n.legacyChannelStream.Context() }

func (n *negotiatedChannelStream) Recv() (backendplugin.ClientFrame, error) {
	return n.legacyChannelStream.Recv()
}

func (n *negotiatedChannelStream) Send(frame backendplugin.ServerFrame) error {
	return n.legacyChannelStream.Send(frame)
}
func (n *negotiatedChannelStream) Close() error { return n.legacyChannelStream.Close() }

type handshakeTestManaged struct {
	events       []aipapi.Event
	idx          int
	mu           sync.Mutex
	recvEntered  atomic.Bool
	cancelCalled atomic.Bool
	cancelCh     chan struct{}
	cancelOnce   sync.Once
	finishCh     chan struct{}
	closeOnce    sync.Once
	cancelCause  aipapi.CancelCause
}

func newHandshakeTestManaged(events []aipapi.Event) *handshakeTestManaged {
	return &handshakeTestManaged{
		events:   events,
		finishCh: make(chan struct{}),
		cancelCh: make(chan struct{}),
	}
}

func (m *handshakeTestManaged) Recv(ctx context.Context) (aipapi.Event, error) {
	m.mu.Lock()
	if m.idx < len(m.events) {
		ev := m.events[m.idx]
		m.idx++
		m.mu.Unlock()
		m.recvEntered.Store(true)
		return ev, nil
	}
	m.mu.Unlock()
	m.recvEntered.Store(true)
	select {
	case <-m.finishCh:
		if m.cancelCalled.Load() {
			return aipapi.Event{}, context.Canceled
		}
		return aipapi.Event{}, io.EOF
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (m *handshakeTestManaged) Close() error { return nil }

func (m *handshakeTestManaged) Cancel(_ context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	m.cancelCause = cause
	m.cancelCalled.Store(true)
	m.cancelOnce.Do(func() { close(m.cancelCh) })
	m.closeOnce.Do(func() { close(m.finishCh) })
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

func (m *handshakeTestManaged) manualFinishEOF() {
	m.closeOnce.Do(func() { close(m.finishCh) })
}

func TestAdditiveHandshake_MissingOptionalNegotiatedStream_UsesLegacyBehavior(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	stream := newLegacyChannelStream(ctx)
	stream.inFrames <- validStartFrame(t)

	ms := newHandshakeTestManaged([]aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
	})

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	// Wait for first event to be forwarded (Accepted + Event).
	waitUntil(t, 2*time.Second, func() bool {
		return len(stream.sentFrames()) >= 2
	})

	// Send in-band CANCEL after START - legacy path must NOT consume it.
	stream.inFrames <- backendplugin.ClientFrame{
		Kind:         backendplugin.ClientFrameCancel,
		CancelReason: backendplugin.CancelReasonClient,
	}

	// Deterministically prove legacy does not own post-START Recv and does not cancel.
	// Bounded timeout only as failure guard, no polling.
	select {
	case <-stream.secondRecvSignal():
		t.Fatalf("legacy stream must not perform second Recv post-START (handshake not negotiated)")
	case <-ms.cancelCh:
		t.Fatalf("legacy stream must not trigger upstream Cancel")
	case <-time.After(400 * time.Millisecond):
	}
	if ms.cancelCalled.Load() {
		t.Fatal("legacy stream must not trigger upstream Cancel")
	}
	if got := stream.recvCount.Load(); got != 1 {
		t.Fatalf("legacy recvCount=%d want 1 (only START)", got)
	}

	// Verify no CancelOutcome was emitted (handshake semantics must not be claimed).
	for _, f := range stream.sentFrames() {
		if f.Kind == backendplugin.ServerFrameCancelOutcome {
			t.Fatalf("legacy stream must not emit CancelOutcome, got %+v", f)
		}
	}

	// Now allow legacy to complete normally via EOF.
	ms.manualFinishEOF()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ForwardExecute returned unexpected error for legacy completion: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForwardExecute did not complete after manual EOF for legacy path")
	}

	sent := stream.sentFrames()
	var sawTerminal bool
	var terminalStatus backendplugin.TerminalStatus
	for _, f := range sent {
		if f.Kind == backendplugin.ServerFrameCancelOutcome {
			t.Fatalf("legacy stream must not emit CancelOutcome at completion, got %+v", f)
		}
		if f.Kind == backendplugin.ServerFrameTerminal {
			sawTerminal = true
			if f.Terminal != nil {
				terminalStatus = f.Terminal.Status
			}
		}
	}
	if !sawTerminal {
		t.Fatal("expected terminal after legacy completion")
	}
	if terminalStatus != backendplugin.TerminalSuccess {
		t.Fatalf("legacy terminal status=%q, want %q (legacy must complete normally, not cancelled)", terminalStatus, backendplugin.TerminalSuccess)
	}
}

func TestAdditiveHandshake_NegotiatedMinor8_ConsumesCancelAndEmitsOutcome(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	neg := backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
		EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
	}
	stream := newNegotiatedChannelStream(ctx, neg)
	stream.inFrames <- validStartFrame(t)

	ms := newHandshakeTestManaged([]aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
	})

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool {
		return len(stream.sentFrames()) >= 2
	})

	stream.inFrames <- backendplugin.ClientFrame{
		Kind:         backendplugin.ClientFrameCancel,
		CancelReason: backendplugin.CancelReasonClient,
	}

	// Negotiated path must own post-START Recv and consume cancel.
	select {
	case <-stream.secondRecvSignal():
	case <-time.After(2 * time.Second):
		t.Fatal("negotiated stream must perform second Recv post-START to consume in-band cancel")
	}
	select {
	case <-ms.cancelCh:
	case <-time.After(2 * time.Second):
		t.Fatal("negotiated stream must call upstream Cancel on in-band cancel")
	}
	if !ms.cancelCalled.Load() {
		t.Fatal("negotiated stream must call upstream Cancel on in-band cancel")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ForwardExecute returned unexpected error for negotiated cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ForwardExecute timed out after negotiated in-band cancel")
	}

	sent := stream.sentFrames()
	var sawOutcome bool
	var sawCancelled bool
	for _, f := range sent {
		if f.Kind == backendplugin.ServerFrameCancelOutcome {
			sawOutcome = true
			if f.CancelOutcome == nil || !f.CancelOutcome.Acknowledged {
				t.Fatalf("CancelOutcome not acknowledged: %+v", f.CancelOutcome)
			}
			if f.CancelOutcome.Mode != backendplugin.CancelModeProvider {
				t.Fatalf("CancelOutcome mode=%q, want provider", f.CancelOutcome.Mode)
			}
		}
		if f.Kind == backendplugin.ServerFrameTerminal && f.Terminal != nil && f.Terminal.Status == backendplugin.TerminalCancelled {
			sawCancelled = true
		}
	}
	if !sawOutcome {
		t.Fatal("negotiated stream must emit CancelOutcome")
	}
	if !sawCancelled {
		t.Fatal("negotiated stream must emit TerminalCancelled after cancel")
	}
}
