package state

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func TestReloadConcurrencyCertification_ExplicitCorrectionWinsLateResult(t *testing.T) {
	t.Parallel()
	coordinator, err := NewBranchCoordinator(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := CaptureParentBranchKey("correction-session", "parent-a", "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Capture(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CommitCapsule(context.Background(), parent, 0, []byte(`{"intent":"initial"}`), [32]byte{1}, "source-1"); err != nil {
		t.Fatal(err)
	}
	lateJob := auxiliary.JobID("late-correction-job")
	if _, err := coordinator.RecordPendingJob(context.Background(), parent, lateJob, 1); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	lateErrs := make(chan error, 1)
	correctionErrs := make(chan error, 1)
	go func() {
		<-start
		_, mergeErr := coordinator.CommitCapsuleForJob(context.Background(), parent, lateJob, parent.Binding(), 1, []byte(`{"intent":"stale-extractor"}`), [32]byte{2}, "source-late")
		lateErrs <- mergeErr
	}()
	go func() {
		<-start
		var commitErr error
		for range 32 {
			state, found, snapshotErr := coordinator.Snapshot(context.Background(), parent)
			if snapshotErr != nil || !found {
				commitErr = snapshotErr
				if commitErr == nil {
					commitErr = ErrBranchNotFound
				}
				break
			}
			_, commitErr = coordinator.CommitCapsule(context.Background(), parent, state.Revision, []byte(`{"intent":"explicit-correction"}`), [32]byte{3}, "source-explicit")
			if !errors.Is(commitErr, ErrRevisionConflict) {
				break
			}
			runtime.Gosched()
		}
		correctionErrs <- commitErr
	}()
	close(start)
	if err := <-correctionErrs; err != nil {
		t.Fatalf("explicit correction: %v", err)
	}
	lateErr := <-lateErrs
	if lateErr != nil && !errors.Is(lateErr, ErrRevisionConflict) && !errors.Is(lateErr, ErrPendingJobMismatch) {
		t.Fatalf("late extractor error=%v want stale-result rejection", lateErr)
	}
	state, found, err := coordinator.Snapshot(context.Background(), parent)
	if err != nil || !found || !bytes.Equal(state.CapsuleJSON, []byte(`{"intent":"explicit-correction"}`)) {
		t.Fatalf("newer explicit intent was not authoritative: state=%#v found=%v err=%v", state, found, err)
	}
	child, err := NewBranchKey(parent.AuthoritativeSessionID, "fork-no-parent", parent.PrincipalPartition)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := coordinator.Snapshot(context.Background(), child); err != nil || found {
		t.Fatalf("fork without explicit parent inherited state: found=%v err=%v", found, err)
	}
}

func TestReloadConcurrencyCertification_ResetAndForkDoNotLeakParentState(t *testing.T) {
	t.Parallel()
	coordinator, err := NewBranchCoordinator(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := CaptureParentBranchKey("reset-session", "parent-a", "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Capture(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CommitCapsule(context.Background(), parent, 0, []byte(`{"intent":"parent"}`), [32]byte{1}, "source"); err != nil {
		t.Fatal(err)
	}
	job := auxiliary.JobID("reset-job")
	if _, err := coordinator.RecordPendingJob(context.Background(), parent, job, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.RecordPreviewIntent(context.Background(), parent, PreviewIntent{Key: "preview-reset", TargetSourceRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.SetPendingInjection(context.Background(), parent, InjectionTarget{BoundaryKey: "boundary-reset", CapsuleRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Retire(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	if _, found, err := coordinator.Snapshot(context.Background(), parent); err != nil || found {
		t.Fatalf("retired parent state still present: found=%v err=%v", found, err)
	}
	if _, err := coordinator.MergeCapsule(context.Background(), parent, job, parent.Binding(), 1, []byte(`{"leak":true}`), [32]byte{2}); err == nil {
		t.Fatal("late retired-parent result unexpectedly merged")
	}

	for name, key := range map[string]BranchKey{
		"new-a-leg":           mustCertificationBranch(t, parent.AuthoritativeSessionID, "new-a-leg", parent.PrincipalPartition),
		"fork-without-parent": mustCertificationBranch(t, "fork-session", "fork-a", parent.PrincipalPartition),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := coordinator.Capture(context.Background(), key); err != nil {
				t.Fatal(err)
			}
			if state, found, err := coordinator.Snapshot(context.Background(), key); err != nil || !found || state.Revision != 0 || state.PendingJobID != "" || state.PendingPreviewIntent != nil || state.PendingInjection != nil {
				t.Fatalf("new branch inherited state=%#v found=%v err=%v", state, found, err)
			}
		})
	}
}

func mustCertificationBranch(t *testing.T, session, aLeg, principal string) BranchKey {
	t.Helper()
	key, err := NewBranchKey(session, aLeg, principal)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
