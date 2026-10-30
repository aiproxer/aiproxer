package runtime

import (
	"context"
	"strings"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// resolveRequestScope produces one authoritative principal/scope snapshot for the request
// before secure-session and backend execution (design Runtime Scope Resolver, req 4.1).
//
// Precedence:
//  1. Trusted scope already attached to the context (by the HTTP auth bridge) wins; the
//     principal projection is derived from it and any legacy principal in context is ignored
//     for identity (requirements 2.2, 4.6).
//  2. Legacy principal fallback: when no scope is present but a non-empty principal is
//     attached, a scope is derived via the shared [coreauth.ScopeFromLegacyPrincipal] helper.
//     Unknown optional fields remain unknown (req 3.5).
//  3. Local synthetic fallback: only under the existing local-mode condition
//     (Executor.SyntheticLocalPrincipal), an explicit local single-user scope is produced.
//
// Returns ok=false when no identity is available and local synthesis is disabled; callers
// preserve prior behavior (empty principal, no scope) in that case.
func (e *Executor) resolveRequestScope(ctx context.Context) (scope.PrincipalScopeView, execview.PrincipalView, bool) {
	if s, ok := scope.ScopeFromContext(ctx); ok {
		return s, s.Principal(), true
	}
	if p, ok := execview.PrincipalFromContext(ctx); ok {
		if id := strings.TrimSpace(p.ID); id != "" {
			s := coreauth.ScopeFromLegacyPrincipal(p)
			return s, s.Principal(), true
		}
	}
	if e != nil && e.SyntheticLocalPrincipal {
		s := localSyntheticScopeForRuntime()
		return s, s.Principal(), true
	}
	return scope.PrincipalScopeView{}, execview.PrincipalView{}, false
}

// localSyntheticScopeForRuntime builds the explicit local single-user scope used when the
// executor is operating under the existing local-mode condition (SyntheticLocalPrincipal).
// The principal id and issuer claim match the legacy local-dev synthetic principal so
// secure-session binding and existing tests remain unchanged (requirements 1.4, 2.4, 4.2).
func localSyntheticScopeForRuntime() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		Origin:      scope.OriginClient,
		SubjectKind: scope.SubjectLocal,
		PrincipalID: scope.Known(syntheticLocalPrincipalID),
		AuthMethod:  scope.Known("local_noop"),
		SafeClaims:  map[string]string{"issuer": syntheticLocalPrincipalIssuer},
	}
}

// scopeFromCtx returns the authoritative scope attached to the request context, or the zero
// view when none is present. Used on usage and traffic emission paths so observer evidence
// carries safe attribution from execctx.Views.Scope (requirement 6.4).
func scopeFromCtx(ctx context.Context) scope.PrincipalScopeView {
	if s, ok := scope.ScopeFromContext(ctx); ok {
		return s
	}
	if v, ok := execctx.FromContext(ctx); ok {
		return v.Scope
	}
	return scope.PrincipalScopeView{}
}
