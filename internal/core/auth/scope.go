package auth

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// ScopeBuildInput is the input to [BuildScope]: a trusted auth decision.
type ScopeBuildInput struct {
	Decision sdkauth.Decision
}

// ScopeBuildResult is the output of [BuildScope]: one authoritative scope snapshot and the
// derived legacy principal projection. The principal is always derived from the scope.
type ScopeBuildResult struct {
	Scope     scope.PrincipalScopeView
	Principal execview.PrincipalView
}

// BuildScope normalizes a trusted auth decision into one authoritative principal/scope
// snapshot plus the derived legacy principal projection.
//
// Precedence (highest first):
//  1. Trusted scope on the decision (Decision.Scope) wins; the principal projection is
//     derived from it and any legacy Decision.Principal is ignored for identity.
//  2. Legacy principal fallback: when no scope is supplied but the decision carries a
//     non-empty principal id, a scope is derived from it. Unknown optional fields remain
//     unknown (no inference). AuthMethod is derived from SatisfiedLevel and CredentialID
//     from Device.KeyID.
//
// Denied or challenged decisions never produce a successful lifecycle scope. Unsafe
// credential-like material in trusted scope values is rejected before lifecycle evidence.
func BuildScope(input ScopeBuildInput) (ScopeBuildResult, error) {
	d := input.Decision
	if d.Outcome != sdkauth.OutcomeAllow {
		return ScopeBuildResult{}, ErrDeniedNoScope
	}
	if d.Scope != nil {
		s := d.Scope.Clone()
		if err := SanitizeScope(s); err != nil {
			return ScopeBuildResult{}, err
		}
		if strings.TrimSpace(s.PrincipalID.String()) == "" {
			return ScopeBuildResult{}, ErrNoIdentity
		}
		return ScopeBuildResult{Scope: s, Principal: s.Principal()}, nil
	}
	if pid := strings.TrimSpace(d.Principal.ID); pid != "" {
		s := ScopeFromLegacyPrincipal(d.Principal)
		s.AuthMethod = authMethodFromLevel(d.SatisfiedLevel)
		if kid := strings.TrimSpace(d.Device.KeyID); kid != "" {
			s.CredentialID = scope.Known(kid)
		}
		if err := SanitizeScope(s); err != nil {
			return ScopeBuildResult{}, err
		}
		return ScopeBuildResult{Scope: s, Principal: s.Principal()}, nil
	}
	return ScopeBuildResult{}, ErrNoIdentity
}

// ScopeFromLegacyPrincipal derives an authoritative scope from a legacy principal view
// without inferring optional org/tenant fields (requirement 3.5). SubjectKind is unknown
// because the legacy view does not carry subject classification. AuthMethod and
// CredentialID remain unknown here; callers that have them (auth BuildScope) set them on
// the returned view. Shared by auth BuildScope and runtime request-scope resolution.
func ScopeFromLegacyPrincipal(p execview.PrincipalView) scope.PrincipalScopeView {
	s := scope.PrincipalScopeView{
		Origin:      scope.OriginClient,
		SubjectKind: scope.SubjectUnknown,
		PrincipalID: scope.Known(strings.TrimSpace(p.ID)),
	}
	if dn := strings.TrimSpace(p.DisplayName); dn != "" {
		s.DisplayName = scope.Known(dn)
	}
	s.Roles = slices.Clone(p.Roles)
	s.SafeClaims = maps.Clone(p.Claims)
	return s
}

func authMethodFromLevel(l sdkauth.RequiredLevel) scope.Value {
	switch l {
	case sdkauth.LevelAPIKey:
		return scope.Known("api_key")
	case sdkauth.LevelAPIKeySSO:
		return scope.Known("api_key_sso")
	case sdkauth.LevelNone:
		return scope.Known("none")
	default:
		return scope.Unknown()
	}
}

// SanitizeScope rejects credential-like material in any scope string field or map value before
// the snapshot enters request lifecycle or audit evidence (requirements 2.6, 5.4). It is the
// shared safety gate called by [BuildScope] for accepted decisions and by the HTTP auth bridge
// for denied/challenged attribution evidence.
//
// The substring heuristic is best-effort defense-in-depth and is not exhaustive; trusted
// callers remain responsible for never placing raw secret material in scope fields.
func SanitizeScope(s scope.PrincipalScopeView) error {
	values := []namedValue{
		{"principal_id", s.PrincipalID},
		{"display_name", s.DisplayName},
		{"auth_method", s.AuthMethod},
		{"credential_id", s.CredentialID},
		{"tenant_id", s.TenantID},
		{"organization_id", s.OrganizationID},
		{"workspace_id", s.WorkspaceID},
		{"project_id", s.ProjectID},
		{"department_id", s.DepartmentID},
		{"cost_center_id", s.CostCenterID},
		{"parent_trace_id", s.ParentTraceID},
	}
	for _, v := range values {
		if v.Value.IsUnknown() {
			continue
		}
		if looksCredentialLike(v.Value.String()) {
			return fmt.Errorf("%w: %s", ErrUnsafeScope, v.Name)
		}
	}
	if slices.ContainsFunc(s.Roles, looksCredentialLike) {
		return fmt.Errorf("%w: roles", ErrUnsafeScope)
	}
	for _, m := range []struct {
		name string
		v    map[string]string
	}{{"safe_claims", s.SafeClaims}, {"policy_labels", s.PolicyLabels}} {
		for k, val := range m.v {
			if looksCredentialLike(k) || looksCredentialLike(val) {
				return fmt.Errorf("%w: %s", ErrUnsafeScope, m.name)
			}
		}
	}
	return nil
}

type namedValue struct {
	Name  string
	Value scope.Value
}

var credentialLikePatterns = []string{
	"bearer ",
	"access_token",
	"refresh_token",
	"id_token",
	"client_secret",
	"password=",
	"api_key=",
	"authorization:",
	"apikey:",
}

func looksCredentialLike(s string) bool {
	low := strings.ToLower(s)
	for _, p := range credentialLikePatterns {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}
