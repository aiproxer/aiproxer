package controlplane

import (
	"context"
	"time"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// EventAppender is the recorder-owned port for appending normalized
// control-plane evidence (requirement 1.7, 1.8). Append is single-event and
// returns the stable identity assigned by the backing store.
type EventAppender interface {
	Append(ctx context.Context, ev cp.Event) (cp.RecordResult, error)
}

// QuerySource is the query-service-owned port for bounded control-plane read
// views (requirement 2.1-2.9, 9.1, 9.5). Query methods return bounded pages and
// report unsupported filters explicitly.
type QuerySource interface {
	Sessions(ctx context.Context, q cp.SessionQuery) (cp.Page[cp.SessionSummary], error)
	Attempts(ctx context.Context, q cp.AttemptQuery) (cp.Page[cp.AttemptRow], error)
	Usage(ctx context.Context, q cp.UsageQuery) (cp.Page[cp.UsageRow], error)
	UsageAggregate(ctx context.Context, q cp.UsageAggregateQuery) (cp.Page[cp.UsageAggregate], error)
	PolicyAudit(ctx context.Context, q cp.EvidenceQuery) (cp.Page[cp.PolicyAuditRow], error)
	Events(ctx context.Context, q cp.EventQuery) (cp.Page[cp.Event], error)
}

// RetentionApplier is the retention-controller-owned port for mutating only
// control-plane evidence outside the retention window (requirement 6.1, 6.2,
// 6.6). It never mutates in-flight runtime stores.
type RetentionApplier interface {
	ApplyRetention(ctx context.Context, cmd RetentionCommand) (RetentionResult, error)
}

// ReadinessProbe reports backing capability availability without leaking raw
// infrastructure errors (requirement 7.1, 7.3).
type ReadinessProbe interface {
	CheckReadiness(ctx context.Context) error
}

// Store is the composed adapter contract for persistence implementations that
// provide append, query, retention, and readiness capabilities. Core services
// consume the narrower interfaces above; runtime wiring and store contract tests
// use Store when they need the complete backing capability. SQL, Bun, HTTP,
// transport, and provider SDK types never cross this boundary.
type Store interface {
	EventAppender
	QuerySource
	RetentionApplier
	ReadinessProbe
}

// RetentionProfile names a retention/redaction profile applied to control-plane
// evidence (requirement 6.1, 6.2).
type RetentionProfile string

const (
	RetentionProfileStandard RetentionProfile = "standard"
	RetentionProfileStrict   RetentionProfile = "strict"
)

// IsKnown reports whether p is one of the documented retention profiles.
func (p RetentionProfile) IsKnown() bool {
	switch p {
	case RetentionProfileStandard, RetentionProfileStrict:
		return true
	}
	return false
}

// RetentionCommand is the batch input for retention/redaction processing
// (requirement 6.1, 6.2, 6.6). It mutates only control-plane evidence and
// never routing, policy, usage, or session outcomes for active requests.
type RetentionCommand struct {
	Cutoff     time.Time
	Profile    RetentionProfile
	Visibility cp.Visibility
}

// RetentionResult is the batch output of a retention/redaction run. Aggregate
// counts are bounded safe integers; status carries any degradation reason.
type RetentionResult struct {
	Marked int
	Pruned int
	Status cp.CapabilityStatus
}

// Clock is the consumer-owned time port so tests can drive deterministic time
// without real sleeps (requirement 1.7, testing steering).
type Clock interface {
	Now() time.Time
}

// SystemClock is the production Clock backed by time.Now.
type SystemClock struct{}

// Now returns the current real time.
func (SystemClock) Now() time.Time { return time.Now() }

// EventIDGenerator produces stable opaque EventID values for a store-local
// sequence (requirement 1.7, 2.7). The store assigns the monotonic sequence;
// the generator binds it to a stable store id so consumers can page,
// deduplicate, and correlate deterministically.
type EventIDGenerator interface {
	NewEventID(sequence int64) cp.EventID
}

// MonotonicIDGenerator is a deterministic EventIDGenerator that pairs a stable
// store id with the store-assigned sequence. It is safe for concurrent use
// only when callers do not mutate StoreID after construction.
type MonotonicIDGenerator struct {
	StoreID string
}

// NewMonotonicIDGenerator returns a MonotonicIDGenerator bound to storeID.
func NewMonotonicIDGenerator(storeID string) *MonotonicIDGenerator {
	return &MonotonicIDGenerator{StoreID: storeID}
}

// NewEventID returns the stable EventID for the supplied store-local sequence.
func (g *MonotonicIDGenerator) NewEventID(sequence int64) cp.EventID {
	return cp.EventID{StoreID: g.StoreID, Sequence: sequence}
}
