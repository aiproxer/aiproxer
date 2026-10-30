package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type nonCooperativeAttemptStream struct {
	cancelStarted chan struct{}
	releaseCancel chan struct{}
	cancelDone    chan struct{}
	closeCalls    atomic.Int32
	cancelCalls   atomic.Int32
	closeOnce     sync.Once
}

func (s *nonCooperativeAttemptStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *nonCooperativeAttemptStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	close(s.cancelStarted)
	<-s.releaseCancel
	close(s.cancelDone)
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

func (s *nonCooperativeAttemptStream) Close() error {
	s.closeCalls.Add(1)
	s.closeOnce.Do(func() { close(s.releaseCancel) })
	return nil
}

func (s *nonCooperativeAttemptStream) release() {
	s.closeOnce.Do(func() { close(s.releaseCancel) })
}

func TestAttemptSession_CancelForceCloseKeepsTerminalOwnerAndBillingBounded(t *testing.T) {
	t.Parallel()

	stream := &nonCooperativeAttemptStream{
		cancelStarted: make(chan struct{}),
		releaseCancel: make(chan struct{}),
		cancelDone:    make(chan struct{}),
	}
	defer stream.release()
	var billingCalls atomic.Int32
	sess := newAttemptSession(attemptSessionInput{
		inner: stream,
		bleg:  b2bua.BLegRecord{ALegID: "a-force", BLegID: "b-force", Seq: 1},
		appendBillingLegFn: func(context.Context, b2bua.BLegRecord, routing.Primary, time.Time, time.Time, usageevidence.LegOutcome) {
			billingCalls.Add(1)
		},
	})

	cancelDone := make(chan aipapi.CancelResult, 1)
	handle := sess.lifecycleHandle()
	go func() {
		cancelDone <- handle.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	}()
	select {
	case <-stream.cancelStarted:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("attempt Cancel did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- handle.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("forced Close returned error: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Close remained blocked behind the terminal winner")
	}

	var result aipapi.CancelResult
	select {
	case result = <-cancelDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("TerminalizeAttempt did not finish after Close unblocked Cancel")
	}
	if result.Mode != aipapi.CancelModeProvider {
		t.Fatalf("Cancel mode = %q, want provider result from physical Cancel", result.Mode)
	}
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("Cancel error = %v, want forced-cleanup deadline", result.Err)
	}
	select {
	case <-stream.cancelDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("owned Cancel worker remained after Close unblocked it")
	}
	if got := stream.cancelCalls.Load(); got != 1 {
		t.Fatalf("physical Cancel calls = %d, want 1", got)
	}
	if got := stream.closeCalls.Load(); got != 1 {
		t.Fatalf("physical Close calls = %d, want 1", got)
	}
	if got := billingCalls.Load(); got != 1 {
		t.Fatalf("billing terminal effects = %d, want 1", got)
	}
	if sess.hasInner() {
		t.Fatal("attempt retained physical inner stream after terminalization")
	}
}

func TestAttemptSession_PendingCancelCauseSynchronizesWithTerminalization(t *testing.T) {
	t.Parallel()

	for range 128 {
		sess := newAttemptSession(attemptSessionInput{})
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			sess.setPendingCancelCause(aipapi.CancelCause{Kind: aipapi.CancelClientGone})
		}()
		go func() {
			defer wg.Done()
			<-start
			sess.TerminalizeAttempt(context.Background(), IntentCancellation, attemptEvidence{})
		}()
		close(start)
		wg.Wait()
	}
}
