package proxymemorystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/uptrace/bun"
)

// RecordPhaseFailure records one immutable memory-processing failure fact and,
// in the same transaction, conditionally releases and reschedules the caller's
// still-live claim.
//
// Transaction protocol:
//  1. The owner's erasure epoch is locked first (the first SQL statement), so
//     it serializes against Forget exactly like every other memory writer.
//  2. The captured epoch and the receipt's fixed expiry are checked before any
//     write. A mismatch writes nothing.
//  3. The fact is inserted on the unique (store_id, billing_call_id, phase)
//     operation key. An identical semantic replay is a no-op; a different
//     owner, claim or payload is a conflict and rolls the transaction back.
//  4. Only the same still-live claim (owner, lease owner, lease deadline,
//     phase-specific revision/version and dirty sequence) may be released and
//     rescheduled. Stale evidence commits its immutable fact but mutates no
//     successor.
func (s *Store) RecordPhaseFailure(ctx context.Context, f proxymemory.FailureEvidence) (proxymemory.FailureOutcome, error) {
	if err := f.Validate(); err != nil {
		return "", err
	}
	var outcome proxymemory.FailureOutcome
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		epoch, err := lockErasureEpochTx(ctx, tx, f.Owner.TenantID, f.Owner.PrincipalID, f.Owner.ProjectKey)
		if err != nil {
			return err
		}
		if epoch != f.ErasureEpoch {
			outcome = proxymemory.FailureErased
			return nil
		}
		if f.RecordedAtMS >= f.ExpiresAtMS {
			outcome = proxymemory.FailureExpired
			return nil
		}
		res, err := tx.NewRaw(`
			INSERT INTO proxy_memory_phase_failures
				(store_id, billing_call_id, phase, tenant_id, principal_id, project_key,
				 claim_id, claim_revision, claim_seq, lease_owner, lease_until_ms, erasure_epoch,
				 failure_kind, completed_at_ms, expires_at_ms, recorded_at_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (store_id, billing_call_id, phase) DO NOTHING
		`, f.StoreID, f.BillingCallID, f.Phase, f.Owner.TenantID, f.Owner.PrincipalID, f.Owner.ProjectKey,
			f.ClaimID, f.ClaimRevision, f.ClaimSeq, f.LeaseOwner, f.LeaseUntilMS, f.ErasureEpoch,
			f.Kind, f.CompletedAtMS, f.ExpiresAtMS, f.RecordedAtMS).Exec(ctx)
		if err != nil {
			return opErr("insert phase failure evidence", err)
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return opErr("insert phase failure evidence rows affected", err)
		}
		if inserted == 0 {
			existing, err := loadFailureTx(ctx, tx, f.StoreID, f.BillingCallID, f.Phase)
			if err != nil {
				return err
			}
			if !sameFailureEvidence(existing, f) {
				return fmt.Errorf("%w: operation %s/%s/%s", proxymemory.ErrFailureConflict, f.StoreID, f.BillingCallID, f.Phase)
			}
			outcome = proxymemory.FailureDuplicate
			return nil
		}
		released, err := releaseFailedClaimTx(ctx, tx, f)
		if err != nil {
			return err
		}
		if released {
			outcome = proxymemory.FailureRecorded
		} else {
			outcome = proxymemory.FailureRecordedStale
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}

// failureRow is the immutable stored shape compared on replay.
type failureRow struct {
	TenantID      string `bun:"tenant_id"`
	PrincipalID   string `bun:"principal_id"`
	ProjectKey    string `bun:"project_key"`
	ClaimID       string `bun:"claim_id"`
	ClaimRevision int64  `bun:"claim_revision"`
	ClaimSeq      int64  `bun:"claim_seq"`
	LeaseOwner    string `bun:"lease_owner"`
	LeaseUntilMS  int64  `bun:"lease_until_ms"`
	ErasureEpoch  int64  `bun:"erasure_epoch"`
	Kind          string `bun:"failure_kind"`
	CompletedAtMS int64  `bun:"completed_at_ms"`
	ExpiresAtMS   int64  `bun:"expires_at_ms"`
}

func loadFailureTx(ctx context.Context, tx bun.Tx, storeID, callID, phase string) (failureRow, error) {
	var row failureRow
	err := tx.NewRaw(`
		SELECT tenant_id, principal_id, project_key, claim_id, claim_revision, claim_seq,
		       lease_owner, lease_until_ms, erasure_epoch, failure_kind, completed_at_ms, expires_at_ms
		FROM proxy_memory_phase_failures
		WHERE store_id = ? AND billing_call_id = ? AND phase = ?
	`, storeID, callID, phase).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return failureRow{}, opErr("load phase failure evidence", err)
	}
	if err != nil {
		return failureRow{}, opErr("load phase failure evidence", err)
	}
	return row, nil
}

func sameFailureEvidence(row failureRow, f proxymemory.FailureEvidence) bool {
	return row.TenantID == f.Owner.TenantID &&
		row.PrincipalID == f.Owner.PrincipalID &&
		row.ProjectKey == f.Owner.ProjectKey &&
		row.ClaimID == f.ClaimID &&
		row.ClaimRevision == f.ClaimRevision &&
		row.ClaimSeq == f.ClaimSeq &&
		row.LeaseOwner == f.LeaseOwner &&
		row.LeaseUntilMS == f.LeaseUntilMS &&
		row.ErasureEpoch == f.ErasureEpoch &&
		row.Kind == f.Kind &&
		row.CompletedAtMS == f.CompletedAtMS &&
		row.ExpiresAtMS == f.ExpiresAtMS
}

// releaseFailedClaimTx clears the claim only when it is still exactly the claim
// the evidence was captured under, and its lease has not expired. Phase 1 keeps
// the revision-aware retry rule: a newer revision under the same valid lease is
// released without inheriting the old revision's backoff. Phase 2 additionally
// fences the claimed bank version and dirty sequence.
func releaseFailedClaimTx(ctx context.Context, tx bun.Tx, f proxymemory.FailureEvidence) (bool, error) {
	var res sql.Result
	var err error
	switch f.Phase {
	case proxymemory.FailurePhaseExtraction:
		res, err = tx.NewRaw(`
			UPDATE proxy_memory_sessions
			SET lease_owner = '', lease_until_ms = 0,
				next_attempt_ms = CASE WHEN revision = ? THEN ? ELSE next_attempt_ms END
			WHERE a_leg_id = ? AND tenant_id = ? AND principal_id = ? AND project_key = ?
				AND lease_owner = ? AND lease_until_ms = ? AND lease_until_ms >= ?
		`, f.ClaimRevision, f.RetryAtMS, f.ClaimID, f.Owner.TenantID, f.Owner.PrincipalID, f.Owner.ProjectKey,
			f.LeaseOwner, f.LeaseUntilMS, f.RecordedAtMS).Exec(ctx)
	case proxymemory.FailurePhaseConsolidation:
		res, err = tx.NewRaw(`
			UPDATE proxy_memory_banks
			SET lease_owner = '', lease_until_ms = 0, next_attempt_ms = ?
			WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
				AND lease_owner = ? AND lease_until_ms = ? AND lease_until_ms >= ?
				AND version = ? AND dirty_seq = ?
		`, f.RetryAtMS, f.Owner.TenantID, f.Owner.PrincipalID, f.Owner.ProjectKey,
			f.LeaseOwner, f.LeaseUntilMS, f.RecordedAtMS, f.ClaimRevision, f.ClaimSeq).Exec(ctx)
	default:
		return false, proxymemory.ErrFailureInvalid
	}
	if err != nil {
		return false, opErr("release failed claim", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, opErr("release failed claim rows affected", err)
	}
	return affected > 0, nil
}
