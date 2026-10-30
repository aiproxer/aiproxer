package conversationview_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	cv "github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

func bootstrapPut(id, text string) cv.PutSteeringRequest {
	return cv.PutSteeringRequest{OverlayID: id, Message: cv.StoredMessageV1{Role: aipapi.RoleSystem, Text: text}, Placement: cv.StoredPlacement{Kind: cv.PlacementStablePrefix}, AnchorMissingPolicy: cv.AnchorStablePrefixFallback, Reason: "bootstrap"}
}

func runBootstrapContract(t *testing.T, store cv.Store, atomic cv.BootstrapStore, id string, allocate func() error, retire func() error) {
	t.Helper()
	ctx := t.Context()
	calls := 0
	decide := func() (cv.BootstrapDecision, error) {
		calls++
		return cv.BootstrapDecision{Outcome: cv.BootstrapMatched, Model: "logical", Overlays: []cv.PutSteeringRequest{bootstrapPut("owner.one", "one"), bootstrapPut("owner.two", "two")}}, nil
	}
	out, err := atomic.BootstrapSteering(ctx, id, "owner", decide)
	require.NoError(t, err)
	require.False(t, out.Reused)
	require.Equal(t, 2, out.Completion.MatchedCount)
	require.Len(t, out.Mutations, 2)
	require.EqualValues(t, 1, out.Mutations[0].SlotOrdinal)
	require.EqualValues(t, 2, out.Mutations[1].SlotOrdinal)
	require.EqualValues(t, 2, out.Mutations[1].StateRevision)
	require.NoError(t, allocate())
	out, err = atomic.BootstrapSteering(ctx, id, "owner", decide)
	require.NoError(t, err)
	require.True(t, out.Reused)
	require.Equal(t, cv.BootstrapMatched, out.Completion.Outcome)
	require.Empty(t, out.Mutations)
	require.Equal(t, 1, calls)
	out, err = atomic.BootstrapSteering(ctx, id, "other", decide)
	require.NoError(t, err)
	require.Equal(t, cv.BootstrapPreexistingSkip, out.Completion.Outcome)
	require.Equal(t, 1, calls)
	snap, err := store.Snapshot(ctx, id)
	require.NoError(t, err)
	require.EqualValues(t, 2, snap.StateRevision)
	require.Len(t, snap.Steering, 2)
	require.Equal(t, "one", snap.Steering[0].Message.Text)
	require.Equal(t, "two", snap.Steering[1].Message.Text)
	require.NoError(t, retire())
	_, err = atomic.BootstrapSteering(ctx, id, "owner", decide)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

func TestBootstrap_MemoryOrderedReuseAndAllocationHistory(t *testing.T) {
	t.Parallel()
	authority, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	leg, err := authority.CreateALeg(t.Context(), "")
	require.NoError(t, err)
	store := cv.NewReferenceStore()
	require.NoError(t, store.CreateALeg(t.Context(), leg.ALegID))
	atomic := cv.NewAuthorizedReferenceStore(store, authority)
	runBootstrapContract(t, store, atomic, leg.ALegID, func() error { _, err := authority.NextBLeg(t.Context(), leg.ALegID); return err }, func() error { return store.DeleteALeg(t.Context(), leg.ALegID) })
}

func TestBootstrap_MemoryCompletionLivesUntilAuthoritativeRetirement(t *testing.T) {
	t.Parallel()
	authority, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{MaxLegs: 2})
	require.NoError(t, err)
	leg, err := authority.CreateALeg(t.Context(), "lifetime")
	require.NoError(t, err)
	now := time.Unix(1, 0)
	store := cv.NewReferenceStoreWithClock(func() time.Time { return now })
	store.SetMaxLegs(1)
	require.NoError(t, store.CreateALeg(t.Context(), leg.ALegID))
	authority.SetALegRetirementObserver(func(id string) { require.NoError(t, store.DeleteALeg(context.Background(), id)) })
	atomic := cv.NewAuthorizedReferenceStore(store, authority)
	out, err := atomic.BootstrapSteering(t.Context(), leg.ALegID, "owner", func() (cv.BootstrapDecision, error) {
		return cv.BootstrapDecision{Outcome: cv.BootstrapMatched, Overlays: []cv.PutSteeringRequest{bootstrapPut("owner.one", "one")}}, nil
	})
	require.NoError(t, err)
	require.False(t, out.Reused)
	now = now.Add(time.Second)
	require.NoError(t, store.CreateALeg(t.Context(), "unmarked-view"))
	out, err = atomic.BootstrapSteering(t.Context(), leg.ALegID, "owner", nil)
	require.NoError(t, err)
	require.True(t, out.Reused)
	snap, err := store.Snapshot(t.Context(), leg.ALegID)
	require.NoError(t, err)
	require.Len(t, snap.Steering, 1)
	_, err = authority.CreateALeg(t.Context(), "lifetime")
	require.NoError(t, err)
	_, err = store.Snapshot(t.Context(), leg.ALegID)
	require.ErrorIs(t, err, cv.ErrALegNotFound)
}

func TestBootstrap_MemoryConcurrentFirstTurnsHaveOneDecision(t *testing.T) {
	t.Parallel()
	authority, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	leg, err := authority.CreateALeg(t.Context(), "")
	require.NoError(t, err)
	store := cv.NewReferenceStore()
	require.NoError(t, store.CreateALeg(t.Context(), leg.ALegID))
	a, b := cv.NewAuthorizedReferenceStore(store, authority), cv.NewAuthorizedReferenceStore(store, authority)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, release, started := make(chan struct{}), make(chan struct{}), make(chan struct{})
	type reply struct {
		result cv.BootstrapResult
		err    error
	}
	first, second := make(chan reply, 1), make(chan reply, 1)
	go func() {
		out, e := a.BootstrapSteering(ctx, leg.ALegID, "owner", func() (cv.BootstrapDecision, error) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return cv.BootstrapDecision{Outcome: cv.BootstrapMatched, Overlays: []cv.PutSteeringRequest{bootstrapPut("owner.one", "one")}}, nil
		})
		first <- reply{out, e}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		close(started)
		out, e := b.BootstrapSteering(ctx, leg.ALegID, "owner", func() (cv.BootstrapDecision, error) { return cv.BootstrapDecision{Outcome: cv.BootstrapNoMatch}, nil })
		second <- reply{out, e}
	}()
	<-started
	close(release)
	winner, loser := <-first, <-second
	require.NoError(t, winner.err)
	require.NoError(t, loser.err)
	require.False(t, winner.result.Reused)
	require.True(t, loser.result.Reused)
	require.Equal(t, winner.result.Completion, loser.result.Completion)
	require.Empty(t, loser.result.Mutations)
}

func TestBootstrap_SQLiteOrderedReuseAndAllocationHistory(t *testing.T) {
	t.Parallel()
	store, closeDB := newTestBunStore(t)
	defer closeDB()
	id := "bootstrap-sqlite"
	require.NoError(t, store.CreateALeg(t.Context(), id))
	runBootstrapContract(t, store, store, id, func() error {
		_, err := store.DB().NewRaw(`UPDATE a_legs SET next_b_seq = next_b_seq + 1 WHERE a_leg_id = ?`, id).Exec(t.Context())
		return err
	}, func() error { return store.DeleteALeg(t.Context(), id) })
}

func TestBootstrap_EmptyAndInvalidBatchAreAtomic(t *testing.T) {
	t.Parallel()
	for _, memory := range []bool{true, false} {
		t.Run(map[bool]string{true: "memory", false: "sqlite"}[memory], func(t *testing.T) {
			var store cv.Store
			var atomic cv.BootstrapStore
			id := "bootstrap-batch"
			if memory {
				a, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
				require.NoError(t, err)
				leg, err := a.CreateALeg(t.Context(), "")
				require.NoError(t, err)
				id = leg.ALegID
				s := cv.NewReferenceStore()
				require.NoError(t, s.CreateALeg(t.Context(), id))
				store, atomic = s, cv.NewAuthorizedReferenceStore(s, a)
			} else {
				s, cleanup := newTestBunStore(t)
				t.Cleanup(cleanup)
				require.NoError(t, s.CreateALeg(t.Context(), id))
				store, atomic = s, s
			}
			for _, outcome := range []cv.BootstrapOutcome{cv.BootstrapNoMatch, cv.BootstrapAmbiguousSkip} {
				owner := string(outcome)
				out, err := atomic.BootstrapSteering(t.Context(), id, owner, func() (cv.BootstrapDecision, error) { return cv.BootstrapDecision{Outcome: outcome}, nil })
				require.NoError(t, err)
				require.Empty(t, out.Mutations)
				out, err = atomic.BootstrapSteering(t.Context(), id, owner, func() (cv.BootstrapDecision, error) { t.Error("reused callback"); return cv.BootstrapDecision{}, nil })
				require.NoError(t, err)
				require.True(t, out.Reused)
			}
			invalid := []struct {
				name  string
				batch []cv.PutSteeringRequest
			}{
				{"utf8", []cv.PutSteeringRequest{bootstrapPut("owner.one", "one"), bootstrapPut("owner.two", "\xff")}},
				{"duplicate", []cv.PutSteeringRequest{bootstrapPut("owner.one", "one"), bootstrapPut("owner.one", "two")}},
				{"capacity", []cv.PutSteeringRequest{bootstrapPut("owner.one", strings.Repeat("x", cv.MaxSteeringTextBytes+1))}},
			}
			for _, tc := range invalid {
				out, err := atomic.BootstrapSteering(t.Context(), id, "owner", func() (cv.BootstrapDecision, error) {
					return cv.BootstrapDecision{Outcome: cv.BootstrapMatched, Overlays: tc.batch}, nil
				})
				require.Error(t, err, tc.name)
				require.Empty(t, out.Mutations)
				snap, err := store.Snapshot(t.Context(), id)
				require.NoError(t, err)
				require.Zero(t, snap.StateRevision)
				require.Empty(t, snap.Steering)
			}
			ctx, cancel := context.WithCancel(t.Context())
			out, err := atomic.BootstrapSteering(ctx, id, "owner", func() (cv.BootstrapDecision, error) {
				cancel()
				return cv.BootstrapDecision{Outcome: cv.BootstrapMatched, Overlays: []cv.PutSteeringRequest{bootstrapPut("owner.one", "one")}}, nil
			})
			require.ErrorIs(t, err, context.Canceled)
			require.Empty(t, out.Mutations)
			out, err = atomic.BootstrapSteering(t.Context(), id, "owner", func() (cv.BootstrapDecision, error) { return cv.BootstrapDecision{}, errors.New("PRIVATE PROMPT") })
			require.Error(t, err)
			require.NotContains(t, err.Error(), "PRIVATE PROMPT")
			require.Empty(t, out.Mutations)
			out, err = atomic.BootstrapSteering(t.Context(), id, "owner", func() (cv.BootstrapDecision, error) {
				return cv.BootstrapDecision{Outcome: cv.BootstrapMatched, Overlays: []cv.PutSteeringRequest{bootstrapPut("owner.one", "one")}}, nil
			})
			require.NoError(t, err)
			require.EqualValues(t, 1, out.Mutations[0].SlotOrdinal)
			require.EqualValues(t, 1, out.Mutations[0].StateRevision)
		})
	}
}
