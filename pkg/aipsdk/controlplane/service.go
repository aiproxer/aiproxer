package controlplane

import "context"

// Recorder is the stable service contract for appending control-plane
// evidence (requirement 1.7, 5.1, 5.4). Implementations apply recording policy
// and capability status without altering normal runtime outcomes.
//
// Preconditions: ctx is non-nil; ev has passed [Event.Validate] or the
// implementation rejects it with [ErrCodeUnsafeEvidence].
// Postconditions: the returned RecordResult carries the stable identity
// assigned by the backing store and the dedupe outcome for the source event
// key.
// Invariants: event IDs are stable within a configured backing store; post-
// output recording failures never request retry, failover, or replacement.
type Recorder interface {
	Record(ctx context.Context, ev Event) (RecordResult, error)
}

// StatusReader is the stable service contract for reading the control-plane
// capability status (requirement 7.1, 7.4).
type StatusReader interface {
	Status(ctx context.Context) (CapabilityStatus, error)
}

// ReadinessReportReader returns independent authority/journal readiness rows
// and aggregate protected-traffic posture (15.7).
type ReadinessReportReader interface {
	Report(ctx context.Context) (ReadinessReport, error)
}

// PageQueries is the stable service contract for bounded cross-session read
// views (requirement 2.1-2.9, 9.1, 9.5). Query consumers do not need to know
// which diagnostic, observer, ledger, or store supplied a result.
//
// Preconditions: ctx is non-nil; query bounds and cursor shape are validated
// by the implementation before store access.
// Postconditions: results contain only safe fields, report unsupported
// filters explicitly, and distinguish disabled capability, empty matches,
// unavailable evidence, and unsupported capability.
type PageQueries interface {
	Sessions(ctx context.Context, q SessionQuery) (Page[SessionSummary], error)
	Attempts(ctx context.Context, q AttemptQuery) (Page[AttemptRow], error)
	Usage(ctx context.Context, q UsageQuery) (Page[UsageRow], error)
	UsageAggregate(ctx context.Context, q UsageAggregateQuery) (Page[UsageAggregate], error)
	PolicyAudit(ctx context.Context, q EvidenceQuery) (Page[PolicyAuditRow], error)
	Events(ctx context.Context, q EventQuery) (Page[Event], error)
}

// Queries composes status and bounded read views for consumers that expose the
// complete protected control-plane query surface.
type Queries interface {
	StatusReader
	PageQueries
}
