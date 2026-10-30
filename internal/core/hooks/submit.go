package hooks

import (
	"context"
	"fmt"
	"maps"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// RunSubmit executes submit hooks in order. meta may be nil; a working meta map is allocated.
//
// When a [SubmitEvidenceFunc] is attached to ctx via [WithSubmitEvidence], the runner
// invokes it once per hook after the hook returns, with the hook's provider id, reject
// flag, the annotations the hook added to meta.Annotations, and the hook's returned
// error. Evidence emission is a side effect isolated from request execution: return
// values and [sdk.SubmitRejectError] semantics are unchanged. A nil seam emits nothing.
func (b *Bus) RunSubmit(ctx context.Context, call *aipapi.Call, meta *sdk.SubmitMeta) error {
	if call == nil {
		return fmt.Errorf("hooks: nil call: %w", aipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("hooks: %w", aipapi.ErrNilContext)
	}
	if meta == nil {
		meta = &sdk.SubmitMeta{}
	}
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	submit := []sdk.SubmitHook{}
	if b != nil {
		submit = b.submit
	}
	submitEvidence := SubmitEvidenceFromContext(ctx)
	for _, h := range submit {
		if execctx.IsSuppressedPluginID(ctx, h.ID()) {
			continue
		}
		var annotationsBefore map[string]string
		if submitEvidence != nil {
			annotationsBefore = maps.Clone(meta.Annotations)
		}
		dec, err := safety.CallValue(safety.BoundaryExtension, "submit_hook", func() (sdk.SubmitDecision, error) {
			return h.Handle(ctx, call, meta)
		})
		if submitEvidence != nil {
			diff := submitAnnotationsDiff(meta.Annotations, annotationsBefore)
			// Skip the seam call for pure no-op hooks (no reject, no error, no added
			// annotations): such outcomes have no representable policy semantics, so
			// [ProjectSubmitOutcome] would return ok=false. Avoiding the spurious call
			// keeps the no-observer path quiet and matches the projector's skip.
			if dec.Reject || err != nil || len(diff) > 0 {
				submitEvidence(ctx, h.ID(), dec.Reject, diff, err)
			}
		}
		if err != nil {
			if h.FailureMode() == sdk.FailOpen {
				logFailOpenHookPanic(ctx, "submit_hook", h.ID(), err)
				continue
			}
			return fmt.Errorf("submit hook %q: %w", h.ID(), err)
		}
		if dec.Reject {
			return &sdk.SubmitRejectError{HookID: h.ID(), Reason: dec.Reason}
		}
	}
	if err := call.Validate(); err != nil {
		return fmt.Errorf("submit hooks: invalid canonical call after submit chain: %w", err)
	}
	return nil
}

// submitAnnotationsDiff returns the annotations a submit hook added to meta.Annotations
// relative to the before snapshot. Nil when the hook added nothing, so a no-op hook
// produces no evidence (matching [ProjectSubmitOutcome]'s ok=false skip semantics).
func submitAnnotationsDiff(after, before map[string]string) map[string]string {
	if len(after) == 0 {
		return nil
	}
	if len(before) == 0 {
		return maps.Clone(after)
	}
	diff := make(map[string]string, len(after))
	for k, v := range after {
		if bv, ok := before[k]; !ok || bv != v {
			diff[k] = v
		}
	}
	if len(diff) == 0 {
		return nil
	}
	return diff
}
