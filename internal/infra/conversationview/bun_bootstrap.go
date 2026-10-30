package conversationview

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

var _ BootstrapStore = (*BunStore)(nil)

func (m *BunStore) BootstrapSteering(ctx context.Context, aLegID, producerID string, decide BootstrapDecide) (BootstrapResult, error) {
	if err := ctx.Err(); err != nil {
		return BootstrapResult{}, err
	}
	if err := validateBootstrapScope(aLegID, producerID); err != nil {
		return BootstrapResult{}, err
	}
	var out BootstrapResult
	err := m.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// SQLite must reserve the writer before ANY reads (not just state reads).
		if m.db.Dialect().Name() == dialect.SQLite {
			res, err := tx.NewRaw(`UPDATE a_legs SET next_b_seq = next_b_seq WHERE a_leg_id = ?`, aLegID).Exec(ctx)
			if err != nil {
				return ErrBootstrapStorage
			}
			n, err := res.RowsAffected()
			if err != nil {
				return ErrBootstrapStorage
			}
			if n != 1 {
				return ErrALegNotFound
			}
		}
		q := `SELECT next_b_seq FROM a_legs WHERE a_leg_id = ?`
		if m.db.Dialect().Name() == dialect.PG {
			q += ` FOR UPDATE`
		}
		var nextSeq int64
		if err := tx.NewRaw(q, aLegID).Scan(ctx, &nextSeq); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrALegNotFound
			}
			return ErrBootstrapStorage
		}
		var completion BootstrapCompletion
		err := tx.NewRaw(`SELECT outcome, matched_count, model_evidence FROM a_leg_steering_bootstrap WHERE a_leg_id = ? AND producer_id = ?`, aLegID, producerID).Scan(ctx, &completion.Outcome, &completion.MatchedCount, &completion.Model)
		if err == nil {
			out = BootstrapResult{Completion: completion, Reused: true}
			return ctx.Err()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return ErrBootstrapStorage
		}
		decision, err := bootstrapDecision(nextSeq > 0, decide)
		if err != nil {
			return err
		}
		rev, nextSlot, err := m.loadStateTx(ctx, tx, aLegID)
		if err != nil {
			return ErrBootstrapStorage
		}
		tags, err := m.loadTagSetTx(ctx, tx, aLegID)
		if err != nil {
			return ErrBootstrapStorage
		}
		live := &legView{revision: uint64(rev), nextSlot: uint64(nextSlot), tags: make(map[MessageIdentity]Tag, len(tags)), steering: make(map[string]*SteeringOverlay)}
		for id := range tags {
			live.tags[id] = Tag{}
		}
		// Include inactive rows: orphaned producer IDs must not be overwritten.
		var ids []string
		if err := tx.NewRaw(`SELECT overlay_id FROM a_leg_steering_overlays WHERE a_leg_id = ?`, aLegID).Scan(ctx, &ids); err != nil {
			return ErrBootstrapStorage
		}
		for _, id := range ids {
			ov, _, err := m.loadOverlayTx(ctx, tx, aLegID, id)
			if err != nil {
				return ErrBootstrapStorage
			}
			live.steering[id] = &ov
		}
		staged, mutations, err := stageBootstrap(live, producerID, decision, time.Now().UTC(), math.MaxInt64)
		if err != nil {
			return err
		}
		for _, mutation := range mutations {
			if err := m.upsertOverlayTx(ctx, tx, aLegID, *staged.steering[mutation.OverlayID]); err != nil {
				return ErrBootstrapStorage
			}
		}
		if len(mutations) > 0 {
			if err := m.upsertStateTx(ctx, tx, aLegID, int64(staged.revision), int64(staged.nextSlot)); err != nil {
				return ErrBootstrapStorage
			}
		}
		completion = bootstrapCompletion(decision)
		if _, err := tx.NewRaw(`INSERT INTO a_leg_steering_bootstrap(a_leg_id,producer_id,outcome,matched_count,model_evidence) VALUES(?,?,?,?,?)`, aLegID, producerID, string(completion.Outcome), completion.MatchedCount, completion.Model).Exec(ctx); err != nil {
			return ErrBootstrapStorage
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		out = BootstrapResult{Completion: completion, Mutations: mutations}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return BootstrapResult{}, ctx.Err()
		}
		// Do not surface driver errors that might include SQL values/prompt text.
		for _, safe := range []error{ErrALegNotFound, ErrBootstrapInvalid, ErrBootstrapCollision, ErrBootstrapDecision, ErrSteeringLimitExceeded, ErrSteeringAnchorExcluded, ErrRevisionExhausted} {
			if errors.Is(err, safe) {
				return BootstrapResult{}, safe
			}
		}
		return BootstrapResult{}, ErrBootstrapStorage
	}
	return out, nil
}

// BootstrapDDL mirrors the forward continuity migration for EnsureSchema.
// The FK is the existing authoritative retirement mechanism in both dialects.
const BootstrapDDL = `CREATE TABLE IF NOT EXISTS a_leg_steering_bootstrap (
 a_leg_id TEXT NOT NULL,
 producer_id TEXT NOT NULL,
 outcome TEXT NOT NULL,
 matched_count INTEGER NOT NULL,
 model_evidence TEXT NOT NULL,
 PRIMARY KEY(a_leg_id, producer_id),
 FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
)`
