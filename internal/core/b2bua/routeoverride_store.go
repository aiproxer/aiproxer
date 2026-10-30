package b2bua

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Snapshot returns a complete value copy of the A-leg override state.
func (s *MemoryStore) Snapshot(ctx context.Context, aLegID string) (routeoverride.State, error) {
	return s.readOverride(ctx, aLegID)
}

// Get uses the same read semantics as Snapshot.
func (s *MemoryStore) Get(ctx context.Context, aLegID string) (routeoverride.State, error) {
	return s.readOverride(ctx, aLegID)
}

func (s *MemoryStore) readOverride(ctx context.Context, aLegID string) (routeoverride.State, error) {
	if err := ctx.Err(); err != nil {
		return routeoverride.State{}, err
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	now := s.nowTime()
	st, retiredID, err := s.legForOverrideLocked(aLegID, now)
	if retiredID != "" {
		retired = append(retired, retiredID)
	}
	if err != nil {
		return routeoverride.State{}, err
	}
	out, err := copyOverride(aLegID, st.override)
	if err != nil {
		return routeoverride.State{}, err
	}
	st.record.LastSeenAt = now
	return out, nil
}

// Replace activates or replaces the A-leg override selector.
func (s *MemoryStore) Replace(ctx context.Context, aLegID, selector string, now time.Time) (routeoverride.State, error) {
	if err := ctx.Err(); err != nil {
		return routeoverride.State{}, err
	}
	normalized, err := normalizeStoredSelector(selector)
	if err != nil {
		return routeoverride.State{}, err
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	seen := s.nowTime()
	st, retiredID, err := s.legForOverrideLocked(aLegID, seen)
	if retiredID != "" {
		retired = append(retired, retiredID)
	}
	if err != nil {
		return routeoverride.State{}, err
	}
	current, err := copyOverride(aLegID, st.override)
	if err != nil {
		return routeoverride.State{}, err
	}
	if current.Active && current.Selector == normalized {
		st.record.LastSeenAt = seen
		return current, nil
	}
	nextRev, err := nextOverrideRevision(current.Revision)
	if err != nil {
		return routeoverride.State{}, err
	}
	next := routeoverride.State{
		ALegID:    aLegID,
		Active:    true,
		Selector:  normalized,
		Revision:  nextRev,
		UpdatedAt: now.UTC(),
	}
	if err := next.Validate(); err != nil {
		return routeoverride.State{}, fmt.Errorf("%w: %v", routeoverride.ErrInvalidSelector, err)
	}
	st.override = next
	st.record.LastSeenAt = seen
	return next.Clone(), nil
}

// Clear deactivates the A-leg override. Already-inactive state is a no-op.
func (s *MemoryStore) Clear(ctx context.Context, aLegID string, now time.Time) (routeoverride.State, error) {
	if err := ctx.Err(); err != nil {
		return routeoverride.State{}, err
	}
	retired := s.lockForOperation()
	defer func() { s.unlockForOperation(retired) }()
	seen := s.nowTime()
	st, retiredID, err := s.legForOverrideLocked(aLegID, seen)
	if retiredID != "" {
		retired = append(retired, retiredID)
	}
	if err != nil {
		return routeoverride.State{}, err
	}
	current, err := copyOverride(aLegID, st.override)
	if err != nil {
		return routeoverride.State{}, err
	}
	if !current.Active {
		st.record.LastSeenAt = seen
		return current, nil
	}
	nextRev, err := nextOverrideRevision(current.Revision)
	if err != nil {
		return routeoverride.State{}, err
	}
	next := routeoverride.State{
		ALegID:    aLegID,
		Revision:  nextRev,
		UpdatedAt: now.UTC(),
	}
	if err := next.Validate(); err != nil {
		return routeoverride.State{}, err
	}
	st.override = next
	st.record.LastSeenAt = seen
	return next.Clone(), nil
}

func (s *MemoryStore) legForOverrideLocked(aLegID string, now time.Time) (*legState, string, error) {
	if aLegID == "" {
		return nil, "", routeoverride.ErrNotFound
	}
	st, ok := s.legs[aLegID]
	if !ok {
		return nil, "", routeoverride.ErrNotFound
	}
	if s.evictIfStaleLocked(st, now) {
		return nil, st.record.ALegID, routeoverride.ErrNotFound
	}
	return st, "", nil
}

func copyOverride(aLegID string, ov routeoverride.State) (routeoverride.State, error) {
	out := ov.Clone()
	out.ALegID = aLegID
	if out.Revision == 0 && !out.Active && out.Selector == "" && out.UpdatedAt.IsZero() {
		return routeoverride.Inactive(aLegID), nil
	}
	if err := out.Validate(); err != nil {
		return routeoverride.State{}, fmt.Errorf("b2bua: stored route override: %w", err)
	}
	return out, nil
}

func normalizeStoredSelector(raw string) (string, error) {
	normalized := routeoverride.NormalizeSelector(raw)
	if normalized == "" {
		return "", fmt.Errorf("%w: empty selector", routeoverride.ErrInvalidSelector)
	}
	if len(normalized) > aipapi.MaxRouteSelectorBytes {
		return "", fmt.Errorf("%w: selector exceeds %d bytes", routeoverride.ErrInvalidSelector, aipapi.MaxRouteSelectorBytes)
	}
	return normalized, nil
}

func nextOverrideRevision(current int64) (int64, error) {
	if current == math.MaxInt64 {
		return 0, routeoverride.ErrRevisionExhausted
	}
	return current + 1, nil
}
