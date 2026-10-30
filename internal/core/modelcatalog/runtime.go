package modelcatalog

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// RefreshFailureCategory classifies the last catalog refresh failure for diagnostics.
type RefreshFailureCategory string

const (
	// RefreshFailureNone means no failure since the last successful refresh (or startup).
	RefreshFailureNone RefreshFailureCategory = ""
	// RefreshFailureFetch indicates a transport or HTTP-layer failure from the snapshot source.
	RefreshFailureFetch RefreshFailureCategory = "fetch"
	// RefreshFailureParse indicates decode, validation, or unsupported schema from fetched bytes.
	RefreshFailureParse RefreshFailureCategory = "parse"
	// RefreshFailureCache indicates a local persistence failure after a successful fetch.
	RefreshFailureCache RefreshFailureCategory = "cache"
)

// RuntimeConfig wires optional catalog refresh for [CatalogRuntime].
type RuntimeConfig struct {
	Source SnapshotSource
	Cache  SnapshotCache
}

// CatalogRuntime coordinates local cache loading and publication of the active immutable snapshot
// for request-time readers. Background refresh ticks are started by the composition root
// (see internal/infra/runtimebundle), which calls [CatalogRuntime.RunRefresh] on a schedule.
type CatalogRuntime struct {
	cfg RuntimeConfig

	lifecycleMu sync.Mutex
	started     bool

	active atomic.Pointer[Snapshot]

	mu       sync.Mutex
	lastFail RefreshFailureCategory
}

// NewCatalogRuntime builds a coordinator. Source and Cache may be nil when updates are disabled
// and no cache load is required (callers should still pass non-nil cache for typical startup).
func NewCatalogRuntime(cfg RuntimeConfig) *CatalogRuntime {
	return &CatalogRuntime{cfg: cfg}
}

// Start loads the local cache once and publishes a valid snapshot when present. It does not run
// network refresh; the composition root calls [CatalogRuntime.RunRefresh] when configured.
func (r *CatalogRuntime) Start(parent context.Context) error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	if r.started {
		return errors.New("modelcatalog runtime: already started")
	}
	r.started = true

	if r.cfg.Cache != nil {
		snap, err := r.cfg.Cache.Load(parent)
		if err == nil && snap.Index != nil {
			cp := snap
			r.active.Store(&cp)
		}
	}
	return nil
}

// RunRefresh fetches a remote snapshot when a source is configured, validates it, persists via cache,
// and publishes the active view. ctx governs fetch/save cancellation.
func (r *CatalogRuntime) RunRefresh(ctx context.Context) {
	if r.cfg.Source == nil {
		return
	}
	snap, err := r.cfg.Source.Fetch(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		r.setFailure(RefreshFailureFetch)
		return
	}
	if snap.Index == nil {
		r.setFailure(RefreshFailureParse)
		return
	}
	if r.cfg.Cache != nil {
		if err := r.cfg.Cache.Save(ctx, snap); err != nil {
			r.setFailure(RefreshFailureCache)
			return
		}
	}
	cp := snap
	r.active.Store(&cp)
	r.setOK()
}

func (r *CatalogRuntime) setFailure(cat RefreshFailureCategory) {
	r.mu.Lock()
	r.lastFail = cat
	r.mu.Unlock()
}

func (r *CatalogRuntime) setOK() {
	r.mu.Lock()
	r.lastFail = RefreshFailureNone
	r.mu.Unlock()
}

// Active returns the current immutable snapshot handle for routing-time use.
// The second result is false when no snapshot has been published yet.
func (r *CatalogRuntime) Active() (Snapshot, bool) {
	p := r.active.Load()
	if p == nil {
		return Snapshot{}, false
	}
	return *p, true
}

// LastRefreshFailure returns the failure category from the most recent refresh attempt.
func (r *CatalogRuntime) LastRefreshFailure() RefreshFailureCategory {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastFail
}

// ActiveIndex implements [ActiveSnapshotProvider]: the current catalog index and generation for routing.
func (r *CatalogRuntime) ActiveIndex() (*SnapshotIndex, SnapshotRef) {
	if r == nil {
		return nil, SnapshotRef{}
	}
	snap, ok := r.Active()
	if !ok {
		return nil, SnapshotRef{}
	}
	ref := SnapshotRef{Generation: snap.Generation}
	if snap.Index == nil {
		return nil, ref
	}
	return snap.Index, ref
}

// PublishSnapshot atomically publishes snap as the active catalog view.
// Production refresh uses [CatalogRuntime.RunRefresh]; this helper supports
// composition-root seeding and deterministic tests without network I/O.
func (r *CatalogRuntime) PublishSnapshot(snap Snapshot) {
	if r == nil {
		return
	}
	cp := snap
	r.active.Store(&cp)
}

// Close marks the runtime stopped. External refresh workers must be stopped separately by the composition root.
func (r *CatalogRuntime) Close() error {
	r.lifecycleMu.Lock()
	defer r.lifecycleMu.Unlock()
	r.started = false
	return nil
}
