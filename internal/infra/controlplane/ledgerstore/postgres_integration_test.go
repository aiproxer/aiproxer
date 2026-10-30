//go:build integration

package ledgerstore

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore/contract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// pgFactory implements contract.Factory for the Postgres durable store.
type pgFactory struct {
	dsn string
}

func (f pgFactory) Build(t *testing.T) controlplane.Store {
	t.Helper()
	return newPostgresStoreForTest(t, f.dsn)
}

func (f pgFactory) ParallelContract() bool { return false }

// TestPostgresStore_Contract runs the shared store contract against the
// Postgres durable adapter when an integration DSN is configured. The suite
// exercises append/source-event-key dedupe, readiness, query filters,
// sessions/attempts/usage/policy-audit projections, pagination/continuation,
// unsupported-filter reporting, retention idempotence, redaction default
// visibility, and unsafe-evidence rejection (tasks 2.3, 2.4, 2.5, 6.x).
func TestPostgresStore_Contract(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	contract.RunSuite(t, pgFactory{dsn: dsn})
}

// pgBuildMu serializes Postgres store construction (schema bootstrap, open,
// migrate) so concurrent contract subtests never race on
// bun_controlplane_migrations or pg_type creation. Subtests remain parallel
// after Build returns; each store lives in its own schema for data isolation.
var pgBuildMu sync.Mutex

var pgSchemaSeq atomic.Uint64

// newPostgresStoreForTest opens a Postgres-backed durable store in an isolated
// schema and registers cleanup. Shared by the contract factory and the explicit
// migration tests so both route through one construction path. Per-store schema
// isolation avoids migration races (each store migrates its own namespace) and
// keeps parallel contract subtests from observing each other's events.
func newPostgresStoreForTest(t *testing.T, dsn string) *DurableStore {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    1,
		MaxIdleConns:    1,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}

	pgBuildMu.Lock()
	seq := pgSchemaSeq.Add(1)
	schemaName := fmt.Sprintf("cp_test_%d_%d", time.Now().UnixNano(), seq)
	bootstrap, berr := db.OpenPostgresBun(ctx, dsn, pool)
	if berr != nil {
		pgBuildMu.Unlock()
		t.Fatal(berr)
	}
	if _, eerr := bootstrap.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %q", schemaName)); eerr != nil {
		_ = bootstrap.Close()
		pgBuildMu.Unlock()
		t.Fatalf("create schema %s: %v", schemaName, eerr)
	}
	if cerr := bootstrap.Close(); cerr != nil {
		pgBuildMu.Unlock()
		t.Fatalf("close bootstrap: %v", cerr)
	}

	schemaDSN, serr := setPostgresSearchPath(dsn, schemaName)
	if serr != nil {
		pgBuildMu.Unlock()
		t.Fatalf("set search_path: %v", serr)
	}
	bunDB, oerr := db.OpenPostgresBun(ctx, schemaDSN, pool)
	if oerr != nil {
		pgBuildMu.Unlock()
		t.Fatal(oerr)
	}
	if _, setErr := bunDB.ExecContext(ctx, fmt.Sprintf("SET search_path TO %s", schemaName)); setErr != nil {
		_ = bunDB.Close()
		pgBuildMu.Unlock()
		t.Fatalf("set search_path on test store: %v", setErr)
	}
	store, nerr := NewDurableStore(ctx, bunDB, DurableConfig{StoreID: "pg-test"})
	if nerr != nil {
		_ = bunDB.Close()
		pgBuildMu.Unlock()
		t.Fatal(nerr)
	}
	pgBuildMu.Unlock()

	t.Cleanup(func() {
		_ = store.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		dropper, derr := db.OpenPostgresBun(dropCtx, dsn, pool)
		if derr != nil {
			return
		}
		defer func() { _ = dropper.Close() }()
		_, _ = dropper.ExecContext(dropCtx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName))
	})
	return store
}

// setPostgresSearchPath returns dsn with a search_path query parameter set to
// schema, replacing any existing search_path. pgdriver forwards unknown query
// params to the server as SET commands, and search_path is a server GUC, so
// each pooled connection binds to the isolated schema.
func setPostgresSearchPath(dsn, schema string) (string, error) {
	before, after, ok := strings.Cut(dsn, "?")
	var base, rawQuery string
	if !ok {
		base, rawQuery = dsn, ""
	} else {
		base, rawQuery = before, after
	}
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("parse dsn query: %w", err)
	}
	vals.Del("search_path")
	vals.Set("search_path", schema)
	encoded := vals.Encode()
	if encoded == "" {
		return base, nil
	}
	return base + "?" + encoded, nil
}

// TestPostgresStore_migrationsCreateTableAndIndexes verifies the gated Postgres
// baseline migration creates the append-only event table and the documented
// indexes (task 2.2, phase 6 risk closure). It complements the SQLite migration
// test so the dialect-aware Postgres DDL is exercised against a live Postgres
// instance, not only by the contract suite's implicit migration.
func TestPostgresStore_migrationsCreateTableAndIndexes(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	store := newPostgresStoreForTest(t, dsn)
	ctx := context.Background()

	var table string
	row := store.sqlDB.QueryRowContext(ctx,
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = current_schema() AND table_name = 'control_plane_events' LIMIT 1`)
	if err := row.Scan(&table); err != nil {
		t.Fatalf("table lookup: %v", err)
	}
	if table != "control_plane_events" {
		t.Fatalf("table name = %q, want control_plane_events", table)
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
		"idx_control_plane_events_outcome",
		"idx_control_plane_events_reason",
		"idx_control_plane_events_surfaced",
		"idx_control_plane_events_usage_plane",
		"idx_control_plane_events_principal",
		"idx_control_plane_events_tenant",
		"idx_control_plane_events_workspace",
		"idx_control_plane_events_project",
	} {
		var name string
		row := store.sqlDB.QueryRowContext(ctx,
			`SELECT indexname FROM pg_indexes
			 WHERE schemaname = current_schema() AND indexname = $1 LIMIT 1`, index)
		if err := row.Scan(&name); err != nil {
			t.Fatalf("index %s lookup: %v", index, err)
		}
		if name != index {
			t.Fatalf("index name = %q, want %q", name, index)
		}
	}
}

// TestPostgresStore_migrationsIdempotent verifies re-running the Postgres
// migration is a no-op (task 2.2, phase 6 risk closure). CREATE TABLE/INDEX IF
// NOT EXISTS must keep the schema stable across repeated starts.
func TestPostgresStore_migrationsIdempotent(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	store := newPostgresStoreForTest(t, dsn)
	if err := runControlPlaneSchemaMigrate(context.Background(), store.db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
