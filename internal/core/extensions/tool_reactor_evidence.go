package extensions

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// ReasonToolReactorFailure records a tool reactor provider failure projected to
// shared evidence. Tool reactors run on the stream path after backend attempt is
// committed, so BackendAttempted is true.
const ReasonToolReactorFailure = "tool_reactor_failure"

// ReasonToolReactorMalformed records a tool reactor rewrite/replace whose output
// failed runner validation. It is distinct from a provider failure: the reactor
// returned a decision without error, but its replacement event was structurally
// illegal and the runner rejected/ignored it.
const ReasonToolReactorMalformed = "tool_reactor_malformed"

// NewToolReactorEvidenceFunc returns a [hooks.ToolReactorEvidenceFunc] that
// projects each tool-reactor decision into shared policy decision evidence and
// emits it through the seam's emitter (requirements 3.3, 4.3, 9.1, 9.5).
//
// The returned func is intended to be attached to the request context via
// [hooks.WithToolReactorEvidence] so [hooks.Bus.ApplyToolReactors] can invoke it
// per reactor without importing the extensions package. A nil seam yields a func
// that emits nothing.
//
// Frontend-specific tool syntax is kept out of evidence semantics: only the
// canonical [sdkhooks.ToolDecision] enum, the reactor's error, and the runner's
// validation error are projected. Provider failures (err != nil) project as
// OutcomeError/EffectNone with ReasonToolReactorFailure. Invalid rewrite/replace
// output (validationErr != nil) projects as OutcomeError/EffectNone with
// ReasonToolReactorMalformed, so a rejected mutation is never recorded as a
// successful allow/mutate or allow/replace. The reactor's failure behavior is
// left to the caller's error policy (the runner preserves existing
// fail-open/fail-closed behavior; evidence is a side effect).
func NewToolReactorEvidenceFunc(ev *DecisionEvidence) hooks.ToolReactorEvidenceFunc {
	return func(ctx context.Context, providerID string, decision sdkhooks.ToolDecision, err error, validationErr error) {
		if ev == nil || ev.Emitter == nil {
			return
		}
		dctx := decisionContextFor(ctx, ev, feature.StageIDToolEventReaction, providerID, false)
		if err != nil {
			rec := recordFromContext(dctx, providerID, true)
			rec.Outcome = policydecision.OutcomeError
			rec.Effect = policydecision.EffectNone
			rec.ReasonCode = ReasonToolReactorFailure
			rec.ClientCategory = CategoryFailure
			emitDecisionRecord(ctx, ev, rec)
			return
		}
		if validationErr != nil {
			rec := recordFromContext(dctx, providerID, true)
			rec.Outcome = policydecision.OutcomeError
			rec.Effect = policydecision.EffectNone
			rec.ReasonCode = ReasonToolReactorMalformed
			rec.ClientCategory = CategoryMalformed
			emitDecisionRecord(ctx, ev, rec)
			return
		}
		emitDecisionRecord(ctx, ev, ProjectToolReactorDecision(dctx, providerID, decision))
	}
}
