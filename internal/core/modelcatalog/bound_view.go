package modelcatalog

import (
	"context"
	"slices"
)

type boundViewKey struct{}

// BoundView is an immutable, request-scoped catalog publication.
// It freezes the active snapshot index/generation (or explicit no-snapshot state)
// so a later CatalogRuntime refresh cannot alter an in-flight request.
//
// BoundView never exposes the mutable CatalogRuntime, its mutexes, or atomics.
type BoundView struct {
	snap *Snapshot
}

// EmptyBoundView returns a safe empty view (nil/unavailable runtime).
func EmptyBoundView() BoundView {
	return BoundView{}
}

// BoundView captures the current catalog snapshot pointer exactly once.
// A nil receiver returns an empty view.
func (r *CatalogRuntime) BoundView() BoundView {
	if r == nil {
		return EmptyBoundView()
	}
	return BoundView{snap: r.active.Load()}
}

// WithBoundView attaches v to ctx for request-scoped consumers.
func WithBoundView(ctx context.Context, v BoundView) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, boundViewKey{}, v)
}

// BoundViewFromContext returns the request-bound catalog view when present.
func BoundViewFromContext(ctx context.Context) (BoundView, bool) {
	if ctx == nil {
		return BoundView{}, false
	}
	v, ok := ctx.Value(boundViewKey{}).(BoundView)
	return v, ok
}

// Active reports whether a snapshot was bound.
func (v BoundView) Active() bool {
	return v.snap != nil && v.snap.Index != nil
}

// Generation returns the frozen catalog snapshot generation.
func (v BoundView) Generation() string {
	if v.snap == nil {
		return ""
	}
	return v.snap.Generation
}

// Snapshot returns a deep-cloned snapshot value so callers cannot mutate the
// active publication through BoundView (WirePayload is cloned; Index is immutable).
func (v BoundView) Snapshot() (Snapshot, bool) {
	if v.snap == nil {
		return Snapshot{}, false
	}
	out := *v.snap
	out.WirePayload = slices.Clone(v.snap.WirePayload)
	return out, true
}

// ActiveIndex implements [ActiveSnapshotProvider] for the frozen snapshot.
func (v BoundView) ActiveIndex() (*SnapshotIndex, SnapshotRef) {
	if v.snap == nil {
		return nil, SnapshotRef{}
	}
	ref := SnapshotRef{Generation: v.snap.Generation}
	if v.snap.Index == nil {
		return nil, ref
	}
	return v.snap.Index, ref
}

var _ ActiveSnapshotProvider = BoundView{}
