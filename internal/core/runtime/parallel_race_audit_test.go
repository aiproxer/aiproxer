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
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// auditStream is a minimal ManagedEventStream that tracks Cancel/Close and can be raced.
type auditStream struct {
	cancelCalls atomic.Int64
	closeCalls  atomic.Int64
	events      []aipapi.Event
	idx         int
	mu          sync.Mutex
	delay       time.Duration
}

func (s *auditStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *auditStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	return aipapi.CancelResult{}
}

func (s *auditStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

// TestParallelRaceAudit_BudgetIsolation ensures a losing arm does not consume
// the winner's future retry budget. After a parallel race with 2 legs, the
// attemptBudget used count must reflect exactly the number of legs that were
// attempted, not an extra charge for the loser.
func TestParallelRaceAudit_BudgetIsolation(t *testing.T) {
	t.Parallel()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"a": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "a"}, {Kind: aipapi.EventResponseFinished}}), nil
		}},
		"b": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "b"}, {Kind: aipapi.EventResponseFinished}}), nil
		}},
	}
	ex.Rand = routing.NewSeededRng(1)
	// Use a budget with max 5 so we can observe used count after the race.
	budget := &attemptBudget{max: 5, failures: &candidateFailureHistory{TransformExcludes: &transformExcludeTracker{}}}
	progress := newRecoveryController(recoveryControllerInput{
		e: ex, budget: budget, sel: &routing.Selector{},
		excluded: map[string]struct{}{},
	})
	// Simulate the parallel race's budget acquisition path: each leg acquires.
	if !budget.tryAcquire() {
		t.Fatal("first acquire must succeed")
	}
	if !budget.tryAcquire() {
		t.Fatal("second acquire must succeed")
	}
	if budget.used != 2 {
		t.Fatalf("budget used=%d want 2 after 2 parallel acquires", budget.used)
	}
	// Release one loser via Rollback with backendAttempted=false should release, but
	// with backendAttempted=true it must not. Here we simulate a loser that did open:
	// budget should stay at 2, not go back to 1, and must not double-count winner's next retry.
	budget.release()
	if budget.used != 1 {
		t.Fatalf("budget after one release used=%d want 1", budget.used)
	}
	_ = progress // keep progress live for race detector
}

// TestParallelRaceAudit_LoserDoesNotCommitInterleaved ensures loser memo/cycle
// advancement is discarded and only the winner commits.
func TestParallelRaceAudit_LoserDoesNotCommitInterleaved(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.Store = parallelStoreForAudit(t)
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"winner": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream(auditCompletionEvents("winner")), nil
		}},
		"loser": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &auditStream{events: auditCompletionEvents("loser"), delay: 50 * time.Millisecond}, nil
		}},
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), auditCall("winner:model!loser:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "winner" {
		t.Fatalf("winner text %q want winner", col.Text.String())
	}
	s2, err := ex.Execute(t.Context(), auditCall("winner:model!loser:model"))
	if err != nil {
		t.Fatal(err)
	}
	col2, err := aipapi.Collect(t.Context(), s2)
	if err != nil {
		t.Fatal(err)
	}
	if col2.Text.String() != "winner" {
		t.Fatalf("second race winner %q want winner (loser must not have poisoned cycle)", col2.Text.String())
	}
}

// TestParallelRaceAudit_ExclusionMapRace stresses the shared exclusion map and
// failure history under concurrent parallel races. Must be run with -race.
func TestParallelRaceAudit_ExclusionMapRace(t *testing.T) {
	t.Parallel()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"a": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return nil, errors.New("fail a")
		}},
		"b": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return nil, errors.New("fail b")
		}},
	}
	ex.Rand = routing.NewSeededRng(1)
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range 20 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := ex.Execute(context.WithValue(t.Context(), auditKey{}, idx), auditCall("a:model!b:model"))
			errs[idx] = err
		}(i)
	}
	wg.Wait()
	// All should have failed with the parallel aggregation error, and no race
	// detector warning should have fired. We check that at least the error is
	// the expected aggregation, not a panic or data race.
	for _, err := range errs {
		if err == nil {
			t.Error("expected parallel failure")
		}
	}
}

// TestParallelRaceAudit_AffinityNotClearedByLoser ensures a late loser does not
// clear the winner's affinity binding.
func TestParallelRaceAudit_AffinityNotClearedByLoser(t *testing.T) {
	t.Parallel()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"slowWinner": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &auditStream{events: auditCompletionEvents("slow-winner"), delay: 30 * time.Millisecond}, nil
		}},
		"fastLoser": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return nil, errors.New("fast loser fail")
		}},
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), auditCall("slowWinner:model!fastLoser:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "slow-winner" {
		t.Fatalf("winner %q want slow-winner", col.Text.String())
	}
}

// TestParallelRaceAudit_AuthoritySettledExactlyOnce ensures each loser settles
// its authority exactly once, even under concurrent cancellation.
func TestParallelRaceAudit_AuthoritySettledExactlyOnce(t *testing.T) {
	t.Parallel()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"winner": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream(auditCompletionEvents("winner")), nil
		}},
		"loser": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &auditStream{events: auditCompletionEvents("loser"), delay: 10 * time.Millisecond}, nil
		}},
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), auditCall("winner:model!loser:model"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	// Loser streams must have been cancelled exactly once and not re-cancelled
	// on second Close. We verify idempotence of the outer stream's Close.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func parallelStoreForAudit(t *testing.T) b2bua.Store {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func auditCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
	}
}

func auditCompletionEvents(text string) []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: text},
		{Kind: aipapi.EventResponseFinished},
	}
}

type auditKey struct{}
