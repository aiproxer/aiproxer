package proxymemorystore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// runContract runs the behavioral store contract of design §12.2–§12.6 against
// a freshly opened database: the §12.2 session upserts and the §12.6 published
// summary directly, the §12.3–§12.5 claim, completion, publish and
// consolidation-input cases through runLeaseContract, and the §12.6 service
// queries, forget and purge cases through runServiceContract, which also runs
// the PostgreSQL bank-lock interleavings that keep the erasure of Requirement
// 8.3 inviolate. open must return a *bun.DB with the proxy memory schema
// applied; store_test.go (in-memory SQLite) and dbparity_postgres_test.go
// (PostgreSQL Direct) provide the dialect-specific opener. openObserver opens
// a separate connection for database-state observation of interleavings that
// occupy every connection in the contract pool; it is nil for a dialect with no
// such observation.
//
// The settings contract of design §4 runs through runSettingsContract, which
// needs a second handle to the same database: openSecond returns it, and it
// stands for a second instance of the shared store.
//
// Every subtest works in its own fixture namespace (A-leg IDs and project keys
// carrying both the subtest scope and a per-run token), so no two cases — or
// two concurrent runs — can collide on a shared database. Each namespace is
// removed again through the subtest's own opener handle.
func runContract(t *testing.T, open func(t *testing.T) *bun.DB, openSecond func(t *testing.T, db *bun.DB) *bun.DB, openObserver func(t *testing.T) *bun.DB) {
	t.Helper()
	runID := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)

	openFixture := func(t *testing.T, f *contractFixture) (*bun.DB, *proxymemorystore.Store) {
		t.Helper()
		db := open(t)
		ensureALegTable(t, db)
		f.seedALeg = func(aLegID string) {
			seedActiveALeg(t, db, aLegID, 0)
			// A capture is only learnable input when the A-leg carries the
			// durable first-turn binding of its owner, so the fixture seeds
			// that binding with the A-leg.
			seedBindingRow(t, db, f.alice, aLegID, f.projectP, "/work/one")
		}
		t.Cleanup(func() { f.removeRows(t, db) })
		return db, proxymemorystore.New(db, proxymemorystore.NewALegReader(db))
	}

	t.Run("admission reads one bounded first-turn snapshot without binding", func(t *testing.T) {
		f := newContractFixture("admission_snapshot", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("snapshot")
		removeBindingRow(t, db, aLeg)
		seedActiveALeg(t, db, aLeg, 7)
		principal := proxymemory.Principal{TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID}
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: "/work/one", Version: 3, MemorySummary: "frozen summary"})

		got, err := st.ReadAdmission(ctx, proxymemory.AdmissionQuery{
			Principal: principal, ALegID: aLeg, AuthoritativeFirstTurn: true,
			Candidate: proxymemory.Project{Key: f.projectP, Label: "/work/one"},
		})
		require.NoError(t, err)
		assert.False(t, got.Bound)
		assert.Equal(t, proxymemory.Project{Key: f.projectP, Label: "/work/one"}, got.Project)
		assert.Equal(t, int64(7), got.NextBSeq)
		assert.Equal(t, int64(0), got.ErasureEpoch)
		assert.Equal(t, "frozen summary", got.Summary)
		assert.Equal(t, int64(3), got.SummaryVersion)
		assert.Equal(t, 0, bindingCount(t, db, aLeg), "the request snapshot must not write a binding")

		resumed, err := st.ReadAdmission(ctx, proxymemory.AdmissionQuery{Principal: principal, ALegID: aLeg})
		require.NoError(t, err)
		assert.Empty(t, resumed.Project, "a resumed request cannot derive a project without a binding or cache")
		assert.Empty(t, resumed.Summary, "a resumed request cannot read the current bank")

		cached, err := st.ReadAdmission(ctx, proxymemory.AdmissionQuery{
			Principal: principal, ALegID: aLeg,
			CachedProject: &proxymemory.Project{Key: f.projectP, Label: "/work/one"},
		})
		require.NoError(t, err)
		assert.Equal(t, f.projectP, cached.Project.Key)
		assert.Empty(t, cached.Summary, "a cached resumed decision validates its epoch without rereading the bank")
	})

	t.Run("forget advances the erasure epoch before an unbound decision commits", func(t *testing.T) {
		f := newContractFixture("admission_forget_epoch", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("queued_unbound")
		removeBindingRow(t, db, aLeg)
		seedActiveALeg(t, db, aLeg, 0)
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: "/work/one", MemorySummary: "to forget"})
		job := proxymemory.FirstTurnJob{
			Principal: proxymemory.Principal{TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID},
			ALegID:    aLeg, Project: proxymemory.Project{Key: f.projectP, Label: "/work/one"},
			Decision:     proxymemory.FirstTurnDecision{Outcome: proxymemory.FirstTurnNoSummary},
			ErasureEpoch: 0, EpochKnown: true,
		}

		require.NoError(t, st.Forget(ctx, f.alice, 2_000))
		committed, err := st.CommitFirstTurn(ctx, job)
		require.NoError(t, err)
		assert.False(t, committed, "a pre-Forget queued decision must not restore an unbound A-leg")
		assert.Equal(t, 0, bindingCount(t, db, aLeg))
		var epoch int64
		require.NoError(t, db.NewRaw(`SELECT epoch FROM proxy_memory_erasure_epochs WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
			f.alice.TenantID, f.alice.PrincipalID, f.projectP).Scan(ctx, &epoch))
		assert.Equal(t, int64(1), epoch)
	})

	t.Run("forget scrubs a persisted first-turn snapshot and rejects its queued writer", func(t *testing.T) {
		f := newContractFixture("forget_first_turn_snapshot", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("bound")
		removeBindingRow(t, db, aLeg)
		seedActiveALeg(t, db, aLeg, 0)
		principal := proxymemory.Principal{TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID}
		project := proxymemory.Project{Key: f.projectP, Label: "/private/workspace"}
		const forgottenText = "private remembered summary"
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: project.Label, Version: 4, MemorySummary: forgottenText})
		job := proxymemory.FirstTurnJob{
			Principal: principal, ALegID: aLeg, Project: project,
			Decision: proxymemory.FirstTurnDecision{
				Outcome: proxymemory.FirstTurnMatched, Injection: forgottenText, SummaryVersion: 4,
			},
			EpochKnown: true,
		}

		committed, err := st.CommitFirstTurn(ctx, job)
		require.NoError(t, err)
		require.True(t, committed)
		before, err := st.ReadAdmission(ctx, proxymemory.AdmissionQuery{
			Principal: principal, ALegID: aLeg, AuthoritativeFirstTurn: true, Candidate: project,
		})
		require.NoError(t, err)
		require.NotNil(t, before.Decision)
		require.Equal(t, forgottenText, before.Decision.Injection)

		require.NoError(t, st.Forget(ctx, f.alice, 2_000))
		committed, err = st.CommitFirstTurn(ctx, job)
		require.NoError(t, err)
		assert.False(t, committed, "a queued pre-Forget writer must not refill the erased snapshot")

		var label, outcome, injection string
		var version, epoch int64
		require.NoError(t, db.NewRaw(`SELECT project_label, first_turn_outcome, first_turn_injection,
			first_turn_summary_version, first_turn_epoch FROM proxy_memory_session_bindings WHERE a_leg_id = ?`, aLeg).
			Scan(ctx, &label, &outcome, &injection, &version, &epoch))
		assert.Empty(t, label)
		assert.Empty(t, outcome)
		assert.Empty(t, injection)
		assert.Zero(t, version)
		assert.Zero(t, epoch)

		after, err := st.ReadAdmission(ctx, proxymemory.AdmissionQuery{
			Principal: principal, ALegID: aLeg, AuthoritativeFirstTurn: true, Candidate: project,
		})
		require.NoError(t, err)
		assert.Nil(t, after.Decision)
		assert.Empty(t, after.Summary)
		assert.Equal(t, int64(1), after.ErasureEpoch)
	})

	t.Run("forget drops an unbound capture queued at the prior epoch", func(t *testing.T) {
		f := newContractFixture("capture_forget_epoch", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("queued_unbound_capture")
		removeBindingRow(t, db, aLeg)
		seedActiveALeg(t, db, aLeg, 0)
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: "/work/one"})

		// This capture was admitted with epoch zero before Forget, but no binding
		// existed for Forget to fence. The writer must validate the epoch too.
		require.NoError(t, st.Forget(ctx, f.alice, 2_000))
		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg,
			Transcript: `["pre-forget history"]`, ActivityMS: 1_000,
			SourceSeq: 1, ErasureEpoch: 0,
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertStale, outcome)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		assert.Equal(t, 0, bindingCount(t, db, aLeg))
	})

	t.Run("capture cannot recreate a retired A-leg session", func(t *testing.T) {
		f := newContractFixture("capture_retired_aleg", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("retired")
		removeBindingRow(t, db, aLeg)
		seedActiveALeg(t, db, aLeg, 0)
		_, err := db.NewRaw(`DELETE FROM a_legs WHERE a_leg_id = ?`, aLeg).Exec(ctx)
		require.NoError(t, err)
		committed, err := st.CommitFirstTurn(ctx, proxymemory.FirstTurnJob{
			Principal: proxymemory.Principal{TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID},
			ALegID:    aLeg, Project: proxymemory.Project{Key: f.projectP, Label: "/work/one"},
			Decision:   proxymemory.FirstTurnDecision{Outcome: proxymemory.FirstTurnNoSummary},
			EpochKnown: true,
		})
		require.NoError(t, err)
		assert.False(t, committed, "a delayed decision must not recreate a retired A-leg binding")
		assert.Equal(t, 0, bindingCount(t, db, aLeg))

		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg,
			Transcript: `["retired history"]`, ActivityMS: 1_000,
			SourceSeq: 1, ErasureEpoch: 0,
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertStale, outcome)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
	})

	t.Run("maintenance sweeps bounded retired bindings but preserves active A-legs", func(t *testing.T) {
		f := newContractFixture("retired_binding_sweep", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		principal := proxymemory.Principal{TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID}
		legs := []string{f.aLeg("active_a"), f.aLeg("active_b"), f.aLeg("retired_a"), f.aLeg("retired_b")}
		for _, aLeg := range legs {
			seedBinding(t, st, principal, aLeg, proxymemory.Project{Key: f.projectP, Label: "/work/one"})
		}
		for _, aLeg := range legs[2:] {
			_, err := db.NewRaw(`DELETE FROM a_legs WHERE a_leg_id = ?`, aLeg).Exec(ctx)
			require.NoError(t, err)
		}

		deleted, err := st.Purge(ctx, 9_000, 8_000, 8_000, 1)
		require.NoError(t, err)
		assert.Equal(t, 0, deleted, "one maintenance pass must not sweep an active binding")
		assert.Equal(t, 1, bindingCount(t, db, legs[0]))
		assert.Equal(t, 1, bindingCount(t, db, legs[1]))
		deleted, err = st.Purge(ctx, 9_000, 8_000, 8_000, 1)
		require.NoError(t, err)
		assert.Equal(t, 0, deleted, "one maintenance pass must not sweep an active binding")
		assert.Equal(t, 1, bindingCount(t, db, legs[0]))
		assert.Equal(t, 1, bindingCount(t, db, legs[1]))
		deleted, err = st.Purge(ctx, 9_000, 8_000, 8_000, 1)
		require.NoError(t, err)
		assert.Equal(t, 1, deleted, "one maintenance pass must honor its limit")
		assert.Equal(t, 0, bindingCount(t, db, legs[2]), "the retired binding behind the active prefix is swept")
		assert.Equal(t, 1, bindingCount(t, db, legs[3]))
		deleted, err = st.Purge(ctx, 9_000, 8_000, 8_000, 1)
		require.NoError(t, err)
		assert.Equal(t, 1, deleted)
		assert.Equal(t, 1, bindingCount(t, db, legs[0]))
		assert.Equal(t, 1, bindingCount(t, db, legs[1]))
		assert.Equal(t, 0, bindingCount(t, db, legs[2])+bindingCount(t, db, legs[3]))
	})

	t.Run("the retired-binding sweep is bounded per call and reaches a late orphan", func(t *testing.T) {
		f := newContractFixture("retired_binding_bound", runID)
		db, _ := openFixture(t, f)
		ctx := context.Background()
		// The orphan sits behind an all-active prefix: the sweep must reach it
		// across calls, and no call may examine the whole binding table. The
		// fixture seeds every A-leg with its durable binding, so the orphan is
		// the A-leg whose authoritative core row is gone.
		legs := []string{f.aLeg("bound_active_a"), f.aLeg("bound_active_b"), f.aLeg("bound_orphan")}
		_, err := db.NewRaw(`DELETE FROM a_legs WHERE a_leg_id = ?`, legs[2]).Exec(ctx)
		require.NoError(t, err, "the orphan's A-leg must be gone from the core continuity database")
		spy := &countingCoreALegs{CoreALegs: proxymemorystore.NewALegReader(db)}
		sweeper := proxymemorystore.New(db, spy)

		deleted := 0
		for range 16 {
			before, _ := spy.snapshot()
			deleted, err = sweeper.Purge(ctx, 9_000, 8_000, 8_000, 1)
			require.NoError(t, err)
			calls, batches := spy.snapshot()
			assert.Equal(t, before+1, calls, "one invocation must issue one core liveness query")
			assert.Equal(t, []int{1}, batches[before:], "one invocation must examine a bounded candidate set")
			if deleted > 0 {
				break
			}
		}
		assert.Equal(t, 1, deleted, "the sweep must reach the orphan behind the active prefix")
		assert.Equal(t, 0, bindingCount(t, db, legs[2]))
		assert.Equal(t, 1, bindingCount(t, db, legs[0]))
		assert.Equal(t, 1, bindingCount(t, db, legs[1]))
	})

	t.Run("forget fences an admitted session before its first delayed capture", func(t *testing.T) {
		f := newContractFixture("forget_admission", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("pending")
		p := proxymemory.Principal{TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID}
		seedBinding(t, st, p, aLeg, proxymemory.Project{Key: f.projectP, Label: "/private/workspace"})
		seq := admissionSeq(t, db, aLeg)
		foreignLeg := f.aLeg("foreign_pending")
		removeBindingRow(t, db, foreignLeg)
		seedBinding(t, st, proxymemory.Principal{TenantID: p.TenantID, PrincipalID: "other"}, foreignLeg,
			proxymemory.Project{Key: f.projectP, Label: "/other/private/workspace"})
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: "/private/workspace", MemorySummary: "old memory"})
		require.NoError(t, st.Forget(ctx, f.alice, 2_000))
		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/private/workspace", ALegID: aLeg,
			Transcript: `["pre-forget history"]`, SourceSeq: seq, ActivityMS: 1_000,
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertForgotten, outcome)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		var label string
		require.NoError(t, db.NewRaw(`SELECT project_label FROM proxy_memory_session_bindings WHERE a_leg_id = ?`, aLeg).Scan(ctx, &label))
		assert.Empty(t, label, "Forget must erase the unnecessary private workspace label")
		require.NoError(t, db.NewRaw(`SELECT project_label FROM proxy_memory_session_bindings WHERE a_leg_id = ?`, foreignLeg).Scan(ctx, &label))
		assert.Equal(t, "/other/private/workspace", label, "another principal's binding must remain untouched")
		cfg := proxymemory.DefaultConfig()
		worker := proxymemory.NewWorker(cfg, st, auxiliary.DisabledBackgroundClient{}, nil,
			func() time.Time { return time.UnixMilli(1_000).Add(24 * time.Hour) }, nil)
		worker.Tick(ctx)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		_, exists := readBank(t, db, f.alice)
		assert.False(t, exists, "the delayed pre-forget capture must not recreate the forgotten bank")
	})

	t.Run("first project decision survives absent project and store reconstruction", func(t *testing.T) {
		f := newContractFixture("binding", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		p := proxymemory.Principal{TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID}
		for _, first := range []proxymemory.Project{{}, {Key: f.projectP, Label: "/work/first"}} {
			aLeg := f.aLeg("binding_" + first.Key)
			removeBindingRow(t, db, aLeg)
			seedBinding(t, st, p, aLeg, first)
			other := proxymemory.Project{Key: f.projectQ, Label: "/work/later"}
			committed, err := proxymemorystore.New(db, proxymemorystore.NewALegReader(db)).CommitFirstTurn(ctx, proxymemory.FirstTurnJob{
				Principal: p, ALegID: aLeg, Project: other,
				Decision:   proxymemory.FirstTurnDecision{Outcome: proxymemory.FirstTurnNoSummary},
				EpochKnown: true,
			})
			require.NoError(t, err)
			assert.False(t, committed, "later requests must not replace the first decision")
			for _, foreign := range []proxymemory.Principal{
				{TenantID: p.TenantID, PrincipalID: "other"},
				{TenantID: "other", PrincipalID: p.PrincipalID},
			} {
				committed, err := st.CommitFirstTurn(ctx, proxymemory.FirstTurnJob{
					Principal: foreign, ALegID: aLeg, Project: first,
					Decision:   proxymemory.FirstTurnDecision{Outcome: proxymemory.FirstTurnNoSummary},
					EpochKnown: true,
				})
				require.NoError(t, err)
				assert.False(t, committed, "another principal must not bind this A-leg")
				_, err = st.ReadAdmission(ctx, proxymemory.AdmissionQuery{Principal: foreign, ALegID: aLeg})
				require.ErrorIs(t, err, proxymemory.ErrNotFound,
					"another principal must not read this A-leg's binding")
			}
		}
	})

	t.Run("admission order survives equal clocks reverse completion and extraction purge", func(t *testing.T) {
		runBase, err := strconv.ParseInt(runID, 36, 64)
		require.NoError(t, err)
		band := func(offset int64) int64 { return -runBase + offset }
		f := newContractFixture("source_order", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("source_order")
		seedBinding(t, st, proxymemory.Principal{TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID}, aLeg,
			proxymemory.Project{Key: f.projectP, Label: "/work/one"})
		older := admissionSeq(t, db, aLeg)
		require.Equal(t, int64(1), older)
		bumpALeg(t, db, aLeg)
		newer := admissionSeq(t, db, aLeg)
		require.Equal(t, int64(2), newer)
		capture := proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg,
			Transcript: `["old"]`, ActivityMS: 1_000, SourceSeq: older,
		}
		outcome, err := st.UpsertSession(ctx, capture)
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStored, outcome)
		capture.Transcript, capture.SourceSeq = `["new"]`, newer
		outcome, err = st.UpsertSession(ctx, capture)
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStored, outcome, "equal milliseconds must not reject a later admission")
		capture.SourceSeq, capture.ActivityMS = older, 9_000
		outcome, err = st.UpsertSession(ctx, capture)
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStale, outcome, "a later response clock cannot advance old history")
		assert.Equal(t, `["new"]`, readSession(t, db, aLeg).Transcript)
		scheduleSessionClaim(t, db, aLeg, band(0))
		claim := claimOneSession(t, st, "source-extract", band(2_000), band(3_000), 9_000)
		require.Equal(t, newer, claim.SourceSeq)
		require.NoError(t, st.CompleteSession(ctx, claim, extractionFor(claim, band(2_100))))
		_, err = st.Purge(ctx, band(4_000), band(-1_000), band(3_000), 100)
		require.NoError(t, err)
		outcome, err = st.UpsertSession(ctx, capture)
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStale, outcome, "durable order survives extraction retention")
		bumpALeg(t, db, aLeg)
		capture.SourceSeq = admissionSeq(t, db, aLeg)
		require.Equal(t, int64(3), capture.SourceSeq)
		capture.ActivityMS, capture.Transcript = 1_000, `["continuation"]`
		outcome, err = st.UpsertSession(ctx, capture)
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStored, outcome, "newer state remains valid even with an equal/older clock")
		scheduleSessionClaim(t, db, aLeg, band(0))
		claim = claimOneSession(t, st, "source-extract-2", band(4_000), band(5_000), 9_000)
		require.NoError(t, st.CompleteSession(ctx, claim, extractionFor(claim, band(4_100))))
		var seq int64
		require.NoError(t, db.NewRaw(`SELECT source_seq FROM proxy_memory_extraction_ledger WHERE a_leg_id = ?`, aLeg).Scan(ctx, &seq))
		require.Equal(t, int64(3), seq)
	})

	t.Run("stale content still advances idleness and protects an in-flight claim", func(t *testing.T) {
		f := newContractFixture("activity_order", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		runBase, err := strconv.ParseInt(runID, 36, 64)
		require.NoError(t, err)
		now := -runBase
		aLeg := f.aLeg("activity")
		capture := proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg,
			Transcript: `["newer history"]`, SourceSeq: 2, ActivityMS: 1_000,
		}
		outcome, err := st.UpsertSession(ctx, capture)
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStored, outcome)
		capture.SourceSeq, capture.ActivityMS, capture.Transcript = 1, 6_000, `["old history"]`
		outcome, err = st.UpsertSession(ctx, capture)
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStale, outcome)
		assert.Equal(t, `["newer history"]`, readSession(t, db, aLeg).Transcript)
		assert.Equal(t, int64(6_000), readSession(t, db, aLeg).LastActivityMS)
		capture.SourceSeq, capture.ActivityMS, capture.Transcript = 3, 2_000, `["latest history"]`
		_, err = st.UpsertSession(ctx, capture)
		require.NoError(t, err)
		assert.Equal(t, int64(6_000), readSession(t, db, aLeg).LastActivityMS, "clock regression must not advance idle eligibility")
		scheduleSessionClaim(t, db, aLeg, now)
		q := proxymemory.ClaimQuery{NowMS: now, NotBeforeMS: 0, IdleBeforeMS: 5_000, LeaseUntilMS: now + 1_000, LeaseOwner: "activity-worker", Limit: 1}
		claims, err := st.ClaimSessions(ctx, q)
		require.NoError(t, err)
		require.Empty(t, claims, "successful late release keeps the session outside the idle window")
		q.IdleBeforeMS = 6_000
		claims, err = st.ClaimSessions(ctx, q)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		capture.SourceSeq, capture.ActivityMS = 1, 8_000
		outcome, err = st.UpsertSession(ctx, capture)
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStale, outcome)
		require.NoError(t, st.CompleteSession(ctx, claims[0], extractionFor(claims[0], now+100)))
		row := readSession(t, db, aLeg)
		assert.Equal(t, `["latest history"]`, row.Transcript)
		assert.Equal(t, int64(8_000), row.LastActivityMS)
		assert.Empty(t, row.LeaseOwner, "the changed activity revision stays pending after completion")
		scheduleSessionClaim(t, db, aLeg, now)
		q.IdleBeforeMS = 7_000
		claims, err = st.ClaimSessions(ctx, q)
		require.NoError(t, err)
		assert.Empty(t, claims)
	})

	t.Run("the durable activity watermark survives extraction, stale releases and a regressed clock", func(t *testing.T) {
		f := newContractFixture("activity_watermark", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("activity_watermark")
		principal := proxymemory.Principal{TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID}
		seedBinding(t, st, principal, aLeg, proxymemory.Project{Key: f.projectP, Label: "/work/one"})

		// The second turn is stored, extracted, and its session row deleted.
		upsertCapture(t, st, f.alice, aLeg, `["second"]`, 1_000)
		scheduleSessionClaim(t, db, aLeg, 0)
		cs := claimOneSession(t, st, "extractor", 0, 4_000, 16_000)
		require.NoError(t, st.CompleteSession(ctx, cs, extractionFor(cs, -2_000)))
		require.Equal(t, 0, sessionCount(t, db, aLeg))
		var sourceActivity int64
		require.NoError(t, db.NewRaw(
			`SELECT source_activity_ms FROM proxy_memory_extraction_ledger WHERE a_leg_id = ?`, aLeg,
		).Scan(ctx, &sourceActivity))
		require.Equal(t, int64(1_000), sourceActivity, "the extracted source activity stays exact")

		// A stale release with a newer clock arrives after the extraction.
		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg,
			Transcript: `["stale"]`, ActivityMS: 6_000, SourceSeq: 1,
		})
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStale, outcome)

		// A later capture with a regressed clock must not lower the recorded
		// activity: the session stays outside the idle window.
		outcome, err = st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg,
			Transcript: `["third"]`, ActivityMS: 2_000, SourceSeq: testSourceSeq(3_000),
		})
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStored, outcome)
		require.Equal(t, int64(6_000), readSession(t, db, aLeg).LastActivityMS,
			"the durable activity watermark is monotone across the extraction")

		scheduleSessionClaim(t, db, aLeg, 0)
		claims, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: 0, NotBeforeMS: 0, IdleBeforeMS: 5_000,
			LeaseUntilMS: 4_000, LeaseOwner: "idle-worker", Limit: 1,
		})
		require.NoError(t, err)
		require.Empty(t, claims, "the session must not become idle-eligible before its newest release")
	})

	t.Run("first upsert stores revision 1", func(t *testing.T) {
		f := newContractFixture("first", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("first_revision")

		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.alice,
			ProjectLabel: "/work/one",
			ALegID:       aLeg,
			Transcript:   `["first"]`,
			ActivityMS:   1_000, SourceSeq: testSourceSeq(1_000),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertStored, outcome)

		row := readSession(t, db, aLeg)
		assert.Equal(t, f.alice.TenantID, row.TenantID)
		assert.Equal(t, f.alice.PrincipalID, row.PrincipalID)
		assert.Equal(t, f.alice.ProjectKey, row.ProjectKey)
		assert.Equal(t, "/work/one", row.ProjectLabel)
		assert.Equal(t, `["first"]`, row.Transcript)
		assert.Equal(t, int64(1), row.Revision)
		assert.Equal(t, int64(1_000), row.LastActivityMS)
		assert.Equal(t, "", row.LeaseOwner)
		assert.Equal(t, int64(0), row.LeaseUntilMS)
		assert.Equal(t, int64(0), row.Attempts)
		assert.Equal(t, int64(0), row.NextAttemptMS)
	})

	t.Run("second upsert for the same owner bumps revision and replaces content", func(t *testing.T) {
		f := newContractFixture("second", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("second_revision")

		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.alice,
			ProjectLabel: "/work/one",
			ALegID:       aLeg,
			Transcript:   `["one"]`,
			ActivityMS:   1_000, SourceSeq: testSourceSeq(1_000),
		})
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStored, outcome)

		_, err = db.NewRaw(`UPDATE proxy_memory_sessions SET attempts = 3, next_attempt_ms = 999 WHERE a_leg_id = ?`, aLeg).Exec(ctx)
		require.NoError(t, err)

		outcome, err = st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.alice,
			ProjectLabel: "/work/other",
			ALegID:       aLeg,
			Transcript:   `["two"]`,
			ActivityMS:   2_000, SourceSeq: testSourceSeq(2_000),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertStored, outcome)

		row := readSession(t, db, aLeg)
		assert.Equal(t, int64(2), row.Revision)
		assert.Equal(t, `["two"]`, row.Transcript)
		assert.Equal(t, int64(2_000), row.LastActivityMS)
		// The first turn decides the project: a later capture never moves the session (Requirement 2.4).
		assert.Equal(t, "/work/one", row.ProjectLabel)
		assert.Equal(t, int64(0), row.Attempts)
		assert.Equal(t, int64(0), row.NextAttemptMS)
	})

	t.Run("an older or duplicate admission never overwrites a newer row", func(t *testing.T) {
		f := newContractFixture("stale_capture", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("stale_capture")

		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.alice,
			ProjectLabel: "/work/one",
			ALegID:       aLeg,
			Transcript:   `["newest"]`,
			ActivityMS:   2_000, SourceSeq: testSourceSeq(2_000),
		})
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStored, outcome)
		// The newest capture's scheduling state: a refused write must not clear it.
		_, err = db.NewRaw(`UPDATE proxy_memory_sessions SET attempts = 3, next_attempt_ms = 999 WHERE a_leg_id = ?`, aLeg).Exec(ctx)
		require.NoError(t, err)

		// An older capture of the same session is refused as stale and stores
		// nothing (Requirements 3.1, 4.3: capture freshness must not replace
		// newer session material or extracted memory with an older turn).
		outcome, err = st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.alice,
			ProjectLabel: "/work/one",
			ALegID:       aLeg,
			Transcript:   `["stale"]`,
			ActivityMS:   1_000, SourceSeq: testSourceSeq(1_000),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertStale, outcome)

		// Redelivery of the same source sequence is refused too: the stored
		// state already covers that admission, so it must not bump the revision.
		outcome, err = st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.alice,
			ProjectLabel: "/work/one",
			ALegID:       aLeg,
			Transcript:   `["same millisecond"]`,
			ActivityMS:   2_000, SourceSeq: testSourceSeq(2_000),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertStale, outcome)

		// An owner mismatch outranks staleness: a foreign owner never learns
		// whether the row would have been fresh.
		outcome, err = st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.owner("bob", f.projectP),
			ProjectLabel: "/work/evil",
			ALegID:       aLeg,
			Transcript:   `["stolen"]`,
			ActivityMS:   1_000, SourceSeq: testSourceSeq(1_000),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertOwnerMismatch, outcome)

		row := readSession(t, db, aLeg)
		assert.Equal(t, `["newest"]`, row.Transcript)
		assert.Equal(t, int64(1), row.Revision)
		assert.Equal(t, int64(2_000), row.LastActivityMS)
		assert.Equal(t, int64(3), row.Attempts)
		assert.Equal(t, int64(999), row.NextAttemptMS)

		// A strictly newer capture advances the row again.
		outcome, err = st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.alice,
			ProjectLabel: "/work/one",
			ALegID:       aLeg,
			Transcript:   `["newer"]`,
			ActivityMS:   3_000, SourceSeq: testSourceSeq(3_000),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertStored, outcome)

		row = readSession(t, db, aLeg)
		assert.Equal(t, `["newer"]`, row.Transcript)
		assert.Equal(t, int64(2), row.Revision)
		assert.Equal(t, int64(3_000), row.LastActivityMS)
		assert.Equal(t, int64(0), row.Attempts)
		assert.Equal(t, int64(0), row.NextAttemptMS)
	})

	t.Run("a post-completion stale capture cannot recreate an extracted session", func(t *testing.T) {
		runBase, err := strconv.ParseInt(runID, 36, 64)
		require.NoError(t, err)
		activity := func(offset int64) int64 { return runBase + offset }
		band := func(offset int64) int64 { return -runBase + offset }
		f := newContractFixture("stale_after_complete", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("stale_after_complete")

		upsertCapture(t, st, f.alice, aLeg, `["turn one"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))
		require.Equal(t, aLeg, cs.ALegID)
		require.NoError(t, st.CompleteSession(ctx, cs, extractionFor(cs, band(100))))
		// Requirement 4.3: the extraction replaced the stored transcript, so the
		// row is gone and the ledger holds the source activity it distilled.
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		stored, ok := readExtraction(t, db, aLeg)
		require.True(t, ok)
		require.Equal(t, activity(8), stored.SourceUpdatedMS)
		require.Equal(t, 1, ledgerCount(t, db, aLeg))

		// A delayed straggler whose activity is older than the extraction's
		// source must not resurrect the session row: the extracted memory
		// already covers that instant (Requirements 3.1, 4.3; design §12.2).
		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg, Transcript: `["straggler"]`, ActivityMS: activity(4), SourceSeq: testSourceSeq(activity(4)),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertStale, outcome)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))

		// An exact redelivery of the captured turn is refused by the recorded
		// watermark too.
		outcome, err = st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg, Transcript: `["turn one"]`, ActivityMS: activity(8), SourceSeq: testSourceSeq(activity(8)),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertStale, outcome)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		ledger, ok := readLedgerRow(t, db, aLeg)
		require.True(t, ok)
		assert.Equal(t, f.alice.TenantID, ledger.TenantID)
		assert.Equal(t, f.alice.PrincipalID, ledger.PrincipalID)
		assert.Equal(t, f.alice.ProjectKey, ledger.ProjectKey)
		assert.Equal(t, activity(8), ledger.SourceActivityMS)

		// Requirement 4.4: a genuinely newer turn still recreates the row.
		upsertCapture(t, st, f.alice, aLeg, `["turn two"]`, activity(12))
		row := readSession(t, db, aLeg)
		assert.Equal(t, `["turn two"]`, row.Transcript)
		assert.Equal(t, int64(1), row.Revision)

		// Once that turn has been extracted, the stale refusals track the
		// newest watermark.
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs = claimOneSession(t, st, "extractor-2", band(0), band(4_000), activity(16))
		require.Equal(t, aLeg, cs.ALegID)
		require.NoError(t, st.CompleteSession(ctx, cs, extractionFor(cs, band(200))))
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		outcome, err = st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg, Transcript: `["straggler two"]`, ActivityMS: activity(10), SourceSeq: testSourceSeq(activity(10)),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertStale, outcome)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		ledger, ok = readLedgerRow(t, db, aLeg)
		require.True(t, ok)
		assert.Equal(t, activity(12), ledger.SourceActivityMS)

		// A stale completion cannot lower either authority, including after
		// retention removes the extraction row. A rejected ledger update must
		// roll back the extraction insert in the same transaction.
		upsertCapture(t, st, f.alice, aLeg, `["turn three"]`, activity(14))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs = claimOneSession(t, st, "extractor-3", band(0), band(4_000), activity(16))
		require.Equal(t, aLeg, cs.ALegID)
		staleExtraction := extractionFor(cs, band(300))
		staleExtraction.SourceUpdatedMS = activity(10)
		require.ErrorIs(t, st.CompleteSession(ctx, cs, staleExtraction), proxymemory.ErrLeaseLost)
		stored, ok = readExtraction(t, db, aLeg)
		require.True(t, ok)
		assert.Equal(t, activity(12), stored.SourceUpdatedMS)
		_, err = st.Purge(ctx, band(1_000), band(-1_000), band(500), 1_000)
		require.NoError(t, err)
		assert.Equal(t, 0, extractionCount(t, db, aLeg))
		require.ErrorIs(t, st.CompleteSession(ctx, cs, staleExtraction), proxymemory.ErrLeaseLost)
		assert.Equal(t, 0, extractionCount(t, db, aLeg))
		assert.Equal(t, 1, sessionCount(t, db, aLeg))
		afterRejectedCompletion, ok := readLedgerRow(t, db, aLeg)
		require.True(t, ok)
		assert.Equal(t, ledger, afterRejectedCompletion)
	})

	t.Run("a purged extraction still fences its A-leg's freshness and ownership", func(t *testing.T) {
		runBase, err := strconv.ParseInt(runID, 36, 64)
		require.NoError(t, err)
		activity := func(offset int64) int64 { return runBase + offset }
		band := func(offset int64) int64 { return -runBase + offset }
		f := newContractFixture("stale_after_purge", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("stale_after_purge")

		upsertCapture(t, st, f.alice, aLeg, `["turn one"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))
		require.Equal(t, aLeg, cs.ALegID)
		require.NoError(t, st.CompleteSession(ctx, cs, extractionFor(cs, band(-2_000))))

		// The retention pass removes the aged extraction row; the ledger row is
		// what remains of the A-leg's participation.
		purged, err := st.Purge(ctx, band(0), band(-1_000), band(-1_000), 1_000)
		require.NoError(t, err)
		require.GreaterOrEqual(t, purged, 1)
		assert.Equal(t, 0, extractionCount(t, db, aLeg))
		assert.Equal(t, 1, ledgerCount(t, db, aLeg))

		// The stale refusal and the ownership verdict survive the purge
		// (Requirements 3.1, 4.3; design §12.2): the watermark, not the
		// extraction row, is what a later capture is compared against.
		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg, Transcript: `["straggler"]`, ActivityMS: activity(4), SourceSeq: testSourceSeq(activity(4)),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertStale, outcome)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))

		outcome, err = st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.owner("bob", f.projectP), ProjectLabel: "/work/evil", ALegID: aLeg, Transcript: `["stolen"]`, ActivityMS: activity(20), SourceSeq: testSourceSeq(activity(20)),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertOwnerMismatch, outcome)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
	})

	t.Run("a different principal reports owner mismatch and leaves the row unchanged", func(t *testing.T) {
		f := newContractFixture("principal_mismatch", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("principal_mismatch")

		_, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.alice,
			ProjectLabel: "/work/one",
			ALegID:       aLeg,
			Transcript:   `["original"]`,
			ActivityMS:   1_000, SourceSeq: testSourceSeq(1_000),
		})
		require.NoError(t, err)

		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.owner("bob", f.projectP),
			ProjectLabel: "/work/evil",
			ALegID:       aLeg,
			Transcript:   `["stolen"]`,
			ActivityMS:   9_000, SourceSeq: testSourceSeq(9_000),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertOwnerMismatch, outcome)

		row := readSession(t, db, aLeg)
		assert.Equal(t, f.alice.PrincipalID, row.PrincipalID)
		assert.Equal(t, `["original"]`, row.Transcript)
		assert.Equal(t, int64(1), row.Revision)
		assert.Equal(t, int64(1_000), row.LastActivityMS)
		assert.Equal(t, "/work/one", row.ProjectLabel)
	})

	t.Run("a different tenant reports owner mismatch and leaves the row unchanged", func(t *testing.T) {
		f := newContractFixture("tenant_mismatch", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("tenant_mismatch")

		_, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.alice,
			ProjectLabel: "/work/one",
			ALegID:       aLeg,
			Transcript:   `["original"]`,
			ActivityMS:   1_000, SourceSeq: testSourceSeq(1_000),
		})
		require.NoError(t, err)

		// The same principal and project under another tenant must not alias
		// the owner's A-leg, even though only the tenant differs.
		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: proxymemory.Owner{
				TenantID: "tenant-b", PrincipalID: f.alice.PrincipalID, ProjectKey: f.alice.ProjectKey,
			},
			ProjectLabel: "/work/evil",
			ALegID:       aLeg,
			Transcript:   `["stolen"]`,
			ActivityMS:   9_000, SourceSeq: testSourceSeq(9_000),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertOwnerMismatch, outcome)

		row := readSession(t, db, aLeg)
		assert.Equal(t, f.alice.TenantID, row.TenantID)
		assert.Equal(t, f.alice.PrincipalID, row.PrincipalID)
		assert.Equal(t, `["original"]`, row.Transcript)
		assert.Equal(t, int64(1_000), row.LastActivityMS)
	})

	t.Run("a different project key reports owner mismatch and leaves the row unchanged", func(t *testing.T) {
		f := newContractFixture("project_mismatch", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("project_mismatch")

		_, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.alice,
			ProjectLabel: "/work/one",
			ALegID:       aLeg,
			Transcript:   `["original"]`,
			ActivityMS:   1_000, SourceSeq: testSourceSeq(1_000),
		})
		require.NoError(t, err)

		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.owner(f.alice.PrincipalID, f.projectQ),
			ProjectLabel: "/work/two",
			ALegID:       aLeg,
			Transcript:   `["moved"]`,
			ActivityMS:   9_000, SourceSeq: testSourceSeq(9_000),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertOwnerMismatch, outcome)

		row := readSession(t, db, aLeg)
		assert.Equal(t, f.alice.ProjectKey, row.ProjectKey)
		assert.Equal(t, `["original"]`, row.Transcript)
		assert.Equal(t, int64(1), row.Revision)
		assert.Equal(t, int64(1_000), row.LastActivityMS)
	})

	t.Run("a fenced A-leg is refused and stores nothing", func(t *testing.T) {
		f := newContractFixture("fenced", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("fenced")

		_, err := db.NewRaw(
			`INSERT INTO proxy_memory_fence (a_leg_id, tenant_id, principal_id, forgotten_at_ms) VALUES (?, ?, ?, ?)`,
			aLeg, f.alice.TenantID, f.alice.PrincipalID, 5_000,
		).Exec(ctx)
		require.NoError(t, err)

		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner:        f.alice,
			ProjectLabel: "/work/one",
			ALegID:       aLeg,
			Transcript:   `["resurrected"]`,
			ActivityMS:   9_000, SourceSeq: testSourceSeq(9_000),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertForgotten, outcome)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
	})

	t.Run("an A-leg extracted under a different owner keeps its first bank", func(t *testing.T) {
		f := newContractFixture("extraction_owner", runID)
		cases := []struct {
			name  string
			slug  string
			owner proxymemory.Owner
			want  proxymemory.UpsertOutcome
		}{
			{
				name:  "different principal",
				slug:  "different_principal",
				owner: f.owner("bob", f.projectP),
				want:  proxymemory.UpsertOwnerMismatch,
			},
			{
				name:  "different project",
				slug:  "different_project",
				owner: f.owner(f.alice.PrincipalID, f.projectQ),
				want:  proxymemory.UpsertOwnerMismatch,
			},
			{
				name:  "same owner",
				slug:  "same_owner",
				owner: f.alice,
				want:  proxymemory.UpsertStored,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				db, st := openFixture(t, f)
				ctx := context.Background()
				aLeg := f.aLeg("extraction_" + tc.slug)
				seedExtraction(t, db, f.alice, aLeg)

				outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
					Owner:        tc.owner,
					ProjectLabel: "/work/other",
					ALegID:       aLeg,
					Transcript:   `["late"]`,
					ActivityMS:   3_000, SourceSeq: testSourceSeq(3_000),
				})
				require.NoError(t, err)
				assert.Equal(t, tc.want, outcome)

				if tc.want == proxymemory.UpsertStored {
					row := readSession(t, db, aLeg)
					assert.Equal(t, int64(1), row.Revision)
					assert.Equal(t, tc.owner, proxymemory.Owner{
						TenantID:    row.TenantID,
						PrincipalID: row.PrincipalID,
						ProjectKey:  row.ProjectKey,
					})
					return
				}
				assert.Equal(t, 0, sessionCount(t, db, aLeg))
			})
		}
	})

	t.Run("published summary is not found without a bank", func(t *testing.T) {
		f := newContractFixture("summary_missing", runID)
		_, st := openFixture(t, f)
		ctx := context.Background()

		summary, version, err := st.PublishedSummary(ctx, f.alice)
		require.ErrorIs(t, err, proxymemory.ErrNotFound)
		assert.Equal(t, "", summary)
		assert.Equal(t, int64(0), version)
	})

	t.Run("an empty published summary reads as not found", func(t *testing.T) {
		f := newContractFixture("summary_empty", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBank(t, db, f.alice, 7, "")

		summary, version, err := st.PublishedSummary(ctx, f.alice)
		require.ErrorIs(t, err, proxymemory.ErrNotFound)
		assert.Equal(t, "", summary)
		assert.Equal(t, int64(0), version)
	})

	t.Run("published summary returns the stored summary and version", func(t *testing.T) {
		f := newContractFixture("summary_success", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBank(t, db, f.alice, 3, "bank summary")

		summary, version, err := st.PublishedSummary(ctx, f.alice)
		require.NoError(t, err)
		assert.Equal(t, "bank summary", summary)
		assert.Equal(t, int64(3), version)
	})

	t.Run("a bank is invisible to another owner", func(t *testing.T) {
		f := newContractFixture("summary_isolation", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBank(t, db, f.alice, 3, "bank summary")

		for _, o := range []proxymemory.Owner{
			f.owner("bob", f.alice.ProjectKey),
			f.owner(f.alice.PrincipalID, f.projectQ),
			{TenantID: "tenant-b", PrincipalID: f.alice.PrincipalID, ProjectKey: f.alice.ProjectKey},
		} {
			summary, _, err := st.PublishedSummary(ctx, o)
			require.ErrorIs(t, err, proxymemory.ErrNotFound)
			assert.Equal(t, "", summary)
		}
	})

	runLeaseContract(t, openFixture, runID)
	runServiceContract(t, openFixture, runID)
	t.Run("phase failure evidence", func(t *testing.T) {
		runPhaseFailureContract(t, open, openObserver, runID)
	})
	runSettingsContract(t, open, openSecond)
}

// runSettingsContract runs the settings contract of design §4 against the
// engine's database: the store keeps the owner's own unprojected overrides and
// the trusted write-time dependency context, the owner key holds no project,
// two principals never see each other's settings, forgetting a bank preserves
// them, a second instance reads the same row, both phase rows and their
// context change atomically, and the revocation removes them.
//
// openSecond returns a second handle to the same database. It stands for a
// second instance of the shared store, so the contract proves the settings
// live in the database and not in the process that wrote them.
func runSettingsContract(t *testing.T, open func(t *testing.T) *bun.DB, openSecond func(t *testing.T, db *bun.DB) *bun.DB) {
	t.Helper()
	runID := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	tenant := "tenant-settings-" + runID
	alice := proxymemory.Principal{TenantID: tenant, PrincipalID: "alice-" + runID}
	bob := proxymemory.Principal{TenantID: tenant, PrincipalID: "bob-" + runID}
	carol := proxymemory.Principal{TenantID: tenant + "-rollback", PrincipalID: "carol-" + runID}
	projectKey := "project-settings-" + runID

	openFixture := func(t *testing.T) (*bun.DB, *proxymemorystore.Store) {
		t.Helper()
		db := open(t)
		store := proxymemorystore.New(db, proxymemorystore.NewALegReader(db))
		t.Cleanup(func() {
			ctx := context.Background()
			for _, principal := range []proxymemory.Principal{alice, bob, carol} {
				if _, err := db.NewRaw(`DELETE FROM proxy_memory_settings WHERE tenant_id = ? AND principal_id = ?`,
					principal.TenantID, principal.PrincipalID).Exec(ctx); err != nil {
					t.Errorf("fixture cleanup settings: %v", err)
				}
			}
			if _, err := db.NewRaw(`DELETE FROM proxy_memory_banks WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
				alice.TenantID, alice.PrincipalID, projectKey).Exec(ctx); err != nil {
				t.Errorf("fixture cleanup settings bank: %v", err)
			}
		})
		return db, store
	}

	written := proxymemory.StoredOwnerSettings{
		Extraction: proxymemory.StoredPhaseSettings{
			Overrides: proxymemory.TargetOverrides{Backend: "anthropic", Model: "gpt-5"},
			Dependency: proxymemory.DependencyContext{
				Present: true, Backend: "anthropic", Model: "gpt-5",
				InheritedBackend: false, InheritedModel: true,
			},
		},
		Consolidation: proxymemory.StoredPhaseSettings{
			Overrides: proxymemory.TargetOverrides{ReasoningEffort: "high"},
			Dependency: proxymemory.DependencyContext{
				Present: true, Backend: "openai", Model: "gpt-5-mini",
				InheritedBackend: true, InheritedModel: true,
			},
		},
	}

	t.Run("the store keeps the raw overrides and the trusted context", func(t *testing.T) {
		db, st := openFixture(t)
		ctx := context.Background()

		require.NoError(t, st.WriteSettings(ctx, alice, written))
		got, err := st.ReadSettings(ctx, alice)
		require.NoError(t, err)
		assert.Equal(t, written, got)

		// The stored row carries exactly the owner's own values, exactly the
		// owner key and exactly the trusted context. The row has no project
		// column, so the settings are global for the owner and apply to every
		// project of that owner. The positional scan fails when the table
		// carries a different number of columns, so this pins the column set
		// on both engines.
		var rowTenant, rowPrincipal, rowPhase, backend, model, effort string
		var present int
		var contextBackend, contextModel string
		var inheritedBackend, inheritedModel int
		require.NoError(t, db.NewRaw(`
			SELECT * FROM proxy_memory_settings
			WHERE tenant_id = ? AND principal_id = ? AND phase = ?
		`, alice.TenantID, alice.PrincipalID, string(proxymemory.PhaseExtraction)).Scan(ctx,
			&rowTenant, &rowPrincipal, &rowPhase, &backend, &model, &effort,
			&present, &contextBackend, &contextModel, &inheritedBackend, &inheritedModel))
		assert.Equal(t, alice.TenantID, rowTenant)
		assert.Equal(t, alice.PrincipalID, rowPrincipal)
		assert.Equal(t, string(proxymemory.PhaseExtraction), rowPhase)
		assert.Equal(t, "anthropic", backend)
		assert.Equal(t, "gpt-5", model)
		assert.Empty(t, effort)
		assert.Equal(t, 1, present)
		assert.Equal(t, "anthropic", contextBackend)
		assert.Equal(t, "gpt-5", contextModel)
		assert.Equal(t, 0, inheritedBackend)
		assert.Equal(t, 1, inheritedModel)
	})

	t.Run("a legacy row keeps its raw overrides and has no context", func(t *testing.T) {
		db, st := openFixture(t)
		ctx := context.Background()

		// The insert names only the pre-context columns, exactly like a row
		// written before the additive migration.
		_, err := db.NewRaw(`INSERT INTO proxy_memory_settings
			(tenant_id, principal_id, phase, backend, model, reasoning_effort)
			VALUES (?, ?, 'extraction', 'anthropic', 'gpt-5', 'high')`,
			alice.TenantID, alice.PrincipalID).Exec(ctx)
		require.NoError(t, err)

		got, err := st.ReadSettings(ctx, alice)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.OwnerOverrides{
			Extraction: proxymemory.TargetOverrides{Backend: "anthropic", Model: "gpt-5", ReasoningEffort: "high"},
		}, got.Overrides())
		assert.False(t, got.Extraction.Dependency.Present, "a legacy row must not gain a fabricated history")
	})

	t.Run("two principals never see each other's settings", func(t *testing.T) {
		_, st := openFixture(t)
		ctx := context.Background()

		require.NoError(t, st.WriteSettings(ctx, alice, written))
		got, err := st.ReadSettings(ctx, bob)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.StoredOwnerSettings{}, got)

		// The other tenant of the same principal is equally invisible.
		other := proxymemory.Principal{TenantID: tenant + "-other", PrincipalID: alice.PrincipalID}
		got, err = st.ReadSettings(ctx, other)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.StoredOwnerSettings{}, got)
	})

	t.Run("forgetting a bank preserves the settings and their context", func(t *testing.T) {
		db, st := openFixture(t)
		ctx := context.Background()

		require.NoError(t, st.WriteSettings(ctx, alice, written))
		owner := proxymemory.Owner{TenantID: alice.TenantID, PrincipalID: alice.PrincipalID, ProjectKey: projectKey}
		seedBankRow(t, db, owner, bankSeed{ProjectLabel: "/w/settings", Version: 1, MemorySummary: "the summary"})

		require.NoError(t, st.Forget(ctx, owner, 1_000))

		got, err := st.ReadSettings(ctx, alice)
		require.NoError(t, err)
		assert.Equal(t, written, got)
	})

	t.Run("a second instance reads the same row", func(t *testing.T) {
		db, st := openFixture(t)
		ctx := context.Background()

		require.NoError(t, st.WriteSettings(ctx, alice, written))

		second := openSecond(t, db)
		secondStore := proxymemorystore.New(second, proxymemorystore.NewALegReader(second))
		got, err := secondStore.ReadSettings(ctx, alice)
		require.NoError(t, err)
		assert.Equal(t, written, got)
	})

	t.Run("a write replaces both phase rows and their context", func(t *testing.T) {
		_, st := openFixture(t)
		ctx := context.Background()

		require.NoError(t, st.WriteSettings(ctx, alice, proxymemory.StoredOwnerSettings{
			Extraction: proxymemory.StoredPhaseSettings{Overrides: proxymemory.TargetOverrides{Backend: "anthropic"}},
		}))
		require.NoError(t, st.WriteSettings(ctx, alice, proxymemory.StoredOwnerSettings{
			Consolidation: proxymemory.StoredPhaseSettings{
				Overrides:  proxymemory.TargetOverrides{Model: "gpt-5"},
				Dependency: proxymemory.DependencyContext{Present: true, Backend: "openai", Model: "gpt-5", InheritedBackend: true},
			},
		}))

		got, err := st.ReadSettings(ctx, alice)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.StoredOwnerSettings{
			Consolidation: proxymemory.StoredPhaseSettings{
				Overrides:  proxymemory.TargetOverrides{Model: "gpt-5"},
				Dependency: proxymemory.DependencyContext{Present: true, Backend: "openai", Model: "gpt-5", InheritedBackend: true},
			},
		}, got)
	})

	t.Run("a failed phase write rolls back the whole settings write", func(t *testing.T) {
		db, st := openFixture(t)
		ctx := context.Background()
		installSettingsWriteFailure(t, db, carol.TenantID)

		err := st.WriteSettings(ctx, carol, written)
		require.Error(t, err, "the injected consolidation failure must reach the caller")

		got, readErr := st.ReadSettings(ctx, carol)
		require.NoError(t, readErr)
		assert.Equal(t, proxymemory.StoredOwnerSettings{}, got,
			"the extraction row must not survive a failed consolidation row")
	})

	t.Run("the revocation removes the owner's settings", func(t *testing.T) {
		_, st := openFixture(t)
		ctx := context.Background()

		require.NoError(t, st.WriteSettings(ctx, alice, written))
		require.NoError(t, st.DeleteSettings(ctx, alice))

		got, err := st.ReadSettings(ctx, alice)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.StoredOwnerSettings{}, got)

		// A second revocation finds nothing: the owner has no settings left.
		err = st.DeleteSettings(ctx, alice)
		assert.ErrorIs(t, err, proxymemory.ErrNotFound)
	})
}

// installSettingsWriteFailure makes the consolidation row insert fail for the
// given tenant, on the engine the fixture runs. The two-phase write is then
// observed through its rollback instead of through a second settings authority.
// The trigger and the function live for one subtest and are removed with it.
func installSettingsWriteFailure(t *testing.T, db *bun.DB, tenantID string) {
	t.Helper()
	ctx := context.Background()
	// The identifier is test-controlled: a run token and a principal name.
	safe := strings.NewReplacer("-", "_", ".", "_").Replace(tenantID)
	quotedTenant := strings.ReplaceAll(tenantID, "'", "''")
	if db.Dialect().Name() == dialect.PG {
		function := "proxy_memory_fail_settings_" + safe
		_, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF NEW.tenant_id = '%s' AND NEW.phase = 'consolidation' THEN
					RAISE EXCEPTION 'injected settings failure';
				END IF;
				RETURN NEW;
			END;
		$$`, function, quotedTenant))
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, fmt.Sprintf(
			`CREATE TRIGGER %s BEFORE INSERT ON proxy_memory_settings FOR EACH ROW EXECUTE FUNCTION %s()`, function, function))
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanupCtx := context.Background()
			_, _ = db.ExecContext(cleanupCtx, "DROP TRIGGER IF EXISTS "+function+" ON proxy_memory_settings")
			_, _ = db.ExecContext(cleanupCtx, "DROP FUNCTION IF EXISTS "+function+"()")
		})
		return
	}
	trigger := "proxy_memory_fail_settings_" + safe
	_, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON proxy_memory_settings
		WHEN NEW.tenant_id = '%s' AND NEW.phase = 'consolidation'
		BEGIN
			SELECT RAISE(ABORT, 'injected settings failure');
		END`, trigger, quotedTenant))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "DROP TRIGGER IF EXISTS "+trigger)
	})
}

// runLeaseContract runs the claim, completion, publish and consolidation-input
// cases of design §12.3, §12.4 and §12.5 through runContract's per-case opener,
// so the SQLite and PostgreSQL Direct lanes execute identical behavior. Every
// case uses its own fixture scope and run token (per-subtest-unique keys) and
// cleans up through its own opener handle.
func runLeaseContract(
	t *testing.T,
	openFixture func(t *testing.T, f *contractFixture) (*bun.DB, *proxymemorystore.Store),
	runID string,
) {
	t.Helper()

	// The §12.3 claim queries are global by design: a claim selects any due
	// row, not only the caller's. On a shared database (the canonical
	// PostgreSQL Direct lane runs on one persistent dev database) pre-existing
	// rows of earlier runs must therefore never become claim candidates of a
	// case. Each case confines itself to a band derived from its unique run
	// token:
	//   - its rows carry next_attempt_ms = band(0), a negative stamp, and every
	//     claim of the suite runs at a band timestamp (also negative), while
	//     residue of an earlier run — including this suite's own leftovers
	//     after an aborted run — is scheduled at the non-negative default or
	//     at that earlier run's own larger negative stamp, so the §12.3
	//     scheduling gate next_attempt_ms <= NowMS excludes it;
	//   - session activities live in [activity(0), activity(16)] and every
	//     session claim window is taken from that band, so a concurrent run
	//     started later (whose more negative band timestamp would pass the
	//     gate) still cannot see this case's sessions through the window;
	//   - a fixture bank carries the dominant pending-work size bankDirty, so
	//     earlier-run residue never outranks it on the dirtiness ordering.
	runBase, err := strconv.ParseInt(runID, 36, 64)
	require.NoError(t, err)
	activity := func(offset int64) int64 { return runBase + offset }
	band := func(offset int64) int64 { return -runBase + offset }

	t.Run("idle gating refuses sessions outside the idle window", func(t *testing.T) {
		f := newContractFixture("idle", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		newer := f.aLeg("idle_newer")
		older := f.aLeg("idle_older")
		inWindow := f.aLeg("idle_in_window")
		for _, seed := range []struct {
			aLegID   string
			activity int64
		}{
			{newer, activity(3)},
			{older, activity(0)},
			{inWindow, activity(2)},
		} {
			upsertCapture(t, st, f.alice, seed.aLegID, `["idle"]`, seed.activity)
			scheduleSessionClaim(t, db, seed.aLegID, band(0))
		}

		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS:        band(0),
			IdleBeforeMS: activity(2),
			NotBeforeMS:  activity(1),
			LeaseUntilMS: band(3_600_000),
			LeaseOwner:   "idle-worker",
			Limit:        10,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		assert.Equal(t, inWindow, claimed[0].ALegID)
		assert.Equal(t, f.alice, claimed[0].Owner)
		assert.Equal(t, "/work/one", claimed[0].ProjectLabel)
		assert.Equal(t, `["idle"]`, claimed[0].Transcript)
		assert.Equal(t, int64(1), claimed[0].Revision)
		assert.Equal(t, activity(2), claimed[0].LastActivityMS)
		assert.Equal(t, "idle-worker", claimed[0].LeaseOwner)
		assert.Equal(t, "", readSession(t, db, newer).LeaseOwner)
		assert.Equal(t, "", readSession(t, db, older).LeaseOwner)
	})

	t.Run("a session has at most one lease owner until its lease expires", func(t *testing.T) {
		f := newContractFixture("lease", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("lease_owner")
		upsertCapture(t, st, f.alice, aLeg, `["lease"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))

		first, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), IdleBeforeMS: activity(16), NotBeforeMS: 0, LeaseUntilMS: band(4_000), LeaseOwner: "owner-1", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, first, 1)
		assert.Equal(t, aLeg, first[0].ALegID)
		assert.Equal(t, "owner-1", first[0].LeaseOwner)

		rival, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), IdleBeforeMS: activity(16), NotBeforeMS: 0, LeaseUntilMS: band(4_000), LeaseOwner: "owner-2", Limit: 1,
		})
		require.NoError(t, err)
		assert.Empty(t, rival)
		row := readSession(t, db, aLeg)
		assert.Equal(t, "owner-1", row.LeaseOwner)
		assert.Equal(t, int64(1), row.Attempts)

		after, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: band(4_001), IdleBeforeMS: activity(16), NotBeforeMS: 0, LeaseUntilMS: band(8_000), LeaseOwner: "owner-2", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, after, 1)
		assert.Equal(t, "owner-2", after[0].LeaseOwner)
		row = readSession(t, db, aLeg)
		assert.Equal(t, "owner-2", row.LeaseOwner)
		assert.Equal(t, band(8_000), row.LeaseUntilMS)
		assert.Equal(t, int64(2), row.Attempts)
	})

	t.Run("completing a session with its claimed revision stores and deletes it", func(t *testing.T) {
		f := newContractFixture("complete_same_rev", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("complete_same_rev")
		upsertCapture(t, st, f.alice, aLeg, `["turn"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))
		require.Equal(t, int64(1), cs.Revision)

		require.NoError(t, st.CompleteSession(ctx, cs, extractionFor(cs, 7_000)))

		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		stored, ok := readExtraction(t, db, aLeg)
		require.True(t, ok)
		assert.Equal(t, storedExtraction{
			TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID, ProjectKey: f.alice.ProjectKey,
			SessionRef: "session-0123456789ab", RawMemory: "raw memory", RolloutSummary: "rollout summary",
			RolloutSlug: "fix-tests", SourceUpdatedMS: activity(8), GeneratedAtMS: 7_000,
		}, stored)
		bank, ok := readBank(t, db, f.alice)
		require.True(t, ok)
		assert.Equal(t, "/work/one", bank.ProjectLabel)
		assert.Equal(t, int64(0), bank.Version)
		assert.Equal(t, int64(1), bank.DirtySeq)
		assert.Equal(t, int64(0), bank.ConsolidatedSeq)
		assert.Equal(t, "", bank.LeaseOwner)
	})

	t.Run("completing after a newer upsert keeps the session with its lease cleared", func(t *testing.T) {
		f := newContractFixture("complete_newer_rev", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("complete_newer_rev")
		upsertCapture(t, st, f.alice, aLeg, `["one"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))
		require.Equal(t, int64(1), cs.Revision)

		upsertCapture(t, st, f.alice, aLeg, `["two"]`, activity(9))

		// Requirement 4.4: the newer turn stays claimable. The claimed-revision
		// mismatch is a success, not ErrLeaseLost (§12.4 step 5).
		require.NoError(t, st.CompleteSession(ctx, cs, extractionFor(cs, band(-2_000))))

		row := readSession(t, db, aLeg)
		assert.Equal(t, int64(2), row.Revision)
		assert.Equal(t, `["two"]`, row.Transcript)
		assert.Equal(t, "", row.LeaseOwner)
		assert.Equal(t, int64(0), row.LeaseUntilMS)
		assert.Equal(t, int64(0), row.Attempts)
		assert.Equal(t, int64(0), row.NextAttemptMS)
		stored, ok := readExtraction(t, db, aLeg)
		require.True(t, ok)
		assert.Equal(t, band(-2_000), stored.GeneratedAtMS)
		assert.Equal(t, activity(8), stored.SourceUpdatedMS)
		bank, ok := readBank(t, db, f.alice)
		require.True(t, ok)
		assert.Equal(t, int64(1), bank.DirtySeq)

		// Retention may remove that extraction before the newer turn is
		// processed. Its exact source activity, not completion time, must keep
		// this retained continuation eligible (Requirement 4.4).
		_, err := st.Purge(ctx, band(0), band(-1_000), band(-1_000), 1_000)
		require.NoError(t, err)
		assert.Equal(t, 0, extractionCount(t, db, aLeg))
		scheduleSessionClaim(t, db, aLeg, band(0))
		newer := claimOneSession(t, st, "extractor-newer", band(0), band(4_000), activity(16))
		require.Equal(t, aLeg, newer.ALegID)
		require.NoError(t, st.CompleteSession(ctx, newer, extractionFor(newer, band(100))))
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		ledger, ok := readLedgerRow(t, db, aLeg)
		require.True(t, ok)
		assert.Equal(t, activity(9), ledger.SourceActivityMS)
	})

	t.Run("completing with the wrong lease owner loses the lease", func(t *testing.T) {
		f := newContractFixture("wrong_owner", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("wrong_owner")
		upsertCapture(t, st, f.alice, aLeg, `["turn"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))

		stale := cs
		stale.LeaseOwner = "intruder"
		require.ErrorIs(t, st.CompleteSession(ctx, stale, extractionFor(cs, 7_000)), proxymemory.ErrLeaseLost)

		row := readSession(t, db, aLeg)
		assert.Equal(t, "extractor", row.LeaseOwner)
		assert.Equal(t, `["turn"]`, row.Transcript)
		assert.Equal(t, int64(1), row.Revision)
		assert.Equal(t, 0, extractionCount(t, db, aLeg))
		_, bankOK := readBank(t, db, f.alice)
		assert.False(t, bankOK)
	})

	t.Run("completing a fenced A-leg deletes the session and loses the lease", func(t *testing.T) {
		f := newContractFixture("fenced_complete", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("fenced_complete")
		upsertCapture(t, st, f.alice, aLeg, `["turn"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))

		_, err := db.NewRaw(
			`INSERT INTO proxy_memory_fence (a_leg_id, tenant_id, principal_id, forgotten_at_ms) VALUES (?, ?, ?, ?)`,
			aLeg, f.alice.TenantID, f.alice.PrincipalID, 6_000,
		).Exec(ctx)
		require.NoError(t, err)

		require.ErrorIs(t, st.CompleteSession(ctx, cs, extractionFor(cs, 7_000)), proxymemory.ErrLeaseLost)

		// The fenced row is removed although the call reports ErrLeaseLost, and
		// nothing is extracted (Requirements 4.5, 8.6).
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		assert.Equal(t, 0, extractionCount(t, db, aLeg))
		_, bankOK := readBank(t, db, f.alice)
		assert.False(t, bankOK)
	})

	t.Run("completing against a foreign extraction owner loses the lease", func(t *testing.T) {
		f := newContractFixture("foreign_extract_owner", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("foreign_extract_owner")
		upsertCapture(t, st, f.alice, aLeg, `["turn"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))
		seedExtraction(t, db, f.owner("bob", f.projectP), aLeg)

		require.ErrorIs(t, st.CompleteSession(ctx, cs, extractionFor(cs, 7_000)), proxymemory.ErrLeaseLost)

		stored, ok := readExtraction(t, db, aLeg)
		require.True(t, ok)
		assert.Equal(t, storedExtraction{
			TenantID: f.alice.TenantID, PrincipalID: "bob", ProjectKey: f.projectP,
			SessionRef: "session-0123456789ab", RawMemory: "raw memory", RolloutSummary: "rollout summary",
			RolloutSlug: "fix-tests", SourceUpdatedMS: 1_000, GeneratedAtMS: 2_000,
		}, stored)
		row := readSession(t, db, aLeg)
		assert.Equal(t, "extractor", row.LeaseOwner)
		_, bankOK := readBank(t, db, f.alice)
		assert.False(t, bankOK)
	})

	t.Run("a lost claim race is skipped and the skip-list query compiles", func(t *testing.T) {
		f := newContractFixture("skip_list", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		lost := f.aLeg("skip_lost")
		won := f.aLeg("skip_won")
		upsertCapture(t, st, f.alice, lost, `["lost"]`, activity(1))
		upsertCapture(t, st, f.alice, won, `["won"]`, activity(2))
		scheduleSessionClaim(t, db, lost, band(0))
		scheduleSessionClaim(t, db, won, band(0))

		h := &claimRaceHook{stealALegID: lost, intruder: "other-instance", leaseUntilMS: band(60_000)}
		db.AddQueryHook(h)

		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), IdleBeforeMS: activity(16), NotBeforeMS: 0, LeaseUntilMS: band(4_000), LeaseOwner: "claimer", Limit: 2,
		})
		require.NoError(t, h.racedErr)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		assert.Equal(t, won, claimed[0].ALegID)
		assert.Equal(t, "claimer", claimed[0].LeaseOwner)
		// The raced candidate keeps the foreign lease: the claim never took it.
		assert.Equal(t, "other-instance", readSession(t, db, lost).LeaseOwner)
		require.Len(t, h.candidates, 2)
		// An empty skip list must not emit NOT IN (which would render as the
		// invalid IN () on PostgreSQL); the raced A-leg is skipped through a real
		// NOT IN query that this engine compiled and ran.
		assert.NotContains(t, h.candidates[0], "NOT IN")
		assert.Contains(t, h.candidates[1], "NOT IN")
	})

	t.Run("failing a session clears its lease and schedules the retry", func(t *testing.T) {
		f := newContractFixture("fail_session", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("fail_session")
		upsertCapture(t, st, f.alice, aLeg, `["turn"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))

		require.NoError(t, st.FailSession(ctx, cs, band(2_000)))

		row := readSession(t, db, aLeg)
		assert.Equal(t, "", row.LeaseOwner)
		assert.Equal(t, int64(0), row.LeaseUntilMS)
		assert.Equal(t, band(2_000), row.NextAttemptMS)
		assert.Equal(t, int64(1), row.Attempts)
		assert.Equal(t, int64(1), row.Revision)
		assert.Equal(t, `["turn"]`, row.Transcript)

		before, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: band(1_999), IdleBeforeMS: activity(16), NotBeforeMS: 0, LeaseUntilMS: band(6_000), LeaseOwner: "retry", Limit: 1,
		})
		require.NoError(t, err)
		assert.Empty(t, before)

		atRetry, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: band(2_000), IdleBeforeMS: activity(16), NotBeforeMS: 0, LeaseUntilMS: band(6_000), LeaseOwner: "retry", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, atRetry, 1)
		assert.Equal(t, "retry", atRetry[0].LeaseOwner)
		assert.Equal(t, int64(2), readSession(t, db, aLeg).Attempts)
	})

	t.Run("failing a superseded session preserves the newer revision's retry gate", func(t *testing.T) {
		f := newContractFixture("fail_session_revision", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("fail_session_revision")
		upsertCapture(t, st, f.alice, aLeg, `["turn"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))
		require.Equal(t, int64(1), cs.Revision)

		// A newer turn arrives while the extraction runs: the stored revision
		// moves on and the newer transcript carries its own activity.
		upsertCapture(t, st, f.alice, aLeg, `["turn","newer"]`, activity(12))
		require.Equal(t, int64(2), readSession(t, db, aLeg).Revision)
		// The newer revision is due inside the fixture's claim band.
		scheduleSessionClaim(t, db, aLeg, band(0))

		// The superseded extraction fails. Its backoff belongs to the revision
		// it claimed, so it must not gate the newer revision.
		require.NoError(t, st.FailSession(ctx, cs, band(12_000)))

		row := readSession(t, db, aLeg)
		assert.Equal(t, "", row.LeaseOwner)
		assert.Equal(t, int64(0), row.LeaseUntilMS)
		assert.Equal(t, band(0), row.NextAttemptMS,
			"a newer revision keeps its own retry gate")
		assert.Equal(t, `["turn","newer"]`, row.Transcript)

		// The newer revision is idle-eligible on its own activity, so it is
		// claimable long before the superseded backoff would have expired.
		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: band(4_000), IdleBeforeMS: activity(16), NotBeforeMS: 0, LeaseUntilMS: band(8_000), LeaseOwner: "next", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1, "the newer revision must be claimable on its own idle window")
		assert.Equal(t, aLeg, claimed[0].ALegID)
		assert.Equal(t, int64(2), claimed[0].Revision)

		// A failure from another lease owner changes nothing.
		foreign := cs
		foreign.LeaseOwner = "other-instance"
		require.NoError(t, st.FailSession(ctx, foreign, band(20_000)))
		assert.Equal(t, "next", readSession(t, db, aLeg).LeaseOwner)
		assert.Equal(t, band(0), readSession(t, db, aLeg).NextAttemptMS)
	})

	t.Run("claiming a bank requires dirty work and publishing consumes notes exactly", func(t *testing.T) {
		f := newContractFixture("bank_claim", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		foreign := f.owner("bob", f.projectP)
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 4, MemoryMD: "old md", MemorySummary: "old summary",
			PublishedAtMS: 1_000, ConsolidatedNames: `["old"]`, DirtySeq: bankDirty, ConsolidatedSeq: 0,
			NextAttemptMS: band(0),
		})
		// A clean bank (dirty_seq == consolidated_seq) is never claimed.
		seedBankRow(t, db, f.owner(f.alice.PrincipalID, f.projectQ), bankSeed{
			ProjectLabel: "/work/two", DirtySeq: bankDirty, ConsolidatedSeq: bankDirty, NextAttemptMS: band(0),
		})
		keepNote := "note_keep_" + f.token
		eatOne := "note_eat_one_" + f.token
		eatTwo := "note_eat_two_" + f.token
		foreignNote := "note_foreign_" + f.token
		seedNote(t, db, f.alice, keepNote, "keep", 2_000)
		seedNote(t, db, f.alice, eatOne, "eat one", 1_000)
		seedNote(t, db, f.alice, eatTwo, "eat two", 3_000)
		seedNote(t, db, foreign, foreignNote, "foreign", 1_000)
		cleanupFixtureOwner(t, db, foreign)

		claimed, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-1", Limit: 5,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		bank := claimed[0]
		assert.Equal(t, f.alice, bank.Owner)
		assert.Equal(t, "/work/one", bank.ProjectLabel)
		assert.Equal(t, int64(4), bank.Version)
		assert.Equal(t, bankDirty, bank.DirtySeq)
		assert.Equal(t, "old md", bank.MemoryMD)
		assert.Equal(t, "old summary", bank.MemorySummary)
		assert.Equal(t, []string{"old"}, bank.ConsolidatedNames)
		assert.Equal(t, "bank-1", bank.LeaseOwner)

		rival, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-2", Limit: 5,
		})
		require.NoError(t, err)
		assert.Empty(t, rival)
		assert.Equal(t, int64(1), mustBank(t, db, f.alice).Attempts)

		require.NoError(t, st.PublishBank(ctx, bank, proxymemory.Publication{
			MemoryMD:          "new md",
			MemorySummary:     "new summary",
			ConsolidatedNames: []string{"alpha", "beta"},
			// The foreign note ID is consumed in the same call: the owner
			// predicate must keep it.
			ConsumedNoteIDs: []string{eatOne, eatTwo, foreignNote},
			PublishedAtMS:   8_000,
		}))

		published := mustBank(t, db, f.alice)
		assert.Equal(t, int64(5), published.Version)
		assert.Equal(t, "new md", published.MemoryMD)
		assert.Equal(t, "new summary", published.MemorySummary)
		assert.Equal(t, int64(8_000), published.PublishedAtMS)
		assert.Equal(t, `["alpha","beta"]`, published.ConsolidatedNames)
		assert.Equal(t, bankDirty, published.ConsolidatedSeq)
		// The omitted note keeps the bank claimable and carries note-only
		// progress (Requirement 5.9): a later consolidation must be able to
		// consume it, so the publish bumps dirty_seq past this round's
		// consolidated_seq instead of stranding it (the corrected invariant).
		assert.Equal(t, bankDirty+1, published.DirtySeq)
		assert.Equal(t, "", published.LeaseOwner)
		assert.Equal(t, int64(0), published.LeaseUntilMS)
		assert.Equal(t, int64(0), published.Attempts)
		assert.Equal(t, int64(0), published.NextAttemptMS)
		assert.Equal(t, []string{keepNote}, noteIDs(t, db, f.alice))
		assert.Equal(t, []string{foreignNote}, noteIDs(t, db, foreign))

		// The bank is dirty again for exactly its residual note: the next
		// consolidation claims it and consumes that note.
		scheduleBankClaim(t, db, f.alice, band(0))
		second, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-3", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, second, 1)
		require.Equal(t, f.alice, second[0].Owner)
		assert.Equal(t, int64(5), second[0].Version)
		assert.Equal(t, bankDirty+1, second[0].DirtySeq)
		residualBatch, err := st.ConsolidationInput(ctx, second[0], 0, 10)
		require.NoError(t, err)
		require.Equal(t, []string{keepNote}, noteIDsOf(residualBatch.Notes))

		require.NoError(t, st.PublishBank(ctx, second[0], proxymemory.Publication{
			MemoryMD: "final md", MemorySummary: "final summary",
			ConsolidatedNames: []string{"gamma"},
			ConsumedNoteIDs:   []string{keepNote},
			PublishedAtMS:     9_000,
		}))
		settled := mustBank(t, db, f.alice)
		assert.Equal(t, int64(6), settled.Version)
		assert.Equal(t, bankDirty+1, settled.DirtySeq)
		assert.Equal(t, bankDirty+1, settled.ConsolidatedSeq)
		assert.Empty(t, noteIDs(t, db, f.alice), "the second round consumes the residual note")

		// The published bank is clean again: nothing left to claim.
		scheduleBankClaim(t, db, f.alice, band(0))
		done, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-4", Limit: 5,
		})
		require.NoError(t, err)
		assert.Empty(t, done)
	})

	t.Run("residual notes keep the bank claimable until every note is consumed", func(t *testing.T) {
		f := newContractFixture("note_progress", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 0, DirtySeq: bankDirty, ConsolidatedSeq: 0,
			NextAttemptMS: band(0),
		})
		// The default note (8 KiB) and phase2_note_bytes (64 KiB) budgets: ten
		// notes exceed the per-consolidation note budget, so the worker needs
		// several cycles and each cycle must consume at least one note.
		noteBytes := 8 << 10
		noteBudget := 64 << 10
		const noteCount = 10
		for i := range noteCount {
			seedNote(t, db, f.alice, fmt.Sprintf("note_progress_%02d_%s", i, f.token),
				strings.Repeat("n", noteBytes), int64(1_000+i))
		}

		cycles := 0
		consumed := 0
		for {
			// Every round re-enters the claim exactly as the worker would on
			// its next tick; the publish left the bank immediately claimable.
			scheduleBankClaim(t, db, f.alice, band(0))
			claimed, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
				NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-progress", Limit: 1,
			})
			require.NoError(t, err)
			if len(claimed) == 0 {
				break
			}
			require.Equal(t, f.alice, claimed[0].Owner,
				"the shared database must not present another run's bank (see the isolation comment)")
			bank := claimed[0]
			batch, err := st.ConsolidationInput(ctx, bank, 0, 10)
			require.NoError(t, err)
			require.Empty(t, batch.Extractions)
			require.NotEmpty(t, batch.Notes, "an empty consolidation window would end the loop")
			sent := fittingPhase2Notes(batch.Notes, noteBudget)
			require.NotEmpty(t, sent, "each cycle must consume at least one note (monotone progress)")
			ids := make([]string, 0, len(sent))
			for _, note := range sent {
				ids = append(ids, note.NoteID)
			}
			require.NoError(t, st.PublishBank(ctx, bank, proxymemory.Publication{
				MemoryMD: "progress md", MemorySummary: "progress summary",
				ConsumedNoteIDs: ids, PublishedAtMS: 9_000,
			}))
			cycles++
			consumed += len(sent)
			remaining := noteIDs(t, db, f.alice)
			require.Len(t, remaining, noteCount-consumed)
		}

		// Ten 8 KiB notes against the 64 KiB budget converge in exactly two
		// rounds (8 + 2); the loop's empty final claim proves the bank settled.
		assert.Equal(t, 2, cycles)
		assert.Equal(t, noteCount, consumed)
		assert.Empty(t, noteIDs(t, db, f.alice))
		settled := mustBank(t, db, f.alice)
		assert.Equal(t, int64(2), settled.Version)
		assert.Equal(t, bankDirty+1, settled.DirtySeq)
		assert.Equal(t, bankDirty+1, settled.ConsolidatedSeq)
	})

	t.Run("a note round that fits in one publication settles the bank in one pass", func(t *testing.T) {
		f := newContractFixture("note_single_pass", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 0, DirtySeq: 1, ConsolidatedSeq: 0, NextAttemptMS: band(0),
		})
		// Three 8 KiB notes are 24 KiB, well inside the 64 KiB note budget.
		noteBudget := 64 << 10
		ids := make([]string, 0, 3)
		for i := range 3 {
			id := fmt.Sprintf("note_single_pass_%02d_%s", i, f.token)
			seedNote(t, db, f.alice, id, strings.Repeat("n", 8<<10), int64(1_000+i))
			ids = append(ids, id)
		}

		claimed, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-single", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		batch, err := st.ConsolidationInput(ctx, claimed[0], 0, 10)
		require.NoError(t, err)
		require.Len(t, batch.Notes, 3)
		sent := fittingPhase2Notes(batch.Notes, noteBudget)
		require.Equal(t, ids, noteIDsOf(sent), "the whole 24 KiB note set fits one publication")

		require.NoError(t, st.PublishBank(ctx, claimed[0], proxymemory.Publication{
			MemoryMD: "single md", MemorySummary: "single summary",
			ConsumedNoteIDs: ids, PublishedAtMS: 9_000,
		}))
		settled := mustBank(t, db, f.alice)
		assert.Equal(t, int64(1), settled.Version)
		assert.Equal(t, int64(1), settled.DirtySeq, "no residual note: the bank is clean")
		assert.Equal(t, int64(1), settled.ConsolidatedSeq)
		assert.Empty(t, noteIDs(t, db, f.alice))

		scheduleBankClaim(t, db, f.alice, band(0))
		done, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-single-2", Limit: 5,
		})
		require.NoError(t, err)
		assert.Empty(t, done)
	})

	t.Run("publishing with a stale version loses the lease", func(t *testing.T) {
		f := newContractFixture("publish_stale_version", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		note := "note_stale_" + f.token
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 2, MemoryMD: "current md", MemorySummary: "current summary",
			PublishedAtMS: 1_000, DirtySeq: bankDirty, ConsolidatedSeq: 0, NextAttemptMS: band(0),
		})
		seedNote(t, db, f.alice, note, "consumed or not", 1_000)

		claimed, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-1", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		require.Equal(t, int64(2), claimed[0].Version)
		require.Equal(t, bankDirty, claimed[0].DirtySeq)
		before := mustBank(t, db, f.alice)

		stale := claimed[0]
		stale.Version = 1
		require.ErrorIs(t, st.PublishBank(ctx, stale, publishEverything("stale", note)), proxymemory.ErrLeaseLost)

		assert.Equal(t, before, mustBank(t, db, f.alice))
		assert.Equal(t, []string{note}, noteIDs(t, db, f.alice), "a failed publish deletes no notes")
	})

	t.Run("publishing with the wrong lease owner loses the lease", func(t *testing.T) {
		f := newContractFixture("publish_wrong_owner", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		note := "note_wrong_owner_" + f.token
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 2, MemoryMD: "current md", MemorySummary: "current summary",
			PublishedAtMS: 1_000, DirtySeq: bankDirty, ConsolidatedSeq: 0, NextAttemptMS: band(0),
		})
		seedNote(t, db, f.alice, note, "consumed or not", 1_000)

		claimed, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-1", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		before := mustBank(t, db, f.alice)

		foreign := claimed[0]
		foreign.LeaseOwner = "intruder"
		require.ErrorIs(t, st.PublishBank(ctx, foreign, publishEverything("foreign", note)), proxymemory.ErrLeaseLost)

		assert.Equal(t, before, mustBank(t, db, f.alice))
		assert.Equal(t, []string{note}, noteIDs(t, db, f.alice))
	})

	t.Run("failing a bank clears its lease and schedules the retry", func(t *testing.T) {
		f := newContractFixture("fail_bank", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty, ConsolidatedSeq: 0, NextAttemptMS: band(0),
		})

		claimed, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-1", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)

		require.NoError(t, st.FailBank(ctx, claimed[0], band(2_000)))

		row := mustBank(t, db, f.alice)
		assert.Equal(t, "", row.LeaseOwner)
		assert.Equal(t, int64(0), row.LeaseUntilMS)
		assert.Equal(t, band(2_000), row.NextAttemptMS)
		assert.Equal(t, int64(1), row.Attempts)
		assert.Equal(t, int64(1), row.Version)
		assert.Equal(t, bankDirty, row.DirtySeq)

		before, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(1_999), LeaseUntilMS: band(6_000), LeaseOwner: "bank-2", Limit: 1,
		})
		require.NoError(t, err)
		assert.Empty(t, before)

		atRetry, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(2_000), LeaseUntilMS: band(6_000), LeaseOwner: "bank-2", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, atRetry, 1)
		assert.Equal(t, "bank-2", atRetry[0].LeaseOwner)
		assert.Equal(t, int64(2), mustBank(t, db, f.alice).Attempts)
	})

	t.Run("a stale bank claim does not take a bank whose retry gate was scheduled in the gap", func(t *testing.T) {
		f := newContractFixture("bank_claim_gap", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		// The bank is dirty and due, and the previous generation still owns an
		// expired lease on it, so the candidate query sees it as claimable.
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty, ConsolidatedSeq: 0,
			LeaseOwner: "owner-b", LeaseUntilMS: band(-1), NextAttemptMS: band(0),
		})

		barrier := &bankClaimGapBarrier{
			marker:  raceMarkerBankClaim,
			reached: make(chan struct{}),
			release: make(chan struct{}),
		}
		db.AddQueryHook(barrier)
		release := sync.OnceFunc(func() { close(barrier.release) })

		claimCtx, cancelClaim := context.WithTimeout(ctx, bankClaimGapDeadline)
		defer cancelClaim()
		type claimOutcome struct {
			claimed []proxymemory.ClaimedBank
			err     error
		}
		outcomes := make(chan claimOutcome, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			claimed, err := st.ClaimBanks(
				context.WithValue(claimCtx, raceMarkerKey{}, raceMarkerBankClaim),
				proxymemory.ClaimQuery{
					NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "owner-a", Limit: 1,
				},
			)
			outcomes <- claimOutcome{claimed: claimed, err: err}
		}()
		t.Cleanup(func() {
			release()
			select {
			case <-finished:
			case <-time.After(bankClaimGapDeadline):
				t.Error("the parked bank claim did not finish")
			}
		})

		// The candidate has been selected and BEGIN is parked before the claim
		// transaction owns a connection, so the competing failure commits in
		// the actual candidate/UPDATE gap on SQLite and PostgreSQL alike.
		select {
		case <-barrier.reached:
		case <-time.After(bankClaimGapDeadline):
			t.Fatal("the bank claim never reached BEGIN after candidate selection")
		}
		require.True(t, barrier.selected, "the candidate SELECT must have executed before the BEGIN barrier")

		// The previous generation's late failure releases the lease and
		// schedules the next attempt in the future.
		require.NoError(t, st.FailBank(ctx, proxymemory.ClaimedBank{
			Owner: f.alice, LeaseOwner: "owner-b",
		}, band(2_000)))

		release()

		outcome := <-outcomes
		require.NoError(t, outcome.err)
		assert.Empty(t, outcome.claimed,
			"a stale claim must not take a bank whose retry gate was scheduled in the gap")

		// The refused claim leaves the settled bank exactly as the failure
		// wrote it: no lease, the scheduled retry time and no counted attempt.
		row := mustBank(t, db, f.alice)
		assert.Equal(t, "", row.LeaseOwner)
		assert.Equal(t, int64(0), row.LeaseUntilMS)
		assert.Equal(t, band(2_000), row.NextAttemptMS, "the scheduled retry gate must survive the stale claim")
		assert.Equal(t, int64(0), row.Attempts, "a refused claim must not count an attempt")

		// The bank is not stranded by the refusal: it is claimable again once
		// its own retry time has passed.
		due, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(2_000), LeaseUntilMS: band(6_000), LeaseOwner: "owner-c", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, due, 1, "the bank must be claimable again once its retry time has passed")
		assert.Equal(t, "owner-c", due[0].LeaseOwner)
		assert.Equal(t, int64(1), mustBank(t, db, f.alice).Attempts)
	})

	t.Run("bank candidate exclusions match the complete owner and keep the ordering", func(t *testing.T) {
		f := newContractFixture("bank_exclusions", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		// Four banks of three owners. O2 and O3 deliberately SHARE the fixture's
		// project key: the exclusion must key on the complete owner triple, never
		// on a project key alone. Outstanding work is strictly decreasing, so the
		// candidate order is fully determined. Each delta exceeds bankDirty so
		// residue of an earlier run can never outrank this case's own banks.
		o2 := f.owner("bob", f.projectP)
		o3 := proxymemory.Owner{TenantID: "tenant-b", PrincipalID: f.alice.PrincipalID, ProjectKey: f.projectP}
		o4 := f.owner(f.alice.PrincipalID, f.projectQ)
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty + 4, ConsolidatedSeq: 0,
			NextAttemptMS: band(0),
		})
		seedBankRow(t, db, o2, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty + 3, ConsolidatedSeq: 0,
			NextAttemptMS: band(0),
		})
		seedBankRow(t, db, o3, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty + 2, ConsolidatedSeq: 0,
			NextAttemptMS: band(0),
		})
		seedBankRow(t, db, o4, bankSeed{
			ProjectLabel: "/work/two", Version: 1, DirtySeq: bankDirty + 1, ConsolidatedSeq: 0,
			NextAttemptMS: band(0),
		})
		cleanupFixtureOwner(t, db, o2)
		cleanupFixtureOwner(t, db, o3)
		// Claiming a bank creates its owner erasure epoch row; remove the rows
		// this case's foreign owners create so the shared database keeps no
		// residue from them.
		t.Cleanup(func() {
			for _, owner := range []proxymemory.Owner{o2, o3} {
				if _, err := db.NewRaw(`DELETE FROM proxy_memory_erasure_epochs
					WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
					owner.TenantID, owner.PrincipalID, owner.ProjectKey).Exec(context.Background()); err != nil {
					t.Errorf("fixture cleanup erasure epoch of %+v: %v", owner, err)
				}
			}
		})
		claim := func(exclude ...proxymemory.Owner) []proxymemory.ClaimedBank {
			t.Helper()
			claimed, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
				NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-exclusions", Limit: 1,
				ExcludeBankOwners: exclude,
			})
			require.NoError(t, err)
			return claimed
		}

		// An empty exclusion list is exactly the previous candidate query: the
		// dirtiest bank wins.
		first := claim()
		require.Len(t, first, 1)
		assert.Equal(t, f.alice, first[0].Owner)

		// Excluding the winner returns the owner that shares its project key but
		// has a different principal; lower-ranked exclusions change nothing else.
		second := claim(f.alice, o3, o4)
		require.Len(t, second, 1)
		assert.Equal(t, o2, second[0].Owner,
			"a same-key owner with a different principal must stay claimable")

		// A same-key owner under another tenant was excluded, and the owner's
		// OTHER project is never suppressed by a project-key-only match.
		third := claim(f.alice, o2, o3)
		require.Len(t, third, 1, "the owner's second project must stay claimable")
		assert.Equal(t, o4, third[0].Owner)

		// Excluding a same-key project of another tenant must not suppress it.
		fourth := claim(f.alice, o2, o4)
		require.Len(t, fourth, 1)
		assert.Equal(t, o3, fourth[0].Owner,
			"a same-key project of another tenant must not be excluded by someone else's triple")

		// Excluding every owner of this case leaves none of them candidate.
		all := claim(f.alice, o2, o3, o4)
		for _, bank := range all {
			assert.NotContains(t, []proxymemory.Owner{f.alice, o2, o3, o4}, bank.Owner,
				"an excluded owner must never be returned (residue of other runs may still be claimable)")
		}
	})

	t.Run("bank exclusions compose with the lost-candidate path", func(t *testing.T) {
		f := newContractFixture("bank_exclusion_offset", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		// The dirtiest bank carries an expired lease of a previous generation and
		// LOSES its candidacy inside the claim: the competing failure commits in
		// the SELECT/UPDATE gap. The exclusion-aware query must behave exactly
		// like the previous one on that path — no excluded owner comes back and
		// the stale candidate's own retry gate survives.
		excluded := proxymemory.Owner{TenantID: "tenant-none", PrincipalID: "none", ProjectKey: f.projectP}
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty + 3, ConsolidatedSeq: 0,
			LeaseOwner: "owner-b", LeaseUntilMS: band(-1), NextAttemptMS: band(0),
		})

		barrier := &bankClaimGapBarrier{
			marker:  raceMarkerBankClaim,
			reached: make(chan struct{}),
			release: make(chan struct{}),
		}
		db.AddQueryHook(barrier)
		release := sync.OnceFunc(func() { close(barrier.release) })

		claimCtx, cancelClaim := context.WithTimeout(ctx, bankClaimGapDeadline)
		defer cancelClaim()
		type claimOutcome struct {
			claimed []proxymemory.ClaimedBank
			err     error
		}
		outcomes := make(chan claimOutcome, 1)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			claimed, err := st.ClaimBanks(
				context.WithValue(claimCtx, raceMarkerKey{}, raceMarkerBankClaim),
				proxymemory.ClaimQuery{
					NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "owner-a", Limit: 1,
					ExcludeBankOwners: []proxymemory.Owner{excluded},
				},
			)
			outcomes <- claimOutcome{claimed: claimed, err: err}
		}()
		t.Cleanup(func() {
			release()
			select {
			case <-finished:
			case <-time.After(bankClaimGapDeadline):
				t.Error("the parked bank claim did not finish")
			}
		})

		select {
		case <-barrier.reached:
		case <-time.After(bankClaimGapDeadline):
			t.Fatal("the bank claim never reached BEGIN after candidate selection")
		}
		require.True(t, barrier.selected, "the candidate SELECT must have executed before the BEGIN barrier")

		// The previous generation's late failure releases the lease and schedules
		// the next attempt in the future, so the selected candidate loses its
		// candidacy before the claim transaction revalidates it.
		require.NoError(t, st.FailBank(ctx, proxymemory.ClaimedBank{
			Owner: f.alice, LeaseOwner: "owner-b",
		}, band(2_000)))

		release()

		outcome := <-outcomes
		require.NoError(t, outcome.err)
		assert.Empty(t, outcome.claimed,
			"a stale claim must not take a bank whose retry gate was scheduled in the gap")
		for _, bank := range outcome.claimed {
			assert.NotEqual(t, excluded, bank.Owner, "an excluded owner must never be claimed")
		}
		row := mustBank(t, db, f.alice)
		assert.Equal(t, band(2_000), row.NextAttemptMS, "the scheduled retry gate must survive the stale claim")
		assert.Equal(t, int64(0), row.Attempts, "a refused claim must not count an attempt")

		// The stale bank is claimable again once its own retry time has passed,
		// and it is still claimable while the unrelated exclusion is present.
		due, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(2_000), LeaseUntilMS: band(6_000), LeaseOwner: "owner-c", Limit: 1,
			ExcludeBankOwners: []proxymemory.Owner{excluded},
		})
		require.NoError(t, err)
		require.Len(t, due, 1)
		assert.Equal(t, f.alice, due[0].Owner)
		assert.Equal(t, "owner-c", due[0].LeaseOwner)
	})

	t.Run("bank candidate exclusions stay within the SQLite bind budget", func(t *testing.T) {
		f := newContractFixture("bank_exclusions_budget", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty, ConsolidatedSeq: 0, NextAttemptMS: band(0),
		})
		// The worker caps one tick at 256 distinct attempts, so no candidate
		// query carries more than 255 excluded owners: 765 owner parameters plus
		// the two scheduling times and the OFFSET, 768 in total, under the
		// conservative SQLite limit of 999. This case executes exactly that
		// bound on both engines.
		exclusions := make([]proxymemory.Owner, 0, 255)
		exclusions = append(exclusions, f.alice)
		for i := 1; i < 255; i++ {
			exclusions = append(exclusions, proxymemory.Owner{
				TenantID:    "tenant-budget",
				PrincipalID: "budget-" + strconv.Itoa(i),
				ProjectKey:  f.projectP,
			})
		}
		require.Len(t, exclusions, 255)
		claimed, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-budget", Limit: 1,
			ExcludeBankOwners: exclusions,
		})
		require.NoError(t, err, "a full tick's exclusion list must stay inside the bind-parameter budget")
		for _, bank := range claimed {
			assert.NotContains(t, exclusions, bank.Owner,
				"an owner excluded by a full-length list must never be claimed (residue of other runs may still be claimable)")
		}
	})

	t.Run("consolidation input is owner-scoped, ascending and limited", func(t *testing.T) {
		f := newContractFixture("consolidation_input", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		foreign := f.owner("bob", f.projectP)
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty, ConsolidatedSeq: 0, NextAttemptMS: band(0),
		})
		claimed, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), LeaseUntilMS: band(4_000), LeaseOwner: "bank-1", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)

		alpha := f.aLeg("consolidation_alpha")
		beta := f.aLeg("consolidation_beta")
		gamma := f.aLeg("consolidation_gamma")
		expired := f.aLeg("consolidation_expired")
		foreignLeg := f.aLeg("consolidation_foreign")
		seedExtractionRow(t, db, f.alice, gamma, "gamma", 3_000)
		seedExtractionRow(t, db, f.alice, expired, "expired", 500)
		seedExtractionRow(t, db, f.alice, alpha, "alpha", 1_000)
		seedExtractionRow(t, db, f.alice, beta, "beta", 2_000)
		seedExtractionRow(t, db, foreign, foreignLeg, "foreign", 9_000)
		keepNote := "note_keep_" + f.token
		oldNote := "note_old_" + f.token
		foreignNote := "note_foreign_" + f.token
		seedNote(t, db, f.alice, keepNote, "keep this note", 2_000)
		seedNote(t, db, f.alice, oldNote, "oldest note", 1_000)
		seedNote(t, db, foreign, foreignNote, "foreign note", 1_000)
		cleanupFixtureOwner(t, db, foreign)

		batch, err := st.ConsolidationInput(ctx, claimed[0], 1_000, 2)
		require.NoError(t, err)
		require.Len(t, batch.Extractions, 2)
		// The oldest two outstanding extractions within sinceMS, ascending.
		assert.Equal(t, []string{"alpha", "beta"}, rolloutSlugs(batch.Extractions))
		assert.Equal(t, int64(1_000), batch.Extractions[0].GeneratedAtMS)
		assert.Equal(t, int64(2_000), batch.Extractions[1].GeneratedAtMS)
		for _, e := range batch.Extractions {
			assert.Equal(t, f.alice, e.Owner)
		}
		// The window still reports every live name, including the extraction
		// the limit left out.
		assert.Equal(t, []string{
			proxymemory.SummaryName(proxymemory.Extraction{SessionRef: "session-0123456789ab", GeneratedAtMS: 1_000, RolloutSlug: "alpha"}),
			proxymemory.SummaryName(proxymemory.Extraction{SessionRef: "session-0123456789ab", GeneratedAtMS: 2_000, RolloutSlug: "beta"}),
			proxymemory.SummaryName(proxymemory.Extraction{SessionRef: "session-0123456789ab", GeneratedAtMS: 3_000, RolloutSlug: "gamma"}),
		}, batch.WindowNames)
		require.Len(t, batch.Notes, 2)
		assert.Equal(t, []string{oldNote, keepNote}, noteIDsOf(batch.Notes))
		assert.Equal(t, "oldest note", batch.Notes[0].Text)
		assert.Equal(t, "keep this note", batch.Notes[1].Text)

		// A wider limit returns every outstanding extraction of the owner in
		// ascending order: sinceMS excluded the expired row and the owner
		// predicate the foreign one.
		all, err := st.ConsolidationInput(ctx, claimed[0], 1_000, 10)
		require.NoError(t, err)
		assert.Equal(t, []string{"alpha", "beta", "gamma"}, rolloutSlugs(all.Extractions))
	})

	t.Run("limited consolidation input keeps excluded extractions obligated", func(t *testing.T) {
		// Requirement 1: an extraction excluded by the per-attempt cap stays
		// outstanding, and the bank stays claimable until every in-window
		// extraction was acknowledged. Oldest first drains a static backlog.
		f := newContractFixture("residual_extraction_drain", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty, ConsolidatedSeq: 0, NextAttemptMS: band(0),
		})
		oldLeg := f.aLeg("residual_old")
		newLeg := f.aLeg("residual_new")
		seedExtractionRow(t, db, f.alice, oldLeg, "old", 1_000)
		seedExtractionRow(t, db, f.alice, newLeg, "new", 2_000)

		first := claimOneBank(t, st, "bank-1", band(0), band(4_000))
		batch, err := st.ConsolidationInput(ctx, first, 1_000, 1)
		require.NoError(t, err)
		require.Len(t, batch.Extractions, 1)
		assert.Equal(t, "old", batch.Extractions[0].RolloutSlug,
			"the oldest outstanding extraction is scheduled first")
		assert.Equal(t, []string{
			proxymemory.SummaryName(batch.Extractions[0]),
			proxymemory.SummaryName(proxymemory.Extraction{SessionRef: "session-0123456789ab", GeneratedAtMS: 2_000, RolloutSlug: "new"}),
		}, batch.WindowNames, "the window reports every live summary name")

		require.NoError(t, st.PublishBank(ctx, first, proxymemory.Publication{
			MemoryMD:          "md",
			MemorySummary:     "summary",
			ConsolidatedNames: []string{proxymemory.SummaryName(batch.Extractions[0])},
			Acknowledged:      []proxymemory.ExtractionRef{extractionRef(batch.Extractions[0])},
			WindowSinceMS:     1_000,
			PublishedAtMS:     8_000,
		}))
		bank := mustBank(t, db, f.alice)
		assert.Equal(t, int64(0), bank.ConsolidatedSeq,
			"an excluded outstanding extraction must keep the bank dirty")
		assert.Equal(t, bankDirty, bank.DirtySeq)

		scheduleBankClaim(t, db, f.alice, band(9_000))
		second := claimOneBank(t, st, "bank-2", band(9_000), band(12_000))
		next, err := st.ConsolidationInput(ctx, second, 1_000, 1)
		require.NoError(t, err)
		require.Len(t, next.Extractions, 1)
		assert.Equal(t, "new", next.Extractions[0].RolloutSlug,
			"the extraction excluded by the cap is selected by the next attempt")

		require.NoError(t, st.PublishBank(ctx, second, proxymemory.Publication{
			MemoryMD:      "md",
			MemorySummary: "summary",
			ConsolidatedNames: []string{
				proxymemory.SummaryName(batch.Extractions[0]),
				proxymemory.SummaryName(next.Extractions[0]),
			},
			Acknowledged:  []proxymemory.ExtractionRef{extractionRef(next.Extractions[0])},
			WindowSinceMS: 1_000,
			PublishedAtMS: 9_000,
		}))
		bank = mustBank(t, db, f.alice)
		assert.Equal(t, bank.DirtySeq, bank.ConsolidatedSeq,
			"acknowledging the last outstanding extraction settles the bank")

		scheduleBankClaim(t, db, f.alice, band(20_000))
		clean, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: band(20_000), LeaseUntilMS: band(24_000), LeaseOwner: "bank-3", Limit: 1,
		})
		require.NoError(t, err)
		assert.Empty(t, clean, "a settled bank is not claimable")
	})

	t.Run("acknowledging a stale revision leaves the extraction obligated", func(t *testing.T) {
		f := newContractFixture("residual_stale_ack", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty, ConsolidatedSeq: 0, NextAttemptMS: band(0),
		})
		leg := f.aLeg("residual_stale_ack")
		seedExtractionRow(t, db, f.alice, leg, "stale", 1_000)
		claimed := claimOneBank(t, st, "bank-1", band(0), band(4_000))

		require.NoError(t, st.PublishBank(ctx, claimed, proxymemory.Publication{
			MemoryMD:      "md",
			MemorySummary: "summary",
			Acknowledged:  []proxymemory.ExtractionRef{{ALegID: leg, GeneratedAtMS: 999}},
			WindowSinceMS: 1_000,
			PublishedAtMS: 8_000,
		}))

		stored, found := readExtraction(t, db, leg)
		require.True(t, found)
		assert.Zero(t, stored.ConsolidatedAtMS, "a replaced revision must not be acknowledged")
		assert.Equal(t, int64(0), mustBank(t, db, f.alice).ConsolidatedSeq,
			"an unacknowledged extraction keeps the bank dirty")

		batch, err := st.ConsolidationInput(ctx, claimed, 1_000, 10)
		require.NoError(t, err)
		require.Len(t, batch.Extractions, 1, "the stale generation time stays outstanding")
	})

	t.Run("a lease-lost publication acknowledges nothing", func(t *testing.T) {
		f := newContractFixture("residual_lease_lost", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty, ConsolidatedSeq: 0, NextAttemptMS: band(0),
		})
		leg := f.aLeg("residual_lease_lost")
		seedExtractionRow(t, db, f.alice, leg, "lost", 1_000)
		claimed := claimOneBank(t, st, "bank-1", band(0), band(4_000))
		stale := claimed
		stale.LeaseOwner = "intruder"

		require.ErrorIs(t, st.PublishBank(ctx, stale, proxymemory.Publication{
			MemoryMD:      "md",
			MemorySummary: "summary",
			Acknowledged:  []proxymemory.ExtractionRef{{ALegID: leg, GeneratedAtMS: 1_000}},
			WindowSinceMS: 1_000,
			PublishedAtMS: 8_000,
		}), proxymemory.ErrLeaseLost)

		stored, found := readExtraction(t, db, leg)
		require.True(t, found)
		assert.Zero(t, stored.ConsolidatedAtMS, "a lease-lost publication must roll its acknowledgments back")
		assert.Equal(t, int64(1), mustBank(t, db, f.alice).Version)
	})

	t.Run("a re-extracted A-leg reopens its obligation", func(t *testing.T) {
		f := newContractFixture("residual_reextract", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("residual_reextract")
		upsertCapture(t, st, f.alice, aLeg, `["one"]`, 1_000)
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), 2_000)
		require.NoError(t, st.CompleteSession(ctx, cs, extractionFor(cs, 1_500)))
		firstRevision, err := st.ConsolidationInput(ctx, proxymemory.ClaimedBank{Owner: f.alice}, 0, 10)
		require.NoError(t, err)
		require.Len(t, firstRevision.Extractions, 1)
		staleRef := extractionRef(firstRevision.Extractions[0])
		scheduleBankClaim(t, db, f.alice, band(0))
		published := publishAllExtractions(t, db, st, f.alice, band(0), band(4_000))

		stored, found := readExtraction(t, db, aLeg)
		require.True(t, found)
		require.NotZero(t, stored.ConsolidatedAtMS, "the first revision is acknowledged")
		assert.Equal(t, published.DirtySeq, published.ConsolidatedSeq)

		upsertCapture(t, st, f.alice, aLeg, `["two"]`, 3_000)
		scheduleSessionClaim(t, db, aLeg, band(5_000))
		cs = claimOneSession(t, st, "extractor", band(5_000), band(8_000), 4_000)
		// A coarse or adjusted clock can give two revisions the same generation
		// time. Acknowledgment must still distinguish the actual content revision.
		require.NoError(t, st.CompleteSession(ctx, cs, extractionFor(cs, 1_500)))

		stored, found = readExtraction(t, db, aLeg)
		require.True(t, found)
		assert.Zero(t, stored.ConsolidatedAtMS, "a new revision is outstanding again")
		assert.Equal(t, int64(1_500), stored.GeneratedAtMS)

		scheduleBankClaim(t, db, f.alice, band(9_000))
		claimed := claimOneBank(t, st, "bank-2", band(9_000), band(12_000))
		require.NoError(t, st.PublishBank(ctx, claimed, proxymemory.Publication{
			Acknowledged: []proxymemory.ExtractionRef{staleRef}, PublishedAtMS: 9_000,
		}))
		batch, err := st.ConsolidationInput(ctx, claimed, 0, 10)
		require.NoError(t, err)
		require.Len(t, batch.Extractions, 1)
		assert.Equal(t, int64(1_500), batch.Extractions[0].GeneratedAtMS)
	})

	t.Run("an expired outstanding extraction does not block settlement", func(t *testing.T) {
		// The consolidation window bounds the obligation: a row older than
		// max_unused_days is deliberately outside it (an explicit, configurable
		// degradation) and must not keep the bank dirty forever.
		f := newContractFixture("residual_expired_settle", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty, ConsolidatedSeq: 0, NextAttemptMS: band(0),
		})
		expired := f.aLeg("residual_expired")
		live := f.aLeg("residual_live")
		seedExtractionRow(t, db, f.alice, expired, "expired", 500)
		seedExtractionRow(t, db, f.alice, live, "live", 2_000)
		claimed := claimOneBank(t, st, "bank-1", band(0), band(4_000))

		batch, err := st.ConsolidationInput(ctx, claimed, 1_000, 10)
		require.NoError(t, err)
		require.Len(t, batch.Extractions, 1, "the expired row is outside the window")
		require.NoError(t, st.PublishBank(ctx, claimed, proxymemory.Publication{
			MemoryMD:          "md",
			MemorySummary:     "summary",
			ConsolidatedNames: batch.WindowNames,
			Acknowledged:      []proxymemory.ExtractionRef{extractionRef(batch.Extractions[0])},
			WindowSinceMS:     1_000,
			PublishedAtMS:     8_000,
		}))

		bank := mustBank(t, db, f.alice)
		assert.Equal(t, bank.DirtySeq, bank.ConsolidatedSeq,
			"an out-of-window row is not an obligation of this window")
		stored, found := readExtraction(t, db, expired)
		require.True(t, found, "the expired row is never deleted by settlement")
		assert.Zero(t, stored.ConsolidatedAtMS, "the expired row is never acknowledged")
	})

	t.Run("window names carry out-of-batch consolidated extractions", func(t *testing.T) {
		f := newContractFixture("residual_window_names", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 1, DirtySeq: bankDirty, ConsolidatedSeq: 0, NextAttemptMS: band(0),
		})
		leg := f.aLeg("residual_window_names")
		seedExtractionRow(t, db, f.alice, leg, "windowed", 1_000)
		claimed := claimOneBank(t, st, "bank-1", band(0), band(4_000))

		batch, err := st.ConsolidationInput(ctx, claimed, 1_000, 10)
		require.NoError(t, err)
		require.Len(t, batch.Extractions, 1)
		require.NoError(t, st.PublishBank(ctx, claimed, proxymemory.Publication{
			MemoryMD:          "md",
			MemorySummary:     "summary",
			ConsolidatedNames: batch.WindowNames,
			Acknowledged:      []proxymemory.ExtractionRef{extractionRef(batch.Extractions[0])},
			WindowSinceMS:     1_000,
			PublishedAtMS:     8_000,
		}))

		after, err := st.ConsolidationInput(ctx, claimed, 1_000, 10)
		require.NoError(t, err)
		assert.Empty(t, after.Extractions, "an acknowledged extraction is no longer outstanding")
		assert.Equal(t, batch.WindowNames, after.WindowNames,
			"the window keeps reporting live names so omission is not removal")
	})
}

// runServiceContract runs the service-query, forget and purge cases of design
// §12.6 through runContract's per-case opener, so the SQLite and PostgreSQL
// Direct lanes execute identical behavior. Every case uses its own fixture
// scope and run token (per-subtest-unique keys) and cleans up through its own
// opener handle.
//
// The forget, purge and lease-participating cases follow runLeaseContract's
// negative-band isolation technique: this suite's claimable and purgeable
// fixture rows carry stamps derived from the unique run token, and every
// claim window or purge cutoff is taken from that same band. Residue of an
// earlier run — including this suite's own leftovers after an aborted run —
// carries the non-negative default or a larger (less negative) band stamp, so
// it never satisfies a band-scoped predicate. The one deliberately global
// clause is purge's fenced-session delete; its expected row count is measured
// from the shared database immediately before the call, so residue cannot
// change the assertion (only a concurrently running purge could).
func runServiceContract(
	t *testing.T,
	openFixture func(t *testing.T, f *contractFixture) (*bun.DB, *proxymemorystore.Store),
	runID string,
) {
	t.Helper()
	runBase, err := strconv.ParseInt(runID, 36, 64)
	require.NoError(t, err)
	activity := func(offset int64) int64 { return runBase + offset }
	band := func(offset int64) int64 { return -runBase + offset }

	t.Run("list banks returns only the caller's banks with their pending counts", func(t *testing.T) {
		f := newContractFixture("list_banks", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		upsertCapture(t, st, f.alice, f.aLeg("list_banks_one"), `["one"]`, 1_000)
		upsertCapture(t, st, f.alice, f.aLeg("list_banks_two"), `["two"]`, 2_000)
		seedExtractionRow(t, db, f.alice, f.aLeg("list_banks_extraction"), "alpha", 2_000)
		seedNote(t, db, f.alice, "note_"+f.token, "pending note", 1_000)
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 3, MemorySummary: "summary", PublishedAtMS: 4_000, DirtySeq: 1,
		})
		// The same principal's second, empty bank in another project.
		seedBankRow(t, db, f.owner(f.alice.PrincipalID, f.projectQ), bankSeed{
			ProjectLabel: "/work/two", Version: 0, DirtySeq: 1,
		})
		// A foreign principal with A's identical project key and a bank of
		// the same principal under another tenant must stay invisible.
		foreign := f.owner("bob", f.projectP)
		foreignLeg := f.aLeg("list_banks_foreign")
		removeBindingRow(t, db, foreignLeg)
		seedBindingRow(t, db, foreign, foreignLeg, f.projectP, "/work/one")
		upsertCapture(t, st, foreign, foreignLeg, `["foreign"]`, 1_000)
		seedExtractionRow(t, db, foreign, f.aLeg("list_banks_foreign_extraction"), "beta", 2_000)
		seedBankRow(t, db, foreign, bankSeed{ProjectLabel: "/work/one", Version: 9, DirtySeq: 1})
		cleanupFixtureOwner(t, db, foreign)
		otherTenant := proxymemory.Owner{TenantID: "tenant-b", PrincipalID: f.alice.PrincipalID, ProjectKey: f.projectP}
		otherTenantLeg := f.aLeg("list_banks_other_tenant")
		removeBindingRow(t, db, otherTenantLeg)
		seedBindingRow(t, db, otherTenant, otherTenantLeg, f.projectP, "/work/one")
		upsertCapture(t, st, otherTenant, otherTenantLeg, `["tenant"]`, 1_000)
		seedBankRow(t, db, otherTenant, bankSeed{ProjectLabel: "/work/one", Version: 8, DirtySeq: 1})
		cleanupFixtureOwner(t, db, otherTenant)

		banks, err := st.ListBanks(ctx, proxymemory.Principal{TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID})
		require.NoError(t, err)
		// The shared database may still hold earlier runs' banks of the same
		// principal (their project keys carry their own run token), so the
		// case pins the returned ordering globally and its own unique project
		// keys exactly. Both the foreign principal and the other tenant reuse
		// this fixture's project keys, so a leaked row would add a second
		// entry under the same scope.
		var ownP, ownQ []proxymemory.BankStatus
		scopes := make([]string, 0, len(banks))
		for _, bank := range banks {
			scopes = append(scopes, bank.ScopeID)
			switch bank.ScopeID {
			case f.projectP:
				ownP = append(ownP, bank)
			case f.projectQ:
				ownQ = append(ownQ, bank)
			}
		}
		assert.IsNonDecreasing(t, scopes, "banks are ordered by scope")
		require.Len(t, ownP, 1, "another owner's bank must not appear under the same scope")
		assert.Equal(t, proxymemory.BankStatus{
			ScopeID: f.projectP, ProjectLabel: "/work/one", Version: 3, PublishedAtMS: 4_000,
			PendingSessions: 2, Extractions: 1, PendingNotes: 1,
		}, ownP[0])
		require.Len(t, ownQ, 1)
		assert.Equal(t, proxymemory.BankStatus{
			ScopeID: f.projectQ, ProjectLabel: "/work/two",
		}, ownQ[0])
	})

	t.Run("bank status counts one owner's pending work and keeps identical keys apart", func(t *testing.T) {
		f := newContractFixture("bank_status", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		upsertCapture(t, st, f.alice, f.aLeg("bank_status"), `["work"]`, 1_000)
		seedExtractionRow(t, db, f.alice, f.aLeg("bank_status_extraction"), "alpha", 2_000)
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 2, MemoryMD: "handbook", MemorySummary: "summary",
			PublishedAtMS: 3_000, DirtySeq: 1,
		})

		status, err := st.BankStatus(ctx, f.alice)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.BankStatus{
			ScopeID: f.projectP, ProjectLabel: "/work/one", Version: 2, PublishedAtMS: 3_000,
			PendingSessions: 1, Extractions: 1, PendingNotes: 0,
		}, status)

		_, err = st.BankStatus(ctx, proxymemory.Owner{
			TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID, ProjectKey: f.projectP + "-missing",
		})
		require.ErrorIs(t, err, proxymemory.ErrNotFound)

		// Another principal with A's identical project key owns its own row
		// and never sees A's counts; A's status stays unchanged.
		foreign := f.owner("bob", f.projectP)
		seedBankRow(t, db, foreign, bankSeed{ProjectLabel: "/work/one", Version: 9, DirtySeq: 1})
		cleanupFixtureOwner(t, db, foreign)
		foreignStatus, err := st.BankStatus(ctx, foreign)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.BankStatus{
			ScopeID: f.projectP, ProjectLabel: "/work/one", Version: 9,
		}, foreignStatus)
		status, err = st.BankStatus(ctx, f.alice)
		require.NoError(t, err)
		assert.Equal(t, int64(2), status.Version)
		assert.Equal(t, 1, status.PendingSessions)
	})

	t.Run("documents join the bank with its rollout summaries", func(t *testing.T) {
		f := newContractFixture("documents", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 5, MemoryMD: "handbook", MemorySummary: "summary",
			PublishedAtMS: 9_000, DirtySeq: 1,
		})
		seedExtractionFull(t, db, f.alice, f.aLeg("documents_alpha"), "session-0123456789ab", "alpha", 1_500_000, 1_000)
		seedExtractionFull(t, db, f.alice, f.aLeg("documents_beta"), "session-0123456789ab", "beta", 2_500_000, 2_000)
		foreign := f.owner("bob", f.projectP)
		seedExtractionFull(t, db, foreign, f.aLeg("documents_foreign"), "session-0123456789ab", "foreign", 3_500_000, 3_000)
		cleanupFixtureOwner(t, db, foreign)

		docs, err := st.Documents(ctx, f.alice)
		require.NoError(t, err)
		assert.Equal(t, "handbook", docs.MemoryMD)
		assert.Equal(t, "summary", docs.MemorySummary)
		assert.Equal(t, "/work/one", docs.ProjectLabel)
		// The key comes from the feature package's SummaryName, and the
		// content pins the extraction's source-update time (not its
		// generation time) as updated_at.
		alphaName := proxymemory.SummaryName(proxymemory.Extraction{
			SessionRef: "session-0123456789ab", RolloutSlug: "alpha", GeneratedAtMS: 1_000,
		})
		betaName := proxymemory.SummaryName(proxymemory.Extraction{
			SessionRef: "session-0123456789ab", RolloutSlug: "beta", GeneratedAtMS: 2_000,
		})
		assert.Equal(t, map[string]string{
			alphaName + ".md": rolloutSummaryDocument("/work/one", "session-0123456789ab", 1_500_000, "rollout summary"),
			betaName + ".md":  rolloutSummaryDocument("/work/one", "session-0123456789ab", 2_500_000, "rollout summary"),
		}, docs.RolloutSummaries)

		_, err = st.Documents(ctx, proxymemory.Owner{
			TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID, ProjectKey: f.projectP + "-missing",
		})
		require.ErrorIs(t, err, proxymemory.ErrNotFound)

		// A bank without extractions keeps an empty rollout summary set.
		empty := f.owner(f.alice.PrincipalID, f.projectQ)
		seedBankRow(t, db, empty, bankSeed{ProjectLabel: "/work/two", DirtySeq: 1})
		docs, err = st.Documents(ctx, empty)
		require.NoError(t, err)
		assert.Empty(t, docs.RolloutSummaries)
	})

	t.Run("add note requires the caller's bank and dirties it", func(t *testing.T) {
		f := newContractFixture("add_note", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		noteID := "note_" + f.token
		require.ErrorIs(t, st.AddNote(ctx, f.alice, proxymemory.Note{
			NoteID: noteID, Text: "no bank yet", CreatedAtMS: 1_000,
		}), proxymemory.ErrNotFound)
		assert.Empty(t, noteIDs(t, db, f.alice))

		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 2, MemoryMD: "handbook", MemorySummary: "summary",
			PublishedAtMS: 3_000, DirtySeq: 4, ConsolidatedSeq: 4,
		})
		require.NoError(t, st.AddNote(ctx, f.alice, proxymemory.Note{
			NoteID: noteID, Text: "remember this", CreatedAtMS: 1_000,
		}))

		assert.Equal(t, []string{noteID}, noteIDs(t, db, f.alice))
		var storedText string
		require.NoError(t, db.NewRaw(`SELECT note FROM proxy_memory_notes WHERE note_id = ?`, noteID).Scan(ctx, &storedText))
		assert.Equal(t, "remember this", storedText)
		bank := mustBank(t, db, f.alice)
		assert.Equal(t, int64(5), bank.DirtySeq, "a note dirties the bank for the next consolidation")
		assert.Equal(t, int64(4), bank.ConsolidatedSeq)
		assert.Equal(t, int64(2), bank.Version)
		assert.Equal(t, "handbook", bank.MemoryMD)

		// A foreign owner shares the project key but has no bank: no note.
		foreign := f.owner("bob", f.projectP)
		foreignNote := "note_foreign_" + f.token
		require.ErrorIs(t, st.AddNote(ctx, foreign, proxymemory.Note{
			NoteID: foreignNote, Text: "foreign", CreatedAtMS: 2_000,
		}), proxymemory.ErrNotFound)
		assert.Empty(t, noteIDs(t, db, foreign))
	})

	t.Run("forget deletes only the caller's rows and fences its A-legs", func(t *testing.T) {
		f := newContractFixture("forget", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		sessionLeg := f.aLeg("forget_session")
		extractionLeg := f.aLeg("forget_extraction")
		upsertCapture(t, st, f.alice, sessionLeg, `["turn"]`, 1_000)
		seedExtractionRow(t, db, f.alice, extractionLeg, "alpha", 2_000)
		ownNote := "note_" + f.token
		seedNote(t, db, f.alice, ownNote, "own note", 1_000)
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 3, MemoryMD: "handbook", MemorySummary: "summary",
			PublishedAtMS: 4_000, DirtySeq: 1,
		})

		// Another principal with A's identical project path keeps its rows.
		foreign := f.owner("bob", f.projectP)
		foreignSessionLeg := f.aLeg("forget_foreign_session")
		foreignExtractionLeg := f.aLeg("forget_foreign_extraction")
		removeBindingRow(t, db, foreignSessionLeg)
		removeBindingRow(t, db, foreignExtractionLeg)
		seedBindingRow(t, db, foreign, foreignSessionLeg, f.projectP, "/work/one")
		upsertCapture(t, st, foreign, foreignSessionLeg, `["foreign"]`, 1_000)
		seedExtractionRow(t, db, foreign, foreignExtractionLeg, "beta", 2_000)
		foreignNote := "note_foreign_" + f.token
		seedNote(t, db, foreign, foreignNote, "foreign note", 1_000)
		seedBankRow(t, db, foreign, bankSeed{
			ProjectLabel: "/work/one", Version: 7, MemoryMD: "foreign handbook", MemorySummary: "foreign summary",
			PublishedAtMS: 5_000, DirtySeq: 1,
		})
		cleanupFixtureOwner(t, db, foreign)

		require.NoError(t, st.Forget(ctx, f.alice, 9_000))

		assert.Equal(t, 0, sessionCount(t, db, sessionLeg))
		assert.Equal(t, 0, extractionCount(t, db, extractionLeg))
		assert.Empty(t, noteIDs(t, db, f.alice))
		_, bankOK := readBank(t, db, f.alice)
		assert.False(t, bankOK)
		assert.Equal(t, storedFence{
			TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID, ForgottenAtMS: 9_000,
		}, readFence(t, db, sessionLeg))
		assert.Equal(t, storedFence{
			TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID, ForgottenAtMS: 9_000,
		}, readFence(t, db, extractionLeg))

		foreignBank := mustBank(t, db, foreign)
		assert.Equal(t, int64(7), foreignBank.Version)
		assert.Equal(t, "foreign handbook", foreignBank.MemoryMD)
		assert.Equal(t, 1, sessionCount(t, db, foreignSessionLeg))
		assert.Equal(t, 1, extractionCount(t, db, foreignExtractionLeg))
		assert.Equal(t, []string{foreignNote}, noteIDs(t, db, foreign))
		assert.Equal(t, 0, fenceCount(t, db, foreignSessionLeg))

		require.ErrorIs(t, st.Forget(ctx, f.alice, 10_000), proxymemory.ErrNotFound)
	})

	t.Run("forget during extraction discards the claimed work", func(t *testing.T) {
		f := newContractFixture("forget_during_extraction", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("forget_during_extraction")
		upsertCapture(t, st, f.alice, aLeg, `["turn"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: "/work/one", Version: 1, DirtySeq: 1})

		require.NoError(t, st.Forget(ctx, f.alice, 5_000))
		require.ErrorIs(t, st.CompleteSession(ctx, cs, extractionFor(cs, 7_000)), proxymemory.ErrLeaseLost)

		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		assert.Equal(t, 0, extractionCount(t, db, aLeg))
		_, bankOK := readBank(t, db, f.alice)
		assert.False(t, bankOK)
		assert.Equal(t, 1, fenceCount(t, db, aLeg))
	})

	t.Run("purge removes abandoned and fenced work but never fence rows", func(t *testing.T) {
		f := newContractFixture("purge", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		now := band(0)
		sessionsBefore := band(-1_000)
		extractionsBefore := band(-1_000)

		oldLeg := f.aLeg("purge_old")
		freshLeg := f.aLeg("purge_fresh")
		leasedLeg := f.aLeg("purge_leased")
		fencedLeg := f.aLeg("purge_fenced")
		expiredLeg := f.aLeg("purge_expired_extraction")
		freshExtractionLeg := f.aLeg("purge_fresh_extraction")
		upsertCapture(t, st, f.alice, oldLeg, `["old"]`, band(-2_000))
		upsertCapture(t, st, f.alice, freshLeg, `["fresh"]`, band(0))
		upsertCapture(t, st, f.alice, leasedLeg, `["leased"]`, band(-2_000))
		_, err := db.NewRaw(
			`UPDATE proxy_memory_sessions SET lease_owner = ?, lease_until_ms = ? WHERE a_leg_id = ?`,
			"other-instance", band(1_000), leasedLeg,
		).Exec(ctx)
		require.NoError(t, err)
		upsertCapture(t, st, f.alice, fencedLeg, `["fenced"]`, band(0))
		_, err = db.NewRaw(
			`INSERT INTO proxy_memory_fence (a_leg_id, tenant_id, principal_id, forgotten_at_ms) VALUES (?, ?, ?, ?)`,
			fencedLeg, f.alice.TenantID, f.alice.PrincipalID, 1_000,
		).Exec(ctx)
		require.NoError(t, err)
		seedBankRow(t, db, f.alice, bankSeed{
			ProjectLabel: "/work/one", Version: 6, MemoryMD: "handbook", MemorySummary: "summary",
			PublishedAtMS: 7_000, DirtySeq: 5, ConsolidatedSeq: 5,
		})
		seedExtractionFull(t, db, f.alice, expiredLeg, "session-0123456789ab", "alpha", 1_000, band(-2_000))
		seedExtractionFull(t, db, f.alice, freshExtractionLeg, "session-0123456789ab", "beta", 2_000, band(0))

		// The expected count is measured from the shared database immediately
		// before the call: purge's fenced-session clause is global by design,
		// so residue of other runs contributes to the deleted total, and the
		// census pins the exact return value without assuming an empty
		// database. The band cutoffs keep earlier-run residue out.
		var expectedSessions, expectedExtractions int
		require.NoError(t, db.NewRaw(`
			SELECT count(*) FROM proxy_memory_sessions
			WHERE (last_activity_ms < ? AND (lease_owner = '' OR lease_until_ms < ?))
			   OR a_leg_id IN (SELECT a_leg_id FROM proxy_memory_fence)
		`, sessionsBefore, now).Scan(ctx, &expectedSessions))
		require.NoError(t, db.NewRaw(
			`SELECT count(*) FROM proxy_memory_extractions WHERE generated_at_ms < ?`,
			extractionsBefore,
		).Scan(ctx, &expectedExtractions))

		purged, err := st.Purge(ctx, now, sessionsBefore, extractionsBefore, 1_000)
		require.NoError(t, err)
		assert.Equal(t, expectedSessions+expectedExtractions, purged)

		assert.Equal(t, 0, sessionCount(t, db, oldLeg))
		assert.Equal(t, 0, sessionCount(t, db, fencedLeg))
		assert.Equal(t, 1, sessionCount(t, db, freshLeg))
		assert.Equal(t, 1, sessionCount(t, db, leasedLeg))
		assert.Equal(t, 0, extractionCount(t, db, expiredLeg))
		assert.Equal(t, 1, extractionCount(t, db, freshExtractionLeg))
		bank := mustBank(t, db, f.alice)
		assert.Equal(t, int64(6), bank.DirtySeq, "purging expired extractions dirties their bank")
		assert.Equal(t, int64(5), bank.ConsolidatedSeq)
		assert.Equal(t, int64(6), bank.Version)
		assert.Equal(t, 1, fenceCount(t, db, fencedLeg), "fence rows are never purged")
	})

	t.Run("a forgotten A-leg stays refused after a future purge", func(t *testing.T) {
		f := newContractFixture("durable_forget", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("durable_forget")
		upsertCapture(t, st, f.alice, aLeg, `["turn"]`, 1_000)
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: "/work/one", DirtySeq: 1})
		require.NoError(t, st.Forget(ctx, f.alice, band(0)))

		// Weeks later the worker purges aged work: the fence row survives.
		_, err := st.Purge(ctx, band(10_000), band(-10_000), band(-10_000), 0)
		require.NoError(t, err)
		assert.Equal(t, 1, fenceCount(t, db, aLeg))

		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg, Transcript: `["resumed"]`, ActivityMS: band(20_000), SourceSeq: testSourceSeq(band(20_000)),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertForgotten, outcome)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
	})

	t.Run("a forgotten contributor stays fenced after its extraction was purged", func(t *testing.T) {
		f := newContractFixture("forget_after_purge", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("forget_after_purge")
		upsertCapture(t, st, f.alice, aLeg, `["turn"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))
		// The extraction is born through the real completion path, which also
		// records the durable participation of the A-leg.
		require.NoError(t, st.CompleteSession(ctx, cs, extractionFor(cs, band(-2_000))))
		assert.Equal(t, 1, ledgerCount(t, db, aLeg))

		// The worker later purges the aged extraction; the ledger row is what
		// still proves the A-leg contributed to the bank.
		purged, err := st.Purge(ctx, band(0), band(-1_000), band(-1_000), 1_000)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, purged, 1)
		assert.Equal(t, 0, extractionCount(t, db, aLeg))
		assert.Equal(t, 1, ledgerCount(t, db, aLeg))

		require.NoError(t, st.Forget(ctx, f.alice, 9_000))
		// The fence was written from the ledger before the owner's ledger rows
		// were deleted, so growth stays bounded once forgotten.
		assert.Equal(t, storedFence{
			TenantID: f.alice.TenantID, PrincipalID: f.alice.PrincipalID, ForgottenAtMS: 9_000,
		}, readFence(t, db, aLeg))
		assert.Equal(t, 0, ledgerCount(t, db, aLeg))

		// Requirement 8.6: resuming the same A-leg weeks later must not
		// recapture the forgotten bank.
		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg, Transcript: `["resumed"]`, ActivityMS: activity(20), SourceSeq: testSourceSeq(activity(20)),
		})
		require.NoError(t, err)
		assert.Equal(t, proxymemory.UpsertForgotten, outcome)
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		assert.Equal(t, 0, ledgerCount(t, db, aLeg))

		// A repeated forget has nothing left to erase.
		require.ErrorIs(t, st.Forget(ctx, f.alice, 10_000), proxymemory.ErrNotFound)
	})

	// The two cases below exercise the bank-row lock that serializes AddNote
	// and Forget for one owner (Requirements 8.3, 8.5). They are gated to
	// PostgreSQL: the harness parks one transaction behind the other's lock,
	// which SQLite's single-writer engine cannot serve on a second connection.
	t.Run("a concurrent forget locks the note's bank row and erases the committed note", func(t *testing.T) {
		f := newContractFixture("add_note_forget_lock", runID)
		db, st := openFixture(t, f)
		if db.Dialect().Name() != dialect.PG {
			t.Skip("the interleaving proof needs PostgreSQL row locks; SQLite serializes writers at the engine level")
		}
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: "/work/one", DirtySeq: 1})
		noteID := "note_" + f.token

		// Park AddNote right after its first bank statement. On PostgreSQL that
		// statement already holds the bank-row lock, so the Forget started on
		// the pool's other connection cannot commit before AddNote's
		// transaction ends. The hook resumes AddNote when Forget reports its
		// own bank lock (the serialized order this case proves) or, without
		// that lock, when Forget has committed behind the parked transaction
		// (the order that left J1's note behind). Both events are observed
		// through Bun query hooks: no sleep, race detector or scheduler
		// assumption is involved.
		hook := &bankLockInterleave{
			pauseMarker: raceMarkerAddNote,
			pauseSQL:    "SELECT 1 FROM proxy_memory_banks",
			lockMarker:  raceMarkerForget,
			lockSQL:     "FOR UPDATE",
			lockSignal:  make(chan struct{}),
		}
		forgetCtx := context.WithValue(context.Background(), raceMarkerKey{}, raceMarkerForget)
		hook.startOpposite = func() <-chan error {
			done := make(chan error, 1)
			go func() { done <- st.Forget(forgetCtx, f.alice, 9_000) }()
			return done
		}
		db.AddQueryHook(hook)

		addNoteErr := st.AddNote(
			context.WithValue(context.Background(), raceMarkerKey{}, raceMarkerAddNote), f.alice,
			proxymemory.Note{NoteID: noteID, Text: "forgotten private note", CreatedAtMS: 1_000},
		)
		forgetErr := hook.oppositeOutcome()
		require.True(t, hook.ran, "the hook must have paused AddNote after its bank statement")

		require.NoError(t, forgetErr, "Forget must commit successfully")
		if addNoteErr != nil {
			require.ErrorIs(t, addNoteErr, proxymemory.ErrNotFound, "a refused note write reports the missing bank")
		}
		_, bankExists := readBank(t, db, f.alice)
		require.False(t, bankExists, "the erasure wins the interleaving")
		require.Empty(t, noteIDs(t, db, f.alice),
			"a note inserted by an overlapping AddNote must never survive the erasure of its bank")
		require.True(t, hook.lockObserved,
			"Forget must take the same bank lock AddNote holds before its deletes")
	})

	t.Run("a concurrent add note waits for the forget's bank lock and finds no bank", func(t *testing.T) {
		f := newContractFixture("forget_add_note_lock", runID)
		db, st := openFixture(t, f)
		if db.Dialect().Name() != dialect.PG {
			t.Skip("the interleaving proof needs PostgreSQL row locks; SQLite serializes writers at the engine level")
		}
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: "/work/one", DirtySeq: 1})
		noteID := "note_" + f.token

		// Park Forget right after its notes DELETE, inside its transaction. On
		// PostgreSQL it already holds the bank lock taken before the deletes,
		// so the AddNote started on the pool's other connection waits for its
		// own bank lock and then finds no bank: ErrNotFound, no note. The hook
		// resumes Forget when AddNote reports its bank lock (the serialized
		// order) or, without that lock, when AddNote has committed against the
		// still-visible bank (the order that left J1's note behind).
		hook := &bankLockInterleave{
			pauseMarker: raceMarkerForget,
			pauseSQL:    "DELETE FROM proxy_memory_notes",
			lockMarker:  raceMarkerAddNote,
			lockSQL:     "FOR UPDATE",
			lockSignal:  make(chan struct{}),
		}
		addNoteCtx := context.WithValue(context.Background(), raceMarkerKey{}, raceMarkerAddNote)
		hook.startOpposite = func() <-chan error {
			done := make(chan error, 1)
			go func() {
				done <- st.AddNote(addNoteCtx, f.alice,
					proxymemory.Note{NoteID: noteID, Text: "forgotten private note", CreatedAtMS: 1_000})
			}()
			return done
		}
		db.AddQueryHook(hook)

		forgetErr := st.Forget(
			context.WithValue(context.Background(), raceMarkerKey{}, raceMarkerForget), f.alice, 9_000,
		)
		addNoteErr := hook.oppositeOutcome()
		require.True(t, hook.ran, "the hook must have paused Forget after deleting the notes")

		require.NoError(t, forgetErr, "Forget must commit successfully")
		_, bankExists := readBank(t, db, f.alice)
		require.False(t, bankExists, "the erasure wins the interleaving")
		require.Empty(t, noteIDs(t, db, f.alice),
			"a note inserted by an overlapping AddNote must never survive the erasure of its bank")
		require.ErrorIs(t, addNoteErr, proxymemory.ErrNotFound,
			"the serialized AddNote runs after the erasure and finds no bank")
		require.True(t, hook.lockObserved,
			"AddNote must take the bank lock before its note write")
	})

	// The two cases below pin the lock order that keeps Forget compatible with
	// the store's other writers on PostgreSQL (review round 2: the bank-first
	// placement of the previous fix deadlocked with both CompleteSession and
	// Purge). Each case parks one transaction on the row lock it takes first,
	// starts a Forget on the pool's second connection, and resumes the parked
	// transaction while that Forget is at the statement that must wait on the
	// lock. With the reordered lock the Forget holds no bank lock while those
	// writers run and both transactions complete; with the bank lock taken
	// first the two transactions form a closed cycle PostgreSQL aborts with
	// deadlock_detected. The bounded context turns a schedule that would keep
	// waiting into a t.Fatal instead of a hang, and a deadlock error is a
	// failure of the assertions below, never retried.
	t.Run("a concurrent forget and complete session do not deadlock on the bank and session rows", func(t *testing.T) {
		f := newContractFixture("forget_complete_lock_order", runID)
		db, st := openFixture(t, f)
		if db.Dialect().Name() != dialect.PG {
			t.Skip("the lock-order proof needs PostgreSQL row locks; SQLite serializes writers at the engine level")
		}
		aLeg := f.aLeg("forget_complete_lock_order")
		upsertCapture(t, st, f.alice, aLeg, `["turn"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		cs := claimOneSession(t, st, "extractor", band(0), band(4_000), activity(16))
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: "/work/one", DirtySeq: 1})

		// CompleteSession holds the session row (its §12.4 step 1) and goes on
		// to the bank upsert; Forget must delete the same session row without
		// holding the bank lock yet, or the two form the F1 cycle.
		ctx, cancel := context.WithTimeout(context.Background(), lockOrderDeadline)
		defer cancel()
		hook := &lockOrderInterleave{
			pauseMarker:   raceMarkerComplete,
			pauseSQL:      "SELECT revision FROM proxy_memory_sessions",
			triggerSQL:    "DELETE FROM proxy_memory_sessions",
			triggerSignal: make(chan struct{}),
		}
		hook.startForget = func() <-chan error {
			done := make(chan error, 1)
			go func() {
				done <- st.Forget(context.WithValue(ctx, raceMarkerKey{}, raceMarkerForget), f.alice, 9_000)
			}()
			return done
		}
		db.AddQueryHook(hook)

		completeErr := st.CompleteSession(
			context.WithValue(ctx, raceMarkerKey{}, raceMarkerComplete), cs, extractionFor(cs, 7_000),
		)
		require.True(t, hook.ran, "the hook must have paused CompleteSession after its session-row lock")
		forgetErr := hook.forgetOutcome(ctx, t)
		require.True(t, hook.triggerObserved,
			"Forget must have reached its session DELETE while CompleteSession held the session row")

		require.NoError(t, completeErr, "a deadlock abort instead of an API outcome fails this case")
		require.NoError(t, forgetErr, "a deadlock abort instead of an API outcome fails this case")
		// The erasure is the last writer: the extraction and the dirty bump
		// CompleteSession just committed are deleted by the same Forget.
		assert.Equal(t, 0, sessionCount(t, db, aLeg))
		assert.Equal(t, 0, extractionCount(t, db, aLeg))
		_, bankExists := readBank(t, db, f.alice)
		assert.False(t, bankExists)
		assert.Empty(t, noteIDs(t, db, f.alice))
		assert.Equal(t, 1, fenceCount(t, db, aLeg))
	})

	t.Run("a concurrent forget and purge do not deadlock on the bank and extraction rows", func(t *testing.T) {
		f := newContractFixture("forget_purge_lock_order", runID)
		db, st := openFixture(t, f)
		if db.Dialect().Name() != dialect.PG {
			t.Skip("the lock-order proof needs PostgreSQL row locks; SQLite serializes writers at the engine level")
		}
		aLeg := f.aLeg("forget_purge_lock_order")
		seedExtractionRow(t, db, f.alice, aLeg, "aged", band(-2_000))
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: "/work/one", DirtySeq: 1})

		// Purge holds the expired extraction row (its per-owner transaction
		// deletes it first) and goes on to the bank dirty bump; Forget must
		// delete the same extraction row without holding the bank lock yet, or
		// the two form the F2 cycle. The band cutoffs keep residue of earlier
		// runs out (see the isolation comment above).
		ctx, cancel := context.WithTimeout(context.Background(), lockOrderDeadline)
		defer cancel()
		hook := &lockOrderInterleave{
			pauseMarker:   raceMarkerPurge,
			pauseSQL:      "DELETE FROM proxy_memory_extractions",
			triggerSQL:    "DELETE FROM proxy_memory_extractions",
			triggerSignal: make(chan struct{}),
		}
		hook.startForget = func() <-chan error {
			done := make(chan error, 1)
			go func() {
				done <- st.Forget(context.WithValue(ctx, raceMarkerKey{}, raceMarkerForget), f.alice, 9_000)
			}()
			return done
		}
		db.AddQueryHook(hook)

		_, purgeErr := st.Purge(
			context.WithValue(ctx, raceMarkerKey{}, raceMarkerPurge), band(0), band(-1_000), band(-1_000), 1,
		)
		require.True(t, hook.ran, "the hook must have paused Purge after its expired-extraction delete")
		forgetErr := hook.forgetOutcome(ctx, t)
		require.True(t, hook.triggerObserved,
			"Forget must have reached its extraction DELETE while Purge held the expired extraction")

		require.NoError(t, purgeErr, "a deadlock abort instead of an API outcome fails this case")
		require.NoError(t, forgetErr, "a deadlock abort instead of an API outcome fails this case")
		// Whichever call removed the aged extraction, the bank is gone with it
		// and the fence row keeps the erasure durable.
		assert.Equal(t, 0, extractionCount(t, db, aLeg))
		_, bankExists := readBank(t, db, f.alice)
		assert.False(t, bankExists)
		assert.Equal(t, 1, fenceCount(t, db, aLeg))
	})

	// The case below pins the claim's snapshot boundary under the approved
	// epoch-first transaction: the claim returns the idle transcript it leased,
	// and a capture committed immediately after the claim transaction cannot
	// rewrite that returned snapshot. The capture cannot interleave inside the
	// transaction because both writers take the owner epoch first; it is
	// therefore committed at the post-commit boundary.
	t.Run("a claim snapshots the idle transcript before a post-commit capture", func(t *testing.T) {
		f := newContractFixture("post_lease_capture", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("post_lease_capture")
		upsertCapture(t, st, f.alice, aLeg, `["idle"]`, activity(8))
		scheduleSessionClaim(t, db, aLeg, band(0))
		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), IdleBeforeMS: activity(16), NotBeforeMS: activity(1),
			LeaseUntilMS: band(4_000), LeaseOwner: "post-lease-worker", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		assert.Equal(t, `["idle"]`, claimed[0].Transcript)
		assert.Equal(t, activity(8), claimed[0].LastActivityMS)
		assert.Equal(t, int64(1), claimed[0].Revision)

		outcome, err := st.UpsertSession(ctx, proxymemory.SessionCapture{
			Owner: f.alice, ProjectLabel: "/work/one", ALegID: aLeg,
			Transcript: `["active"]`, ActivityMS: activity(17), SourceSeq: testSourceSeq(activity(17)),
		})
		require.NoError(t, err)
		require.Equal(t, proxymemory.UpsertStored, outcome)

		require.NoError(t, st.CompleteSession(ctx, claimed[0], extractionFor(claimed[0], band(100))))
		row := readSession(t, db, aLeg)
		assert.Equal(t, `["active"]`, row.Transcript)
		assert.Equal(t, activity(17), row.LastActivityMS)
		assert.Empty(t, row.LeaseOwner)
	})

	t.Run("a claim reconciles a session that left the idle window after its selection", func(t *testing.T) {
		f := newContractFixture("claim_window_race", runID)
		db, st := openFixture(t, f)
		if db.Dialect().Name() != dialect.PG {
			t.Skip("the interleaving proof needs a second connection; SQLite serializes writers at the engine level")
		}
		ctx := context.Background()
		// The candidate order is deterministic (last_activity ascending), so
		// the hook can advance exactly the row each candidate SELECT returned.
		raced := []string{
			f.aLeg("claim_window_race_a"),
			f.aLeg("claim_window_race_b"),
			f.aLeg("claim_window_race_c"),
		}
		for i, aLeg := range raced {
			upsertCapture(t, st, f.alice, aLeg, `["race"]`, activity(int64(2+2*i)))
			scheduleSessionClaim(t, db, aLeg, band(0))
		}

		hook := &claimWindowRaceHook{
			owner:        f.alice,
			projectLabel: "/work/one",
			aLegs:        raced,
			activeMS:     activity(17),
		}
		db.AddQueryHook(hook)

		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: band(0), IdleBeforeMS: activity(16), NotBeforeMS: activity(1),
			LeaseUntilMS: band(4_000), LeaseOwner: "racer", Limit: 3,
		})
		require.NoError(t, err)
		require.NoError(t, hook.racedErr)
		assert.Empty(t, claimed, "no candidate may be leased once it left the idle window")
		assert.Equal(t, len(raced), hook.raced,
			"every candidate must have been advanced between its SELECT and UPDATE")

		for _, aLeg := range raced {
			row := readSession(t, db, aLeg)
			assert.Equal(t, activity(17), row.LastActivityMS, "the foreign capture advanced %q", aLeg)
			assert.Equal(t, "", row.LeaseOwner, "the reconciled claim must not lease %q", aLeg)
			assert.Equal(t, int64(0), row.LeaseUntilMS)
			assert.Equal(t, int64(0), row.Attempts, "a reconciled claim must not count an attempt")
		}
	})
}

// contractFixture namespaces one subtest's rows on a shared database. Every key
// embeds the subtest scope and the run token, so cases cannot collide with one
// another, and cleanup can delete exactly the rows this fixture created.
type contractFixture struct {
	token    string
	projectP string
	projectQ string
	alice    proxymemory.Owner
	aLegs    []string
	seedALeg func(string)
}

// Existing scheduling fixtures order their captures by synthetic activity;
// translate that fixture order to positive sequence numbers. Causal-order
// regressions use the authoritative a_legs sequence instead of this
// convenience.
func testSourceSeq(activityMS int64) int64 { return (1 << 62) + activityMS }

func newContractFixture(scope, runID string) *contractFixture {
	f := &contractFixture{token: scope + "_" + runID}
	f.projectP = "project-p-" + f.token
	f.projectQ = "project-q-" + f.token
	f.alice = proxymemory.Owner{TenantID: "tenant-a", PrincipalID: "alice", ProjectKey: f.projectP}
	return f
}

// aLeg returns the fixture's A-leg key for name and records it for cleanup.
func (f *contractFixture) aLeg(name string) string {
	id := "a_leg_2_1_" + name + "_" + f.token
	f.aLegs = append(f.aLegs, id)
	if f.seedALeg != nil {
		f.seedALeg(id)
	}
	return id
}

// owner returns a probe owner in this fixture's namespace.
func (f *contractFixture) owner(principalID, projectKey string) proxymemory.Owner {
	return proxymemory.Owner{TenantID: f.alice.TenantID, PrincipalID: principalID, ProjectKey: projectKey}
}

// removeRows deletes exactly the rows this fixture created through the
// subtest's own opener handle: sessions, extractions and fence rows by A-leg
// ID, plus the fixture owner's banks and notes. Shared tables are never
// truncated and other runs' keys are never touched.
func (f *contractFixture) removeRows(t *testing.T, db *bun.DB) {
	t.Helper()
	ctx := context.Background()
	for _, aLeg := range f.aLegs {
		for _, query := range []string{
			`DELETE FROM proxy_memory_session_bindings WHERE a_leg_id = ?`,
			`DELETE FROM proxy_memory_sessions WHERE a_leg_id = ?`,
			`DELETE FROM proxy_memory_extractions WHERE a_leg_id = ?`,
			`DELETE FROM proxy_memory_extraction_ledger WHERE a_leg_id = ?`,
			`DELETE FROM proxy_memory_fence WHERE a_leg_id = ?`,
			`DELETE FROM a_legs WHERE a_leg_id = ?`,
		} {
			if _, err := db.NewRaw(query, aLeg).Exec(ctx); err != nil {
				t.Errorf("fixture cleanup %s: %v", query, err)
			}
		}
	}
	for _, projectKey := range []string{f.projectP, f.projectQ} {
		if _, err := db.NewRaw(`DELETE FROM proxy_memory_erasure_epochs WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
			f.alice.TenantID, f.alice.PrincipalID, projectKey).Exec(ctx); err != nil {
			t.Errorf("fixture cleanup erasure epoch %q: %v", projectKey, err)
		}
		for _, table := range []string{"proxy_memory_banks", "proxy_memory_notes", "proxy_memory_phase_failures"} {
			if _, err := db.NewRaw(
				`DELETE FROM `+table+` WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
				f.alice.TenantID, f.alice.PrincipalID, projectKey,
			).Exec(ctx); err != nil {
				t.Errorf("fixture cleanup %s %q: %v", table, projectKey, err)
			}
		}
	}
}

type storedSession struct {
	TenantID       string
	PrincipalID    string
	ProjectKey     string
	ProjectLabel   string
	Transcript     string
	Revision       int64
	LastActivityMS int64
	LeaseOwner     string
	LeaseUntilMS   int64
	Attempts       int64
	NextAttemptMS  int64
}

func readSession(t *testing.T, db *bun.DB, aLegID string) storedSession {
	t.Helper()
	var row storedSession
	err := db.NewRaw(`
		SELECT tenant_id, principal_id, project_key, project_label, transcript, revision,
		       last_activity_ms, lease_owner, lease_until_ms, attempts, next_attempt_ms
		FROM proxy_memory_sessions WHERE a_leg_id = ?
	`, aLegID).Scan(context.Background(),
		&row.TenantID, &row.PrincipalID, &row.ProjectKey, &row.ProjectLabel, &row.Transcript,
		&row.Revision, &row.LastActivityMS, &row.LeaseOwner, &row.LeaseUntilMS, &row.Attempts, &row.NextAttemptMS,
	)
	require.NoError(t, err, "session row %q must exist", aLegID)
	return row
}

func sessionCount(t *testing.T, db *bun.DB, aLegID string) int {
	t.Helper()
	var n int
	err := db.NewRaw(`SELECT count(*) FROM proxy_memory_sessions WHERE a_leg_id = ?`, aLegID).Scan(context.Background(), &n)
	require.NoError(t, err)
	return n
}

// countingCoreALegs counts the core liveness queries the retired-binding
// sweep issues and the size of every candidate batch it asks about.
type countingCoreALegs struct {
	proxymemorystore.CoreALegs
	mu      sync.Mutex
	calls   int
	batches []int
}

func (c *countingCoreALegs) ActiveALegs(ctx context.Context, aLegIDs []string) (map[string]bool, error) {
	c.mu.Lock()
	c.calls++
	c.batches = append(c.batches, len(aLegIDs))
	c.mu.Unlock()
	return c.CoreALegs.ActiveALegs(ctx, aLegIDs)
}

// snapshot returns the number of core liveness queries and their batch sizes.
func (c *countingCoreALegs) snapshot() (int, []int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, append([]int(nil), c.batches...)
}

func bindingCount(t *testing.T, db *bun.DB, aLegID string) int {
	t.Helper()
	var n int
	err := db.NewRaw(`SELECT count(*) FROM proxy_memory_session_bindings WHERE a_leg_id = ?`, aLegID).Scan(context.Background(), &n)
	require.NoError(t, err)
	return n
}

func seedActiveALeg(t *testing.T, db *bun.DB, aLegID string, nextBSeq int64) {
	t.Helper()
	ctx := context.Background()
	ensureALegTable(t, db)
	_, err := db.NewRaw(`INSERT INTO a_legs (a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, next_b_seq) VALUES (?, ?, 0, 0, ?)
		ON CONFLICT (a_leg_id) DO UPDATE SET next_b_seq = excluded.next_b_seq`, aLegID, aLegID, nextBSeq).Exec(ctx)
	require.NoError(t, err)
}

// bumpALeg advances the authoritative next B-leg sequence of one A-leg, which
// is what a later admitted request observes as its source order.
func bumpALeg(t *testing.T, db *bun.DB, aLegID string) {
	t.Helper()
	_, err := db.NewRaw(`UPDATE a_legs SET next_b_seq = next_b_seq + 1 WHERE a_leg_id = ?`, aLegID).
		Exec(context.Background())
	require.NoError(t, err)
}

// admissionSeq is the source sequence the request path derives for the next
// admitted turn of one A-leg: the authoritative next B-leg sequence plus one.
func admissionSeq(t *testing.T, db *bun.DB, aLegID string) int64 {
	t.Helper()
	var next int64
	require.NoError(t, db.NewRaw(`SELECT next_b_seq FROM a_legs WHERE a_leg_id = ?`, aLegID).
		Scan(context.Background(), &next))
	return next + 1
}

// seedBinding commits one first-turn decision, which is what creates an
// A-leg's binding row and its frozen decision in the current contract.
func seedBinding(t *testing.T, st *proxymemorystore.Store, p proxymemory.Principal, aLegID string, project proxymemory.Project) {
	t.Helper()
	committed, err := st.CommitFirstTurn(t.Context(), proxymemory.FirstTurnJob{
		Principal: p, ALegID: aLegID, Project: project,
		Decision:   proxymemory.FirstTurnDecision{Outcome: proxymemory.FirstTurnNoSummary},
		EpochKnown: true,
	})
	require.NoError(t, err)
	require.True(t, committed)
}

// seedBindingRow inserts the durable first-turn binding of one A-leg: the
// authoritative decision a capture needs before it may become memory. The
// decision is the owner's own no-summary outcome under epoch zero.
func seedBindingRow(t *testing.T, db *bun.DB, owner proxymemory.Owner, aLegID, projectKey, projectLabel string) {
	t.Helper()
	_, err := db.NewRaw(`INSERT INTO proxy_memory_session_bindings
		(a_leg_id, tenant_id, principal_id, project_key, project_label,
		 first_turn_outcome, first_turn_injection, first_turn_summary_version, first_turn_epoch)
		VALUES (?, ?, ?, ?, ?, 'no_summary', '', 0, 0)
		ON CONFLICT (a_leg_id) DO NOTHING`,
		aLegID, owner.TenantID, owner.PrincipalID, projectKey, projectLabel).Exec(context.Background())
	require.NoError(t, err)
}

// removeBindingRow deletes the durable first-turn binding of one A-leg, so a
// case can observe the unbound state.
func removeBindingRow(t *testing.T, db *bun.DB, aLegID string) {
	t.Helper()
	_, err := db.NewRaw(`DELETE FROM proxy_memory_session_bindings WHERE a_leg_id = ?`, aLegID).
		Exec(context.Background())
	require.NoError(t, err)
}

func ensureALegTable(t *testing.T, db *bun.DB) {
	t.Helper()
	// Use the real continuity schema so standalone SQLite and shared PostgreSQL
	// runs exercise the same required columns, regardless of component order.
	require.NoError(t, conversationview.EnsureSchema(context.Background(), db))
}

// storedFence is one proxy_memory_fence row.
type storedFence struct {
	TenantID      string
	PrincipalID   string
	ForgottenAtMS int64
}

func readFence(t *testing.T, db *bun.DB, aLegID string) storedFence {
	t.Helper()
	var row storedFence
	require.NoError(t, db.NewRaw(`
		SELECT tenant_id, principal_id, forgotten_at_ms FROM proxy_memory_fence WHERE a_leg_id = ?
	`, aLegID).Scan(context.Background(), &row.TenantID, &row.PrincipalID, &row.ForgottenAtMS))
	return row
}

func fenceCount(t *testing.T, db *bun.DB, aLegID string) int {
	t.Helper()
	var n int
	require.NoError(t, db.NewRaw(
		`SELECT count(*) FROM proxy_memory_fence WHERE a_leg_id = ?`, aLegID,
	).Scan(context.Background(), &n))
	return n
}

func seedExtraction(t *testing.T, db *bun.DB, o proxymemory.Owner, aLegID string) {
	t.Helper()
	seedExtractionRow(t, db, o, aLegID, "fix-tests", 2_000)
}

// seedExtractionRow inserts one extraction row with a chosen slug and
// generation time.
func seedExtractionRow(t *testing.T, db *bun.DB, o proxymemory.Owner, aLegID, slug string, generatedAtMS int64) {
	t.Helper()
	_, err := db.NewRaw(`
		INSERT INTO proxy_memory_extractions
			(a_leg_id, tenant_id, principal_id, project_key, session_ref, raw_memory, rollout_summary, rollout_slug, source_updated_ms, generated_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, aLegID, o.TenantID, o.PrincipalID, o.ProjectKey,
		"session-0123456789ab", "raw memory", "rollout summary", slug, 1_000, generatedAtMS,
	).Exec(context.Background())
	require.NoError(t, err)
}

// seedExtractionFull inserts one extraction row with every content and
// timestamp column under the caller's control.
func seedExtractionFull(t *testing.T, db *bun.DB, o proxymemory.Owner, aLegID, sessionRef, slug string, sourceUpdatedMS, generatedAtMS int64) {
	t.Helper()
	_, err := db.NewRaw(`
		INSERT INTO proxy_memory_extractions
			(a_leg_id, tenant_id, principal_id, project_key, session_ref, raw_memory, rollout_summary, rollout_slug, source_updated_ms, generated_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, aLegID, o.TenantID, o.PrincipalID, o.ProjectKey,
		sessionRef, "raw memory", "rollout summary", slug, sourceUpdatedMS, generatedAtMS,
	).Exec(context.Background())
	require.NoError(t, err)
}

func seedBank(t *testing.T, db *bun.DB, o proxymemory.Owner, version int64, summary string) {
	t.Helper()
	seedBankRow(t, db, o, bankSeed{ProjectLabel: "/work/one", Version: version, MemorySummary: summary, PublishedAtMS: 1_000})
}

// bankDirty is the fixture bank's pending-work size: larger than the
// pending-work size of any earlier-run residue on a shared database, so a
// fixture bank is the first candidate of the dirtiness ordering, and small
// enough that dirty_seq - consolidated_seq stays inside int64.
const bankDirty = int64(1) << 62

// bankSeed describes one seeded bank row; zero values keep the schema defaults.
type bankSeed struct {
	ProjectLabel      string
	Version           int64
	MemoryMD          string
	MemorySummary     string
	PublishedAtMS     int64
	ConsolidatedNames string
	DirtySeq          int64
	ConsolidatedSeq   int64
	LeaseOwner        string
	LeaseUntilMS      int64
	Attempts          int64
	NextAttemptMS     int64
}

// seedBankRow inserts one bank row with every scheduling and content column
// under the caller's control.
func seedBankRow(t *testing.T, db *bun.DB, o proxymemory.Owner, s bankSeed) {
	t.Helper()
	names := s.ConsolidatedNames
	if names == "" {
		names = "[]"
	}
	_, err := db.NewRaw(`
		INSERT INTO proxy_memory_banks
			(tenant_id, principal_id, project_key, project_label, version, memory_md, memory_summary,
			 published_at_ms, consolidated_names, dirty_seq, consolidated_seq,
			 lease_owner, lease_until_ms, attempts, next_attempt_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, o.TenantID, o.PrincipalID, o.ProjectKey, s.ProjectLabel, s.Version, s.MemoryMD,
		s.MemorySummary, s.PublishedAtMS, names, s.DirtySeq, s.ConsolidatedSeq,
		s.LeaseOwner, s.LeaseUntilMS, s.Attempts, s.NextAttemptMS).Exec(context.Background())
	require.NoError(t, err)
}

// seedNote inserts one explicit note for o.
func seedNote(t *testing.T, db *bun.DB, o proxymemory.Owner, noteID, text string, createdAtMS int64) {
	t.Helper()
	_, err := db.NewRaw(`
		INSERT INTO proxy_memory_notes (note_id, tenant_id, principal_id, project_key, note, created_at_ms)
		VALUES (?, ?, ?, ?, ?, ?)
	`, noteID, o.TenantID, o.PrincipalID, o.ProjectKey, text, createdAtMS).Exec(context.Background())
	require.NoError(t, err)
}

// cleanupFixtureOwner removes exactly the rows of an extra owner a case created
// in its own namespace (for example the foreign principal that proves
// isolation). It runs before the case's main fixture cleanup.
func cleanupFixtureOwner(t *testing.T, db *bun.DB, o proxymemory.Owner) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, query := range []string{
			`DELETE FROM proxy_memory_banks WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
			`DELETE FROM proxy_memory_notes WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
			`DELETE FROM proxy_memory_extraction_ledger WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
		} {
			if _, err := db.NewRaw(query, o.TenantID, o.PrincipalID, o.ProjectKey).Exec(ctx); err != nil {
				t.Errorf("fixture cleanup %s: %v", query, err)
			}
		}
	})
}

// upsertCapture stores one captured turn the way the capture writer would.
func upsertCapture(t *testing.T, st *proxymemorystore.Store, o proxymemory.Owner, aLegID, transcript string, activityMS int64) {
	t.Helper()
	outcome, err := st.UpsertSession(t.Context(), proxymemory.SessionCapture{
		Owner: o, ProjectLabel: "/work/one", ALegID: aLegID, Transcript: transcript, ActivityMS: activityMS, SourceSeq: testSourceSeq(activityMS),
	})
	require.NoError(t, err)
	require.Equal(t, proxymemory.UpsertStored, outcome)
}

// claimOneSession claims exactly one session of the current case: the row is
// scheduled at claimMS and the window ends at idleBeforeMS (see the isolation
// comment in runLeaseContract).
func claimOneSession(t *testing.T, st *proxymemorystore.Store, leaseOwner string, claimMS, leaseUntilMS, idleBeforeMS int64) proxymemory.ClaimedSession {
	t.Helper()
	claimed, err := st.ClaimSessions(t.Context(), proxymemory.ClaimQuery{
		NowMS: claimMS, IdleBeforeMS: idleBeforeMS, NotBeforeMS: 0, LeaseUntilMS: leaseUntilMS, LeaseOwner: leaseOwner, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1, "claim owner %q", leaseOwner)
	return claimed[0]
}

// scheduleSessionClaim moves aLegID's next-attempt stamp into the fixture's
// claim band (see the isolation comment in runLeaseContract) so its row becomes
// claimable exactly when a case claims inside that band.
func scheduleSessionClaim(t *testing.T, db *bun.DB, aLegID string, nextAttemptMS int64) {
	t.Helper()
	_, err := db.NewRaw(
		`UPDATE proxy_memory_sessions SET next_attempt_ms = ? WHERE a_leg_id = ?`,
		nextAttemptMS, aLegID,
	).Exec(context.Background())
	require.NoError(t, err)
}

// scheduleBankClaim moves an owner's bank next-attempt stamp into the fixture's
// claim band (see the isolation comment in runLeaseContract) so the bank
// becomes claimable exactly when a case claims inside that band. A publish
// resets next_attempt_ms to 0, so a case that re-claims a just-published bank
// re-schedules it here.
func scheduleBankClaim(t *testing.T, db *bun.DB, o proxymemory.Owner, nextAttemptMS int64) {
	t.Helper()
	_, err := db.NewRaw(
		`UPDATE proxy_memory_banks SET next_attempt_ms = ? WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
		nextAttemptMS, o.TenantID, o.PrincipalID, o.ProjectKey,
	).Exec(context.Background())
	require.NoError(t, err)
}

// extractionFor builds the phase 1 result a worker would submit for cs.
func extractionFor(cs proxymemory.ClaimedSession, generatedAtMS int64) proxymemory.Extraction {
	return proxymemory.Extraction{
		Owner:           cs.Owner,
		ALegID:          cs.ALegID,
		SessionRef:      "session-0123456789ab",
		RawMemory:       "raw memory",
		RolloutSummary:  "rollout summary",
		RolloutSlug:     "fix-tests",
		SourceUpdatedMS: cs.LastActivityMS,
		GeneratedAtMS:   generatedAtMS,
	}
}

// claimOneBank claims exactly one bank for the current case: the fixture bank
// is scheduled at nowMS and its retry time has already passed.
func claimOneBank(t *testing.T, st *proxymemorystore.Store, leaseOwner string, nowMS, leaseUntilMS int64) proxymemory.ClaimedBank {
	t.Helper()
	claimed, err := st.ClaimBanks(t.Context(), proxymemory.ClaimQuery{
		NowMS: nowMS, LeaseUntilMS: leaseUntilMS, LeaseOwner: leaseOwner, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	return claimed[0]
}

// extractionRef identifies one carried extraction revision.
func extractionRef(e proxymemory.Extraction) proxymemory.ExtractionRef {
	return proxymemory.ExtractionRef{ALegID: e.ALegID, Revision: e.Revision, GeneratedAtMS: e.GeneratedAtMS}
}

// publishAllExtractions acknowledges every outstanding extraction of o under
// the given lease window and returns the bank after the publication.
func publishAllExtractions(t *testing.T, db *bun.DB, st *proxymemorystore.Store, o proxymemory.Owner, nowMS, leaseUntilMS int64) storedBank {
	t.Helper()
	claimed := claimOneBank(t, st, "publish-all", nowMS, leaseUntilMS)
	batch, err := st.ConsolidationInput(t.Context(), claimed, 0, 1_000)
	require.NoError(t, err)
	publication := proxymemory.Publication{
		MemoryMD:      "md",
		MemorySummary: "summary",
		WindowSinceMS: 0,
		PublishedAtMS: nowMS + 1,
	}
	for _, e := range batch.Extractions {
		publication.Acknowledged = append(publication.Acknowledged, extractionRef(e))
		publication.ConsolidatedNames = append(publication.ConsolidatedNames, proxymemory.SummaryName(e))
	}
	require.NoError(t, st.PublishBank(t.Context(), claimed, publication))
	return mustBank(t, db, o)
}

// publishEverything builds a full publication for the two CAS-failure cases;
// its content never lands, so only its shape matters.
func publishEverything(prefix, noteID string) proxymemory.Publication {
	return proxymemory.Publication{
		MemoryMD:          prefix + " md",
		MemorySummary:     prefix + " summary",
		ConsolidatedNames: []string{prefix},
		ConsumedNoteIDs:   []string{noteID},
		WindowSinceMS:     0,
		PublishedAtMS:     8_000,
	}
}

type storedExtraction struct {
	TenantID         string
	PrincipalID      string
	ProjectKey       string
	SessionRef       string
	RawMemory        string
	RolloutSummary   string
	RolloutSlug      string
	SourceUpdatedMS  int64
	GeneratedAtMS    int64
	ConsolidatedAtMS int64
}

func readExtraction(t *testing.T, db *bun.DB, aLegID string) (storedExtraction, bool) {
	t.Helper()
	var row storedExtraction
	err := db.NewRaw(`
		SELECT tenant_id, principal_id, project_key, session_ref, raw_memory, rollout_summary,
		       rollout_slug, source_updated_ms, generated_at_ms, consolidated_at_ms
		FROM proxy_memory_extractions WHERE a_leg_id = ?
	`, aLegID).Scan(context.Background(),
		&row.TenantID, &row.PrincipalID, &row.ProjectKey, &row.SessionRef, &row.RawMemory,
		&row.RolloutSummary, &row.RolloutSlug, &row.SourceUpdatedMS, &row.GeneratedAtMS,
		&row.ConsolidatedAtMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedExtraction{}, false
	}
	require.NoError(t, err)
	return row, true
}

func extractionCount(t *testing.T, db *bun.DB, aLegID string) int {
	t.Helper()
	var n int
	require.NoError(t, db.NewRaw(
		`SELECT count(*) FROM proxy_memory_extractions WHERE a_leg_id = ?`, aLegID,
	).Scan(context.Background(), &n))
	return n
}

// ledgerCount returns the number of extraction-ledger participation rows for
// aLegID.
func ledgerCount(t *testing.T, db *bun.DB, aLegID string) int {
	t.Helper()
	var n int
	require.NoError(t, db.NewRaw(
		`SELECT count(*) FROM proxy_memory_extraction_ledger WHERE a_leg_id = ?`, aLegID,
	).Scan(context.Background(), &n))
	return n
}

// storedLedger is one proxy_memory_extraction_ledger row: the identity the
// A-leg's participation is fenced by and the source activity its latest
// extraction carried.
type storedLedger struct {
	TenantID         string
	PrincipalID      string
	ProjectKey       string
	SourceActivityMS int64
	RecordedAtMS     int64
}

func readLedgerRow(t *testing.T, db *bun.DB, aLegID string) (storedLedger, bool) {
	t.Helper()
	var row storedLedger
	err := db.NewRaw(`
		SELECT tenant_id, principal_id, project_key, source_activity_ms, recorded_at_ms
		FROM proxy_memory_extraction_ledger WHERE a_leg_id = ?
	`, aLegID).Scan(context.Background(),
		&row.TenantID, &row.PrincipalID, &row.ProjectKey, &row.SourceActivityMS, &row.RecordedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return storedLedger{}, false
	}
	require.NoError(t, err)
	return row, true
}

type storedBank struct {
	TenantID          string
	PrincipalID       string
	ProjectKey        string
	ProjectLabel      string
	Version           int64
	MemoryMD          string
	MemorySummary     string
	PublishedAtMS     int64
	ConsolidatedNames string
	DirtySeq          int64
	ConsolidatedSeq   int64
	LeaseOwner        string
	LeaseUntilMS      int64
	Attempts          int64
	NextAttemptMS     int64
}

func readBank(t *testing.T, db *bun.DB, o proxymemory.Owner) (storedBank, bool) {
	t.Helper()
	var row storedBank
	err := db.NewRaw(`
		SELECT tenant_id, principal_id, project_key, project_label, version, memory_md, memory_summary,
		       published_at_ms, consolidated_names, dirty_seq, consolidated_seq,
		       lease_owner, lease_until_ms, attempts, next_attempt_ms
		FROM proxy_memory_banks WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
	`, o.TenantID, o.PrincipalID, o.ProjectKey).Scan(context.Background(),
		&row.TenantID, &row.PrincipalID, &row.ProjectKey, &row.ProjectLabel, &row.Version, &row.MemoryMD,
		&row.MemorySummary, &row.PublishedAtMS, &row.ConsolidatedNames, &row.DirtySeq, &row.ConsolidatedSeq,
		&row.LeaseOwner, &row.LeaseUntilMS, &row.Attempts, &row.NextAttemptMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedBank{}, false
	}
	require.NoError(t, err)
	return row, true
}

// mustBank reads one bank row and requires it to exist.
func mustBank(t *testing.T, db *bun.DB, o proxymemory.Owner) storedBank {
	t.Helper()
	row, ok := readBank(t, db, o)
	require.True(t, ok, "bank row for %#v must exist", o)
	return row
}

// noteIDs returns o's note IDs ordered by creation time.
func noteIDs(t *testing.T, db *bun.DB, o proxymemory.Owner) []string {
	t.Helper()
	var ids []string
	require.NoError(t, db.NewRaw(`
		SELECT note_id FROM proxy_memory_notes
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
		ORDER BY created_at_ms, note_id
	`, o.TenantID, o.PrincipalID, o.ProjectKey).Scan(context.Background(), &ids))
	return ids
}

// rolloutSlugs lists the extraction slugs in result order.
func rolloutSlugs(extractions []proxymemory.Extraction) []string {
	slugs := make([]string, 0, len(extractions))
	for _, e := range extractions {
		slugs = append(slugs, e.RolloutSlug)
	}
	return slugs
}

// rolloutSummaryDocument renders the rollout-summary document content the
// store contract pins for design §12.6: the extraction's session ref, the
// bank's project label and the RFC-3339 form of its source-update time, then
// the summary.
func rolloutSummaryDocument(label, sessionRef string, sourceUpdatedMS int64, summary string) string {
	return "session: " + sessionRef + "\ncwd: " + label + "\nupdated_at: " +
		time.UnixMilli(sourceUpdatedMS).UTC().Format(time.RFC3339) + "\n\n" + summary
}

// noteIDsOf lists the note IDs in result order.
func noteIDsOf(notes []proxymemory.Note) []string {
	ids := make([]string, 0, len(notes))
	for _, n := range notes {
		ids = append(ids, n.NoteID)
	}
	return ids
}

// fittingPhase2Notes mirrors the worker's note selection for the residual-note
// progress cases (design §7.3): the oldest-first prefix of the owner's notes
// whose total text size stays within budgetBytes, stopping at the first note
// that does not fit — that note and every later one stay pending for a later
// consolidation.
func fittingPhase2Notes(notes []proxymemory.Note, budgetBytes int) []proxymemory.Note {
	sent := make([]proxymemory.Note, 0, len(notes))
	total := 0
	for _, note := range notes {
		if total+len(note.Text) > budgetBytes {
			break
		}
		total += len(note.Text)
		sent = append(sent, note)
	}
	return sent
}

// claimRaceHook manufactures one deterministic lost lease race inside a
// ClaimSessions call: right after the claim's candidate SELECT has returned, it
// leases the first candidate to a foreign instance through the same database,
// so the claim's conditional UPDATE loses the row and has to skip it. It also
// records every candidate SELECT, which lets the case pin the empty and
// non-empty skip-list query shapes without inspecting the implementation.
type claimRaceHook struct {
	stealALegID  string
	intruder     string
	leaseUntilMS int64

	raced      bool
	racedErr   error
	candidates []string
}

// BeforeQuery implements bun.QueryHook.
func (*claimRaceHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

// AfterQuery implements bun.QueryHook.
func (h *claimRaceHook) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	if !strings.Contains(event.Query, "SELECT a_leg_id FROM proxy_memory_sessions") {
		return
	}
	h.candidates = append(h.candidates, event.Query)
	if h.raced {
		return
	}
	h.raced = true
	_, h.racedErr = event.DB.NewRaw(`
		UPDATE proxy_memory_sessions SET lease_owner = ?, lease_until_ms = ? WHERE a_leg_id = ?
	`, h.intruder, h.leaseUntilMS, h.stealALegID).Exec(context.Background())
}

// claimWindowRaceHook manufactures the claim TOCTOU of the PostgreSQL
// reconciliation case: right after each candidate SELECT returns, a foreign
// capture of the row that SELECT picked advances its last_activity_ms past
// IdleBeforeMS through the real UpsertSession API, so the claim's conditional
// UPDATE must reconcile the complete candidate predicate and lease nothing.
// The candidate order is deterministic (last_activity ascending), so the k-th
// candidate SELECT raced the k-th entry of aLegs; the write runs in the
// claim's own goroutine, on the pool's other connection, before the UPDATE is
// sent — no sleep and no scheduler assumption.
type claimWindowRaceHook struct {
	owner        proxymemory.Owner
	projectLabel string
	aLegs        []string // the rows in their candidate order
	activeMS     int64    // the advancing capture's activity

	raced    int
	racedErr error
}

// BeforeQuery implements bun.QueryHook.
func (*claimWindowRaceHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

// AfterQuery implements bun.QueryHook.
func (h *claimWindowRaceHook) AfterQuery(_ context.Context, event *bun.QueryEvent) {
	if !strings.Contains(event.Query, "SELECT a_leg_id FROM proxy_memory_sessions") {
		return
	}
	if h.raced >= len(h.aLegs) {
		return
	}
	_, h.racedErr = proxymemorystore.New(event.DB, proxymemorystore.NewALegReader(event.DB)).UpsertSession(context.Background(), proxymemory.SessionCapture{
		Owner:        h.owner,
		ProjectLabel: h.projectLabel,
		ALegID:       h.aLegs[h.raced],
		Transcript:   `["raced"]`,
		ActivityMS:   h.activeMS, SourceSeq: testSourceSeq(h.activeMS),
	})
	h.raced++
}

// bankClaimGapBarrier parks the bank claim at BEGIN, after candidate selection
// and before the claim transaction acquires its connection. The competing
// failure then commits in the actual candidate/UPDATE gap on a single-connection
// SQLite as well as on PostgreSQL; the claim's epoch-first transaction
// revalidates the retry gate after the barrier is released. The selected flag
// is set by the candidate SELECT's AfterQuery and asserted by the test, so the
// barrier cannot move before selection without failing.
type bankClaimGapBarrier struct {
	marker   raceMarker
	reached  chan struct{}
	release  chan struct{}
	once     sync.Once
	selected bool
}

// BeforeQuery implements bun.QueryHook.
func (b *bankClaimGapBarrier) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if raceMarkerOf(ctx) != b.marker {
		return ctx
	}
	if strings.TrimSpace(event.Query) != "BEGIN" {
		return ctx
	}
	b.once.Do(func() {
		close(b.reached)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	})
	return ctx
}

// AfterQuery records that the candidate SELECT actually executed before the
// BEGIN barrier. The test fails if the barrier is moved before selection.
func (b *bankClaimGapBarrier) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	if raceMarkerOf(ctx) == b.marker && strings.Contains(event.Query, "SELECT tenant_id, principal_id, project_key FROM proxy_memory_banks") {
		b.selected = true
	}
}

// bankClaimGapDeadline bounds the bank claim's parked UPDATE and the claim
// goroutine that owns it, so a schedule that never resolves fails the case
// instead of hanging the suite.
const bankClaimGapDeadline = 10 * time.Second

// raceMarker tags one store call's context so only its statements pass through
// the interleaving hooks of the PostgreSQL bank-lock cases: the shared database
// handle sees every query of every call, and the marker keeps a hook attached
// to the call it parks rather than to whichever call runs first.
type raceMarker string

// raceMarkerKey is the private context key of a raceMarker.
type raceMarkerKey struct{}

const (
	raceMarkerAddNote   raceMarker = "add-note"
	raceMarkerForget    raceMarker = "forget"
	raceMarkerComplete  raceMarker = "complete-session"
	raceMarkerPurge     raceMarker = "purge"
	raceMarkerBankClaim raceMarker = "claim-bank"
)

// raceMarkerOf returns the marker of ctx, or "" for a call without one.
func raceMarkerOf(ctx context.Context) raceMarker {
	marker, _ := ctx.Value(raceMarkerKey{}).(raceMarker)
	return marker
}

// bankLockInterleave drives one deterministic AddNote/Forget interleaving of
// the PostgreSQL bank-lock contract: it parks the paused call's transaction
// right after pauseSQL and starts the opposite call on the same pool's second
// connection. The pause then ends on exactly one of two events, both observed
// through Bun query hooks:
//
//   - the opposite call issued lockSQL, its own bank lock. The paused call
//     already holds that row's lock, so the opposite call is blocked and
//     cannot commit before the paused transaction ends: the serialized order
//     under test;
//   - the opposite call committed while the paused transaction was parked,
//     which is only possible when nothing serializes the two — a store without
//     the bank lock, the order that let a note survive a successful Forget.
//
// The cases using it are gated to PostgreSQL: SQLite's single-writer engine
// cannot serve the second connection this harness parks behind the first.
type bankLockInterleave struct {
	pauseMarker raceMarker // marker of the call parked after pauseSQL
	pauseSQL    string     // substring of the statement the pause follows
	lockMarker  raceMarker // marker of the opposite call
	lockSQL     string     // substring of that call's bank lock statement

	// startOpposite starts the opposite call once and returns the channel that
	// receives its outcome. The hook's owner sets it before installing the hook.
	startOpposite func() <-chan error

	lockSignal chan struct{}
	lockOnce   sync.Once
	opposite   <-chan error

	ran          bool
	lockObserved bool
	oppositeErr  error
}

// BeforeQuery implements bun.QueryHook. It reports the opposite call's bank
// lock attempt, which happens before the statement is sent to the engine and
// therefore also when that statement then blocks on the paused transaction's
// lock.
func (h *bankLockInterleave) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if raceMarkerOf(ctx) == h.lockMarker && strings.Contains(event.Query, h.lockSQL) {
		h.lockOnce.Do(func() { close(h.lockSignal) })
	}
	return ctx
}

// AfterQuery implements bun.QueryHook. After the paused call ran pauseSQL it
// starts the opposite call and holds the paused transaction open until one of
// the two events described on the type is observed. The paused call's own
// statements keep flowing through this hook; only the pause statement is ever
// matched, and only once.
func (h *bankLockInterleave) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	if h.ran || raceMarkerOf(ctx) != h.pauseMarker || !strings.Contains(event.Query, h.pauseSQL) {
		return
	}
	h.ran = true
	h.opposite = h.startOpposite()
	select {
	case <-h.lockSignal:
		h.lockObserved = true
	case h.oppositeErr = <-h.opposite:
	}
}

// oppositeOutcome returns the opposite call's outcome. When the pause was
// released by the opposite call's bank lock — the serialized order — the paused
// call must end first, so this waits for the opposite call's commit; otherwise
// the pause already received the committed outcome.
func (h *bankLockInterleave) oppositeOutcome() error {
	if h.lockObserved {
		return <-h.opposite
	}
	return h.oppositeErr
}

// lockOrderDeadline bounds the two concurrent lock-order regressions: a
// schedule that keeps waiting — a transaction parked behind a lock that never
// releases — fails the case through the bounded context instead of hanging the
// suite. PostgreSQL's own deadlock detector aborts a true inversion long
// before this (deadlock_timeout is one second by default) and the case fails
// on that error, not on the deadline.
const lockOrderDeadline = 10 * time.Second

// lockOrderInterleave drives one deterministic Forget-vs-writer interleaving of
// the PostgreSQL lock-order regressions: it parks the other call's transaction
// right after pauseSQL (at that point the transaction holds the row lock that
// pauseSQL took), starts a Forget on the same pool's second connection, and
// resumes the parked transaction when that Forget reports triggerSQL. Bun's
// BeforeQuery hook sees a statement before it is sent to the engine, so the
// resumed transaction runs while the Forget is blocked at, or about to execute,
// the trigger statement — with the pre-reorder lock order that Forget already
// holds the bank row lock taken before the trigger statement, which closes the
// deadlock cycle the assertions reject. The pause also ends on the parked
// call's bounded context so a schedule that would never resolve fails the case
// instead of hanging it. The cases using it are gated to PostgreSQL: SQLite's
// single-writer engine cannot serve the second connection this harness parks
// behind the first.
type lockOrderInterleave struct {
	pauseMarker raceMarker // marker of the call parked after pauseSQL
	pauseSQL    string     // substring of the statement the pause follows
	triggerSQL  string     // substring of the Forget statement that ends the pause

	// startForget starts the Forget once, returning the channel that receives
	// its outcome. The hook's owner sets it before installing the hook.
	startForget func() <-chan error

	triggerSignal chan struct{} // created by the case before installing the hook
	triggerOnce   sync.Once
	forget        <-chan error

	ran             bool
	triggerObserved bool
	forgetErr       error
}

// BeforeQuery implements bun.QueryHook: it reports the Forget's trigger
// statement, which happens before the statement is sent to the engine and
// therefore also when that statement then blocks on the parked transaction's
// lock. The marker check runs first so only the Forget's own goroutine touches
// the sync.Once.
func (h *lockOrderInterleave) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if raceMarkerOf(ctx) == raceMarkerForget && strings.Contains(event.Query, h.triggerSQL) {
		h.triggerOnce.Do(func() { close(h.triggerSignal) })
	}
	return ctx
}

// AfterQuery implements bun.QueryHook: after the parked call ran pauseSQL it
// starts the Forget and holds the parked transaction open until the Forget
// reports triggerSQL, the Forget ends early (only an error can end it before
// the trigger), or the parked call's bounded context expires. The marker check
// runs first so the shared pause state is only touched by the parked call's
// own goroutine.
func (h *lockOrderInterleave) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	if raceMarkerOf(ctx) != h.pauseMarker {
		return
	}
	if h.ran || !strings.Contains(event.Query, h.pauseSQL) {
		return
	}
	h.ran = true
	h.forget = h.startForget()
	select {
	case <-h.triggerSignal:
		h.triggerObserved = true
	case h.forgetErr = <-h.forget:
	case <-ctx.Done():
	}
}

// forgetOutcome returns the Forget's outcome. When the pause ended on the
// trigger statement the parked call must end first, so the outcome is still
// pending here; the wait is bounded by the same context as the parked call and
// a deadline fails the case instead of hanging it.
func (h *lockOrderInterleave) forgetOutcome(ctx context.Context, t *testing.T) error {
	t.Helper()
	if h.forgetErr != nil {
		return h.forgetErr
	}
	select {
	case err := <-h.forget:
		return err
	case <-ctx.Done():
		t.Fatal("the concurrent forget did not finish inside the bounded context; a lock wait is not resolving")
		return nil
	}
}
