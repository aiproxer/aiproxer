package metering

import "fmt"

// EconomicPerspective identifies whose economics a fact or authority decision
// represents. Customer and operator perspectives are independent even when
// values happen to match (requirements 1.1, 1.4).
type EconomicPerspective string

const (
	PerspectiveCustomer EconomicPerspective = "customer"
	PerspectiveOperator EconomicPerspective = "operator"
	PerspectiveNone     EconomicPerspective = "none" // pure technical metrics
)

// IsKnown reports whether p is a documented economic perspective.
func (p EconomicPerspective) IsKnown() bool {
	switch p {
	case PerspectiveCustomer, PerspectiveOperator, PerspectiveNone:
		return true
	}
	return false
}

// Validate returns an error when p is not a known perspective.
func (p EconomicPerspective) Validate() error {
	if !p.IsKnown() {
		return fmt.Errorf("metering: unknown economic perspective %q", p)
	}
	return nil
}

// Boundary is a legal metering measurement boundary on the proxy data path.
// Derived bases such as "derived:<id>" are rule/exposure concerns and are not
// known Boundary values here.
type Boundary string

const (
	BoundaryFrontendIngress Boundary = "frontend_ingress"
	BoundaryBackendIngress  Boundary = "backend_ingress"
	BoundaryBackendEgress   Boundary = "backend_egress"
	BoundaryFrontendEgress  Boundary = "frontend_egress"
)

// IsKnown reports whether b is a documented metering boundary.
func (b Boundary) IsKnown() bool {
	switch b {
	case BoundaryFrontendIngress, BoundaryBackendIngress, BoundaryBackendEgress, BoundaryFrontendEgress:
		return true
	}
	return false
}

// Validate returns an error when b is not a known boundary.
func (b Boundary) Validate() error {
	if !b.IsKnown() {
		return fmt.Errorf("metering: unknown boundary %q", b)
	}
	return nil
}

// LifecycleScope identifies the lifecycle object a fact or rule applies to
// (requirement 1.2).
type LifecycleScope string

const (
	LifecycleLogicalRequest   LifecycleScope = "logical_request"
	LifecycleBackendAttempt   LifecycleScope = "backend_attempt"
	LifecycleAuxiliaryRequest LifecycleScope = "auxiliary_request"
)

// IsKnown reports whether s is a documented lifecycle scope.
func (s LifecycleScope) IsKnown() bool {
	switch s {
	case LifecycleLogicalRequest, LifecycleBackendAttempt, LifecycleAuxiliaryRequest:
		return true
	}
	return false
}

// Validate returns an error when s is not a known lifecycle scope.
func (s LifecycleScope) Validate() error {
	if !s.IsKnown() {
		return fmt.Errorf("metering: unknown lifecycle scope %q", s)
	}
	return nil
}

// FactKind classifies how a fact participates in aggregation (requirement 3.2).
type FactKind string

const (
	FactKindDelta                    FactKind = "delta"
	FactKindCumulative               FactKind = "cumulative"
	FactKindCorrection               FactKind = "correction"
	FactKindAuthoritativeReplacement FactKind = "authoritative_replacement"
	FactKindReservationEstimate      FactKind = "reservation_estimate"
	FactKindUnavailable              FactKind = "unavailable"
)

// IsKnown reports whether k is a documented fact kind.
func (k FactKind) IsKnown() bool {
	switch k {
	case FactKindDelta, FactKindCumulative, FactKindCorrection,
		FactKindAuthoritativeReplacement, FactKindReservationEstimate, FactKindUnavailable:
		return true
	}
	return false
}

// Validate returns an error when k is not a known fact kind.
func (k FactKind) Validate() error {
	if !k.IsKnown() {
		return fmt.Errorf("metering: unknown fact kind %q", k)
	}
	return nil
}

// RequiresSupersedes reports whether k must identify superseded fact identities.
func (k FactKind) RequiresSupersedes() bool {
	return k == FactKindCorrection || k == FactKindAuthoritativeReplacement
}

// Presence distinguishes authoritative zero/non-zero from absence/unknown.
type Presence string

const (
	PresencePresent Presence = "present"
	PresenceAbsent  Presence = "absent"
	PresenceUnknown Presence = "unknown"
)

// IsKnown reports whether p is a documented presence value.
func (p Presence) IsKnown() bool {
	switch p {
	case PresencePresent, PresenceAbsent, PresenceUnknown:
		return true
	}
	return false
}

// Validate returns an error when p is not a known presence value.
func (p Presence) Validate() error {
	if !p.IsKnown() {
		return fmt.Errorf("metering: unknown presence %q", p)
	}
	return nil
}

// Source identifies how a fact or quantity was obtained.
type Source string

const (
	SourceObserved         Source = "observed"
	SourceDerived          Source = "derived"
	SourceProviderReported Source = "provider_reported"
	SourceConfigured       Source = "configured"
	SourceEstimated        Source = "estimated"
)

// IsKnown reports whether s is a documented source.
func (s Source) IsKnown() bool {
	switch s {
	case SourceObserved, SourceDerived, SourceProviderReported, SourceConfigured, SourceEstimated:
		return true
	}
	return false
}

// Validate returns an error when s is not a known source.
func (s Source) Validate() error {
	if !s.IsKnown() {
		return fmt.Errorf("metering: unknown source %q", s)
	}
	return nil
}

// Authority is evidence provenance for a fact (design: authoritative,
// delegated, estimated, advisory, unavailable). Independent of EconomicPerspective.
type Authority string

const (
	AuthorityAuthoritative Authority = "authoritative"
	AuthorityDelegated     Authority = "delegated"
	AuthorityEstimated     Authority = "estimated"
	AuthorityAdvisory      Authority = "advisory"
	AuthorityUnavailable   Authority = "unavailable"
)

// IsKnown reports whether a is a documented authority/provenance class.
func (a Authority) IsKnown() bool {
	switch a {
	case AuthorityAuthoritative, AuthorityDelegated, AuthorityEstimated,
		AuthorityAdvisory, AuthorityUnavailable:
		return true
	}
	return false
}

// Validate returns an error when a is not a known authority class.
func (a Authority) Validate() error {
	if !a.IsKnown() {
		return fmt.Errorf("metering: unknown authority %q", a)
	}
	return nil
}

// SurfacedState records whether attempt output reached the client.
type SurfacedState string

const (
	SurfacedYes     SurfacedState = "yes"
	SurfacedNo      SurfacedState = "no"
	SurfacedUnknown SurfacedState = "unknown"
)

// IsKnown reports whether s is a documented surfaced state.
func (s SurfacedState) IsKnown() bool {
	switch s {
	case SurfacedYes, SurfacedNo, SurfacedUnknown:
		return true
	}
	return false
}

// Validate returns an error when s is not a known surfaced state.
func (s SurfacedState) Validate() error {
	if !s.IsKnown() {
		return fmt.Errorf("metering: unknown surfaced state %q", s)
	}
	return nil
}

// AttemptOutcome classifies the terminal role of a backend attempt.
type AttemptOutcome string

const (
	AttemptOutcomeWinner   AttemptOutcome = "winner"
	AttemptOutcomeLoser    AttemptOutcome = "loser"
	AttemptOutcomeCanceled AttemptOutcome = "canceled"
	AttemptOutcomeFailed   AttemptOutcome = "failed"
	AttemptOutcomeUnknown  AttemptOutcome = "unknown"
)

// IsKnown reports whether o is a documented attempt outcome.
func (o AttemptOutcome) IsKnown() bool {
	switch o {
	case AttemptOutcomeWinner, AttemptOutcomeLoser, AttemptOutcomeCanceled,
		AttemptOutcomeFailed, AttemptOutcomeUnknown:
		return true
	}
	return false
}

// Validate returns an error when o is not a known attempt outcome.
func (o AttemptOutcome) Validate() error {
	if !o.IsKnown() {
		return fmt.Errorf("metering: unknown attempt outcome %q", o)
	}
	return nil
}

// Correlation carries safe lifecycle identifiers without raw payloads
// (requirements 2.6, 2.7, 13.2).
type Correlation struct {
	RequestID string `json:"request_id,omitempty"`
	ALegID    string `json:"a_leg_id,omitempty"`
	BLegID    string `json:"b_leg_id,omitempty"`
	AttemptID string `json:"attempt_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// VersionRef is an immutable identity for a bound policy, pricebook, or rule
// snapshot carried on metering facts. Richer snapshot envelopes live in
// pkg/aipsdk/economics.
type VersionRef struct {
	ID          string `json:"id,omitempty"`
	Version     string `json:"version,omitempty"`
	EffectiveAt int64  `json:"effective_at_unix_ms,omitempty"` // unix ms; 0 = unset
	FetchedAt   int64  `json:"fetched_at_unix_ms,omitempty"`   // unix ms; 0 = unset
}
