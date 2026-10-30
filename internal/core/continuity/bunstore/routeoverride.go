package bunstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Snapshot returns a complete value copy of the A-leg override state.
func (s *Store) Snapshot(ctx context.Context, aLegID string) (routeoverride.State, error) {
	return s.readOverride(ctx, aLegID)
}

// Get uses the same read semantics as Snapshot.
func (s *Store) Get(ctx context.Context, aLegID string) (routeoverride.State, error) {
	return s.readOverride(ctx, aLegID)
}

func (s *Store) readOverride(ctx context.Context, aLegID string) (routeoverride.State, error) {
	if err := ctx.Err(); err != nil {
		return routeoverride.State{}, err
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return routeoverride.State{}, routeoverride.ErrNotFound
	}
	var out routeoverride.State
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := s.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		st, err := s.loadOverrideTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		if err := s.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		out = st
		return nil
	})
	if err != nil {
		return routeoverride.State{}, err
	}
	return out, nil
}

// Replace activates or replaces the A-leg override selector.
func (s *Store) Replace(ctx context.Context, aLegID, selector string, now time.Time) (routeoverride.State, error) {
	if err := ctx.Err(); err != nil {
		return routeoverride.State{}, err
	}
	normalized, err := normalizeStoredSelector(selector)
	if err != nil {
		return routeoverride.State{}, err
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return routeoverride.State{}, routeoverride.ErrNotFound
	}
	var out routeoverride.State
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := s.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		current, err := s.loadOverrideTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		if current.Active && current.Selector == normalized {
			if err := s.touchALegTx(ctx, tx, aLegID); err != nil {
				return err
			}
			out = current
			return nil
		}
		nextRev, err := nextOverrideRevision(current.Revision)
		if err != nil {
			return err
		}
		next := routeoverride.State{
			ALegID:    aLegID,
			Active:    true,
			Selector:  normalized,
			Revision:  nextRev,
			UpdatedAt: now.UTC(),
		}
		if err := next.Validate(); err != nil {
			return fmt.Errorf("%w: %v", routeoverride.ErrInvalidSelector, err)
		}
		if err := s.upsertOverrideTx(ctx, tx, next); err != nil {
			return err
		}
		if err := s.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		out = next
		return nil
	})
	if err != nil {
		return routeoverride.State{}, err
	}
	return out.Clone(), nil
}

// Clear deactivates the A-leg override. Already-inactive state is a no-op.
func (s *Store) Clear(ctx context.Context, aLegID string, now time.Time) (routeoverride.State, error) {
	if err := ctx.Err(); err != nil {
		return routeoverride.State{}, err
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return routeoverride.State{}, routeoverride.ErrNotFound
	}
	var out routeoverride.State
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := s.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		current, err := s.loadOverrideTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		if !current.Active {
			if err := s.touchALegTx(ctx, tx, aLegID); err != nil {
				return err
			}
			out = current
			return nil
		}
		nextRev, err := nextOverrideRevision(current.Revision)
		if err != nil {
			return err
		}
		next := routeoverride.State{
			ALegID:    aLegID,
			Revision:  nextRev,
			UpdatedAt: now.UTC(),
		}
		if err := next.Validate(); err != nil {
			return err
		}
		if err := s.upsertOverrideTx(ctx, tx, next); err != nil {
			return err
		}
		if err := s.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		out = next
		return nil
	})
	if err != nil {
		return routeoverride.State{}, err
	}
	return out.Clone(), nil
}

func (s *Store) lockALegTx(ctx context.Context, tx bun.Tx, aLegID string) error {
	q := `SELECT a_leg_id FROM a_legs WHERE a_leg_id = ?`
	if s.db.Dialect().Name() == dialect.PG {
		q = `SELECT a_leg_id FROM a_legs WHERE a_leg_id = ? FOR UPDATE`
	}
	var id string
	err := tx.NewRaw(q, aLegID).Scan(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return routeoverride.ErrNotFound
	}
	if err != nil {
		return opErr("lock a leg for route override", err)
	}
	return nil
}

func (s *Store) loadOverrideTx(ctx context.Context, tx bun.Tx, aLegID string) (routeoverride.State, error) {
	var active int
	var selector string
	var revision int64
	var updatedAt int64
	err := tx.NewRaw(
		`SELECT active, selector, revision, updated_at_unix FROM a_leg_route_overrides WHERE a_leg_id = ?`,
		aLegID,
	).Scan(ctx, &active, &selector, &revision, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return routeoverride.Inactive(aLegID), nil
	}
	if err != nil {
		return routeoverride.State{}, opErr("select route override", err)
	}
	st := routeoverride.State{
		ALegID:   aLegID,
		Active:   active != 0,
		Selector: selector,
		Revision: revision,
	}
	if revision != 0 {
		st.UpdatedAt = time.Unix(0, updatedAt).UTC()
	}
	if err := st.Validate(); err != nil {
		return routeoverride.State{}, fmt.Errorf("bunstore: stored route override: %w", err)
	}
	return st, nil
}

func (s *Store) touchALegTx(ctx context.Context, tx bun.Tx, aLegID string) error {
	_, err := tx.NewRaw(
		`UPDATE a_legs SET last_seen_at_unix = ? WHERE a_leg_id = ?`,
		time.Now().UnixNano(),
		aLegID,
	).Exec(ctx)
	if err != nil {
		return opErr("touch a leg last seen", err)
	}
	return nil
}

func (s *Store) upsertOverrideTx(ctx context.Context, tx bun.Tx, st routeoverride.State) error {
	active := 0
	if st.Active {
		active = 1
	}
	_, err := tx.NewRaw(`
INSERT INTO a_leg_route_overrides(a_leg_id, active, selector, revision, updated_at_unix)
VALUES(?,?,?,?,?)
ON CONFLICT(a_leg_id) DO UPDATE SET
	active=excluded.active,
	selector=excluded.selector,
	revision=excluded.revision,
	updated_at_unix=excluded.updated_at_unix
`, st.ALegID, active, st.Selector, st.Revision, st.UpdatedAt.UnixNano()).Exec(ctx)
	if err != nil {
		return opErr("upsert route override", err)
	}
	return nil
}

func normalizeStoredSelector(raw string) (string, error) {
	normalized := routeoverride.NormalizeSelector(raw)
	if normalized == "" {
		return "", fmt.Errorf("%w: empty selector", routeoverride.ErrInvalidSelector)
	}
	if len(normalized) > aipapi.MaxRouteSelectorBytes {
		return "", fmt.Errorf("%w: selector exceeds %d bytes", routeoverride.ErrInvalidSelector, aipapi.MaxRouteSelectorBytes)
	}
	return normalized, nil
}

func nextOverrideRevision(current int64) (int64, error) {
	if current == math.MaxInt64 {
		return 0, routeoverride.ErrRevisionExhausted
	}
	return current + 1, nil
}
