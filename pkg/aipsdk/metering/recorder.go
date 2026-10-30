package metering

import "context"

// Recorder appends metering facts to a durable journal. Implementations must
// treat SameFactReplay replays as idempotent no-ops and must not mutate
// prior fact bodies in place; corrections use dedicated FactKind values.
// SameFactIdentity alone is stream membership; differing Kind or payload for
// the same FactID/stream/sequence is an identity collision.
type Recorder interface {
	Append(ctx context.Context, fact Fact) error
}
