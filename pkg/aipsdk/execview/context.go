package execview

import (
	"context"
	"strings"
)

type ctxKey int

// offset avoids collision with other context values in the same process (aligned with previous httpauth key range).
const (
	keyPrincipal ctxKey = iota + 18300
	keyFrontendID
)

// WithPrincipal returns a child context carrying the canonical principal for downstream
// handlers and the execution pipeline. A nil parent is treated as [context.TODO] so the
// result is always non-nil and safe for functions that do not allow nil [context.Context].
//
// Transport auth sets this at the edge; the policy core reads the same value without importing
// transport packages (introduce-hexagonal-architecture task 4.1).
func WithPrincipal(ctx context.Context, p PrincipalView) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, keyPrincipal, p)
}

// PrincipalFromContext returns the principal attached with [WithPrincipal], if any.
// A nil ctx is tolerated and returns (PrincipalView{}, false).
func PrincipalFromContext(ctx context.Context) (PrincipalView, bool) {
	if ctx == nil {
		return PrincipalView{}, false
	}
	raw := ctx.Value(keyPrincipal)
	p, ok := raw.(PrincipalView)
	return p, ok
}

// WithFrontendID returns a child context carrying the auth wire frontend id (same vocabulary as
// transport auth decision events, e.g. openai_compatible, anthropic, gemini). Transport wiring sets
// this at the edge; the executor reads it for session-start audit without importing HTTP packages.
// Whitespace-only ids are ignored (returns ctx unchanged) so a caller does not clobber a parent value.
// A nil parent is treated as [context.TODO].
func WithFrontendID(ctx context.Context, frontendID string) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	id := strings.TrimSpace(frontendID)
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, keyFrontendID, id)
}

// FrontendIDFromContext returns the frontend id attached with [WithFrontendID], if any.
// The second result is false when unset or when the stored value is not a string.
// A nil ctx is tolerated and returns ("", false).
func FrontendIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	raw := ctx.Value(keyFrontendID)
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	return s, true
}
