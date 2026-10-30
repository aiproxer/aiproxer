package controlplane

import "time"

// VersionRef is an immutable snapshot identity for policy or rating versions
// carried on control-plane evidence (requirements 1.6, 14.3, 17.4). It mirrors
// economics.VersionRef without importing economics so the SDK import surface
// stays minimal.
type VersionRef struct {
	ID          string    `json:"id,omitempty"`
	Version     string    `json:"version,omitempty"`
	EffectiveAt time.Time `json:"effective_at,omitzero"`
	FetchedAt   time.Time `json:"fetched_at,omitzero"`
}

// UsagePerspective identifies whose economics usage evidence represents.
type UsagePerspective string

const (
	UsagePerspectiveCustomer UsagePerspective = "customer"
	UsagePerspectiveOperator UsagePerspective = "operator"
	UsagePerspectiveNone     UsagePerspective = "none"
)

// UsageBoundary is a legal metering checkpoint on the proxy data path.
type UsageBoundary string

const (
	UsageBoundaryFrontendIngress UsageBoundary = "frontend_ingress"
	UsageBoundaryBackendIngress  UsageBoundary = "backend_ingress"
	UsageBoundaryBackendEgress   UsageBoundary = "backend_egress"
	UsageBoundaryFrontendEgress  UsageBoundary = "frontend_egress"
)

// UsageLifecycleScope identifies the lifecycle object usage evidence applies to.
type UsageLifecycleScope string

const (
	UsageLifecycleLogicalRequest   UsageLifecycleScope = "logical_request"
	UsageLifecycleBackendAttempt   UsageLifecycleScope = "backend_attempt"
	UsageLifecycleAuxiliaryRequest UsageLifecycleScope = "auxiliary_request"
)

// UsageProvenance is evidence provenance for usage facts, independent of
// economic perspective and metering boundary (requirement 14.1).
type UsageProvenance string

const (
	UsageProvenanceAuthoritative UsageProvenance = "authoritative"
	UsageProvenanceDelegated     UsageProvenance = "delegated"
	UsageProvenanceEstimated     UsageProvenance = "estimated"
	UsageProvenanceAdvisory      UsageProvenance = "advisory"
	UsageProvenanceUnavailable   UsageProvenance = "unavailable"
)

// UsageFactKind classifies how a usage fact participates in aggregation.
type UsageFactKind string

const (
	UsageFactKindDelta                    UsageFactKind = "delta"
	UsageFactKindCumulative               UsageFactKind = "cumulative"
	UsageFactKindCorrection               UsageFactKind = "correction"
	UsageFactKindAuthoritativeReplacement UsageFactKind = "authoritative_replacement"
	UsageFactKindReservationEstimate      UsageFactKind = "reservation_estimate"
	UsageFactKindUnavailable              UsageFactKind = "unavailable"
)

// UsageSurfaced records whether attempt output reached the client.
type UsageSurfaced string

const (
	UsageSurfacedYes     UsageSurfaced = "yes"
	UsageSurfacedNo      UsageSurfaced = "no"
	UsageSurfacedUnknown UsageSurfaced = "unknown"
)

// AuthorityHandleType classifies the authority handle referenced by evidence.
type AuthorityHandleType string

const (
	AuthorityHandleReservation AuthorityHandleType = "reservation"
	AuthorityHandleLease       AuthorityHandleType = "lease"
)
