package state

import (
	"context"
	"fmt"
	"strings"
)

// CommitCapsule commits an opaque capsule revision using compare-and-swap
// semantics. expectedRevision zero is valid only for an empty branch.
func (c *BranchCoordinator) CommitCapsule(ctx context.Context, key BranchKey, expectedRevision uint64, capsule []byte, digest [32]byte, sourceHighWatermark string) (BranchState, error) {
	if c == nil {
		return BranchState{}, ErrBranchNotFound
	}
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, err
	}
	if len(capsule) > c.maxCapsuleBytes {
		return BranchState{}, fmt.Errorf("%w: capsule bytes=%d max=%d", ErrStateTooLarge, len(capsule), c.maxCapsuleBytes)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	entry, ok := c.entries[binding]
	if !ok {
		// State may only be created by the explicit pre-child Capture call.
		// This keeps a detached child A-leg from silently becoming authority.
		return BranchState{}, ErrBranchNotFound
	}
	if entry.State.Revision != expectedRevision {
		return BranchState{}, fmt.Errorf("%w: have %d, want %d", ErrRevisionConflict, entry.State.Revision, expectedRevision)
	}
	entry.State.Revision++
	entry.State.CapsuleJSON = cloneBytes(capsule)
	entry.State.CapsuleDigest = digest
	entry.State.SourceHighWatermark = strings.TrimSpace(sourceHighWatermark)
	entry.State.UpdatedAt = c.now()
	entry.ExpiresAt = entry.State.UpdatedAt.Add(c.ttl)
	next := c.cloneEntriesLocked()
	next[binding] = entry
	if err := c.persist(ctx, next); err != nil {
		return BranchState{}, fmt.Errorf("compactioncontinuity: persist capsule: %w", err)
	}
	c.entries = next
	return cloneBranchState(entry.State), nil
}

// CommitSource stores a bounded sanitized source snapshot on the post-open
// refresh path (RequestOpened, after the preview intent is bound). It does
// not advance the capsule revision, but still compare-checks the current
// revision. Pre-open preparation persists only the preview intent and the
// deterministic capsule with the prior watermark; that staged state stays
// TTL-bounded and inert until a successful open binds the preview intent;
// failed opens must never bind intents, submit billable jobs, or commit release watermarks (§571).
func (c *BranchCoordinator) CommitSource(ctx context.Context, key BranchKey, expectedRevision uint64, source []byte, highWatermark string) (BranchState, error) {
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, err
	}
	if len(source) > c.maxSourceBytes {
		return BranchState{}, fmt.Errorf("%w: source bytes=%d max=%d", ErrStateTooLarge, len(source), c.maxSourceBytes)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	entry, ok := c.entries[binding]
	if !ok {
		return BranchState{}, ErrBranchNotFound
	}
	if entry.State.Revision != expectedRevision {
		return BranchState{}, fmt.Errorf("%w: have %d, want %d", ErrRevisionConflict, entry.State.Revision, expectedRevision)
	}
	entry.State.SanitizedSourceJSON = cloneBytes(source)
	entry.State.SourceHighWatermark = strings.TrimSpace(highWatermark)
	entry.State.UpdatedAt = c.now()
	entry.ExpiresAt = entry.State.UpdatedAt.Add(c.ttl)
	next := c.cloneEntriesLocked()
	next[binding] = entry
	if err := c.persist(ctx, next); err != nil {
		return BranchState{}, fmt.Errorf("compactioncontinuity: persist source: %w", err)
	}
	c.entries = next
	return cloneBranchState(entry.State), nil
}
