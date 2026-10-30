package controlplane

import "time"

// CapabilityState is the operator-visible readiness state of the control-plane
// capability (requirement 7.1, 7.5).
type CapabilityState string

const (
	CapabilityDisabled    CapabilityState = "disabled"
	CapabilityReady       CapabilityState = "ready"
	CapabilityDegraded    CapabilityState = "degraded"
	CapabilityUnavailable CapabilityState = "unavailable"
)

// IsKnown reports whether s is one of the documented capability states.
func (s CapabilityState) IsKnown() bool {
	switch s {
	case CapabilityDisabled, CapabilityReady, CapabilityDegraded, CapabilityUnavailable:
		return true
	}
	return false
}

// ReasonCode is a bounded, safe operator-visible reason for a non-ready
// capability state (requirement 7.2, 7.3). It never carries raw
// infrastructure error text, DSNs, SQL, or driver details.
type ReasonCode string

const (
	ReasonNone                ReasonCode = ""
	ReasonDisabled            ReasonCode = "disabled"
	ReasonUnsupported         ReasonCode = "unsupported"
	ReasonStoreNotReady       ReasonCode = "store_not_ready"
	ReasonBackingUnavailable  ReasonCode = "backing_unavailable"
	ReasonRecordingFailure    ReasonCode = "recording_failure"
	ReasonQueryFailure        ReasonCode = "query_failure"
	ReasonRetentionFailure    ReasonCode = "retention_failure"
	ReasonRedactionFailure    ReasonCode = "redaction_failure"
	ReasonPendingTerminalWork ReasonCode = "pending_terminal_work"
)

// IsKnown reports whether r is one of the documented non-empty reason codes.
func (r ReasonCode) IsKnown() bool {
	switch r {
	case ReasonDisabled, ReasonUnsupported, ReasonStoreNotReady, ReasonBackingUnavailable,
		ReasonRecordingFailure, ReasonQueryFailure, ReasonRetentionFailure, ReasonRedactionFailure,
		ReasonPendingTerminalWork:
		return true
	}
	return false
}

// RecordingPolicy controls how the recorder treats failures per lifecycle
// category (requirement 5.4, 5.5, 7.6).
type RecordingPolicy string

const (
	RecordingDisabled        RecordingPolicy = "disabled"
	RecordingBestEffort      RecordingPolicy = "best_effort"
	RecordingRequiredPreWork RecordingPolicy = "required_pre_work"
)

// IsKnown reports whether p is one of the documented recording policies.
func (p RecordingPolicy) IsKnown() bool {
	switch p {
	case RecordingDisabled, RecordingBestEffort, RecordingRequiredPreWork:
		return true
	}
	return false
}

// CapabilityStatus is the operator-visible status snapshot for the
// control-plane capability (requirement 7.1, 7.2, 7.3).
type CapabilityStatus struct {
	State           CapabilityState `json:"state"`
	Reason          ReasonCode      `json:"reason,omitempty"`
	LastFailureAt   time.Time       `json:"last_failure_at,omitzero"`
	RecordingPolicy RecordingPolicy `json:"recording_policy,omitempty"`
}

// ErrorCode is the stable classification carried on query/recording errors so
// future feature consumers and HTTP adapters can map failures without raw
// infrastructure details (requirement 7.4, 9.4).
type ErrorCode string

const (
	ErrCodeDisabled          ErrorCode = "disabled"
	ErrCodeUnavailable       ErrorCode = "unavailable"
	ErrCodeDegraded          ErrorCode = "degraded"
	ErrCodeInvalidQuery      ErrorCode = "invalid_query"
	ErrCodeTooBroad          ErrorCode = "too_broad"
	ErrCodeUnsupportedFilter ErrorCode = "unsupported_filter"
	ErrCodeUnsafeEvidence    ErrorCode = "unsafe_evidence"
)

// HTTP-layer error codes for the operator query adapter (not control-plane query
// classifications), kept as named constants so every response code is discoverable.
const (
	ErrCodeMethodNotAllowed        ErrorCode = "method_not_allowed"
	ErrCodeControlPlaneUnavailable ErrorCode = "control_plane_unavailable"
)

// IsKnown reports whether c is one of the documented error codes.
func (c ErrorCode) IsKnown() bool {
	switch c {
	case ErrCodeDisabled, ErrCodeUnavailable, ErrCodeDegraded, ErrCodeInvalidQuery,
		ErrCodeTooBroad, ErrCodeUnsupportedFilter, ErrCodeUnsafeEvidence:
		return true
	}
	return false
}
