package backendplugin_test

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

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"go.uber.org/goleak"
)

func TestForwardExecute_ForwardsEventsVerbatim(t *testing.T) {
	t.Parallel()

	events := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "hel"},
		{Kind: aipapi.EventTextDelta, Delta: "lo"},
		{Kind: aipapi.EventResponseFinished},
	}
	ms := &scriptedManaged{events: events}
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))

	err := backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		if ctx == nil {
			t.Fatal("open ctx must not be nil")
		}
		if inv.RequestID != "r1" {
			t.Fatalf("inv.RequestID=%q", inv.RequestID)
		}
		if call.ID != "r1" {
			t.Fatalf("call.ID=%q", call.ID)
		}
		return ms, nil
	})
	if err != nil {
		t.Fatalf("ForwardExecute: %v", err)
	}

	if len(stream.sent) < 1 || stream.sent[0].Kind != backendplugin.ServerFrameAccepted {
		t.Fatalf("first frame=%v, want accepted", stream.sent)
	}
	gotEvents := make([]aipapi.Event, 0, len(events))
	var sawTerminal bool
	for i, f := range stream.sent[1:] {
		wantSeq := uint64(i + 1)
		if f.Sequence != wantSeq {
			t.Fatalf("frame[%d].Sequence=%d, want %d", i, f.Sequence, wantSeq)
		}
		switch f.Kind {
		case backendplugin.ServerFrameEvent:
			if f.Event == nil {
				t.Fatalf("frame[%d]: nil event", i)
			}
			gotEvents = append(gotEvents, aipapi.Event{
				Kind:  f.Event.Kind,
				Delta: derefStr(f.Event.Delta),
			})
		case backendplugin.ServerFrameTerminal:
			sawTerminal = true
			if f.Terminal == nil || f.Terminal.Status != backendplugin.TerminalSuccess {
				t.Fatalf("terminal=%+v, want success", f.Terminal)
			}
		default:
			t.Fatalf("unexpected frame kind %q", f.Kind)
		}
	}
	if !sawTerminal {
		t.Fatal("expected terminal success after upstream EOF")
	}
	if len(gotEvents) != len(events) {
		t.Fatalf("got %d events, want %d (%v)", len(gotEvents), len(events), gotEvents)
	}
	for i := range events {
		if gotEvents[i].Kind != events[i].Kind || gotEvents[i].Delta != events[i].Delta {
			t.Fatalf("event[%d]=%+v, want %+v", i, gotEvents[i], events[i])
		}
	}
}

func TestForwardExecute_ForwardsOpeningAccountingEvidenceBeforeCanonicalEvents(t *testing.T) {
	t.Parallel()

	input := int64(41)
	usage := backendplugin.AccountingEvidence{
		InputTokens: &input,
		Presence:    aipapi.UsagePresence{InputTokens: true},
		Source:      backendplugin.AccountingSourceProviderReported,
		Authority:   backendplugin.AccountingAuthorityAuthoritative,
		Plane:       backendplugin.AccountingPlaneProviderBillable,
		DedupeKey:   "compaction:turn-1",
	}
	ms := &evidenceManaged{evidence: []backendplugin.AccountingEvidence{usage}, events: []aipapi.Event{{Kind: aipapi.EventResponseFinished}}}
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	stream.neg = backendplugin.Negotiation{EnabledFeatures: []string{backendplugin.FeatureAccountingEvidence}}

	if err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return ms, nil
	}); err != nil {
		t.Fatalf("ForwardExecute: %v", err)
	}
	if len(stream.sent) < 4 {
		t.Fatalf("frames = %d, want accepted, evidence, event, terminal", len(stream.sent))
	}
	if stream.sent[1].Kind != backendplugin.ServerFrameAccountingEvidence {
		t.Fatalf("first post-accepted frame = %q, want accounting evidence", stream.sent[1].Kind)
	}
	if stream.sent[1].Accounting == nil || stream.sent[1].Accounting.DedupeKey != usage.DedupeKey {
		t.Fatalf("evidence frame = %#v", stream.sent[1].Accounting)
	}
	if stream.sent[2].Kind != backendplugin.ServerFrameEvent {
		t.Fatalf("canonical frame after evidence = %q", stream.sent[2].Kind)
	}
	if stream.sent[3].Kind != backendplugin.ServerFrameTerminal || stream.sent[3].Terminal == nil || stream.sent[3].Terminal.Status != backendplugin.TerminalSuccess {
		t.Fatalf("terminal frame = %#v, want successful terminal", stream.sent[3])
	}
	for i, frame := range stream.sent[1:] {
		if frame.Sequence != uint64(i+1) {
			t.Fatalf("frame %d sequence = %d, want %d", i+1, frame.Sequence, i+1)
		}
	}
}

func TestForwardExecute_CancelsUpstreamWhenStreamContextDone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	ms := &blockingManaged{unblocked: make(chan struct{})}
	stream := newFakeExecuteStream(ctx, validStartFrame(t))

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool { return ms.recvEntered.Load() })
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ForwardExecute to return on stream cancel")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ForwardExecute did not return after stream context cancel")
	}

	waitUntil(t, 2*time.Second, func() bool { return ms.cancelCalled.Load() })
	if got := ms.cancelCause.Kind; got != aipapi.CancelContextDone {
		t.Fatalf("Cancel cause kind=%q, want %q", got, aipapi.CancelContextDone)
	}
	if got := ms.cancelCause.Detail; got != "plugin_cancel" {
		t.Fatalf("Cancel detail=%q, want plugin_cancel", got)
	}
}

func TestForwardExecute_UpstreamErrorReturnsWithoutTerminal(t *testing.T) {
	// Name predates the fix (it pinned no terminal); the body now pins the
	// fixed contract: one failure terminal, no healthy-generation invalidation.
	t.Parallel()

	// The legacy receive path reaches the same owned classifier as the active
	// path, but only the generic case was observed directly here. Mirror the
	// active-path table so capability rejection is proven on this seam too
	// rather than inferred from the shared classifier.
	capabilityWrap := fmt.Errorf("%w: test model is not in instance inventory", aipapi.ErrCapabilityReject)
	capabilityTyped := &aipapi.RejectError{Reason: "test model is not in instance inventory"}
	tests := []struct {
		name    string
		recvErr error
		want    backendplugin.ErrorCode
		wantMsg string
	}{
		{name: "generic", recvErr: errors.New("upstream boom sk-or-v1-SECRET-1234567890"), want: backendplugin.ErrorCodeInternal, wantMsg: "upstream execution failed"},
		{name: "capability/wrap", recvErr: capabilityWrap, want: backendplugin.ErrorCodeFailedPrecondition, wantMsg: "capability rejected"},
		{name: "capability/typed", recvErr: capabilityTyped, want: backendplugin.ErrorCodeFailedPrecondition, wantMsg: "capability rejected"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ms := &scriptedManaged{err: tc.recvErr}
			stream := newFakeExecuteStream(context.Background(), validStartFrame(t))

			err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
				return ms, nil
			})
			if !errors.Is(err, tc.recvErr) {
				t.Fatalf("err=%v, want %v", err, tc.recvErr)
			}
			if tc.want == backendplugin.ErrorCodeFailedPrecondition && !errors.Is(err, aipapi.ErrCapabilityReject) {
				t.Fatalf("capability err=%v must wrap aipapi.ErrCapabilityReject", err)
			}
			var terminals []backendplugin.ServerFrame
			for _, f := range stream.sent {
				if f.Kind == backendplugin.ServerFrameCancelOutcome {
					t.Fatalf("unexpected CancelOutcome on non-cancellation upstream error: %+v", f)
				}
				if f.Kind == backendplugin.ServerFrameTerminal {
					terminals = append(terminals, f)
				}
			}
			if len(terminals) != 1 {
				t.Fatalf("got %d terminal frames, want exactly 1; frames=%+v", len(terminals), stream.sent)
			}
			term := terminals[0].Terminal
			if term == nil || term.Status != backendplugin.TerminalFailure {
				t.Fatalf("terminal status = %+v, want TerminalFailure", term)
			}
			if term.Error == nil || term.Error.Code != tc.want {
				t.Fatalf("terminal error = %+v, want Code=%v", term.Error, tc.want)
			}
			if term.Error.Message != tc.wantMsg {
				t.Fatalf("terminal error message = %q, want %q", term.Error.Message, tc.wantMsg)
			}
			if term.Error.Retryable || term.Error.OutputCommitted {
				t.Fatalf("pre-output terminal must be non-retryable without output commit: %+v", term.Error)
			}
			// Exact message equality above proves the terminal carries the
			// bounded constant rather than upstream text; the secret-bearing
			// generic input and the reject reason make that concrete.
			if strings.Contains(term.Error.Message, "SECRET") || strings.Contains(term.Error.Message, "sk-or-") {
				t.Fatalf("terminal error leaked secret: %q", term.Error.Message)
			}
		})
	}
}

func TestForwardExecute_OpenErrorPropagates(t *testing.T) {
	t.Parallel()

	capabilityErr := fmt.Errorf("%w: test model is not in instance inventory", aipapi.ErrCapabilityReject)
	tests := []struct {
		name    string
		openErr error
		want    backendplugin.ErrorCode
		msg     string
	}{
		{name: "capability", openErr: capabilityErr, want: backendplugin.ErrorCodeFailedPrecondition, msg: "capability rejected"},
		{name: "generic", openErr: errors.New("open failed sk-or-v1-SECRET-1234567890"), want: backendplugin.ErrorCodeInternal, msg: "upstream execution failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
			err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
				return nil, tc.openErr
			})
			if !errors.Is(err, tc.openErr) {
				t.Fatalf("err=%v, want %v", err, tc.openErr)
			}
			if tc.name == "capability" && !errors.Is(err, aipapi.ErrCapabilityReject) {
				t.Fatalf("capability err=%v must wrap aipapi.ErrCapabilityReject", err)
			}
			if len(stream.sent) < 2 {
				t.Fatalf("frames=%+v, want accepted plus exactly one failure terminal", stream.sent)
			}
			if stream.sent[0].Kind != backendplugin.ServerFrameAccepted {
				t.Fatalf("first frame=%v, want accepted", stream.sent[0].Kind)
			}
			var terminals []backendplugin.ServerFrame
			for _, f := range stream.sent {
				if f.Kind == backendplugin.ServerFrameCancelOutcome {
					t.Fatalf("unexpected CancelOutcome on pre-output open error: %+v", f)
				}
				if f.Kind == backendplugin.ServerFrameTerminal {
					terminals = append(terminals, f)
				}
			}
			if len(terminals) != 1 {
				t.Fatalf("got %d terminal frames, want exactly 1; frames=%+v", len(terminals), stream.sent)
			}
			term := terminals[0].Terminal
			if term == nil || term.Status != backendplugin.TerminalFailure {
				t.Fatalf("terminal status = %+v, want TerminalFailure", term)
			}
			if term.Error == nil || term.Error.Code != tc.want {
				t.Fatalf("terminal error = %+v, want Code=%v", term.Error, tc.want)
			}
			if term.Error.Retryable || term.Error.OutputCommitted {
				t.Fatalf("pre-output terminal must be non-retryable without output commit: %+v", term.Error)
			}
			if term.Error.Message != tc.msg {
				t.Fatalf("terminal error message = %q, want %q", term.Error.Message, tc.msg)
			}
			if strings.Contains(term.Error.Message, "SECRET") || strings.Contains(term.Error.Message, "sk-or-") {
				t.Fatalf("terminal error leaked secret: %q", term.Error.Message)
			}
			if len(term.Error.Message) > 256 {
				t.Fatalf("terminal error message exceeds 256-byte bound: %d", len(term.Error.Message))
			}
		})
	}
}

func TestForwardExecute_NilOpen(t *testing.T) {
	t.Parallel()
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	err := backendplugin.ForwardExecute(stream, nil)
	if err == nil {
		t.Fatal("expected error for nil open")
	}
}

func TestForwardExecute_WrappedEOFEmitsTerminalSuccess(t *testing.T) {
	t.Parallel()

	ms := &wrappedEOFManaged{}
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))

	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return ms, nil
	})
	if err != nil {
		t.Fatalf("ForwardExecute: %v", err)
	}
	var sawTerminal bool
	for _, f := range stream.sent {
		if f.Kind == backendplugin.ServerFrameTerminal {
			sawTerminal = true
			if f.Terminal == nil || f.Terminal.Status != backendplugin.TerminalSuccess {
				t.Fatalf("terminal=%+v, want success", f.Terminal)
			}
		}
	}
	if !sawTerminal {
		t.Fatal("wrapped io.EOF must emit terminal success")
	}
}

func TestForwardExecute_NilStreamFromOpen(t *testing.T) {
	t.Parallel()
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected error for nil stream from open")
	}
	if !strings.Contains(err.Error(), "nil stream") {
		t.Fatalf("err=%v, want nil stream error", err)
	}
}

func TestForwardExecute_CloseExactlyOnceOnCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	ms := &closeCountManaged{unblocked: make(chan struct{})}
	stream := newFakeExecuteStream(ctx, validStartFrame(t))

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool { return ms.recvEntered.Load() })
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected ForwardExecute to return on stream cancel")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ForwardExecute did not return after stream context cancel")
	}

	waitUntil(t, 2*time.Second, func() bool { return ms.closeCalls.Load() == 1 })
	if got := ms.closeCalls.Load(); got != 1 {
		t.Fatalf("Close calls=%d, want exactly 1", got)
	}
}

func validStartFrame(t *testing.T) backendplugin.ClientFrame {
	t.Helper()
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r1", AttemptID: "a1", ALegID: "aleg", BLegID: "bleg",
		CanonicalModelID: "m1",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	return backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameStart, InstanceID: "i1", Invocation: &inv,
	}
}

type fakeExecuteStream struct {
	ctx   context.Context
	inbox []backendplugin.ClientFrame
	mu    sync.Mutex
	sent  []backendplugin.ServerFrame
	neg   backendplugin.Negotiation
}

func newFakeExecuteStream(ctx context.Context, start backendplugin.ClientFrame) *fakeExecuteStream {
	return &fakeExecuteStream{ctx: ctx, inbox: []backendplugin.ClientFrame{start}}
}

func (f *fakeExecuteStream) Context() context.Context { return f.ctx }

func (f *fakeExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	if len(f.inbox) == 0 {
		return backendplugin.ClientFrame{}, io.EOF
	}
	fr := f.inbox[0]
	f.inbox = f.inbox[1:]
	return fr, nil
}

func (f *fakeExecuteStream) Send(frame backendplugin.ServerFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, frame)
	return nil
}

func (f *fakeExecuteStream) Negotiation() backendplugin.Negotiation { return f.neg }

type scriptedManaged struct {
	events []aipapi.Event
	err    error
	idx    int
	closed atomic.Bool
}

type evidenceManaged struct {
	evidence []backendplugin.AccountingEvidence
	events   []aipapi.Event
	idx      int
}

func (m *evidenceManaged) Recv(context.Context) (aipapi.Event, error) {
	if m.idx >= len(m.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := m.events[m.idx]
	m.idx++
	return ev, nil
}

func (m *evidenceManaged) DrainAccountingEvidence() []backendplugin.AccountingEvidence {
	evidence := append([]backendplugin.AccountingEvidence(nil), m.evidence...)
	m.evidence = nil
	return evidence
}

func (m *evidenceManaged) Close() error { return nil }

func (m *evidenceManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (m *scriptedManaged) Recv(context.Context) (aipapi.Event, error) {
	if m.err != nil && m.idx >= len(m.events) {
		return aipapi.Event{}, m.err
	}
	if m.idx >= len(m.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := m.events[m.idx]
	m.idx++
	return ev, nil
}

func (m *scriptedManaged) Close() error {
	m.closed.Store(true)
	return nil
}

func (m *scriptedManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// blockingManaged Recv ignores the passed context and only unblocks when Cancel is called,
// proving ForwardExecute observes stream cancellation and invokes Cancel (codex-grade).
type blockingManaged struct {
	recvEntered  atomic.Bool
	cancelCalled atomic.Bool
	cancelCause  aipapi.CancelCause
	unblocked    chan struct{}
	closeOnce    sync.Once
}

func (m *blockingManaged) Recv(context.Context) (aipapi.Event, error) {
	m.recvEntered.Store(true)
	<-m.unblocked
	return aipapi.Event{}, context.Canceled
}

func (m *blockingManaged) Close() error { return nil }

func (m *blockingManaged) Cancel(_ context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	m.cancelCause = cause
	m.cancelCalled.Store(true)
	m.closeOnce.Do(func() { close(m.unblocked) })
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

type wrappedEOFManaged struct{}

func (m *wrappedEOFManaged) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, fmt.Errorf("wrapped: %w", io.EOF)
}

func (m *wrappedEOFManaged) Close() error { return nil }

func (m *wrappedEOFManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

type closeCountManaged struct {
	recvEntered atomic.Bool
	closeCalls  atomic.Int32
	unblocked   chan struct{}
}

func (m *closeCountManaged) Recv(ctx context.Context) (aipapi.Event, error) {
	m.recvEntered.Store(true)
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-m.unblocked:
		return aipapi.Event{}, context.Canceled
	}
}

func (m *closeCountManaged) Close() error {
	m.closeCalls.Add(1)
	return nil
}

func (m *closeCountManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

type trackingManagedWithDeadline struct {
	recvEntered    atomic.Bool
	cancelCalled   atomic.Int32
	cancelDeadline time.Time
	cancelCause    aipapi.CancelCause
	unblocked      chan struct{}
	events         []aipapi.Event
	eventIdx       int
	mu             sync.Mutex
	cancelMode     aipapi.CancelMode
}

func (m *trackingManagedWithDeadline) Recv(ctx context.Context) (aipapi.Event, error) {
	m.recvEntered.Store(true)
	m.mu.Lock()
	if m.eventIdx < len(m.events) {
		ev := m.events[m.eventIdx]
		m.eventIdx++
		m.mu.Unlock()
		return ev, nil
	}
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-m.unblocked:
		return aipapi.Event{}, context.Canceled
	}
}

func (m *trackingManagedWithDeadline) Close() error { return nil }

func (m *trackingManagedWithDeadline) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	m.cancelCalled.Add(1)
	m.cancelCause = cause
	if d, ok := ctx.Deadline(); ok {
		m.cancelDeadline = d
	}
	close(m.unblocked)
	mode := m.cancelMode
	if mode == "" {
		mode = aipapi.CancelModeProvider
	}
	return aipapi.CancelResult{Mode: mode}
}

type negotiatedInBandStream struct {
	*channelExecuteStream
	neg backendplugin.Negotiation
}

func (n *negotiatedInBandStream) Negotiation() backendplugin.Negotiation { return n.neg }

func newNegotiatedInBandStream(ctx context.Context) *negotiatedInBandStream {
	return &negotiatedInBandStream{
		channelExecuteStream: newChannelExecuteStream(ctx),
		neg: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}
}

func TestForwardExecute_InBandCancel_SequencingAndMode(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := newNegotiatedInBandStream(ctx)
	stream.inFrames <- validStartFrame(t)

	ms := &trackingManagedWithDeadline{
		unblocked:  make(chan struct{}),
		cancelMode: aipapi.CancelModeTransport,
		events: []aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: "hello"},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		return len(stream.sent) >= 2 // Accepted + Event
	})

	stream.inFrames <- backendplugin.ClientFrame{
		Kind:         backendplugin.ClientFrameCancel,
		CancelReason: backendplugin.CancelReasonClient,
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ForwardExecute returned unexpected error on in-band cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ForwardExecute timed out after in-band cancel")
	}

	if ms.cancelCalled.Load() != 1 {
		t.Fatalf("Cancel called %d times, want 1", ms.cancelCalled.Load())
	}
	if ms.cancelCause.Detail != "client" {
		t.Fatalf("Cancel cause detail = %q, want client", ms.cancelCause.Detail)
	}

	stream.mu.Lock()
	sent := stream.sent
	stream.mu.Unlock()

	// Verify sent frame sequence:
	// 0: Accepted (seq 0)
	// 1: Event (seq 1)
	// 2: CancelOutcome (seq 2)
	// 3: Terminal (seq 3)
	if len(sent) < 4 {
		t.Fatalf("sent %d frames, want at least 4: %+v", len(sent), sent)
	}

	if sent[0].Kind != backendplugin.ServerFrameAccepted {
		t.Errorf("frame 0 kind = %v, want ServerFrameAccepted", sent[0].Kind)
	}
	if sent[1].Kind != backendplugin.ServerFrameEvent || sent[1].Sequence != 1 {
		t.Errorf("frame 1 = %+v, want Event with Sequence=1", sent[1])
	}
	if sent[2].Kind != backendplugin.ServerFrameCancelOutcome || sent[2].Sequence != 2 {
		t.Errorf("frame 2 = %+v, want CancelOutcome with Sequence=2", sent[2])
	}
	if sent[2].CancelOutcome == nil || sent[2].CancelOutcome.Mode != backendplugin.CancelModeTransport {
		t.Errorf("frame 2 CancelOutcome = %+v, want Mode=CancelModeTransport", sent[2].CancelOutcome)
	}
	if sent[3].Kind != backendplugin.ServerFrameTerminal || sent[3].Sequence != 3 {
		t.Errorf("frame 3 = %+v, want Terminal with Sequence=3", sent[3])
	}
	if sent[3].Terminal == nil || sent[3].Terminal.Status != backendplugin.TerminalCancelled {
		t.Errorf("frame 3 Terminal = %+v, want Status=TerminalCancelled", sent[3].Terminal)
	}
}

func TestForwardExecute_InBandCancel_DeadlineCalculation(t *testing.T) {
	t.Parallel()

	streamDeadline := time.Now().Add(10 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), streamDeadline)
	defer cancel()

	stream := newNegotiatedInBandStream(ctx)
	stream.inFrames <- validStartFrame(t)

	cancelDeadline := time.Now().Add(500 * time.Millisecond)
	ms := &trackingManagedWithDeadline{
		unblocked: make(chan struct{}),
	}

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool { return ms.recvEntered.Load() })

	stream.inFrames <- backendplugin.ClientFrame{
		Kind:                 backendplugin.ClientFrameCancel,
		CancelReason:         backendplugin.CancelReasonDeadline,
		CancelDeadlineUnixMS: cancelDeadline.UnixMilli(),
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ForwardExecute error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	if ms.cancelDeadline.IsZero() {
		t.Fatal("expected cancel context to have deadline")
	}
	// The effective deadline should match cancelDeadline (within 50ms), NOT the 10s streamDeadline
	if diff := ms.cancelDeadline.Sub(cancelDeadline); diff < -100*time.Millisecond || diff > 100*time.Millisecond {
		t.Fatalf("effective cancel deadline diff=%v, got %v want ~%v", diff, ms.cancelDeadline, cancelDeadline)
	}
}

func TestForwardExecute_CloseInputDoesNotCancelUpstream(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := newChannelExecuteStream(ctx)
	stream.inFrames <- validStartFrame(t)

	ms := &closeCountManaged{unblocked: make(chan struct{})}

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool { return ms.recvEntered.Load() })

	// Send CloseInput
	stream.inFrames <- backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameCloseInput,
	}

	// Wait briefly to ensure CloseInput is processed without canceling
	time.Sleep(50 * time.Millisecond)
	if ms.closeCalls.Load() != 0 {
		t.Errorf("CloseInput caused upstream Close calls: %d", ms.closeCalls.Load())
	}

	// Now unblock ms to finish normally
	close(ms.unblocked)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestForwardExecute_InBandCancel_Idempotent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := newNegotiatedInBandStream(ctx)
	stream.inFrames <- validStartFrame(t)

	ms := &trackingManagedWithDeadline{
		unblocked: make(chan struct{}),
	}

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool { return ms.recvEntered.Load() })

	// Send multiple Cancel frames
	stream.inFrames <- backendplugin.ClientFrame{Kind: backendplugin.ClientFrameCancel, CancelReason: backendplugin.CancelReasonHost}
	stream.inFrames <- backendplugin.ClientFrame{Kind: backendplugin.ClientFrameCancel, CancelReason: backendplugin.CancelReasonHost}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ForwardExecute error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout")
	}

	if calls := ms.cancelCalled.Load(); calls != 1 {
		t.Fatalf("ms.Cancel called %d times, want exactly 1", calls)
	}
}

type delayedCancelBlockingManaged struct {
	events        []aipapi.Event
	eventIdx      int
	mu            sync.Mutex
	cancelCalled  atomic.Int32
	cancelCause   aipapi.CancelCause
	cancelEntered chan struct{}
	releaseCancel chan struct{}
}

func newDelayedCancelBlockingManaged(events []aipapi.Event, releaseCancel chan struct{}) *delayedCancelBlockingManaged {
	return &delayedCancelBlockingManaged{
		events:        events,
		cancelEntered: make(chan struct{}),
		releaseCancel: releaseCancel,
	}
}

func (m *delayedCancelBlockingManaged) Recv(ctx context.Context) (aipapi.Event, error) {
	m.mu.Lock()
	if m.eventIdx < len(m.events) {
		ev := m.events[m.eventIdx]
		m.eventIdx++
		m.mu.Unlock()
		return ev, nil
	}
	m.mu.Unlock()

	select {
	case <-m.cancelEntered:
		return aipapi.Event{}, context.Canceled
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}

func (m *delayedCancelBlockingManaged) Close() error { return nil }

func (m *delayedCancelBlockingManaged) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	m.cancelCalled.Add(1)
	m.cancelCause = cause
	close(m.cancelEntered)
	select {
	case <-m.releaseCancel:
	case <-ctx.Done():
	}
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

// TestForwardExecute_InBandCancel_OutcomeDroppedWhenCancelExceedsGraceWait pins the contract
// that CancelOutcome must precede TerminalCancelled by construction.
// In the current implementation, if the physical Cancel call blocks longer than the 100ms
// grace period in the upstream reader, the upstream reader emits ServerFrameTerminal first,
// causing the sequencer to drop the subsequently emitted ServerFrameCancelOutcome frame.
// The test uses an implementation-neutral release strategy (polling for a Terminal up to ~1200ms
// before unblocking Cancel) so that it deterministically fails on the current race-prone code
// and deterministically passes once refactored to single-sender ordering by construction.
func TestForwardExecute_InBandCancel_OutcomeDroppedWhenCancelExceedsGraceWait(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := newNegotiatedInBandStream(ctx)
	stream.inFrames <- validStartFrame(t)

	releaseCancel := make(chan struct{})
	ms := newDelayedCancelBlockingManaged([]aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "chunk"},
	}, releaseCancel)

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		return len(stream.sent) >= 2 // Accepted + Event
	})

	stream.inFrames <- backendplugin.ClientFrame{
		Kind:         backendplugin.ClientFrameCancel,
		InstanceID:   "contract",
		CancelReason: backendplugin.CancelReasonClient,
	}

	// Release strategy: poll up to ~1200ms for a ServerFrameTerminal to appear in the outbox;
	// regardless of whether it appeared, release the Cancel block so execution can finish.
	terminalPollDeadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(terminalPollDeadline) {
		stream.mu.Lock()
		hasTerminal := false
		for _, f := range stream.sent {
			if f.Kind == backendplugin.ServerFrameTerminal {
				hasTerminal = true
				break
			}
		}
		stream.mu.Unlock()
		if hasTerminal {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Release physical Cancel completion regardless of whether terminal appeared.
	close(releaseCancel)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ForwardExecute returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ForwardExecute did not return within 5s after releasing cancel")
	}

	stream.mu.Lock()
	sent := append([]backendplugin.ServerFrame(nil), stream.sent...)
	stream.mu.Unlock()

	var terminalFrames []backendplugin.ServerFrame
	var outcomeFrames []backendplugin.ServerFrame
	terminalIdx := -1
	outcomeIdx := -1

	for i, f := range sent {
		switch f.Kind {
		case backendplugin.ServerFrameTerminal:
			terminalFrames = append(terminalFrames, f)
			if terminalIdx == -1 {
				terminalIdx = i
			}
		case backendplugin.ServerFrameCancelOutcome:
			outcomeFrames = append(outcomeFrames, f)
			if outcomeIdx == -1 {
				outcomeIdx = i
			}
		}
	}

	// Assertion 1: Exactly one ServerFrameTerminal in the entire outbox.
	if len(terminalFrames) != 1 {
		t.Errorf("got %d ServerFrameTerminal frames, want exactly 1; outbox=%+v", len(terminalFrames), sent)
	} else if terminalFrames[0].Terminal == nil || terminalFrames[0].Terminal.Status != backendplugin.TerminalCancelled {
		t.Errorf("terminal status=%v, want %v", terminalFrames[0].Terminal, backendplugin.TerminalCancelled)
	}

	// Assertion 2: Exactly one ServerFrameCancelOutcome present in the outbox.
	if len(outcomeFrames) != 1 {
		t.Errorf("got %d ServerFrameCancelOutcome frames, want exactly 1; outbox=%+v", len(outcomeFrames), sent)
	}

	// Assertion 3: The CancelOutcome appears EARLIER in the outbox than the Terminal.
	if outcomeIdx == -1 || terminalIdx == -1 || outcomeIdx >= terminalIdx {
		t.Errorf("CancelOutcome index (%d) must be earlier than Terminal index (%d); outbox=%+v", outcomeIdx, terminalIdx, sent)
	}

	// Assertion 4: ms.Cancel was called exactly once; cause detail maps from reason "client".
	if got := ms.cancelCalled.Load(); got != 1 {
		t.Errorf("ms.Cancel called %d times, want exactly 1", got)
	}
	if got := ms.cancelCause.Detail; got != "client" {
		t.Errorf("cancel cause detail=%q, want %q", got, "client")
	}
}

func TestForwardExecute_Active_TerminalSuccess_ExactlyOneTerminalAndNoFrameAfterTerminal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := newNegotiatedInBandStream(ctx)
	stream.inFrames <- validStartFrame(t)

	events := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventResponseFinished},
	}
	ms := &scriptedManaged{events: events}

	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return ms, nil
	})
	if err != nil {
		t.Fatalf("ForwardExecute: %v", err)
	}

	stream.mu.Lock()
	sent := append([]backendplugin.ServerFrame(nil), stream.sent...)
	stream.mu.Unlock()

	if len(sent) < 3 {
		t.Fatalf("expected at least 3 frames (Accepted, Event, Terminal), got %d: %+v", len(sent), sent)
	}

	var terminalCount int
	terminalIdx := -1
	for i, f := range sent {
		if f.Kind == backendplugin.ServerFrameTerminal {
			terminalCount++
			terminalIdx = i
		}
	}

	if terminalCount != 1 {
		t.Fatalf("got %d terminal frames, want exactly 1", terminalCount)
	}
	if terminalIdx != len(sent)-1 {
		t.Fatalf("terminal frame at index %d is not the final frame (total frames: %d)", terminalIdx, len(sent))
	}
	if sent[terminalIdx].Terminal == nil || sent[terminalIdx].Terminal.Status != backendplugin.TerminalSuccess {
		t.Fatalf("terminal status = %+v, want TerminalSuccess", sent[terminalIdx].Terminal)
	}
}

//nolint:paralleltest // serial by design: goleak must not observe goroutines from sibling parallel tests
func TestForwardExecute_Active_InBandCancel_Goleak(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := newNegotiatedInBandStream(ctx)
	stream.inFrames <- validStartFrame(t)

	ms := &trackingManagedWithDeadline{
		unblocked:  make(chan struct{}),
		cancelMode: aipapi.CancelModeProvider,
		events: []aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: "chunk"},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool {
		stream.mu.Lock()
		defer stream.mu.Unlock()
		return len(stream.sent) >= 2
	})

	stream.inFrames <- backendplugin.ClientFrame{
		Kind:         backendplugin.ClientFrameCancel,
		CancelReason: backendplugin.CancelReasonClient,
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ForwardExecute returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ForwardExecute timed out after in-band cancel")
	}
}

//nolint:paralleltest // serial by design: goleak must not observe goroutines from sibling parallel tests
func TestForwardExecute_Active_UpstreamError_Goleak(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := newNegotiatedInBandStream(ctx)
	stream.inFrames <- validStartFrame(t)

	upstreamErr := errors.New("upstream failure for leak test")
	ms := &scriptedManaged{
		events: []aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: "partial"},
		},
		err: upstreamErr,
	}

	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return ms, nil
	})
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("ForwardExecute returned %v, want %v", err, upstreamErr)
	}
}

func TestForwardExecute_Active_UpstreamErrorWithoutCancel_EmitsTerminalFailureAndReturnsOriginalError(t *testing.T) {
	t.Parallel()

	capabilityWrap := fmt.Errorf("%w: test model is not in instance inventory", aipapi.ErrCapabilityReject)
	capabilityTyped := &aipapi.RejectError{Reason: "test model is not in instance inventory"}
	tests := []struct {
		name       string
		recvErr    error
		want       backendplugin.ErrorCode
		wantMsg    string
		withOutput bool
	}{
		{name: "generic post-output", recvErr: errors.New("provider network failure"), want: backendplugin.ErrorCodeInternal, wantMsg: "upstream execution failed", withOutput: true},
		{name: "capability/wrap post-output", recvErr: capabilityWrap, want: backendplugin.ErrorCodeFailedPrecondition, wantMsg: "capability rejected", withOutput: true},
		{name: "capability/typed post-output", recvErr: capabilityTyped, want: backendplugin.ErrorCodeFailedPrecondition, wantMsg: "capability rejected", withOutput: true},
		{name: "generic pre-output", recvErr: errors.New("provider network failure"), want: backendplugin.ErrorCodeInternal, wantMsg: "upstream execution failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream := newNegotiatedInBandStream(ctx)
			stream.inFrames <- validStartFrame(t)

			var events []aipapi.Event
			if tc.withOutput {
				events = []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "part1"}}
			}
			ms := &scriptedManaged{events: events, err: tc.recvErr}

			err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
				return ms, nil
			})

			if !errors.Is(err, tc.recvErr) {
				t.Fatalf("ForwardExecute returned err=%v, want %v", err, tc.recvErr)
			}
			if tc.want == backendplugin.ErrorCodeFailedPrecondition && !errors.Is(err, aipapi.ErrCapabilityReject) {
				t.Fatalf("capability err=%v must wrap aipapi.ErrCapabilityReject", err)
			}

			stream.mu.Lock()
			sent := append([]backendplugin.ServerFrame(nil), stream.sent...)
			stream.mu.Unlock()

			var terminalFrames []backendplugin.ServerFrame
			for _, f := range sent {
				if f.Kind == backendplugin.ServerFrameCancelOutcome {
					t.Fatalf("unexpected CancelOutcome on non-cancellation upstream error: %+v", f)
				}
				if f.Kind == backendplugin.ServerFrameTerminal {
					terminalFrames = append(terminalFrames, f)
				}
			}

			if len(terminalFrames) != 1 {
				t.Fatalf("got %d terminal frames, want exactly 1; frames=%+v", len(terminalFrames), sent)
			}
			term := terminalFrames[0].Terminal
			if term == nil || term.Status != backendplugin.TerminalFailure {
				t.Fatalf("terminal status = %+v, want TerminalFailure", term)
			}
			if term.Error == nil || term.Error.Code != tc.want {
				t.Fatalf("terminal error = %+v, want Code=%v", term.Error, tc.want)
			}
			if term.Error.Message != tc.wantMsg {
				t.Fatalf("terminal error message = %q, want %q", term.Error.Message, tc.wantMsg)
			}
			if term.Error.Retryable {
				t.Fatalf("terminal must be non-retryable: %+v", term.Error)
			}
			if term.Error.OutputCommitted != tc.withOutput {
				t.Fatalf("terminal output-committed=%v, want %v: %+v", term.Error.OutputCommitted, tc.withOutput, term.Error)
			}
		})
	}
}

// blockingSendStream holds the coordinator inside its first event-frame host
// write and then fails it, so the test controls when the coordinator stops.
type blockingSendStream struct {
	*channelExecuteStream
	sendEntered chan struct{}
	releaseSend chan struct{}
	sendErr     error
	once        sync.Once
}

func (s *blockingSendStream) Negotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
		EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
	}
}

func (s *blockingSendStream) Send(frame backendplugin.ServerFrame) error {
	if frame.Kind != backendplugin.ServerFrameEvent {
		return s.channelExecuteStream.Send(frame)
	}
	s.once.Do(func() { close(s.sendEntered) })
	<-s.releaseSend
	return s.sendErr
}

// eofManaged emits its script and then EOF, signalling when the script is spent.
type eofManaged struct {
	events []aipapi.Event
	idx    int
	spent  chan struct{}
}

func (m *eofManaged) Recv(context.Context) (aipapi.Event, error) {
	if m.idx < len(m.events) {
		m.idx++
		return m.events[m.idx-1], nil
	}
	select {
	case <-m.spent:
	default:
		close(m.spent)
	}
	return aipapi.Event{}, io.EOF
}

func (m *eofManaged) Close() error { return nil }

func (m *eofManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// TestForwardExecute_Active_TerminalObservationBoundedAfterCoordinatorStops pins
// that the upstream reader's terminal observation can be abandoned once the
// coordinator stopped receiving. The coordinator consumes one observation and
// blocks in the host write; the reader publishes the next sixteen, filling
// upstreamObsCh exactly, and reaches EOF; the write then fails, so the
// coordinator leaves its loop. The terminal observation must not wait for buffer
// space nobody frees, or ForwardExecute never returns.
//
//nolint:paralleltest // serial by design: a regressing run intentionally leaves the coordinator blocked
func TestForwardExecute_Active_TerminalObservationBoundedAfterCoordinatorStops(t *testing.T) {
	hostWriteErr := errors.New("plugin host write failed")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := &blockingSendStream{
		channelExecuteStream: newChannelExecuteStream(ctx),
		sendEntered:          make(chan struct{}),
		releaseSend:          make(chan struct{}),
		sendErr:              hostWriteErr,
	}
	stream.inFrames <- validStartFrame(t)
	events := make([]aipapi.Event, 17) // one consumed + sixteen buffered
	for i := range events {
		events[i] = aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "chunk"}
	}
	ms := &eofManaged{events: events, spent: make(chan struct{})}

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	<-stream.sendEntered
	<-ms.spent
	close(stream.releaseSend)

	select {
	case err := <-done:
		if !errors.Is(err, hostWriteErr) {
			t.Fatalf("ForwardExecute returned %v, want the host write failure %v", err, hostWriteErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ForwardExecute did not return: the terminal upstream observation blocked on a full observation buffer after the coordinator stopped")
	}
}
