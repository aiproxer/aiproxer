package extensions

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// AttemptEvidenceFunc projects an attempt-lifecycle failure into shared policy
// decision evidence when attached to the request context. It is invoked at the
// attempt-record boundary (the narrow runtime seam) for attempt failures that
// [ProjectAttemptObservation] can represent. Implementations must not change
// retry/failover behavior or no-output/failover invariants; evidence emission is
// a side effect isolated from request execution.
//
// The func is defined in extensions (not a context seam in hooks) because the
// attempt lifecycle runner lives in internal/core/runtime, which already imports
// extensions. Carrying it on the context preserves the no-observer/
// non-interference default: when no seam is attached, no evidence is emitted
// (requirements 7.6, 10.5).
//
// A nil fn means no evidence is emitted.
type AttemptEvidenceFunc func(ctx context.Context, providerID string, err error)

type attemptEvidenceCtxKey struct{}

// WithAttemptEvidence attaches fn to ctx so the runtime attempt-record boundary
// can emit per-attempt policy decision evidence. A nil fn means no evidence is
// emitted.
func WithAttemptEvidence(ctx context.Context, fn AttemptEvidenceFunc) context.Context {
	if ctx == nil || fn == nil {
		return ctx
	}
	return context.WithValue(ctx, attemptEvidenceCtxKey{}, fn)
}

// AttemptEvidenceFromContext returns the evidence func attached by
// [WithAttemptEvidence], or nil when none is attached.
func AttemptEvidenceFromContext(ctx context.Context) AttemptEvidenceFunc {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value(attemptEvidenceCtxKey{})
	if raw == nil {
		return nil
	}
	fn, ok := raw.(AttemptEvidenceFunc)
	if !ok {
		return nil
	}
	return fn
}

// NewAttemptEvidenceFunc returns an [AttemptEvidenceFunc] that projects an
// attempt-lifecycle failure into shared policy decision evidence and emits it
// through the seam's emitter (requirements 3.6, 4.5, 7.2, 7.5).
//
// The returned func is intended to be attached to the request context via
// [WithAttemptEvidence] by the stream path so the attempt-record boundary can
// invoke it for attempt failures. A nil seam (or nil emitter) yields a func that
// emits nothing, preserving the no-observer/non-interference default
// (requirements 7.6, 10.5).
//
// A successful attempt (err == nil) has no representable policy semantics and
// emits nothing; runtime behavior is still preserved (requirements 9.5, 10.5).
// The attempt stage runs after backend attempt is committed, so
// BackendAttempted is true on projected records.
func NewAttemptEvidenceFunc(ev *DecisionEvidence) AttemptEvidenceFunc {
	return func(ctx context.Context, providerID string, err error) {
		if ev == nil || ev.Emitter == nil {
			return
		}
		dctx := decisionContextFor(ctx, ev, feature.StageIDAttemptLifecycle, providerID, false)
		rec, ok := ProjectAttemptObservation(dctx, providerID, err)
		if !ok {
			return
		}
		emitDecisionRecord(ctx, ev, rec)
	}
}
