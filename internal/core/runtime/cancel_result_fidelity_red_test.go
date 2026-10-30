package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type customCancelStream struct {
	mode        aipapi.CancelMode
	cancelErr   error
	closeErr    error
	cancelCalls atomic.Int64
	closeCalls  atomic.Int64
}

func (s *customCancelStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	return aipapi.CancelResult{
		Mode: s.mode,
		Err:  s.cancelErr,
	}
}

func (s *customCancelStream) Close() error {
	s.closeCalls.Add(1)
	return s.closeErr
}

func (s *customCancelStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func TestRED_CancelResultFidelity_SessionLifecycleHandleReturnsFabricatedMode(t *testing.T) {
	t.Parallel()

	errTransport := errors.New("transport reset")
	physStream := &customCancelStream{
		mode:      aipapi.CancelModeTransport,
		cancelErr: errTransport,
	}

	ex := TestExecutor()
	terminal := newTurnTerminal()
	bindTurnTerminalRuntime(terminal, ex)

	sess := newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{ALegID: "a-fid-1", BLegID: "b-fid-1", Seq: 1},
	})
	sess.storeInner(physStream)

	handle := sess.lifecycleHandle()
	res := handle.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})

	if res.Mode != aipapi.CancelModeTransport {
		t.Fatalf("expected Mode %v, got %v", aipapi.CancelModeTransport, res.Mode)
	}
	if !errors.Is(res.Err, errTransport) {
		t.Fatalf("expected error %v, got %v", errTransport, res.Err)
	}
}

func TestRED_CancelResultFidelity_ReadyAttemptLifecycleReturnsFabricatedMode(t *testing.T) {
	t.Parallel()

	physStream := &customCancelStream{
		mode: aipapi.CancelModeCloseOnly,
	}

	sess := newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{ALegID: "a-fid-2", BLegID: "b-fid-2", Seq: 1},
	})
	sess.storeInner(physStream)

	ready := newReadyAttempt(sess, pendingSelectionEffects{})
	handle := ready.lifecycleHandle()

	res := handle.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})

	if res.Mode != aipapi.CancelModeCloseOnly {
		t.Fatalf("expected Mode %v, got %v", aipapi.CancelModeCloseOnly, res.Mode)
	}
}

func TestCharacterization_CancelCloseInvocationCounts_PinnedUnderRaces(t *testing.T) {
	t.Parallel()

	physStream := &customCancelStream{
		mode: aipapi.CancelModeProvider,
	}

	sess := newAttemptSession(attemptSessionInput{
		bleg: b2bua.BLegRecord{ALegID: "a-count-1", BLegID: "b-count-1", Seq: 1},
	})
	sess.storeInner(physStream)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_ = sess.cancelViaLifecycle(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		})
		wg.Go(func() {
			_ = sess.closeViaLifecycle()
		})
		wg.Go(func() {
			sess.TerminalizeAttempt(context.Background(), IntentCancellation, attemptEvidence{
				Command: sdkterminal.CommandCancel,
			})
		})
		wg.Go(func() {
			_, _ = sess.receive(context.Background(), false)
		})
	}
	wg.Wait()

	if got := physStream.cancelCalls.Load(); got > 1 {
		t.Fatalf("physical Cancel called %d times, want at-most-once (<= 1)", got)
	}
	if got := physStream.closeCalls.Load(); got > 1 {
		t.Fatalf("physical Close called %d times, want at-most-once (<= 1)", got)
	}
}
