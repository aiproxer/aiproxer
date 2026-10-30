package b2buatest

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
)

// Store is the minimal contract for interleaved-state conformance: A-leg continuity
// plus interleaved state persistence.
type Store interface {
	b2bua.Store
	b2bua.InterleavedStateStore
}

// TestInterleavedStateStore exercises round-trip, empty-state, unknown A-leg, and
// invalid-state rejection. newStore is called from each subtest so construction
// failures and t.Cleanup hooks are attributed to the correct subtest.
func TestInterleavedStateStore(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("roundTrip", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		leg, err := s.CreateALeg(ctx, "thinker-session")
		if err != nil {
			t.Fatal(err)
		}
		want := interleavedstate.State{
			Cycle: interleavedstate.CycleState{
				SelectorKey: "selector-v1",
				Sequence: []interleavedstate.CycleEntry{
					{Key: "exec-a", Role: interleavedstate.RoleExecutor},
					{Key: "thinker", Role: interleavedstate.RoleThinker},
				},
				NextIndex: 1,
			},
		}
		if err := s.SetInterleavedState(ctx, leg.ALegID, want); err != nil {
			t.Fatalf("SetInterleavedState: %v", err)
		}
		got, err := s.FetchInterleavedState(ctx, leg.ALegID)
		if err != nil {
			t.Fatalf("FetchInterleavedState: %v", err)
		}
		if !got.Equal(want) {
			t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
		}
		if got.IsEmpty() {
			t.Fatal("populated state must not report empty")
		}
	})

	t.Run("zeroValueIsHarmless", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		const continuityKey = "non-thinker-session"
		leg, err := s.CreateALeg(ctx, continuityKey)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.FetchInterleavedState(ctx, leg.ALegID)
		if err != nil {
			t.Fatalf("FetchInterleavedState on never-set leg: %v", err)
		}
		if !got.IsEmpty() {
			t.Fatalf("never-set state must be empty, got %+v", got)
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("empty state must validate: %v", err)
		}
		if err := s.SetInterleavedState(ctx, leg.ALegID, interleavedstate.State{}); err != nil {
			t.Fatalf("SetInterleavedState empty: %v", err)
		}
		got, err = s.FetchInterleavedState(ctx, leg.ALegID)
		if err != nil {
			t.Fatalf("FetchInterleavedState after empty set: %v", err)
		}
		if !got.IsEmpty() {
			t.Fatalf("empty set must round-trip as empty, got %+v", got)
		}
		resolved, err := s.ResolveALeg(ctx, continuityKey)
		if err != nil {
			t.Fatalf("ResolveALeg: %v", err)
		}
		if resolved.ALegID != leg.ALegID {
			t.Fatalf("ResolveALeg id: got %q want %q", resolved.ALegID, leg.ALegID)
		}
	})

	t.Run("unknownALeg", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		if _, err := s.FetchInterleavedState(ctx, "missing"); !errors.Is(err, b2bua.ErrALegNotFound) {
			t.Fatalf("Fetch unknown: got %v want %v", err, b2bua.ErrALegNotFound)
		}
		if err := s.SetInterleavedState(ctx, "missing", interleavedstate.State{}); !errors.Is(err, b2bua.ErrALegNotFound) {
			t.Fatalf("Set unknown: got %v want %v", err, b2bua.ErrALegNotFound)
		}
	})

	t.Run("rejectsInvalidState", func(t *testing.T) {
		t.Parallel()
		s := newStore(t)
		leg, err := s.CreateALeg(ctx, "k")
		if err != nil {
			t.Fatal(err)
		}
		bad := interleavedstate.State{
			Cycle: interleavedstate.CycleState{
				SelectorKey: "k",
				Sequence:    []interleavedstate.CycleEntry{{Key: "a"}},
				NextIndex:   5,
			},
		}
		if err := s.SetInterleavedState(ctx, leg.ALegID, bad); err == nil {
			t.Fatal("expected error for out-of-bounds cursor")
		}
		got, err := s.FetchInterleavedState(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.IsEmpty() {
			t.Fatalf("rejected set must not mutate stored state, got %+v", got)
		}
	})
}
