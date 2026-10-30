// Package storecontract holds reusable contract tests for [routeoverride.Store]
// implementations (memory, SQLite, PostgreSQL).
package storecontract

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ContractPair is one isolated store under test plus the A-leg continuity it owns.
type ContractPair struct {
	Override routeoverride.Store
	Legs     b2bua.Store
}

// ContractEnv constructs a fresh pair per subtest. SeedRevision writes an
// existing revision so overflow can be exercised without unbounded Replace
// loops. PeekLastSeenAt must read A-leg liveness without performing another
// override operation.
type ContractEnv struct {
	New            func(t *testing.T) ContractPair
	SeedRevision   func(t *testing.T, pair ContractPair, aLegID string, revision int64)
	PeekLastSeenAt func(t *testing.T, pair ContractPair, aLegID string) time.Time
	// AdvanceClock moves the adapter clock or last-seen watermark so the next
	// successful override op can prove LastSeenAt moved forward without sleeps.
	AdvanceClock func(t *testing.T, pair ContractPair, aLegID string)
	// SeedStoredState writes override state without Replace, including invalid
	// stored values used to prove snapshot failures are surfaced.
	SeedStoredState func(t *testing.T, pair ContractPair, aLegID string, st routeoverride.State)
	// Spawn starts fn concurrently. Callers must supply a test-file goroutine
	// (the shared contract package must not contain `go` statements).
	Spawn func(fn func())
}

// RunAll exercises revisioned override semantics against one adapter.
func RunAll(t *testing.T, env ContractEnv) {
	t.Helper()
	if env.New == nil {
		t.Fatal("ContractEnv.New is required")
	}

	t.Run("revision0InactiveForExistingALeg", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pair := env.New(t)
		leg, err := pair.Legs.CreateALeg(ctx, "ov-rev0")
		if err != nil {
			t.Fatal(err)
		}
		got, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if err := got.Validate(); err != nil {
			t.Fatal(err)
		}
		if got.Active || got.Selector != "" || got.Revision != 0 || !got.UpdatedAt.IsZero() {
			t.Fatalf("want revision-0 inactive, got %+v", got)
		}
		viaGet, err := pair.Override.Get(ctx, leg.ALegID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if viaGet != got {
			t.Fatalf("Get must match Snapshot: got %+v want %+v", viaGet, got)
		}
	})

	t.Run("notFoundUnknownALeg", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pair := env.New(t)
		now := time.Unix(1_700_000_000, 0).UTC()
		if _, err := pair.Override.Snapshot(ctx, "missing"); !errors.Is(err, routeoverride.ErrNotFound) {
			t.Fatalf("Snapshot: got %v want %v", err, routeoverride.ErrNotFound)
		}
		if _, err := pair.Override.Get(ctx, "missing"); !errors.Is(err, routeoverride.ErrNotFound) {
			t.Fatalf("Get: got %v want %v", err, routeoverride.ErrNotFound)
		}
		if _, err := pair.Override.Replace(ctx, "missing", "openai:gpt-4", now); !errors.Is(err, routeoverride.ErrNotFound) {
			t.Fatalf("Replace: got %v want %v", err, routeoverride.ErrNotFound)
		}
		if _, err := pair.Override.Clear(ctx, "missing", now); !errors.Is(err, routeoverride.ErrNotFound) {
			t.Fatalf("Clear: got %v want %v", err, routeoverride.ErrNotFound)
		}
	})

	t.Run("firstSetReplaceIdenticalClearRepeatedClear", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pair := env.New(t)
		leg, err := pair.Legs.CreateALeg(ctx, "ov-lifecycle")
		if err != nil {
			t.Fatal(err)
		}
		t0 := time.Unix(1_700_000_100, 0).UTC()
		t1 := t0.Add(time.Second)
		t2 := t1.Add(time.Second)
		t3 := t2.Add(time.Second)

		first, err := pair.Override.Replace(ctx, leg.ALegID, "  openai:gpt-4  ", t0)
		if err != nil {
			t.Fatalf("first Replace: %v", err)
		}
		if err := first.Validate(); err != nil {
			t.Fatal(err)
		}
		if !first.Active || first.Selector != "openai:gpt-4" || first.Revision != 1 || !first.UpdatedAt.Equal(t0) {
			t.Fatalf("first set: %+v", first)
		}

		replaced, err := pair.Override.Replace(ctx, leg.ALegID, "anthropic:claude", t1)
		if err != nil {
			t.Fatalf("replace: %v", err)
		}
		if !replaced.Active || replaced.Selector != "anthropic:claude" || replaced.Revision != 2 || !replaced.UpdatedAt.Equal(t1) {
			t.Fatalf("replace: %+v", replaced)
		}

		noop, err := pair.Override.Replace(ctx, leg.ALegID, " anthropic:claude ", t2)
		if err != nil {
			t.Fatalf("identical Replace: %v", err)
		}
		if noop.Revision != 2 || !noop.UpdatedAt.Equal(t1) || noop.Selector != "anthropic:claude" {
			t.Fatalf("identical PUT must not churn revision/updated_at: %+v", noop)
		}

		cleared, err := pair.Override.Clear(ctx, leg.ALegID, t2)
		if err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if cleared.Active || cleared.Selector != "" || cleared.Revision != 3 || !cleared.UpdatedAt.Equal(t2) {
			t.Fatalf("clear: %+v", cleared)
		}

		again, err := pair.Override.Clear(ctx, leg.ALegID, t3)
		if err != nil {
			t.Fatalf("repeated Clear: %v", err)
		}
		if again.Revision != 3 || !again.UpdatedAt.Equal(t2) || again.Active {
			t.Fatalf("repeated clear must be a no-op: %+v", again)
		}
	})

	t.Run("snapshotIsCompleteValueCopy", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pair := env.New(t)
		leg, err := pair.Legs.CreateALeg(ctx, "ov-copy")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(1_700_000_200, 0).UTC()
		if _, err := pair.Override.Replace(ctx, leg.ALegID, "openai:gpt-4", now); err != nil {
			t.Fatal(err)
		}
		got, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		got.Selector = "mutated-by-caller"
		again, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		if again.Selector != "openai:gpt-4" {
			t.Fatalf("store leaked caller mutation: %+v", again)
		}
	})

	t.Run("lastSeenAtRefreshedOnSuccessfulOps", func(t *testing.T) {
		t.Parallel()
		if env.PeekLastSeenAt == nil {
			t.Fatal("ContractEnv.PeekLastSeenAt is required to prove A-leg liveness refresh")
		}
		if env.AdvanceClock == nil {
			t.Fatal("ContractEnv.AdvanceClock is required to prove LastSeenAt moves forward")
		}
		ctx := context.Background()
		pair := env.New(t)
		leg, err := pair.Legs.CreateALeg(ctx, "ov-seen")
		if err != nil {
			t.Fatal(err)
		}
		runAndAssertMoved := func(t *testing.T, op string, fn func() error) {
			t.Helper()
			env.AdvanceClock(t, pair, leg.ALegID)
			watermark := env.PeekLastSeenAt(t, pair, leg.ALegID)
			if err := fn(); err != nil {
				t.Fatal(err)
			}
			after := env.PeekLastSeenAt(t, pair, leg.ALegID)
			if !after.After(watermark) {
				t.Fatalf("%s must move LastSeenAt forward: watermark=%v after=%v", op, watermark, after)
			}
		}
		now := time.Unix(1_700_000_300, 0).UTC()
		runAndAssertMoved(t, "Snapshot", func() error {
			_, err := pair.Override.Snapshot(ctx, leg.ALegID)
			return err
		})
		runAndAssertMoved(t, "Get", func() error {
			_, err := pair.Override.Get(ctx, leg.ALegID)
			return err
		})
		runAndAssertMoved(t, "Replace", func() error {
			_, err := pair.Override.Replace(ctx, leg.ALegID, "openai:gpt-4", now)
			return err
		})
		runAndAssertMoved(t, "identical Replace", func() error {
			_, err := pair.Override.Replace(ctx, leg.ALegID, "openai:gpt-4", now.Add(time.Second))
			return err
		})
		st, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		if st.Revision != 1 {
			t.Fatalf("idempotent Replace must not churn revision, got %d", st.Revision)
		}
		runAndAssertMoved(t, "Clear", func() error {
			_, err := pair.Override.Clear(ctx, leg.ALegID, now.Add(2*time.Second))
			return err
		})
		runAndAssertMoved(t, "repeated Clear", func() error {
			_, err := pair.Override.Clear(ctx, leg.ALegID, now.Add(3*time.Second))
			return err
		})
	})

	t.Run("aLegDeletionDoesNotLeaveOrphanOrInherit", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pair := env.New(t)
		leg, err := pair.Legs.CreateALeg(ctx, "ov-del")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(1_700_000_400, 0).UTC()
		if _, err := pair.Override.Replace(ctx, leg.ALegID, "openai:gpt-4", now); err != nil {
			t.Fatal(err)
		}
		replacement, err := pair.Legs.CreateALeg(ctx, "ov-del")
		if err != nil {
			t.Fatal(err)
		}
		if replacement.ALegID == leg.ALegID {
			t.Fatal("continuity-key recreation must allocate a new A-leg id")
		}
		if _, err := pair.Override.Snapshot(ctx, leg.ALegID); !errors.Is(err, routeoverride.ErrNotFound) {
			t.Fatalf("deleted A-leg Snapshot: got %v want %v", err, routeoverride.ErrNotFound)
		}
		got, err := pair.Override.Snapshot(ctx, replacement.ALegID)
		if err != nil {
			t.Fatalf("new A-leg Snapshot: %v", err)
		}
		if got.Active || got.Revision != 0 {
			t.Fatalf("new A-leg must not inherit override: %+v", got)
		}
	})

	t.Run("canceledContext", func(t *testing.T) {
		t.Parallel()
		pair := env.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		now := time.Unix(1_700_000_450, 0).UTC()
		if _, err := pair.Override.Snapshot(ctx, "x"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Snapshot canceled: got %v", err)
		}
		if _, err := pair.Override.Get(ctx, "x"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Get canceled: got %v", err)
		}
		if _, err := pair.Override.Replace(ctx, "x", "openai:gpt-4", now); !errors.Is(err, context.Canceled) {
			t.Fatalf("Replace canceled: got %v", err)
		}
		if _, err := pair.Override.Clear(ctx, "x", now); !errors.Is(err, context.Canceled) {
			t.Fatalf("Clear canceled: got %v", err)
		}
	})

	t.Run("concurrentWritersCompleteStatesAndMonotonicRevisions", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pair := env.New(t)
		leg, err := pair.Legs.CreateALeg(ctx, "ov-race")
		if err != nil {
			t.Fatal(err)
		}
		if env.Spawn == nil {
			t.Fatal("ContractEnv.Spawn is required for concurrent writer coverage")
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make([]routeoverride.State, 2)
		errs := make([]error, 2)
		selectors := []string{"openai:gpt-4", "anthropic:claude"}
		now := time.Unix(1_700_000_500, 0).UTC()
		for i := range 2 {
			wg.Add(1)
			env.Spawn(func() {
				defer wg.Done()
				<-start
				st, err := pair.Override.Replace(ctx, leg.ALegID, selectors[i], now.Add(time.Duration(i)*time.Millisecond))
				results[i] = st
				errs[i] = err
			})
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("writer %d: %v", i, err)
			}
			if err := results[i].Validate(); err != nil {
				t.Fatalf("writer %d state: %v", i, err)
			}
		}
		final, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		if err := final.Validate(); err != nil {
			t.Fatal(err)
		}
		if !final.Active {
			t.Fatalf("final state should be active, got %+v", final)
		}
		if final.Revision < 1 {
			t.Fatalf("expected monotonic revision >= 1, got %d", final.Revision)
		}
		winner := results[0]
		if results[1].Revision > winner.Revision {
			winner = results[1]
		}
		if final.Revision != winner.Revision || final.Selector != winner.Selector {
			t.Fatalf("highest committed revision must match latest complete state: final=%+v winner=%+v", final, winner)
		}
		if final.Selector != "openai:gpt-4" && final.Selector != "anthropic:claude" {
			t.Fatalf("torn selector: %+v", final)
		}
	})

	t.Run("deleteVersusReplaceAndClearBarriers", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		now := time.Unix(1_700_000_600, 0).UTC()

		t.Run("replaceThenDeleteCleansUp", func(t *testing.T) {
			t.Parallel()
			pair := env.New(t)
			leg, err := pair.Legs.CreateALeg(ctx, "ov-bar-1")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pair.Override.Replace(ctx, leg.ALegID, "openai:gpt-4", now); err != nil {
				t.Fatal(err)
			}
			if _, err := pair.Legs.CreateALeg(ctx, "ov-bar-1"); err != nil {
				t.Fatal(err)
			}
			if _, err := pair.Override.Snapshot(ctx, leg.ALegID); !errors.Is(err, routeoverride.ErrNotFound) {
				t.Fatalf("mutation-before-delete must be cleaned up, got %v", err)
			}
		})

		t.Run("deleteThenReplaceReturnsNotFound", func(t *testing.T) {
			t.Parallel()
			pair := env.New(t)
			leg, err := pair.Legs.CreateALeg(ctx, "ov-bar-2")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pair.Legs.CreateALeg(ctx, "ov-bar-2"); err != nil {
				t.Fatal(err)
			}
			if _, err := pair.Override.Replace(ctx, leg.ALegID, "openai:gpt-4", now); !errors.Is(err, routeoverride.ErrNotFound) {
				t.Fatalf("delete-before-mutation must return not-found, got %v", err)
			}
		})

		t.Run("deleteThenClearReturnsNotFound", func(t *testing.T) {
			t.Parallel()
			pair := env.New(t)
			leg, err := pair.Legs.CreateALeg(ctx, "ov-bar-3")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pair.Legs.CreateALeg(ctx, "ov-bar-3"); err != nil {
				t.Fatal(err)
			}
			if _, err := pair.Override.Clear(ctx, leg.ALegID, now); !errors.Is(err, routeoverride.ErrNotFound) {
				t.Fatalf("delete-before-clear must return not-found, got %v", err)
			}
		})

		t.Run("recreatedALegDoesNotInherit", func(t *testing.T) {
			t.Parallel()
			pair := env.New(t)
			leg, err := pair.Legs.CreateALeg(ctx, "ov-bar-4")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pair.Override.Replace(ctx, leg.ALegID, "openai:gpt-4", now); err != nil {
				t.Fatal(err)
			}
			fresh, err := pair.Legs.CreateALeg(ctx, "ov-bar-4")
			if err != nil {
				t.Fatal(err)
			}
			got, err := pair.Override.Snapshot(ctx, fresh.ALegID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Active || got.Revision != 0 {
				t.Fatalf("recreated A-leg inherited override: %+v", got)
			}
		})

		t.Run("concurrentReplaceVersusDelete", func(t *testing.T) {
			t.Parallel()
			runConcurrentDeleteVersusMutation(t, env, "ov-cdel-rep", now, func(ctx context.Context, pair ContractPair, aLegID string) error {
				_, err := pair.Override.Replace(ctx, aLegID, "openai:gpt-4", now)
				return err
			})
		})

		t.Run("concurrentClearVersusDelete", func(t *testing.T) {
			t.Parallel()
			runConcurrentDeleteVersusMutation(t, env, "ov-cdel-clr", now, func(ctx context.Context, pair ContractPair, aLegID string) error {
				_, err := pair.Override.Clear(ctx, aLegID, now)
				return err
			})
		})
	})

	t.Run("revisionOverflowFailsClosed", func(t *testing.T) {
		t.Parallel()
		if env.SeedRevision == nil {
			t.Fatal("ContractEnv.SeedRevision is required to prove revision overflow refusal")
		}
		ctx := context.Background()
		pair := env.New(t)
		leg, err := pair.Legs.CreateALeg(ctx, "ov-overflow")
		if err != nil {
			t.Fatal(err)
		}
		env.SeedRevision(t, pair, leg.ALegID, math.MaxInt64)
		before, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(1_700_000_700, 0).UTC()
		if _, err := pair.Override.Replace(ctx, leg.ALegID, "other:m", now); !errors.Is(err, routeoverride.ErrRevisionExhausted) {
			t.Fatalf("Replace overflow: got %v want %v", err, routeoverride.ErrRevisionExhausted)
		}
		after, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("overflow must leave state unchanged: before=%+v after=%+v", before, after)
		}
		if _, err := pair.Override.Clear(ctx, leg.ALegID, now); !errors.Is(err, routeoverride.ErrRevisionExhausted) {
			t.Fatalf("Clear overflow: got %v want %v", err, routeoverride.ErrRevisionExhausted)
		}
		unchanged, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		if unchanged != before {
			t.Fatalf("Clear overflow must leave state unchanged: before=%+v after=%+v", before, unchanged)
		}
	})

	t.Run("laterCommitIsHigherRevision", func(t *testing.T) {
		t.Parallel()
		if env.Spawn == nil {
			t.Fatal("ContractEnv.Spawn is required for sequenced writer coverage")
		}
		ctx := context.Background()
		pair := env.New(t)
		leg, err := pair.Legs.CreateALeg(ctx, "ov-later")
		if err != nil {
			t.Fatal(err)
		}
		t0 := time.Unix(1_700_000_800, 0).UTC()
		t1 := t0.Add(time.Second)
		firstDone := make(chan struct{})
		var wg sync.WaitGroup
		var first, second routeoverride.State
		var firstErr, secondErr error
		wg.Add(2)
		env.Spawn(func() {
			defer wg.Done()
			first, firstErr = pair.Override.Replace(ctx, leg.ALegID, "openai:gpt-4", t0)
			close(firstDone)
		})
		env.Spawn(func() {
			defer wg.Done()
			<-firstDone
			second, secondErr = pair.Override.Replace(ctx, leg.ALegID, "anthropic:claude", t1)
		})
		wg.Wait()
		if firstErr != nil {
			t.Fatalf("first Replace: %v", firstErr)
		}
		if secondErr != nil {
			t.Fatalf("second Replace: %v", secondErr)
		}
		if second.Revision <= first.Revision {
			t.Fatalf("later commit must have higher revision: first=%+v second=%+v", first, second)
		}
		if second.Selector != "anthropic:claude" || !second.Active {
			t.Fatalf("later committed state: %+v", second)
		}
		final, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		if final.Revision != second.Revision || final.Selector != second.Selector {
			t.Fatalf("snapshot must match later commit: final=%+v second=%+v", final, second)
		}
	})

	t.Run("invalidMutationLeavesStateUnchanged", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pair := env.New(t)
		leg, err := pair.Legs.CreateALeg(ctx, "ov-invalid")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(1_700_000_900, 0).UTC()
		want, err := pair.Override.Replace(ctx, leg.ALegID, "openai:gpt-4", now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pair.Override.Replace(ctx, leg.ALegID, "   ", now.Add(time.Second)); !errors.Is(err, routeoverride.ErrInvalidSelector) {
			t.Fatalf("empty Replace: got %v want %v", err, routeoverride.ErrInvalidSelector)
		}
		afterEmpty, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		if afterEmpty != want {
			t.Fatalf("empty Replace must not mutate: before=%+v after=%+v", want, afterEmpty)
		}
		tooLarge := strings.Repeat("a", aipapi.MaxRouteSelectorBytes+1)
		if _, err := pair.Override.Replace(ctx, leg.ALegID, tooLarge, now.Add(2*time.Second)); !errors.Is(err, routeoverride.ErrInvalidSelector) {
			t.Fatalf("oversized Replace: got %v want %v", err, routeoverride.ErrInvalidSelector)
		}
		afterLarge, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		if afterLarge != want {
			t.Fatalf("oversized Replace must not mutate: before=%+v after=%+v", want, afterLarge)
		}
	})

	t.Run("snapshotFailureIsNotConvertedToInactive", func(t *testing.T) {
		t.Parallel()
		if env.SeedStoredState == nil {
			t.Fatal("ContractEnv.SeedStoredState is required to prove snapshot failures are surfaced")
		}
		ctx := context.Background()
		pair := env.New(t)
		leg, err := pair.Legs.CreateALeg(ctx, "ov-corrupt")
		if err != nil {
			t.Fatal(err)
		}
		env.SeedStoredState(t, pair, leg.ALegID, routeoverride.State{
			ALegID:    leg.ALegID,
			Active:    true,
			Selector:  strings.Repeat("x", aipapi.MaxRouteSelectorBytes+1),
			Revision:  1,
			UpdatedAt: time.Unix(1, 0).UTC(),
		})
		got, err := pair.Override.Snapshot(ctx, leg.ALegID)
		if err == nil {
			t.Fatalf("corrupt stored selector must be surfaced, got %+v", got)
		}
		if errors.Is(err, routeoverride.ErrNotFound) {
			t.Fatal("must not convert stored-state failure to not-found")
		}
		if got.Revision == 0 && !got.Active && got.Selector == "" && err == nil {
			t.Fatal("must not convert stored-state failure to inactive")
		}
		if _, err := pair.Legs.FetchALeg(ctx, leg.ALegID); err != nil {
			t.Fatalf("A-leg should still exist after snapshot failure: %v", err)
		}
		if _, err := pair.Override.Get(ctx, leg.ALegID); err == nil {
			t.Fatal("Get must also surface stored-state failure")
		}
	})
}

func runConcurrentDeleteVersusMutation(
	t *testing.T,
	env ContractEnv,
	continuityKey string,
	now time.Time,
	mutate func(ctx context.Context, pair ContractPair, aLegID string) error,
) {
	t.Helper()
	if env.Spawn == nil {
		t.Fatal("ContractEnv.Spawn is required for concurrent delete coverage")
	}
	ctx := context.Background()
	pair := env.New(t)
	leg, err := pair.Legs.CreateALeg(ctx, continuityKey)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mutateErr, createErr error
	var replacement b2bua.ALegRecord
	wg.Add(2)
	env.Spawn(func() {
		defer wg.Done()
		<-start
		mutateErr = mutate(ctx, pair, leg.ALegID)
	})
	env.Spawn(func() {
		defer wg.Done()
		<-start
		replacement, createErr = pair.Legs.CreateALeg(ctx, continuityKey)
	})
	close(start)
	wg.Wait()
	if createErr != nil {
		t.Fatalf("recreate: %v", createErr)
	}
	if mutateErr != nil && !errors.Is(mutateErr, routeoverride.ErrNotFound) {
		t.Fatalf("mutation race: %v", mutateErr)
	}
	if _, err := pair.Override.Snapshot(ctx, leg.ALegID); !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("old A-leg must be gone after recreate, got %v", err)
	}
	got, err := pair.Override.Snapshot(ctx, replacement.ALegID)
	if err != nil {
		t.Fatalf("new A-leg Snapshot: %v", err)
	}
	if got.Active || got.Revision != 0 {
		t.Fatalf("new A-leg must not inherit override: %+v", got)
	}
}
