package compaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/resultmerge"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

var ErrInvalidCompactionContinuityResultAdapter = errors.New("featurehost/compaction: invalid compaction-continuity result adapter")

// CompactionContinuityResultAdapter binds one process-owned coordinator and
// authoritative parent key without decoding a branch binding into a BranchKey.
type CompactionContinuityResultAdapter struct {
	coordinator *state.BranchCoordinator
	parentKey   state.BranchKey
	binding     string
}

// NewCompactionContinuityResultAdapter binds result consumption to the process-owned coordinator and authoritative parentKey.
func NewCompactionContinuityResultAdapter(coordinator *state.BranchCoordinator, parentKey state.BranchKey) (*CompactionContinuityResultAdapter, error) {
	if coordinator == nil {
		return nil, fmt.Errorf("%w: nil branch coordinator", ErrInvalidCompactionContinuityResultAdapter)
	}
	binding, err := state.BranchBinding(parentKey)
	if err != nil {
		return nil, fmt.Errorf("%w: parent key: %w", ErrInvalidCompactionContinuityResultAdapter, err)
	}
	return &CompactionContinuityResultAdapter{
		coordinator: coordinator,
		parentKey:   parentKey,
		binding:     binding,
	}, nil
}

// ValidatePendingJob validates the job and translates its defensive state copy.
func (a *CompactionContinuityResultAdapter) ValidatePendingJob(ctx context.Context, branchBinding string, jobID auxiliary.JobID) (resultmerge.ParentState, error) {
	if err := a.validateBinding(branchBinding); err != nil {
		return resultmerge.ParentState{}, err
	}
	state, err := a.coordinator.ValidatePendingJob(ctx, a.parentKey, jobID)
	if err != nil {
		return resultmerge.ParentState{}, err
	}
	return a.parentState(state)
}

// CommitCapsuleForJob delegates the job-bound CAS to the real coordinator
// after validating both branch-binding arguments against the captured parent.
func (a *CompactionContinuityResultAdapter) CommitCapsuleForJob(ctx context.Context, branchBinding string, jobID auxiliary.JobID, resultBranchBinding string, expectedRevision uint64, capsule []byte, digest [32]byte, sourceHighWatermark string) (resultmerge.ParentState, error) {
	if err := a.validateBinding(branchBinding); err != nil {
		return resultmerge.ParentState{}, err
	}
	if err := a.validateBinding(resultBranchBinding); err != nil {
		return resultmerge.ParentState{}, err
	}
	state, err := a.coordinator.CommitCapsuleForJob(ctx, a.parentKey, jobID, resultBranchBinding, expectedRevision, capsule, digest, sourceHighWatermark)
	if err != nil {
		return resultmerge.ParentState{}, err
	}
	return a.parentState(state)
}

func (a *CompactionContinuityResultAdapter) validateBinding(binding string) error {
	if a == nil || a.coordinator == nil || binding != a.binding {
		return state.ErrBranchMismatch
	}
	return nil
}

func (a *CompactionContinuityResultAdapter) parentState(st state.BranchState) (resultmerge.ParentState, error) {
	if st.PendingJobBranchBinding != "" && st.PendingJobBranchBinding != a.binding {
		return resultmerge.ParentState{}, state.ErrBranchMismatch
	}
	if st.PendingJobID != "" && st.PendingJobBranchBinding != a.binding {
		return resultmerge.ParentState{}, state.ErrBranchMismatch
	}
	return resultmerge.ParentState{
		BranchBinding:            a.binding,
		Revision:                 st.Revision,
		CapsuleJSON:              append([]byte(nil), st.CapsuleJSON...),
		CapsuleDigest:            st.CapsuleDigest,
		SourceHighWatermark:      st.SourceHighWatermark,
		PendingJobID:             st.PendingJobID,
		PendingJobTargetRevision: st.PendingJobTargetRevision,
		PendingJobBranchBinding:  st.PendingJobBranchBinding,
	}, nil
}

var _ resultmerge.ParentCoordinator = (*CompactionContinuityResultAdapter)(nil)
