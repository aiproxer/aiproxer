package bunstore

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/uptrace/bun"
)

func (s *Store) AddUsage(ctx context.Context, delta domain.UsageDelta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE aip_secure_sessions SET
		usage_in = usage_in + ?, usage_out = usage_out + ?
		WHERE session_id = ?`, delta.InputTokens, delta.OutputTokens, string(delta.SessionID))
		if err != nil {
			return opErr("usage update totals", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return opErr("usage update totals rows affected", err)
		}
		if n == 0 {
			s.invalidateSessionMetaCache(delta.SessionID)
			return domain.ErrSessionNotFound
		}
		now := time.Now().UnixNano()
		_, err = tx.ExecContext(
			ctx, `INSERT INTO aip_secure_usage(
		session_id, turn_id, b_leg_id, input_tokens, output_tokens,
		cache_read_tokens, cache_write_tokens, non_cached_input_tokens,
		reasoning_tokens, non_reasoning_output_tokens, total_tokens,
		cost_nano_units, cost_minor_units, currency, cost_source, raw_usage_json,
		billing_unavailable, request_started_at_unix, first_remote_event_at_unix,
		first_meaningful_token_at_unix, remote_completed_at_unix, proxy_completed_at_unix,
		ttft_millis, remote_duration_millis, completion_duration_millis, completion_tps_milli,
		created_at_unix
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			string(delta.SessionID), string(delta.TurnID), delta.BLegID,
			delta.InputTokens, delta.OutputTokens,
			delta.CacheReadTokens, delta.CacheWriteTokens, delta.NonCachedInputTokens,
			delta.ReasoningTokens, delta.NonReasoningOutputTokens, delta.TotalTokens,
			delta.CostNanoUnits, delta.CostMinorUnits, delta.Currency, delta.CostSource, delta.RawUsageJSON,
			boolToInt(delta.BillingUnavailable), unixNanoOrZero(delta.RequestStartedAt), unixNanoOrZero(delta.FirstRemoteEventAt),
			unixNanoOrZero(delta.FirstMeaningfulTokenAt), unixNanoOrZero(delta.RemoteCompletedAt), unixNanoOrZero(delta.ProxyCompletedAt),
			delta.TTFTMillis, delta.RemoteDurationMillis, delta.CompletionDurationMillis, delta.CompletionTPSMilli, now,
		)
		if err != nil {
			return opErr("usage insert row", err)
		}
		if delta.BLegID != "" {
			acct := domain.AttemptAccounting{
				BLegID:                   delta.BLegID,
				InputTokens:              delta.InputTokens,
				OutputTokens:             delta.OutputTokens,
				CacheReadTokens:          delta.CacheReadTokens,
				CacheWriteTokens:         delta.CacheWriteTokens,
				NonCachedInputTokens:     delta.NonCachedInputTokens,
				ReasoningTokens:          delta.ReasoningTokens,
				NonReasoningOutputTokens: delta.NonReasoningOutputTokens,
				TotalTokens:              delta.TotalTokens,
				CostNanoUnits:            delta.CostNanoUnits,
				CostMinorUnits:           delta.CostMinorUnits,
				Currency:                 delta.Currency,
				CostSource:               delta.CostSource,
				RawUsageJSON:             delta.RawUsageJSON,
				BillingUnavailable:       delta.BillingUnavailable,
				RequestStartedAt:         delta.RequestStartedAt,
				FirstRemoteEventAt:       delta.FirstRemoteEventAt,
				FirstMeaningfulTokenAt:   delta.FirstMeaningfulTokenAt,
				RemoteCompletedAt:        delta.RemoteCompletedAt,
				ProxyCompletedAt:         delta.ProxyCompletedAt,
				TTFTMillis:               delta.TTFTMillis,
				RemoteDurationMillis:     delta.RemoteDurationMillis,
				CompletionDurationMillis: delta.CompletionDurationMillis,
				CompletionTPSMilli:       delta.CompletionTPSMilli,
			}
			var existingJ string
			if err := tx.QueryRowContext(ctx, `SELECT latest_attempt_accounting_json FROM aip_secure_sessions
			WHERE session_id = ?`, string(delta.SessionID)).Scan(&existingJ); err != nil {
				return opErr("usage accounting load", err)
			}
			var existing domain.AttemptAccounting
			if strings.TrimSpace(existingJ) != "" && existingJ != "{}" {
				if err := json.Unmarshal([]byte(existingJ), &existing); err != nil {
					return opErr("decode existing accounting", err)
				}
			}
			if existing.BLegID == acct.BLegID {
				acct = domain.MergeAttemptAccounting(existing, acct)
			}
			acctJ, err := marshalJSONText(acct)
			if err != nil {
				return opErr("marshal accounting", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE aip_secure_sessions SET latest_attempt_accounting_json = ?
			WHERE session_id = ?`, acctJ, string(delta.SessionID)); err != nil {
				return opErr("usage accounting", err)
			}
		}
		return nil
	})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func unixNanoOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func unixNanoTimeOrZero(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}
