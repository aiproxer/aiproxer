package app

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Clock reports the current time for deterministic orchestration and tests.
type Clock interface {
	Now() time.Time
}

// RuleSource provides immutable concurrency rule snapshots.
type RuleSource interface {
	Snapshot(ctx context.Context) (RuleSnapshot, error)
}

// LeaseStore is the persistence port implemented by task 8.2 dialects.
type LeaseStore interface {
	Acquire(ctx context.Context, cmd AcquireCommand) (AcquireResult, error)
	Renew(ctx context.Context, cmd RenewCommand) (RenewResult, error)
	Release(ctx context.Context, cmd ReleaseCommand) (ReleaseResult, error)
	Query(ctx context.Context, q QueryCommand) (QueryResult, error)
	CheckReadiness(ctx context.Context) (domain.Readiness, error)

	// Atomic lease-set operations (Phase 6 / requirements 10.3–10.9).
	AcquireSet(ctx context.Context, cmd AcquireSetCommand) (AcquireSetResult, error)
	RenewSet(ctx context.Context, cmd RenewSetCommand) (RenewSetResult, error)
	ReleaseSet(ctx context.Context, cmd ReleaseSetCommand) (ReleaseSetResult, error)
	QuerySets(ctx context.Context, q QuerySetsCommand) (QuerySetsResult, error)
	MarkSetUncertain(ctx context.Context, setID string, now time.Time) error
}

// RuleSnapshot is the immutable rule set consumed by admit orchestration.
type RuleSnapshot struct {
	ID          string
	Version     string
	EffectiveAt time.Time
	FetchedAt   time.Time
	State       economics.SnapshotState
	Readiness   domain.Readiness
	Rules       []domain.Rule
}

// PolicyRef returns the bindable policy snapshot identity for this rule set.
func (s RuleSnapshot) PolicyRef() economics.PolicySnapshotRef {
	id := s.ID
	if id == "" {
		id = "concurrency"
	}
	return economics.PolicySnapshotRef{
		VersionRef: economics.VersionRef{
			ID:          id,
			Version:     s.Version,
			EffectiveAt: s.EffectiveAt,
			FetchedAt:   s.FetchedAt,
		},
		PolicyID: id,
	}
}

// SnapshotStateFromReadiness maps concurrency readiness onto economics.SnapshotState.
func SnapshotStateFromReadiness(r domain.Readiness) economics.SnapshotState {
	switch r.State {
	case domain.ReadinessStateReady:
		return economics.SnapshotReady
	case domain.ReadinessStateDegraded:
		return economics.SnapshotDegraded
	case domain.ReadinessStateUnavailable:
		return economics.SnapshotUnavailable
	case domain.ReadinessStateDisabled:
		return economics.SnapshotDisabled
	default:
		if r.State == "" {
			return economics.SnapshotReady
		}
		return economics.SnapshotUnavailable
	}
}

// AcquireCommand requests an idempotent lease insert under a capacity limit.
type AcquireCommand struct {
	Lease      domain.Lease
	RuleID     string
	Dimensions domain.Dimensions
	Limit      int
	Mode       domain.RuleMode
	Now        time.Time
}

// AcquireResult is the store outcome for one acquire attempt.
type AcquireResult struct {
	Lease            domain.Lease
	Replayed         bool
	RemainingSlots   int
	CapacityExceeded bool
	Rejected         bool
}

// RenewCommand extends a lease with generation CAS.
type RenewCommand struct {
	LeaseID            string
	RequestID          string
	ExpectedGeneration int64
	TTL                time.Duration
	Now                time.Time
}

// RenewResult is the store outcome for renew.
type RenewResult struct {
	Lease domain.Lease
}

// ReleaseCommand releases a lease idempotently.
type ReleaseCommand struct {
	LeaseID   string
	RequestID string
	Reason    string
	Now       time.Time
}

// ReleaseResult is the store outcome for release.
type ReleaseResult struct {
	Applied bool
	Lease   domain.Lease
}

// QueryCommand is a bounded lease query.
type QueryCommand struct {
	LeaseID   string
	RequestID string
	RuleID    string
	State     domain.LeaseState
	Now       time.Time
	Limit     int
}

// QueryResult is a bounded page of leases.
type QueryResult struct {
	Leases []domain.Lease
}

// AdmitInput is application-level lease admission input.
type AdmitInput struct {
	RequestID      string
	Scope          scope.PrincipalScopeView
	Namespace      string
	TTL            time.Duration
	BoundVersion   economics.PolicySnapshotRef
	IdempotencyKey string
	Lifecycle      metering.LifecycleScope
	ParentLeaseID  string
	AuxPolicy      domain.AuxPolicy
	RuleID         string
}

// AdmittedLease is one rule occupancy acquired or replayed during Admit.
type AdmittedLease struct {
	LeaseID         string
	RuleID          string
	Generation      int64
	ExpiresAt       time.Time
	RenewBefore     time.Duration
	TTL             time.Duration
	FailureBehavior domain.FailureBehavior
	Acquired        bool
	Replayed        bool
}

// AdmitResult is the application admit outcome.
//
// For multi-rule allow, scalar LeaseID/Generation/... are the primary lease
// (last successfully acquired / lastAllow) for backward compatibility. Leases
// lists every occupancy acquired or replayed in this Admit.
type AdmitResult struct {
	Kind            domain.DecisionKind
	LeaseID         string
	Generation      int64
	ExpiresAt       time.Time
	RemainingSlots  int
	Readiness       domain.Readiness
	BoundVersion    economics.PolicySnapshotRef
	Evidence        domain.Evidence
	Acquired        bool
	Replayed        bool
	RuleID          string
	RenewBefore     time.Duration
	TTL             time.Duration
	FailureBehavior domain.FailureBehavior
	// Leases holds all rule occupancies from this Admit. It is empty on deny,
	// and on error every lease acquired by this Admit is rolled back.
	Leases []AdmittedLease
	SetID  string
}

// RenewInput is application-level lease renewal input.
type RenewInput struct {
	LeaseID            string
	RequestID          string
	ExpectedGeneration int64
	TTL                time.Duration
	SetID              string
	RenewBefore        time.Duration
}

// ReleaseInput is application-level lease release input.
type ReleaseInput struct {
	LeaseID   string
	RequestID string
	Reason    string
	SetID     string
}

// AcquireSetMember is one rule occupancy proposed for an atomic set acquire.
type AcquireSetMember struct {
	Lease      domain.Lease
	RuleID     string
	Dimensions domain.Dimensions
	Limit      int
	Mode       domain.RuleMode
}

// AcquireSetCommand requests atomic multi-rule occupancy for one logical request.
type AcquireSetCommand struct {
	SetID       string
	RequestID   string
	Members     []AcquireSetMember
	TTL         time.Duration
	RenewBefore time.Duration
	Now         time.Time
}

// AcquireSetResult is the store outcome for one set acquire/replay.
type AcquireSetResult struct {
	Set              domain.LeaseSet
	Replayed         bool
	RemainingSlots   int
	CapacityExceeded bool
	Rejected         bool
	LockOrder        []string
	DenyingRuleID    string
}

// RenewSetCommand renews every member of a lease set under one generation CAS.
type RenewSetCommand struct {
	SetID              string
	RequestID          string
	ExpectedGeneration int64
	TTL                time.Duration
	RenewBefore        time.Duration
	Now                time.Time
}

// RenewSetResult is the store outcome for set renew.
type RenewSetResult struct {
	Set       domain.LeaseSet
	Uncertain bool
}

// ReleaseSetCommand releases an entire lease set idempotently.
type ReleaseSetCommand struct {
	SetID     string
	RequestID string
	Reason    string
	Now       time.Time
}

// ReleaseSetResult is the store outcome for set release.
type ReleaseSetResult struct {
	Applied bool
	Set     domain.LeaseSet
}

// QuerySetsCommand is a bounded lease-set query.
type QuerySetsCommand struct {
	SetID     string
	RequestID string
	State     domain.LeaseSetState
	Now       time.Time
	Limit     int
}

// LeaseSetOccupancyCounts is a bounded control-plane projection of set states.
type LeaseSetOccupancyCounts struct {
	Active    int
	Uncertain int
	Expiring  int
	Released  int
	Failed    int
}

// QuerySetsResult is a bounded page of lease sets.
type QuerySetsResult struct {
	Sets []domain.LeaseSet
}
