package execctx

import "context"

type auxiliaryRoleKey struct{}

// WithAuxiliaryRole attaches the opaque label of the auxiliary request that
// produced a call.
//
// The label is a *label*, not an authority: it says which auxiliary request
// produced the call, never who may make it. The core's auxiliary client sets
// it when it builds the child context, so a request-part hook cannot remove
// it and a client-facing field cannot own it. The executor captures it once,
// at the assembly boundary, into the typed frozen facts; the post-freeze path
// never reads it from a context.
func WithAuxiliaryRole(ctx context.Context, role string) context.Context {
	if ctx == nil || role == "" {
		return ctx
	}
	return context.WithValue(ctx, auxiliaryRoleKey{}, role)
}

// AuxiliaryRoleFromContext returns the auxiliary role label attached with
// [WithAuxiliaryRole].
//
// It is an assembly-boundary read only: the post-freeze attempt-open path
// must read the captured typed fact instead, and the frozen-facts ratchet
// forbids this call there.
func AuxiliaryRoleFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	role, ok := ctx.Value(auxiliaryRoleKey{}).(string)
	if !ok || role == "" {
		return "", false
	}
	return role, true
}
