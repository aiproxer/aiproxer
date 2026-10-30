package bunstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

func (s *Store) ListAttemptEvidence(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AttemptEvidence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ok, err := s.sessionExists(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrSessionNotFound
	}
	limit := opts.Limit
	if limit <= 0 || limit > 5000 {
		limit = 100
	}
	// The newest limit attempts, returned oldest first: the same window the
	// memory store serves, so a long session never hides its latest attempts.
	attRows, err := s.db.QueryContext(ctx, `
		SELECT turn_id, a_leg_id, b_leg_id, attempt_seq,
			requested_model, requested_alias, resolved_backend, resolved_model,
			route_source, route_reason, settings_json, started_at_unix,
			ended_at_unix, success, surface_state, http_status, provider_status,
			error_code, timeout_class, debug_reason, open_failed
		FROM (
			SELECT id, turn_id, a_leg_id, b_leg_id, attempt_seq,
				requested_model, requested_alias, resolved_backend, resolved_model,
				route_source, route_reason, settings_json, started_at_unix,
				ended_at_unix, success, surface_state, http_status, provider_status,
				error_code, timeout_class, debug_reason, open_failed
			FROM aip_secure_attempt_traces
			WHERE session_id = ?
			ORDER BY id DESC
			LIMIT ?
		) AS newest
		ORDER BY id ASC`, string(id), limit)
	if err != nil {
		return nil, opErr("list attempts query", err)
	}
	attClosed := false
	defer func() {
		if !attClosed {
			_ = attRows.Close()
		}
	}()
	out := make([]domain.AttemptEvidence, 0, limit)
	for attRows.Next() {
		var turnID, aLeg, bLeg string
		var attemptSeq int
		var reqModel, reqAlias, resBack, resModel, routeSrc, routeReason, settingsJ string
		var startedUnix, endedUnix int64
		var successInt int
		var surface, provStatus, errCode, timeoutClass, debugReason string
		var httpStatus, openFailed int
		if err := attRows.Scan(&turnID, &aLeg, &bLeg, &attemptSeq, &reqModel, &reqAlias, &resBack, &resModel,
			&routeSrc, &routeReason, &settingsJ, &startedUnix, &endedUnix, &successInt, &surface, &httpStatus,
			&provStatus, &errCode, &timeoutClass, &debugReason, &openFailed); err != nil {
			return nil, opErr("list attempts scan", err)
		}
		var settings domain.AttemptSettings
		if strings.TrimSpace(settingsJ) != "" && settingsJ != "{}" {
			if err := json.Unmarshal([]byte(settingsJ), &settings); err != nil {
				return nil, opErr("list attempts settings json", err)
			}
		}
		tr := domain.AttemptTrace{
			SessionID:       id,
			TurnID:          domain.TurnID(turnID),
			ALegID:          aLeg,
			BLegID:          bLeg,
			AttemptSeq:      attemptSeq,
			RequestedModel:  reqModel,
			RequestedAlias:  reqAlias,
			ResolvedBackend: resBack,
			ResolvedModel:   resModel,
			RouteSource:     routeSrc,
			RouteReason:     routeReason,
			Settings:        settings,
			StartedAt:       time.Unix(0, startedUnix),
			OpenFailed:      openFailed != 0,
		}
		oc := domain.AttemptOutcome{
			SessionID:      id,
			TurnID:         domain.TurnID(turnID),
			BLegID:         bLeg,
			Success:        successInt != 0,
			SurfaceState:   domain.SurfaceState(surface),
			HTTPStatus:     httpStatus,
			ProviderStatus: provStatus,
			ErrorCode:      errCode,
			TimeoutClass:   timeoutClass,
			DebugReason:    debugReason,
			EndedAt:        unixNanoTimeOrZero(endedUnix),
		}
		out = append(out, domain.AttemptEvidence{
			Trace:      tr,
			Outcome:    oc,
			Accounting: domain.AttemptAccounting{BLegID: bLeg},
		})
	}
	if err := attRows.Err(); err != nil {
		return nil, opErr("list attempts rows", err)
	}
	// Release first result set before opening another on the same pool (SQLite single connection).
	if err := attRows.Close(); err != nil {
		return nil, opErr("list attempts att close rows", err)
	}
	attClosed = true

	usageRows, err := s.db.QueryContext(ctx, `
		SELECT b_leg_id,
			COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
			COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0),
			COALESCE(SUM(non_cached_input_tokens),0),
			COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(non_reasoning_output_tokens),0),
			COALESCE(MAX(total_tokens),0),
			COALESCE(SUM(cost_nano_units),0), COALESCE(SUM(cost_minor_units),0),
			MAX(currency) AS cur, MAX(cost_source) AS cost_source, MAX(raw_usage_json) AS raw_usage_json,
			MAX(billing_unavailable) AS bu,
			MAX(request_started_at_unix), MAX(first_remote_event_at_unix), MAX(first_meaningful_token_at_unix),
			MAX(remote_completed_at_unix), MAX(proxy_completed_at_unix),
			MAX(ttft_millis), MAX(remote_duration_millis), MAX(completion_duration_millis), MAX(completion_tps_milli)
		FROM aip_secure_usage WHERE session_id = ? GROUP BY b_leg_id`, string(id))
	if err != nil {
		return nil, opErr("list attempts usage", err)
	}
	defer func() { _ = usageRows.Close() }()
	byLeg := make(map[string]domain.AttemptAccounting)
	for usageRows.Next() {
		var b string
		var inTok, outTok, cr, cw, nonCached, reasoning, nonReasoning, total, costNano, costMinor int64
		var reqAt, firstRemoteAt, firstMeaningfulAt, remoteDoneAt, proxyDoneAt int64
		var ttft, remoteDur, completionDur, tps int64
		var cur, costSource, rawUsage string
		var bu int
		if err := usageRows.Scan(
			&b, &inTok, &outTok, &cr, &cw, &nonCached, &reasoning, &nonReasoning, &total,
			&costNano, &costMinor, &cur, &costSource, &rawUsage, &bu,
			&reqAt, &firstRemoteAt, &firstMeaningfulAt, &remoteDoneAt, &proxyDoneAt,
			&ttft, &remoteDur, &completionDur, &tps,
		); err != nil {
			return nil, opErr("list attempts usage scan", err)
		}
		byLeg[b] = domain.AttemptAccounting{
			BLegID:                   b,
			InputTokens:              inTok,
			OutputTokens:             outTok,
			CacheReadTokens:          cr,
			CacheWriteTokens:         cw,
			NonCachedInputTokens:     nonCached,
			ReasoningTokens:          reasoning,
			NonReasoningOutputTokens: nonReasoning,
			TotalTokens:              total,
			CostNanoUnits:            costNano,
			CostMinorUnits:           costMinor,
			Currency:                 cur,
			CostSource:               costSource,
			RawUsageJSON:             rawUsage,
			BillingUnavailable:       bu != 0,
			RequestStartedAt:         unixNanoTimeOrZero(reqAt),
			FirstRemoteEventAt:       unixNanoTimeOrZero(firstRemoteAt),
			FirstMeaningfulTokenAt:   unixNanoTimeOrZero(firstMeaningfulAt),
			RemoteCompletedAt:        unixNanoTimeOrZero(remoteDoneAt),
			ProxyCompletedAt:         unixNanoTimeOrZero(proxyDoneAt),
			TTFTMillis:               ttft,
			RemoteDurationMillis:     remoteDur,
			CompletionDurationMillis: completionDur,
			CompletionTPSMilli:       tps,
		}
	}
	if err := usageRows.Err(); err != nil {
		return nil, opErr("list attempts usage rows", err)
	}
	for i := range out {
		if ac, ok := byLeg[out[i].Trace.BLegID]; ok {
			out[i].Accounting = ac
		}
	}
	return out, nil
}

func (s *Store) Summary(ctx context.Context, query domain.SummaryQuery) ([]domain.Summary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT s.session_id, s.owner_id, s.workspace_id, s.last_activity_unix,
		s.attempt_count,
		(SELECT COUNT(1) FROM aip_secure_turns t WHERE t.session_id = s.session_id) AS turn_count,
		s.status, s.resume_eligible, s.a_leg_id, s.policy_version, s.transcript_enabled,
		s.redaction_profile, s.audit_mode, s.usage_in, s.usage_out
		FROM aip_secure_sessions s`
	// 2 cond max (owner+workspace); 3 args max (2 cond + limit).
	cond := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if query.OwnerID != "" {
		cond = append(cond, `s.owner_id = ?`)
		args = append(args, query.OwnerID)
	}
	if query.WorkspaceID != "" {
		cond = append(cond, `s.workspace_id = ?`)
		args = append(args, query.WorkspaceID)
	}
	if len(cond) > 0 {
		q += ` WHERE ` + strings.Join(cond, ` AND `)
	}
	q += ` ORDER BY s.last_activity_unix DESC, s.session_id ASC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, opErr("summary query", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.Summary, 0, limit)
	for rows.Next() {
		var sid, ownerID, wsID string
		var lastActUnix int64
		var attemptCount, turnCount int
		var status string
		var resumeElig, transcriptEn int
		var aLeg, polVer, redProf, auditMode string
		var usageIn, usageOut int64
		if err := rows.Scan(&sid, &ownerID, &wsID, &lastActUnix, &attemptCount, &turnCount,
			&status, &resumeElig, &aLeg, &polVer, &transcriptEn, &redProf, &auditMode, &usageIn, &usageOut); err != nil {
			return nil, opErr("summary scan", err)
		}
		out = append(out, domain.Summary{
			SessionID:      domain.SessionID(sid),
			OwnerID:        ownerID,
			WorkspaceID:    wsID,
			LastActivityAt: time.Unix(0, lastActUnix),
			TurnCount:      turnCount,
			AttemptCount:   attemptCount,

			Status:            domain.SessionStatus(status),
			ResumeEligible:    resumeElig != 0,
			ALegID:            aLeg,
			PolicyVersion:     polVer,
			TranscriptEnabled: transcriptEn != 0,
			RedactionProfile:  redProf,
			AuditMode:         auditMode,
			UsageInputTokens:  usageIn,
			UsageOutputTokens: usageOut,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, opErr("summary rows", err)
	}
	return out, nil
}

func (s *Store) UsageTokenTotals(ctx context.Context, id domain.SessionID) (int64, int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	var in, out int64
	err := s.db.QueryRowContext(
		ctx,
		`SELECT usage_in, usage_out FROM aip_secure_sessions WHERE session_id = ?`, string(id),
	).Scan(&in, &out)
	if errors.Is(err, sql.ErrNoRows) {
		s.invalidateSessionMetaCache(id)
		return 0, 0, domain.ErrSessionNotFound
	}
	if err != nil {
		return 0, 0, opErr("usage totals", err)
	}
	return in, out, nil
}

func (s *Store) CheckReadiness(ctx context.Context, policy domain.PolicyMetadata) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_ = policy
	return nil
}
