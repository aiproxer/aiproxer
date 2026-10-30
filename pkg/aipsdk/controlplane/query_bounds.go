package controlplane

import (
	"errors"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// QueryClass distinguishes historical metering, live authority, lease, and
// proprietary financial projections (requirement 14.5).
type QueryClass string

const (
	QueryClassHistoricalMetering  QueryClass = "historical_metering"
	QueryClassLiveReservation     QueryClass = "live_reservation"
	QueryClassActiveLease         QueryClass = "active_lease"
	QueryClassRemainingAuthority  QueryClass = "remaining_authority"
	QueryClassFinancialProjection QueryClass = "financial_projection"
	QueryClassPendingTerminalWork QueryClass = "pending_terminal_work"
)

// IsKnown reports whether c is a documented query class.
func (c QueryClass) IsKnown() bool {
	switch c {
	case QueryClassHistoricalMetering, QueryClassLiveReservation, QueryClassActiveLease,
		QueryClassRemainingAuthority, QueryClassFinancialProjection, QueryClassPendingTerminalWork:
		return true
	default:
		return false
	}
}

// ErrQueryTooBroad is returned when a query lacks selective bounds (14.4, 14.8).
var ErrQueryTooBroad = errors.New("controlplane: query too broad")

// ErrQueryUnsupported is returned when a query class or filter is unsupported (14.5, 14.8).
var ErrQueryUnsupported = errors.New("controlplane: query unsupported")

// ValidateUsageQuery rejects too-broad or unsupported usage evidence queries.
func ValidateUsageQuery(q UsageQuery) error {
	if q.Class == QueryClassFinancialProjection {
		return ErrQueryUnsupported
	}
	if q.Class != "" && q.Class != QueryClassHistoricalMetering && q.Class.IsKnown() {
		return ErrQueryUnsupported
	}
	if !usageQuerySelectiveBound(q) {
		return ErrQueryTooBroad
	}
	return nil
}

// ValidateAccountingLimitStatusQuery rejects queries that do not target remaining
// live authority rows.
func ValidateAccountingLimitStatusQuery(q AccountingLimitStatusQuery) error {
	if q.Class == QueryClassFinancialProjection {
		return ErrQueryUnsupported
	}
	if q.Class != "" && q.Class != QueryClassRemainingAuthority && q.Class.IsKnown() {
		return ErrQueryUnsupported
	}
	if !accountingLimitSelectiveBound(q) {
		return ErrQueryTooBroad
	}
	return nil
}

// ValidateAccountingDecisionQuery rejects too-broad decision history queries.
func ValidateAccountingDecisionQuery(q AccountingDecisionQuery) error {
	if q.Class == QueryClassFinancialProjection {
		return ErrQueryUnsupported
	}
	if q.Class != "" && q.Class != QueryClassLiveReservation && q.Class != QueryClassHistoricalMetering && q.Class.IsKnown() {
		return ErrQueryUnsupported
	}
	if !accountingDecisionSelectiveBound(q) {
		return ErrQueryTooBroad
	}
	return nil
}

func usageQuerySelectiveBound(q UsageQuery) bool {
	c := q.Common
	if strings.TrimSpace(c.BackendID) != "" ||
		strings.TrimSpace(c.Model) != "" ||
		strings.TrimSpace(c.FrontendID) != "" ||
		strings.TrimSpace(c.TraceID) != "" ||
		strings.TrimSpace(c.SessionID) != "" ||
		strings.TrimSpace(c.ALegID) != "" ||
		strings.TrimSpace(c.BLegID) != "" ||
		strings.TrimSpace(q.RuleID) != "" {
		return true
	}
	if scopeFilterBound(c.Scope) {
		return true
	}
	return false
}

func accountingLimitSelectiveBound(q AccountingLimitStatusQuery) bool {
	if strings.TrimSpace(q.RuleID) != "" {
		return true
	}
	c := q.Common
	if strings.TrimSpace(c.BackendID) != "" ||
		strings.TrimSpace(c.Model) != "" ||
		strings.TrimSpace(c.FrontendID) != "" ||
		strings.TrimSpace(c.TraceID) != "" ||
		strings.TrimSpace(c.SessionID) != "" ||
		strings.TrimSpace(c.ALegID) != "" ||
		strings.TrimSpace(c.BLegID) != "" {
		return true
	}
	return scopeFilterBound(c.Scope)
}

func accountingDecisionSelectiveBound(q AccountingDecisionQuery) bool {
	if strings.TrimSpace(q.RuleID) != "" {
		return true
	}
	c := q.Common
	if strings.TrimSpace(c.BackendID) != "" ||
		strings.TrimSpace(c.Model) != "" ||
		strings.TrimSpace(c.FrontendID) != "" ||
		strings.TrimSpace(c.TraceID) != "" ||
		strings.TrimSpace(c.SessionID) != "" ||
		strings.TrimSpace(c.ALegID) != "" ||
		strings.TrimSpace(c.BLegID) != "" ||
		strings.TrimSpace(c.Outcome) != "" ||
		strings.TrimSpace(c.ReasonCode) != "" {
		return true
	}
	return scopeFilterBound(c.Scope)
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
