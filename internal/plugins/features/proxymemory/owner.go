package proxymemory

import (
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// Principal identifies the owner of a memory bank: one tenant and one
// authenticated principal.
type Principal struct {
	TenantID    string
	PrincipalID string
}

// Owner identifies exactly one memory bank: one tenant, one principal and one
// opaque project key derived from request content. ProjectKey is never a
// filesystem path.
type Owner struct {
	TenantID    string
	PrincipalID string
	ProjectKey  string
}

// Ineligible is a bounded refusal reason. It is safe as a metric label value
// and never contains principal IDs, project labels or user content.
type Ineligible string

const (
	IneligibleUnknownPrincipal Ineligible = "unknown_principal"
	IneligibleInternal         Ineligible = "internal_origin"
	IneligibleFallback         Ineligible = "fallback_principal"
	IneligibleLocal            Ineligible = "local_principal"
	IneligibleNotAllowlisted   Ineligible = "not_allowlisted"
)

// LocalFallbackPrincipalID mirrors internal/core/auth.LocalUnknownOSPrincipalID.
// The feature tree cannot import internal/core; featurehost asserts equality.
const LocalFallbackPrincipalID = "aip_local_unknown"

// CheckEligible applies the ordered eligibility rules of design §2 to one
// request's principal scope snapshot. It returns the owner principal and true
// only when the request may use memory; otherwise it returns the zero
// principal and the bounded refusal reason.
func CheckEligible(cfg Config, sc scope.PrincipalScopeView) (Principal, Ineligible, bool) {
	principalID := sc.PrincipalID.String()
	if !sc.PrincipalID.IsKnown() || strings.TrimSpace(principalID) == "" {
		return Principal{}, IneligibleUnknownPrincipal, false
	}
	if sc.Origin == scope.OriginInternal {
		return Principal{}, IneligibleInternal, false
	}
	if principalID == LocalFallbackPrincipalID {
		return Principal{}, IneligibleFallback, false
	}
	if sc.SubjectKind == scope.SubjectLocal && !cfg.AllowLocalPrincipal {
		return Principal{}, IneligibleLocal, false
	}
	if !wildcardPrincipals(cfg.Principals) && !slices.Contains(cfg.Principals, principalID) {
		return Principal{}, IneligibleNotAllowlisted, false
	}
	principal := Principal{PrincipalID: principalID}
	if sc.TenantID.IsKnown() {
		principal.TenantID = sc.TenantID.String()
	}
	return principal, "", true
}

// wildcardPrincipals reports whether the allowlist is exactly ["*"].
func wildcardPrincipals(principals []string) bool {
	return len(principals) == 1 && principals[0] == "*"
}

// HasSession reports whether the request carries an established session: a
// proxy-issued session identifier or a client session hint (Requirement 1.9).
// Requests with neither value get a fresh A-leg every time and must not count
// as one session.
func HasSession(v session.SessionView) bool {
	return strings.TrimSpace(v.AuthoritativeSessionID) != "" || strings.TrimSpace(v.ClientSessionHint) != ""
}
