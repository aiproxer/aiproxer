package snapshotsource

import (
	"context"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// MemoryRuleSource is an injectable publishable RuleSnapshotSource for tests
// and enterprise refresh adapters (requirements 11.5, 11.6).
type MemoryRuleSource struct {
	mu   sync.RWMutex
	snap economics.Snapshot[economics.PolicyRulesView]
}

// NewMemoryRuleSource returns a source seeded with snap (deep-copied on read).
func NewMemoryRuleSource(snap economics.Snapshot[economics.PolicyRulesView]) *MemoryRuleSource {
	return &MemoryRuleSource{snap: clonePolicySnap(snap)}
}

// Publish replaces the active snapshot atomically for subsequent Snapshot calls.
// Prior returned snapshots remain unchanged (immutable value copies).
func (s *MemoryRuleSource) Publish(snap economics.Snapshot[economics.PolicyRulesView]) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = clonePolicySnap(snap)
}

// Snapshot implements economics.RuleSnapshotSource.
func (s *MemoryRuleSource) Snapshot(ctx context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	if err := ctx.Err(); err != nil {
		return economics.Snapshot[economics.PolicyRulesView]{}, err
	}
	if s == nil {
		return economics.Snapshot[economics.PolicyRulesView]{
			State: economics.SnapshotDisabled,
		}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clonePolicySnap(s.snap), nil
}

// MemoryRatingSource is an injectable RatingSnapshotSource.
type MemoryRatingSource struct {
	mu   sync.RWMutex
	snap economics.Snapshot[economics.RatingCatalogView]
}

// NewMemoryRatingSource returns a rating source seeded with snap.
func NewMemoryRatingSource(snap economics.Snapshot[economics.RatingCatalogView]) *MemoryRatingSource {
	return &MemoryRatingSource{snap: snap}
}

// Publish replaces the active rating snapshot.
func (s *MemoryRatingSource) Publish(snap economics.Snapshot[economics.RatingCatalogView]) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = snap
}

// Snapshot implements economics.RatingSnapshotSource.
func (s *MemoryRatingSource) Snapshot(ctx context.Context) (economics.Snapshot[economics.RatingCatalogView], error) {
	if err := ctx.Err(); err != nil {
		return economics.Snapshot[economics.RatingCatalogView]{}, err
	}
	if s == nil {
		return economics.Snapshot[economics.RatingCatalogView]{
			State: economics.SnapshotDisabled,
		}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, nil
}

// StaticRatingFromCatalog builds a ready static rating snapshot from catalog metadata.
func StaticRatingFromCatalog(id, catalogVersion, currency string, fetchedAt time.Time) economics.Snapshot[economics.RatingCatalogView] {
	if id == "" {
		id = "rating"
	}
	if catalogVersion == "" {
		catalogVersion = "static"
	}
	if fetchedAt.IsZero() {
		fetchedAt = time.Now().UTC()
	}
	return economics.Snapshot[economics.RatingCatalogView]{
		ID:          id,
		Version:     catalogVersion,
		EffectiveAt: fetchedAt,
		FetchedAt:   fetchedAt,
		State:       economics.SnapshotReady,
		Value: economics.RatingCatalogView{
			Currency:       currency,
			CatalogVersion: catalogVersion,
		},
	}
}

func clonePolicySnap(in economics.Snapshot[economics.PolicyRulesView]) economics.Snapshot[economics.PolicyRulesView] {
	out := in
	if len(in.Value.Payload) > 0 {
		out.Value.Payload = append([]byte(nil), in.Value.Payload...)
	}
	return out
}

var (
	_ economics.RuleSnapshotSource   = (*MemoryRuleSource)(nil)
	_ economics.RatingSnapshotSource = (*MemoryRatingSource)(nil)
)
