package scope

import "context"

type ctxKey int

const keyScope ctxKey = iota + 18400 // offset avoids collision with other packages' context keys

// WithScope returns a child context carrying the authoritative principal/scope snapshot for
// downstream handlers and the execution pipeline. The value is cloned so callers cannot
// mutate the stored scope through the returned view (requirement 5.5). A nil parent is
// treated as [context.TODO] so the result is always non-nil.
func WithScope(ctx context.Context, v PrincipalScopeView) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, keyScope, v.Clone())
}

// ScopeFromContext returns the authoritative scope attached with [WithScope], if any.
// The returned view is a copy of the stored snapshot.
// A nil ctx is tolerated and returns (PrincipalScopeView{}, false).
func ScopeFromContext(ctx context.Context) (PrincipalScopeView, bool) {
	if ctx == nil {
		return PrincipalScopeView{}, false
	}
	raw := ctx.Value(keyScope)
	v, ok := raw.(PrincipalScopeView)
	if !ok {
		return PrincipalScopeView{}, false
	}
	return v.Clone(), true
}
