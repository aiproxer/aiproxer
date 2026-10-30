package leasestore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	_ "modernc.org/sqlite"
)

// TestRenewCAS_GenerationOnlyWouldResurrectReleased characterizes the occupancy
// corruption race (reqs 10.7, 10.8, 16.2): Release keeps generation, so a Renew
// UPDATE keyed only on generation can revive a released row.
func TestRenewCAS_GenerationOnlyWouldResurrectReleased(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newInternalSQLiteStore(t, "cas-char")
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	leaseID := "lease-gen-only"
	acq, err := store.Acquire(ctx, internalAcquireCmd(leaseID, "req-1", now, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	gen := acq.Lease.Generation
	if _, err := store.Release(ctx, app.ReleaseCommand{LeaseID: leaseID, Now: now}); err != nil {
		t.Fatal(err)
	}
	assertLeaseState(t, store, leaseID, now, domain.LeaseStateReleased)

	// Stale Renew write values (domain.Renew on a pre-Release active copy).
	renewedAt := now.Add(5 * time.Second).UnixNano()
	expiresAt := now.Add(time.Minute).UnixNano()
	res, err := store.db.NewRaw(
		`
UPDATE concurrency_leases SET
	renewed_at_unix=?, expires_at_unix=?, generation=?, state=?
WHERE store_id=? AND lease_id=? AND generation=?
`,
		renewedAt, expiresAt, gen+1, string(domain.LeaseStateActive),
		store.cfg.StoreID, leaseID, gen,
	).Exec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Fatalf("generation-only UPDATE should resurrect released lease; affected=%d", affected)
	}
	assertLeaseState(t, store, leaseID, now, domain.LeaseStateActive)
}

// TestRenewCAS_ActiveStatePredicateBlocksResurrection proves the production Renew
// CAS preimage requires an active/expiring row so Release cannot be undone.
func TestRenewCAS_ActiveStatePredicateBlocksResurrection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newInternalSQLiteStore(t, "cas-pred")
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	leaseID := "lease-pred"
	acq, err := store.Acquire(ctx, internalAcquireCmd(leaseID, "req-1", now, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	stale := acq.Lease
	if _, err := store.Release(ctx, app.ReleaseCommand{LeaseID: leaseID, Now: now}); err != nil {
		t.Fatal(err)
	}
	assertLeaseState(t, store, leaseID, now, domain.LeaseStateReleased)

	// Simulate T1 domain.Renew on a stale active snapshot after T2 Release committed.
	if err := stale.Renew(now.Add(5*time.Second), acq.Lease.Generation, time.Minute); err != nil {
		t.Fatalf("stale domain.Renew should succeed on active copy: %v", err)
	}
	row, err := leaseToRow(store.cfg.StoreID, stale, string(stale.Dimensions.Key()))
	if err != nil {
		t.Fatal(err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	affected, err := store.renewCASUpdate(ctx, tx, leaseID, acq.Lease.Generation, row)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("active-state Renew CAS must not match released preimage; affected=%d", affected)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertLeaseState(t, store, leaseID, now, domain.LeaseStateReleased)
}

// TestDurableStore_RenewAfterReleaseReturnsReleased ensures the public Renew path
// rejects a released lease without resurrecting (domain check + CAS).
func TestDurableStore_RenewAfterReleaseReturnsReleased(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newInternalSQLiteStore(t, "cas-after-rel")
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	leaseID := "lease-after-rel"
	acq, err := store.Acquire(ctx, internalAcquireCmd(leaseID, "req-1", now, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, app.ReleaseCommand{LeaseID: leaseID, Now: now}); err != nil {
		t.Fatal(err)
	}
	_, err = store.Renew(ctx, app.RenewCommand{
		LeaseID:            leaseID,
		ExpectedGeneration: acq.Lease.Generation,
		TTL:                time.Minute,
		Now:                now.Add(time.Second),
	})
	if !errors.Is(err, domain.ErrLeaseReleased) {
		t.Fatalf("renew after release err=%v want ErrLeaseReleased", err)
	}
	assertLeaseState(t, store, leaseID, now, domain.LeaseStateReleased)
}

// TestRenewCASMissError_AfterReleaseReturnsReleased covers the RowsAffected=0
// reload path used when CAS loses to a concurrent Release.
func TestRenewCASMissError_AfterReleaseReturnsReleased(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newInternalSQLiteStore(t, "cas-miss")
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	leaseID := "lease-miss"
	acq, err := store.Acquire(ctx, internalAcquireCmd(leaseID, "req-1", now, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, app.ReleaseCommand{LeaseID: leaseID, Now: now}); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	err = store.renewCASMissError(ctx, tx, app.RenewCommand{
		LeaseID:            leaseID,
		ExpectedGeneration: acq.Lease.Generation,
		TTL:                time.Minute,
		Now:                now.Add(time.Second),
	})
	if !errors.Is(err, domain.ErrLeaseReleased) {
		t.Fatalf("cas miss err=%v want ErrLeaseReleased", err)
	}
}

// TestDurableStore_ReleaseAlreadyReleasedIsIdempotent covers the early-return path.
func TestDurableStore_ReleaseAlreadyReleasedIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newInternalSQLiteStore(t, "release-idem")
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	leaseID := "lease-rel-idem"
	if _, err := store.Acquire(ctx, internalAcquireCmd(leaseID, "req-1", now, time.Minute)); err != nil {
		t.Fatal(err)
	}
	first, err := store.Release(ctx, app.ReleaseCommand{LeaseID: leaseID, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Applied {
		t.Fatal("first release not applied")
	}
	second, err := store.Release(ctx, app.ReleaseCommand{LeaseID: leaseID, Now: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Applied {
		t.Fatal("second release not applied")
	}
	assertLeaseState(t, store, leaseID, now, domain.LeaseStateReleased)
}

// TestReleaseCAS_StatePredicateRejectsReleasedRow ensures Release UPDATE cannot
// rewrite an already-released row when only keyed on store_id/lease_id would.
func TestReleaseCAS_StatePredicateRejectsReleasedRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newInternalSQLiteStore(t, "release-cas")
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	leaseID := "lease-rel-cas"
	if _, err := store.Acquire(ctx, internalAcquireCmd(leaseID, "req-1", now, time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release(ctx, app.ReleaseCommand{LeaseID: leaseID, Now: now}); err != nil {
		t.Fatal(err)
	}
	assertLeaseState(t, store, leaseID, now, domain.LeaseStateReleased)

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	stale := domain.Lease{
		LeaseID:    leaseID,
		State:      domain.LeaseStateReleased,
		ReleasedAt: now.Add(time.Minute),
	}
	affected, err := store.releaseCASUpdate(ctx, tx, leaseID, stale)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("affected=%d want 0 against released row", affected)
	}
	assertLeaseState(t, store, leaseID, now, domain.LeaseStateReleased)
}

func assertLeaseState(t *testing.T, store *DurableStore, leaseID string, at time.Time, want domain.LeaseState) {
	t.Helper()
	ctx := context.Background()
	q, err := store.Query(ctx, app.QueryCommand{LeaseID: leaseID, Now: at, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Leases) != 1 {
		t.Fatalf("expected 1 lease, got %d", len(q.Leases))
	}
	if q.Leases[0].State != want {
		t.Fatalf("state=%s want %s", q.Leases[0].State, want)
	}
}

func newInternalSQLiteStore(t *testing.T, storeID string) *DurableStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "leases.db")
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.ExecContext(context.Background(), `PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewDurable(context.Background(), bunDB, DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func internalAcquireCmd(leaseID, logicalID string, now time.Time, ttl time.Duration) app.AcquireCommand {
	dims := domain.Dimensions{Principal: scope.Known("alice")}
	lease := domain.NewLease(domain.NewLeaseParams{
		LeaseID:     leaseID,
		RuleID:      "max-active",
		RuleVersion: "v1",
		LogicalID:   logicalID,
		Namespace:   "default",
		Dimensions:  dims,
		Now:         now,
		TTL:         ttl,
	})
	return app.AcquireCommand{
		Lease:      lease,
		RuleID:     "max-active",
		Dimensions: dims,
		Limit:      5,
		Mode:       domain.RuleModeStrict,
		Now:        now,
	}
}
