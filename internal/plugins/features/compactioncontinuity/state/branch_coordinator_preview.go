package state

import (
	"context"
	"fmt"
	"strings"
)

// BindPreviewIntent consumes a non-billable intent after a successful primary
// Open and records its committed transaction identity.
func (c *BranchCoordinator) BindPreviewIntent(ctx context.Context, key BranchKey, intentKey, transactionID string) (BranchState, error) {
	if strings.TrimSpace(transactionID) == "" {
		return BranchState{}, ErrInvalidTransaction
	}
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	entry, ok := c.entries[binding]
	if !ok || entry.State.PendingPreviewIntent == nil {
		return BranchState{}, ErrPreviewIntentMismatch
	}
	if entry.State.PendingPreviewIntent.Key != intentKey {
		return BranchState{}, ErrPreviewIntentMismatch
	}
	entry.State.LastCompactionTransaction = strings.TrimSpace(transactionID)
	entry.State.PendingPreviewIntent = nil
	entry.State.UpdatedAt = c.now()
	entry.ExpiresAt = entry.State.UpdatedAt.Add(c.ttl)
	next := c.cloneEntriesLocked()
	next[binding] = entry
	if err := c.persist(ctx, next); err != nil {
		return BranchState{}, fmt.Errorf("compactioncontinuity: persist preview binding: %w", err)
	}
	c.entries = next
	return cloneBranchState(entry.State), nil
}

func (c *BranchCoordinator) RecordPreviewIntent(ctx context.Context, key BranchKey, intent PreviewIntent) (BranchState, error) {
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, err
	}
	if strings.TrimSpace(intent.Key) == "" {
		return BranchState{}, ErrPreviewIntentMismatch
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	entry, ok := c.entries[binding]
	if !ok {
		return BranchState{}, ErrBranchNotFound
	}
	if entry.State.PendingPreviewIntent != nil {
		old := entry.State.PendingPreviewIntent
		if *old == intent {
			return cloneBranchState(entry.State), nil
		}
		return BranchState{}, ErrPreviewIntentMismatch
	}
	if c.previewIntentCountLocked() >= c.maxPreviewIntents {
		return BranchState{}, ErrPreviewIntentLimit
	}
	entry.State.PendingPreviewIntent = &intent
	entry.State.UpdatedAt = c.now()
	entry.ExpiresAt = entry.State.UpdatedAt.Add(c.ttl)
	next := c.cloneEntriesLocked()
	next[binding] = entry
	if err := c.persist(ctx, next); err != nil {
		return BranchState{}, fmt.Errorf("compactioncontinuity: persist preview intent: %w", err)
	}
	c.entries = next
	return cloneBranchState(entry.State), nil
}

func (c *BranchCoordinator) previewIntentCountLocked() int {
	count := 0
	for _, entry := range c.entries {
		if entry.State.PendingPreviewIntent != nil {
			count++
		}
	}
	return count
}
