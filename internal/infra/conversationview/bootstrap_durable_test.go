package conversationview_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	continuity "github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	cv "github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

func TestBootstrap_SQLiteIndependentHandlesRollbackAndReopen(t *testing.T) {
	t.Parallel()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "bootstrap.db")) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	runBootstrapDurable(t, func() *bun.DB {
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		b, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
		return b
	})
}

func runBootstrapDurable(t *testing.T, open func() *bun.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	first, second := open(), open()
	legs, err := continuity.NewWithContext(ctx, first)
	require.NoError(t, err)
	leg, err := legs.CreateALeg(ctx, "")
	require.NoError(t, err)
	id := leg.ALegID
	s1, s2 := cv.NewBunStore(first), cv.NewBunStore(second)
	cleanupDB := open()
	t.Cleanup(func() { require.NoError(t, cv.NewBunStore(cleanupDB).DeleteALeg(context.Background(), id)) })
	decision := func() (cv.BootstrapDecision, error) {
		return cv.BootstrapDecision{Outcome: cv.BootstrapMatched, Model: strings.Repeat("界", 100), Overlays: []cv.PutSteeringRequest{bootstrapPut("owner.one", "one"), bootstrapPut("owner.two", "two")}}, nil
	}
	// A second-insert SQL failure must roll back the first insert, marker and slots.
	trigger := "bootstrap_fail_" + id
	fn := "bootstrap_function_" + id
	if first.Dialect().Name() == dialect.PG {
		_, err = first.NewRaw(`CREATE FUNCTION ?() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'PRIVATE PROMPT'; END $$`, bun.Ident(fn)).Exec(ctx)
		require.NoError(t, err)
		_, err = first.NewRaw(`CREATE TRIGGER ? BEFORE INSERT ON a_leg_steering_overlays FOR EACH ROW WHEN (NEW.a_leg_id = ? AND NEW.overlay_id = 'owner.two') EXECUTE FUNCTION ?()`, bun.Ident(trigger), id, bun.Ident(fn)).Exec(ctx)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, e := cleanupDB.NewRaw(`DROP TRIGGER IF EXISTS ? ON a_leg_steering_overlays`, bun.Ident(trigger)).Exec(context.Background())
			require.NoError(t, e)
			_, e = cleanupDB.NewRaw(`DROP FUNCTION IF EXISTS ?()`, bun.Ident(fn)).Exec(context.Background())
			require.NoError(t, e)
		})
	} else {
		_, err = first.NewRaw(`CREATE TRIGGER ? BEFORE INSERT ON a_leg_steering_overlays WHEN NEW.a_leg_id = ? AND NEW.overlay_id = 'owner.two' BEGIN SELECT RAISE(ABORT, 'PRIVATE PROMPT'); END`, bun.Ident(trigger), id).Exec(ctx)
		require.NoError(t, err)
	}
	result, err := s1.BootstrapSteering(ctx, id, "owner", decision)
	require.ErrorIs(t, err, cv.ErrBootstrapStorage)
	require.NotContains(t, err.Error(), "PRIVATE PROMPT")
	require.Empty(t, result.Mutations)
	snap, err := s2.Snapshot(ctx, id)
	require.NoError(t, err)
	require.Zero(t, snap.StateRevision)
	require.Empty(t, snap.Steering)
	var markers, states int
	require.NoError(t, first.NewRaw(`SELECT count(*) FROM a_leg_steering_bootstrap WHERE a_leg_id=?`, id).Scan(ctx, &markers))
	require.Zero(t, markers)
	require.NoError(t, first.NewRaw(`SELECT count(*) FROM a_leg_conversation_view_state WHERE a_leg_id=?`, id).Scan(ctx, &states))
	require.Zero(t, states)
	drop := `DROP TRIGGER ?`
	if first.Dialect().Name() == dialect.PG {
		drop += ` ON a_leg_steering_overlays`
	}
	_, err = first.NewRaw(drop, bun.Ident(trigger)).Exec(ctx)
	require.NoError(t, err)

	entered, release, started := make(chan struct{}), make(chan struct{}), make(chan struct{})
	type response struct {
		out cv.BootstrapResult
		err error
	}
	winner, loser := make(chan response, 1), make(chan response, 1)
	var callbacks atomic.Int32
	go func() {
		out, e := s1.BootstrapSteering(ctx, id, "owner", func() (cv.BootstrapDecision, error) {
			callbacks.Add(1)
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return decision()
		})
		winner <- response{out, e}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		close(started)
		out, e := s2.BootstrapSteering(ctx, id, "owner", func() (cv.BootstrapDecision, error) {
			callbacks.Add(1)
			return cv.BootstrapDecision{Outcome: cv.BootstrapNoMatch}, nil
		})
		loser <- response{out, e}
	}()
	<-started
	close(release)
	a, b := <-winner, <-loser
	require.NoError(t, a.err)
	require.NoError(t, b.err)
	require.False(t, a.out.Reused)
	require.True(t, b.out.Reused)
	require.EqualValues(t, 1, callbacks.Load())
	require.Equal(t, a.out.Completion, b.out.Completion)
	require.Equal(t, strings.Repeat("界", 42), a.out.Completion.Model)
	require.EqualValues(t, 1, a.out.Mutations[0].SlotOrdinal)
	require.EqualValues(t, 2, a.out.Mutations[1].StateRevision)
	// Close both independent handles, then reconstruct from a fresh handle.
	require.NoError(t, first.Close())
	require.NoError(t, second.Close())
	third := open()
	s1 = cv.NewBunStore(third)
	replay, e := s1.BootstrapSteering(ctx, id, "owner", nil)
	require.NoError(t, e)
	require.True(t, replay.Reused)
	require.Equal(t, a.out.Completion, replay.Completion)
	require.NoError(t, s1.DeleteALeg(ctx, id))
	require.NoError(t, third.NewRaw(`SELECT count(*) FROM a_leg_steering_bootstrap WHERE a_leg_id=?`, id).Scan(ctx, &markers))
	require.Zero(t, markers)
	require.NoError(t, third.NewRaw(`SELECT count(*) FROM a_leg_steering_overlays WHERE a_leg_id=?`, id).Scan(ctx, &states))
	require.Zero(t, states)
	runBootstrapDurableFailures(t, cv.NewBunStore(third))
	t.Run("probe_rollback", func(t *testing.T) { runBootstrapDurableProbeRollback(t, cv.NewBunStore(third)) })
}

// A probe decision error must leave no trace: the failed decide rolls the whole
// transaction back (no bootstrap marker, no overlay), and a later valid matched
// decision still commits, so a first-turn retry remains possible.
func runBootstrapDurableProbeRollback(t *testing.T, s *cv.BunStore) {
	t.Helper()
	owner, err := continuity.NewWithContext(t.Context(), s.DB())
	require.NoError(t, err)
	a, err := owner.CreateALeg(t.Context(), "")
	require.NoError(t, err)
	id := a.ALegID
	t.Cleanup(func() { require.NoError(t, s.DeleteALeg(context.Background(), id)) })
	out, err := s.BootstrapSteering(t.Context(), id, "probe", func() (cv.BootstrapDecision, error) {
		return cv.BootstrapDecision{}, errors.New("probe")
	})
	require.ErrorIs(t, err, cv.ErrBootstrapDecision)
	require.Empty(t, out.Mutations)
	snap, err := s.Snapshot(t.Context(), id)
	require.NoError(t, err)
	require.Zero(t, snap.StateRevision)
	require.Empty(t, snap.Steering)
	var markers, overlays int
	require.NoError(t, s.DB().NewRaw(`SELECT count(*) FROM a_leg_steering_bootstrap WHERE a_leg_id=? AND producer_id=?`, id, "probe").Scan(t.Context(), &markers))
	require.Zero(t, markers)
	require.NoError(t, s.DB().NewRaw(`SELECT count(*) FROM a_leg_steering_overlays WHERE a_leg_id=?`, id).Scan(t.Context(), &overlays))
	require.Zero(t, overlays)
	out, err = s.BootstrapSteering(t.Context(), id, "probe", func() (cv.BootstrapDecision, error) {
		return cv.BootstrapDecision{Outcome: cv.BootstrapMatched, Overlays: []cv.PutSteeringRequest{bootstrapPut("probe.one", "one")}}, nil
	})
	require.NoError(t, err)
	require.False(t, out.Reused)
	require.Len(t, out.Mutations, 1)
	require.Equal(t, cv.BootstrapMatched, out.Completion.Outcome)
	require.NoError(t, s.DB().NewRaw(`SELECT count(*) FROM a_leg_steering_bootstrap WHERE a_leg_id=? AND producer_id=?`, id, "probe").Scan(t.Context(), &markers))
	require.Equal(t, 1, markers)
	snap, err = s.Snapshot(t.Context(), id)
	require.NoError(t, err)
	require.Len(t, snap.Steering, 1)
}

func runBootstrapDurableFailures(t *testing.T, s *cv.BunStore) {
	t.Helper()
	owner, err := continuity.NewWithContext(t.Context(), s.DB())
	require.NoError(t, err)
	for _, kind := range []string{"empty", "history", "collision", "count", "total", "invalid_utf8", "invalid_nul", "cancel", "cancel_after_insert", "revision", "slot"} {
		t.Run(kind, func(t *testing.T) {
			// Use a real continuity allocation owner rather than a synthetic row.
			a, err := owner.CreateALeg(t.Context(), "")
			require.NoError(t, err)
			id := a.ALegID
			t.Cleanup(func() { require.NoError(t, s.DeleteALeg(context.Background(), id)) })
			batch := []cv.PutSteeringRequest{bootstrapPut("owner.one", "one"), bootstrapPut("owner.two", "two")}
			expected := cv.ErrSteeringLimitExceeded
			switch kind {
			case "history":
				_, e := owner.NextBLeg(t.Context(), id)
				require.NoError(t, e)
				attempts, e := owner.LoadAttempts(t.Context(), id)
				require.NoError(t, e)
				require.Empty(t, attempts)
				out, e := s.BootstrapSteering(t.Context(), id, "owner", func() (cv.BootstrapDecision, error) {
					t.Error("allocated history called decision")
					return cv.BootstrapDecision{}, nil
				})
				require.NoError(t, e)
				require.Equal(t, cv.BootstrapPreexistingSkip, out.Completion.Outcome)
				require.Empty(t, out.Mutations)
				out, e = s.BootstrapSteering(t.Context(), id, "owner", nil)
				require.NoError(t, e)
				require.True(t, out.Reused)
				snap, e := s.Snapshot(t.Context(), id)
				require.NoError(t, e)
				require.Zero(t, snap.StateRevision)
				return
			case "empty":
				for _, outcome := range []cv.BootstrapOutcome{cv.BootstrapNoMatch, cv.BootstrapAmbiguousSkip} {
					out, e := s.BootstrapSteering(t.Context(), id, string(outcome), func() (cv.BootstrapDecision, error) { return cv.BootstrapDecision{Outcome: outcome}, nil })
					require.NoError(t, e)
					require.Empty(t, out.Mutations)
					out, e = s.BootstrapSteering(t.Context(), id, string(outcome), nil)
					require.NoError(t, e)
					require.True(t, out.Reused)
				}
				snap, e := s.Snapshot(t.Context(), id)
				require.NoError(t, e)
				require.Zero(t, snap.StateRevision)
				return
			case "collision":
				_, e := s.PutSteering(t.Context(), id, bootstrapPut("owner.orphan", "orphan"))
				require.NoError(t, e)
				_, e = s.DeactivateSteering(t.Context(), id, "owner.orphan")
				require.NoError(t, e)
				expected = cv.ErrBootstrapCollision
			case "invalid_utf8", "invalid_nul":
				batch[1].Message.Text = "\xff"
				if kind == "invalid_nul" {
					batch[1].Message.Text = "PRIVATE PROMPT\x00"
				}
				expected = cv.ErrBootstrapInvalid
			case "count":
				for i := range 63 {
					_, e := s.PutSteering(t.Context(), id, bootstrapPut(fmt.Sprintf("other.%d", i), "text"))
					require.NoError(t, e)
				}
			case "total":
				for i := range 3 {
					_, e := s.PutSteering(t.Context(), id, bootstrapPut(fmt.Sprintf("other.%d", i), strings.Repeat("x", cv.MaxSteeringTextBytes)))
					require.NoError(t, e)
				}
				batch[0].Message.Text = strings.Repeat("y", cv.MaxSteeringTextBytes)
			case "cancel", "cancel_after_insert":
				expected = context.Canceled
			case "revision", "slot":
				rev, slot := int64(0), int64(1)
				if kind == "revision" {
					rev = math.MaxInt64 - 1
				} else {
					slot = math.MaxInt64 - 1
				}
				_, e := s.DB().NewRaw(`INSERT INTO a_leg_conversation_view_state(a_leg_id,state_revision,next_slot_ordinal) VALUES(?,?,?)`, id, rev, slot).Exec(t.Context())
				require.NoError(t, e)
				expected = cv.ErrRevisionExhausted
			}
			before, e := s.Snapshot(t.Context(), id)
			require.NoError(t, e)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "cancel_after_insert" {
				s.DB().AddQueryHook(&bootstrapCancelAfterInsert{id: id, cancel: cancel})
			}
			out, e := s.BootstrapSteering(ctx, id, "owner", func() (cv.BootstrapDecision, error) {
				if kind == "cancel" {
					cancel()
				}
				return cv.BootstrapDecision{Outcome: cv.BootstrapMatched, Overlays: batch}, nil
			})
			require.ErrorIs(t, e, expected)
			require.Empty(t, out.Mutations)
			after, e := s.Snapshot(t.Context(), id)
			require.NoError(t, e)
			require.Equal(t, before, after)
			var n int
			require.NoError(t, s.DB().NewRaw(`SELECT count(*) FROM a_leg_steering_bootstrap WHERE a_leg_id=?`, id).Scan(t.Context(), &n))
			require.Zero(t, n)
		})
	}
}

// This narrow fault seam cancels after a real successful intermediate insert,
// not before SQL work begins. The surrounding transaction must undo that write.
type bootstrapCancelAfterInsert struct {
	id     string
	cancel context.CancelFunc
	once   sync.Once
}

func (*bootstrapCancelAfterInsert) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *bootstrapCancelAfterInsert) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	if event.Err == nil && strings.Contains(event.Query, "INSERT INTO a_leg_steering_overlays") && strings.Contains(event.Query, h.id) {
		h.once.Do(h.cancel)
	}
}
