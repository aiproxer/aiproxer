package metering

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// QueryClass distinguishes historical metering from live authority views
// (requirement 14.5). The metering journal supports historical_metering only;
// other classes are unsupported on this querier.
type QueryClass string

const (
	QueryClassHistoricalMetering  QueryClass = "historical_metering"
	QueryClassLiveReservation     QueryClass = "live_reservation"
	QueryClassActiveLease         QueryClass = "active_lease"
	QueryClassRemainingAuthority  QueryClass = "remaining_authority"
	QueryClassFinancialProjection QueryClass = "financial_projection"
)

// IsKnown reports whether c is a documented query class.
func (c QueryClass) IsKnown() bool {
	switch c {
	case QueryClassHistoricalMetering, QueryClassLiveReservation, QueryClassActiveLease,
		QueryClassRemainingAuthority, QueryClassFinancialProjection:
		return true
	default:
		return false
	}
}

// UnsupportedFilter names a requested filter that recorded facts cannot apply.
type UnsupportedFilter struct {
	Field  string `json:"field"`
	Reason string `json:"reason,omitempty"`
}

// TimeRange bounds a query by fact recorded time. Either bound may be omitted;
// time alone is not a selective bound (requirement 14.4, 14.8).
type TimeRange struct {
	From time.Time `json:"from,omitzero"`
	To   time.Time `json:"to,omitzero"`
}

// ScopeFilters carries safe, presence-aware principal/scope filter dimensions.
type ScopeFilters struct {
	PrincipalID    scope.Value `json:"principal_id,omitzero"`
	CredentialID   scope.Value `json:"credential_id,omitzero"`
	TenantID       scope.Value `json:"tenant_id,omitzero"`
	OrganizationID scope.Value `json:"organization_id,omitzero"`
	WorkspaceID    scope.Value `json:"workspace_id,omitzero"`
	ProjectID      scope.Value `json:"project_id,omitzero"`
	DepartmentID   scope.Value `json:"department_id,omitzero"`
	CostCenterID   scope.Value `json:"cost_center_id,omitzero"`
}

// Query is a bounded filter for listing metering facts. Unsupported or
// too-broad filters return stable outcomes rather than scanning (14.4, 14.8).
type Query struct {
	Class           QueryClass          `json:"class,omitempty"`
	Scope           ScopeFilters        `json:"scope,omitzero"`
	TimeRange       TimeRange           `json:"time_range,omitzero"`
	Perspective     EconomicPerspective `json:"perspective,omitempty"`
	Boundary        Boundary            `json:"boundary,omitempty"`
	Lifecycle       LifecycleScope      `json:"lifecycle,omitempty"`
	StreamID        string              `json:"stream_id,omitempty"`
	RequestID       string              `json:"request_id,omitempty"`
	TraceID         string              `json:"trace_id,omitempty"`
	SessionID       string              `json:"session_id,omitempty"`
	ALegID          string              `json:"a_leg_id,omitempty"`
	BLegID          string              `json:"b_leg_id,omitempty"`
	AttemptID       string              `json:"attempt_id,omitempty"`
	FrontendID      string              `json:"frontend_id,omitempty"`
	BackendID       string              `json:"backend_id,omitempty"`
	Model           string              `json:"model,omitempty"`
	RouteID         string              `json:"route_id,omitempty"`
	RuleID          string              `json:"rule_id,omitempty"`
	Source          Source              `json:"source,omitempty"`
	Authority       Authority           `json:"authority,omitempty"`
	IdentityVersion int                 `json:"identity_version,omitempty"`
	Limit           int                 `json:"limit,omitempty"`
	Cursor          string              `json:"cursor,omitempty"`
}

// Page is one bounded page of facts plus continuation and unsupported-filter
// reporting.
type Page struct {
	Facts       []Fact              `json:"facts"`
	NextCursor  string              `json:"next_cursor,omitempty"`
	Unsupported []UnsupportedFilter `json:"unsupported,omitempty"`
}

// Querier lists metering facts with bounded pagination.
type Querier interface {
	List(ctx context.Context, q Query) (Page, error)
}
