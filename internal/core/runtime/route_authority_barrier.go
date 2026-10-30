package runtime

import (
	"context"
	"sync"
	"sync/atomic"
)

type routeAuthoritySnapshotBarrierKey struct{}

// routeAuthoritySnapshotBarrier is a deterministic test gate after the
// per-turn override snapshot and before submit/request stages and route-plan
// construction. Production requests do not install a barrier, so
// waitRouteAuthoritySnapshotBarrier is a no-op.
type routeAuthoritySnapshotBarrier struct {
	arrivedOnce sync.Once
	arrived     chan struct{}
	releaseOnce sync.Once
	release     chan struct{}
	aLegID      atomic.Value
}

func newRouteAuthoritySnapshotBarrier() *routeAuthoritySnapshotBarrier {
	return &routeAuthoritySnapshotBarrier{
		arrived: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func withRouteAuthoritySnapshotBarrier(ctx context.Context, b *routeAuthoritySnapshotBarrier) context.Context {
	if ctx == nil || b == nil {
		return ctx
	}
	return context.WithValue(ctx, routeAuthoritySnapshotBarrierKey{}, b)
}

func (b *routeAuthoritySnapshotBarrier) signalArrived() {
	if b == nil {
		return
	}
	b.arrivedOnce.Do(func() { close(b.arrived) })
}

func (b *routeAuthoritySnapshotBarrier) waitUntilArrived(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-b.arrived:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *routeAuthoritySnapshotBarrier) releaseWaiters() {
	if b == nil {
		return
	}
	b.releaseOnce.Do(func() { close(b.release) })
}

func (b *routeAuthoritySnapshotBarrier) resolvedALegID() string {
	if b == nil {
		return ""
	}
	s, _ := b.aLegID.Load().(string)
	return s
}

func waitRouteAuthoritySnapshotBarrier(ctx context.Context, aLegID string) error {
	if ctx == nil {
		return nil
	}
	b, _ := ctx.Value(routeAuthoritySnapshotBarrierKey{}).(*routeAuthoritySnapshotBarrier)
	if b == nil {
		return nil
	}
	b.aLegID.Store(aLegID)
	b.signalArrived()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-b.release:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
