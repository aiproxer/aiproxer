package proxymemorystore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/uptrace/bun"
)

// ALegState is the narrow core continuity state one advisory memory operation
// needs: whether the authoritative A-leg still exists, and its next B-leg
// sequence.
type ALegState struct {
	// Active reports whether the core continuity database still holds the
	// A-leg row.
	Active bool
	// NextBSeq is the A-leg's next B-leg sequence, the durable order of the
	// requests that ran on it.
	NextBSeq int64
}

// CoreALegs is the narrow read-only view of the core continuity database that
// the advisory memory store needs.
//
// It is deliberately not a database handle: the memory store must never write
// a core table. Every call is a short, independent read that completes before
// a memory transaction starts, because the memory database is isolated from
// the core database and a memory transaction must never hold a core
// connection.
type CoreALegs interface {
	// ALegState returns the core state of one A-leg. A missing A-leg is
	// reported as an inactive state, not as an error.
	ALegState(ctx context.Context, aLegID string) (ALegState, error)
	// ActiveALegs reports which of the given A-legs still exist in the core
	// continuity database. It answers one bounded query for a page of
	// candidates, so a bounded maintenance sweep does not read the core
	// database once per row.
	ActiveALegs(ctx context.Context, aLegIDs []string) (map[string]bool, error)
}

// ALegReader reads the core continuity A-leg row through the core database
// handle. It performs one short read per call and holds no connection across
// a memory transaction.
type ALegReader struct {
	db *bun.DB
}

var _ CoreALegs = (*ALegReader)(nil)

// NewALegReader returns a reader over the core continuity database handle.
func NewALegReader(db *bun.DB) *ALegReader {
	return &ALegReader{db: db}
}

// ALegState reads the A-leg's next B-leg sequence. A missing row means the
// A-leg is no longer active.
func (r *ALegReader) ALegState(ctx context.Context, aLegID string) (ALegState, error) {
	if r == nil || r.db == nil {
		return ALegState{}, errors.New("proxymemorystore: no core A-leg reader")
	}
	if ctx == nil {
		return ALegState{}, errors.New("proxymemorystore: nil core A-leg context")
	}
	var next int64
	err := r.db.NewRaw(`SELECT next_b_seq FROM a_legs WHERE a_leg_id = ?`, aLegID).Scan(ctx, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return ALegState{}, nil
	}
	if err != nil {
		return ALegState{}, opErr("read core A-leg", err)
	}
	return ALegState{Active: true, NextBSeq: next}, nil
}

// ActiveALegs reports which of the given A-legs still exist in the core
// continuity database. An empty candidate list answers an empty map.
func (r *ALegReader) ActiveALegs(ctx context.Context, aLegIDs []string) (map[string]bool, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("proxymemorystore: no core A-leg reader")
	}
	if ctx == nil {
		return nil, errors.New("proxymemorystore: nil core A-leg context")
	}
	active := make(map[string]bool, len(aLegIDs))
	if len(aLegIDs) == 0 {
		return active, nil
	}
	args := make([]any, 0, len(aLegIDs))
	for _, aLegID := range aLegIDs {
		args = append(args, aLegID)
	}
	var found []string
	err := r.db.NewRaw(
		`SELECT a_leg_id FROM a_legs WHERE a_leg_id IN (`+placeholders(len(aLegIDs))+`)`,
		args...).Scan(ctx, &found)
	if err != nil {
		return nil, opErr("read core A-legs", err)
	}
	for _, aLegID := range found {
		active[aLegID] = true
	}
	return active, nil
}
