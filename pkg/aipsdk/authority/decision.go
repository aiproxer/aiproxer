package authority

import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// DecisionKind is the outcome of an admit call.
type DecisionKind string

const (
	DecisionAllow    DecisionKind = "allow"
	DecisionDeny     DecisionKind = "deny"
	DecisionAdvisory DecisionKind = "advisory"
)

// IsKnown reports whether k is a documented decision kind.
func (k DecisionKind) IsKnown() bool {
	switch k {
	case DecisionAllow, DecisionDeny, DecisionAdvisory:
		return true
	}
	return false
}

// ReservationKind classifies a held reservation handle.
type ReservationKind string

const (
	ReservationQuota  ReservationKind = "quota"
	ReservationBudget ReservationKind = "budget"
	ReservationSpend  ReservationKind = "spend"
	ReservationCredit ReservationKind = "credit"
	ReservationOther  ReservationKind = "other"
)

// IsKnown reports whether k is a documented reservation kind.
func (k ReservationKind) IsKnown() bool {
	switch k {
	case ReservationQuota, ReservationBudget, ReservationSpend, ReservationCredit, ReservationOther:
		return true
	}
	return false
}

// Reservation is a successful admit hold to settle or compensate later. Handle
// remains opaque; Quantity or Money carries safe provider-neutral reserved
// exposure when later settlement needs the admission-time amount.
type Reservation struct {
	Handle   string             `json:"handle"`
	Kind     ReservationKind    `json:"kind,omitempty"`
	RuleID   string             `json:"rule_id,omitempty"`
	Quantity *metering.Quantity `json:"quantity,omitempty"`
	Money    *economics.Money   `json:"money,omitempty"`
}

// ClampKind classifies an enforcement clamp applied at admit.
type ClampKind string

const (
	ClampMaxOutputTokens ClampKind = "max_output_tokens"
	ClampMaxSpend        ClampKind = "max_spend"
	ClampOther           ClampKind = "other"
)

// IsKnown reports whether k is a documented clamp kind.
func (k ClampKind) IsKnown() bool {
	switch k {
	case ClampMaxOutputTokens, ClampMaxSpend, ClampOther:
		return true
	}
	return false
}

// Clamp is a non-widening constraint imposed by a provider.
type Clamp struct {
	Kind   ClampKind       `json:"kind"`
	Value  int64           `json:"value,omitempty"`
	Money  economics.Money `json:"money,omitzero"`
	RuleID string          `json:"rule_id,omitempty"`
}

// SettlementKind classifies settlement completion.
type SettlementKind string

const (
	SettlementFinal       SettlementKind = "final"
	SettlementPartial     SettlementKind = "partial"
	SettlementEstimated   SettlementKind = "estimated"
	SettlementUnavailable SettlementKind = "unavailable"
)

// IsKnown reports whether k is a documented settlement kind.
func (k SettlementKind) IsKnown() bool {
	switch k {
	case SettlementFinal, SettlementPartial, SettlementEstimated, SettlementUnavailable:
		return true
	}
	return false
}

// Settlement is the result of settling a prior reservation.
type Settlement struct {
	Kind          SettlementKind                `json:"kind"`
	Handle        string                        `json:"handle,omitempty"`
	Consumed      economics.Money               `json:"consumed,omitzero"`
	Released      economics.Money               `json:"released,omitzero"`
	Evidence      SafeEvidence                  `json:"evidence,omitzero"`
	BoundVersions []economics.PolicySnapshotRef `json:"bound_versions,omitempty"`
}

// Readiness reports whether the authority plane can enforce required decisions.
type Readiness string

const (
	ReadinessReady       Readiness = "ready"
	ReadinessDegraded    Readiness = "degraded"
	ReadinessUnavailable Readiness = "unavailable"
	ReadinessDisabled    Readiness = "disabled"
)

// IsKnown reports whether r is a documented readiness value.
func (r Readiness) IsKnown() bool {
	switch r {
	case ReadinessReady, ReadinessDegraded, ReadinessUnavailable, ReadinessDisabled:
		return true
	}
	return false
}

// Decision is the admit result from a RequestProvider or AttemptProvider.
type Decision struct {
	Kind               DecisionKind                  `json:"kind"`
	ProviderID         string                        `json:"provider_id,omitempty"`
	Stage              Stage                         `json:"stage,omitempty"`
	Reservations       []Reservation                 `json:"reservations,omitempty"`
	Clamps             []Clamp                       `json:"clamps,omitempty"`
	CompensationHandle string                        `json:"compensation_handle,omitempty"`
	Readiness          Readiness                     `json:"readiness,omitempty"`
	BoundVersions      []economics.PolicySnapshotRef `json:"bound_versions,omitempty"`
	RatingVersions     []economics.RatingSnapshotRef `json:"rating_versions,omitempty"`
	Exposure           economics.ExposureBasis       `json:"exposure,omitzero"`
	Evidence           SafeEvidence                  `json:"evidence,omitzero"`
}
