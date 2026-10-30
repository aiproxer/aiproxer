package proxymemorystore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

type admissionBindingRow struct {
	TenantID          string `bun:"tenant_id"`
	PrincipalID       string `bun:"principal_id"`
	ProjectKey        string `bun:"project_key"`
	ProjectLabel      string `bun:"project_label"`
	DecisionOutcome   string `bun:"first_turn_outcome"`
	DecisionInjection string `bun:"first_turn_injection"`
	SummaryVersion    int64  `bun:"first_turn_summary_version"`
	DecisionEpoch     int64  `bun:"first_turn_epoch"`
}

const (
	admissionBindingSelect = `SELECT tenant_id, principal_id, project_key, project_label,
		first_turn_outcome, first_turn_injection, first_turn_summary_version, first_turn_epoch
		FROM proxy_memory_session_bindings WHERE a_leg_id = ?`
	admissionEpochSelect = `SELECT epoch FROM proxy_memory_erasure_epochs
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`
	admissionEpochInsert = `INSERT INTO proxy_memory_erasure_epochs (tenant_id, principal_id, project_key, epoch)
		VALUES (?, ?, ?, 0) ON CONFLICT (tenant_id, principal_id, project_key) DO NOTHING`
)

// ReadAdmission reads the bounded, read-only A-leg/binding/epoch/summary view
// of one request transform, in one consistent snapshot.
//
// The core A-leg facts are read before the memory transaction starts: the
// memory database is isolated from the core continuity database, so the read
// must not hold a core connection while a memory transaction is open. The
// memory tables are then read in one repeatable-read snapshot.
func (s *Store) ReadAdmission(ctx context.Context, q proxymemory.AdmissionQuery) (proxymemory.AdmissionSnapshot, error) {
	if ctx == nil {
		return proxymemory.AdmissionSnapshot{}, errors.New("proxymemorystore: nil admission context")
	}
	if err := ctx.Err(); err != nil {
		return proxymemory.AdmissionSnapshot{}, err
	}
	state, err := s.core.ALegState(ctx, q.ALegID)
	if err != nil {
		return proxymemory.AdmissionSnapshot{}, err
	}
	if !state.Active {
		return proxymemory.AdmissionSnapshot{}, proxymemory.ErrNotFound
	}
	var out proxymemory.AdmissionSnapshot
	out.NextBSeq = state.NextBSeq
	err = s.db.RunInTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, func(ctx context.Context, tx bun.Tx) error {
		return s.readAdmissionTx(ctx, tx, q, &out)
	})
	if err != nil {
		return proxymemory.AdmissionSnapshot{}, opErr("read admission snapshot", err)
	}
	return out, nil
}

func (s *Store) readAdmissionTx(ctx context.Context, tx bun.Tx, q proxymemory.AdmissionQuery, out *proxymemory.AdmissionSnapshot) error {
	if q.ALegID == "" {
		return proxymemory.ErrNotFound
	}

	var binding admissionBindingRow
	err := tx.NewRaw(admissionBindingSelect, q.ALegID).Scan(ctx,
		&binding.TenantID, &binding.PrincipalID, &binding.ProjectKey, &binding.ProjectLabel,
		&binding.DecisionOutcome, &binding.DecisionInjection, &binding.SummaryVersion, &binding.DecisionEpoch)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// The candidate or process-cache project is selected below.
	case err != nil:
		return opErr("read A-leg project binding", err)
	default:
		if binding.TenantID != q.Principal.TenantID || binding.PrincipalID != q.Principal.PrincipalID {
			return proxymemory.ErrNotFound
		}
		out.Bound = true
		out.Project = proxymemory.Project{Key: binding.ProjectKey, Label: binding.ProjectLabel}
	}

	if !out.Bound {
		switch {
		case q.CachedProject != nil:
			out.Project = *q.CachedProject
		case q.AuthoritativeFirstTurn:
			out.Project = q.Candidate
		default:
			return nil
		}
	}

	epoch, err := readErasureEpochTx(ctx, tx, q.Principal.TenantID, q.Principal.PrincipalID, out.Project.Key)
	if err != nil {
		return err
	}
	out.ErasureEpoch = epoch
	if out.Bound && binding.DecisionOutcome != "" && binding.DecisionEpoch == epoch {
		decision := proxymemory.FirstTurnDecision{
			Outcome:        proxymemory.FirstTurnOutcome(binding.DecisionOutcome),
			Injection:      binding.DecisionInjection,
			SummaryVersion: binding.SummaryVersion,
		}
		if !validDecision(decision) {
			return errors.New("proxymemorystore: invalid persisted first-turn decision")
		}
		out.Decision = &decision
	}

	// Only an authoritative first turn may read the current bank; a later
	// replay uses the frozen decision or omits recall.
	if !q.AuthoritativeFirstTurn || out.Decision != nil || out.Project.Key == "" {
		return nil
	}
	err = tx.NewRaw(`SELECT memory_summary, version FROM proxy_memory_banks
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`,
		q.Principal.TenantID, q.Principal.PrincipalID, out.Project.Key).Scan(ctx, &out.Summary, &out.SummaryVersion)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		if ctx.Err() != nil {
			return err
		}
		out.Summary, out.SummaryVersion = "", 0
		out.SummaryUnavailable = true
		return nil
	case out.Summary == "":
		out.SummaryVersion = 0
	}
	return nil
}

// CommitFirstTurn persists one immutable decision only while the active
// A-leg, immutable project and observed erasure epoch still match. A conflict
// is a single-shot drop, never a re-read that assigns a replacement.
//
// The core A-leg liveness is read before the memory transaction starts: the
// memory database is isolated from the core continuity database, so the read
// must not hold a core connection while a memory transaction is open.
func (s *Store) CommitFirstTurn(ctx context.Context, job proxymemory.FirstTurnJob) (bool, error) {
	if ctx == nil {
		return false, errors.New("proxymemorystore: nil first-turn context")
	}
	if !job.EpochKnown || job.ALegID == "" || !validDecision(job.Decision) {
		return false, errors.New("proxymemorystore: invalid first-turn job")
	}
	state, err := s.core.ALegState(ctx, job.ALegID)
	if err != nil {
		return false, err
	}
	if !state.Active {
		return false, nil
	}
	committed := false
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// The epoch is the outer fence: a decision queued before a Forget fails here.
		currentEpoch, err := lockErasureEpochTx(ctx, tx, job.Principal.TenantID, job.Principal.PrincipalID, job.Project.Key)
		if err != nil {
			return err
		}
		if currentEpoch != job.ErasureEpoch {
			return nil
		}
		fenced, err := fenceExistsTx(ctx, tx, job.ALegID)
		if err != nil {
			return err
		}
		if fenced {
			return nil
		}

		var existing admissionBindingRow
		found, err := loadAdmissionBindingTx(ctx, tx, s.db.Dialect().Name(), job.ALegID, &existing)
		if err != nil {
			return err
		}
		if found {
			if !sameBindingOwner(existing, job) {
				return nil
			}
			if existing.DecisionOutcome != "" && existing.DecisionEpoch == job.ErasureEpoch {
				committed = sameDecision(existing, job)
				return nil
			}
			res, err := tx.NewRaw(`UPDATE proxy_memory_session_bindings
				SET first_turn_outcome = ?, first_turn_injection = ?,
					first_turn_summary_version = ?, first_turn_epoch = ?
				WHERE a_leg_id = ? AND tenant_id = ? AND principal_id = ? AND project_key = ?`,
				string(job.Decision.Outcome), job.Decision.Injection, job.Decision.SummaryVersion, job.ErasureEpoch,
				job.ALegID, job.Principal.TenantID, job.Principal.PrincipalID, job.Project.Key).Exec(ctx)
			if err != nil {
				return opErr("save first-turn decision", err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return opErr("save first-turn decision rows affected", err)
			}
			committed = affected == 1
			return nil
		}

		res, err := tx.NewRaw(`INSERT INTO proxy_memory_session_bindings
			(a_leg_id, tenant_id, principal_id, project_key, project_label,
			 first_turn_outcome, first_turn_injection, first_turn_summary_version, first_turn_epoch)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (a_leg_id) DO NOTHING`,
			job.ALegID, job.Principal.TenantID, job.Principal.PrincipalID, job.Project.Key, job.Project.Label,
			string(job.Decision.Outcome), job.Decision.Injection, job.Decision.SummaryVersion, job.ErasureEpoch).Exec(ctx)
		if err != nil {
			return opErr("insert first-turn decision", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return opErr("insert first-turn decision rows affected", err)
		}
		if affected == 1 {
			committed = true
			return nil
		}

		// Another transaction won the binding race: a matching decision is
		// durable success, a different one is an immutable-decision conflict.
		found, err = loadAdmissionBindingTx(ctx, tx, s.db.Dialect().Name(), job.ALegID, &existing)
		if err != nil {
			return err
		}
		if found && sameBindingOwner(existing, job) && existing.DecisionEpoch == job.ErasureEpoch {
			committed = sameDecision(existing, job)
		}
		return nil
	})
	if err != nil {
		return false, opErr("commit first-turn decision", err)
	}
	return committed, nil
}

func sameBindingOwner(row admissionBindingRow, job proxymemory.FirstTurnJob) bool {
	return row.TenantID == job.Principal.TenantID && row.PrincipalID == job.Principal.PrincipalID &&
		row.ProjectKey == job.Project.Key
}

// bindingOwnerMatches reports whether the stored binding belongs to the
// capture's owner.
func bindingOwnerMatches(row admissionBindingRow, owner proxymemory.Owner) bool {
	return row.TenantID == owner.TenantID && row.PrincipalID == owner.PrincipalID &&
		row.ProjectKey == owner.ProjectKey
}

func sameDecision(row admissionBindingRow, job proxymemory.FirstTurnJob) bool {
	return row.DecisionOutcome == string(job.Decision.Outcome) &&
		row.DecisionInjection == job.Decision.Injection &&
		row.SummaryVersion == job.Decision.SummaryVersion
}

func loadAdmissionBindingTx(ctx context.Context, tx bun.Tx, name dialect.Name, aLegID string, row *admissionBindingRow) (bool, error) {
	query := admissionBindingSelect
	if name == dialect.PG {
		query += ` FOR UPDATE`
	}
	err := tx.NewRaw(query, aLegID).Scan(ctx,
		&row.TenantID, &row.PrincipalID, &row.ProjectKey, &row.ProjectLabel,
		&row.DecisionOutcome, &row.DecisionInjection, &row.SummaryVersion, &row.DecisionEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, opErr("read first-turn binding", err)
	}
	return true, nil
}

func validDecision(decision proxymemory.FirstTurnDecision) bool {
	switch decision.Outcome {
	case proxymemory.FirstTurnMatched:
		return decision.Injection != ""
	case proxymemory.FirstTurnNoSummary, proxymemory.FirstTurnNoProject, proxymemory.FirstTurnUnavailable:
		return decision.Injection == ""
	default:
		return false
	}
}

// readErasureEpochTx reads the owner-scoped erasure epoch inside the memory
// transaction. A missing row is epoch zero.
func readErasureEpochTx(ctx context.Context, tx bun.Tx, tenantID, principalID, projectKey string) (int64, error) {
	var epoch int64
	err := tx.NewRaw(admissionEpochSelect, tenantID, principalID, projectKey).Scan(ctx, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, opErr("read erasure epoch", err)
	}
	return epoch, nil
}

// lockErasureEpochTx creates the owner-scoped epoch when it is absent and
// locks it. The lock serializes every memory writer of one owner against
// Forget, and it is the first statement of a writing transaction, so the
// transaction owns its write lock before it reads.
func lockErasureEpochTx(ctx context.Context, tx bun.Tx, tenantID, principalID, projectKey string) (int64, error) {
	if _, err := tx.NewRaw(admissionEpochInsert, tenantID, principalID, projectKey).Exec(ctx); err != nil {
		return 0, opErr("create erasure epoch", err)
	}
	query := admissionEpochSelect
	if tx.Dialect().Name() == dialect.PG {
		query += ` FOR UPDATE`
	}
	var epoch int64
	if err := tx.NewRaw(query, tenantID, principalID, projectKey).Scan(ctx, &epoch); err != nil {
		return 0, opErr("lock erasure epoch", err)
	}
	return epoch, nil
}

// bumpErasureEpochTx advances the owner-scoped erasure epoch. Forget calls it
// before it erases anything, so queued work carrying the previous epoch fails
// its check even without a binding to fence.
func bumpErasureEpochTx(ctx context.Context, tx bun.Tx, owner proxymemory.Owner) error {
	_, err := tx.NewRaw(`INSERT INTO proxy_memory_erasure_epochs (tenant_id, principal_id, project_key, epoch)
		VALUES (?, ?, ?, 1) ON CONFLICT (tenant_id, principal_id, project_key)
		DO UPDATE SET epoch = proxy_memory_erasure_epochs.epoch + 1`,
		owner.TenantID, owner.PrincipalID, owner.ProjectKey).Exec(ctx)
	if err != nil {
		return opErr("advance erasure epoch", err)
	}
	return nil
}
