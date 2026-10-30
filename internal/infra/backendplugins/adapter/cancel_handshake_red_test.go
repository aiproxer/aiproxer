package adapter

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type dummyExecuteSession struct {
	executeFn func(backendplugin.ExecuteStream) error
}

func (d *dummyExecuteSession) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{}, nil
}

func (d *dummyExecuteSession) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (d *dummyExecuteSession) Execute(stream backendplugin.ExecuteStream) error {
	if d.executeFn != nil {
		return d.executeFn(stream)
	}
	return nil
}

func (d *dummyExecuteSession) Close(context.Context) error {
	return nil
}

func testCallWithMessages(id string) aipapi.Call {
	return aipapi.Call{
		ID: id,
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
}

func TestRED_Adapter_CancelDeadlineNotPropagated(t *testing.T) {
	t.Parallel()

	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			<-stream.Context().Done()
			return stream.Context().Err()
		},
	}

	call := testCallWithMessages("req-1")
	cand := routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(context.Background(), sess, Options{
		InstanceID:    "inst-1",
		CancelTimeout: 500 * time.Millisecond,
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	cancelCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_ = ms.Cancel(cancelCtx, aipapi.CancelCause{Kind: aipapi.CancelExplicit})

	stream, ok := ms.(*managedStream)
	if !ok {
		t.Fatalf("ms is not *managedStream")
	}
	// Read the start frame first
	<-stream.hostFrames

	// Read the cancel frame
	select {
	case cancelFrame := <-stream.hostFrames:
		if cancelFrame.Kind != backendplugin.ClientFrameCancel {
			t.Fatalf("expected ClientFrameCancel, got %v", cancelFrame.Kind)
		}
		if cancelFrame.CancelDeadlineUnixMS == 0 {
			t.Fatalf("cancelFrame.CancelDeadlineUnixMS = 0, want non-zero propagated deadline")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no cancel frame found in hostFrames")
	}
}

func TestRED_Adapter_CancelReturnsImmediatelyWithoutWaitingForOutcomeOrTerminal(t *testing.T) {
	t.Parallel()

	terminalReached := make(chan struct{})
	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			// Read start frame
			_, _ = stream.Recv()
			if err := stream.Send(backendplugin.ServerFrame{
				Kind: backendplugin.ServerFrameAccepted,
			}); err != nil {
				return err
			}
			// Wait before sending terminal
			<-terminalReached
			return stream.Send(backendplugin.ServerFrame{
				Kind:     backendplugin.ServerFrameTerminal,
				Sequence: 1,
				Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalCancelled},
			})
		},
	}

	call := testCallWithMessages("req-2")
	cand := routing.AttemptCandidate{Key: "cand-2", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(context.Background(), sess, Options{
		InstanceID:    "inst-1",
		CancelTimeout: 500 * time.Millisecond,
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}
	defer func() {
		_ = ms.Close()
	}()

	time.AfterFunc(50*time.Millisecond, func() {
		close(terminalReached)
	})

	// Call Cancel while connector has not terminated yet
	res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})

	stream, ok := ms.(*managedStream)
	if !ok {
		t.Fatalf("ms is not *managedStream")
	}
	if !stream.terminalSeen.Load() {
		t.Fatalf("Cancel returned %v before connector reached terminal or cancel outcome", res)
	}
}

func TestRED_Adapter_CancelOutcomeFrameDiscarded(t *testing.T) {
	t.Parallel()

	stream := &managedStream{
		ctx:        context.Background(),
		cancel:     func() {},
		hostFrames: make(chan backendplugin.ClientFrame, 4),
		events:     make(chan aipapi.Event, 4),
		done:       make(chan struct{}),
		opt: Options{
			Negotiation: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
				EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
			},
		},
	}

	err := stream.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
	if err != nil {
		t.Fatalf("onPluginFrame accepted failed: %v", err)
	}

	outcomeFrame := backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameCancelOutcome,
		Sequence: 1,
		CancelOutcome: &backendplugin.CancelOutcome{
			Acknowledged: true,
			Reason:       backendplugin.CancelReasonHost,
			Mode:         backendplugin.CancelModeProvider,
			Detail:       "provider cancelled",
		},
	}

	err = stream.onPluginFrame(outcomeFrame)
	if err != nil {
		t.Fatalf("onPluginFrame failed: %v", err)
	}

	prog := stream.CancellationProgress()
	if !prog.OutcomeSeen {
		t.Fatal("CancellationProgress().OutcomeSeen is false after ServerFrameCancelOutcome")
	}
	if !prog.OutcomeAcknowledged {
		t.Fatal("CancellationProgress().OutcomeAcknowledged is false")
	}
	if prog.OutcomeMode != backendplugin.CancelModeProvider {
		t.Fatalf("CancellationProgress().OutcomeMode = %v, want %v", prog.OutcomeMode, backendplugin.CancelModeProvider)
	}
	if prog.OutcomeReason != backendplugin.CancelReasonHost {
		t.Fatalf("CancellationProgress().OutcomeReason = %v, want %v", prog.OutcomeReason, backendplugin.CancelReasonHost)
	}
	if prog.OutcomeDetail != "provider cancelled" {
		t.Fatalf("CancellationProgress().OutcomeDetail = %q, want 'provider cancelled'", prog.OutcomeDetail)
	}
}

func TestCharacterization_Adapter_LegacyExecuteContextCancelTransportDeath(t *testing.T) {
	t.Parallel()

	execCtxCancelSeen := make(chan struct{})
	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			<-stream.Context().Done()
			close(execCtxCancelSeen)
			return stream.Context().Err()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	call := testCallWithMessages("req-legacy")
	cand := routing.AttemptCandidate{Key: "cand-legacy", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(ctx, sess, Options{
		InstanceID: "inst-legacy",
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: 7,
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}

	// Cancel parent context -> cancels Execute context
	cancel()

	select {
	case <-execCtxCancelSeen:
	case <-time.After(1 * time.Second):
		t.Fatal("Execute stream context was not canceled when parent context was canceled")
	}

	_ = ms.Close()
	_, recvErr := ms.Recv(context.Background())
	if !errors.Is(recvErr, context.Canceled) && !errors.Is(recvErr, io.EOF) {
		t.Logf("recvErr after transport death: %v", recvErr)
	}
}

func TestAdapter_CancelGraceTimeoutForcesTransportCancel(t *testing.T) {
	t.Parallel()

	execCtxDone := make(chan struct{})
	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			// Read start frame
			_, _ = stream.Recv()
			// Block and ignore cancel frame until stream context is force-cancelled
			<-stream.Context().Done()
			close(execCtxDone)
			return stream.Context().Err()
		},
	}

	call := testCallWithMessages("req-timeout")
	cand := routing.AttemptCandidate{Key: "cand-timeout", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(context.Background(), sess, Options{
		InstanceID:    "inst-timeout",
		CancelTimeout: 50 * time.Millisecond,
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})

	if res.Mode != aipapi.CancelModeTransport {
		t.Fatalf("expected CancelModeTransport on timeout, got %v", res.Mode)
	}
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded error on timeout, got %v", res.Err)
	}

	select {
	case <-execCtxDone:
	case <-time.After(1 * time.Second):
		t.Fatal("active Execute context was not force-cancelled after grace timeout")
	}

	stream, ok := ms.(*managedStream)
	if !ok {
		t.Fatalf("ms is not *managedStream")
	}
	prog := stream.CancellationProgress()
	if !prog.ForcedAbort {
		t.Fatal("expected ForcedAbort to be true after grace timeout")
	}
}

func TestAdapter_CancelAfterCloseReturnsCloseOnly(t *testing.T) {
	t.Parallel()

	sess := &dummyExecuteSession{}
	call := testCallWithMessages("req-close")
	cand := routing.AttemptCandidate{Key: "cand-close", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(context.Background(), sess, Options{
		InstanceID: "inst-close",
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}

	if err := ms.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeCloseOnly {
		t.Fatalf("Cancel after Close mode = %v, want CancelModeCloseOnly", res.Mode)
	}
}

func TestAdapter_LegacyCancelDoesNotSendCancelFrame(t *testing.T) {
	t.Parallel()

	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			<-stream.Context().Done()
			return stream.Context().Err()
		},
	}

	call := testCallWithMessages("req-legacy-cancel")
	cand := routing.AttemptCandidate{Key: "cand-legacy-cancel", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(context.Background(), sess, Options{
		InstanceID:    "inst-legacy-cancel",
		CancelTimeout: 50 * time.Millisecond,
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: 7,
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeTransport {
		t.Fatalf("legacy Cancel mode = %v, want CancelModeTransport", res.Mode)
	}

	stream, ok := ms.(*managedStream)
	if !ok {
		t.Fatalf("ms is not *managedStream")
	}
	// Only the START frame should be present in hostFrames
	select {
	case frame := <-stream.hostFrames:
		if frame.Kind != backendplugin.ClientFrameStart {
			t.Fatalf("expected start frame, got %v", frame.Kind)
		}
	default:
	}

	select {
	case frame := <-stream.hostFrames:
		t.Fatalf("unexpected frame in hostFrames on legacy stream: %+v", frame)
	case <-time.After(50 * time.Millisecond):
		// Expected: no cancel frame sent on legacy stream
	}
}

func TestAdapter_Cancel_OutcomeBeforeTerminal(t *testing.T) {
	t.Parallel()

	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			for {
				f, err := stream.Recv()
				if err != nil {
					return err
				}
				if f.Kind == backendplugin.ClientFrameStart {
					_ = stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
				}
				if f.Kind == backendplugin.ClientFrameCancel {
					_ = stream.Send(backendplugin.ServerFrame{
						Kind:     backendplugin.ServerFrameCancelOutcome,
						Sequence: 1,
						CancelOutcome: &backendplugin.CancelOutcome{
							Acknowledged: true,
							Mode:         backendplugin.CancelModeProvider,
							Reason:       f.CancelReason,
							Detail:       "provider acknowledged",
						},
					})
					_ = stream.Send(backendplugin.ServerFrame{
						Kind:     backendplugin.ServerFrameTerminal,
						Sequence: 2,
						Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalCancelled},
					})
					return nil
				}
			}
		},
	}

	call := testCallWithMessages("req-outcome-first")
	cand := routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(context.Background(), sess, Options{
		InstanceID:    "inst-1",
		CancelTimeout: 500 * time.Millisecond,
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeProvider {
		t.Fatalf("res.Mode = %v, want CancelModeProvider", res.Mode)
	}

	stream, ok := ms.(*managedStream)
	if !ok {
		t.Fatalf("ms is not *managedStream")
	}
	prog := stream.CancellationProgress()
	if !prog.OutcomeSeen || !prog.OutcomeAcknowledged || prog.OutcomeMode != backendplugin.CancelModeProvider {
		t.Fatalf("unexpected progress: %+v", prog)
	}
	if prog.OutcomeDetail != "provider acknowledged" {
		t.Fatalf("unexpected detail: %q", prog.OutcomeDetail)
	}
	if !prog.TerminalSeen {
		t.Fatal("expected TerminalSeen to be true")
	}
	if prog.ForcedAbort {
		t.Fatal("expected ForcedAbort to be false")
	}
}

func TestAdapter_Cancel_TerminalWithoutOutcome(t *testing.T) {
	t.Parallel()

	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			for {
				f, err := stream.Recv()
				if err != nil {
					return err
				}
				if f.Kind == backendplugin.ClientFrameStart {
					_ = stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
				}
				if f.Kind == backendplugin.ClientFrameCancel {
					_ = stream.Send(backendplugin.ServerFrame{
						Kind:     backendplugin.ServerFrameTerminal,
						Sequence: 1,
						Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalCancelled},
					})
					return nil
				}
			}
		},
	}

	call := testCallWithMessages("req-term-without-outcome")
	cand := routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(context.Background(), sess, Options{
		InstanceID:    "inst-1",
		CancelTimeout: 500 * time.Millisecond,
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeNone {
		t.Fatalf("res.Mode = %v, want CancelModeNone", res.Mode)
	}

	stream, ok := ms.(*managedStream)
	if !ok {
		t.Fatalf("ms is not *managedStream")
	}
	prog := stream.CancellationProgress()
	if prog.OutcomeSeen {
		t.Fatal("expected OutcomeSeen to be false")
	}
	if !prog.TerminalSeen {
		t.Fatal("expected TerminalSeen to be true")
	}
	if prog.ForcedAbort {
		t.Fatal("expected ForcedAbort to be false")
	}
}

func TestAdapter_PluginFrame_DuplicateAndLateFrames(t *testing.T) {
	t.Parallel()

	stream := &managedStream{
		ctx:        context.Background(),
		cancel:     func() {},
		hostFrames: make(chan backendplugin.ClientFrame, 4),
		events:     make(chan aipapi.Event, 4),
		done:       make(chan struct{}),
		opt: Options{
			Negotiation: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
				EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
			},
		},
	}

	// 1. Initial Accepted frame
	if err := stream.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		t.Fatalf("onPluginFrame(Accepted) failed: %v", err)
	}

	// 2. Duplicate Accepted frame -> rejected
	if err := stream.onPluginFrame(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err == nil {
		t.Fatal("expected duplicate Accepted to fail")
	}

	// 3. Monotonic sequence gap -> rejected
	if err := stream.onPluginFrame(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameDiagnostic,
		Sequence: 5,
	}); err == nil {
		t.Fatal("expected sequence gap to fail")
	}

	// 4. Correct sequence diagnostic -> accepted
	if err := stream.onPluginFrame(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameDiagnostic,
		Sequence: 1,
	}); err != nil {
		t.Fatalf("onPluginFrame(Diagnostic) failed: %v", err)
	}

	// 5. Terminal frame -> accepted
	if err := stream.onPluginFrame(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameTerminal,
		Sequence: 2,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	}); err != nil {
		t.Fatalf("onPluginFrame(Terminal) failed: %v", err)
	}

	// 6. Duplicate terminal -> rejected
	if err := stream.onPluginFrame(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameTerminal,
		Sequence: 3,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	}); !errors.Is(err, backendplugin.ErrEventAfterTerminal) && !errors.Is(err, backendplugin.ErrMultipleTerminals) {
		t.Fatalf("expected ErrEventAfterTerminal or ErrMultipleTerminals, got %v", err)
	}

	// 7. Late frame after terminal -> rejected
	if err := stream.onPluginFrame(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameDiagnostic,
		Sequence: 4,
	}); !errors.Is(err, backendplugin.ErrEventAfterTerminal) {
		t.Fatalf("expected ErrEventAfterTerminal, got %v", err)
	}
}

func TestAdapter_Cancel_NoDeadlineBoundedForce(t *testing.T) {
	t.Parallel()

	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			<-stream.Context().Done()
			return stream.Context().Err()
		},
	}

	call := testCallWithMessages("req-nodeadline")
	cand := routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(context.Background(), sess, Options{
		InstanceID:    "inst-1",
		CancelTimeout: 0, // Should default to bounded 2s
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	started := time.Now()
	res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	elapsed := time.Since(started)

	if res.Mode != aipapi.CancelModeTransport {
		t.Fatalf("res.Mode = %v, want CancelModeTransport", res.Mode)
	}
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", res.Err)
	}
	// Bounded default is 2s; should finish within ~3s
	if elapsed < 1800*time.Millisecond || elapsed > 4*time.Second {
		t.Fatalf("elapsed = %v, want bounded around 2s", elapsed)
	}
}

func TestAdapter_Cancel_PoisonConnectorBoundedForce(t *testing.T) {
	t.Parallel()

	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			// Poison connector: reads start, sends accepted, then ignores everything and hangs
			_, _ = stream.Recv()
			_ = stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
			<-stream.Context().Done()
			return stream.Context().Err()
		},
	}

	call := testCallWithMessages("req-poison")
	cand := routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(context.Background(), sess, Options{
		InstanceID:    "inst-1",
		CancelTimeout: 50 * time.Millisecond,
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}
	defer func() { _ = ms.Close() }()

	res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeTransport {
		t.Fatalf("res.Mode = %v, want CancelModeTransport", res.Mode)
	}
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", res.Err)
	}
	stream, ok := ms.(*managedStream)
	if !ok {
		t.Fatalf("ms is not *managedStream")
	}
	prog := stream.CancellationProgress()
	if !prog.ForcedAbort {
		t.Fatal("expected ForcedAbort to be true")
	}
}

func TestAdapter_SameSession_UnrelatedExecuteSurvival(t *testing.T) {
	t.Parallel()

	var execCount int
	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			execCount++
			if execCount == 1 {
				// Stream 1: poison / cancelled
				<-stream.Context().Done()
				return stream.Context().Err()
			}
			// Stream 2: executes normally
			for {
				f, err := stream.Recv()
				if err != nil {
					return err
				}
				if f.Kind == backendplugin.ClientFrameStart {
					_ = stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
					d := "stream 2 output"
					_ = stream.Send(backendplugin.ServerFrame{
						Kind:     backendplugin.ServerFrameEvent,
						Sequence: 1,
						Event:    &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta, Delta: &d},
					})
					_ = stream.Send(backendplugin.ServerFrame{
						Kind:     backendplugin.ServerFrameTerminal,
						Sequence: 2,
						Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
					})
					return nil
				}
			}
		},
	}

	cand := routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	opt := Options{
		InstanceID:    "inst-survival",
		CancelTimeout: 50 * time.Millisecond,
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}

	// 1. First Execute: cancelled / forced
	ms1, err := openStream(context.Background(), sess, opt, testCallWithMessages("req-1"), cand)
	if err != nil {
		t.Fatalf("openStream 1 failed: %v", err)
	}
	res1 := ms1.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res1.Mode != aipapi.CancelModeTransport {
		t.Fatalf("ms1 Cancel mode = %v, want CancelModeTransport", res1.Mode)
	}
	_ = ms1.Close()

	// 2. Second Execute on the same session survives and executes successfully
	ms2, err := openStream(context.Background(), sess, opt, testCallWithMessages("req-2"), cand)
	if err != nil {
		t.Fatalf("openStream 2 failed: %v", err)
	}
	defer func() { _ = ms2.Close() }()

	ev, err := ms2.Recv(context.Background())
	if err != nil {
		t.Fatalf("ms2 Recv failed: %v", err)
	}
	if ev.Kind != aipapi.EventTextDelta || ev.Delta != "stream 2 output" {
		t.Fatalf("ms2 unexpected event: %+v", ev)
	}
}

func TestAdapter_CloseCancelIdempotency(t *testing.T) {
	t.Parallel()

	sess := &dummyExecuteSession{
		executeFn: func(stream backendplugin.ExecuteStream) error {
			<-stream.Context().Done()
			return stream.Context().Err()
		},
	}

	call := testCallWithMessages("req-idempotency")
	cand := routing.AttemptCandidate{Key: "cand-1", Primary: routing.Primary{Backend: "b1", Model: "m1"}}
	ms, err := openStream(context.Background(), sess, Options{
		InstanceID:    "inst-idempotency",
		CancelTimeout: 50 * time.Millisecond,
		Negotiation: backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}, call, cand)
	if err != nil {
		t.Fatalf("openStream failed: %v", err)
	}

	// Multiple concurrent Close() calls
	closeDone := make(chan error, 10)
	for range 10 {
		go func() {
			closeDone <- ms.Close()
		}()
	}
	for range 10 {
		if err := <-closeDone; err != nil {
			t.Fatalf("concurrent Close failed: %v", err)
		}
	}

	// Cancel after Close returns CancelModeCloseOnly
	res := ms.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeCloseOnly {
		t.Fatalf("Cancel after Close mode = %v, want CancelModeCloseOnly", res.Mode)
	}
}
