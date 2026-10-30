package state

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// CommitCapsuleForJob applies a revision only if the pending job and parent
// binding still match, preventing late child results from moving branches.
func (c *BranchCoordinator) CommitCapsuleForJob(ctx context.Context, key BranchKey, jobID auxiliary.JobID, branchBinding string, expectedRevision uint64, capsule []byte, digest [32]byte, sourceHighWatermark string) (BranchState, error) {
	if _, err := c.ValidatePendingJob(ctx, key, jobID); err != nil {
		return BranchState{}, err
	}
	if branchBinding != key.Binding() {
		return BranchState{}, ErrBranchMismatch
	}
	return c.mergeCapsule(ctx, key, jobID, branchBinding, expectedRevision, capsule, digest, sourceHighWatermark)
}

// MergeCapsule validates pending job ownership and commits a late result as a
// new revision. Raw result ownership is outside this coordinator.
func (c *BranchCoordinator) MergeCapsule(ctx context.Context, key BranchKey, jobID auxiliary.JobID, branchBinding string, baseRevision uint64, capsule []byte, digest [32]byte) (BranchState, error) {
	return c.mergeCapsule(ctx, key, jobID, branchBinding, baseRevision, capsule, digest, "")
}

func (c *BranchCoordinator) mergeCapsule(ctx context.Context, key BranchKey, jobID auxiliary.JobID, branchBinding string, baseRevision uint64, capsule []byte, digest [32]byte, sourceHighWatermark string) (BranchState, error) {
	if c == nil {
		return BranchState{}, ErrBranchNotFound
	}
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, err
	}
	if strings.TrimSpace(branchBinding) == "" || branchBinding != binding {
		return BranchState{}, ErrBranchMismatch
	}
	if len(capsule) > c.maxCapsuleBytes {
		return BranchState{}, fmt.Errorf("%w: capsule bytes=%d max=%d", ErrStateTooLarge, len(capsule), c.maxCapsuleBytes)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	entry, ok := c.entries[binding]
	if !ok {
		return BranchState{}, ErrBranchNotFound
	}
	if entry.State.PendingJobID == "" || entry.State.PendingJobID != jobID || entry.State.PendingJobBranchBinding != binding {
		return BranchState{}, ErrPendingJobMismatch
	}
	if entry.State.PendingJobTargetRevision != baseRevision {
		return BranchState{}, fmt.Errorf("%w: target revision %d, base %d", ErrRevisionConflict, entry.State.PendingJobTargetRevision, baseRevision)
	}
	if entry.State.Revision != baseRevision {
		return BranchState{}, fmt.Errorf("%w: have %d, want %d", ErrRevisionConflict, entry.State.Revision, baseRevision)
	}
	entry.State.Revision++
	entry.State.CapsuleJSON = cloneBytes(capsule)
	entry.State.CapsuleDigest = digest
	if strings.TrimSpace(sourceHighWatermark) != "" {
		entry.State.SourceHighWatermark = strings.TrimSpace(sourceHighWatermark)
	}
	entry.State.PendingJobID = ""
	entry.State.PendingJobTargetRevision = 0
	entry.State.PendingJobBranchBinding = ""
	entry.State.UpdatedAt = c.now()
	entry.ExpiresAt = entry.State.UpdatedAt.Add(c.ttl)
	next := c.cloneEntriesLocked()
	next[binding] = entry
	if err := c.persist(ctx, next); err != nil {
		return BranchState{}, fmt.Errorf("compactioncontinuity: persist merge: %w", err)
	}
	c.entries = next
	return cloneBranchState(entry.State), nil
}

// RecordPendingJob binds a committed background job to the captured parent.
func (c *BranchCoordinator) RecordPendingJob(ctx context.Context, key BranchKey, jobID auxiliary.JobID, targetRevision uint64) (BranchState, error) {
	if strings.TrimSpace(string(jobID)) == "" {
		return BranchState{}, ErrPendingJobMismatch
	}
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	entry, ok := c.entries[binding]
	if !ok {
		return BranchState{}, ErrBranchNotFound
	}
	if entry.State.PendingJobID != "" {
		if entry.State.PendingJobID == jobID && entry.State.PendingJobTargetRevision == targetRevision && entry.State.PendingJobBranchBinding == binding {
			return cloneBranchState(entry.State), nil
		}
		return BranchState{}, ErrPendingJobMismatch
	}
	if targetRevision > entry.State.Revision {
		return BranchState{}, ErrRevisionConflict
	}
	entry.State.PendingJobID = jobID
	entry.State.PendingJobTargetRevision = targetRevision
	entry.State.PendingJobBranchBinding = binding
	entry.State.UpdatedAt = c.now()
	entry.ExpiresAt = entry.State.UpdatedAt.Add(c.ttl)
	next := c.cloneEntriesLocked()
	next[binding] = entry
	if err := c.persist(ctx, next); err != nil {
		return BranchState{}, fmt.Errorf("compactioncontinuity: persist pending job: %w", err)
	}
	c.entries = next
	return cloneBranchState(entry.State), nil
}

// ValidatePendingJob is the common guard for Await, merge, revision, and
// reinjection paths. It never accepts a child A-leg as a lookup key.
func (c *BranchCoordinator) ValidatePendingJob(_ context.Context, key BranchKey, jobID auxiliary.JobID) (BranchState, error) {
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	entry, ok := c.entries[binding]
	if !ok {
		if c.hasSiblingBranchLocked(key) {
			return BranchState{}, ErrBranchMismatch
		}
		return BranchState{}, ErrPendingJobMismatch
	}
	if entry.State.PendingJobID == "" || entry.State.PendingJobID != jobID || entry.State.PendingJobBranchBinding != binding {
		return BranchState{}, ErrPendingJobMismatch
	}
	return cloneBranchState(entry.State), nil
}

// ValidatePendingJobBinding adds the opaque branch-binding check used around
// scheduler Await. No external call is made while coordinator state is held.
func (c *BranchCoordinator) ValidatePendingJobBinding(ctx context.Context, key BranchKey, jobID auxiliary.JobID, branchBinding string) (BranchState, error) {
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, err
	}
	if branchBinding != binding {
		return BranchState{}, ErrBranchMismatch
	}
	return c.ValidatePendingJob(ctx, key, jobID)
}

// hasSiblingBranchLocked identifies a likely private child A-leg lookup. It is
// intentionally only a diagnostic guard: the exact binding remains the sole
// authority, and no client hint can select a sibling branch.
func (c *BranchCoordinator) hasSiblingBranchLocked(key BranchKey) bool {
	normalized := normalizeBranchKey(key)
	for _, entry := range c.entries {
		other := normalizeBranchKey(entry.Key)
		if other.AuthoritativeSessionID == normalized.AuthoritativeSessionID &&
			other.PrincipalPartition == normalized.PrincipalPartition &&
			other.ALegID != normalized.ALegID {
			return true
		}
	}
	return false
}
