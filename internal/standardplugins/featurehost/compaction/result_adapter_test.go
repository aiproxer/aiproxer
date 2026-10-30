package compaction

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/resultmerge"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func TestCompactionContinuityResultAdapterUsesCapturedParentForValidateAndCommit(t *testing.T) {
	t.Parallel()

	coordinator, err := state.NewBranchCoordinator(context.Background(), state.Config{})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := state.CaptureParentBranchKey("session-result-parent", "a-parent", "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	child, err := state.NewBranchKey("session-result-parent", "a-child", "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	parentBinding := parent.Binding()
	if parentBinding == "" || parentBinding == child.Binding() {
		t.Fatal("parent and private child must have distinct bindings")
	}
	if _, err := coordinator.Capture(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CommitCapsule(context.Background(), parent, 0, []byte(`{"schema_version":1}`), [32]byte{1}, "source-1"); err != nil {
		t.Fatal(err)
	}
	jobID := auxiliary.JobID("job-result-parent")
	if _, err := coordinator.RecordPendingJob(context.Background(), parent, jobID, 1); err != nil {
		t.Fatal(err)
	}

	adapter, err := NewCompactionContinuityResultAdapter(coordinator, parent)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := adapter.ValidatePendingJob(context.Background(), parentBinding, jobID)
	if err != nil {
		t.Fatalf("parent validation: %v", err)
	}
	if validated.BranchBinding != parentBinding || validated.Revision != 1 ||
		string(validated.CapsuleJSON) != `{"schema_version":1}` ||
		validated.CapsuleDigest != [32]byte{1} || validated.SourceHighWatermark != "source-1" ||
		validated.PendingJobID != jobID || validated.PendingJobTargetRevision != 1 ||
		validated.PendingJobBranchBinding != parentBinding {
		t.Fatalf("translated parent state = %#v", validated)
	}

	committed, err := adapter.CommitCapsuleForJob(
		context.Background(),
		parentBinding,
		jobID,
		parentBinding,
		validated.Revision,
		[]byte(`{"schema_version":1,"merged":true}`),
		[32]byte{2},
		"source-2",
	)
	if err != nil {
		t.Fatalf("parent job commit: %v", err)
	}
	if committed.BranchBinding != parentBinding || committed.Revision != 2 ||
		string(committed.CapsuleJSON) != `{"schema_version":1,"merged":true}` ||
		committed.CapsuleDigest != [32]byte{2} || committed.SourceHighWatermark != "source-2" ||
		committed.PendingJobID != "" || committed.PendingJobTargetRevision != 0 ||
		committed.PendingJobBranchBinding != "" {
		t.Fatalf("translated committed state = %#v", committed)
	}
	if _, err := coordinator.ValidatePendingJob(context.Background(), parent, jobID); !errors.Is(err, state.ErrPendingJobMismatch) {
		t.Fatalf("job-bound commit did not clear pending job: %v", err)
	}

	if _, err := adapter.ValidatePendingJob(context.Background(), child.Binding(), jobID); !errors.Is(err, state.ErrBranchMismatch) {
		t.Fatalf("child binding validation error = %v, want ErrBranchMismatch", err)
	}
	if _, err := adapter.CommitCapsuleForJob(context.Background(), parentBinding, jobID, child.Binding(), 1, []byte(`child`), [32]byte{3}, "source-child"); !errors.Is(err, state.ErrBranchMismatch) {
		t.Fatalf("child result binding commit error = %v, want ErrBranchMismatch", err)
	}
	if _, err := adapter.CommitCapsuleForJob(context.Background(), child.Binding(), jobID, parentBinding, 1, []byte(`child`), [32]byte{3}, "source-child"); !errors.Is(err, state.ErrBranchMismatch) {
		t.Fatalf("child parent binding commit error = %v, want ErrBranchMismatch", err)
	}
}

func TestCompactionContinuityResultAdapterRejectsInvalidCapturedParent(t *testing.T) {
	t.Parallel()

	key, err := state.NewBranchKey("session-result-invalid", "a-parent", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCompactionContinuityResultAdapter(nil, key); err == nil {
		t.Fatal("nil coordinator must be rejected")
	}
	if _, err := NewCompactionContinuityResultAdapter(&state.BranchCoordinator{}, state.BranchKey{}); err == nil {
		t.Fatal("invalid parent key must be rejected")
	}
}

var _ resultmerge.ParentCoordinator = (*CompactionContinuityResultAdapter)(nil)
