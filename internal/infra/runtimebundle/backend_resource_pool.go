package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

var (
	errBackendResourcePoolClosed  = errors.New("runtimebundle: backend resource pool is closed")
	errBackendResourceBuilderNil  = errors.New("runtimebundle: backend resource builder is nil")
	errBackendResourceUnavailable = errors.New("runtimebundle: backend resource is unavailable")
	errBackendResourceLifecycle   = errors.New("runtimebundle: backend resource lifecycle callback is incompatible with pooling")
)

type backendResourceState uint8

const (
	backendResourceBuilding backendResourceState = iota
	backendResourceLive
	backendResourceDetached
	backendResourceFailed
)

// backendResourcePool is the process-scoped owner of reusable physical
// connector entries. current contains only the currently acquirable
// incarnation for an identity; owned retains every successful incarnation
// until its physical cleanup has completed, including detached entries.
type backendResourcePool struct {
	mu          sync.Mutex
	current     map[backendResourceIdentity]*backendResourceEntry
	owned       map[*backendResourceEntry]struct{}
	closing     bool
	nextInc     uint64
	buildCtx    context.Context
	cancelBuild context.CancelFunc
	buildWG     sync.WaitGroup
	handoffWG   sync.WaitGroup
	closeDone   chan struct{}
	closeErr    error
}

type backendResourceEntry struct {
	identity    backendResourceIdentity
	incarnation uint64
	state       backendResourceState
	claims      int
	ready       chan struct{}

	backend  execbackend.Backend
	cleanup  func() error
	buildErr error

	cleanupOnce      sync.Once
	cleanupErr       error
	cleanupScheduled bool
}

type backendResourceLease struct {
	pool  *backendResourcePool
	entry *backendResourceEntry
	once  sync.Once
	err   error
}

func newBackendResourcePool() *backendResourcePool {
	buildCtx, cancelBuild := context.WithCancel(context.Background())
	return &backendResourcePool{
		current:     make(map[backendResourceIdentity]*backendResourceEntry),
		owned:       make(map[*backendResourceEntry]struct{}),
		buildCtx:    buildCtx,
		cancelBuild: cancelBuild,
		closeDone:   make(chan struct{}),
	}
}

// Acquire reserves a claim before waiting for a building entry. The first
// claimant starts one builder outside the mutex; every other claimant joins
// that entry and reserves its own claim before waiting on ready.
func (p *backendResourcePool) Acquire(
	ctx context.Context,
	identity backendResourceIdentity,
	builder func(context.Context, uint64) (execbackend.Backend, func() error, error),
) (pluginreg.BackendBuildResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return pluginreg.BackendBuildResult{}, err
	}
	if builder == nil {
		return pluginreg.BackendBuildResult{}, errBackendResourceBuilderNil
	}

	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		return pluginreg.BackendBuildResult{}, errBackendResourcePoolClosed
	}
	// Add is serialized with Close's closing transition. Close can therefore
	// wait for every Acquire that may still hand off a result.
	p.handoffWG.Add(1)
	defer p.handoffWG.Done()

	entry := p.current[identity]
	startBuild := false
	if entry == nil {
		p.nextInc++
		entry = &backendResourceEntry{
			identity:    identity,
			incarnation: p.nextInc,
			state:       backendResourceBuilding,
			claims:      1, // reserve the initiating claimant before starting build
			ready:       make(chan struct{}),
		}
		p.current[identity] = entry
		// Add is under mu so Close cannot miss a builder that has been
		// admitted but whose goroutine has not started yet.
		p.buildWG.Add(1)
		startBuild = true
	} else {
		// current only contains live or building entries. Reserve every
		// claimant before waiting so an early release cannot clean the entry
		// before a scheduled waiter completes its handoff.
		entry.claims++
	}
	p.mu.Unlock()

	if startBuild {
		go func() {
			defer p.buildWG.Done()
			p.build(p.buildCtx, entry, builder)
		}()
	}
	return p.await(ctx, entry)
}

func (p *backendResourcePool) build(
	buildCtx context.Context,
	entry *backendResourceEntry,
	builder func(context.Context, uint64) (execbackend.Backend, func() error, error),
) {
	backend, cleanup, err := builder(buildCtx, entry.incarnation)
	p.mu.Lock()
	entry.cleanup = cleanup
	p.mu.Unlock()
	if err == nil {
		err = pooledBackendLifecycleError(backend)
	}
	if err != nil {
		// A builder may return a cleanup capability for a partially constructed
		// resource alongside its failure.  Route it through the entry-level
		// cleanup-once authority before publishing the failure, and keep both
		// failures visible to Acquire.  Physical cleanup must remain outside the
		// pool mutex so callbacks can safely use pool state.
		cleanupErr := entry.cleanupPhysical(p)
		buildErr := err
		if cleanupErr != nil {
			buildErr = errors.Join(err, cleanupErr)
		}

		p.mu.Lock()
		entry.buildErr = buildErr
		entry.state = backendResourceFailed
		if p.current[entry.identity] == entry {
			delete(p.current, entry.identity)
		}
		close(entry.ready)
		p.mu.Unlock()
		return
	}

	backend = stripPooledBackendLifecycle(backend)
	p.mu.Lock()
	entry.backend = backend
	p.owned[entry] = struct{}{}
	if p.closing || p.current[entry.identity] != entry || entry.claims == 0 {
		closing := p.closing
		entry.state = backendResourceDetached
		entry.cleanupScheduled = true
		if p.current[entry.identity] == entry {
			delete(p.current, entry.identity)
		}
		close(entry.ready)
		p.mu.Unlock()
		// During terminal Close, defer cleanup until Close has joined all
		// handoffs. This prevents a late result from tearing down the physical
		// resource before the waiting Acquire calls have returned.
		if !closing {
			_ = entry.cleanupPhysical(p)
		}
		return
	}
	entry.state = backendResourceLive
	close(entry.ready)
	p.mu.Unlock()
}

func (p *backendResourcePool) await(
	ctx context.Context,
	entry *backendResourceEntry,
) (pluginreg.BackendBuildResult, error) {
	select {
	case <-ctx.Done():
		_ = p.releaseClaim(entry)
		return pluginreg.BackendBuildResult{}, ctx.Err()
	case <-entry.ready:
	}

	if err := ctx.Err(); err != nil {
		_ = p.releaseClaim(entry)
		return pluginreg.BackendBuildResult{}, err
	}

	p.mu.Lock()
	if entry.state == backendResourceLive && !p.closing && p.current[entry.identity] == entry {
		backend := entry.backend
		p.mu.Unlock()
		lease := &backendResourceLease{pool: p, entry: entry}
		return pluginreg.BackendBuildResult{Backend: backend, Cleanup: lease.release}, nil
	}
	err := entry.buildErr
	if err == nil {
		if p.closing {
			err = errBackendResourcePoolClosed
		} else {
			err = errBackendResourceUnavailable
		}
	}
	p.mu.Unlock()
	_ = p.releaseClaim(entry)
	return pluginreg.BackendBuildResult{}, err
}

// Invalidate detaches only the exact current incarnation. Existing leases
// retain their claims and ownership until their normal release or Pool.Close.
func (p *backendResourcePool) Invalidate(identity backendResourceIdentity, incarnation uint64) {
	p.mu.Lock()
	entry := p.current[identity]
	if entry == nil || entry.incarnation != incarnation {
		p.mu.Unlock()
		return
	}
	delete(p.current, identity)
	entry.state = backendResourceDetached
	p.mu.Unlock()
}

// Close is the terminal pool boundary. It linearizes closing under mu, then
// cancels and joins admitted builders and Acquire handoffs before snapshotting
// residual ownership. Concurrent callers wait for the same completion and see
// the stored cleanup result.
func (p *backendResourcePool) Close() error {
	p.mu.Lock()
	if p.closing {
		done := p.closeDone
		p.mu.Unlock()
		<-done
		p.mu.Lock()
		err := p.closeErr
		p.mu.Unlock()
		return err
	}
	p.closing = true
	cancelBuild := p.cancelBuild
	p.mu.Unlock()

	if cancelBuild != nil {
		cancelBuild()
	}
	// No mutex is held while joining goroutines or running physical cleanup.
	p.buildWG.Wait()
	p.handoffWG.Wait()

	p.mu.Lock()
	for identity, entry := range p.current {
		delete(p.current, identity)
		if entry.state != backendResourceFailed {
			entry.state = backendResourceDetached
		}
	}
	entries := make([]*backendResourceEntry, 0, len(p.owned))
	for entry := range p.owned {
		if entry.state != backendResourceFailed {
			entry.state = backendResourceDetached
		}
		entry.cleanupScheduled = true
		entries = append(entries, entry)
	}
	p.mu.Unlock()

	var closeErr error
	for _, entry := range entries {
		closeErr = errors.Join(closeErr, entry.cleanupPhysical(p))
	}
	p.mu.Lock()
	p.closeErr = closeErr
	close(p.closeDone)
	p.mu.Unlock()
	return closeErr
}

func (p *backendResourcePool) releaseClaim(entry *backendResourceEntry) error {
	p.mu.Lock()
	if entry.claims > 0 {
		entry.claims--
	}
	shouldCleanup := entry.claims == 0 &&
		(entry.state == backendResourceLive || entry.state == backendResourceDetached) &&
		!entry.cleanupScheduled
	if shouldCleanup {
		if p.current[entry.identity] == entry {
			delete(p.current, entry.identity)
		}
		entry.state = backendResourceDetached
		entry.cleanupScheduled = true
	}
	p.mu.Unlock()

	if shouldCleanup {
		return entry.cleanupPhysical(p)
	}
	return nil
}

func (l *backendResourceLease) release() error {
	l.once.Do(func() {
		if l.pool == nil || l.entry == nil {
			return
		}
		l.err = l.pool.releaseClaim(l.entry)
	})
	return l.err
}

func (entry *backendResourceEntry) cleanupPhysical(pool *backendResourcePool) error {
	entry.cleanupOnce.Do(func() {
		if entry.cleanup != nil {
			entry.cleanupErr = entry.cleanup()
		}
		pool.mu.Lock()
		delete(pool.owned, entry)
		pool.mu.Unlock()
	})
	return entry.cleanupErr
}

// A pooled generation receives only query/execution behavior. Physical
// lifecycle callbacks remain owned by the reconciliation entry, preventing a
// generation ledger from closing a connector retained by another generation.
func stripPooledBackendLifecycle(backend execbackend.Backend) execbackend.Backend {
	backend.Close = nil
	backend.Start = nil
	backend.Stop = nil
	backend.CleanupIdleTransports = nil
	return backend
}

func pooledBackendLifecycleError(backend execbackend.Backend) error {
	switch {
	case backend.Close != nil:
		return fmt.Errorf("%w: Close", errBackendResourceLifecycle)
	case backend.Start != nil:
		return fmt.Errorf("%w: Start", errBackendResourceLifecycle)
	case backend.Stop != nil:
		return fmt.Errorf("%w: Stop", errBackendResourceLifecycle)
	case backend.CleanupIdleTransports != nil:
		return fmt.Errorf("%w: CleanupIdleTransports", errBackendResourceLifecycle)
	case backend.PreflightCapability != nil:
		return fmt.Errorf("%w: PreflightCapability", errBackendResourceLifecycle)
	default:
		return nil
	}
}
