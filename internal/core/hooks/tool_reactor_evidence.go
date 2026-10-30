package hooks

import (
	"context"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// ToolReactorEvidenceFunc projects a tool-reactor decision into shared policy
// decision evidence when attached to the request context. It is invoked by
// [Bus.ApplyToolReactors] after each reactor returns and the runner has validated
// any rewrite/replace output, with the reactor's provider id, its decision, the
// reactor's returned error (provider failure), and the validation error for an
// invalid rewrite/replace (malformed output). Implementations must not change
// runtime behavior; evidence emission is a side effect isolated from request
// execution.
//
// The func is defined here (in hooks) so the reactor runner can call it without
// importing the extensions package (which would create an import cycle, since
// extensions imports hooks). The extensions package provides a constructor that
// wires the func to the configured evidence emitter and projector.
type ToolReactorEvidenceFunc func(ctx context.Context, providerID string, decision sdk.ToolDecision, err error, validationErr error)

type toolReactorEvidenceCtxKey struct{}

// WithToolReactorEvidence attaches fn to ctx so [Bus.ApplyToolReactors] can emit
// per-reactor policy decision evidence. A nil fn means no evidence is emitted.
func WithToolReactorEvidence(ctx context.Context, fn ToolReactorEvidenceFunc) context.Context {
	if ctx == nil || fn == nil {
		return ctx
	}
	return context.WithValue(ctx, toolReactorEvidenceCtxKey{}, fn)
}

// ToolReactorEvidenceFromContext returns the evidence func attached by
// [WithToolReactorEvidence], or nil when none is attached.
func ToolReactorEvidenceFromContext(ctx context.Context) ToolReactorEvidenceFunc {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(toolReactorEvidenceCtxKey{})
	if raw == nil {
		return nil
	}
	fn, ok := raw.(ToolReactorEvidenceFunc)
	if !ok {
		return nil
	}
	return fn
}
