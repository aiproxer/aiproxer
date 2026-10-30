package ledgerstore

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// queryPrep carries the shared prepared state for a bounded query: the resolved
// page limit, the decoded and shape-checked cursor payload, and the resolved
// visibility. It deliberately excludes store-specific filtering, scanning, and
// pagination tails so adapters keep their per-view logic explicit.
type queryPrep struct {
	limit      int
	cursor     cursorPayload
	visibility cp.Visibility
}

// prepareQuery performs the shared ctx/limit/cursor/visibility setup used by
// every MemoryStore and DurableStore query view. It returns a queryPrep on
// success or the first error encountered. Store-specific filtering, scanning,
// and pagination stay in the caller (requirement 2.6, 2.7, 4.6).
func prepareQuery(ctx context.Context, defaultPageSize, maxPageSize, limit int, cursor cp.Cursor, shape uint64, visibility cp.Visibility) (queryPrep, error) {
	if err := ctx.Err(); err != nil {
		return queryPrep{}, err
	}
	lim, err := resolveLimit(limit, defaultPageSize, maxPageSize)
	if err != nil {
		return queryPrep{}, err
	}
	cur, err := decodeAndCheckCursor(cursor, shape)
	if err != nil {
		return queryPrep{}, err
	}
	return queryPrep{limit: lim, cursor: cur, visibility: resolveVisibility(visibility)}, nil
}

// resolveLimit applies the default page size when a query omits Limit and
// rejects pages larger than the configured maximum (requirement 2.6).
func resolveLimit(limit, defaultPageSize, maxPageSize int) (int, error) {
	if limit <= 0 {
		return defaultPageSize, nil
	}
	if limit > maxPageSize {
		return 0, fmt.Errorf("%w: limit %d exceeds max page size %d", controlplane.ErrTooBroad, limit, maxPageSize)
	}
	return limit, nil
}

// decodeAndCheckCursor decodes the opaque cursor and verifies its shape hash
// matches the current query shape so cursors cannot be replayed across
// incompatible views (requirement 2.7).
func decodeAndCheckCursor(cursor cp.Cursor, shape uint64) (cursorPayload, error) {
	cur, err := decodeCursor(cursor)
	if err != nil {
		return cursorPayload{}, fmt.Errorf("%w: %v", controlplane.ErrInvalidQuery, err)
	}
	if !cur.IsZero() && cur.ShapeHash != shape {
		return cursorPayload{}, fmt.Errorf("%w: cursor shape mismatch", controlplane.ErrInvalidQuery)
	}
	return cur, nil
}
