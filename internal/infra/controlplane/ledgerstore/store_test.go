package ledgerstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore/contract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

var memSQLiteDSN atomic.Int64

// sqliteFactory implements contract.Factory for the SQLite durable store.
type sqliteFactory struct {
	unsupported []string
}

func (f sqliteFactory) Build(t *testing.T) controlplane.Store {
	t.Helper()
	store := newSQLiteStoreForTest(t, f.unsupported)
	return store
}

func (f sqliteFactory) UnsupportedConfig() contract.UnsupportedConfig {
	return contract.UnsupportedConfig{Fields: f.unsupported}
}

// TestSQLiteStore_Contract runs the shared store contract against the SQLite
// durable adapter (tasks 2.3, 2.4, 2.5).
func TestSQLiteStore_Contract(t *testing.T) {
	t.Parallel()
	contract.RunSuite(t, sqliteFactory{})
}

// TestSQLiteStore_ContractUnsupportedFilters exercises unsupported-filter
// reporting through the shared contract for the SQLite adapter (task 2.4).
func TestSQLiteStore_ContractUnsupportedFilters(t *testing.T) {
	t.Parallel()
	f := sqliteFactory{unsupported: []string{contract.FieldBackendID, contract.FieldScopeTenantID}}
	contract.RunSuite(t, f)
}

// TestSQLiteStore_migrationsCreateTableAndIndexes verifies the baseline
// migration creates the append-only event table and the documented indexes
// (task 2.2).
func TestSQLiteStore_migrationsCreateTableAndIndexes(t *testing.T) {
	t.Parallel()
	store := newSQLiteStoreForTest(t, nil)
	ctx := context.Background()
	var tableName string
	if err := store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'control_plane_events'`).Scan(ctx, &tableName); err != nil {
		t.Fatalf("table lookup: %v", err)
	}
	if tableName != "control_plane_events" {
		t.Fatalf("table name = %q, want control_plane_events", tableName)
	}
	for _, index := range []string{
		"idx_control_plane_events_source_key",
		"idx_control_plane_events_order",
		"idx_control_plane_events_category_time",
		"idx_control_plane_events_trace",
		"idx_control_plane_events_session",
		"idx_control_plane_events_a_leg",
		"idx_control_plane_events_b_leg",
		"idx_control_plane_events_backend_model",
		"idx_control_plane_events_surfaced",
		"idx_control_plane_events_usage_plane",
		"idx_control_plane_events_principal",
	} {
		var name string
		if err := store.db.NewRaw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(ctx, &name); err != nil {
			t.Fatalf("index %s lookup: %v", index, err)
		}
		if name != index {
			t.Fatalf("index name = %q, want %q", name, index)
		}
	}
}

// TestSQLiteStore_migrationsIdempotent verifies running migrations twice is a
// no-op (task 2.2).
func TestSQLiteStore_migrationsIdempotent(t *testing.T) {
	t.Parallel()
	store := newSQLiteStoreForTest(t, nil)
	if err := runControlPlaneSchemaMigrate(context.Background(), store.db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// TestSQLiteStore_noDetailTables verifies the first implementation does not
// create per-category detail tables (design "Physical Data Model", task 2.2).
func TestSQLiteStore_noDetailTables(t *testing.T) {
	t.Parallel()
	store := newSQLiteStoreForTest(t, nil)
	ctx := context.Background()
	rows, err := store.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'control_plane_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name != "control_plane_events" {
			t.Fatalf("unexpected control-plane table %q (only control_plane_events expected)", name)
		}
	}
}

// TestSQLiteStore_sourceKeyDedupePersists verifies source-event-key dedupe
// keeps a single row across repeated appends (requirement 1.7, task 2.3).
func TestSQLiteStore_sourceKeyDedupePersists(t *testing.T) {
	t.Parallel()
	store := newSQLiteStoreForTest(t, nil)
	ctx := context.Background()
	ev := contractEvent()
	first, err := store.Append(ctx, ev)
	if err != nil {
		t.Fatalf("Append() first error = %v", err)
	}
	second, err := store.Append(ctx, ev)
	if err != nil {
		t.Fatalf("Append() second error = %v", err)
	}
	if second.Dedupe != cp.DedupeDuplicate {
		t.Fatalf("second dedupe = %q, want duplicate", second.Dedupe)
	}
	if second.ID != first.ID {
		t.Fatalf("second identity = %#v, want %#v", second.ID, first.ID)
	}
	page, err := store.Events(ctx, cp.EventQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Events() len = %d, want 1 (dedupe must not add a row)", len(page.Items))
	}
}

// TestSQLiteStore_cancelledContextPropagates verifies the durable adapter
// respects context cancellation (requirement 7.3).
func TestSQLiteStore_cancelledContextPropagates(t *testing.T) {
	t.Parallel()
	store := newSQLiteStoreForTest(t, nil)
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Append(cctx, contractEvent()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append(canceled) error = %v, want context.Canceled", err)
	}
}

// TestSQLiteStore_storageErrorClassified verifies storage failures are
// classified at the adapter boundary without leaking raw infrastructure text
// to core contracts (requirement 7.3).
func TestSQLiteStore_storageErrorClassified(t *testing.T) {
	t.Parallel()
	store := newSQLiteStoreForTest(t, nil)
	if err := store.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err := store.CheckReadiness(context.Background())
	if err == nil {
		t.Fatalf("CheckReadiness after close must fail")
	}
	if !errors.Is(err, controlplane.ErrUnavailable) {
		t.Fatalf("CheckReadiness error = %v, want ErrUnavailable", err)
	}
}

func newSQLiteStoreForTest(t *testing.T, unsupported []string) *DurableStore {
	t.Helper()
	// No ledgerstore SQLite test reopens the database file, so a uniquely
	// named shared-cache in-memory DSN avoids per-commit fsync latency.
	dsn := fmt.Sprintf("file:ls-mem-%d?mode=memory&cache=shared&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", memSQLiteDSN.Add(1))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	store, err := NewDurableStore(context.Background(), bunDB, DurableConfig{
		StoreID:            "sqlite-test",
		UnsupportedFilters: unsupported,
	})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
