package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	schedulekit "github.com/aiproxer/aiproxer/internal/testkit/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestContinuationTransactionCancellationWinsAndJoinsOpenedWork(t *testing.T) {
	schedule := schedulekit.CancellationRaceSchedule()
	terminalOwner, stream, b1, authority, store := newContinuationRedHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := make(chan struct{})
	var startOnce sync.Once
	var opened *continuationCountingManagedStream
	stream.recovery.opener = func(ctx context.Context, _ replacementOpenRequest) (replacementOpenResult, error) {
		startOnce.Do(func() { close(started) })
		opened = &continuationCountingManagedStream{}
		b2 := continuationOpenResult(t, b1)
		b2.ready.session.storeInner(opened)
		// Cancellation wins between open and publication. The transaction must
		// dispose the prepared B2 and deactivate partial steering before return.
		<-ctx.Done()
		return b2, nil
	}
	result := make(chan struct {
		published bool
		err       error
	}, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		published, err := runContinuationTransaction(ctx, terminalOwner, stream, continuationIntent())
		result <- struct {
			published bool
			err       error
		}{published: published, err: err}
	})
	awaitSignal(t, started)
	schedule.Cancel.Arrive()
	cancel()
	out := <-result
	wg.Wait()
	if out.published {
		t.Fatalf("cancellation published B2: err=%v", out.err)
	}
	if !errors.Is(out.err, context.Canceled) && out.err == nil {
		t.Fatal("cancellation did not produce a bounded cancellation outcome")
	}
	if stream.attempt.snapshot() != b1 {
		t.Fatal("cancellation replaced current B1")
	}
	if authority.settleCalls.Load() != 0 {
		t.Fatalf("B1 settlement calls = %d, want zero when cancellation wins before publication", authority.settleCalls.Load())
	}
	if opened == nil || opened.cancelCalls.Load() != 1 || opened.closeCalls.Load() != 1 {
		t.Fatalf("opened B2 cleanup = %+v, want one Cancel and one Close", opened)
	}
	if store.deactivateCalls.Load() != 1 {
		t.Fatalf("partial overlay deactivation calls = %d, want one", store.deactivateCalls.Load())
	}
	if terminalOwner.requestTerminal().Owner().State() == terminal.StateOpen {
		t.Fatal("cancellation left the A-side terminal open")
	}
}

func TestContinuationTransactionDoesNotReplayCompletedToolEffects(t *testing.T) {
	terminalOwner, stream, b1, _, _ := newContinuationRedHarness(t, nil)
	completed := []aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "tool-1", ToolName: "read"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "tool-1", Delta: `{"path":"already-read"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "tool-1"},
	}
	for _, event := range completed {
		stream.responsePipeline.rememberClientEvent(event)
	}
	want := stream.responsePipeline.committedToolEventsSnapshot()
	if len(want) != len(completed) {
		t.Fatalf("completed tool evidence = %d, want %d", len(want), len(completed))
	}
	var openerCalls atomic.Int32
	stream.recovery.opener = func(_ context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		openerCalls.Add(1)
		if got := countToolEvents(req.pinnedFacts.baseline); got != 0 {
			t.Fatalf("completed tool effects were replayed into B2 input: %d tool items", got)
		}
		return continuationOpenResult(t, b1), nil
	}
	published, err := runContinuationTransaction(context.Background(), terminalOwner, stream, continuationIntent())
	if err != nil || !published {
		t.Fatalf("continuation with completed tool effects = published %v, err %v", published, err)
	}
	if got := len(stream.responsePipeline.committedToolEventsSnapshot()); got != len(want) {
		t.Fatalf("completed tool evidence count after continuation = %d, want %d", got, len(want))
	}
	if openerCalls.Load() != 1 {
		t.Fatalf("B2 opener calls = %d, want one; completed tool effect was not re-executed", openerCalls.Load())
	}
}

func TestContinuationTransactionFinalOverlayCleanupIsIdempotent(t *testing.T) {
	terminalOwner, stream, b1, _, store := newContinuationRedHarness(t, nil)
	stream.recovery.opener = func(_ context.Context, _ replacementOpenRequest) (replacementOpenResult, error) {
		return continuationOpenResult(t, b1), nil
	}
	published, err := runContinuationTransaction(context.Background(), terminalOwner, stream, continuationIntent())
	if err != nil || !published {
		t.Fatalf("continuation = published %v, err %v", published, err)
	}
	if err := deactivateContinuationOverlay(context.Background(), terminalOwner, b1.bleg.ALegID); err != nil {
		t.Fatalf("first final overlay deactivation: %v", err)
	}
	if err := deactivateContinuationOverlay(context.Background(), terminalOwner, b1.bleg.ALegID); err != nil {
		t.Fatalf("repeated final overlay deactivation: %v", err)
	}
	snap, err := store.Snapshot(context.Background(), b1.bleg.ALegID)
	if err != nil {
		t.Fatalf("conversation snapshot: %v", err)
	}
	if len(snap.Steering) != 0 {
		t.Fatalf("final continuation left active overlays: %+v", snap.Steering)
	}
	if store.deactivateCalls.Load() != 2 {
		t.Fatalf("overlay deactivation calls = %d, want exactly two idempotent calls", store.deactivateCalls.Load())
	}
}

func TestContinuationTransactionSourceUsesCanonicalWriterAndNoDirectCallAppend(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, readErr := os.ReadFile(entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), "runContinuationTransaction") {
			source.Write(data)
		}
	}
	src := source.String()
	if src == "" {
		t.Fatal("missing Task 4.2 runContinuationTransaction implementation")
	}
	if !strings.Contains(src, "sdkadapter.NewWriter") && !strings.Contains(src, "NewWriterWithObserver") && !strings.Contains(src, "steeringWriterFactory") {
		t.Fatal("continuation transaction does not use the canonical steering writer")
	}
	for _, forbidden := range []string{
		"Call.Messages = append",
		"Call.Items = append",
		"Messages = append",
		"Items = append",
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("continuation transaction directly appends hidden content with %q", forbidden)
		}
	}
}

func countToolEvents(call aipapi.Call) int {
	count := 0
	for _, item := range call.Items {
		if item.Kind == aipapi.ItemKindToolCall || item.Kind == aipapi.ItemKindToolResult {
			count++
		}
	}
	return count
}

type continuationCountingManagedStream struct {
	closeCalls  atomic.Int32
	cancelCalls atomic.Int32
}

func (s *continuationCountingManagedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, context.Canceled
	}
	<-ctx.Done()
	return aipapi.Event{}, ctx.Err()
}

func (s *continuationCountingManagedStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func (s *continuationCountingManagedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

var _ aipapi.ManagedEventStream = (*continuationCountingManagedStream)(nil)
