package bunstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/infra/db"
)

func TestNew_AppliesRouteOverrideSchema_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	var n int
	err := st.db.NewRaw(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='a_leg_route_overrides'`).Scan(ctx, &n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("a_leg_route_overrides table missing, count=%d", n)
	}
	var idx int
	err = st.db.NewRaw(`
SELECT count(*) FROM sqlite_master
WHERE type='index' AND tbl_name='a_leg_route_overrides' AND sql LIKE '%selector%'
`).Scan(ctx, &idx)
	if err != nil {
		t.Fatal(err)
	}
	if idx != 0 {
		t.Fatalf("selector must not be indexed, got %d matching indexes", idx)
	}
}

func TestRouteOverride_restartSurvival_SQLite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cont-ov.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	s1, err := New(bunDB)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	leg, err := s1.CreateALeg(ctx, "ov-persist")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_001_000, 0).UTC()
	want, err := s1.Replace(ctx, leg.ALegID, "openai:gpt-4", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	sqlDB2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB2.Close() })
	sqlDB2.SetMaxOpenConns(1)
	bunDB2, err := db.NewBunDB(sqlDB2, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := New(bunDB2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, err := s2.Snapshot(ctx, leg.ALegID)
	if err != nil {
		t.Fatalf("reopen Snapshot: %v", err)
	}
	if got.ALegID != want.ALegID || !got.Active || got.Selector != want.Selector || got.Revision != want.Revision {
		t.Fatalf("durable override mismatch: got %+v want %+v", got, want)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("durable updated_at: got %v want %v", got.UpdatedAt, want.UpdatedAt)
	}
}

func TestRouteOverride_aLegDeleteCascades(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "ov-cascade")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Replace(ctx, leg.ALegID, "openai:gpt-4", time.Unix(1, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateALeg(ctx, "ov-cascade"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.NewRaw(`SELECT count(*) FROM a_leg_route_overrides WHERE a_leg_id = ?`, leg.ALegID).Scan(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("override row survived A-leg delete: count=%d", n)
	}
	if _, err := st.Snapshot(ctx, leg.ALegID); !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("deleted A-leg Snapshot: got %v", err)
	}
}

func TestRouteOverride_snapshotAfterCloseIsSurfaced(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "ov-closed")
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	cleanup()
	got, err := st.Snapshot(ctx, leg.ALegID)
	if err == nil {
		t.Fatalf("closed store snapshot must fail, got %+v", got)
	}
	if errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatal("must not convert close failure to not-found/inactive")
	}
}

func TestRouteOverride_legacyALegHasNoRow(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "ov-legacy")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.NewRaw(`SELECT count(*) FROM a_leg_route_overrides WHERE a_leg_id = ?`, leg.ALegID).Scan(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("legacy A-leg must not be backfilled, count=%d", n)
	}
	got, err := st.Snapshot(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active || got.Revision != 0 || got.Selector != "" {
		t.Fatalf("legacy A-leg must be revision-0 inactive: %+v", got)
	}
}

func TestRouteOverride_clearedStateSurvivesReopen_SQLite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cont-ov-clear.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	s1, err := New(bunDB)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	leg, err := s1.CreateALeg(ctx, "ov-clear-persist")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_002_000, 0).UTC()
	if _, err := s1.Replace(ctx, leg.ALegID, "openai:gpt-4", now); err != nil {
		t.Fatal(err)
	}
	cleared, err := s1.Clear(ctx, leg.ALegID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Active || cleared.Revision < 2 {
		t.Fatalf("cleared state: %+v", cleared)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	sqlDB2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB2.Close() })
	sqlDB2.SetMaxOpenConns(1)
	bunDB2, err := db.NewBunDB(sqlDB2, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := New(bunDB2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, err := s2.Snapshot(ctx, leg.ALegID)
	if err != nil {
		t.Fatalf("reopen Snapshot: %v", err)
	}
	if got.Active || got.Selector != "" || got.Revision != cleared.Revision {
		t.Fatalf("cleared override must survive reopen: got %+v want %+v", got, cleared)
	}
}

func TestRouteOverride_recreateContinuityKeyDoesNotInherit_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "ov-recreate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Replace(ctx, leg.ALegID, "openai:gpt-4", time.Unix(1, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	fresh, err := st.CreateALeg(ctx, "ov-recreate")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ALegID == leg.ALegID {
		t.Fatal("recreate must allocate a new A-leg")
	}
	if _, err := st.Snapshot(ctx, leg.ALegID); !errors.Is(err, routeoverride.ErrNotFound) {
		t.Fatalf("old A-leg Snapshot: %v", err)
	}
	got, err := st.Snapshot(ctx, fresh.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active || got.Revision != 0 {
		t.Fatalf("new A-leg must not inherit override: %+v", got)
	}
}
