package controlplane

import (
	"time"
)

// ConcurrencyAuthorityState reports operator-visible lease-authority readiness.
type ConcurrencyAuthorityState string

const (
	ConcurrencyAuthorityDisabled    ConcurrencyAuthorityState = "disabled"
	ConcurrencyAuthorityReady       ConcurrencyAuthorityState = "ready"
	ConcurrencyAuthorityDegraded    ConcurrencyAuthorityState = "degraded"
	ConcurrencyAuthorityUnavailable ConcurrencyAuthorityState = "unavailable"
)

// IsKnown reports whether s is a documented concurrency authority state.
func (s ConcurrencyAuthorityState) IsKnown() bool {
	switch s {
	case ConcurrencyAuthorityDisabled, ConcurrencyAuthorityReady, ConcurrencyAuthorityDegraded, ConcurrencyAuthorityUnavailable:
		return true
	default:
		return false
	}
}

// ConcurrencyLeaseState is the queryable occupancy state for one lease row.
type ConcurrencyLeaseState string

const (
	ConcurrencyLeaseActive   ConcurrencyLeaseState = "active"
	ConcurrencyLeaseExpiring ConcurrencyLeaseState = "expiring"
	ConcurrencyLeaseExpired  ConcurrencyLeaseState = "expired"
	ConcurrencyLeaseReleased ConcurrencyLeaseState = "released"
)

// IsKnown reports whether s is a documented concurrency lease state.
func (s ConcurrencyLeaseState) IsKnown() bool {
	switch s {
	case ConcurrencyLeaseActive, ConcurrencyLeaseExpiring, ConcurrencyLeaseExpired, ConcurrencyLeaseReleased:
		return true
	default:
		return false
	}
}

// LeaseSetOccupancyCounts is a bounded projection of lease-set states (Phase 6).
type LeaseSetOccupancyCounts struct {
	Active         int `json:"active"`
	Uncertain      int `json:"uncertain"`
	Expiring       int `json:"expiring"`
	Released       int `json:"released"`
	Failed         int `json:"failed"`
	PendingRelease int `json:"pending_release,omitempty"`
}

// ConcurrencyAuthorityStatus is the safe readiness snapshot for lease authority.
type ConcurrencyAuthorityStatus struct {
	State          ConcurrencyAuthorityState `json:"state"`
	Reason         string                    `json:"reason,omitempty"`
	EvidenceState  EvidenceState             `json:"evidence_state,omitempty"`
	RedactionState RedactionState            `json:"redaction_state,omitempty"`
	LastUpdatedAt  time.Time                 `json:"last_updated_at,omitzero"`
	// LeaseSets exposes bounded active/uncertain/expiring/released/failed counts.
	LeaseSets LeaseSetOccupancyCounts `json:"lease_sets,omitzero"`
}

// ConcurrencyLeaseQuery requests a bounded page of lease rows.
// Unsupported filters must fail closed with an unsupported/too-broad outcome.
type ConcurrencyLeaseQuery struct {
	RequestID string                `json:"request_id,omitempty"`
	LeaseID   string                `json:"lease_id,omitempty"`
	RuleID    string                `json:"rule_id,omitempty"`
	State     ConcurrencyLeaseState `json:"state,omitempty"`
	Limit     int                   `json:"limit,omitempty"`
	Cursor    Cursor                `json:"cursor,omitzero"`
}

// ConcurrencyLeaseRow is one safe lease correlation row (no other principals).
type ConcurrencyLeaseRow struct {
	LeaseID        string                `json:"lease_id"`
	RequestID      string                `json:"request_id,omitempty"`
	RuleID         string                `json:"rule_id,omitempty"`
	RuleVersion    string                `json:"rule_version,omitempty"`
	DimensionKey   string                `json:"dimension_key,omitempty"` // safe scope correlation
	State          ConcurrencyLeaseState `json:"state"`
	Generation     int64                 `json:"generation,omitempty"`
	ExpiresAt      time.Time             `json:"expires_at,omitzero"`
	ReleasedAt     time.Time             `json:"released_at,omitzero"`
	RemainingSlots int                   `json:"remaining_slots,omitempty"`
	EvidenceState  EvidenceState         `json:"evidence_state,omitempty"`
	RedactionState RedactionState        `json:"redaction_state,omitempty"`
}

// ConcurrencyCapacityRow reports remaining slots for one rule (requirement 10.12).
type ConcurrencyCapacityRow struct {
	RuleID         string `json:"rule_id"`
	RuleVersion    string `json:"rule_version,omitempty"`
	DimensionKey   string `json:"dimension_key,omitempty"`
	Limit          int    `json:"limit"`
	Active         int    `json:"active"`
	Expiring       int    `json:"expiring,omitempty"`
	RemainingSlots int    `json:"remaining_slots"`
}
