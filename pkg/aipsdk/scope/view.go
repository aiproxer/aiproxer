package scope

import (
	"maps"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// SubjectKind classifies the request caller category (requirement 1.2).
type SubjectKind string

const (
	SubjectUnknown SubjectKind = "unknown"
	SubjectHuman   SubjectKind = "human"
	SubjectService SubjectKind = "service"
	SubjectLocal   SubjectKind = "local"
)

// Origin records whether the request originated from a client or an
// internal auxiliary derivation (requirement 4.4).
type Origin string

const (
	OriginClient   Origin = "client"
	OriginInternal Origin = "internal"
)

// PrincipalScopeView is the authoritative, protocol-neutral principal and
// scope attribution snapshot for one accepted request. It is safe-by-construction:
// raw credentials, raw transport headers, bearer/API/OAuth/resume tokens, and
// unvetted claim values are never fields here.
type PrincipalScopeView struct {
	SubjectKind    SubjectKind       `json:"subject_kind"`
	PrincipalID    Value             `json:"principal_id"`
	DisplayName    Value             `json:"display_name"`
	AuthMethod     Value             `json:"auth_method"`
	CredentialID   Value             `json:"credential_id"`
	Roles          []string          `json:"roles,omitempty"`
	SafeClaims     map[string]string `json:"safe_claims,omitempty"`
	TenantID       Value             `json:"tenant_id"`
	OrganizationID Value             `json:"organization_id"`
	WorkspaceID    Value             `json:"workspace_id"`
	ProjectID      Value             `json:"project_id"`
	DepartmentID   Value             `json:"department_id"`
	CostCenterID   Value             `json:"cost_center_id"`
	PolicyLabels   map[string]string `json:"policy_labels,omitempty"`
	Origin         Origin            `json:"origin"`
	ParentTraceID  Value             `json:"parent_trace_id"`
}

// Clone returns a deep copy of the view so roles, safe claims, and policy
// labels cannot be mutated through the returned view (requirements 5.5, 4.2).
// Nil slices and maps are preserved as nil.
func (v PrincipalScopeView) Clone() PrincipalScopeView {
	out := v
	out.Roles = slices.Clone(v.Roles)
	out.SafeClaims = maps.Clone(v.SafeClaims)
	out.PolicyLabels = maps.Clone(v.PolicyLabels)
	return out
}

// Principal projects the authoritative scope onto the legacy
// [execview.PrincipalView] compatibility shape, preserving identity, display
// label, roles, and claims (requirements 1.5, 4.6, 7.3). Unknown scope values
// project to empty strings; roles and safe claims are copied so callers cannot
// mutate the authoritative scope through the projection.
func (v PrincipalScopeView) Principal() execview.PrincipalView {
	return execview.PrincipalView{
		ID:          v.PrincipalID.String(),
		DisplayName: v.DisplayName.String(),
		Roles:       slices.Clone(v.Roles),
		Claims:      maps.Clone(v.SafeClaims),
	}
}
