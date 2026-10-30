package bunstore

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/uptrace/bun"
)

func (s *Store) NextTranscriptSeq(ctx context.Context, id domain.SessionID) (int64, error) {
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
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM aip_secure_transcript WHERE session_id = ?`, string(id)).Scan(&maxSeq)
	if err != nil {
		return 0, opErr("next transcript seq", err)
	}
	if maxSeq == math.MaxInt64 {
		return 0, opErr("next transcript seq", fmt.Errorf("transcript seq overflow"))
	}
	return maxSeq + 1, nil
}

func (s *Store) AppendTranscript(ctx context.Context, item domain.TranscriptItem) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Serialize per-session transcript seq: lock parent row, then allocate MAX(seq)+1 in the same transaction.
		res, err := tx.ExecContext(ctx, `UPDATE aip_secure_sessions SET last_activity_unix = last_activity_unix WHERE session_id = ?`,
			string(item.SessionID))
		if err != nil {
			return opErr("transcript lock session", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return opErr("transcript lock session rows affected", err)
		}
		if n == 0 {
			s.invalidateSessionMetaCache(item.SessionID)
			return domain.ErrSessionNotFound
		}
		enabled, err := s.transcriptEnabledCached(ctx, tx, item.SessionID)
		if err != nil {
			if errors.Is(err, domain.ErrSessionNotFound) {
				return domain.ErrSessionNotFound
			}
			return opErr("transcript policy read", err)
		}
		if !enabled {
			return domain.ErrTranscriptDisabled
		}
		var nextSeq int64
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM aip_secure_transcript WHERE session_id = ?`,
			string(item.SessionID)).Scan(&nextSeq)
		if err != nil {
			return opErr("transcript next seq", err)
		}
		_, err = tx.ExecContext(
			ctx, `INSERT INTO aip_secure_transcript(
		session_id, seq, turn_id, event_kind, payload_ref, created_at_unix
	) VALUES(?,?,?,?,?,?)`,
			string(item.SessionID), nextSeq, string(item.TurnID), item.EventKind, item.PayloadRef, item.CreatedAt.UnixNano(),
		)
		if err != nil {
			return mapUniqueErr(err)
		}
		if err := s.insertTurnIgnore(ctx, tx, string(item.SessionID), string(item.TurnID)); err != nil {
			return opErr("insert turn", err)
		}
		return nil
	})
}
