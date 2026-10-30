package bunstore

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/uptrace/bun"
)

// Quarantine marks a session terminal and appends a secret_guard/blocked audit row atomically.
func (s *Store) Quarantine(ctx context.Context, in domain.QuarantineInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE aip_secure_sessions SET last_activity_unix = last_activity_unix WHERE session_id = ?`,
			string(in.SessionID))
		if err != nil {
			return opErr("quarantine lock session", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return opErr("quarantine lock session rows affected", err)
		}
		if n == 0 {
			s.invalidateSessionMetaCache(in.SessionID)
			return domain.ErrSessionNotFound
		}

		var status, qReason, qEventID string
		var quarantinedAtUnix int64
		err = tx.QueryRowContext(ctx, `SELECT status, quarantined_at_unix, quarantine_reason_code, quarantine_event_id FROM aip_secure_sessions WHERE session_id = ?`,
			string(in.SessionID)).Scan(&status, &quarantinedAtUnix, &qReason, &qEventID)
		if err != nil {
			return opErr("quarantine read status", err)
		}
		rec := domain.Record{
			Status:               domain.SessionStatus(status),
			QuarantineReasonCode: qReason,
			QuarantineEventID:    qEventID,
		}
		if quarantinedAtUnix != 0 || domain.SessionStatus(status).IsQuarantined() {
			rec.QuarantinedAt = time.Unix(0, quarantinedAtUnix)
		}
		plan, err := domain.PlanQuarantine(rec, in)
		if err != nil {
			return err
		}
		if !plan.Apply {
			return nil
		}

		_, err = tx.ExecContext(
			ctx, `UPDATE aip_secure_sessions SET
			status = ?,
			resume_eligible = 0,
			quarantined_at_unix = ?,
			quarantine_reason_code = ?,
			quarantine_event_id = ?
			WHERE session_id = ?`,
			string(plan.Status),
			plan.QuarantinedAt.UnixNano(),
			plan.ReasonCode,
			plan.EventID,
			string(in.SessionID),
		)
		if err != nil {
			return opErr("quarantine update", err)
		}

		var nextSeq int64
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM aip_secure_audit WHERE session_id = ?`,
			string(in.SessionID)).Scan(&nextSeq)
		if err != nil {
			return opErr("quarantine next audit seq", err)
		}
		_, err = tx.ExecContext(
			ctx, `INSERT INTO aip_secure_audit(
			session_id, seq, turn_id, action, result, created_at_unix
		) VALUES(?,?,?,?,?,?)`,
			string(in.SessionID), nextSeq, string(in.TurnID), plan.AuditAction, plan.AuditResult, plan.QuarantinedAt.UnixNano(),
		)
		if err != nil {
			return opErr("quarantine audit", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.invalidateSessionMetaCache(in.SessionID)
	return nil
}
