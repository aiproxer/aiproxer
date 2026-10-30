package extensions

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// NewSubmitEvidenceFunc returns a [hooks.SubmitEvidenceFunc] that projects each
// submit-hook outcome into shared policy decision evidence and emits it through
// the seam's emitter (requirements 3.5, 4.6, 9.1, 9.5).
//
// The returned func is intended to be attached to the request context via
// [hooks.WithSubmitEvidence] so [hooks.Bus.RunSubmit] can invoke it per hook
// without importing the extensions package. A nil seam (or nil emitter) yields a
// func that emits nothing, preserving the no-observer/non-interference default
// (requirements 7.6, 10.5).
//
// Submit hooks may mutate the canonical call, but [sdk.SubmitDecision] does not
// report mutation, so the projector only emits compatible evidence for
// representable outcomes: reject (deny/none), annotation (allow/annotate), and
// provider failure (error/none). A no-op hook (no reject, no error, no added
// annotations) has no representable policy semantics and emits nothing; runtime
// behavior is still preserved (requirements 9.5, 10.5).
func NewSubmitEvidenceFunc(ev *DecisionEvidence) hooks.SubmitEvidenceFunc {
	return func(ctx context.Context, providerID string, rejected bool, annotations map[string]string, err error) {
		if ev == nil || ev.Emitter == nil {
			return
		}
		dctx := decisionContextFor(ctx, ev, feature.StageIDSubmit, providerID, false)
		rec, ok := ProjectSubmitOutcome(dctx, providerID, rejected, annotations, err)
		if !ok {
			return
		}
		emitDecisionRecord(ctx, ev, rec)
	}
}
