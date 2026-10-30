package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Phase 4.2 RED/GREEN: stream terminal session — winner executes effects once;
// losers await the same completion (requirements 7.1–7.8, 13.4, 13.7; D8, D13, D17).

func TestStreamTerminal_ConcurrentClaim_EffectsOnce(t *testing.T) {
	t.Parallel()
	const rounds = 32
	for i := range rounds {
		term := newStreamTerminal(sdk.ScopeRequest)
		var effects atomic.Int32
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make([]coreterm.Result, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			results[0] = term.Terminalize(context.Background(), sdk.CommandClose, func() coreterm.AccumulatorSnapshot {
				return coreterm.NewAccumulatorSnapshot([]byte("close"), false)
			}, func(context.Context, coreterm.Outcome) error {
				effects.Add(1)
				time.Sleep(2 * time.Millisecond)
				return nil
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			results[1] = term.Terminalize(context.Background(), sdk.CommandEOF, func() coreterm.AccumulatorSnapshot {
				return coreterm.NewAccumulatorSnapshot([]byte("recv"), true)
			}, func(context.Context, coreterm.Outcome) error {
				effects.Add(1)
				time.Sleep(2 * time.Millisecond)
				return nil
			})
		}()
		close(start)
		wg.Wait()

		if effects.Load() != 1 {
			t.Fatalf("iter %d: effects=%d want 1", i, effects.Load())
		}
		winners := 0
		var winner coreterm.Result
		for _, r := range results {
			if r.Won {
				winners++
				winner = r
			}
		}
		if winners != 1 {
			t.Fatalf("iter %d: winners=%d results=%+v", i, winners, results)
		}
		for _, r := range results {
			if !r.Outcome.Snapshot.Equal(winner.Outcome.Snapshot) {
				t.Fatalf("iter %d: divergent snapshots", i)
			}
			if r.Outcome.Command != winner.Outcome.Command {
				t.Fatalf("iter %d: divergent commands", i)
			}
		}
		if !term.Owner().State().IsTerminal() {
			t.Fatalf("iter %d: state=%q want released/failed", i, term.Owner().State())
		}
	}
}

func TestStreamTerminal_LoserAwaitsWinnerCompletion(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdk.ScopeRequest)
	entered := make(chan struct{})
	release := make(chan struct{})
	var order []string
	var mu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = term.Terminalize(context.Background(), sdk.CommandCancel, func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot([]byte("win"), true)
		}, func(context.Context, coreterm.Outcome) error {
			close(entered)
			<-release
			mu.Lock()
			order = append(order, "winner-done")
			mu.Unlock()
			return nil
		})
	}()
	<-entered
	go func() {
		defer wg.Done()
		r := term.Terminalize(context.Background(), sdk.CommandClose, func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot([]byte("lose"), false)
		}, func(context.Context, coreterm.Outcome) error {
			mu.Lock()
			order = append(order, "loser-effects")
			mu.Unlock()
			return nil
		})
		if r.Won {
			t.Error("loser must not win")
		}
		mu.Lock()
		order = append(order, "loser-returned")
		mu.Unlock()
	}()
	select {
	case <-time.After(20 * time.Millisecond):
	case <-func() chan struct{} {
		ch := make(chan struct{})
		go func() {
			wg.Wait()
			close(ch)
		}()
		return ch
	}():
		t.Fatal("loser returned before winner finished")
	}
	mu.Lock()
	if len(order) != 0 {
		t.Fatalf("loser returned before winner finished: %v", order)
	}
	mu.Unlock()
	close(release)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 || order[0] != "winner-done" || order[len(order)-1] != "loser-returned" {
		t.Fatalf("order=%v", order)
	}
	for _, s := range order {
		if s == "loser-effects" {
			t.Fatal("loser must not run effects")
		}
	}
}

func TestStreamTerminal_OutputCommittedBlocksGateReplacement(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdk.ScopeRequest)
	r := term.Terminalize(context.Background(), sdk.CommandGateReplacement, func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot([]byte("x"), true)
	}, func(context.Context, coreterm.Outcome) error {
		t.Fatal("effects must not run when output committed")
		return nil
	})
	if r.Won || !errors.Is(r.Err, sdk.ErrOutputCommitted) {
		t.Fatalf("got %+v", r)
	}
	if term.Owner().State() != sdk.StateOpen {
		t.Fatalf("state=%q", term.Owner().State())
	}
}

func TestStreamTerminal_CleanupContextDetachedFromCancel(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdk.ScopeAttempt)
	parent, cancel := context.WithCancel(context.Background())
	var saw error
	r := term.Terminalize(parent, sdk.CommandParallelLoser, func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot([]byte("a"), false)
	}, func(ctx context.Context, _ coreterm.Outcome) error {
		cancel()
		saw = ctx.Err()
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.After(time.Now()) {
			t.Errorf("cleanup ctx must be bounded, ok=%v deadline=%v", ok, deadline)
		}
		return nil
	})
	if !r.Won {
		t.Fatalf("claim: %+v", r)
	}
	if saw != nil {
		t.Fatalf("detached cleanup ctx must not inherit cancel: %v", saw)
	}
}

func TestStreamTerminal_PanicAdvancesFailed(t *testing.T) {
	t.Parallel()
	term := newStreamTerminal(sdk.ScopeRequest)
	r := term.Terminalize(context.Background(), sdk.CommandPanic, func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}, func(context.Context, coreterm.Outcome) error { return nil })
	if !r.Won {
		t.Fatalf("%+v", r)
	}
	if term.Owner().State() != sdk.StateFailed {
		t.Fatalf("state=%q want failed", term.Owner().State())
	}
}
