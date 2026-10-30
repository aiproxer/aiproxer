package proxymemorystore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// UpsertSession stores or replaces one session capture under its owner per
// design §12.2: a fenced A-leg yields UpsertForgotten; an A-leg without a
// durable first-turn binding for this owner yields UpsertUnbound; an A-leg
// already owned by another owner (session row or extraction row) yields
// UpsertOwnerMismatch and changes nothing; a capture that is not newer than
// the stored row yields UpsertStale without replacing content, but may advance
// release activity; otherwise the transcript is stored with revision 1, or the
// existing row's transcript and last activity are replaced with revision + 1
// and the scheduling state is cleared.
//
// One transaction validates the Forget fence and observed erasure epoch, reads
// the durable first-turn binding, then runs the extraction-owner check,
// stored-row and durable extraction-watermark probes, and the owner-scoped
// upsert. The upsert re-checks the live-row owner/source-sequence predicate;
// after a successful write, the ledger watermark is read again to close the
// completion/unblock window. Thus a capture cannot change owners, move source
// order behind an existing transcript, outlive its A-leg, or recreate content
// after Forget (Requirements 1.5, 3.1, 4.3; design §12.2). When the upsert's
// own predicate loses a race, the stored row and then ledger are read again so
// the outcome stays truthful.
//
// The core A-leg liveness is read before the transaction starts: the memory
// database is isolated from the core continuity database, so the read must not
// hold a core connection while a memory transaction is open. A capture that
// raced a core retirement is therefore dropped rather than refused inside the
// transaction; the retired-binding sweep removes the leftover binding.
func (s *Store) UpsertSession(ctx context.Context, c proxymemory.SessionCapture) (proxymemory.UpsertOutcome, error) {
	if c.SourceSeq <= 0 {
		return proxymemory.UpsertStale, errors.New("proxy-memory: capture requires a positive admission sequence")
	}
	state, err := s.core.ALegState(ctx, c.ALegID)
	if err != nil {
		return proxymemory.UpsertStored, err
	}
	if !state.Active {
		return proxymemory.UpsertStale, nil
	}
	outcome := proxymemory.UpsertStored
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Acquire the writer snapshot before any reads: a deferred
		// read-then-write transaction can become SQLITE_BUSY_SNAPSHOT when a
		// foreground continuity write commits between the fence probe and the
		// epoch lock. The epoch is the outer fence for Forget and this capture,
		// so taking it first preserves the same race ordering.
		currentEpoch, err := lockErasureEpochTx(ctx, tx, c.Owner.TenantID, c.Owner.PrincipalID, c.Owner.ProjectKey)
		if err != nil {
			return err
		}
		fenced, err := fenceExistsTx(ctx, tx, c.ALegID)
		if err != nil {
			return err
		}
		if fenced {
			outcome = proxymemory.UpsertForgotten
			return nil
		}
		if currentEpoch != c.ErasureEpoch {
			outcome = proxymemory.UpsertStale
			return nil
		}
		// The capture learns from an authoritative first-turn decision, so the
		// A-leg must carry the durable binding of that decision for this owner
		// under the current epoch. A capture whose decision never committed is
		// dropped, never repaired: a missing binding cannot be reconstructed
		// from a capture (design §9, §12.2).
		var binding admissionBindingRow
		bound, err := loadAdmissionBindingTx(ctx, tx, s.db.Dialect().Name(), c.ALegID, &binding)
		if err != nil {
			return err
		}
		if !bound {
			outcome = proxymemory.UpsertUnbound
			return nil
		}
		if !bindingOwnerMatches(binding, c.Owner) {
			outcome = proxymemory.UpsertOwnerMismatch
			return nil
		}
		if binding.DecisionOutcome == "" || binding.DecisionEpoch != currentEpoch {
			outcome = proxymemory.UpsertUnbound
			return nil
		}
		owner, extracted, err := extractionOwnerTx(ctx, tx, c.ALegID)
		if err != nil {
			return err
		}
		if extracted && owner != c.Owner {
			outcome = proxymemory.UpsertOwnerMismatch
			return nil
		}
		if err := touchCaptureActivityTx(ctx, tx, c); err != nil {
			return err
		}
		stored, found, err := sessionActivityTx(ctx, tx, c.ALegID)
		if err != nil {
			return err
		}
		if found {
			if stored.owner() != c.Owner {
				outcome = proxymemory.UpsertOwnerMismatch
				return nil
			}
			if c.SourceSeq <= stored.sourceSeq {
				outcome = proxymemory.UpsertStale
				return nil
			}
		}
		watermark, found, err := ledgerWatermarkTx(ctx, tx, c.ALegID)
		if err != nil {
			return err
		}
		if found {
			if watermark.owner() != c.Owner {
				outcome = proxymemory.UpsertOwnerMismatch
				return nil
			}
			if c.SourceSeq <= watermark.sourceSeq {
				outcome = proxymemory.UpsertStale
				return nil
			}
			// The durable activity watermark is monotone: a stale release or a
			// regressed clock never lowers the recorded activity, so the
			// session cannot become idle-eligible earlier than its newest
			// release.
			c.ActivityMS = max(c.ActivityMS, watermark.lastActivityMS, watermark.sourceActivityMS)
		}
		res, err := tx.NewRaw(`
			INSERT INTO proxy_memory_sessions
				(a_leg_id, tenant_id, principal_id, project_key, project_label, transcript, revision, last_activity_ms, source_seq)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)
			ON CONFLICT (a_leg_id) DO UPDATE SET
				transcript = excluded.transcript,
				revision = proxy_memory_sessions.revision + 1,
				last_activity_ms = CASE WHEN proxy_memory_sessions.last_activity_ms > excluded.last_activity_ms
					THEN proxy_memory_sessions.last_activity_ms ELSE excluded.last_activity_ms END,
				source_seq = excluded.source_seq,
				attempts = 0,
				next_attempt_ms = 0
			WHERE proxy_memory_sessions.tenant_id = excluded.tenant_id
				AND proxy_memory_sessions.principal_id = excluded.principal_id
				AND proxy_memory_sessions.project_key = excluded.project_key
				AND proxy_memory_sessions.source_seq < excluded.source_seq
		`, c.ALegID, c.Owner.TenantID, c.Owner.PrincipalID, c.Owner.ProjectKey, c.ProjectLabel, c.Transcript, c.ActivityMS, c.SourceSeq).Exec(ctx)
		if err != nil {
			return opErr("upsert session capture", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return opErr("session capture rows affected", err)
		}
		if affected == 0 {
			// A concurrent upsert or completion changed the row between the
			// probes and this write. Re-read both durable authorities so a row
			// removed by completion is still classified as stale when its
			// extraction watermark covers this capture.
			raced, found, err := sessionActivityTx(ctx, tx, c.ALegID)
			if err != nil {
				return err
			}
			if found {
				if raced.owner() != c.Owner {
					outcome = proxymemory.UpsertOwnerMismatch
					return nil
				}
				if c.SourceSeq <= raced.sourceSeq {
					outcome = proxymemory.UpsertStale
					return nil
				}
			}
			watermark, found, err := ledgerWatermarkTx(ctx, tx, c.ALegID)
			if err != nil {
				return err
			}
			if found && watermark.owner() != c.Owner {
				outcome = proxymemory.UpsertOwnerMismatch
			} else if found && c.SourceSeq <= watermark.sourceSeq {
				outcome = proxymemory.UpsertStale
			} else {
				outcome = proxymemory.UpsertOwnerMismatch
			}
			return nil
		}
		// A PostgreSQL INSERT can unblock after a concurrent completion commits
		// its deletion and watermark, even though this upsert's statement
		// snapshot predates that commit. Recheck the durable watermark before
		// accepting the row. Delete only this activity's row so a newer capture
		// that raced after our write is never removed here.
		watermark, found, err = ledgerWatermarkTx(ctx, tx, c.ALegID)
		if err != nil {
			return err
		}
		if found && watermark.owner() != c.Owner {
			if err := deleteCaptureIfMatchesTx(ctx, tx, c); err != nil {
				return err
			}
			outcome = proxymemory.UpsertOwnerMismatch
			return nil
		}
		if found && c.SourceSeq <= watermark.sourceSeq {
			if err := deleteCaptureIfMatchesTx(ctx, tx, c); err != nil {
				return err
			}
			outcome = proxymemory.UpsertStale
			return nil
		}
		outcome = proxymemory.UpsertStored
		return nil
	})
	if err != nil {
		return proxymemory.UpsertStored, err
	}
	return outcome, nil
}

// touchCaptureActivityTx records successful releases independently of content
// order. Changing activity invalidates an in-flight revision snapshot without
// replacing its transcript/source sequence or stealing its lease. No row is
// inserted: stale content already extracted cannot resurrect a session.
//
// The durable extraction ledger carries the monotone activity watermark of the
// A-leg. A release that arrives after the extraction deleted the session row
// still advances that watermark, so a later capture with a regressed clock
// cannot make the session idle-eligible earlier than its newest release. The
// ledger's exact extracted source metadata (source_activity_ms) is untouched:
// the watermark is a separate column, never a repurpose of the source.
func touchCaptureActivityTx(ctx context.Context, tx bun.Tx, c proxymemory.SessionCapture) error {
	_, err := tx.NewRaw(`UPDATE proxy_memory_sessions
		SET last_activity_ms = ?, revision = revision + 1, attempts = 0, next_attempt_ms = 0
		WHERE a_leg_id = ? AND tenant_id = ? AND principal_id = ? AND project_key = ?
			AND source_seq >= ? AND last_activity_ms < ?`, c.ActivityMS,
		c.ALegID, c.Owner.TenantID, c.Owner.PrincipalID, c.Owner.ProjectKey, c.SourceSeq, c.ActivityMS).Exec(ctx)
	if err != nil {
		return opErr("record capture release activity", err)
	}
	_, err = tx.NewRaw(`UPDATE proxy_memory_extraction_ledger
		SET last_activity_ms = CASE WHEN last_activity_ms < ? THEN ? ELSE last_activity_ms END
		WHERE a_leg_id = ? AND tenant_id = ? AND principal_id = ? AND project_key = ?`,
		c.ActivityMS, c.ActivityMS, c.ALegID, c.Owner.TenantID, c.Owner.PrincipalID, c.Owner.ProjectKey).Exec(ctx)
	if err != nil {
		return opErr("record capture release watermark", err)
	}
	return nil
}

// ClaimSessions claims up to q.Limit idle sessions under q.LeaseOwner per
// design §12.3: a candidate SELECT finds the oldest claimable row, then one
// transaction locks the owner erasure epoch first (lockErasureEpochTx), revalidates
// the complete candidate predicate and owner columns in a conditional UPDATE,
// and returns the transcript snapshot with the epoch and lease deadline. The
// epoch lock is the common serialization authority with Forget; a session that
// left the window or was forgotten between the SELECT and the UPDATE is skipped
// exactly like a lost race instead of being leased with a stale candidacy. A
// lost race adds the candidate to the skip list, which only renders as an
// a_leg_id NOT IN (…) clause when it is non-empty because an empty IN () is
// invalid SQL on PostgreSQL.
func (s *Store) ClaimSessions(ctx context.Context, q proxymemory.ClaimQuery) ([]proxymemory.ClaimedSession, error) {
	if q.Limit <= 0 {
		return nil, nil
	}
	var claimed []proxymemory.ClaimedSession
	skipped := make([]string, 0, 1)
	for range q.Limit {
		candidate, found, err := s.sessionCandidate(ctx, q, skipped)
		if err != nil {
			return nil, err
		}
		if !found {
			break
		}
		candidate, ownerFound, err := s.sessionCandidateOwner(ctx, candidate.aLegID)
		if err != nil {
			return nil, err
		}
		if !ownerFound {
			// The candidate vanished between the SELECT and the owner lookup.
			skipped = append(skipped, candidate.aLegID)
			continue
		}
		cs := proxymemory.ClaimedSession{
			ALegID:       candidate.aLegID,
			LeaseOwner:   q.LeaseOwner,
			LeaseUntilMS: q.LeaseUntilMS,
		}
		err = s.claimSessionTx(ctx, candidate, q, &cs)
		if errors.Is(err, sql.ErrNoRows) {
			// Another instance won the row between the SELECT and the UPDATE,
			// or the row left the candidate window in that gap.
			skipped = append(skipped, candidate.aLegID)
			continue
		}
		if err != nil {
			return nil, opErr("claim session snapshot", err)
		}
		claimed = append(claimed, cs)
	}
	return claimed, nil
}

// claimSessionTx runs the approved epoch-first claim transaction: the owner
// erasure epoch (lockErasureEpochTx) is the first SQL statement, the complete
// candidate predicate and owner columns are revalidated in the conditional
// UPDATE, and the returned transcript snapshot plus the observed epoch and
// lease deadline all belong to that one transaction. A Forget that wins the
// transaction boundary makes the epoch lock wait or the eligibility update
// match no row; no epoch is observed only after dispatch.
func (s *Store) claimSessionTx(ctx context.Context, candidate sessionClaimCandidate, q proxymemory.ClaimQuery, cs *proxymemory.ClaimedSession) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		epoch, err := lockErasureEpochTx(ctx, tx, candidate.tenantID, candidate.principalID, candidate.projectKey)
		if err != nil {
			return err
		}
		err = tx.NewRaw(`
			UPDATE proxy_memory_sessions
			SET lease_owner = ?, lease_until_ms = ?, attempts = attempts + 1
			WHERE a_leg_id = ?
				AND tenant_id = ? AND principal_id = ? AND project_key = ?
				AND last_activity_ms <= ?
				AND last_activity_ms >= ?
				AND next_attempt_ms <= ?
				AND (lease_owner = '' OR lease_until_ms < ?)
			RETURNING tenant_id, principal_id, project_key, project_label, transcript, revision, last_activity_ms, source_seq
		`, q.LeaseOwner, q.LeaseUntilMS, candidate.aLegID,
			candidate.tenantID, candidate.principalID, candidate.projectKey,
			q.IdleBeforeMS, q.NotBeforeMS, q.NowMS, q.NowMS).Scan(ctx,
			&cs.Owner.TenantID, &cs.Owner.PrincipalID, &cs.Owner.ProjectKey, &cs.ProjectLabel,
			&cs.Transcript, &cs.Revision, &cs.LastActivityMS, &cs.SourceSeq)
		if err != nil {
			return err
		}
		cs.ErasureEpoch = epoch
		cs.LeaseUntilMS = q.LeaseUntilMS
		return nil
	})
}

// sessionClaimCandidate names the owner columns of one candidate row so the
// claim transaction can lock the correct erasure epoch before revalidating the
// row. The candidate read itself grants no authority.
type sessionClaimCandidate struct {
	aLegID      string
	tenantID    string
	principalID string
	projectKey  string
}

// sessionCandidate runs design §12.3 step 1: the oldest session inside the
// idle window whose retry time has passed and whose lease is free or expired.
func (s *Store) sessionCandidate(ctx context.Context, q proxymemory.ClaimQuery, skip []string) (sessionClaimCandidate, bool, error) {
	query := `SELECT a_leg_id FROM proxy_memory_sessions
WHERE last_activity_ms <= ? AND last_activity_ms >= ? AND next_attempt_ms <= ?
  AND (lease_owner = '' OR lease_until_ms < ?)`
	args := []any{q.IdleBeforeMS, q.NotBeforeMS, q.NowMS, q.NowMS}
	if len(skip) > 0 {
		query += ` AND a_leg_id NOT IN (` + placeholders(len(skip)) + `)`
		for _, aLegID := range skip {
			args = append(args, aLegID)
		}
	}
	query += ` ORDER BY last_activity_ms ASC, a_leg_id ASC LIMIT 1`

	var aLegID string
	err := s.db.NewRaw(query, args...).Scan(ctx, &aLegID)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionClaimCandidate{}, false, nil
	}
	if err != nil {
		return sessionClaimCandidate{}, false, opErr("select claimable session", err)
	}
	return sessionClaimCandidate{aLegID: aLegID}, true, nil
}

// sessionCandidateOwner resolves the owner columns of a candidate row. It is a
// correlation lookup only: the conditional claim still revalidates the owner
// columns transactionally, so a vanished or relabelled candidate is skipped
// rather than claimed under an inferred owner.
func (s *Store) sessionCandidateOwner(ctx context.Context, aLegID string) (sessionClaimCandidate, bool, error) {
	candidate := sessionClaimCandidate{aLegID: aLegID}
	err := s.db.NewRaw(`
		SELECT tenant_id, principal_id, project_key FROM proxy_memory_sessions WHERE a_leg_id = ?
	`, aLegID).Scan(ctx, &candidate.tenantID, &candidate.principalID, &candidate.projectKey)
	if errors.Is(err, sql.ErrNoRows) {
		return candidate, false, nil
	}
	if err != nil {
		return candidate, false, opErr("read claim candidate owner", err)
	}
	return candidate, true, nil
}

// CompleteSession stores e for the claimed session per design §12.4. One
// transaction runs the lease/owner row lock (SELECT … FOR UPDATE on
// PostgreSQL), the fence check, the owner-scoped extraction upsert, the bank
// dirty marking and the claimed-revision delete.
//
// The extraction row is keyed by the lease-verified claim (cs), so a
// mismatched Extraction argument can never write across owners. Failure
// mapping: a missing or foreign-owned lease row is ErrLeaseLost; a fence row
// deletes the session row and reports ErrLeaseLost with nothing stored; an
// existing extraction row owned by someone else reports ErrLeaseLost and rolls
// back. A newer upsert that changed the revision while the extraction ran
// keeps the session row and clears its lease — that mismatch is a success, not
// ErrLeaseLost (Requirements 4.3, 4.4).
func (s *Store) CompleteSession(ctx context.Context, cs proxymemory.ClaimedSession, e proxymemory.Extraction) error {
	if cs.SourceSeq <= 0 || e.SourceUpdatedMS != cs.LastActivityMS {
		return proxymemory.ErrLeaseLost
	}
	fenced := false
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := s.lockSessionLeaseTx(ctx, tx, cs); err != nil {
			return err
		}
		hasFence, err := fenceExistsTx(ctx, tx, cs.ALegID)
		if err != nil {
			return err
		}
		if hasFence {
			// Requirement 8.6: the fenced row can never become memory. The
			// delete commits before ErrLeaseLost is reported so the caller can
			// rely on the row being gone.
			if _, err := tx.NewRaw(`DELETE FROM proxy_memory_sessions WHERE a_leg_id = ?`, cs.ALegID).Exec(ctx); err != nil {
				return opErr("delete fenced session", err)
			}
			fenced = true
			return nil
		}
		if err := extractionUpsertTx(ctx, tx, cs, e); err != nil {
			return err
		}
		if err := bankDirtyTx(ctx, tx, cs); err != nil {
			return err
		}
		return deleteClaimedSessionTx(ctx, tx, cs)
	})
	if err != nil {
		return err
	}
	if fenced {
		return proxymemory.ErrLeaseLost
	}
	return nil
}

// lockSessionLeaseTx runs design §12.4 step 1: the row must still exist with
// the claimed lease owner and owner columns — which also covers a concurrent
// forget — and on PostgreSQL it is locked for the rest of the transaction so a
// concurrent CompleteSession or FailSession for the same lease cannot
// interleave.
func (s *Store) lockSessionLeaseTx(ctx context.Context, tx bun.Tx, cs proxymemory.ClaimedSession) error {
	query := `
		SELECT revision FROM proxy_memory_sessions
		WHERE a_leg_id = ? AND lease_owner = ? AND tenant_id = ? AND principal_id = ? AND project_key = ?`
	if s.db.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	// Step 5 keys the delete on the revision claimed at ClaimSessions time, so
	// the freshly read value only proves the row is still there and locks it.
	var lockedRevision int64
	err := tx.NewRaw(query, cs.ALegID, cs.LeaseOwner, cs.Owner.TenantID, cs.Owner.PrincipalID, cs.Owner.ProjectKey).Scan(ctx, &lockedRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return proxymemory.ErrLeaseLost
	}
	if err != nil {
		return opErr("lock session lease", err)
	}
	return nil
}

// extractionUpsertTx runs design §12.4 step 3 under the lease-verified claim
// identity of cs. RowsAffected == 0 means an existing extraction row belongs to
// another owner: the caller rolls back and reports ErrLeaseLost. A successful
// write also records the A-leg's durable participation in
// proxy_memory_extraction_ledger, in this same transaction: Purge deletes the
// extraction row later, and the ledger both keeps the A-leg fenced by a
// Forget after that (Requirement 8.6) and retains the latest extraction's
// source sequence for stale-capture checks (Requirements 3.1, 4.3). The ledger
// owner is immutable; the owner predicate above already proved it is this
// owner's.
func extractionUpsertTx(ctx context.Context, tx bun.Tx, cs proxymemory.ClaimedSession, e proxymemory.Extraction) error {
	res, err := tx.NewRaw(`
		INSERT INTO proxy_memory_extractions
			(a_leg_id, tenant_id, principal_id, project_key, session_ref, raw_memory, rollout_summary, rollout_slug, source_updated_ms, generated_at_ms, source_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (a_leg_id) DO UPDATE SET
			session_ref = excluded.session_ref,
			raw_memory = excluded.raw_memory,
			rollout_summary = excluded.rollout_summary,
			rollout_slug = excluded.rollout_slug,
			source_updated_ms = excluded.source_updated_ms,
			source_seq = excluded.source_seq,
			generated_at_ms = excluded.generated_at_ms,
			consolidated_at_ms = 0,
			extraction_revision = proxy_memory_extractions.extraction_revision + 1
			WHERE proxy_memory_extractions.tenant_id = excluded.tenant_id
				AND proxy_memory_extractions.principal_id = excluded.principal_id
				AND proxy_memory_extractions.project_key = excluded.project_key
				AND proxy_memory_extractions.source_seq <= excluded.source_seq
	`, cs.ALegID, cs.Owner.TenantID, cs.Owner.PrincipalID, cs.Owner.ProjectKey,
		e.SessionRef, e.RawMemory, e.RolloutSummary, e.RolloutSlug, e.SourceUpdatedMS, e.GeneratedAtMS, cs.SourceSeq).Exec(ctx)
	if err != nil {
		return opErr("upsert extraction", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return opErr("upsert extraction rows affected", err)
	}
	if affected == 0 {
		return proxymemory.ErrLeaseLost
	}
	ledgerRes, err := tx.NewRaw(`
		INSERT INTO proxy_memory_extraction_ledger
			(a_leg_id, tenant_id, principal_id, project_key, recorded_at_ms, source_activity_ms, last_activity_ms, source_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (a_leg_id) DO UPDATE SET
			source_activity_ms = excluded.source_activity_ms,
			source_seq = excluded.source_seq,
			recorded_at_ms = excluded.recorded_at_ms,
			last_activity_ms = CASE WHEN proxy_memory_extraction_ledger.last_activity_ms < excluded.last_activity_ms
				THEN excluded.last_activity_ms ELSE proxy_memory_extraction_ledger.last_activity_ms END
		WHERE proxy_memory_extraction_ledger.tenant_id = excluded.tenant_id
			AND proxy_memory_extraction_ledger.principal_id = excluded.principal_id
			AND proxy_memory_extraction_ledger.project_key = excluded.project_key
			AND proxy_memory_extraction_ledger.source_seq <= excluded.source_seq
	`, cs.ALegID, cs.Owner.TenantID, cs.Owner.PrincipalID, cs.Owner.ProjectKey,
		e.GeneratedAtMS, e.SourceUpdatedMS, e.SourceUpdatedMS, cs.SourceSeq).Exec(ctx)
	if err != nil {
		return opErr("record extraction participation", err)
	}
	ledgerAffected, err := ledgerRes.RowsAffected()
	if err != nil {
		return opErr("extraction participation rows affected", err)
	}
	if ledgerAffected == 0 {
		return proxymemory.ErrLeaseLost
	}
	return nil
}

// bankDirtyTx runs design §12.4 step 4: the owner's bank is created on the
// first extraction (dirty_seq 1) or marked dirty again, which is what makes the
// next consolidation pick it up.
func bankDirtyTx(ctx context.Context, tx bun.Tx, cs proxymemory.ClaimedSession) error {
	_, err := tx.NewRaw(`
		INSERT INTO proxy_memory_banks (tenant_id, principal_id, project_key, project_label, dirty_seq)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT (tenant_id, principal_id, project_key) DO UPDATE SET
			dirty_seq = proxy_memory_banks.dirty_seq + 1
	`, cs.Owner.TenantID, cs.Owner.PrincipalID, cs.Owner.ProjectKey, cs.ProjectLabel).Exec(ctx)
	if err != nil {
		return opErr("dirty bank", err)
	}
	return nil
}

// deleteClaimedSessionTx runs design §12.4 step 5. When a newer turn arrived
// while the extraction ran, the claimed revision no longer matches: the row is
// kept and its lease cleared so it can be claimed again once idle again
// (Requirement 4.4).
func deleteClaimedSessionTx(ctx context.Context, tx bun.Tx, cs proxymemory.ClaimedSession) error {
	res, err := tx.NewRaw(`DELETE FROM proxy_memory_sessions WHERE a_leg_id = ? AND revision = ?`, cs.ALegID, cs.Revision).Exec(ctx)
	if err != nil {
		return opErr("delete extracted session", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return opErr("delete extracted session rows affected", err)
	}
	if affected > 0 {
		return nil
	}
	if _, err := tx.NewRaw(`
		UPDATE proxy_memory_sessions
		SET lease_owner = '', lease_until_ms = 0, attempts = 0, next_attempt_ms = 0
		WHERE a_leg_id = ?
	`, cs.ALegID).Exec(ctx); err != nil {
		return opErr("clear extracted session lease", err)
	}
	return nil
}

// FailSession clears the session's lease and schedules its next attempt per
// design §12.4. The settlement is revision-aware: the retry gate belongs to
// the revision the caller claimed. A newer stored revision carries its own
// transcript and its own idle window, so the obsolete lease is released
// without gating it — the failed extraction of an older revision must not
// postpone a newer, healthy transcript. The lease-owner predicate keeps a
// stale caller from clearing a fresh lease taken by another instance, and the
// owner columns keep the settlement inside the owner's row; a miss is not an
// error because there is no result to discard.
func (s *Store) FailSession(ctx context.Context, cs proxymemory.ClaimedSession, retryAtMS int64) error {
	_, err := s.db.NewRaw(`
		UPDATE proxy_memory_sessions
		SET lease_owner = '', lease_until_ms = 0,
			next_attempt_ms = CASE WHEN revision = ? THEN ? ELSE next_attempt_ms END
		WHERE a_leg_id = ? AND lease_owner = ?
			AND tenant_id = ? AND principal_id = ? AND project_key = ?
	`, cs.Revision, retryAtMS, cs.ALegID, cs.LeaseOwner,
		cs.Owner.TenantID, cs.Owner.PrincipalID, cs.Owner.ProjectKey).Exec(ctx)
	if err != nil {
		return opErr("fail session", err)
	}
	return nil
}

// placeholders renders n bun positional placeholders for an IN (…) list.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// fenceExistsTx reports whether a Forget fenced aLegID (design §12.2 step 1).
func fenceExistsTx(ctx context.Context, tx bun.Tx, aLegID string) (bool, error) {
	var one int
	err := tx.NewRaw(`SELECT 1 FROM proxy_memory_fence WHERE a_leg_id = ?`, aLegID).Scan(ctx, &one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, opErr("select memory fence", err)
	}
	return true, nil
}

// extractionOwnerTx returns the owner recorded by aLegID's stored extraction,
// if one exists (design §12.2 step 2: it keeps an A-leg in its first bank even
// after a successful extraction deleted the session row).
func extractionOwnerTx(ctx context.Context, tx bun.Tx, aLegID string) (proxymemory.Owner, bool, error) {
	var owner proxymemory.Owner
	err := tx.NewRaw(`
		SELECT tenant_id, principal_id, project_key FROM proxy_memory_extractions WHERE a_leg_id = ?
	`, aLegID).Scan(ctx, &owner.TenantID, &owner.PrincipalID, &owner.ProjectKey)
	if errors.Is(err, sql.ErrNoRows) {
		return proxymemory.Owner{}, false, nil
	}
	if err != nil {
		return proxymemory.Owner{}, false, opErr("select extraction owner", err)
	}
	return owner, true, nil
}

// ledgerWatermark is the durable identity, the latest extracted source
// activity and the monotone activity watermark for one A-leg. It contains no
// conversation or memory content.
type ledgerWatermark struct {
	tenantID         string
	principalID      string
	projectKey       string
	sourceActivityMS int64
	lastActivityMS   int64
	sourceSeq        int64
}

func (w ledgerWatermark) owner() proxymemory.Owner {
	return proxymemory.Owner{TenantID: w.tenantID, PrincipalID: w.principalID, ProjectKey: w.projectKey}
}

// ledgerWatermarkTx reads the durable extraction owner, the exact extracted
// source activity and the monotone activity watermark, if one exists. Unlike
// the extraction row, this survives retention purge.
func ledgerWatermarkTx(ctx context.Context, tx bun.Tx, aLegID string) (ledgerWatermark, bool, error) {
	var watermark ledgerWatermark
	err := tx.NewRaw(`
		SELECT tenant_id, principal_id, project_key, source_activity_ms, last_activity_ms, source_seq
		FROM proxy_memory_extraction_ledger WHERE a_leg_id = ?
	`, aLegID).Scan(ctx,
		&watermark.tenantID, &watermark.principalID, &watermark.projectKey,
		&watermark.sourceActivityMS, &watermark.lastActivityMS, &watermark.sourceSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return ledgerWatermark{}, false, nil
	}
	if err != nil {
		return ledgerWatermark{}, false, opErr("select extraction watermark", err)
	}
	return watermark, true, nil
}

// deleteCaptureIfMatchesTx removes only the row written by the current capture
// when a post-write watermark check discovers that an extraction already
// covers it. A later activity cannot be deleted by this compensation.
func deleteCaptureIfMatchesTx(ctx context.Context, tx bun.Tx, c proxymemory.SessionCapture) error {
	if _, err := tx.NewRaw(`
		DELETE FROM proxy_memory_sessions
		WHERE a_leg_id = ? AND tenant_id = ? AND principal_id = ? AND project_key = ? AND source_seq = ?
	`, c.ALegID, c.Owner.TenantID, c.Owner.PrincipalID, c.Owner.ProjectKey, c.SourceSeq).Exec(ctx); err != nil {
		return opErr("remove capture covered by extraction", err)
	}
	return nil
}

// sessionActivity is the stored session-row probe of UpsertSession: the owner
// columns an upsert must match and the stored activity an upsert must exceed.
type sessionActivity struct {
	tenantID       string
	principalID    string
	projectKey     string
	lastActivityMS int64
	sourceSeq      int64
}

// owner renders the probed owner columns.
func (s sessionActivity) owner() proxymemory.Owner {
	return proxymemory.Owner{TenantID: s.tenantID, PrincipalID: s.principalID, ProjectKey: s.projectKey}
}

// sessionActivityTx reads aLegID's stored session row, if one exists.
func sessionActivityTx(ctx context.Context, tx bun.Tx, aLegID string) (sessionActivity, bool, error) {
	var stored sessionActivity
	err := tx.NewRaw(`
		SELECT tenant_id, principal_id, project_key, last_activity_ms, source_seq
		FROM proxy_memory_sessions WHERE a_leg_id = ?
	`, aLegID).Scan(ctx, &stored.tenantID, &stored.principalID, &stored.projectKey, &stored.lastActivityMS, &stored.sourceSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionActivity{}, false, nil
	}
	if err != nil {
		return sessionActivity{}, false, opErr("select stored session activity", err)
	}
	return stored, true, nil
}
