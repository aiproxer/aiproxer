package session

import (
	"context"
	"maps"
)

type sessionViewContextKey struct{}

// SecureTurnPolicyView is the content-free secure-session policy needed by
// SDK consumers. Presence means the proxy has authorized the current turn;
// no secure-session domain object or turn/session identifier is exposed.
type SecureTurnPolicyView struct {
	TranscriptEnabled bool
}

type secureTurnPolicyContextKey struct{}

type secureTurnPolicyContextValue struct {
	view       SecureTurnPolicyView
	authorized bool
}

// WithSessionView attaches a defensive copy of the proxy-authoritative
// session snapshot to ctx. The snapshot is safe for feature plugins and never
// contains credentials or resume proofs.
func WithSessionView(ctx context.Context, view SessionView) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, sessionViewContextKey{}, cloneSessionView(view))
}

// SessionViewFromContext returns a defensive copy of the proxy-authoritative
// session snapshot attached with [WithSessionView].
func SessionViewFromContext(ctx context.Context) (SessionView, bool) {
	if ctx == nil {
		return SessionView{}, false
	}
	view, ok := ctx.Value(sessionViewContextKey{}).(SessionView)
	if !ok {
		return SessionView{}, false
	}
	return cloneSessionView(view), true
}

// WithSecureTurnPolicy attaches the minimal policy view authorized by core
// for the current secure-session turn. It has no wire representation and no
// content, lineage, credential, or resume-token fields.
func WithSecureTurnPolicy(ctx context.Context, view SecureTurnPolicyView) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, secureTurnPolicyContextKey{}, secureTurnPolicyContextValue{
		view:       view,
		authorized: true,
	})
}

// SecureTurnPolicyFromContext returns the authorized policy view attached with
// [WithSecureTurnPolicy].
func SecureTurnPolicyFromContext(ctx context.Context) (SecureTurnPolicyView, bool) {
	if ctx == nil {
		return SecureTurnPolicyView{}, false
	}
	value, ok := ctx.Value(secureTurnPolicyContextKey{}).(secureTurnPolicyContextValue)
	if !ok || !value.authorized {
		return SecureTurnPolicyView{}, false
	}
	return value.view, true
}

// WithoutSecureTurnPolicy masks any inherited secure-turn policy in ctx.
// Core uses this when it projects a detached child without an authoritative
// session, preserving the primary-turn authority boundary for SDK consumers.
func WithoutSecureTurnPolicy(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, secureTurnPolicyContextKey{}, secureTurnPolicyContextValue{})
}

func cloneSessionView(view SessionView) SessionView {
	view.Labels = maps.Clone(view.Labels)
	return view
}
