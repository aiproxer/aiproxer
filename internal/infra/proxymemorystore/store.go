package proxymemorystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/uptrace/bun"
)

// Store is the Bun-backed proxymemory.Store implementation. Every method runs
// one self-contained statement or transaction on the memory database pool. The
// only in-process state is the retired-binding sweep's continuation cursor and
// the mutex that guards it.
//
// The memory database is isolated from the core continuity database: the
// memory tables live in their own database, so a memory transaction can never
// hold a core connection. The core A-leg facts the store needs are read
// through the narrow CoreALegs view, outside every memory transaction.
type Store struct {
	db   *bun.DB
	core CoreALegs
	// sweepMu serializes the retired-binding sweep of one store: the sweep's
	// continuation cursor is shared state, so two concurrent sweeps must not
	// read the same candidates or advance the cursor past each other. It is
	// not a store-wide lock: the capture, admission and claim paths never
	// take it.
	sweepMu sync.Mutex
	// sweepAfter is the continuation cursor of the retired-binding sweep: the
	// last A-leg the sweep resolved. The next sweep continues after it, so
	// successive invocations walk the binding table instead of rescanning its
	// first page.
	sweepAfter string
}

// Compile-time assertion that the store satisfies the feature-owned contract.
var _ proxymemory.Store = (*Store)(nil)

// New creates a Store over the memory database db. The core continuity facts
// come from the narrow core view. The caller owns db and applies EnsureSchema
// before first use.
func New(db *bun.DB, core CoreALegs) *Store {
	return &Store{db: db, core: core}
}

// PublishedSummary returns the bank's published summary and version per design
// §12.6: SELECT memory_summary, version FROM proxy_memory_banks WHERE <owner>.
// A missing row or an empty summary returns ErrNotFound.
func (s *Store) PublishedSummary(ctx context.Context, o proxymemory.Owner) (string, int64, error) {
	var summary string
	var version int64
	err := s.db.NewRaw(`
		SELECT memory_summary, version FROM proxy_memory_banks
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
	`, o.TenantID, o.PrincipalID, o.ProjectKey).Scan(ctx, &summary, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, proxymemory.ErrNotFound
	}
	if err != nil {
		return "", 0, opErr("select published summary", err)
	}
	if summary == "" {
		return "", 0, proxymemory.ErrNotFound
	}
	return summary, version, nil
}

// opErr prefixes a store operation failure with the package name.
func opErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("proxymemorystore: %s: %w", op, err)
}
