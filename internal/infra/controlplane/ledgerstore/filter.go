package ledgerstore

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore/fields"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// scopeValueMatches applies one scope dimension's filter. An unknown filter
// means "no constraint on this dimension" (matches any record). A known filter
// matches records whose dimension is known with the same value, including
// known-empty (requirement 4.3: unknown vs known-empty stay distinguishable).
func scopeValueMatches(filter, record scope.Value) bool {
	if filter.IsUnknown() {
		return true
	}
	return record.IsKnown() && record.Value == filter.Value
}

func scopeFiltersMatch(filter cp.ScopeFilters, snap cp.ScopeSnapshot, unsupported map[string]struct{}) bool {
	if !scopeDimMatch(filter.PrincipalID, snap.PrincipalID, fields.ScopePrincipalID, unsupported) {
		return false
	}
	if !scopeDimMatch(filter.CredentialID, snap.CredentialID, fields.ScopeCredentialID, unsupported) {
		return false
	}
	if !scopeDimMatch(filter.TenantID, snap.TenantID, fields.ScopeTenantID, unsupported) {
		return false
	}
	if !scopeDimMatch(filter.OrganizationID, snap.OrganizationID, fields.ScopeOrganizationID, unsupported) {
		return false
	}
	if !scopeDimMatch(filter.WorkspaceID, snap.WorkspaceID, fields.ScopeWorkspaceID, unsupported) {
		return false
	}
	if !scopeDimMatch(filter.ProjectID, snap.ProjectID, fields.ScopeProjectID, unsupported) {
		return false
	}
	if !scopeDimMatch(filter.DepartmentID, snap.DepartmentID, fields.ScopeDepartmentID, unsupported) {
		return false
	}
	if !scopeDimMatch(filter.CostCenterID, snap.CostCenterID, fields.ScopeCostCenterID, unsupported) {
		return false
	}
	return true
}

// scopeDimMatch applies one scope dimension's filter unless the store declares
// the dimension unsupported, in which case the dimension imposes no constraint
// (requirement 2.5: reported filters are not applied).
func scopeDimMatch(filter, record scope.Value, field string, unsupported map[string]struct{}) bool {
	if isUnsupportedField(unsupported, field) {
		return true
	}
	return scopeValueMatches(filter, record)
}

// commonFiltersMatch applies the shared common filters to a candidate event,
// skipping any field the store declares unsupported. Reported-but-not-applied
// filters must not narrow the result (requirement 2.5, 8.6, 9.4).
func commonFiltersMatch(c cp.CommonFilters, ev cp.Event, unsupported map[string]struct{}) bool {
	if !scopeFiltersMatch(c.Scope, ev.Scope, unsupported) {
		return false
	}
	if !isUnsupportedField(unsupported, fields.TimeRange) {
		if !timeRangeMatch(c.TimeRange, ev.OccurredAt) {
			return false
		}
	}
	if c.BackendID != "" && !isUnsupportedField(unsupported, fields.BackendID) && c.BackendID != ev.Correlation.BackendID {
		return false
	}
	if c.Model != "" && !isUnsupportedField(unsupported, fields.Model) && c.Model != ev.Correlation.Model {
		return false
	}
	if c.FrontendID != "" && !isUnsupportedField(unsupported, fields.FrontendID) && c.FrontendID != ev.Correlation.FrontendID {
		return false
	}
	if c.TraceID != "" && !isUnsupportedField(unsupported, fields.TraceID) && c.TraceID != ev.Correlation.TraceID {
		return false
	}
	if c.SessionID != "" && !isUnsupportedField(unsupported, fields.SessionID) && c.SessionID != ev.Correlation.SessionID {
		return false
	}
	if c.ALegID != "" && !isUnsupportedField(unsupported, fields.ALegID) && c.ALegID != ev.Correlation.ALegID {
		return false
	}
	if c.BLegID != "" && !isUnsupportedField(unsupported, fields.BLegID) && c.BLegID != ev.Correlation.BLegID {
		return false
	}
	if c.Outcome != "" && !isUnsupportedField(unsupported, fields.Outcome) {
		if !outcomeMatches(ev, c.Outcome) {
			return false
		}
	}
	if c.ReasonCode != "" && !isUnsupportedField(unsupported, fields.ReasonCode) {
		if !reasonCodeMatches(ev, c.ReasonCode) {
			return false
		}
	}
	return true
}

// timeRangeMatch applies a half-open [From, To) range over the event time.
// Either bound may be omitted. To is exclusive so a page bounded by To=now
// does not include events that occurred exactly at the boundary (requirement
// 2.6, 7.4).
func timeRangeMatch(r cp.TimeRange, t time.Time) bool {
	if !r.From.IsZero() && t.Before(r.From) {
		return false
	}
	if !r.To.IsZero() && !t.Before(r.To) {
		return false
	}
	return true
}

func outcomeMatches(ev cp.Event, want string) bool {
	if ev.Attempt() != nil && ev.Attempt().Outcome.IsKnown() {
		if string(ev.Attempt().Outcome) == want {
			return true
		}
	}
	if ev.Attempt() != nil && ev.Attempt().RouteOutcome == want {
		return true
	}
	if ev.Policy() != nil && ev.Policy().Outcome == want {
		return true
	}
	if ev.Auth() != nil && ev.Auth().Outcome == want {
		return true
	}
	return false
}

func reasonCodeMatches(ev cp.Event, want string) bool {
	if ev.Auth() != nil && ev.Auth().ReasonCode == want {
		return true
	}
	if ev.Policy() != nil && ev.Policy().ReasonCode == want {
		return true
	}
	if ev.Audit() != nil && ev.Audit().ReasonCode == want {
		return true
	}
	return false
}

// unsupportedCommonFilters reports the canonical common filter field names
// that the store cannot apply for the given query, in stable order. A field is
// reported when the consumer set a value for it AND the store is configured to
// not support it (requirement 2.5, 8.6, 9.4). Reported filters are NOT applied;
// the consumer is told explicitly rather than receiving a silently widened
// result.
func unsupportedCommonFilters(set map[string]struct{}, c cp.CommonFilters) []cp.UnsupportedFilter {
	var out []cp.UnsupportedFilter
	add := func(field, reason string, set_ bool) {
		if set_ && isUnsupportedField(set, field) {
			out = append(out, cp.UnsupportedFilter{Field: field, Reason: reason})
		}
	}
	add(fields.ScopePrincipalID, "scope principal_id not supported by this store", c.Scope.PrincipalID.IsKnown())
	add(fields.ScopeCredentialID, "scope credential_id not supported by this store", c.Scope.CredentialID.IsKnown())
	add(fields.ScopeTenantID, "scope tenant_id not supported by this store", c.Scope.TenantID.IsKnown())
	add(fields.ScopeOrganizationID, "scope organization_id not supported by this store", c.Scope.OrganizationID.IsKnown())
	add(fields.ScopeWorkspaceID, "scope workspace_id not supported by this store", c.Scope.WorkspaceID.IsKnown())
	add(fields.ScopeProjectID, "scope project_id not supported by this store", c.Scope.ProjectID.IsKnown())
	add(fields.ScopeDepartmentID, "scope department_id not supported by this store", c.Scope.DepartmentID.IsKnown())
	add(fields.ScopeCostCenterID, "scope cost_center_id not supported by this store", c.Scope.CostCenterID.IsKnown())
	add(fields.TimeRange, "time_range not supported by this store", !c.TimeRange.From.IsZero() || !c.TimeRange.To.IsZero())
	add(fields.BackendID, "backend_id not supported by this store", c.BackendID != "")
	add(fields.Model, "model not supported by this store", c.Model != "")
	add(fields.FrontendID, "frontend_id not supported by this store", c.FrontendID != "")
	add(fields.TraceID, "trace_id not supported by this store", c.TraceID != "")
	add(fields.SessionID, "session_id not supported by this store", c.SessionID != "")
	add(fields.ALegID, "a_leg_id not supported by this store", c.ALegID != "")
	add(fields.BLegID, "b_leg_id not supported by this store", c.BLegID != "")
	add(fields.Outcome, "outcome not supported by this store", c.Outcome != "")
	add(fields.ReasonCode, "reason_code not supported by this store", c.ReasonCode != "")
	return out
}

func unsupportedEventFilters(set map[string]struct{}, q cp.EventQuery) []cp.UnsupportedFilter {
	out := unsupportedCommonFilters(set, q.Common)
	if q.Category != "" && isUnsupportedField(set, fields.EventCategory) {
		out = append(out, cp.UnsupportedFilter{Field: fields.EventCategory, Reason: "category not supported by this store"})
	}
	return out
}

func unsupportedSessionFilters(set map[string]struct{}, q cp.SessionQuery) []cp.UnsupportedFilter {
	return unsupportedCommonFilters(set, q.Common)
}

func unsupportedAttemptFilters(set map[string]struct{}, q cp.AttemptQuery) []cp.UnsupportedFilter {
	out := unsupportedCommonFilters(set, q.Common)
	if q.Surfaced != "" && isUnsupportedField(set, fields.AttemptSurfaced) {
		out = append(out, cp.UnsupportedFilter{Field: fields.AttemptSurfaced, Reason: "attempt.surfaced not supported by this store"})
	}
	return out
}

func unsupportedUsageFilters(set map[string]struct{}, q cp.UsageQuery) []cp.UnsupportedFilter {
	out := unsupportedCommonFilters(set, q.Common)
	if q.Plane != "" && isUnsupportedField(set, fields.UsagePlane) {
		out = append(out, cp.UnsupportedFilter{Field: fields.UsagePlane, Reason: "usage.plane not supported by this store"})
	}
	if q.Availability != "" && isUnsupportedField(set, fields.UsageAvailability) {
		out = append(out, cp.UnsupportedFilter{Field: fields.UsageAvailability, Reason: "usage.availability not supported by this store"})
	}
	if q.Perspective != "" && isUnsupportedField(set, fields.UsagePerspective) {
		out = append(out, cp.UnsupportedFilter{Field: fields.UsagePerspective, Reason: "usage.perspective not supported by this store"})
	}
	if q.Boundary != "" && isUnsupportedField(set, fields.UsageBoundary) {
		out = append(out, cp.UnsupportedFilter{Field: fields.UsageBoundary, Reason: "usage.boundary not supported by this store"})
	}
	if q.LifecycleScope != "" && isUnsupportedField(set, fields.UsageLifecycleScope) {
		out = append(out, cp.UnsupportedFilter{Field: fields.UsageLifecycleScope, Reason: "usage.lifecycle_scope not supported by this store"})
	}
	// Usage evidence rows do not carry rule identity; refuse rather than widen (14.8).
	if strings.TrimSpace(q.RuleID) != "" {
		out = append(out, cp.UnsupportedFilter{Field: fields.UsageRuleID, Reason: "usage.rule_id is not indexed on usage evidence"})
	}
	return out
}

// usageDetailMatchesQuery applies dual-plane usage filters against decoded evidence.
// Callers must skip matching when unsupportedUsageFilters already reported a field.
func usageDetailMatchesQuery(u *cp.UsageDetail, q cp.UsageQuery, unsupportedSet map[string]struct{}) bool {
	if u == nil {
		return false
	}
	if q.Plane != "" && !isUnsupportedField(unsupportedSet, fields.UsagePlane) && string(u.Plane) != q.Plane {
		return false
	}
	if q.Availability != "" && !isUnsupportedField(unsupportedSet, fields.UsageAvailability) && string(u.Availability) != q.Availability {
		return false
	}
	if q.Perspective != "" && !isUnsupportedField(unsupportedSet, fields.UsagePerspective) && u.Perspective != q.Perspective {
		return false
	}
	if q.Boundary != "" && !isUnsupportedField(unsupportedSet, fields.UsageBoundary) && u.Boundary != q.Boundary {
		return false
	}
	if q.LifecycleScope != "" && !isUnsupportedField(unsupportedSet, fields.UsageLifecycleScope) && u.LifecycleScope != q.LifecycleScope {
		return false
	}
	return true
}

func unsupportedUsageAggregateFilters(set map[string]struct{}, q cp.UsageAggregateQuery) []cp.UnsupportedFilter {
	return unsupportedCommonFilters(set, q.Common)
}

func unsupportedEvidenceFilters(set map[string]struct{}, q cp.EvidenceQuery) []cp.UnsupportedFilter {
	out := unsupportedCommonFilters(set, q.Common)
	if q.Effect != "" && isUnsupportedField(set, fields.EvidenceEffect) {
		out = append(out, cp.UnsupportedFilter{Field: fields.EvidenceEffect, Reason: "evidence.effect not supported by this store"})
	}
	if q.Category != "" && isUnsupportedField(set, fields.EvidenceCategory) {
		out = append(out, cp.UnsupportedFilter{Field: fields.EvidenceCategory, Reason: "evidence.category not supported by this store"})
	}
	return out
}

func isUnsupportedField(set map[string]struct{}, field string) bool {
	_, ok := set[field]
	return ok
}
