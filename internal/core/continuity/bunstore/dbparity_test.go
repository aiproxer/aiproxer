package bunstore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	routeoverrideStorecontract "github.com/aiproxer/aiproxer/internal/core/routeoverride/storecontract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit/b2buatest"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// continuityParityFixture abstracts backend-specific store construction and lifecycle
// for canonical database parity testing across SQLite and PostgreSQL.
type continuityParityFixture interface {
	NewStore(t *testing.T) *Store
	RouteOverrideEnv(t *testing.T) routeoverrideStorecontract.ContractEnv
	ReopenStore(t *testing.T) (*Store, func() *Store)
}

type sqliteContinuityFixture struct{}

func newSQLiteContinuityFixture(t *testing.T) continuityParityFixture {
	t.Helper()
	return &sqliteContinuityFixture{}
}

func (f *sqliteContinuityFixture) NewStore(t *testing.T) *Store {
	t.Helper()
	st, cleanup := newTestStore(t)
	t.Cleanup(cleanup)
	return st
}

func (f *sqliteContinuityFixture) RouteOverrideEnv(t *testing.T) routeoverrideStorecontract.ContractEnv {
	t.Helper()
	return routeoverrideStorecontract.ContractEnv{
		New: func(t *testing.T) routeoverrideStorecontract.ContractPair {
			t.Helper()
			s := f.NewStore(t)
			ov, ok := routeoverride.AsStore(s)
			if !ok {
				t.Fatal("continuity/bunstore.Store does not implement routeoverride.Store")
			}
			return routeoverrideStorecontract.ContractPair{Override: ov, Legs: s}
		},
		SeedRevision:    seedBunRevision,
		PeekLastSeenAt:  peekBunLastSeenAt,
		AdvanceClock:    advanceBunLastSeen,
		SeedStoredState: seedBunStoredState,
		Spawn:           func(fn func()) { go fn() },
	}
}

func (f *sqliteContinuityFixture) ReopenStore(t *testing.T) (*Store, func() *Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, fmt.Sprintf("reopen_%d.db", testMemDBSeq.Add(1)))
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"

	var current *Store
	open := func() *Store {
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
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
		current = st
		return st
	}

	s1 := open()
	t.Cleanup(func() {
		if current != nil {
			_ = current.Close()
		}
	})

	reopen := func() *Store {
		if current != nil {
			_ = current.Close()
			current = nil
		}
		return open()
	}
	return s1, reopen
}

// runContinuityParitySuite executes the canonical behavioral and transactional parity suite
// for continuity persistence against the provided fixture.
func runContinuityParitySuite(t *testing.T, f continuityParityFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("ALegLifecycle", func(t *testing.T) {
		t.Run("roundTrip", func(t *testing.T) {
			st := f.NewStore(t)
			ck := fmt.Sprintf("ck-rt-%d", time.Now().UnixNano())
			created, err := st.CreateALeg(ctx, "  "+ck+"  ")
			require.NoError(t, err)
			assert.Equal(t, ck, created.ContinuityKey, "continuity key must be trimmed")
			require.NotEmpty(t, created.ALegID)

			got, err := st.FetchALeg(ctx, created.ALegID)
			require.NoError(t, err)
			assert.Equal(t, created.ALegID, got.ALegID)
			assert.Equal(t, ck, got.ContinuityKey)
		})

		t.Run("resolveUpdatesLastSeen", func(t *testing.T) {
			st := f.NewStore(t)
			ck := fmt.Sprintf("ck-touch-%d", time.Now().UnixNano())
			_, err := st.CreateALeg(ctx, ck)
			require.NoError(t, err)

			first, err := st.ResolveALeg(ctx, ck)
			require.NoError(t, err)

			var second b2bua.ALegRecord
			advanced := false
			for range 50 {
				time.Sleep(2 * time.Millisecond)
				second, err = st.ResolveALeg(ctx, ck)
				require.NoError(t, err)
				if second.LastSeenAt.After(first.LastSeenAt) {
					advanced = true
					break
				}
			}
			assert.True(t, advanced, "ResolveALeg must advance LastSeenAt (first=%v, second=%v)", first.LastSeenAt, second.LastSeenAt)
		})

		t.Run("invalidContinuityKey", func(t *testing.T) {
			st := f.NewStore(t)
			_, err := st.ResolveALeg(ctx, "   ")
			assert.ErrorIs(t, err, b2bua.ErrInvalidContinuityKey)
		})

		t.Run("notFound", func(t *testing.T) {
			st := f.NewStore(t)
			_, err := st.ResolveALeg(ctx, fmt.Sprintf("no-such-key-%d", time.Now().UnixNano()))
			assert.ErrorIs(t, err, b2bua.ErrALegNotFound)
		})

		t.Run("fetchErrors", func(t *testing.T) {
			st := f.NewStore(t)
			_, err := st.FetchALeg(ctx, "   ")
			assert.ErrorIs(t, err, b2bua.ErrALegNotFound)

			_, err = st.FetchALeg(ctx, "a_non_existent_id")
			assert.ErrorIs(t, err, b2bua.ErrALegNotFound)
		})

		t.Run("replacesSameContinuityKey", func(t *testing.T) {
			st := f.NewStore(t)
			ck := fmt.Sprintf("ck-dup-%d", time.Now().UnixNano())
			a1, err := st.CreateALeg(ctx, ck)
			require.NoError(t, err)

			a2, err := st.CreateALeg(ctx, ck)
			require.NoError(t, err)
			assert.NotEqual(t, a1.ALegID, a2.ALegID, "recreation must yield a new A-leg ID")

			_, err = st.FetchALeg(ctx, a1.ALegID)
			assert.ErrorIs(t, err, b2bua.ErrALegNotFound, "old A-leg must be removed")

			got, err := st.FetchALeg(ctx, a2.ALegID)
			require.NoError(t, err)
			assert.Equal(t, a2.ALegID, got.ALegID)
		})

		t.Run("weightedFirstConsumed", func(t *testing.T) {
			st := f.NewStore(t)
			leg, err := st.CreateALeg(ctx, "")
			require.NoError(t, err)

			err = st.SetWeightedFirstConsumed(ctx, leg.ALegID, true)
			require.NoError(t, err)

			got, err := st.FetchALeg(ctx, leg.ALegID)
			require.NoError(t, err)
			assert.True(t, got.WeightedFirstConsumed)

			err = st.SetWeightedFirstConsumed(ctx, "a_unknown_id", true)
			assert.ErrorIs(t, err, b2bua.ErrALegNotFound)
		})
	})

	t.Run("BLEgAllocation", func(t *testing.T) {
		t.Run("unknownALeg", func(t *testing.T) {
			st := f.NewStore(t)
			_, err := st.NextBLeg(ctx, "a_unknown_id")
			assert.ErrorIs(t, err, b2bua.ErrALegNotFound)
		})

		t.Run("monotonicSequential", func(t *testing.T) {
			st := f.NewStore(t)
			leg, err := st.CreateALeg(ctx, "")
			require.NoError(t, err)

			bl1, err := st.NextBLeg(ctx, leg.ALegID)
			require.NoError(t, err)
			assert.Equal(t, 1, bl1.Seq)
			assert.NotEmpty(t, bl1.BLegID)

			bl2, err := st.NextBLeg(ctx, leg.ALegID)
			require.NoError(t, err)
			assert.Equal(t, 2, bl2.Seq)
			assert.NotEmpty(t, bl2.BLegID)
		})

		t.Run("monotonicConcurrent", func(t *testing.T) {
			st := f.NewStore(t)
			leg, err := st.CreateALeg(ctx, fmt.Sprintf("ck-bseq-conc-%d", time.Now().UnixNano()))
			require.NoError(t, err)

			const workers = 20
			const per = 10
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

			require.NoError(t, firstErr)
			require.Len(t, seqs, workers*per)

			sort.Ints(seqs)
			for i, seq := range seqs {
				assert.Equal(t, i+1, seq, "monotonic contiguous sequence mismatch at index %d", i)
			}
		})
	})

	t.Run("AttemptLineage", func(t *testing.T) {
		t.Run("recordAndLoad", func(t *testing.T) {
			st := f.NewStore(t)
			leg, err := st.CreateALeg(ctx, "")
			require.NoError(t, err)

			for i := range 3 {
				bl, err := st.NextBLeg(ctx, leg.ALegID)
				require.NoError(t, err)
				rec := aipapi.AttemptRecord{
					ALegID:         leg.ALegID,
					Seq:            bl.Seq,
					BLegID:         bl.BLegID,
					BackendID:      fmt.Sprintf("backend-%d", i),
					EffectiveModel: "model-v1",
					StartedAt:      time.Unix(int64(10+i), 0),
					FinishedAt:     time.Unix(int64(20+i), 0),
					Outcome:        aipapi.AttemptSuccess,
					Reason:         "ok",
				}
				require.NoError(t, st.RecordAttempt(ctx, rec))
			}

			loaded, err := st.LoadAttempts(ctx, leg.ALegID)
			require.NoError(t, err)
			require.Len(t, loaded, 3)

			for i := 1; i < len(loaded); i++ {
				assert.Greater(t, loaded[i].Seq, loaded[i-1].Seq, "attempts must be ordered strictly ascending by Seq")
			}
		})

		t.Run("upsert", func(t *testing.T) {
			st := f.NewStore(t)
			leg, err := st.CreateALeg(ctx, "")
			require.NoError(t, err)

			bl, err := st.NextBLeg(ctx, leg.ALegID)
			require.NoError(t, err)

			rec1 := aipapi.AttemptRecord{
				ALegID:         leg.ALegID,
				Seq:            bl.Seq,
				BLegID:         bl.BLegID,
				BackendID:      "initial-backend",
				EffectiveModel: "m1",
				StartedAt:      time.Unix(1, 0),
				FinishedAt:     time.Unix(2, 0),
				Outcome:        aipapi.AttemptSurfacedFailure,
				Reason:         "err1",
			}
			require.NoError(t, st.RecordAttempt(ctx, rec1))

			rec2 := rec1
			rec2.BackendID = "updated-backend"
			rec2.Reason = "updated-reason"
			rec2.Outcome = aipapi.AttemptSuccess
			require.NoError(t, st.RecordAttempt(ctx, rec2))

			loaded, err := st.LoadAttempts(ctx, leg.ALegID)
			require.NoError(t, err)
			require.Len(t, loaded, 1)
			assert.Equal(t, "updated-backend", loaded[0].BackendID)
			assert.Equal(t, "updated-reason", loaded[0].Reason)
			assert.Equal(t, aipapi.AttemptSuccess, loaded[0].Outcome)
		})

		t.Run("invalidBLeg", func(t *testing.T) {
			st := f.NewStore(t)
			leg, err := st.CreateALeg(ctx, "")
			require.NoError(t, err)

			bl, err := st.NextBLeg(ctx, leg.ALegID)
			require.NoError(t, err)

			rec := aipapi.AttemptRecord{
				ALegID:         leg.ALegID,
				Seq:            bl.Seq,
				BLegID:         "mismatched-b-leg-id",
				BackendID:      "stub",
				EffectiveModel: "m",
				StartedAt:      time.Now(),
				FinishedAt:     time.Now(),
				Outcome:        aipapi.AttemptSuccess,
			}
			err = st.RecordAttempt(ctx, rec)
			assert.ErrorIs(t, err, b2bua.ErrInvalidAttempt)
		})

		t.Run("unknownALeg", func(t *testing.T) {
			st := f.NewStore(t)
			_, err := st.LoadAttempts(ctx, "a_unknown_id")
			assert.ErrorIs(t, err, b2bua.ErrALegNotFound)
		})

		t.Run("emptyForExisting", func(t *testing.T) {
			st := f.NewStore(t)
			leg, err := st.CreateALeg(ctx, "")
			require.NoError(t, err)

			out, err := st.LoadAttempts(ctx, leg.ALegID)
			require.NoError(t, err)
			assert.Empty(t, out)
		})
	})

	t.Run("InterleavedState", func(t *testing.T) {
		t.Run("contract", func(t *testing.T) {
			b2buatest.TestInterleavedStateStore(t, func(t *testing.T) b2buatest.Store {
				t.Helper()
				return f.NewStore(t)
			})
		})

		t.Run("rejectsCorruptState", func(t *testing.T) {
			st := f.NewStore(t)
			leg, err := st.CreateALeg(ctx, fmt.Sprintf("ck-corrupt-%d", time.Now().UnixNano()))
			require.NoError(t, err)

			badJSON := `{"cycle":{"selector_key":"k","sequence":[{"key":"a","role":"executor"}],"next_index":5}}`
			_, err = st.db.NewRaw(
				`UPDATE a_legs SET interleaved_state_json = ? WHERE a_leg_id = ?`,
				badJSON, leg.ALegID,
			).Exec(ctx)
			require.NoError(t, err)

			_, err = st.FetchInterleavedState(ctx, leg.ALegID)
			assert.Error(t, err, "corrupt JSON with invalid cycle cursor must fail validation")
		})
	})

	t.Run("RouteOverride", func(t *testing.T) {
		t.Run("contract", func(t *testing.T) {
			routeoverrideStorecontract.RunAll(t, f.RouteOverrideEnv(t))
		})

		t.Run("deleteCascades", func(t *testing.T) {
			st := f.NewStore(t)
			ck := fmt.Sprintf("ck-ov-cascade-%d", time.Now().UnixNano())
			leg, err := st.CreateALeg(ctx, ck)
			require.NoError(t, err)

			_, err = st.Replace(ctx, leg.ALegID, "openai:gpt-4", time.Unix(1, 0).UTC())
			require.NoError(t, err)

			// Recreating with the same continuity key removes the old A-leg
			_, err = st.CreateALeg(ctx, ck)
			require.NoError(t, err)

			var n int
			err = st.db.NewRaw(`SELECT count(*) FROM a_leg_route_overrides WHERE a_leg_id = ?`, leg.ALegID).Scan(ctx, &n)
			require.NoError(t, err)
			assert.Equal(t, 0, n, "foreign key cascade must remove route override when A-leg is deleted")

			_, err = st.Snapshot(ctx, leg.ALegID)
			assert.ErrorIs(t, err, routeoverride.ErrNotFound)
		})

		t.Run("legacyALegHasNoRow", func(t *testing.T) {
			st := f.NewStore(t)
			leg, err := st.CreateALeg(ctx, fmt.Sprintf("ck-ov-legacy-%d", time.Now().UnixNano()))
			require.NoError(t, err)

			var n int
			err = st.db.NewRaw(`SELECT count(*) FROM a_leg_route_overrides WHERE a_leg_id = ?`, leg.ALegID).Scan(ctx, &n)
			require.NoError(t, err)
			assert.Equal(t, 0, n, "newly created A-leg must not have a row in a_leg_route_overrides")

			got, err := st.Snapshot(ctx, leg.ALegID)
			require.NoError(t, err)
			assert.False(t, got.Active)
			assert.Equal(t, int64(0), got.Revision)
			assert.Empty(t, got.Selector)
		})

		t.Run("recreateContinuityKeyDoesNotInherit", func(t *testing.T) {
			st := f.NewStore(t)
			ck := fmt.Sprintf("ck-ov-recreate-%d", time.Now().UnixNano())
			leg1, err := st.CreateALeg(ctx, ck)
			require.NoError(t, err)

			_, err = st.Replace(ctx, leg1.ALegID, "openai:gpt-4", time.Unix(1, 0).UTC())
			require.NoError(t, err)

			leg2, err := st.CreateALeg(ctx, ck)
			require.NoError(t, err)
			assert.NotEqual(t, leg1.ALegID, leg2.ALegID)

			got, err := st.Snapshot(ctx, leg2.ALegID)
			require.NoError(t, err)
			assert.False(t, got.Active)
			assert.Equal(t, int64(0), got.Revision)
			assert.Empty(t, got.Selector)
		})
	})

	t.Run("RestartSurvival", func(t *testing.T) {
		t.Run("aLegAndAttempts", func(t *testing.T) {
			s1, reopen := f.ReopenStore(t)
			ck := fmt.Sprintf("ck-restart-leg-%d", time.Now().UnixNano())
			leg, err := s1.CreateALeg(ctx, ck)
			require.NoError(t, err)

			bleg, err := s1.NextBLeg(ctx, leg.ALegID)
			require.NoError(t, err)

			rec := aipapi.AttemptRecord{
				BLegID:         bleg.BLegID,
				ALegID:         leg.ALegID,
				Seq:            bleg.Seq,
				BackendID:      "stub-restart",
				EffectiveModel: "m-restart",
				StartedAt:      time.Unix(100, 0),
				FinishedAt:     time.Unix(200, 0),
				Outcome:        aipapi.AttemptSuccess,
				Reason:         "ok",
			}
			require.NoError(t, s1.RecordAttempt(ctx, rec))

			s2 := reopen()

			got, err := s2.ResolveALeg(ctx, ck)
			require.NoError(t, err)
			assert.Equal(t, leg.ALegID, got.ALegID)

			attempts, err := s2.LoadAttempts(ctx, leg.ALegID)
			require.NoError(t, err)
			require.Len(t, attempts, 1)
			assert.Equal(t, "stub-restart", attempts[0].BackendID)
			assert.Equal(t, aipapi.AttemptSuccess, attempts[0].Outcome)
		})

		t.Run("interleavedState", func(t *testing.T) {
			s1, reopen := f.ReopenStore(t)
			ck := fmt.Sprintf("ck-restart-intl-%d", time.Now().UnixNano())
			leg, err := s1.CreateALeg(ctx, ck)
			require.NoError(t, err)

			want := interleavedstate.State{
				Cycle: interleavedstate.CycleState{
					SelectorKey: "sk-restart",
					Sequence: []interleavedstate.CycleEntry{
						{Key: "exec-1", Role: interleavedstate.RoleExecutor},
						{Key: "think-1", Role: interleavedstate.RoleThinker},
					},
					NextIndex: 1,
				},
			}
			require.NoError(t, s1.SetInterleavedState(ctx, leg.ALegID, want))

			s2 := reopen()

			got, err := s2.FetchInterleavedState(ctx, leg.ALegID)
			require.NoError(t, err)
			assert.True(t, got.Equal(want), "interleaved state must round-trip across reopen: got %+v want %+v", got, want)
		})

		t.Run("routeOverride", func(t *testing.T) {
			s1, reopen := f.ReopenStore(t)
			ck := fmt.Sprintf("ck-restart-ov-%d", time.Now().UnixNano())
			leg, err := s1.CreateALeg(ctx, ck)
			require.NoError(t, err)

			now := time.Unix(1_700_000_000, 0).UTC()
			want, err := s1.Replace(ctx, leg.ALegID, "openai:gpt-4", now)
			require.NoError(t, err)

			s2 := reopen()

			got, err := s2.Snapshot(ctx, leg.ALegID)
			require.NoError(t, err)
			assert.True(t, got.Active)
			assert.Equal(t, want.Selector, got.Selector)
			assert.Equal(t, want.Revision, got.Revision)

			cleared, err := s2.Clear(ctx, leg.ALegID, now.Add(time.Second))
			require.NoError(t, err)
			assert.False(t, cleared.Active)

			s3 := reopen()

			gotAfterClear, err := s3.Snapshot(ctx, leg.ALegID)
			require.NoError(t, err)
			assert.False(t, gotAfterClear.Active)
			assert.Empty(t, gotAfterClear.Selector)
			assert.Equal(t, cleared.Revision, gotAfterClear.Revision)
		})
	})

	t.Run("MigrationAndSchemaParity", func(t *testing.T) {
		st := f.NewStore(t)
		require.NoError(t, dbparity.VerifySchema(ctx, st.db, ContinuityLogicalSchemaSpec()))

		_, thisFile, _, ok := runtime.Caller(0)
		require.True(t, ok)
		discovered, err := dbparity.DiscoverMigrations(filepath.Dir(thisFile))
		require.NoError(t, err)
		require.NotEmpty(t, discovered)

		var names []string
		rows, err := st.db.QueryContext(ctx, "SELECT name FROM bun_continuity_migrations")
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		recorded := make(map[string]bool)
		for rows.Next() {
			var name string
			require.NoError(t, rows.Scan(&name))
			names = append(names, name)
			id := name
			if len(name) >= 14 {
				id = name[:14]
			}
			recorded[id] = true
		}
		require.NoError(t, dbparity.AssertMigrationHistoryIDs(dbparity.MigrationIDs(discovered), recorded))

		// Verify migration rerun idempotency
		require.NoError(t, runContinuitySchemaMigrate(ctx, st.db))
		var countAfter int
		require.NoError(t, st.db.NewRaw("SELECT count(*) FROM bun_continuity_migrations").Scan(ctx, &countAfter))
		require.Equal(t, len(names), countAfter)
	})
}

// TestDBParity_SQLite is the canonical parity entry point for continuity persistence on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()
	runContinuityParitySuite(t, newSQLiteContinuityFixture(t))
}
