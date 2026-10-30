package bunstore

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/uptrace/bun"
)

func (s *Store) AppendAttemptTrace(ctx context.Context, trace domain.AttemptTrace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		settingsJSON, err := marshalJSONText(trace.Settings)
		if err != nil {
			return opErr("marshal settings", err)
		}
		openFailed := 0
		if trace.OpenFailed {
			openFailed = 1
		}
		traceJSON, err := marshalJSONText(trace)
		if err != nil {
			return opErr("marshal trace", err)
		}
		_, err = tx.ExecContext(
			ctx, `INSERT INTO aip_secure_attempt_traces(
		session_id, turn_id, a_leg_id, b_leg_id, attempt_seq,
		requested_model, requested_alias, resolved_backend, resolved_model,
		route_source, route_reason, settings_json, started_at_unix, open_failed
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			string(trace.SessionID), string(trace.TurnID), trace.ALegID, trace.BLegID, trace.AttemptSeq,
			trace.RequestedModel, trace.RequestedAlias, trace.ResolvedBackend, trace.ResolvedModel,
			trace.RouteSource, trace.RouteReason, settingsJSON, trace.StartedAt.UnixNano(), openFailed,
		)
		if err != nil {
			if isFKConstraintErr(err) {
				s.invalidateSessionMetaCache(trace.SessionID)
				return domain.ErrSessionNotFound
			}
			return opErr("insert attempt trace", err)
		}
		res, err := tx.ExecContext(ctx, `UPDATE aip_secure_sessions SET
		attempt_count = attempt_count + 1,
		latest_attempt_trace_json = ?
		WHERE session_id = ?`, traceJSON, string(trace.SessionID))
		if err != nil {
			return opErr("update session trace", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return opErr("update session trace rows affected", err)
		}
		if n == 0 {
			s.invalidateSessionMetaCache(trace.SessionID)
			return domain.ErrSessionNotFound
		}
		return nil
	})
}

func (s *Store) UpdateAttemptOutcome(ctx context.Context, outcome domain.AttemptOutcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	outcomeJSON, err := marshalJSONText(outcome)
	if err != nil {
		return opErr("marshal outcome", err)
	}
	success := 0
	if outcome.Success {
		success = 1
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.ExecContext(
			ctx, `UPDATE aip_secure_attempt_traces SET
		ended_at_unix = ?,
		success = ?,
		surface_state = ?,
		http_status = ?,
		provider_status = ?,
		error_code = ?,
		timeout_class = ?,
		debug_reason = ?,
		outcome_json = ?
		WHERE session_id = ? AND turn_id = ? AND b_leg_id = ?`,
			outcome.EndedAt.UnixNano(),
			success,
			string(outcome.SurfaceState),
			outcome.HTTPStatus,
			outcome.ProviderStatus,
			outcome.ErrorCode,
			outcome.TimeoutClass,
			outcome.DebugReason,
			outcomeJSON,
			string(outcome.SessionID), string(outcome.TurnID), outcome.BLegID,
		)
		if err != nil {
			return opErr("update attempt trace outcome", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return opErr("update attempt trace outcome rows affected", err)
		}
		if n == 0 {
			return domain.ErrSessionNotFound
		}
		if _, err := tx.ExecContext(ctx, `UPDATE aip_secure_sessions SET latest_attempt_outcome_json = ?
		WHERE session_id = ?`, outcomeJSON, string(outcome.SessionID)); err != nil {
			return opErr("update session latest outcome", err)
		}
		return nil
	})
}
