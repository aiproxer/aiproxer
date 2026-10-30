package proxymemorystore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// seedSessionRow inserts one claimable session row in the caller's fixture
// namespace.
func seedSessionRow(t *testing.T, db *bun.DB, o proxymemory.Owner, aLegID, projectLabel string, revision, lastActivityMS int64) {
	t.Helper()
	_, err := db.NewRaw(`
		INSERT INTO proxy_memory_sessions
			(a_leg_id, tenant_id, principal_id, project_key, project_label, transcript, revision, last_activity_ms, source_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, aLegID, o.TenantID, o.PrincipalID, o.ProjectKey, projectLabel,
		`[{"type":"message","role":"user","text":"hello"}]`, revision, lastActivityMS, testSourceSeq(lastActivityMS)).Exec(context.Background())
	require.NoError(t, err)
}

// failureRow reads one stored failure fact by its immutable operation key.
func failureRow(t *testing.T, db *bun.DB, storeID, callID, phase string) (owner proxymemory.Owner, kind string, epoch, completedAt, expiresAt, recordedAt int64, revision, seq, leaseUntil int64, leaseOwner string) {
	t.Helper()
	err := db.NewRaw(`
		SELECT tenant_id, principal_id, project_key, failure_kind, erasure_epoch,
		       completed_at_ms, expires_at_ms, recorded_at_ms,
		       claim_revision, claim_seq, lease_until_ms, lease_owner
		FROM proxy_memory_phase_failures
		WHERE store_id = ? AND billing_call_id = ? AND phase = ?
	`, storeID, callID, phase).Scan(context.Background(),
		&owner.TenantID, &owner.PrincipalID, &owner.ProjectKey, &kind, &epoch,
		&completedAt, &expiresAt, &recordedAt, &revision, &seq, &leaseUntil, &leaseOwner)
	require.NoError(t, err)
	return owner, kind, epoch, completedAt, expiresAt, recordedAt, revision, seq, leaseUntil, leaseOwner
}

func failureCount(t *testing.T, db *bun.DB, o proxymemory.Owner) int {
	t.Helper()
	var count int
	require.NoError(t, db.NewRaw(`
		SELECT COUNT(*) FROM proxy_memory_phase_failures
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
	`, o.TenantID, o.PrincipalID, o.ProjectKey).Scan(context.Background(), &count))
	return count
}

func sessionScheduling(t *testing.T, db *bun.DB, aLegID string) (owner proxymemory.Owner, leaseOwner string, leaseUntil, nextAttempt int64) {
	t.Helper()
	require.NoError(t, db.NewRaw(`
		SELECT tenant_id, principal_id, project_key, lease_owner, lease_until_ms, next_attempt_ms
		FROM proxy_memory_sessions WHERE a_leg_id = ?
	`, aLegID).Scan(context.Background(), &owner.TenantID, &owner.PrincipalID, &owner.ProjectKey, &leaseOwner, &leaseUntil, &nextAttempt))
	return owner, leaseOwner, leaseUntil, nextAttempt
}

func bankScheduling(t *testing.T, db *bun.DB, o proxymemory.Owner) (leaseOwner string, leaseUntil, nextAttempt, version, dirtySeq int64) {
	t.Helper()
	require.NoError(t, db.NewRaw(`
		SELECT lease_owner, lease_until_ms, next_attempt_ms, version, dirty_seq
		FROM proxy_memory_banks WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
	`, o.TenantID, o.PrincipalID, o.ProjectKey).Scan(context.Background(), &leaseOwner, &leaseUntil, &nextAttempt, &version, &dirtySeq))
	return leaseOwner, leaseUntil, nextAttempt, version, dirtySeq
}

func sessionEvidence(claim proxymemory.ClaimedSession, storeID, callID string, completedAt, expiresAt, recordedAt int64) proxymemory.FailureEvidence {
	return proxymemory.FailureEvidence{
		Owner:         claim.Owner,
		Phase:         proxymemory.FailurePhaseExtraction,
		StoreID:       storeID,
		BillingCallID: callID,
		ClaimID:       claim.ALegID,
		ClaimRevision: claim.Revision,
		LeaseOwner:    claim.LeaseOwner,
		LeaseUntilMS:  claim.LeaseUntilMS,
		ErasureEpoch:  claim.ErasureEpoch,
		RetryAtMS:     8_000,
		Kind:          proxymemory.FailureKindInvalidResult,
		CompletedAtMS: completedAt,
		ExpiresAtMS:   expiresAt,
		RecordedAtMS:  recordedAt,
	}
}

func runPhaseFailureContract(t *testing.T, open func(t *testing.T) *bun.DB, openObserver func(t *testing.T) *bun.DB, runID string) {
	t.Helper()
	openFixture := func(t *testing.T, f *contractFixture) (*bun.DB, *proxymemorystore.Store) {
		t.Helper()
		db := open(t)
		ensureALegTable(t, db)
		f.seedALeg = func(aLegID string) {
			seedActiveALeg(t, db, aLegID, 0)
			seedBindingRow(t, db, f.alice, aLegID, f.projectP, "/work/phase-failure")
		}
		t.Cleanup(func() { f.removeRows(t, db) })
		return db, proxymemorystore.New(db, proxymemorystore.NewALegReader(db))
	}

	t.Run("claim captures the erasure epoch and lease deadline", func(t *testing.T) {
		f := newContractFixture("failure_claim_snapshot", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("claim_snapshot")
		seedSessionRow(t, db, f.alice, aLeg, "/work/phase-failure", 3, 1_000)
		q := proxymemory.ClaimQuery{
			NowMS: 1_500, IdleBeforeMS: 1_200, NotBeforeMS: 0,
			LeaseUntilMS: 9_000, LeaseOwner: "generation-a", Limit: 1,
		}
		claimed, err := st.ClaimSessions(ctx, q)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		assert.Equal(t, int64(9_000), claimed[0].LeaseUntilMS, "the claim must capture the lease deadline it wrote")
		assert.Equal(t, int64(0), claimed[0].ErasureEpoch, "the claim must capture the owner's epoch under its lock")

		// A Forget advances the epoch and removes the row; a fresh session is
		// claimed at the new epoch even when its revision restarts at the same
		// value.
		require.NoError(t, st.Forget(ctx, f.alice, 2_000))
		seedSessionRow(t, db, f.alice, aLeg, "/work/phase-failure", 3, 2_500)
		q.NowMS, q.IdleBeforeMS, q.LeaseUntilMS = 3_000, 2_600, 12_000
		claimed, err = st.ClaimSessions(ctx, q)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		assert.Equal(t, int64(1), claimed[0].ErasureEpoch, "a post-Forget claim must carry the advanced epoch")
	})

	t.Run("one transaction records the fact and releases the same live claim", func(t *testing.T) {
		f := newContractFixture("failure_release", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("release")
		seedSessionRow(t, db, f.alice, aLeg, "/work/phase-failure", 4, 1_000)
		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: 1_500, IdleBeforeMS: 1_200, NotBeforeMS: 0,
			LeaseUntilMS: 9_000, LeaseOwner: "generation-a", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		session := claimed[0]
		evidence := sessionEvidence(session, "store-failure", "call-failure", 1_500, 7_500, 1_600)

		outcome, err := st.RecordPhaseFailure(ctx, evidence)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.FailureRecorded, outcome)

		owner, kind, epoch, completedAt, expiresAt, recordedAt, revision, seq, leaseUntil, leaseOwner := failureRow(t, db, "store-failure", "call-failure", proxymemory.FailurePhaseExtraction)
		assert.Equal(t, session.Owner, owner)
		assert.Equal(t, proxymemory.FailureKindInvalidResult, kind)
		assert.Equal(t, session.ErasureEpoch, epoch)
		assert.Equal(t, int64(1_500), completedAt)
		assert.Equal(t, int64(7_500), expiresAt)
		assert.Equal(t, int64(1_600), recordedAt)
		assert.Equal(t, session.Revision, revision)
		assert.Equal(t, int64(0), seq)
		assert.Equal(t, session.LeaseUntilMS, leaseUntil)
		assert.Equal(t, session.LeaseOwner, leaseOwner)

		gotOwner, leaseOwnerNow, leaseUntilNow, nextAttempt := sessionScheduling(t, db, aLeg)
		assert.Equal(t, session.Owner, gotOwner)
		assert.Empty(t, leaseOwnerNow, "the live claim must be released")
		assert.Zero(t, leaseUntilNow, "the live claim's deadline must be cleared")
		assert.Equal(t, int64(8_000), nextAttempt, "the retry schedule must be written in the same transaction")
	})

	t.Run("identical replay is a no-op and never clears a successor", func(t *testing.T) {
		f := newContractFixture("failure_replay", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("replay")
		seedSessionRow(t, db, f.alice, aLeg, "/work/phase-failure", 4, 1_000)
		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: 1_500, IdleBeforeMS: 1_200, NotBeforeMS: 0,
			LeaseUntilMS: 9_000, LeaseOwner: "generation-a", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		evidence := sessionEvidence(claimed[0], "store-replay", "call-replay", 1_500, 60_000, 1_600)
		outcome, err := st.RecordPhaseFailure(ctx, evidence)
		require.NoError(t, err)
		require.Equal(t, proxymemory.FailureRecorded, outcome)

		// A successor claims the same session under a new lease.
		successor, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: 8_500, IdleBeforeMS: 1_200, NotBeforeMS: 0,
			LeaseUntilMS: 20_000, LeaseOwner: "generation-b", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, successor, 1)

		replay := evidence
		replay.RecordedAtMS = 9_000 // first-write audit time is excluded from replay comparison
		outcome, err = st.RecordPhaseFailure(ctx, replay)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.FailureDuplicate, outcome)
		_, _, _, _, _, recordedAt, _, _, _, _ := failureRow(t, db, "store-replay", "call-replay", proxymemory.FailurePhaseExtraction)
		assert.Equal(t, int64(1_600), recordedAt, "a replay must not refresh the first-write time")

		_, leaseOwnerNow, leaseUntilNow, nextAttempt := sessionScheduling(t, db, aLeg)
		assert.Equal(t, "generation-b", leaseOwnerNow, "an identical replay must not clear the successor's lease")
		assert.Equal(t, int64(20_000), leaseUntilNow)
		assert.Equal(t, int64(8_000), nextAttempt, "an identical replay must not rewrite the retry schedule")

		conflict := evidence
		conflict.Owner.PrincipalID = "mallory"
		outcome, err = st.RecordPhaseFailure(ctx, conflict)
		assert.ErrorIs(t, err, proxymemory.ErrFailureConflict)
		assert.Empty(t, outcome)
		_, leaseOwnerNow, _, _ = sessionScheduling(t, db, aLeg)
		assert.Equal(t, "generation-b", leaseOwnerNow, "a conflict must not touch the live claim")
	})

	t.Run("stale evidence is immutable but cannot mutate a successor", func(t *testing.T) {
		f := newContractFixture("failure_stale", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("stale")
		seedSessionRow(t, db, f.alice, aLeg, "/work/phase-failure", 4, 1_000)
		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: 1_500, IdleBeforeMS: 1_200, NotBeforeMS: 0,
			LeaseUntilMS: 9_000, LeaseOwner: "generation-a", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)

		// A successor takes over before the late evidence is committed.
		_, err = db.NewRaw(`UPDATE proxy_memory_sessions SET lease_owner = ?, lease_until_ms = ? WHERE a_leg_id = ?`,
			"generation-b", int64(20_000), aLeg).Exec(ctx)
		require.NoError(t, err)

		outcome, err := st.RecordPhaseFailure(ctx, sessionEvidence(claimed[0], "store-stale", "call-stale", 1_500, 7_500, 1_600))
		require.NoError(t, err)
		assert.Equal(t, proxymemory.FailureRecordedStale, outcome)
		_, _, _, _, _, _, _, _, _, _ = failureRow(t, db, "store-stale", "call-stale", proxymemory.FailurePhaseExtraction)

		_, leaseOwnerNow, leaseUntilNow, nextAttempt := sessionScheduling(t, db, aLeg)
		assert.Equal(t, "generation-b", leaseOwnerNow)
		assert.Equal(t, int64(20_000), leaseUntilNow)
		assert.Zero(t, nextAttempt, "stale evidence must not schedule the successor")
	})

	t.Run("phase 2 releases only the claimed version and dirty sequence", func(t *testing.T) {
		f := newContractFixture("failure_bank_release", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		seedBankRow(t, db, f.alice, bankSeed{ProjectLabel: "/work/phase-failure", Version: 2, DirtySeq: 5, ConsolidatedSeq: 0})
		claimed, err := st.ClaimBanks(ctx, proxymemory.ClaimQuery{
			NowMS: 1_500, LeaseUntilMS: 9_000, LeaseOwner: "generation-a", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		bank := claimed[0]
		assert.Equal(t, int64(9_000), bank.LeaseUntilMS)
		assert.Equal(t, int64(0), bank.ErasureEpoch)
		evidence := proxymemory.FailureEvidence{
			Owner:         bank.Owner,
			Phase:         proxymemory.FailurePhaseConsolidation,
			StoreID:       "store-bank",
			BillingCallID: "call-bank",
			ClaimID:       bank.Owner.ProjectKey,
			ClaimRevision: bank.Version,
			ClaimSeq:      bank.DirtySeq,
			LeaseOwner:    bank.LeaseOwner,
			LeaseUntilMS:  bank.LeaseUntilMS,
			ErasureEpoch:  bank.ErasureEpoch,
			RetryAtMS:     8_000,
			Kind:          proxymemory.FailureKindInvalidResult,
			CompletedAtMS: 1_500,
			ExpiresAtMS:   7_500,
			RecordedAtMS:  1_600,
		}
		outcome, err := st.RecordPhaseFailure(ctx, evidence)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.FailureRecorded, outcome)
		leaseOwnerNow, leaseUntilNow, nextAttempt, version, dirtySeq := bankScheduling(t, db, f.alice)
		assert.Empty(t, leaseOwnerNow)
		assert.Zero(t, leaseUntilNow)
		assert.Equal(t, int64(8_000), nextAttempt)
		assert.Equal(t, int64(2), version)
		assert.Equal(t, int64(5), dirtySeq)

		// A new claim on the same bank; evidence for the old dirty sequence
		// must not clear the new claim.
		_, err = db.NewRaw(`UPDATE proxy_memory_banks SET dirty_seq = 6, lease_owner = ?, lease_until_ms = ? WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
			"generation-b", int64(20_000), f.alice.TenantID, f.alice.PrincipalID, f.alice.ProjectKey).Exec(ctx)
		require.NoError(t, err)
		stale := evidence
		stale.BillingCallID = "call-bank-stale"
		outcome, err = st.RecordPhaseFailure(ctx, stale)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.FailureRecordedStale, outcome)
		leaseOwnerNow, _, _, _, _ = bankScheduling(t, db, f.alice)
		assert.Equal(t, "generation-b", leaseOwnerNow)
	})

	t.Run("expired receipts write nothing and bounded purge removes expired facts", func(t *testing.T) {
		f := newContractFixture("failure_expiry", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("expiry")
		seedSessionRow(t, db, f.alice, aLeg, "/work/phase-failure", 4, 1_000)
		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: 1_500, IdleBeforeMS: 1_200, NotBeforeMS: 0,
			LeaseUntilMS: 9_000, LeaseOwner: "generation-a", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)

		expired := sessionEvidence(claimed[0], "store-expired", "call-expired", 1_000, 2_000, 3_000)
		outcome, err := st.RecordPhaseFailure(ctx, expired)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.FailureExpired, outcome)
		_, leaseOwnerNow, _, _ := sessionScheduling(t, db, aLeg)
		assert.Equal(t, "generation-a", leaseOwnerNow, "an expired receipt must not release the claim")
		assert.Zero(t, failureCount(t, db, f.alice))

		live := sessionEvidence(claimed[0], "store-purge", "call-purge", 1_000, 5_000, 1_500)
		outcome, err = st.RecordPhaseFailure(ctx, live)
		require.NoError(t, err)
		require.Equal(t, proxymemory.FailureRecorded, outcome)
		require.Equal(t, 1, failureCount(t, db, f.alice))

		deleted, err := st.Purge(ctx, 6_000, 0, 0, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, deleted, 1)
		assert.Zero(t, failureCount(t, db, f.alice), "the bounded purge must remove only the expired fixed-horizon fact")

		// A replay of the purged receipt cannot recreate the row: the frozen
		// expiry already passed, and a later configuration change cannot
		// revive it.
		replay := live
		replay.RecordedAtMS = 6_000
		outcome, err = st.RecordPhaseFailure(ctx, replay)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.FailureExpired, outcome)
		assert.Zero(t, failureCount(t, db, f.alice))
	})

	t.Run("expired failure purge honors the per-pass budget and stable order", func(t *testing.T) {
		f := newContractFixture("failure_purge_budget", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		base := proxymemory.FailureEvidence{
			Owner: f.alice, Phase: proxymemory.FailurePhaseExtraction,
			StoreID: "store-budget", ClaimID: "a_leg_budget",
			LeaseOwner: "generation-budget", LeaseUntilMS: 9_000,
			Kind:          proxymemory.FailureKindInvalidResult,
			CompletedAtMS: 1_000, ExpiresAtMS: 2_000, RecordedAtMS: 1_500,
		}
		for i := range 3 {
			e := base
			e.BillingCallID = fmt.Sprintf("call-budget-%d", i)
			seedFailureRow(t, db, f.alice, "store-budget", e.BillingCallID, e)
		}
		live := base
		live.BillingCallID = "call-budget-live"
		live.CompletedAtMS = 1_000
		live.ExpiresAtMS = 60_000
		live.RecordedAtMS = 1_500
		seedFailureRow(t, db, f.alice, "store-budget", live.BillingCallID, live)
		require.Equal(t, 4, failureCount(t, db, f.alice))

		deleted, err := st.Purge(ctx, 3_000, 0, 0, 0)
		require.NoError(t, err)
		assert.Zero(t, deleted, "a nonpositive failure budget must delete no expired facts")
		require.Equal(t, 4, failureCount(t, db, f.alice))

		remaining := []int{3, 2, 1}
		for _, want := range remaining {
			deleted, err = st.Purge(ctx, 3_000, 0, 0, 1)
			require.NoError(t, err)
			assert.Equal(t, 1, deleted, "one pass must delete at most the budget")
			assert.Equal(t, want, failureCount(t, db, f.alice), "remaining expired facts after one bounded pass")
			var liveCount int
			require.NoError(t, db.NewRaw(`SELECT COUNT(*) FROM proxy_memory_phase_failures WHERE billing_call_id = ?`, live.BillingCallID).Scan(ctx, &liveCount))
			assert.Equal(t, 1, liveCount, "the live fact must survive every bounded pass")
		}
		deleted, err = st.Purge(ctx, 3_000, 0, 0, 1)
		require.NoError(t, err)
		assert.Zero(t, deleted, "an exhausted expiry window must be a no-op")
	})

	t.Run("forget erases facts and refuses a delayed old-epoch insert", func(t *testing.T) {
		f := newContractFixture("failure_forget", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("forget")
		seedSessionRow(t, db, f.alice, aLeg, "/work/phase-failure", 4, 1_000)
		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: 1_500, IdleBeforeMS: 1_200, NotBeforeMS: 0,
			LeaseUntilMS: 9_000, LeaseOwner: "generation-a", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		evidence := sessionEvidence(claimed[0], "store-forget", "call-forget", 1_500, 7_500, 1_600)
		outcome, err := st.RecordPhaseFailure(ctx, evidence)
		require.NoError(t, err)
		require.Equal(t, proxymemory.FailureRecorded, outcome)
		require.Equal(t, 1, failureCount(t, db, f.alice))

		require.NoError(t, st.Forget(ctx, f.alice, 2_000))
		assert.Zero(t, failureCount(t, db, f.alice), "Forget must delete the owner's failure facts in the same transaction")

		delayed := sessionEvidence(claimed[0], "store-delayed", "call-delayed", 2_100, 8_000, 2_100)
		outcome, err = st.RecordPhaseFailure(ctx, delayed)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.FailureErased, outcome, "an old-epoch insert must be refused after Forget")
		assert.Zero(t, failureCount(t, db, f.alice))
	})

	t.Run("forget that wins after the insert lock still erases the fact", func(t *testing.T) {
		f := newContractFixture("failure_forget_interleave", runID)
		db, st := openFixture(t, f)
		if db.Dialect().Name() != dialect.PG {
			t.Skip("the two-writer interleave needs a second PostgreSQL connection")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		aLeg := f.aLeg("forget_interleave")
		seedSessionRow(t, db, f.alice, aLeg, "/work/phase-failure", 4, 1_000)
		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: 1_500, IdleBeforeMS: 1_200, NotBeforeMS: 0,
			LeaseUntilMS: 9_000, LeaseOwner: "generation-a", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		evidence := sessionEvidence(claimed[0], "store-interleave", "call-interleave", 1_500, 7_500, 1_600)

		barrier := &failureInsertBarrier{reached: make(chan struct{}), release: make(chan struct{})}
		db.AddQueryHook(barrier)
		type recordOutcome struct {
			outcome proxymemory.FailureOutcome
			err     error
		}
		recorded := make(chan recordOutcome, 1)
		go func() {
			outcome, err := st.RecordPhaseFailure(context.WithValue(ctx, failureInsertKey{}, true), evidence)
			recorded <- recordOutcome{outcome: outcome, err: err}
		}()
		select {
		case <-barrier.reached:
		case <-ctx.Done():
			t.Fatal("the failure insert never reached its statement")
		}

		forgotten := make(chan error, 1)
		go func() { forgotten <- st.Forget(ctx, f.alice, 2_000) }()
		// The insert transaction holds the epoch lock, so Forget cannot pass
		// its epoch bump until the insert commits. Releasing here lets both
		// finish without a sleep or a scheduler assumption.
		close(barrier.release)

		record := <-recorded
		require.NoError(t, record.err)
		assert.Equal(t, proxymemory.FailureRecorded, record.outcome)
		require.NoError(t, <-forgotten)
		assert.Zero(t, failureCount(t, db, f.alice), "Forget must erase a fact that committed just before it")
		var epoch int64
		require.NoError(t, db.NewRaw(`SELECT epoch FROM proxy_memory_erasure_epochs WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
			f.alice.TenantID, f.alice.PrincipalID, f.alice.ProjectKey).Scan(ctx, &epoch))
		assert.Equal(t, int64(1), epoch)

		// A delayed insert carrying the pre-Forget epoch is refused.
		delayed := evidence
		delayed.BillingCallID = "call-interleave-delayed"
		outcome, err := st.RecordPhaseFailure(ctx, delayed)
		require.NoError(t, err)
		assert.Equal(t, proxymemory.FailureErased, outcome)
		assert.Zero(t, failureCount(t, db, f.alice))
	})

	t.Run("a claim parked before its transaction cannot resurrect a forgotten session", func(t *testing.T) {
		f := newContractFixture("failure_claim_after_forget", runID)
		db, st := openFixture(t, f)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		aLeg := f.aLeg("claim_after_forget")
		seedSessionRow(t, db, f.alice, aLeg, "/work/phase-failure", 3, 1_000)

		barrier := &sessionClaimForgetBarrier{reached: make(chan struct{}), release: make(chan struct{})}
		db.AddQueryHook(barrier)
		type claimOutcome struct {
			claimed []proxymemory.ClaimedSession
			err     error
		}
		outcomes := make(chan claimOutcome, 1)
		go func() {
			claimed, err := st.ClaimSessions(
				context.WithValue(ctx, remediationClaimKey{}, true),
				proxymemory.ClaimQuery{NowMS: 1_500, IdleBeforeMS: 1_200, NotBeforeMS: 0, LeaseUntilMS: 9_000, LeaseOwner: "generation-a", Limit: 1},
			)
			outcomes <- claimOutcome{claimed: claimed, err: err}
		}()
		select {
		case <-barrier.reached:
		case <-ctx.Done():
			t.Fatal("the claim never reached BEGIN after candidate selection")
		}
		require.True(t, barrier.selected, "the candidate SELECT must have executed before the BEGIN barrier")

		// The claim is parked at BEGIN before it owns a transaction connection,
		// so the Forget can complete on the single SQLite connection as well as
		// on PostgreSQL. The claim then starts its epoch-first transaction and
		// must observe the advanced epoch and vanished row.
		require.NoError(t, st.Forget(ctx, f.alice, 2_000))
		close(barrier.release)

		outcome := <-outcomes
		require.NoError(t, outcome.err)
		assert.Empty(t, outcome.claimed, "a claim parked before the Forget must not capture the forgotten row")
		assert.Zero(t, sessionCount(t, db, aLeg))
		var epoch int64
		require.NoError(t, db.NewRaw(`SELECT epoch FROM proxy_memory_erasure_epochs WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
			f.alice.TenantID, f.alice.PrincipalID, f.alice.ProjectKey).Scan(ctx, &epoch))
		assert.Equal(t, int64(1), epoch)
	})

	t.Run("a PostgreSQL claim waits for an uncommitted epoch writer", func(t *testing.T) {
		f := newContractFixture("failure_claim_epoch_wait", runID)
		db, st := openFixture(t, f)
		if db.Dialect().Name() != dialect.PG {
			t.Skip("SQLite has one writer; the shared pre-transaction Forget proof above covers its ordering")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		aLeg := f.aLeg("claim_epoch_wait")
		seedSessionRow(t, db, f.alice, aLeg, "/work/phase-failure", 3, 1_000)
		_, err := db.NewRaw(`INSERT INTO proxy_memory_erasure_epochs (tenant_id, principal_id, project_key, epoch) VALUES (?, ?, ?, 0)`,
			f.alice.TenantID, f.alice.PrincipalID, f.alice.ProjectKey).Exec(ctx)
		require.NoError(t, err)

		// The contract pool is fully occupied by the held writer transaction and
		// the blocked claim, so the PostgreSQL-state observation needs its own
		// connection and must not compete with either.
		require.NotNil(t, openObserver, "the PostgreSQL epoch wait needs a dedicated observer opener")
		observer := openObserver(t)
		require.NotNil(t, observer)

		// Hold precisely Forget's first owner-epoch update uncommitted.
		writer, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		type claimOutcome struct {
			claimed []proxymemory.ClaimedSession
			err     error
		}
		outcomes := make(chan claimOutcome, 1)
		writerOpen := true
		claimStarted := false
		claimDone := false
		cancelClaim := func() {}
		// Registered before any assertion that can end the subtest: cancel and
		// join the bounded claimant, and only then release the epoch writer, so
		// the writer stays held through the claimant's entire attempt.
		t.Cleanup(func() {
			cancelClaim()
			if claimStarted && !claimDone {
				select {
				case <-outcomes:
				case <-time.After(5 * time.Second):
					t.Errorf("the claim did not return after its bounded attempt was cancelled")
				}
			}
			if writerOpen {
				if rbErr := writer.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
					t.Errorf("rollback the held epoch writer: %v", rbErr)
				}
			}
		})
		_, err = writer.NewRaw(`UPDATE proxy_memory_erasure_epochs SET epoch = epoch + 1 WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
			f.alice.TenantID, f.alice.PrincipalID, f.alice.ProjectKey).Exec(ctx)
		require.NoError(t, err)
		var writerPID int
		require.NoError(t, writer.NewRaw(`SELECT pg_backend_pid()`).Scan(ctx, &writerPID))

		barrier := &claimEpochWaitBarrier{reached: make(chan struct{})}
		db.AddQueryHook(barrier)
		claimCtx, cancelClaimCtx := context.WithTimeout(ctx, 750*time.Millisecond)
		cancelClaim = cancelClaimCtx
		claimStarted = true
		go func() {
			claimed, err := st.ClaimSessions(
				context.WithValue(claimCtx, remediationClaimKey{}, true),
				proxymemory.ClaimQuery{NowMS: 1_500, IdleBeforeMS: 1_200, NotBeforeMS: 0, LeaseUntilMS: 9_000, LeaseOwner: "generation-a", Limit: 1},
			)
			outcomes <- claimOutcome{claimed: claimed, err: err}
		}()
		select {
		case <-barrier.reached:
		case <-ctx.Done():
			t.Fatal("the claim never reached its epoch lock statement")
		}

		// Hook entry is only client-side arrival. Observe PostgreSQL itself:
		// while the claimant context is still live, the claimant must be
		// executing the owner-epoch statement and waiting on the held writer's
		// transaction. A cancellation-aware delay inside the hook never reaches
		// this state and fails here, not at a liveness watchdog.
		observedPID := 0
		for observedPID == 0 {
			if claimCtx.Err() != nil {
				t.Fatalf("no live epoch statement was observed executing at PostgreSQL before the bounded attempt expired")
			}
			var pid int
			scanErr := observer.NewRaw(`
				SELECT a.pid FROM pg_stat_activity a
				WHERE a.datname = current_database()
				  AND a.pid <> pg_backend_pid()
				  AND a.state = 'active'
				  AND a.wait_event_type = 'Lock'
				  AND ? = ANY (pg_blocking_pids(a.pid))
				  AND strpos(a.query, 'INSERT INTO proxy_memory_erasure_epochs') > 0
				LIMIT 1
			`, writerPID).Scan(ctx, &pid)
			if scanErr == nil {
				observedPID = pid
				break
			}
			require.ErrorIs(t, scanErr, sql.ErrNoRows, "observe the blocked epoch statement")
			select {
			case <-claimCtx.Done():
			case <-time.After(2 * time.Millisecond):
			}
		}
		require.NoError(t, claimCtx.Err(), "the epoch statement must be observed while the claimant context is live")
		require.NotZero(t, observedPID)

		// Keep the writer held through the claimant's entire bounded attempt.
		// A claim that succeeds, or that returns without the expected
		// cancellation, fails the fixture; releasing the writer is allowed only
		// after that attempt is over.
		select {
		case outcome := <-outcomes:
			claimDone = true
			require.Error(t, outcome.err,
				"the epoch writer must remain held until the claimant's bounded attempt expires")
			require.ErrorIs(t, claimCtx.Err(), context.DeadlineExceeded,
				"the bounded claimant attempt must expire before the writer is released")
			assert.Empty(t, outcome.claimed, "no claim may succeed while the epoch writer is uncommitted")
		case <-time.After(5 * time.Second):
			t.Fatal("the claimant neither timed out nor returned while the epoch writer was held")
		}
		require.NoError(t, writer.Rollback())
		writerOpen = false

		freshCtx, cancelFresh := context.WithTimeout(ctx, 5*time.Second)
		defer cancelFresh()
		after, err := st.ClaimSessions(freshCtx, proxymemory.ClaimQuery{
			NowMS: 1_500, IdleBeforeMS: 1_200, NotBeforeMS: 0,
			LeaseUntilMS: 9_000, LeaseOwner: "generation-b", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, after, 1, "after the writer releases, the still-eligible claim proceeds")
		assert.Equal(t, "generation-b", after[0].LeaseOwner)
	})

	t.Run("invalid evidence is refused before any write", func(t *testing.T) {
		f := newContractFixture("failure_invalid", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		evidence := proxymemory.FailureEvidence{
			Owner: f.alice, Phase: proxymemory.FailurePhaseExtraction,
			StoreID: "store-invalid", BillingCallID: "call-invalid",
			LeaseOwner: "generation-a", LeaseUntilMS: 9_000,
			Kind:          proxymemory.FailureKindInvalidResult,
			CompletedAtMS: 1_000, ExpiresAtMS: 2_000, RecordedAtMS: 1_500,
		}
		outcome, err := st.RecordPhaseFailure(ctx, evidence)
		require.ErrorIs(t, err, proxymemory.ErrFailureInvalid)
		assert.Empty(t, outcome)
		assert.Zero(t, failureCount(t, db, f.alice))
	})

	t.Run("a failed insert rolls back the claim release", func(t *testing.T) {
		f := newContractFixture("failure_rollback", runID)
		db, st := openFixture(t, f)
		ctx := context.Background()
		aLeg := f.aLeg("rollback")
		seedSessionRow(t, db, f.alice, aLeg, "/work/phase-failure", 4, 1_000)
		claimed, err := st.ClaimSessions(ctx, proxymemory.ClaimQuery{
			NowMS: 1_500, IdleBeforeMS: 1_200, NotBeforeMS: 0,
			LeaseUntilMS: 9_000, LeaseOwner: "generation-a", Limit: 1,
		})
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		evidence := sessionEvidence(claimed[0], "store-rollback", "call-rollback", 1_500, 7_500, 1_600)

		// Force the insert to fail inside the transaction: the fact exists in
		// another owner's row, so the unique operation key conflicts and the
		// whole transaction (including the claim release) must roll back.
		conflicting := evidence
		conflicting.ClaimRevision++
		seedFailureRow(t, db, f.alice, "store-rollback", "call-rollback", conflicting)
		outcome, err := st.RecordPhaseFailure(ctx, evidence)
		require.True(t, errors.Is(err, proxymemory.ErrFailureConflict), "conflict error = %v", err)
		assert.Empty(t, outcome)
		_, leaseOwnerNow, leaseUntilNow, nextAttempt := sessionScheduling(t, db, aLeg)
		assert.Equal(t, "generation-a", leaseOwnerNow, "the claim release must roll back with the failed insert")
		assert.Equal(t, int64(9_000), leaseUntilNow)
		assert.Zero(t, nextAttempt)
	})
}

// seedFailureRow inserts one raw failure fact, used to create a conflicting
// operation key.
func seedFailureRow(t *testing.T, db *bun.DB, o proxymemory.Owner, storeID, callID string, e proxymemory.FailureEvidence) {
	t.Helper()
	_, err := db.NewRaw(`
		INSERT INTO proxy_memory_phase_failures
			(store_id, billing_call_id, phase, tenant_id, principal_id, project_key, claim_id,
			 claim_revision, claim_seq, lease_owner, lease_until_ms, erasure_epoch,
			 failure_kind, completed_at_ms, expires_at_ms, recorded_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, storeID, callID, e.Phase, o.TenantID, o.PrincipalID, o.ProjectKey, e.ClaimID,
		e.ClaimRevision, e.ClaimSeq, e.LeaseOwner, e.LeaseUntilMS, e.ErasureEpoch,
		e.Kind, e.CompletedAtMS, e.ExpiresAtMS, e.RecordedAtMS).Exec(context.Background())
	require.NoError(t, err)
}

// failureInsertKey scopes the insert barrier to the one RecordPhaseFailure
// call of the interleaving case.
type failureInsertKey struct{}

// failureInsertBarrier parks one failure INSERT before it reaches the engine.
// The insert transaction already owns the owner epoch lock, so the competing
// Forget must wait for it to commit.
type failureInsertBarrier struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *failureInsertBarrier) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if ctx.Value(failureInsertKey{}) != true || !strings.Contains(event.Query, "INSERT INTO proxy_memory_phase_failures") {
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

func (*failureInsertBarrier) AfterQuery(context.Context, *bun.QueryEvent) {}

// remediationClaimKey scopes the claim barriers to this remediation test.
type remediationClaimKey struct{}

// sessionClaimForgetBarrier parks the claimant at BEGIN, after candidate and
// owner selection and before the claim transaction acquires a connection, so
// the competing Forget can commit on the single SQLite writer. The selected
// flag is set by the candidate SELECT's AfterQuery and asserted by the test;
// moving the barrier before selection fails rather than producing a vacuous
// "no candidate" result.
type sessionClaimForgetBarrier struct {
	reached  chan struct{}
	release  chan struct{}
	once     sync.Once
	selected bool
}

func (b *sessionClaimForgetBarrier) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if ctx.Value(remediationClaimKey{}) != true || strings.TrimSpace(event.Query) != "BEGIN" {
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

func (b *sessionClaimForgetBarrier) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	if ctx.Value(remediationClaimKey{}) == true && strings.Contains(event.Query, "SELECT a_leg_id FROM proxy_memory_sessions") {
		b.selected = true
	}
}

// claimEpochWaitBarrier signals that the claimant reached the epoch lock
// statement hook. It deliberately does not park: the test holds the writer
// uncommitted through the claimant's whole bounded attempt and requires the
// expected cancellation/no-success, rather than treating hook entry alone as a
// database lock-wait observation.
type claimEpochWaitBarrier struct {
	reached chan struct{}
	once    sync.Once
}

func (b *claimEpochWaitBarrier) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if ctx.Value(remediationClaimKey{}) != true || !strings.Contains(event.Query, "INSERT INTO proxy_memory_erasure_epochs") {
		return ctx
	}
	b.once.Do(func() { close(b.reached) })
	return ctx
}

func (*claimEpochWaitBarrier) AfterQuery(context.Context, *bun.QueryEvent) {}
