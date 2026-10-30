package usagelimit

import (
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// ReservationDescriptor is the app-owned, per-rule mutation descriptor. A
// reservation set is passed to the store as one logical operation so every
// matched strict rule observes the same atomic commit boundary.
type ReservationDescriptor struct {
	RuleID         string
	Kind           RuleKind
	Unit           AmountUnit
	Dimensions     Dimensions
	ReservationKey ReservationKey
	ReservationID  string
	Amount         Amount
	SourceKey      string
	Authority      AuthorityLevel
}

// ReservationSet is the complete set of strict rule reservations for one
// logical request/B-leg mutation.
type ReservationSet []ReservationDescriptor

// SettlementDescriptor carries one rule's reserved and final enforceable
// amounts. The store applies all descriptors in one atomic operation.
type SettlementDescriptor struct {
	Reservation          ReservationDescriptor
	FinalUsage           Amount
	EstimatedUsage       Amount
	SourceKey            string
	Sequence             int
	Authority            AuthorityLevel
	MeasurementAuthority MeasurementAuthority
}

// MeasurementAuthority describes the evidence authority for quantity facts.
type MeasurementAuthority struct {
	Usage AuthorityLevel
}

func (a MeasurementAuthority) ForUnit(_ AmountUnit) AuthorityLevel {
	if a.Usage != "" {
		return a.Usage
	}
	return AuthorityLevelAny
}

// ReleaseDescriptor carries one rule's reservation to release. The store
// applies all descriptors in one atomic operation.
type ReleaseDescriptor struct {
	Reservation ReservationDescriptor
	SourceKey   string
	Sequence    int
}

// AdmissionReservation reports one applied reservation created by Admit.
// The legacy single-reservation fields on AdmissionResult remain the primary
// reservation for compatibility with older callers.
type AdmissionReservation struct {
	ReservationID  string
	RuleID         string
	ReservedAmount Amount
}

// AdmissionInput carries the safe quantity data required for pre-backend quota admission.
type AdmissionInput struct {
	Correlation    controlplane.Correlation
	Scope          scope.PrincipalScopeView
	Dimensions     Dimensions
	Request        Amount
	RequestCount   Amount
	PreflightUsage PreflightUsage
	Authority      AuthorityLevel
	ReservationKey ReservationKey
	EstimateOnly   bool
	// SkipEvidence suppresses durable policy/accounting evidence projection.
	// Clamp preview uses this with EstimateOnly so repeated preview admits do
	// not write audit noise; estimate-only precheck denials still record.
	SkipEvidence bool
	// Phase 7.2: lifecycle stage for rule filtering; exposure/facts for amount selection.
	LifecycleScope metering.LifecycleScope
	Perspective    metering.EconomicPerspective
	Exposure       economics.ExposureBasis
	Facts          []metering.Fact
}

// AdmissionResult reports the legal pre-backend accounting decision.
type AdmissionResult struct {
	Allowed bool
	Outcome DecisionOutcome
	RuleIDs []string
	// AdvisoryRuleIDs are the matched rules configured with RuleModeAdvisory.
	// They are tracked separately from RuleIDs so the runtime can apply final
	// advisory usage to advisory windows even when no strict reservation was
	// created (requirement 7.7). Advisory rules never reserve.
	AdvisoryRuleIDs []string
	// UnreservedRuleIDs are matched rules whose allowed execution created no
	// reservation (advisory and strict fail-open paths). Their final usage is
	// applied through ApplyUsage at lifecycle completion.
	UnreservedRuleIDs []string
	// SelectedRuleID identifies the rule whose outcome won admission severity.
	// RuleIDs remains the complete matched set for multi-rule evidence.
	SelectedRuleID  string
	RuleKind        RuleKind
	ReservationID   string
	Reserved        bool
	ReservedAmount  Amount
	Reservations    []AdmissionReservation
	PolicyRecord    policydecision.Record
	AccountingEvent controlplane.Event
	// BoundVersion is the policy snapshot identity captured at admission (11.2).
	BoundVersion economics.PolicySnapshotRef
	// BoundRatingVersion is the rating/catalog snapshot identity when known (14.3).
	BoundRatingVersion economics.RatingSnapshotRef
}

// SettlementKind classifies the settlement path.
type SettlementKind string

const (
	SettlementKindFinal        SettlementKind = "final"
	SettlementKindPartial      SettlementKind = "partial"
	SettlementKindUnavailable  SettlementKind = "unavailable"
	SettlementKindCancellation SettlementKind = "cancellation"
	SettlementKindSwallowed    SettlementKind = "swallowed"
	SettlementKindLosing       SettlementKind = "losing"
)

// SettleInput carries the safe request data required for surfaced-attempt
// settlement orchestration.
type SettleInput struct {
	Reservations   []SettlementDescriptor
	Correlation    controlplane.Correlation
	Scope          scope.PrincipalScopeView
	ReservationKey ReservationKey
	ReservationID  string
	RuleID         string
	Kind           SettlementKind
	// Sequence distinguishes partial, cancellation, final, and authoritative
	// reconciliation mutations that share one reservation identity.
	Sequence             int
	FinalUsage           Amount
	ReservedUsage        Amount
	EstimatedUsage       Amount
	Authority            AuthorityLevel
	MeasurementAuthority MeasurementAuthority
	FinalUsagePresent    bool
	Stage                string
	BackendAttempted     bool
	OutputCommitted      bool
	ClientCanceled       bool
	// Facts/Exposure carry quantity evidence for dual-plane rules.
	Exposure economics.ExposureBasis
	Facts    []metering.Fact
	// BoundVersion pins settlement to the admission-time policy snapshot (11.4).
	BoundVersion economics.PolicySnapshotRef
	// BoundRatingVersion pins rating identity when known at settlement (14.3).
	BoundRatingVersion economics.RatingSnapshotRef
}

// SettleResult reports the settlement outcome for surfaced attempts.
type SettleResult struct {
	Applied          bool
	ReservationID    string
	ReleasedDelta    Amount
	OverageDelta     Amount
	AdjustmentDelta  Amount
	Mutations        []SettlementMutation
	PolicyRecords    []policydecision.Record
	AccountingEvents []controlplane.Event
	PolicyRecord     policydecision.Record
	AccountingEvent  controlplane.Event
}

// SettlementMutation contains the per-rule result of one atomic settlement
// set. The first mutation is mirrored into the legacy aggregate fields on
// SettleResult for callers that only enforce one rule.
type SettlementMutation struct {
	RuleID          string
	ReservationID   string
	ReleasedDelta   Amount
	OverageDelta    Amount
	AdjustmentDelta Amount
}

// ReleaseKind classifies the release path for swallowed or losing attempts.
type ReleaseKind string

const (
	ReleaseKindSwallowed        ReleaseKind = "swallowed"
	ReleaseKindLosing           ReleaseKind = "losing"
	ReleaseKindAdmissionFailure ReleaseKind = "admission_failure"
)

// SettlementSequence returns the stable replay sequence for one lifecycle
// stage. Authoritative final reconciliation is deliberately distinct from an
// estimated final settlement even though both use SettlementKindFinal.
func SettlementSequence(kind SettlementKind, authority AuthorityLevel) int {
	switch kind {
	case SettlementKindPartial:
		return 1
	case SettlementKindCancellation:
		return 2
	case SettlementKindUnavailable:
		return 3
	case SettlementKindFinal:
		if authority == AuthorityLevelAuthoritative {
			return 5
		}
		return 4
	case SettlementKindSwallowed:
		return 6
	case SettlementKindLosing:
		return 7
	default:
		return 1
	}
}

// ReleaseInput carries the safe request data required to release a reservation
// without attributing surfaced usage to the released attempt.
type ReleaseInput struct {
	Reservations   []ReleaseDescriptor
	Correlation    controlplane.Correlation
	Scope          scope.PrincipalScopeView
	ReservationKey ReservationKey
	ReservationID  string
	RuleID         string
	Kind           ReleaseKind
	// Sequence distinguishes release stages that share one reservation identity.
	Sequence         int
	Amount           Amount
	Authority        AuthorityLevel
	Stage            string
	BackendAttempted bool
	OutputCommitted  bool
}

// ReleaseResult reports the release outcome for swallowed or losing attempts.
type ReleaseResult struct {
	Applied          bool
	ReservationID    string
	ReleasedDelta    Amount
	Mutations        []ReleaseMutation
	PolicyRecords    []policydecision.Record
	AccountingEvents []controlplane.Event
	PolicyRecord     policydecision.Record
	AccountingEvent  controlplane.Event
}

// ReleaseMutation contains the per-rule result of one atomic release set.
type ReleaseMutation struct {
	RuleID        string
	ReservationID string
	ReleasedDelta Amount
}

// ApplyUsageCommand applies final quantity usage to matched advisory or
// otherwise unreserved accounting windows without creating a reservation.
type ApplyUsageCommand struct {
	Correlation          controlplane.Correlation
	Scope                scope.PrincipalScopeView
	Dimensions           Dimensions
	RuleIDs              []string
	Usage                PreflightUsage
	RequestCount         Amount
	Authority            AuthorityLevel
	MeasurementAuthority MeasurementAuthority
	Kind                 SettlementKind
	UsagePresent         bool
	At                   time.Time
	SourceKey            string
}

// ApplyUsageResult reports which advisory windows were updated. Applied is true
// when at least one matched rule's window consumed usage. RuleIDs lists the
// rules that were actually updated (rules with no matching live window are
// skipped).
type ApplyUsageResult struct {
	Applied bool
	RuleIDs []string
}
