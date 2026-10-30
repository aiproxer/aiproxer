package controlplane

import (
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// scopeValue is a package-local alias for scope.Value so query filter structs
// preserve presence-aware unknown-vs-known-empty semantics without repeating
// the type at every field (requirement 4.3).
type scopeValue = scope.Value

// Cursor is an opaque continuation token tied to a specific query shape and
// visibility level (requirement 2.7, 7.4). Consumers must treat the Token as
// opaque bytes; the core query service validates cursor shape before store
// access and rejects reuse across different query conditions.
type Cursor struct {
	Token string `json:"token,omitempty"`
}

// IsZero reports whether the cursor carries no continuation.
func (c Cursor) IsZero() bool { return c.Token == "" }

// UnsupportedFilter names a requested filter that recorded evidence cannot
// apply, with a safe reason (requirement 2.5, 8.6, 9.4). Stores report
// unsupported filters explicitly rather than silently widening the query.
type UnsupportedFilter struct {
	Field  string `json:"field"`
	Reason string `json:"reason,omitempty"`
}

// TimeRange bounds a query by event time. Either bound may be omitted to
// express an open range; both bounds require a selective filter for broad
// event queries against durable stores (requirement 2.6, 7.4).
type TimeRange struct {
	From time.Time `json:"from,omitzero"`
	To   time.Time `json:"to,omitzero"`
}

// ScopeFilters carries the safe, presence-aware principal/scope filter
// dimensions (requirement 2.5, 4.2, 4.3). Unknown values match records whose
// dimension is unknown; known-empty values match records whose dimension is
// known and intentionally empty.
type ScopeFilters struct {
	PrincipalID    scopeValue `json:"principal_id,omitzero"`
	CredentialID   scopeValue `json:"credential_id,omitzero"`
	TenantID       scopeValue `json:"tenant_id,omitzero"`
	OrganizationID scopeValue `json:"organization_id,omitzero"`
	WorkspaceID    scopeValue `json:"workspace_id,omitzero"`
	ProjectID      scopeValue `json:"project_id,omitzero"`
	DepartmentID   scopeValue `json:"department_id,omitzero"`
	CostCenterID   scopeValue `json:"cost_center_id,omitzero"`
}

// CommonFilters are the correlation and scope filters shared across query
// shapes (requirement 2.5, 9.1).
type CommonFilters struct {
	Scope      ScopeFilters `json:"scope,omitzero"`
	TimeRange  TimeRange    `json:"time_range,omitzero"`
	BackendID  string       `json:"backend_id,omitempty"`
	Model      string       `json:"model,omitempty"`
	FrontendID string       `json:"frontend_id,omitempty"`
	TraceID    string       `json:"trace_id,omitempty"`
	SessionID  string       `json:"session_id,omitempty"`
	ALegID     string       `json:"a_leg_id,omitempty"`
	BLegID     string       `json:"b_leg_id,omitempty"`
	Outcome    string       `json:"outcome,omitempty"`
	ReasonCode string       `json:"reason_code,omitempty"`
}

// SessionQuery requests bounded session summaries (requirement 2.1).
type SessionQuery struct {
	Common     CommonFilters `json:"common,omitzero"`
	Limit      int           `json:"limit,omitempty"`
	Cursor     Cursor        `json:"cursor,omitzero"`
	Visibility Visibility    `json:"visibility,omitempty"`
}

// AttemptQuery requests bounded backend attempt rows (requirement 2.2).
type AttemptQuery struct {
	Common     CommonFilters `json:"common,omitzero"`
	Surfaced   string        `json:"surfaced,omitempty"`
	Limit      int           `json:"limit,omitempty"`
	Cursor     Cursor        `json:"cursor,omitzero"`
	Visibility Visibility    `json:"visibility,omitempty"`
}

// UsageQuery requests bounded usage rows (requirement 2.3, 9.2).
type UsageQuery struct {
	Common         CommonFilters       `json:"common,omitzero"`
	Plane          string              `json:"plane,omitempty"`
	Availability   string              `json:"availability,omitempty"`
	Perspective    UsagePerspective    `json:"perspective,omitempty"`
	Boundary       UsageBoundary       `json:"boundary,omitempty"`
	LifecycleScope UsageLifecycleScope `json:"lifecycle_scope,omitempty"`
	RuleID         string              `json:"rule_id,omitempty"`
	Class          QueryClass          `json:"class,omitempty"`
	Limit          int                 `json:"limit,omitempty"`
	Cursor         Cursor              `json:"cursor,omitzero"`
	Visibility     Visibility          `json:"visibility,omitempty"`
}

// UsageAggregateQuery requests bounded usage aggregates grouped by requested
// dimensions (requirement 2.3, 6.4).
type UsageAggregateQuery struct {
	Common     CommonFilters `json:"common,omitzero"`
	GroupBy    []string      `json:"group_by,omitempty"`
	Limit      int           `json:"limit,omitempty"`
	Cursor     Cursor        `json:"cursor,omitzero"`
	Visibility Visibility    `json:"visibility,omitempty"`
}

// EvidenceQuery requests bounded policy and audit evidence rows (requirement
// 2.4, 9.3).
type EvidenceQuery struct {
	Common     CommonFilters `json:"common,omitzero"`
	Effect     string        `json:"effect,omitempty"`
	Category   Category      `json:"category,omitempty"`
	Limit      int           `json:"limit,omitempty"`
	Cursor     Cursor        `json:"cursor,omitzero"`
	Visibility Visibility    `json:"visibility,omitempty"`
}

// EventQuery requests bounded raw control-plane events (requirement 9.1).
type EventQuery struct {
	Common     CommonFilters `json:"common,omitzero"`
	Category   Category      `json:"category,omitempty"`
	Limit      int           `json:"limit,omitempty"`
	Cursor     Cursor        `json:"cursor,omitzero"`
	Visibility Visibility    `json:"visibility,omitempty"`
}

// Page is a bounded result set with continuation, unsupported-filter
// reporting, and visibility metadata (requirement 2.6, 2.7, 2.8, 3.5).
type Page[T any] struct {
	Items       []T                 `json:"items"`
	Next        Cursor              `json:"next,omitzero"`
	Unsupported []UnsupportedFilter `json:"unsupported,omitempty"`
	Visibility  Visibility          `json:"visibility"`
}

// SessionSummary is one row of a SessionQuery result (requirement 2.1).
type SessionSummary struct {
	SessionID     string        `json:"session_id"`
	LastActivity  time.Time     `json:"last_activity"`
	Scope         ScopeSnapshot `json:"scope"`
	UsageTotals   *UsageTotals  `json:"usage_totals,omitempty"`
	AttemptCount  int           `json:"attempt_count,omitempty"`
	EvidenceState EvidenceState `json:"evidence_state"`
}

// UsageTotals carries safe aggregate usage totals for a session summary
// (requirement 2.1). Aggregate values are distinct from detailed usage rows
// (requirement 6.4).
type UsageTotals struct {
	InputTokens   int   `json:"input_tokens,omitempty"`
	OutputTokens  int   `json:"output_tokens,omitempty"`
	TotalTokens   int   `json:"total_tokens,omitempty"`
	CostNanoUnits int64 `json:"cost_nano_units,omitempty"`
}

// AttemptRow is one row of an AttemptQuery result (requirement 2.2, 3.2).
type AttemptRow struct {
	Correlation   Correlation     `json:"correlation"`
	BackendID     string          `json:"backend_id,omitempty"`
	Model         string          `json:"model,omitempty"`
	RouteOutcome  string          `json:"route_outcome,omitempty"`
	Surfaced      AttemptSurfaced `json:"surfaced"`
	Outcome       AttemptOutcome  `json:"outcome"`
	ErrorClass    string          `json:"error_class,omitempty"`
	StartedAt     time.Time       `json:"started_at,omitzero"`
	FinishedAt    time.Time       `json:"finished_at,omitzero"`
	EvidenceState EvidenceState   `json:"evidence_state"`
}

// UsageRow is one row of a UsageQuery result (requirement 2.3, 9.2).
type UsageRow struct {
	Correlation      Correlation         `json:"correlation"`
	Plane            UsagePlane          `json:"plane"`
	Availability     UsageAvailability   `json:"availability"`
	Perspective      UsagePerspective    `json:"perspective,omitempty"`
	Boundary         UsageBoundary       `json:"boundary,omitempty"`
	LifecycleScope   UsageLifecycleScope `json:"lifecycle_scope,omitempty"`
	Provenance       UsageProvenance     `json:"provenance,omitempty"`
	FactKind         UsageFactKind       `json:"fact_kind,omitempty"`
	Surfaced         UsageSurfaced       `json:"surfaced,omitempty"`
	PolicyVersion    VersionRef          `json:"policy_version,omitzero"`
	RatingVersion    VersionRef          `json:"rating_version,omitzero"`
	InputTokens      int                 `json:"input_tokens,omitempty"`
	OutputTokens     int                 `json:"output_tokens,omitempty"`
	CacheReadTokens  int                 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int                 `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int                 `json:"reasoning_tokens,omitempty"`
	TotalTokens      int                 `json:"total_tokens,omitempty"`
	TokenPresence    UsageTokenPresence  `json:"token_presence,omitzero"`
	CostNanoUnits    int64               `json:"cost_nano_units,omitempty"`
	CostPresent      bool                `json:"cost_present"`
	Currency         string              `json:"currency,omitempty"`
	EvidenceState    EvidenceState       `json:"evidence_state"`
	RedactionState   RedactionState      `json:"redaction_state"`
}

// UsageAggregate is one row of a UsageAggregateQuery result (requirement 2.3,
// 6.4). Aggregate rows are logically distinct from detailed usage rows.
type UsageAggregate struct {
	Scope          ScopeSnapshot       `json:"scope,omitzero"`
	BackendID      string              `json:"backend_id,omitempty"`
	Model          string              `json:"model,omitempty"`
	Plane          UsagePlane          `json:"plane"`
	Perspective    UsagePerspective    `json:"perspective,omitempty"`
	Boundary       UsageBoundary       `json:"boundary,omitempty"`
	LifecycleScope UsageLifecycleScope `json:"lifecycle_scope,omitempty"`
	InputTokens    int64               `json:"input_tokens,omitempty"`
	OutputTokens   int64               `json:"output_tokens,omitempty"`
	TotalTokens    int64               `json:"total_tokens,omitempty"`
	CostNanoUnits  int64               `json:"cost_nano_units,omitempty"`
	WindowStart    time.Time           `json:"window_start,omitzero"`
	WindowEnd      time.Time           `json:"window_end,omitzero"`
	EvidenceState  EvidenceState       `json:"evidence_state"`
}

// PolicyAuditRow is one row of an EvidenceQuery result for policy or audit
// evidence (requirement 2.4, 9.3).
type PolicyAuditRow struct {
	Correlation    Correlation    `json:"correlation"`
	Category       Category       `json:"category"`
	Stage          string         `json:"stage,omitempty"`
	Outcome        string         `json:"outcome,omitempty"`
	Effect         string         `json:"effect,omitempty"`
	ReasonCode     string         `json:"reason_code,omitempty"`
	OccurredAt     time.Time      `json:"occurred_at,omitzero"`
	Visibility     Visibility     `json:"visibility,omitempty"`
	RedactionState RedactionState `json:"redaction_state"`
	EvidenceState  EvidenceState  `json:"evidence_state"`
}
