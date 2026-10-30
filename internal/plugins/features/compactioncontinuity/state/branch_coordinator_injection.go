package state

import (
	"context"
	"fmt"
	"strings"
)

func (c *BranchCoordinator) SetPendingInjection(ctx context.Context, key BranchKey, target InjectionTarget) (BranchState, error) {
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, err
	}
	if strings.TrimSpace(target.BoundaryKey) == "" || target.CapsuleRevision == 0 {
		return BranchState{}, ErrInjectionMismatch
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	entry, ok := c.entries[binding]
	if !ok {
		return BranchState{}, ErrBranchNotFound
	}
	if entry.State.PendingJobID != "" && entry.State.PendingJobBranchBinding != binding {
		return BranchState{}, ErrPendingJobMismatch
	}
	if target.CapsuleRevision > entry.State.Revision {
		return BranchState{}, ErrRevisionConflict
	}
	if entry.State.LastReleasedInjection != nil && *entry.State.LastReleasedInjection == (InjectionWatermark{BranchBinding: binding, BoundaryKey: target.BoundaryKey, CapsuleRevision: target.CapsuleRevision}) {
		return BranchState{}, ErrInjectionAlreadyReleased
	}
	if entry.State.PendingInjection != nil && *entry.State.PendingInjection == target {
		return cloneBranchState(entry.State), nil
	}
	entry.State.PendingInjection = &target
	entry.State.UpdatedAt = c.now()
	entry.ExpiresAt = entry.State.UpdatedAt.Add(c.ttl)
	next := c.cloneEntriesLocked()
	next[binding] = entry
	if err := c.persist(ctx, next); err != nil {
		return BranchState{}, fmt.Errorf("compactioncontinuity: persist pending injection: %w", err)
	}
	c.entries = next
	return cloneBranchState(entry.State), nil
}

// ValidateInjection enforces branch and pending-target consistency before a
// caller mutates a canonical request.
func (c *BranchCoordinator) ValidateInjection(_ context.Context, key BranchKey, target InjectionTarget) (BranchState, error) {
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	entry, ok := c.entries[binding]
	if !ok || entry.State.PendingInjection == nil || *entry.State.PendingInjection != target {
		return BranchState{}, ErrInjectionMismatch
	}
	if entry.State.LastReleasedInjection != nil && *entry.State.LastReleasedInjection == (InjectionWatermark{BranchBinding: binding, BoundaryKey: target.BoundaryKey, CapsuleRevision: target.CapsuleRevision}) {
		return BranchState{}, ErrInjectionAlreadyReleased
	}
	return cloneBranchState(entry.State), nil
}

// CommitReleasedInjection advances the durable compound watermark only after
// successful final client release. A pre-release validation/Open/abort path
// must leave PendingInjection untouched.
func (c *BranchCoordinator) CommitReleasedInjection(ctx context.Context, key BranchKey, watermark InjectionWatermark) (BranchState, error) {
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, err
	}
	if watermark.BranchBinding != binding {
		return BranchState{}, ErrBranchMismatch
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	entry, ok := c.entries[binding]
	if !ok || entry.State.PendingInjection == nil {
		return BranchState{}, ErrInjectionMismatch
	}
	if *entry.State.PendingInjection != (InjectionTarget{BoundaryKey: watermark.BoundaryKey, CapsuleRevision: watermark.CapsuleRevision}) {
		return BranchState{}, ErrInjectionMismatch
	}
	wm := watermark
	entry.State.LastReleasedInjection = &wm
	entry.State.PendingInjection = nil
	entry.State.UpdatedAt = c.now()
	entry.ExpiresAt = entry.State.UpdatedAt.Add(c.ttl)
	next := c.cloneEntriesLocked()
	next[binding] = entry
	if err := c.persist(ctx, next); err != nil {
		return BranchState{}, fmt.Errorf("compactioncontinuity: persist release watermark: %w", err)
	}
	c.entries = next
	return cloneBranchState(entry.State), nil
}
