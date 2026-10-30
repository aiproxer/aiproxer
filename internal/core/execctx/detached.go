package execctx

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// SessionMode identifies the trusted execution policy selected by core before
// an auxiliary call enters the executor. It is intentionally not part of the
// canonical Call or any frontend/wire contract.
type SessionMode uint8

const (
	SessionModeNormal SessionMode = iota
	SessionModeDetached
)

type detachedContextKey struct{}

// DetachedSession carries content-free parent lineage for one private
// auxiliary execution. Parent identifiers are correlation only; the child
// executor allocates its own A-leg and never uses these values as route or
// secure-session authority.
type DetachedSession struct {
	ParentSessionID     string
	ParentALegID        string
	ParentTraceID       string
	ParentBranchBinding string
	// AuxiliaryRole is trusted, content-free metadata for internal workload
	// classification. It is never copied into a canonical call or used as
	// session, route, or continuity authority.
	AuxiliaryRole string
}

type detachedContextValue struct {
	mode SessionMode
	meta DetachedSession
}

// WithDetachedSession marks ctx with trusted internal detached execution
// policy and a defensive copy of the parent lineage metadata.
func WithDetachedSession(ctx context.Context, meta DetachedSession) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return session.WithoutSecureTurnPolicy(context.WithValue(ctx, detachedContextKey{}, detachedContextValue{
		mode: SessionModeDetached,
		meta: meta,
	}))
}

// SessionModeFromContext returns the explicitly selected internal mode. The
// bool distinguishes the normal default from a trusted detached marker.
func SessionModeFromContext(ctx context.Context) (SessionMode, bool) {
	if ctx == nil {
		return SessionModeNormal, false
	}
	v, ok := ctx.Value(detachedContextKey{}).(detachedContextValue)
	if !ok {
		return SessionModeNormal, false
	}
	return v.mode, v.mode == SessionModeDetached
}

// DetachedSessionFromContext returns the trusted parent lineage, if ctx was
// marked for detached execution.
func DetachedSessionFromContext(ctx context.Context) (DetachedSession, bool) {
	if ctx == nil {
		return DetachedSession{}, false
	}
	v, ok := ctx.Value(detachedContextKey{}).(detachedContextValue)
	if !ok || v.mode != SessionModeDetached {
		return DetachedSession{}, false
	}
	return v.meta, true
}
