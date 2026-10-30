package secretguard

import "context"

type ctxKey int

const keyRequestMatcher ctxKey = iota + 18500 // offset avoids collision with other packages' context keys

// WithRequestMatcher returns a child context carrying an opaque request-scoped Matcher.
// A nil parent is treated as [context.TODO]. A nil matcher clears/omits attachment semantics
// for consumers that check RequestMatcherFromContext.
func WithRequestMatcher(ctx context.Context, m Matcher) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, keyRequestMatcher, m)
}

// RequestMatcherFromContext returns the Matcher attached with [WithRequestMatcher], if any.
// A nil ctx is tolerated and returns (nil, false).
func RequestMatcherFromContext(ctx context.Context) (Matcher, bool) {
	if ctx == nil {
		return nil, false
	}
	raw := ctx.Value(keyRequestMatcher)
	m, ok := raw.(Matcher)
	return m, ok && m != nil
}

// ContextMatcherResolver implements [MatcherResolver] by reading the request-scoped matcher
// from context. When no matcher is present it returns (nil, nil) and never reads the environment.
type ContextMatcherResolver struct{}

// Resolve implements [MatcherResolver].
func (ContextMatcherResolver) Resolve(ctx context.Context) (Matcher, error) {
	m, ok := RequestMatcherFromContext(ctx)
	if ok {
		return m, nil
	}
	return nil, nil
}

var _ MatcherResolver = ContextMatcherResolver{}
