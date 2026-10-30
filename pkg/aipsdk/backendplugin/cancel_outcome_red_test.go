package backendplugin_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type cancelFailingManaged struct {
	recvEntered atomic.Bool
	unblocked   chan struct{}
	cancelRes   aipapi.CancelResult
	cancelCalls atomic.Int32
	cancelCause aipapi.CancelCause
}

func (m *cancelFailingManaged) Recv(ctx context.Context) (aipapi.Event, error) {
	m.recvEntered.Store(true)
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-m.unblocked:
		return aipapi.Event{}, context.Canceled
	}
}

func (m *cancelFailingManaged) Close() error { return nil }

func (m *cancelFailingManaged) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	m.cancelCalls.Add(1)
	m.cancelCause = cause
	close(m.unblocked)
	return m.cancelRes
}

func TestRED_ForwardExecute_InBandCancel_FailureOutcome(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := newNegotiatedChannelExecuteStream(ctx)
	stream.inFrames <- validStartFrame(t)

	sentinelErr := errors.New("adapter-private-unique-cancel-error-7ab3\nwith newline")
	ms := &cancelFailingManaged{
		unblocked: make(chan struct{}),
		cancelRes: aipapi.CancelResult{
			Mode: aipapi.CancelModeProvider,
			Err:  sentinelErr,
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
			return ms, nil
		})
	}()

	waitUntil(t, 2*time.Second, func() bool { return ms.recvEntered.Load() })

	cancelDeadline := time.Now().Add(500 * time.Millisecond)
	stream.inFrames <- backendplugin.ClientFrame{
		Kind:                 backendplugin.ClientFrameCancel,
		CancelReason:         backendplugin.CancelReasonHost,
		CancelDeadlineUnixMS: cancelDeadline.UnixMilli(),
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ForwardExecute returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ForwardExecute timed out after in-band cancel")
	}

	stream.mu.Lock()
	sent := stream.sent
	stream.mu.Unlock()

	var outcomeFrame *backendplugin.ServerFrame
	for i := range sent {
		if sent[i].Kind == backendplugin.ServerFrameCancelOutcome {
			outcomeFrame = &sent[i]
			break
		}
	}

	if outcomeFrame == nil || outcomeFrame.CancelOutcome == nil {
		t.Fatalf("expected ServerFrameCancelOutcome, got frames: %+v", sent)
	}

	outcome := outcomeFrame.CancelOutcome
	if outcome.Acknowledged != false {
		t.Fatalf("outcome.Acknowledged = %v, want false (RED failure)", outcome.Acknowledged)
	}
	if outcome.Mode != backendplugin.CancelModeProvider {
		t.Fatalf("outcome.Mode = %v, want CancelModeProvider", outcome.Mode)
	}
	if outcome.Detail != "cancel failed" {
		t.Fatalf("outcome.Detail = %q, want low-cardinality cancellation failure", outcome.Detail)
	}
	if strings.Contains(outcome.Detail, "adapter-private-unique-cancel-error-7ab3") {
		t.Fatalf("outcome.Detail leaked adapter error: %q", outcome.Detail)
	}
	if strings.Contains(outcome.Detail, "\n") || strings.Contains(outcome.Detail, "\r") {
		t.Fatalf("outcome.Detail contains newlines: %q, want single-line", outcome.Detail)
	}
}
