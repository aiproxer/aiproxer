package controlplane

import "time"

// AuthDetail is the typed detail block for CategoryAuth events (requirement
// 1.1). Outcome and ReasonCode are safe, non-secret classifications; raw
// challenge material and credentials are never carried here.
type AuthDetail struct {
	Outcome    string `json:"outcome"`
	ReasonCode string `json:"reason_code,omitempty"`
	Frontend   string `json:"frontend,omitempty"`
	AuthMethod string `json:"auth_method,omitempty"`
	IsNew      bool   `json:"is_new,omitempty"`
}

// SessionDetail is the typed detail block for CategorySession events
// (requirement 1.2). ClientSessionRef is an opaque, non-secret client
// correlation id, not a resume proof or token.
type SessionDetail struct {
	SessionID        string        `json:"session_id,omitempty"`
	ClientSessionRef string        `json:"client_session_ref,omitempty"`
	ALegID           string        `json:"a_leg_id,omitempty"`
	Action           SessionAction `json:"action"`
	Certainty        string        `json:"certainty,omitempty"`
}

// SessionAction classifies the lifecycle action recorded for a session event.
type SessionAction string

const (
	SessionActionCreated SessionAction = "created"
	SessionActionResumed SessionAction = "resumed"
	SessionActionUpdated SessionAction = "updated"
	SessionActionDenied  SessionAction = "denied"
)

// IsKnown reports whether a is one of the documented session actions.
func (a SessionAction) IsKnown() bool {
	switch a {
	case SessionActionCreated, SessionActionResumed, SessionActionUpdated, SessionActionDenied:
		return true
	}
	return false
}

// AttemptDetail is the typed detail block for CategoryAttempt events
// (requirement 1.3, 3.2, 3.3). Surfaced distinguishes client-visible attempts
// from swallowed, cancelled, or losing attempts so query consumers do not
// read a replacement as a transparent continuation of surfaced output.
type AttemptDetail struct {
	ALegID       string          `json:"a_leg_id,omitempty"`
	BLegID       string          `json:"b_leg_id,omitempty"`
	AttemptSeq   int             `json:"attempt_seq,omitempty"`
	BackendID    string          `json:"backend_id,omitempty"`
	Model        string          `json:"model,omitempty"`
	RouteOutcome string          `json:"route_outcome,omitempty"`
	Surfaced     AttemptSurfaced `json:"surfaced"`
	Outcome      AttemptOutcome  `json:"outcome"`
	ErrorClass   string          `json:"error_class,omitempty"`
	StartedAt    time.Time       `json:"started_at,omitzero"`
	FinishedAt   time.Time       `json:"finished_at,omitzero"`
}

// AttemptSurfaced classifies whether a backend attempt produced client-visible
// output (requirement 3.2, 3.3).
type AttemptSurfaced string

const (
	AttemptSurfacedUnknown   AttemptSurfaced = "unknown"
	AttemptSurfacedSurfaced  AttemptSurfaced = "surfaced"
	AttemptSurfacedSwallowed AttemptSurfaced = "swallowed"
)

// IsKnown reports whether s is one of the documented surfaced states.
func (s AttemptSurfaced) IsKnown() bool {
	switch s {
	case AttemptSurfacedUnknown, AttemptSurfacedSurfaced, AttemptSurfacedSwallowed:
		return true
	}
	return false
}

// AttemptOutcome classifies the terminal outcome of a backend attempt
// (requirement 3.2).
type AttemptOutcome string

const (
	AttemptOutcomeUnknown   AttemptOutcome = "unknown"
	AttemptOutcomeSucceeded AttemptOutcome = "succeeded"
	AttemptOutcomeFailed    AttemptOutcome = "failed"
	AttemptOutcomeCancelled AttemptOutcome = "cancelled"
	AttemptOutcomeLostRace  AttemptOutcome = "lost_race"
)

// IsKnown reports whether o is one of the documented attempt outcomes.
func (o AttemptOutcome) IsKnown() bool {
	switch o {
	case AttemptOutcomeUnknown, AttemptOutcomeSucceeded, AttemptOutcomeFailed, AttemptOutcomeCancelled, AttemptOutcomeLostRace:
		return true
	}
	return false
}

// UsageDetail is the typed detail block for CategoryUsage events (requirement
// 1.4, 9.2). Raw usage JSON from upstream sources is never surfaced here;
// only typed safe token/cost/accounting fields and explicit availability
// state are carried.
type UsageDetail struct {
	// Legacy compatibility projections (requirement 14.2, 17.4). Plane and
	// Availability remain for existing consumers; dual-plane fields below are
	// authoritative when present.
	Plane        UsagePlane        `json:"plane"`
	Availability UsageAvailability `json:"availability"`
	// Dual-plane identity (requirements 1.6, 14.1). Perspective, provenance,
	// and boundary are independent dimensions.
	Perspective         UsagePerspective    `json:"perspective,omitempty"`
	Boundary            UsageBoundary       `json:"boundary,omitempty"`
	LifecycleScope      UsageLifecycleScope `json:"lifecycle_scope,omitempty"`
	Provenance          UsageProvenance     `json:"provenance,omitempty"`
	FactKind            UsageFactKind       `json:"fact_kind,omitempty"`
	Surfaced            UsageSurfaced       `json:"surfaced,omitempty"`
	PolicyVersion       VersionRef          `json:"policy_version,omitzero"`
	RatingVersion       VersionRef          `json:"rating_version,omitzero"`
	InputTokens         int                 `json:"input_tokens,omitempty"`
	OutputTokens        int                 `json:"output_tokens,omitempty"`
	CacheReadTokens     int                 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens    int                 `json:"cache_write_tokens,omitempty"`
	ReasoningTokens     int                 `json:"reasoning_tokens,omitempty"`
	TotalTokens         int                 `json:"total_tokens,omitempty"`
	TokenPresence       UsageTokenPresence  `json:"token_presence,omitzero"`
	CostNanoUnits       int64               `json:"cost_nano_units,omitempty"`
	CostPresent         bool                `json:"cost_present"`
	Currency            string              `json:"currency,omitempty"`
	AccountingAuthority string              `json:"accounting_authority,omitempty"`
	CostSource          string              `json:"cost_source,omitempty"`
}

// UsageTokenPresence records which token counters were explicitly present on
// projected usage evidence (requirement 2.9). False means omitted, not zero.
type UsageTokenPresence struct {
	InputTokens      bool `json:"input_tokens"`
	OutputTokens     bool `json:"output_tokens"`
	CacheReadTokens  bool `json:"cache_read_tokens"`
	CacheWriteTokens bool `json:"cache_write_tokens"`
	ReasoningTokens  bool `json:"reasoning_tokens"`
	TotalTokens      bool `json:"total_tokens"`
}

// Any reports whether at least one token counter was explicitly present.
func (p UsageTokenPresence) Any() bool {
	return p.InputTokens || p.OutputTokens || p.CacheReadTokens ||
		p.CacheWriteTokens || p.ReasoningTokens || p.TotalTokens
}

// UsagePlane identifies whether usage evidence is observed from the provider
// stream or authoritative from the accounting ledger (requirement 9.2).
type UsagePlane string

const (
	UsagePlaneObserved   UsagePlane = "observed"
	UsagePlaneAccounting UsagePlane = "accounting"
)

// IsKnown reports whether p is one of the documented usage planes.
func (p UsagePlane) IsKnown() bool {
	switch p {
	case UsagePlaneObserved, UsagePlaneAccounting:
		return true
	}
	return false
}

// UsageAvailability distinguishes observed, accounting-authoritative,
// unavailable, and failed-accounting usage evidence (requirement 9.2).
type UsageAvailability string

const (
	UsageAvailabilityObserved       UsageAvailability = "observed"
	UsageAvailabilityAccountingAuth UsageAvailability = "accounting_authoritative"
	UsageAvailabilityUnavailable    UsageAvailability = "unavailable"
	UsageAvailabilityFailed         UsageAvailability = "failed_accounting"
)

// IsKnown reports whether a is one of the documented usage availability values.
func (a UsageAvailability) IsKnown() bool {
	switch a {
	case UsageAvailabilityObserved, UsageAvailabilityAccountingAuth, UsageAvailabilityUnavailable, UsageAvailabilityFailed:
		return true
	}
	return false
}

// PolicyDetail is the typed detail block for CategoryPolicy events (requirement
// 1.5, 9.3). It records safe decision stage, outcome, effect, and reason code
// without changing the original decision outcome and without exposing
// privileged content by default.
type PolicyDetail struct {
	Stage      string `json:"stage"`
	Outcome    string `json:"outcome"`
	Effect     string `json:"effect,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
}

// AuditDetail is the typed detail block for CategoryAudit events (requirement
// 1.6, 9.3). Action and Result are safe summaries; privileged audit evidence
// is marked via the Event RedactionState and Visibility, not raw fields here.
type AuditDetail struct {
	Action     string `json:"action"`
	Result     string `json:"result,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
}

// LifecycleDetail is the typed detail block for CategoryLifecycle events that
// do not fit one of the domain-specific categories (for example operator-visible
// shutdown or degraded-state transitions).
type LifecycleDetail struct {
	Stage  string `json:"stage"`
	Action string `json:"action,omitempty"`
}
