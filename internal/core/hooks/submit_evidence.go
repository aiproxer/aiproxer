package hooks

import (
	"context"
)

// SubmitEvidenceFunc projects a submit-hook outcome into shared policy decision
// evidence when attached to the request context. It is invoked by [Bus.RunSubmit]
// after each submit hook returns, with the hook's provider id, whether it
// rejected the call, the annotations it added to [sdk.SubmitMeta.Annotations],
// and the hook's returned error (provider failure). Implementations must not
// change runtime behavior; evidence emission is a side effect isolated from
// request execution and preserves [sdk.SubmitRejectError] semantics.
//
// The func is defined here (in hooks) so the submit runner can call it without
// importing the extensions package (which would create an import cycle, since
// extensions imports hooks). The extensions package provides a constructor that
// wires the func to the configured evidence emitter and projector, mirroring
// [ToolReactorEvidenceFunc].
//
// A nil fn means no evidence is emitted.
type SubmitEvidenceFunc func(ctx context.Context, providerID string, rejected bool, annotations map[string]string, err error)

type submitEvidenceCtxKey struct{}

// WithSubmitEvidence attaches fn to ctx so [Bus.RunSubmit] can emit per-hook
// policy decision evidence. A nil fn means no evidence is emitted.
func WithSubmitEvidence(ctx context.Context, fn SubmitEvidenceFunc) context.Context {
	if ctx == nil || fn == nil {
		return ctx
	}
	return context.WithValue(ctx, submitEvidenceCtxKey{}, fn)
}

// SubmitEvidenceFromContext returns the evidence func attached by
// [WithSubmitEvidence], or nil when none is attached.
func SubmitEvidenceFromContext(ctx context.Context) SubmitEvidenceFunc {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(submitEvidenceCtxKey{})
	if raw == nil {
		return nil
	}
	fn, ok := raw.(SubmitEvidenceFunc)
	if !ok {
		return nil
	}
	return fn
}
