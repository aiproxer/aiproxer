package state

import (
	"context"
	"fmt"
	"sort"
	"time"

	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

// NewBranchCoordinator constructs the process-owned coordinator.
func NewBranchCoordinator(ctx context.Context, cfg Config) (*BranchCoordinator, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultMaxEntries
	}
	if cfg.MaxPreviewIntents <= 0 {
		cfg.MaxPreviewIntents = DefaultMaxPreviewIntents
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.MaxCapsuleBytes <= 0 {
		cfg.MaxCapsuleBytes = DefaultMaxCapsuleBytes
	}
	if cfg.MaxSourceBytes <= 0 {
		cfg.MaxSourceBytes = DefaultMaxSourceBytes
	}
	c := &BranchCoordinator{
		store:             cfg.Store,
		now:               cfg.Now,
		maxEntries:        cfg.MaxEntries,
		maxPreviewIntents: cfg.MaxPreviewIntents,
		ttl:               cfg.TTL,
		maxCapsuleBytes:   cfg.MaxCapsuleBytes,
		maxSourceBytes:    cfg.MaxSourceBytes,
		entries:           make(map[string]branchEntry),
	}
	if err := c.load(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *BranchCoordinator) load(ctx context.Context) error {
	if c == nil || c.store == nil {
		return nil
	}
	var raw persistedState
	found, err := c.store.Get(ctx, aipstate.ScopeGlobal, stateNamespace, stateKey, &raw)
	if err != nil {
		return fmt.Errorf("%w: load: %w", ErrInvalidState, err)
	}
	if !found {
		return nil
	}
	if raw.Version != stateVersion || len(raw.Entries) > c.maxEntries {
		return fmt.Errorf("%w: unsupported version or entry bound", ErrInvalidState)
	}
	for _, entry := range raw.Entries {
		if err := entry.Key.Validate(); err != nil || entry.Key.Binding() == "" {
			return fmt.Errorf("%w: branch entry: %v", ErrInvalidState, err)
		}
		if entry.ExpiresAt.IsZero() {
			entry.ExpiresAt = c.now().Add(c.ttl)
		}
		if entry.ExpiresAt.After(c.now()) {
			c.entries[entry.Key.Binding()] = cloneBranchEntry(entry)
		}
	}
	if c.previewIntentCountLocked() > c.maxPreviewIntents {
		return fmt.Errorf("%w: persisted intents exceed bound", ErrInvalidState)
	}
	return nil
}

func (c *BranchCoordinator) persist(ctx context.Context, entries map[string]branchEntry) error {
	if c.store == nil {
		return nil
	}
	list := make([]branchEntry, 0, len(entries))
	for _, entry := range entries {
		list = append(list, cloneBranchEntry(entry))
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Key.Binding() < list[j].Key.Binding()
	})
	return c.store.Put(ctx, aipstate.ScopeGlobal, stateNamespace, stateKey, persistedState{Version: stateVersion, Entries: list}, c.ttl)
}

func (c *BranchCoordinator) cleanupLocked(now time.Time) {
	for binding, entry := range c.entries {
		if !entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(now) {
			delete(c.entries, binding)
		}
	}
}

func (c *BranchCoordinator) cloneEntriesLocked() map[string]branchEntry {
	out := make(map[string]branchEntry, len(c.entries))
	for binding, entry := range c.entries {
		out[binding] = cloneBranchEntry(entry)
	}
	return out
}

// Capture records the parent branch before detached child creation. It is
// idempotent for an existing non-expired branch.
func (c *BranchCoordinator) Capture(ctx context.Context, key BranchKey) (string, error) {
	if c == nil {
		return "", ErrBranchNotFound
	}
	binding, err := BranchBinding(key)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	if entry, ok := c.entries[binding]; ok {
		return entry.Key.Binding(), nil
	}
	if len(c.entries) >= c.maxEntries {
		return "", ErrBranchLimit
	}
	now := c.now()
	next := c.cloneEntriesLocked()
	next[binding] = branchEntry{Key: normalizeBranchKey(key), State: BranchState{UpdatedAt: now}, ExpiresAt: now.Add(c.ttl)}
	if err := c.persist(ctx, next); err != nil {
		return "", fmt.Errorf("compactioncontinuity: persist capture: %w", err)
	}
	c.entries = next
	return binding, nil
}

// Snapshot returns a defensive copy of state for the captured parent branch.
func (c *BranchCoordinator) Snapshot(_ context.Context, key BranchKey) (BranchState, bool, error) {
	if c == nil {
		return BranchState{}, false, ErrBranchNotFound
	}
	binding, err := BranchBinding(key)
	if err != nil {
		return BranchState{}, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	entry, ok := c.entries[binding]
	if !ok {
		return BranchState{}, false, nil
	}
	return cloneBranchState(entry.State), true, nil
}

// Retire removes a branch and its pending state, used for explicit reset/new
// A-leg replacement. It does not cancel external background work.
func (c *BranchCoordinator) Retire(ctx context.Context, key BranchKey) error {
	binding, err := BranchBinding(key)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(c.now())
	if _, ok := c.entries[binding]; !ok {
		return nil
	}
	next := c.cloneEntriesLocked()
	delete(next, binding)
	if err := c.persist(ctx, next); err != nil {
		return fmt.Errorf("compactioncontinuity: persist retire: %w", err)
	}
	c.entries = next
	return nil
}
