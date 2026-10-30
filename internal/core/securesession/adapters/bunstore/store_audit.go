package bunstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/jellydator/ttlcache/v3"
	"github.com/uptrace/bun"
)

func (s *Store) NextAuditSeq(ctx context.Context, id domain.SessionID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	ok, err := s.sessionExists(ctx, id)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, domain.ErrSessionNotFound
	}
	var maxSeq int64
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM aip_secure_audit WHERE session_id = ?`, string(id)).Scan(&maxSeq)
	if err != nil {
		return 0, opErr("next audit seq", err)
	}
	if maxSeq == math.MaxInt64 {
		return 0, opErr("next audit seq", fmt.Errorf("audit seq overflow"))
	}
	return maxSeq + 1, nil
}

func (s *Store) AppendAudit(ctx context.Context, item domain.AuditItem) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE aip_secure_sessions SET last_activity_unix = last_activity_unix WHERE session_id = ?`,
			string(item.SessionID))
		if err != nil {
			return opErr("append audit lock session", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return opErr("append audit lock session rows affected", err)
		}
		if n == 0 {
			s.invalidateSessionMetaCache(item.SessionID)
			return domain.ErrSessionNotFound
		}
		var nextSeq int64
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM aip_secure_audit WHERE session_id = ?`,
			string(item.SessionID)).Scan(&nextSeq)
		if err != nil {
			return opErr("append audit next seq", err)
		}
		_, err = tx.ExecContext(
			ctx, `INSERT INTO aip_secure_audit(
		session_id, seq, turn_id, action, result, created_at_unix
	) VALUES(?,?,?,?,?,?)`,
			string(item.SessionID), nextSeq, string(item.TurnID), item.Action, item.Result, item.CreatedAt.UnixNano(),
		)
		if err != nil {
			if isFKConstraintErr(err) {
				s.invalidateSessionMetaCache(item.SessionID)
				return domain.ErrSessionNotFound
			}
			return opErr("append audit", err)
		}
		return nil
	})
}

func (s *Store) Audit(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AuditItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ok, err := s.sessionExists(ctx, id)
	if err != nil {
		return nil, opErr("audit exists", err)
	}
	if !ok {
		return nil, domain.ErrSessionNotFound
	}
	q := `SELECT seq, turn_id, action, result, created_at_unix FROM aip_secure_audit
		WHERE session_id = ? AND seq > ? ORDER BY seq ASC`
	args := []any{string(id), opts.AfterSeq}
	if opts.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, opts.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, opErr("audit query", err)
	}
	defer func() { _ = rows.Close() }()
	auditCap := opts.Limit
	if auditCap <= 0 {
		// Unbounded read when no LIMIT; give a small buffer to limit repeated growth.
		auditCap = 16
	}
	out := make([]domain.AuditItem, 0, auditCap)
	for rows.Next() {
		var (
			seq                    int64
			turnID, action, result string
			createdUnix            int64
		)
		if err := rows.Scan(&seq, &turnID, &action, &result, &createdUnix); err != nil {
			return nil, opErr("audit scan", err)
		}
		out = append(out, domain.AuditItem{
			SessionID: id, TurnID: domain.TurnID(turnID), Seq: seq,
			Action: action, Result: result, CreatedAt: time.Unix(0, createdUnix),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, opErr("audit rows", err)
	}
	return out, nil
}

func (s *Store) sessionExists(ctx context.Context, id domain.SessionID) (bool, error) {
	if s.meta != nil {
		if it := s.meta.exists.Get(id); it != nil {
			return it.Value(), nil
		}
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM aip_secure_sessions WHERE session_id = ?`, string(id)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		if s.meta != nil {
			s.meta.exists.Set(id, false, ttlcache.DefaultTTL)
		}
		return false, nil
	}
	if err != nil {
		return false, opErr("session exists", err)
	}
	if s.meta != nil {
		s.meta.exists.Set(id, true, ttlcache.DefaultTTL)
	}
	return true, nil
}

func (s *Store) Transcript(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.TranscriptItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ok, err := s.sessionExists(ctx, id)
	if err != nil {
		return nil, opErr("transcript exists", err)
	}
	if !ok {
		return nil, domain.ErrSessionNotFound
	}
	enabled, err := s.transcriptEnabledCached(ctx, s.db, id)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			return nil, domain.ErrSessionNotFound
		}
		return nil, opErr("transcript policy", err)
	}
	if !enabled {
		return []domain.TranscriptItem{}, nil
	}
	q := `SELECT seq, turn_id, event_kind, payload_ref, created_at_unix FROM aip_secure_transcript
		WHERE session_id = ? AND seq > ? ORDER BY seq ASC`
	args := []any{string(id), opts.AfterSeq}
	if opts.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, opts.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, opErr("transcript query", err)
	}
	defer func() { _ = rows.Close() }()
	transcriptCap := opts.Limit
	if transcriptCap <= 0 {
		transcriptCap = 16
	}
	out := make([]domain.TranscriptItem, 0, transcriptCap)
	for rows.Next() {
		var seq int64
		var turnID, kind, payload string
		var createdUnix int64
		if err := rows.Scan(&seq, &turnID, &kind, &payload, &createdUnix); err != nil {
			return nil, opErr("transcript scan", err)
		}
		out = append(out, domain.TranscriptItem{
			SessionID: id, TurnID: domain.TurnID(turnID), Seq: seq,
			EventKind: kind, PayloadRef: payload, CreatedAt: time.Unix(0, createdUnix),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, opErr("transcript rows", err)
	}
	return out, nil
}
