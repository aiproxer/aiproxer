package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/uptrace/bun"
)

type PoolOpener func(context.Context, string, PoolSettings) (*bun.DB, error)

// noCopy signals go vet's copylocks analyzer to reject accidental copies.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

type postgresPoolKey struct {
	dsn  string
	pool PoolSettings
}

type postgresPoolEntry struct {
	db     *bun.DB
	claims int
}

// PoolRegistry owns pools created during one runtime build. It is not a
// global registry and is only used by the composition root during startup.
//
// Runtime construction opens pools sequentially. openGate keeps that
// invariant if a caller does invoke Open concurrently, while still allowing a
// waiting caller's context to cancel. The mutex protects the registry map so
// Stats and Close can safely run concurrently with callers that inspect it.
type PoolRegistry struct {
	noCopy noCopy
	mu     sync.Mutex
	pools  map[postgresPoolKey]postgresPoolEntry
	gate   chan struct{}
	opener PoolOpener
	closed bool
	// closePool optionally overrides pool close (tests inject drain failures).
	closePool func(*bun.DB) error
	// beforeGateWait is invoked after Close marks the registry closed and
	// immediately before waiting on the open gate (tests only).
	beforeGateWait func()
}

func NewPoolRegistry(opener PoolOpener) *PoolRegistry {
	if opener == nil {
		opener = OpenPostgresBun
	}
	return &PoolRegistry{
		pools:  make(map[postgresPoolKey]postgresPoolEntry),
		gate:   newPoolOpenGate(),
		opener: opener,
	}
}

func newPoolOpenGate() chan struct{} {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return gate
}

// Open returns the pool for a sanitized DSN and pool-settings key, creating it
// on the first request. The caller's context is passed directly to the opener
// so cancellation and deadlines stop a pending connection attempt.
func (r *PoolRegistry) Open(ctx context.Context, dsn string, pool PoolSettings) (*bun.DB, error) {
	if r == nil {
		return nil, fmt.Errorf("db: nil postgres pool registry")
	}
	if ctx == nil {
		return nil, ErrNilContext
	}
	sanitized, err := SanitizePostgresDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: sanitize postgres pool key: %w", err)
	}
	key := postgresPoolKey{dsn: sanitized, pool: pool}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("db: postgres pool registry is closed")
	}
	r.ensureReadyLocked()
	if existing, ok := r.pools[key]; ok {
		r.mu.Unlock()
		return existing.db, nil
	}
	gate := r.gate
	r.mu.Unlock()

	if err := acquirePoolOpen(ctx, gate); err != nil {
		return nil, err
	}
	defer releasePoolOpen(gate)

	// A previous opener may have populated this key while this caller waited.
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, fmt.Errorf("db: postgres pool registry is closed")
	}
	if existing, ok := r.pools[key]; ok {
		r.mu.Unlock()
		return existing.db, nil
	}
	opener := r.opener
	r.mu.Unlock()

	opened, err := opener(ctx, sanitized, pool)
	if err == nil && opened == nil {
		err = fmt.Errorf("db: postgres opener returned nil db")
	}
	if err != nil {
		return nil, r.closeOpenedPool(opened, err)
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, r.closeOpenedPool(opened, fmt.Errorf("db: postgres pool registry closed during open"))
	}
	r.pools[key] = postgresPoolEntry{db: opened}
	r.mu.Unlock()
	return opened, nil
}

func acquirePoolOpen(ctx context.Context, gate chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate:
		return nil
	}
}

func releasePoolOpen(gate chan struct{}) {
	gate <- struct{}{}
}

func (r *PoolRegistry) closePoolHandle(db *bun.DB) error {
	if db == nil {
		return nil
	}
	if r != nil && r.closePool != nil {
		return r.closePool(db)
	}
	return db.Close()
}

func (r *PoolRegistry) closeOpenedPool(db *bun.DB, cause error) error {
	if db == nil {
		return cause
	}
	if err := r.closePoolHandle(db); err != nil {
		return errors.Join(cause, fmt.Errorf("db: close postgres pool after failed open: %w", err))
	}
	return cause
}

// Claim marks a successfully bound store against a registry-owned pool so
// [PruneUnclaimed] will retain it. Open does not claim.
func (r *PoolRegistry) Claim(db *bun.DB) error {
	if r == nil {
		return fmt.Errorf("db: nil postgres pool registry")
	}
	if db == nil {
		return fmt.Errorf("db: claim nil postgres pool")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("db: postgres pool registry is closed")
	}
	r.ensureReadyLocked()
	for key, entry := range r.pools {
		if entry.db == db {
			entry.claims++
			r.pools[key] = entry
			return nil
		}
	}
	return fmt.Errorf("db: claim unknown postgres pool")
}

// ensureReadyLocked initializes the zero value. Caller must hold r.mu.
func (r *PoolRegistry) ensureReadyLocked() {
	if r.pools == nil {
		r.pools = make(map[postgresPoolKey]postgresPoolEntry)
	}
	if r.gate == nil {
		r.gate = newPoolOpenGate()
	}
	if r.opener == nil {
		r.opener = OpenPostgresBun
	}
}

// PruneUnclaimed closes and removes pools that were opened but never [Claim]ed.
// Claimed pools are retained for later [Close]. Safe to call when closed (no-op).
//
// PruneUnclaimed is a build-phase barrier: the composition root calls it after
// all Open/Claim operations have completed, so no pool can become claimable
// while this method is selecting the unclaimed set. Callers must not invoke
// Open or Claim concurrently with PruneUnclaimed.
func (r *PoolRegistry) PruneUnclaimed() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	var toClose []*bun.DB
	for key, entry := range r.pools {
		if entry.claims > 0 {
			continue
		}
		toClose = append(toClose, entry.db)
		delete(r.pools, key)
	}
	r.mu.Unlock()

	var errs []error
	for _, pool := range toClose {
		if err := r.closePoolHandle(pool); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Close marks the registry closed, waits for any in-flight Open (honoring ctx),
// then closes every pool in the snapshot taken at mark time. Cancellation still
// closes that snapshot and does not steal the open gate: an in-flight opener
// observes closed, closes any pool it created, and releases the gate.
func (r *PoolRegistry) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return ErrNilContext
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	pools := r.pools
	r.pools = nil
	r.ensureReadyLocked()
	gate := r.gate
	beforeWait := r.beforeGateWait
	r.mu.Unlock()

	if beforeWait != nil {
		beforeWait()
	}

	var waitErr error
	select {
	case <-ctx.Done():
		waitErr = ctx.Err()
	case <-gate:
		releasePoolOpen(gate)
	}

	var errs []error
	if waitErr != nil {
		errs = append(errs, waitErr)
	}
	for _, entry := range pools {
		if err := r.closePoolHandle(entry.db); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *PoolRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pools)
}

// Stats returns a snapshot of database/sql pool statistics for every
// registry-owned pool, aggregated for observability. Handles are copied under
// the registry mutex; Stats() is called outside the lock so scrapes do not hold
// the mutex across driver reads. Safe for concurrent use; order of entries is
// unspecified.
func (r *PoolRegistry) Stats() []sql.DBStats {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	dbs := make([]*bun.DB, 0, len(r.pools))
	for _, entry := range r.pools {
		if entry.db != nil {
			dbs = append(dbs, entry.db)
		}
	}
	r.mu.Unlock()
	out := make([]sql.DBStats, 0, len(dbs))
	for _, db := range dbs {
		out = append(out, db.Stats())
	}
	return out
}
