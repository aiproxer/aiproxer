package metering

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// ValidateQuery rejects too-broad or unsupported metering journal queries before
// store access (requirements 14.4, 14.5, 14.8).
func ValidateQuery(q Query) error {
	if unsupported := queryClassUnsupported(q.Class); unsupported != "" {
		return ErrQueryUnsupported
	}
	if !HasSelectiveBound(q) {
		return ErrQueryTooBroad
	}
	if q.Limit > MaxQueryLimit {
		return ErrQueryLimitExceeded
	}
	return nil
}

// QueryUnsupported returns filters requested on the metering journal that are not
// indexed or not applicable to historical facts.
func QueryUnsupported(q Query) []UnsupportedFilter {
	out := make([]UnsupportedFilter, 0, 4)
	if unsupported := queryClassUnsupported(q.Class); unsupported != "" {
		out = append(out, UnsupportedFilter{Field: "class", Reason: unsupported})
	}
	if strings.TrimSpace(q.RouteID) != "" {
		out = append(out, UnsupportedFilter{Field: "route_id", Reason: "metering facts do not record route identity"})
	}
	return out
}

func queryClassUnsupported(class QueryClass) string {
	switch class {
	case "", QueryClassHistoricalMetering:
		return ""
	case QueryClassFinancialProjection:
		return "financial projections are not served by the metering journal"
	case QueryClassLiveReservation, QueryClassActiveLease, QueryClassRemainingAuthority:
		return "query class requires an authority or lease querier, not the metering journal"
	default:
		if class != "" && !class.IsKnown() {
			return "unknown query class"
		}
		return "query class is not supported by the metering journal"
	}
}

// HasSelectiveBound reports whether q carries at least one indexed selective
// filter so a store can page without scanning the full journal.
func HasSelectiveBound(q Query) bool {
	if strings.TrimSpace(q.StreamID) != "" ||
		strings.TrimSpace(q.RequestID) != "" ||
		strings.TrimSpace(q.TraceID) != "" ||
		strings.TrimSpace(q.SessionID) != "" ||
		strings.TrimSpace(q.ALegID) != "" ||
		strings.TrimSpace(q.BLegID) != "" ||
		strings.TrimSpace(q.AttemptID) != "" ||
		strings.TrimSpace(q.FrontendID) != "" ||
		strings.TrimSpace(q.BackendID) != "" ||
		strings.TrimSpace(q.Model) != "" ||
		strings.TrimSpace(q.RuleID) != "" {
		return true
	}
	if scopeFilterBound(q.Scope) {
		return true
	}
	return false
}

func scopeFilterBound(s ScopeFilters) bool {
	return scopeValueBound(s.PrincipalID) ||
		scopeValueBound(s.CredentialID) ||
		scopeValueBound(s.TenantID) ||
		scopeValueBound(s.OrganizationID) ||
		scopeValueBound(s.WorkspaceID) ||
		scopeValueBound(s.ProjectID) ||
		scopeValueBound(s.DepartmentID) ||
		scopeValueBound(s.CostCenterID)
}

func scopeValueBound(v scope.Value) bool {
	return v.IsKnown() && strings.TrimSpace(v.Value) != ""
}

// FactMatchesQuery applies every supported filter to one fact.
func FactMatchesQuery(f Fact, q Query) bool {
	if streamID := strings.TrimSpace(q.StreamID); streamID != "" && strings.TrimSpace(f.StreamID) != streamID {
		return false
	}
	if requestID := strings.TrimSpace(q.RequestID); requestID != "" && strings.TrimSpace(f.Correlation.RequestID) != requestID {
		return false
	}
	if traceID := strings.TrimSpace(q.TraceID); traceID != "" && strings.TrimSpace(f.Correlation.TraceID) != traceID {
		return false
	}
	if sessionID := strings.TrimSpace(q.SessionID); sessionID != "" && strings.TrimSpace(f.Correlation.SessionID) != sessionID {
		return false
	}
	if aLeg := strings.TrimSpace(q.ALegID); aLeg != "" && strings.TrimSpace(f.Correlation.ALegID) != aLeg {
		return false
	}
	if bLeg := strings.TrimSpace(q.BLegID); bLeg != "" && strings.TrimSpace(f.Correlation.BLegID) != bLeg {
		return false
	}
	if attemptID := strings.TrimSpace(q.AttemptID); attemptID != "" && strings.TrimSpace(f.Correlation.AttemptID) != attemptID {
		return false
	}
	if frontendID := strings.TrimSpace(q.FrontendID); frontendID != "" && strings.TrimSpace(f.FrontendID) != frontendID {
		return false
	}
	if backendID := strings.TrimSpace(q.BackendID); backendID != "" && strings.TrimSpace(f.BackendID) != backendID {
		return false
	}
	if model := strings.TrimSpace(q.Model); model != "" && strings.TrimSpace(f.Model) != model {
		return false
	}
	if ruleID := strings.TrimSpace(q.RuleID); ruleID != "" && strings.TrimSpace(f.PolicyVersion.ID) != ruleID {
		return false
	}
	if q.Perspective != "" && f.Perspective != q.Perspective {
		return false
	}
	if q.Boundary != "" && f.Boundary != q.Boundary {
		return false
	}
	if q.Lifecycle != "" && f.Lifecycle != q.Lifecycle {
		return false
	}
	if q.Source != "" && f.Source != q.Source {
		return false
	}
	if q.Authority != "" && f.Authority != q.Authority {
		return false
	}
	if q.IdentityVersion != 0 && f.EffectiveIdentityVersion() != q.IdentityVersion {
		return false
	}
	if !scopeFiltersMatch(q.Scope, f.Scope) {
		return false
	}
	if !timeRangeMatch(q.TimeRange, f.RecordedAt) {
		return false
	}
	return true
}

func scopeFiltersMatch(filter ScopeFilters, target scope.PrincipalScopeView) bool {
	return scopeValueMatches(filter.PrincipalID, target.PrincipalID) &&
		scopeValueMatches(filter.CredentialID, target.CredentialID) &&
		scopeValueMatches(filter.TenantID, target.TenantID) &&
		scopeValueMatches(filter.OrganizationID, target.OrganizationID) &&
		scopeValueMatches(filter.WorkspaceID, target.WorkspaceID) &&
		scopeValueMatches(filter.ProjectID, target.ProjectID) &&
		scopeValueMatches(filter.DepartmentID, target.DepartmentID) &&
		scopeValueMatches(filter.CostCenterID, target.CostCenterID)
}

func scopeValueMatches(filter, actual scope.Value) bool {
	if !filter.IsKnown() {
		return true
	}
	return filter.Equal(actual)
}

func timeRangeMatch(tr TimeRange, at time.Time) bool {
	if !tr.From.IsZero() && at.Before(tr.From) {
		return false
	}
	if !tr.To.IsZero() && at.After(tr.To) {
		return false
	}
	return true
}
