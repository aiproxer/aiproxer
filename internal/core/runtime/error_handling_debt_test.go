package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func debtTerminalSnapshot() coreterm.AccumulatorSnapshot {
	return coreterm.NewAccumulatorSnapshot(nil, false)
}

func debtTerminalizeClose(term *streamTerminal, effects func(context.Context, coreterm.Outcome) error) coreterm.Result {
	return term.Terminalize(context.Background(), sdk.CommandClose, debtTerminalSnapshot, effects)
}

// debtFailAdvanceEffect forces the owner into failed before returning, so the
// winner's post-effect state advance is the step that fails.
func debtFailAdvanceEffect(term *streamTerminal, effectErr error) func(context.Context, coreterm.Outcome) error {
	return func(context.Context, coreterm.Outcome) error {
		if err := term.Owner().Advance(sdk.StateFailed); err != nil {
			return errors.Join(effectErr, err)
		}
		return effectErr
	}
}

// TestStreamTerminal_StateAdvanceFailureSharedWithWaiters proves the once-only
// outcome is shared with a caller that is already waiting: a competing
// Terminalize must observe the state-advance failure the winner surfaced
// instead of reporting success from a clean effect.
func TestStreamTerminal_StateAdvanceFailureSharedWithWaiters(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdk.ScopeRequest)
	entered := make(chan struct{})
	release := make(chan struct{})

	var winner coreterm.Result
	var winnerWG sync.WaitGroup
	winnerWG.Go(func() {
		winner = debtTerminalizeClose(term, func(context.Context, coreterm.Outcome) error {
			close(entered)
			<-release
			return term.Owner().Advance(sdk.StateFailed)
		})
	})
	<-entered

	// Park the competitor on t.done before the winner can publish, so the test
	// exercises the wait path rather than a channel that is already closed.
	var loser coreterm.Result
	loserDone := make(chan struct{})
	go func() {
		defer close(loserDone)
		loser = debtTerminalizeClose(term, func(context.Context, coreterm.Outcome) error {
			t.Error("loser must not run terminal effects")
			return nil
		})
	}()
	select {
	case <-loserDone:
		t.Fatal("loser returned before the winner published its result")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	winnerWG.Wait()
	<-loserDone

	if !winner.Won || loser.Won {
		t.Fatalf("winner=%+v loser=%+v", winner, loser)
	}
	for name, r := range map[string]coreterm.Result{"winner": winner, "loser": loser} {
		if !errors.Is(r.Err, sdk.ErrInvalidTransition) {
			t.Fatalf("%s err = %v, want ErrInvalidTransition", name, r.Err)
		}
		if r.State != sdk.StateFailed {
			t.Fatalf("%s state = %q, want failed", name, r.State)
		}
	}
}

// TestStreamTerminal_DurablePendingAdvanceFailureSharedWithWaiters proves both
// callers keep the durable-pending classification and the transition failure.
func TestStreamTerminal_DurablePendingAdvanceFailureSharedWithWaiters(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdk.ScopeRequest)
	winner := debtTerminalizeClose(term, debtFailAdvanceEffect(term, terminalworkapp.ErrDurablePending))
	if !winner.Won {
		t.Fatalf("winner claim: %+v", winner)
	}
	loser := debtTerminalizeClose(term, func(context.Context, coreterm.Outcome) error {
		t.Error("loser must not run terminal effects")
		return nil
	})
	if loser.Won {
		t.Fatalf("loser won a claimed terminal: %+v", loser)
	}

	for name, r := range map[string]coreterm.Result{"winner": winner, "loser": loser} {
		if !errors.Is(r.Err, terminalworkapp.ErrDurablePending) {
			t.Fatalf("%s err = %v, want ErrDurablePending preserved", name, r.Err)
		}
		if !errors.Is(r.Err, sdk.ErrInvalidTransition) {
			t.Fatalf("%s err = %v, want ErrInvalidTransition preserved", name, r.Err)
		}
		if r.State != sdk.StateFailed {
			t.Fatalf("%s state = %q, want failed", name, r.State)
		}
	}
}

// TestStreamTerminal_EffectPanicSharedWithWaiter proves a recovered terminal
// effect panic reaches the caller that awaited the winner.
func TestStreamTerminal_EffectPanicSharedWithWaiter(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdk.ScopeRequest)
	entered := make(chan struct{})
	release := make(chan struct{})

	var winner coreterm.Result
	var winnerWG sync.WaitGroup
	winnerWG.Go(func() {
		winner = debtTerminalizeClose(term, func(context.Context, coreterm.Outcome) error {
			close(entered)
			<-release
			panic("effect boom")
		})
	})
	<-entered

	// Park the competitor on t.done before the winner publishes, so a
	// regression that releases waiters before storing the result is observable.
	var loser coreterm.Result
	loserDone := make(chan struct{})
	go func() {
		defer close(loserDone)
		loser = debtTerminalizeClose(term, func(context.Context, coreterm.Outcome) error {
			t.Error("loser must not run terminal effects")
			return nil
		})
	}()
	select {
	case <-loserDone:
		t.Fatal("loser returned before the winner published its result")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	winnerWG.Wait()
	<-loserDone

	if !winner.Won || loser.Won {
		t.Fatalf("winner=%+v loser=%+v", winner, loser)
	}
	if winner.Err == nil {
		t.Fatal("winner must report the recovered panic")
	}
	if loser.Err != winner.Err {
		t.Fatalf("loser err = %v, want the winner's published %v", loser.Err, winner.Err)
	}
	for name, r := range map[string]coreterm.Result{"winner": winner, "loser": loser} {
		if r.State != sdk.StateFailed {
			t.Fatalf("%s state = %q, want failed", name, r.State)
		}
	}
}

// TestStreamTerminal_AdvanceFailureEffectsRunOnce proves the shared failure
// does not make competing callers re-run the terminal effects.
func TestStreamTerminal_AdvanceFailureEffectsRunOnce(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdk.ScopeRequest)
	const callers = 8
	var effects, winners atomic.Int32
	results := make([]coreterm.Result, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			<-start
			results[i] = debtTerminalizeClose(term, func(context.Context, coreterm.Outcome) error {
				effects.Add(1)
				return term.Owner().Advance(sdk.StateFailed)
			})
			if results[i].Won {
				winners.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()

	if effects.Load() != 1 {
		t.Fatalf("effects = %d, want 1", effects.Load())
	}
	if winners.Load() != 1 {
		t.Fatalf("winners = %d, want 1", winners.Load())
	}
	for i, r := range results {
		if !errors.Is(r.Err, sdk.ErrInvalidTransition) {
			t.Fatalf("result[%d] err = %v, want ErrInvalidTransition", i, r.Err)
		}
		if r.State != sdk.StateFailed {
			t.Fatalf("result[%d] state = %q, want failed", i, r.State)
		}
	}
}

type fetchErrorB2BUAStore struct {
	b2bua.Store
	fetchErr  error
	createErr error
	creates   atomic.Int32
}

func (s *fetchErrorB2BUAStore) FetchALeg(context.Context, string) (b2bua.ALegRecord, error) {
	return b2bua.ALegRecord{}, s.fetchErr
}

func (s *fetchErrorB2BUAStore) CreateALeg(context.Context, string) (b2bua.ALegRecord, error) {
	s.creates.Add(1)
	return b2bua.ALegRecord{}, s.createErr
}

// TestPrepareDetached_FetchALegFailureIsNotMasked proves only ErrALegNotFound
// may fall back to creating a fresh A-leg.
func TestPrepareDetached_FetchALegFailureIsNotMasked(t *testing.T) {
	t.Parallel()
	base, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fetchErr := errors.New("continuity store unavailable")
	store := &fetchErrorB2BUAStore{Store: base, fetchErr: fetchErr, createErr: errors.New("create must not be reached")}
	ex := TestExecutor()
	ex.Store = store
	call := &aipapi.Call{
		Session:  aipapi.SessionRef{ALegID: "existing-a-leg"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("detached")}}},
	}

	_, _, _, err = ex.prepareSubmitAndALegDetached(context.Background(), nil, call)
	if !errors.Is(err, fetchErr) {
		t.Fatalf("err = %v, want the A-leg load failure", err)
	}
	if creates := store.creates.Load(); creates != 0 {
		t.Fatalf("CreateALeg calls = %d, want 0 after a non-not-found load failure", creates)
	}
}
