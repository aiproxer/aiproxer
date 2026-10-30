// Package fields owns the canonical filter-field string names reported by
// control-plane ledger stores in cp.UnsupportedFilter.Field. The adapter
// (internal/infra/controlplane/ledgerstore) and the shared store-contract
// suite (ledgerstore/contract) both reference these constants so consumers
// receive stable unsupported-filter reporting across adapters (requirement
// 2.5, 8.6, 9.4).
//
// This package is dependency-free production code; it must not import any
// testing-only package.
package fields

// Scope dimension filter field names.
const (
	ScopePrincipalID    = "scope.principal_id"
	ScopeCredentialID   = "scope.credential_id"
	ScopeTenantID       = "scope.tenant_id"
	ScopeOrganizationID = "scope.organization_id"
	ScopeWorkspaceID    = "scope.workspace_id"
	ScopeProjectID      = "scope.project_id"
	ScopeDepartmentID   = "scope.department_id"
	ScopeCostCenterID   = "scope.cost_center_id"
)

// Common filter field names shared by every query view.
const (
	TimeRange  = "common.time_range"
	BackendID  = "common.backend_id"
	Model      = "common.model"
	FrontendID = "common.frontend_id"
	TraceID    = "common.trace_id"
	SessionID  = "common.session_id"
	ALegID     = "common.a_leg_id"
	BLegID     = "common.b_leg_id"
	Outcome    = "common.outcome"
	ReasonCode = "common.reason_code"
)

// View-specific filter field names.
const (
	AttemptSurfaced     = "attempt.surfaced"
	UsagePlane          = "usage.plane"
	UsageAvailability   = "usage.availability"
	UsagePerspective    = "usage.perspective"
	UsageBoundary       = "usage.boundary"
	UsageLifecycleScope = "usage.lifecycle_scope"
	UsageRuleID         = "usage.rule_id"
	EvidenceEffect      = "evidence.effect"
	EvidenceCategory    = "evidence.category"
	EventCategory       = "event.category"
	UsageGroupBy        = "usage.group_by"
)
