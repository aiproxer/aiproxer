package httpauth

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// WithPrincipal is a transport-named alias for [execview.WithPrincipal] so the auth middleware
// and the policy core share one context key without the core importing this package.
func WithPrincipal(ctx context.Context, p execview.PrincipalView) context.Context {
	return execview.WithPrincipal(ctx, p)
}

// PrincipalFromContext is a transport-named alias for [execview.PrincipalFromContext].
func PrincipalFromContext(ctx context.Context) (execview.PrincipalView, bool) {
	return execview.PrincipalFromContext(ctx)
}

// WithScope is a transport-named alias for [scope.WithScope] so transport auth and the core
// share one context key for the authoritative principal/scope snapshot.
func WithScope(ctx context.Context, v scope.PrincipalScopeView) context.Context {
	return scope.WithScope(ctx, v)
}

// ScopeFromContext is a transport-named alias for [scope.ScopeFromContext].
func ScopeFromContext(ctx context.Context) (scope.PrincipalScopeView, bool) {
	return scope.ScopeFromContext(ctx)
}

// WithIngressAttribution delegates to [secretguard.WithIngressAttribution].
func WithIngressAttribution(ctx context.Context, a IngressAttribution) context.Context {
	return secretguard.WithIngressAttribution(ctx, a)
}

// IngressAttributionFromContext delegates to [secretguard.IngressAttributionFromContext].
func IngressAttributionFromContext(ctx context.Context) (IngressAttribution, bool) {
	return secretguard.IngressAttributionFromContext(ctx)
}

// WithCredentialMatcher delegates to [secretguard.WithRequestMatcher].
func WithCredentialMatcher(ctx context.Context, m secretguard.Matcher) context.Context {
	return secretguard.WithRequestMatcher(ctx, m)
}

// CredentialMatcherFromContext delegates to [secretguard.RequestMatcherFromContext].
func CredentialMatcherFromContext(ctx context.Context) (secretguard.Matcher, bool) {
	return secretguard.RequestMatcherFromContext(ctx)
}
