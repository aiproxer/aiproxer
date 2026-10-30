package backendplugin

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"go.uber.org/goleak"
)

// cancelUntilClosedManaged models an adapter whose graceful Cancel path does
// not observe its context. The adapter's minimum termination contract is that
// Close force-unblocks that path so its Cancel call can still be joined.
type cancelUntilClosedManaged struct {
	recvEntered atomic.Bool
	cancelCalls atomic.Int32
	closeCalls  atomic.Int32
	closeOnce   sync.Once
	closed      chan struct{}
}

func (m *cancelUntilClosedManaged) Recv(ctx context.Context) (aipapi.Event, error) {
	m.recvEntered.Store(true)
	select {
	case <-m.closed:
		return aipapi.Event{}, context.Canceled
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (m *cancelUntilClosedManaged) Close() error {
	m.closeCalls.Add(1)
	m.closeOnce.Do(func() { close(m.closed) })
	return nil
}

func (m *cancelUntilClosedManaged) Cancel(_ context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	_ = cause
	m.cancelCalls.Add(1)
	<-m.closed
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

//nolint:paralleltest // mutates package-level fallbackCancelGrace
func TestRED_ForwardExecute_InBandCancel_ForceCloseJoinsGracefulCancel(t *testing.T) {
	previous := fallbackCancelGrace
	fallbackCancelGrace = 100 * time.Millisecond
	t.Cleanup(func() { fallbackCancelGrace = previous })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := newCoordinatorTestStream(ctx)
	stream.inFrames <- coordinatorStartFrame()
	managed := &cancelUntilClosedManaged{closed: make(chan struct{})}

	done := make(chan error, 1)
	go func() {
		done <- ForwardExecute(stream, func(context.Context, Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return managed, nil
		})
	}()
	var doneReceived atomic.Bool
	t.Cleanup(func() {
		cancel()
		if !doneReceived.Load() {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("ForwardExecute did not exit during cleanup")
			}
		}
	})
	waitCoordinatorUntil(t, 2*time.Second, func() bool { return managed.recvEntered.Load() })
	stream.inFrames <- ClientFrame{Kind: ClientFrameCancel, CancelReason: CancelReasonClient}

	select {
	case err := <-done:
		doneReceived.Store(true)
		if err != nil {
			t.Fatalf("ForwardExecute returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForwardExecute waited indefinitely for graceful Cancel")
	}
	if got := managed.cancelCalls.Load(); got != 1 {
		t.Fatalf("Cancel calls = %d, want 1", got)
	}
	if got := managed.closeCalls.Load(); got != 1 {
		t.Fatalf("Close calls = %d, want 1 force close", got)
	}
}

//nolint:paralleltest // mutates package-level fallbackCancelGrace
func TestEffectiveCancellationTiming_CapsPeerDeadlineByFallbackGrace(t *testing.T) {
	previous := fallbackCancelGrace
	fallbackCancelGrace = 50 * time.Millisecond
	t.Cleanup(func() { fallbackCancelGrace = previous })

	timing := effectiveCancellationTiming(context.Background(), controlCancelReq{
		deadlineMS: time.Now().Add(time.Hour).UnixMilli(),
		outcome:    true,
	})
	deadline := timing.deadline
	if remaining := time.Until(deadline); remaining > 200*time.Millisecond {
		t.Fatalf("peer deadline was not capped by fallback grace: remaining=%s", remaining)
	}
}

type immediateCancelStuckRecvManaged struct {
	recvEntered    atomic.Bool
	cancelReturned chan struct{}
	closed         chan struct{}
	closeOnce      sync.Once
	closeCalls     atomic.Int32
}

func (m *immediateCancelStuckRecvManaged) Recv(ctx context.Context) (aipapi.Event, error) {
	m.recvEntered.Store(true)
	select {
	case <-m.closed:
		return aipapi.Event{}, context.Canceled
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (m *immediateCancelStuckRecvManaged) Close() error {
	m.closeCalls.Add(1)
	m.closeOnce.Do(func() { close(m.closed) })
	return nil
}

func (m *immediateCancelStuckRecvManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	close(m.cancelReturned)
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

//nolint:paralleltest // mutates package-level fallbackCancelGrace
func TestRED_ForwardExecute_ShortPeerDeadlineForcesCloseNearEffectiveDeadline(t *testing.T) {
	previous := fallbackCancelGrace
	fallbackCancelGrace = 500 * time.Millisecond
	t.Cleanup(func() { fallbackCancelGrace = previous })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := newCoordinatorTestStream(ctx)
	stream.inFrames <- coordinatorStartFrame()
	managed := &immediateCancelStuckRecvManaged{
		cancelReturned: make(chan struct{}),
		closed:         make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() {
		done <- ForwardExecute(stream, func(context.Context, Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return managed, nil
		})
	}()
	var doneReceived atomic.Bool
	t.Cleanup(func() {
		cancel()
		if !doneReceived.Load() {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("ForwardExecute did not exit during cleanup")
			}
		}
	})
	waitCoordinatorUntil(t, 2*time.Second, func() bool { return managed.recvEntered.Load() })

	deadline := time.Now().Add(75 * time.Millisecond)
	stream.inFrames <- ClientFrame{
		Kind:                 ClientFrameCancel,
		CancelReason:         CancelReasonClient,
		CancelDeadlineUnixMS: deadline.UnixMilli(),
	}
	select {
	case <-managed.cancelReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not return")
	}

	closeWaitStart := time.Now()
	select {
	case <-managed.closed:
		if elapsed := time.Since(closeWaitStart); elapsed > 250*time.Millisecond {
			t.Fatalf("force Close took %s; want near peer deadline rather than fallback grace", elapsed)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("stuck Recv was not force-closed near the effective peer deadline")
	}
	select {
	case <-done:
		doneReceived.Store(true)
	case <-time.After(2 * time.Second):
		t.Fatal("ForwardExecute did not terminate after force Close")
	}
}

type panicCancelManaged struct {
	recvEntered atomic.Bool
	closed      chan struct{}
	closeOnce   sync.Once
	closeCalls  atomic.Int32
}

func (m *panicCancelManaged) Recv(ctx context.Context) (aipapi.Event, error) {
	m.recvEntered.Store(true)
	select {
	case <-m.closed:
		return aipapi.Event{}, context.Canceled
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (m *panicCancelManaged) Close() error {
	m.closeCalls.Add(1)
	m.closeOnce.Do(func() { close(m.closed) })
	return nil
}

func (m *panicCancelManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	panic("adapter-private-panic-detail-4f37")
}

//nolint:paralleltest // mutates package-level fallbackCancelGrace
func TestForwardExecute_PanickingCancelReturnsBoundedOutcome(t *testing.T) {
	previous := fallbackCancelGrace
	fallbackCancelGrace = 50 * time.Millisecond
	t.Cleanup(func() { fallbackCancelGrace = previous })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := newCoordinatorTestStream(ctx)
	stream.inFrames <- coordinatorStartFrame()
	managed := &panicCancelManaged{closed: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- ForwardExecute(stream, func(context.Context, Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return managed, nil
		})
	}()
	var doneReceived atomic.Bool
	t.Cleanup(func() {
		cancel()
		if !doneReceived.Load() {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("ForwardExecute did not exit during cleanup")
			}
		}
	})
	waitCoordinatorUntil(t, 2*time.Second, func() bool { return managed.recvEntered.Load() })
	stream.inFrames <- ClientFrame{Kind: ClientFrameCancel, CancelReason: CancelReasonClient}

	select {
	case err := <-done:
		doneReceived.Store(true)
		if err != nil {
			t.Fatalf("ForwardExecute returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForwardExecute hung after panicking Cancel")
	}
	stream.mu.Lock()
	frames := append([]ServerFrame(nil), stream.sent...)
	stream.mu.Unlock()
	for _, frame := range frames {
		if frame.Kind == ServerFrameCancelOutcome {
			if frame.CancelOutcome == nil || frame.CancelOutcome.Acknowledged {
				t.Fatalf("panic outcome = %+v, want bounded negative acknowledgement", frame.CancelOutcome)
			}
			if frame.CancelOutcome.Detail != genericCancelOutcomeDetail {
				t.Fatalf("panic outcome detail = %q, want %q", frame.CancelOutcome.Detail, genericCancelOutcomeDetail)
			}
			return
		}
	}
	t.Fatalf("missing CancelOutcome after panic: %+v", frames)
}

type eofAfterEventManaged struct {
	recvCalls     atomic.Int32
	cancelCalls   atomic.Int32
	cancelStarted chan struct{}
	cancelOnce    sync.Once
	releaseEOF    chan struct{}
	releaseOnce   sync.Once
}

func (m *eofAfterEventManaged) Recv(context.Context) (aipapi.Event, error) {
	if m.recvCalls.Add(1) == 1 {
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "event"}, nil
	}
	<-m.releaseEOF
	return aipapi.Event{}, io.EOF
}

func (m *eofAfterEventManaged) Close() error { return nil }

func (m *eofAfterEventManaged) release() {
	m.releaseOnce.Do(func() { close(m.releaseEOF) })
}

func (m *eofAfterEventManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	m.cancelCalls.Add(1)
	m.cancelOnce.Do(func() { close(m.cancelStarted) })
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

// TestRED_ForwardExecute_CancelQueuedWithUpstreamEOF keeps both observations
// ready after the control reader has received CANCEL. The terminal must not
// win the select and suppress the exactly-once CancelOutcome.
//
//nolint:paralleltest // serial by design: goleak must not observe goroutines from sibling parallel tests
func TestRED_ForwardExecute_CancelQueuedWithUpstreamEOF(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := newCoordinatorTestStream(ctx)
	stream.inFrames <- coordinatorStartFrame()
	managed := &eofAfterEventManaged{
		cancelStarted: make(chan struct{}),
		releaseEOF:    make(chan struct{}),
	}

	done := make(chan error, 1)
	go func() {
		done <- ForwardExecute(stream, func(context.Context, Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return managed, nil
		})
	}()
	var doneReceived atomic.Bool
	t.Cleanup(func() {
		cancel()
		managed.release()
		if !doneReceived.Load() {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("ForwardExecute did not exit during cleanup")
			}
		}
	})
	waitCoordinatorUntil(t, 2*time.Second, func() bool {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		return len(stream.sent) >= 2
	})
	stream.inFrames <- ClientFrame{Kind: ClientFrameCancel, CancelReason: CancelReasonClient}

	// Wait until managed Cancel is invoked by the coordinator before releasing upstream EOF.
	// This ensures cancellation has been initiated on the coordinator boundary while
	// the upstream EOF observation arrives, which is the race this test protects.
	select {
	case <-managed.cancelStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("managed stream Cancel was not invoked")
	}
	managed.release()

	select {
	case err := <-done:
		doneReceived.Store(true)
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("ForwardExecute returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForwardExecute did not finish after queued cancel and upstream EOF")
	}

	stream.mu.Lock()
	frames := append([]ServerFrame(nil), stream.sent...)
	stream.mu.Unlock()
	outcome, terminal := -1, -1
	for i, frame := range frames {
		if frame.Kind == ServerFrameCancelOutcome && outcome == -1 {
			outcome = i
		}
		if frame.Kind == ServerFrameTerminal && terminal == -1 {
			terminal = i
		}
	}
	if managed.cancelCalls.Load() != 1 {
		t.Fatalf("Cancel calls = %d, want exactly 1; frames=%+v", managed.cancelCalls.Load(), frames)
	}
	if outcome == -1 || terminal == -1 || outcome >= terminal {
		t.Fatalf("CancelOutcome index=%d must precede terminal index=%d; frames=%+v", outcome, terminal, frames)
	}
}

type cancelThenEvidenceManaged struct {
	recvCalls       atomic.Int32
	cancelReturned  chan struct{}
	releaseTerminal chan struct{}
	releaseOnce     sync.Once
	evidenceReady   atomic.Bool
	evidence        AccountingEvidence
}

func (m *cancelThenEvidenceManaged) Recv(context.Context) (aipapi.Event, error) {
	if m.recvCalls.Add(1) == 1 {
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "partial"}, nil
	}
	<-m.releaseTerminal
	return aipapi.Event{}, io.EOF
}

func (m *cancelThenEvidenceManaged) Close() error { return nil }

func (m *cancelThenEvidenceManaged) release() {
	m.releaseOnce.Do(func() { close(m.releaseTerminal) })
}

func (m *cancelThenEvidenceManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	close(m.cancelReturned)
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

func (m *cancelThenEvidenceManaged) DrainAccountingEvidence() []AccountingEvidence {
	if !m.evidenceReady.Swap(false) {
		return nil
	}
	return []AccountingEvidence{m.evidence}
}

//nolint:paralleltest // interacts with package-level cancellation timing
func TestRED_ForwardExecute_CancelOutcomeWaitsForFinalEvidenceBeforeTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := newCoordinatorTestStream(ctx)
	stream.inFrames <- coordinatorStartFrame()
	input := int64(99)
	managed := &cancelThenEvidenceManaged{
		cancelReturned:  make(chan struct{}),
		releaseTerminal: make(chan struct{}),
		evidence: AccountingEvidence{
			InputTokens: &input,
			Presence:    aipapi.UsagePresence{InputTokens: true},
			Source:      AccountingSourceProviderReported,
			Authority:   AccountingAuthorityAuthoritative,
			Plane:       AccountingPlaneProviderBillable,
			DedupeKey:   "final-after-cancel",
		},
	}
	t.Cleanup(managed.release)

	done := make(chan error, 1)
	go func() {
		done <- ForwardExecute(stream, func(context.Context, Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return managed, nil
		})
	}()
	var doneReceived atomic.Bool
	t.Cleanup(func() {
		cancel()
		if !doneReceived.Load() {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("ForwardExecute did not exit during cleanup")
			}
		}
	})
	waitCoordinatorUntil(t, 2*time.Second, func() bool {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		return len(stream.sent) >= 2
	})
	stream.inFrames <- ClientFrame{Kind: ClientFrameCancel, CancelReason: CancelReasonClient}

	select {
	case <-managed.cancelReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not return")
	}
	waitCoordinatorUntil(t, 2*time.Second, func() bool {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		for _, frame := range stream.sent {
			if frame.Kind == ServerFrameCancelOutcome {
				return true
			}
		}
		return false
	})
	stream.mu.Lock()
	preTerminal := append([]ServerFrame(nil), stream.sent...)
	stream.mu.Unlock()
	var outcomeSeen, terminalSeen bool
	for _, frame := range preTerminal {
		outcomeSeen = outcomeSeen || frame.Kind == ServerFrameCancelOutcome
		terminalSeen = terminalSeen || frame.Kind == ServerFrameTerminal
	}
	if !outcomeSeen {
		t.Fatalf("CancelOutcome was not sent promptly: %+v", preTerminal)
	}
	if terminalSeen {
		t.Fatalf("TerminalCancelled was emitted before upstream terminal observation: %+v", preTerminal)
	}

	managed.evidenceReady.Store(true)
	managed.release()
	select {
	case err := <-done:
		doneReceived.Store(true)
		if err != nil {
			t.Fatalf("ForwardExecute returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForwardExecute did not finish after final evidence and EOF")
	}

	stream.mu.Lock()
	frames := append([]ServerFrame(nil), stream.sent...)
	stream.mu.Unlock()
	evidenceIndex, terminalIndex := -1, -1
	for i, frame := range frames {
		if frame.Kind == ServerFrameAccountingEvidence && frame.Accounting != nil && frame.Accounting.DedupeKey == "final-after-cancel" {
			evidenceIndex = i
		}
		if frame.Kind == ServerFrameTerminal {
			terminalIndex = i
		}
	}
	if evidenceIndex == -1 || terminalIndex == -1 || evidenceIndex >= terminalIndex {
		t.Fatalf("final accounting evidence must precede terminal: evidence=%d terminal=%d frames=%+v", evidenceIndex, terminalIndex, frames)
	}
}

type coordinatorTestStream struct {
	ctx      context.Context
	inFrames chan ClientFrame
	mu       sync.Mutex
	sent     []ServerFrame
}

func newCoordinatorTestStream(ctx context.Context) *coordinatorTestStream {
	return &coordinatorTestStream{ctx: ctx, inFrames: make(chan ClientFrame, 8)}
}

func (s *coordinatorTestStream) Context() context.Context { return s.ctx }

func (s *coordinatorTestStream) Recv() (ClientFrame, error) {
	select {
	case frame := <-s.inFrames:
		return frame, nil
	case <-s.ctx.Done():
		return ClientFrame{}, s.ctx.Err()
	}
}

func (s *coordinatorTestStream) Send(frame ServerFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, frame)
	return nil
}

func (s *coordinatorTestStream) Negotiation() Negotiation {
	return Negotiation{
		Compatible:      true,
		NegotiatedMinor: ProtocolMinorCancellationHandshake,
		EnabledFeatures: []string{FeatureCancellationHandshake},
	}
}

func coordinatorStartFrame() ClientFrame {
	text := "test"
	return ClientFrame{
		Kind:       ClientFrameStart,
		InstanceID: "instance",
		Invocation: &Invocation{
			RequestID:        "request",
			AttemptID:        "attempt",
			ALegID:           "aleg",
			BLegID:           "bleg",
			CanonicalModelID: "model",
			Messages:         []Message{{Role: RoleUser, Parts: []Part{{Kind: PartKindText, Text: &text}}}},
			Options:          GenerationOptions{ResponseSchemaJSON: RawJSONAbsentValue()},
		},
	}
}

func waitCoordinatorUntil(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}
