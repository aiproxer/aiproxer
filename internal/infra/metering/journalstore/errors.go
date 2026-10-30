package journalstore

import (
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// ErrIdentityCollision is returned when Append sees the same SourceEventKey
// (or legacy IdempotencyKey row) with a different Sequence, Kind, or
// double-count-sensitive payload than the stored fact (requirements 3.1, 3.4, 13.4).
var ErrIdentityCollision = errors.New("metering/journalstore: fact identity collision")

// ErrUniqueRaceMissingRow is returned when a unique-constraint race on Append
// is followed by a lookup that finds no winning row. Callers should treat this
// as a transient inconsistency and retry.
var ErrUniqueRaceMissingRow = errors.New("metering/journalstore: unique race missing winner row")

// ErrSupersessionTarget is returned when a correction/replacement supersedes a
// missing fact or a fact outside the same stream (requirements 6.6, D7).
var ErrSupersessionTarget = errors.New("metering/journalstore: supersession target invalid")

// ErrSupersessionCycle is returned when supersession edges would form a cycle
// (requirements 6.7, D7).
var ErrSupersessionCycle = errors.New("metering/journalstore: supersession cycle")

// ErrQueryTooBroad is returned when List lacks a required selective bound so the
// store cannot safely page without scanning (requirement 14.4/14.8).
var ErrQueryTooBroad = metering.ErrQueryTooBroad

// ErrInvalidCursor identifies a cursor that is malformed or was created for
// a different store/filter. V2 cursors are opaque and filter-bound.
var ErrInvalidCursor = errors.New("metering/journalstore: invalid cursor")

// ErrQueryOutOfScope identifies a query that attempts to use another store's
// namespace or an untrusted subject scope.
var ErrQueryOutOfScope = errors.New("metering/journalstore: query out of scope")

// ErrPageSizeExceeded is returned by V2 bounded queries when a caller asks
// for more than the hard safety bound.
var ErrPageSizeExceeded = errors.New("metering/journalstore: page size exceeds hard maximum")
