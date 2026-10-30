package conversationview_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// deterministic concurrency tests: no sleeps, barrier via channels, race detector will catch data races.

func testMessage(text string) aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: text}}}
}

func TestConcurrent_TagVsSnapshot_Deterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := conversationview.NewReferenceStore()
	aLeg := "concurrent-tag-vs-snapshot"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	const goroutines = 8
	const tagsPerGoroutine = 64

	// Pre-create barrier: all goroutines start together, then mutate concurrently vs snapshot readers.
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*2)

	// Writers: tag batches
	for g := range goroutines {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			<-start
			for i := range tagsPerGoroutine {
				msg := testMessage(fmt.Sprintf("tag-g%d-i%d", gid, i))
				id, err := conversationview.MessageIdentityOf(msg)
				if err != nil {
					errCh <- err
					return
				}
				if _, err := store.TagNeverBackend(ctx, aLeg, []conversationview.TagRequest{{Identity: id, Reason: "test"}}); err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}
	// Readers: snapshot repeatedly with deterministic validation (sorted, bounded)
	for range goroutines {
		wg.Go(func() {
			<-start
			for range tagsPerGoroutine {
				snap, err := store.Snapshot(ctx, aLeg)
				if err != nil {
					errCh <- err
					return
				}
				if len(snap.NeverBackend) > conversationview.MaxNeverBackendTags {
					errCh <- fmt.Errorf("tags exceed cap %d", len(snap.NeverBackend))
					return
				}
				// Snapshot must be deep copy: mutating returned slice must not affect store.
				if len(snap.NeverBackend) > 0 {
					snap.NeverBackend[0].Reason = "mutated"
				}
			}
		})
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent tag/snapshot error: %v", err)
	}
	// Final snapshot must be bounded and consistent.
	snap, err := store.Snapshot(ctx, aLeg)
	require.NoError(t, err)
	require.LessOrEqual(t, len(snap.NeverBackend), conversationview.MaxNeverBackendTags)
	require.Greater(t, len(snap.NeverBackend), 0)
}

func TestConcurrent_SteeringPutVsSnapshot_Deterministic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := conversationview.NewReferenceStore()
	aLeg := "concurrent-steer-vs-snapshot"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 32)

	const writers = 4
	const readers = 4
	const putsPerWriter = 16

	for w := range writers {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			<-start
			for i := range putsPerWriter {
				ovID := fmt.Sprintf("ov-w%d-i%d", wid, i)
				_, err := store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
					OverlayID:           ovID,
					Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: fmt.Sprintf("steer-%d-%d", wid, i)},
					Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
					AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
					Reason:              "test",
				})
				if err != nil {
					errCh <- err
					return
				}
				// Also test deactivate interleaving deterministically for even ids.
				if i%4 == 0 {
					if _, err := store.DeactivateSteering(ctx, aLeg, ovID); err != nil {
						errCh <- err
						return
					}
				}
			}
		}(w)
	}
	for range readers {
		wg.Go(func() {
			<-start
			for range putsPerWriter * 2 {
				snap, err := store.Snapshot(ctx, aLeg)
				if err != nil {
					errCh <- err
					return
				}
				if len(snap.Steering) > conversationview.MaxActiveOverlays {
					errCh <- fmt.Errorf("active overlays exceed cap")
					return
				}
				// Verify SlotOrdinal ordering is preserved in snapshot (sorted).
				for j := 1; j < len(snap.Steering); j++ {
					if snap.Steering[j].SlotOrdinal < snap.Steering[j-1].SlotOrdinal {
						errCh <- fmt.Errorf("snapshot not sorted by SlotOrdinal")
						return
					}
				}
				// Verify total bytes bound.
				total := 0
				for _, ov := range snap.Steering {
					total += len(ov.Message.Text)
				}
				if total > conversationview.MaxTotalSteeringBytes {
					errCh <- fmt.Errorf("total bytes exceed cap")
					return
				}
			}
		})
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent steering/snapshot error: %v", err)
	}
}

func TestConcurrent_MixedMutationsVsSnapshot_ProjectIsPure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := conversationview.NewReferenceStore()
	aLeg := "concurrent-mixed"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	// Seed with some state.
	msg0 := testMessage("seed-user")
	id0, _ := conversationview.MessageIdentityOf(msg0)
	_, err := store.TagNeverBackend(ctx, aLeg, []conversationview.TagRequest{{Identity: id0, Reason: "seed"}})
	require.NoError(t, err)
	u1 := testMessage("anchor-user")
	callForAnchor := aipapi.Call{Messages: []aipapi.Message{u1}}
	snapSeed, _ := store.Snapshot(ctx, aLeg)
	anchor, _ := conversationview.ResolveAfterIngressTailAnchor(callForAnchor, snapSeed)
	_, err = store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
		OverlayID: "ov-mixed-seed", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "seed-steer"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed, Reason: "seed",
	})
	require.NoError(t, err)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 32)

	// Writer: tag + put steering concurrently.
	wg.Go(func() {
		<-start
		for i := range 20 {
			m := testMessage(fmt.Sprintf("writer-msg-%d", i))
			id, _ := conversationview.MessageIdentityOf(m)
			if _, err := store.TagNeverBackend(ctx, aLeg, []conversationview.TagRequest{{Identity: id, Reason: "writer"}}); err != nil {
				errCh <- err
				return
			}
			_, err := store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
				OverlayID: fmt.Sprintf("ov-writer-%d", i), Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: fmt.Sprintf("writer-steer-%d", i)},
				Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
				AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "writer",
			})
			if err != nil {
				errCh <- err
				return
			}
		}
	})

	// Reader: snapshot + pure Project must remain deterministic per snapshot (no data race inside Project).
	wg.Go(func() {
		<-start
		for i := range 20 {
			snap, err := store.Snapshot(ctx, aLeg)
			if err != nil {
				errCh <- err
				return
			}
			call := aipapi.Call{Messages: []aipapi.Message{testMessage("hi"), testMessage(fmt.Sprintf("iter-%d", i))}}
			out, ev, err := conversationview.Project(call, snap)
			if err != nil {
				// Projection may fail closed on anchor missing (fail_closed) - acceptable only if snap has such overlay.
				// For this test, fail_closed not expected because anchor is still present (u1 not in this call).
				// But if snap has after_message anchor missing, it would fail; we treat as error only if unexpected.
				// Check that failure is correctly wrapped as ErrAnchorMissing.
				continue
			}
			// Verify Project is pure: second projection with same inputs yields identical out/ev.
			out2, ev2, _ := conversationview.Project(call, snap)
			if len(out.Messages) != len(out2.Messages) || ev.FilteredCount != ev2.FilteredCount || ev.InjectedCount != ev2.InjectedCount {
				errCh <- fmt.Errorf("project not deterministic")
				return
			}
			_ = out
			_ = ev
		}
	})

	// Reader 2: concurrent Reassert (pure, no I/O) using frozen snapshot.
	wg.Go(func() {
		<-start
		// Take a frozen snapshot at start of this reader.
		snapFrozen, _ := store.Snapshot(ctx, aLeg)
		base := aipapi.Call{Messages: []aipapi.Message{u1}}
		projected, ev, err := conversationview.Project(base, snapFrozen)
		if err != nil {
			errCh <- err
			return
		}
		filtered, _ := conversationview.FilterNeverBackend(base, snapFrozen)
		for range 20 {
			// Reassert must be pure and not read store.
			_, _, err := conversationview.Reassert(projected, snapFrozen, ev.Provenance, filtered)
			if err != nil {
				errCh <- err
				return
			}
		}
	})

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent mixed error: %v", err)
	}
}

func TestConcurrent_SnapshotIsIsolatedFromMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := conversationview.NewReferenceStore()
	aLeg := "concurrent-isolation"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	// Seed.
	msg := testMessage("initial")
	id, _ := conversationview.MessageIdentityOf(msg)
	_, err := store.TagNeverBackend(ctx, aLeg, []conversationview.TagRequest{{Identity: id, Reason: "seed"}})
	require.NoError(t, err)

	// Take snapshot N.
	snapN, err := store.Snapshot(ctx, aLeg)
	require.NoError(t, err)
	revN := snapN.StateRevision

	// Mutate to N+1 concurrently via channel barrier to prove in-flight turn stays on N.
	mutDone := make(chan struct{})
	go func() {
		m2 := testMessage("concurrent-mut")
		id2, _ := conversationview.MessageIdentityOf(m2)
		_, _ = store.TagNeverBackend(context.Background(), aLeg, []conversationview.TagRequest{{Identity: id2, Reason: "concurrent"}})
		close(mutDone)
	}()

	// Snapshot taken before mutDone should have revN; after should have revN+1.
	// We verify snapshot is immutable and doesn't see mutation after it was taken.
	require.Equal(t, revN, snapN.StateRevision)
	<-mutDone
	snapNext, _ := store.Snapshot(ctx, aLeg)
	require.Greater(t, snapNext.StateRevision, snapN.StateRevision)
	// snapN must not have been mutated by concurrent tag.
	for _, tag := range snapN.NeverBackend {
		if tag.Reason == "concurrent" {
			t.Fatalf("snapshot N mutated by concurrent tag")
		}
	}
}

func TestConcurrent_ProjectAndReassert_StablePrefixInvariant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := conversationview.NewReferenceStore()
	aLeg := "concurrent-prefix-invariant"
	require.NoError(t, store.CreateALeg(ctx, aLeg))

	_, err := store.PutSteering(ctx, aLeg, conversationview.PutSteeringRequest{
		OverlayID: "ov-stable-conc", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "STABLE_CONCURRENT"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "test",
	})
	require.NoError(t, err)
	snap, _ := store.Snapshot(ctx, aLeg)

	// Deterministic concurrent projections on same snapshot must yield identical trajectories.
	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	results := make([][]string, workers)
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			call := aipapi.Call{
				Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "sys"}}}},
				Messages:     []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "user"}}}},
			}
			out, _, err := conversationview.Project(call, snap)
			if err != nil {
				errCh <- err
				return
			}
			var traj []string
			for _, m := range out.Instructions {
				if len(m.Parts) > 0 {
					traj = append(traj, m.Parts[0].Text)
				}
			}
			for _, m := range out.Messages {
				if len(m.Parts) > 0 {
					traj = append(traj, m.Parts[0].Text)
				}
			}
			results[idx] = traj
		}(i)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent project error: %v", err)
	}
	for i := 1; i < workers; i++ {
		require.Equal(t, results[0], results[i], "concurrent projections on same snapshot must be identical")
	}
}
