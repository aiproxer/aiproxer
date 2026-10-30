package leasestore_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	_ "modernc.org/sqlite"
)

func TestSQLiteStore_FiveSlotContract(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "leases.db")
	a := newSQLiteStore(t, path, "sqlite-test")
	b := newSQLiteStore(t, path, "sqlite-test")
	runFiveSlotContract(t, a, b)
}

func TestSQLiteStore_ReadinessReportsSingleNode(t *testing.T) {
	t.Parallel()
	store := newSQLiteStore(t, filepath.Join(t.TempDir(), "ready.db"), "sqlite-ready")
	ready, err := store.CheckReadiness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != domain.ReadinessStateReady {
		t.Fatalf("state=%s want ready", ready.State)
	}
	if ready.Reason == "" {
		t.Fatal("expected single-node limitation reason")
	}
}

func TestSQLiteStore_ReclaimUsesIndexedPredicate(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "reclaim.db")
	store := newSQLiteStore(t, path, "sqlite-reclaim")
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("sql-%d", i)
		if _, err := store.Acquire(ctx, acquireCmd(id, fmt.Sprintf("req-%d", i), now, time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	plan, err := leasestore.ExplainReclaimPlan(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if plan == "" {
		t.Fatal("empty reclaim plan")
	}
	lower := strings.ToLower(plan)
	if !strings.Contains(lower, "idx_concurrency_leases_capacity") && !strings.Contains(lower, "search") {
		t.Fatalf("expected indexed reclaim plan, got %q", plan)
	}

	later := now.Add(2 * time.Second)
	res, err := store.Acquire(ctx, acquireCmd("sql-new", "req-new", later, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res.CapacityExceeded {
		t.Fatal("reclaim should free capacity")
	}
}

func newSQLiteStore(t *testing.T, path, storeID string) *leasestore.DurableStore {
	t.Helper()
	// Apply busy_timeout via DSN so every pooled connection inherits it (a one-shot
	// PRAGMA only affects the connection that executed it). Cap the pool at 1 to
	// avoid SQLITE_BUSY under concurrent writers in race tests.
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := leasestore.NewDurable(ctx, bunDB, leasestore.DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
