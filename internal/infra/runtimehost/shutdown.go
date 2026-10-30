package runtimehost

import (
	"context"
	"errors"
)

// BeginShutdown prevents new lease acquisitions and candidate publication.
// In-flight leases and pins keep their retains; pinned generations are not
// force-closed (req 13.x, 5.7). Publication that already holds Manager.mu may
// still complete; DetachActive observes any resulting active generation.
func (m *Manager) BeginShutdown() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.shuttingDown.Store(true)
	m.mu.Unlock()
}

// ShuttingDown reports whether BeginShutdown has been called.
func (m *Manager) ShuttingDown() bool {
	if m == nil {
		return true
	}
	return m.shuttingDown.Load()
}

// DetachActive clears the active pointer, marks the prior generation retiring,
// and retains it for drain/close. It does not close generation-owned resources.
func (m *Manager) DetachActive() *Generation {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prior := m.active.Swap(nil)
	if prior == nil {
		return nil
	}
	prior.markRetiring()
	m.retained = append(m.retained, prior)
	return prior
}

// SnapshotRetained returns a defensive copy of currently retained generations.
func (m *Manager) SnapshotRetained() []*Generation {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Generation, len(m.retained))
	copy(out, m.retained)
	return out
}

// ShutdownDetached prevents new acquisitions, detaches the active generation,
// and retires every retained generation with context-bounded drain waiting.
// Retained generations are retired concurrently (bounded by the finite
// retention budget) so one pinned generation cannot block unrelated drained
// generations from closing. A context timeout/cancel returns an error without
// force-closing a still-pinned generation. Per-generation retirement
// admission (Generation.retireAdmit) safely interleaves with any
// already-scheduled automatic post-publish retirement for the same
// generation — at most one retirement attempt runs at a time per generation.
//
// Fan-out uses a buffered result channel (not a wait-group) so request/lease
// refcounting remains packed-atomic (req 10.4) while shutdown retires each
// retained generation via RetireGeneration.
func (m *Manager) ShutdownDetached(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.BeginShutdown()
	m.DetachActive()
	gens := m.SnapshotRetained()

	n := 0
	for _, g := range gens {
		if g != nil {
			n++
		}
	}
	errCh := make(chan error, n)
	for _, g := range gens {
		if g == nil {
			continue
		}
		go func(g *Generation) {
			_, err := m.RetireGeneration(ctx, g)
			if err != nil && !errors.Is(err, ErrAlreadyClosed) {
				errCh <- err
				return
			}
			errCh <- nil
		}(g)
	}
	var out error
	for i := 0; i < n; i++ {
		out = errors.Join(out, <-errCh)
	}
	m.SweepClosed()
	return out
}
