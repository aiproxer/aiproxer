package bunstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit/b2buatest"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	_ "modernc.org/sqlite" // register sqlite driver for tests
)

func TestNew_NilBunDB(t *testing.T) {
	t.Parallel()
	_, err := New(nil)
	if err == nil {
		t.Fatal("expected error for nil *bun.DB")
	}
}

func TestNewWithContext_NilContext(t *testing.T) {
	t.Parallel()
	_, err := NewWithContext(nil, nil) //nolint:staticcheck // contract: nil ctx must be rejected
	if err == nil {
		t.Fatal("expected error for nil context")
	}
}

func TestNew_AppliesSchema_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	var n int
	err := st.db.NewRaw(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='a_legs'`).Scan(ctx, &n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("a_legs table missing, count=%d", n)
	}
}

func TestSchemaMigrateTwice_Idempotent_SQLite(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	if err := runContinuitySchemaMigrate(ctx, st.db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var applied int
	err := st.db.NewRaw(
		`SELECT count(*) FROM bun_continuity_migrations WHERE name = ?`, BaselineMigrationName,
	).Scan(ctx, &applied)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("expected one applied baseline migration row, got %d", applied)
	}
}

func TestInterleavedStateMigration_duplicateColumnErrorsAreIdempotent(t *testing.T) {
	t.Parallel()
	for _, msg := range []string{
		"SQL logic error: duplicate column name: interleaved_state_json",
		`ERROR: column "interleaved_state_json" of relation "a_legs" already exists`,
	} {
		if !isDuplicateColumnErr(errors.New(msg)) {
			t.Fatalf("duplicate column error not recognized: %q", msg)
		}
	}
	for _, msg := range []string{
		"syntax error near interleaved_state_json",
		`ERROR: relation "a_legs" already exists`,
		`ERROR: index "idx_foo" already exists`,
		`ERROR: relation "interleaved_state_json" already exists`,
		`ERROR: index "interleaved_state_json" already exists`,
		"duplicate column name: other_column",
	} {
		if isDuplicateColumnErr(errors.New(msg)) {
			t.Fatalf("unrelated migration error must not be treated as duplicate column: %q", msg)
		}
	}
}

func TestResolveALeg_InvalidContinuityKey(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	_, err := st.ResolveALeg(ctx, "  ")
	if !errors.Is(err, b2bua.ErrInvalidContinuityKey) {
		t.Fatalf("got %v want %v", err, b2bua.ErrInvalidContinuityKey)
	}
}

func TestResolveALeg_NotFound(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	_, err := st.ResolveALeg(ctx, "no-such-key")
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("got %v want %v", err, b2bua.ErrALegNotFound)
	}
}

func TestCreateALeg_FetchALeg_RoundTrip(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	created, err := st.CreateALeg(ctx, "  ck1  ")
	if err != nil {
		t.Fatal(err)
	}
	if created.ContinuityKey != "ck1" {
		t.Fatalf("continuity key trim: got %q", created.ContinuityKey)
	}
	got, err := st.FetchALeg(ctx, created.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ALegID != created.ALegID || got.ContinuityKey != "ck1" {
		t.Fatalf("fetch mismatch: %+v vs %+v", got, created)
	}
}

func TestResolveALeg_UpdatesLastSeen(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := st.CreateALeg(ctx, "touch"); err != nil {
		t.Fatal(err)
	}
	first, err := st.ResolveALeg(ctx, "touch")
	if err != nil {
		t.Fatal(err)
	}
	var second b2bua.ALegRecord
	for range 50 {
		second, err = st.ResolveALeg(ctx, "touch")
		if err != nil {
			t.Fatal(err)
		}
		if second.LastSeenAt.After(first.LastSeenAt) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("last_seen not advanced: first=%v second=%v", first.LastSeenAt, second.LastSeenAt)
}

func TestFetchALeg_EmptyID_NotFound(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	_, err := st.FetchALeg(context.Background(), "  ")
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("got %v want %v", err, b2bua.ErrALegNotFound)
	}
}

func TestFetchALeg_Unknown_NotFound(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	_, err := st.FetchALeg(context.Background(), "a_not_real_id")
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("got %v want %v", err, b2bua.ErrALegNotFound)
	}
}

func TestCreateALeg_ReplacesSameContinuityKey(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	a1, err := st.CreateALeg(ctx, "dup")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := st.CreateALeg(ctx, "dup")
	if err != nil {
		t.Fatal(err)
	}
	if a1.ALegID == a2.ALegID {
		t.Fatal("expected new a-leg id")
	}
	_, err = st.FetchALeg(ctx, a1.ALegID)
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("old a-leg should be gone: %v", err)
	}
	if _, err := st.FetchALeg(ctx, a2.ALegID); err != nil {
		t.Fatal(err)
	}
}

func TestRestartSurvival_FileSQLite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cont.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	st, err := New(bunDB)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "persist")
	if err != nil {
		t.Fatal(err)
	}
	id := leg.ALegID
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	sqlDB2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB2.Close() })
	bunDB2, err := db.NewBunDB(sqlDB2, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	st2, err := New(bunDB2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	got, err := st2.FetchALeg(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ALegID != id || got.ContinuityKey != "persist" {
		t.Fatalf("after reopen: %+v", got)
	}
}

// TestRestartSurvival_BLEgAttemptsResolve_sqliteBun mirrors sqlitestore.TestStore_restartSurvival
// for the Bun-backed path: durable B-leg, attempt record, close, reopen, ResolveALeg + LoadAttempts
// parity across process-style reconnect.
func TestRestartSurvival_BLEgAttemptsResolve_sqliteBun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "continuity.db")
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
	leg, err := s1.CreateALeg(ctx, "session-xyz")
	if err != nil {
		t.Fatal(err)
	}
	bleg, err := s1.NextBLeg(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	rec := aipapi.AttemptRecord{
		BLegID:         bleg.BLegID,
		ALegID:         leg.ALegID,
		Seq:            bleg.Seq,
		BackendID:      "stub",
		EffectiveModel: "m",
		StartedAt:      time.Unix(1, 0),
		FinishedAt:     time.Unix(2, 0),
		Outcome:        aipapi.AttemptSuccess,
		Reason:         "ok",
	}
	if err := s1.RecordAttempt(ctx, rec); err != nil {
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

	got, err := s2.ResolveALeg(ctx, "session-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if got.ALegID != leg.ALegID {
		t.Fatalf("a-leg id %q want %q", got.ALegID, leg.ALegID)
	}
	rows, err := s2.LoadAttempts(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("attempts %+v", rows)
	}
	if rows[0].BackendID != "stub" || rows[0].Outcome != aipapi.AttemptSuccess {
		t.Fatalf("row %+v", rows[0])
	}
}

func TestNextBLeg_MonotonicConcurrent(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	const workers = 40
	const per = 12
	var wg sync.WaitGroup
	seqs := make([]int, 0, workers*per)
	var mu sync.Mutex
	var firstErr error
	for range workers {
		wg.Go(func() {
			for range per {
				bl, err := st.NextBLeg(ctx, leg.ALegID)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				mu.Lock()
				seqs = append(seqs, bl.Seq)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	if len(seqs) != workers*per {
		t.Fatalf("got %d seqs want %d", len(seqs), workers*per)
	}
	sort.Ints(seqs)
	seen := make(map[int]struct{}, len(seqs))
	for _, s := range seqs {
		if _, ok := seen[s]; ok {
			t.Fatalf("duplicate seq %d", s)
		}
		seen[s] = struct{}{}
	}
	for want := 1; want <= workers*per; want++ {
		if _, ok := seen[want]; !ok {
			t.Fatalf("missing seq %d", want)
		}
	}
}

func TestNextBLeg_UnknownALeg(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	_, err := st.NextBLeg(context.Background(), "a_missing")
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("got %v want %v", err, b2bua.ErrALegNotFound)
	}
}

func TestSetWeightedFirstConsumed(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetWeightedFirstConsumed(ctx, leg.ALegID, true); err != nil {
		t.Fatal(err)
	}
	got, err := st.FetchALeg(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.WeightedFirstConsumed {
		t.Fatal("expected weighted first consumed")
	}
}

func TestSetWeightedFirstConsumed_NotFound(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	err := st.SetWeightedFirstConsumed(context.Background(), "a_nope", true)
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("got %v want %v", err, b2bua.ErrALegNotFound)
	}
}

func TestRecordAttempt_Upsert(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	bl, err := st.NextBLeg(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	t1 := time.Unix(1, 0)
	t2 := time.Unix(2, 0)
	rec1 := aipapi.AttemptRecord{
		ALegID: leg.ALegID, Seq: bl.Seq, BLegID: bl.BLegID,
		BackendID: "b1", EffectiveModel: "m1",
		StartedAt: t1, FinishedAt: t1, Outcome: aipapi.AttemptSuccess, Reason: "a",
	}
	if err := st.RecordAttempt(ctx, rec1); err != nil {
		t.Fatal(err)
	}
	rec2 := rec1
	rec2.BackendID = "b2"
	rec2.Reason = "b"
	rec2.FinishedAt = t2
	if err := st.RecordAttempt(ctx, rec2); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadAttempts(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("want 1 attempt got %d", len(loaded))
	}
	if loaded[0].BackendID != "b2" || loaded[0].Reason != "b" {
		t.Fatalf("upsert did not replace: %+v", loaded[0])
	}
}

func TestLoadAttempts_Ordering(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		bl, err := st.NextBLeg(ctx, leg.ALegID)
		if err != nil {
			t.Fatal(err)
		}
		rec := aipapi.AttemptRecord{
			ALegID: leg.ALegID, Seq: bl.Seq, BLegID: bl.BLegID,
			BackendID: "x", EffectiveModel: "y",
			StartedAt: time.Unix(int64(10+i), 0), FinishedAt: time.Unix(int64(20+i), 0),
			Outcome: aipapi.AttemptSurfacedFailure, Reason: "",
		}
		if err := st.RecordAttempt(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := st.LoadAttempts(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 3 {
		t.Fatalf("len=%d", len(loaded))
	}
	for i := 1; i < len(loaded); i++ {
		if loaded[i].Seq <= loaded[i-1].Seq {
			t.Fatalf("not ordered by seq: %+v", loaded)
		}
	}
}

func TestLoadAttempts_UnknownALeg(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	_, err := st.LoadAttempts(context.Background(), "a_unknown")
	if !errors.Is(err, b2bua.ErrALegNotFound) {
		t.Fatalf("got %v want %v", err, b2bua.ErrALegNotFound)
	}
}

func TestLoadAttempts_EmptyForExistingALeg(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := st.LoadAttempts(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("want empty slice, got %d", len(out))
	}
}

func TestRecordAttempt_InvalidBLeg(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	bl, err := st.NextBLeg(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	rec := aipapi.AttemptRecord{
		ALegID: leg.ALegID, Seq: bl.Seq, BLegID: "wrong-b-leg",
		BackendID: "x", EffectiveModel: "y",
		StartedAt: time.Now(), FinishedAt: time.Now(),
		Outcome: aipapi.AttemptSuccess,
	}
	err = st.RecordAttempt(ctx, rec)
	if err == nil || !errors.Is(err, b2bua.ErrInvalidAttempt) {
		t.Fatalf("want ErrInvalidAttempt wrap, got %v", err)
	}
}

var testMemDBSeq atomic.Int64

func newTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	id := testMemDBSeq.Add(1)
	dsn := fmt.Sprintf("file:mem%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", id)
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
	st, err := New(bunDB)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	return st, func() { _ = st.Close() }
}

func TestFetchInterleavedState_RejectsCorruptStoredState(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	leg, err := st.CreateALeg(ctx, "corrupt-interleaved")
	if err != nil {
		t.Fatal(err)
	}
	badJSON := `{"cycle":{"selector_key":"k","sequence":[{"key":"a","role":"executor"}],"next_index":5}}`
	if _, err := st.db.NewRaw(
		`UPDATE a_legs SET interleaved_state_json = ? WHERE a_leg_id = ?`,
		badJSON, leg.ALegID,
	).Exec(ctx); err != nil {
		t.Fatalf("inject corrupt state: %v", err)
	}
	_, err = st.FetchInterleavedState(ctx, leg.ALegID)
	if err == nil {
		t.Fatal("expected validation error for corrupt stored interleaved state")
	}
}

func TestStore_InterleavedState(t *testing.T) {
	t.Parallel()
	b2buatest.TestInterleavedStateStore(t, func(t *testing.T) b2buatest.Store {
		t.Helper()
		st, cleanup := newTestStore(t)
		t.Cleanup(cleanup)
		return st
	})
}

func TestStore_InterleavedState_restartSurvival(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cont.db")
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
	leg, err := s1.CreateALeg(ctx, "durable-session")
	if err != nil {
		t.Fatal(err)
	}
	want := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			SelectorKey: "sk",
			Sequence: []interleavedstate.CycleEntry{
				{Key: "x", Role: interleavedstate.RoleExecutor},
				{Key: "t", Role: interleavedstate.RoleThinker},
			},
			NextIndex: 1,
		},
	}
	if err := s1.SetInterleavedState(ctx, leg.ALegID, want); err != nil {
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
	got, err := s2.FetchInterleavedState(ctx, leg.ALegID)
	if err != nil {
		t.Fatalf("reopen fetch: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("durable round-trip mismatch: got %+v want %+v", got, want)
	}
}
