package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// maxClampPreviewIterations bounds the strictly-narrowing clamp preview loop
// (adapter PreviewAttempt comment; design Clamp Preview / V-15).
const maxClampPreviewIterations = 4

// attemptClampPreviewFunc previews non-widening clamps for one bounded-loop
// iteration without recording durable admission evidence or reservations.
type attemptClampPreviewFunc func(ctx context.Context, in authority.AttemptAdmission) ([]authority.Clamp, error)

// resolveAttemptClampPreviewer selects the clamp-preview path: the full
// AttemptCoordinator when at least one slot implements AttemptClampPreviewer,
// otherwise a direct UsageAuthority adapter fallback (V-15) so single-provider
// deployments without a multi-provider AttemptCoordinator still get bounded,
// side-effect-free clamp preview.
func (e *Executor) resolveAttemptClampPreviewer() attemptClampPreviewFunc {
	if e == nil {
		return nil
	}
	if e.AttemptCoordinator != nil {
		for _, slot := range e.AttemptCoordinator.Slots {
			if _, ok := slot.Provider.(authority.AttemptClampPreviewer); ok {
				return e.AttemptCoordinator.PreviewClamps
			}
		}
		return nil
	}
	adapter := newUsageAuthorityProviderAdapter(e.UsageAuthority)
	if adapter == nil {
		return nil
	}
	return func(ctx context.Context, in authority.AttemptAdmission) ([]authority.Clamp, error) {
		d, err := adapter.PreviewAttempt(ctx, in)
		if err != nil {
			return nil, err
		}
		if d.Kind == authority.DecisionDeny {
			return nil, fmt.Errorf("executor: attempt clamp preview denied by usage authority")
		}
		return d.Clamps, nil
	}
}

// previewAndApplyAttemptClamps runs side-effect-free bounded strictly-narrowing
// clamp preview before backend-ingress freeze (design Final Attempt Sequence /
// Clamp Preview). Each iteration rebuilds exposure from the current call,
// previews, applies known clamps to a clone, and verifies non-widening.
func (e *Executor) previewAndApplyAttemptClamps(
	ctx context.Context,
	call *aipapi.Call,
	c routing.AttemptCandidate,
	aLegID string,
	blegID string,
) (previewed []authority.Clamp, previewRan bool, err error) {
	if e == nil || call == nil {
		return nil, false, nil
	}
	preview := e.resolveAttemptClampPreviewer()
	if preview == nil {
		return nil, false, nil
	}

	var converged []authority.Clamp
	working := aipapi.CloneCall(*call)
	for range maxClampPreviewIterations {
		qs := e.previewExposureQuantities(ctx, working)
		in := authority.AttemptAdmission{
			RequestID:   strings.TrimSpace(working.ID),
			AttemptID:   strings.TrimSpace(blegID),
			BLegID:      strings.TrimSpace(blegID),
			ALegID:      strings.TrimSpace(aLegID),
			BackendID:   strings.TrimSpace(c.Primary.Backend),
			Model:       strings.TrimSpace(c.Primary.Model),
			Perspective: metering.PerspectiveOperator,
			Lifecycle:   metering.LifecycleBackendAttempt,
			Scope:       scopeFromCtx(ctx),
			Exposure: economics.ExposureBasis{
				Perspective: metering.PerspectiveOperator,
				Boundary:    metering.BoundaryBackendIngress,
				Lifecycle:   metering.LifecycleBackendAttempt,
				Quantities:  qs,
				Output:      previewOutputAssumption(working),
			},
		}
		next, perr := preview(ctx, in)
		if perr != nil {
			return nil, true, fmt.Errorf("executor: attempt clamp preview: %w", perr)
		}
		candidate := aipapi.CloneCall(working)
		if aerr := e.applyPreviewClamps(ctx, &candidate, c, next, inputTokensFromQuantities(qs)); aerr != nil {
			return nil, true, aerr
		}
		if werr := checkpoint.AssertNotWidened(working, candidate); werr != nil {
			return nil, true, fmt.Errorf("executor: preview clamp widened exposure: %w", werr)
		}
		if clampSetsEqualExact(converged, next) && maxOutputEqual(working, candidate) {
			*call = working
			return converged, true, nil
		}
		converged = cloneAuthorityClamps(next)
		working = candidate
	}
	return nil, true, fmt.Errorf("executor: attempt clamp preview did not converge within %d iterations", maxClampPreviewIterations)
}

func (e *Executor) previewExposureQuantities(ctx context.Context, call aipapi.Call) []metering.Quantity {
	qs := checkpoint.QuantitiesFromCall(call)
	if e == nil || e.AdminCountService == nil {
		return qs
	}
	count, err := e.AdminCountService.CountCall(ctx, accountingapp.CountCallInput{
		CallID: strings.TrimSpace(call.ID),
		Call:   call,
	})
	if err != nil {
		return qs
	}
	return checkpoint.MergeQuantities(qs, countedInputQuantities(count))
}

func previewOutputAssumption(call aipapi.Call) economics.ConservativeOutputAssumption {
	if call.Options.MaxOutputTokens == nil {
		return economics.ConservativeOutputAssumption{}
	}
	return economics.ConservativeOutputAssumption{
		BoundKind:  economics.OutputBoundClientProvided,
		TokenCount: int64(*call.Options.MaxOutputTokens),
		Present:    true,
	}
}

func inputTokensFromQuantities(qs []metering.Quantity) int64 {
	for _, q := range qs {
		if q.Component == metering.ComponentInputToken && q.Present {
			return q.Value
		}
	}
	return 0
}

func (e *Executor) applyPreviewClamps(ctx context.Context, call *aipapi.Call, c routing.AttemptCandidate, clamps []authority.Clamp, inputTokens int64) error {
	if call == nil {
		return nil
	}
	for _, clamp := range clamps {
		switch clamp.Kind {
		case authority.ClampMaxOutputTokens:
			if clamp.Value < 0 {
				return fmt.Errorf("executor: preview max_output_tokens clamp negative")
			}
			maxOut := int(clamp.Value)
			if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens >= 0 && *call.Options.MaxOutputTokens < maxOut {
				continue
			}
			call.Options.MaxOutputTokens = &maxOut
		default:
			return fmt.Errorf("executor: preview unknown clamp kind %q", clamp.Kind)
		}
	}
	return nil
}

func clampSetsEqualExact(a, b []authority.Clamp) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !clampExactInSet(b, x) {
			return false
		}
	}
	for _, y := range b {
		if !clampExactInSet(a, y) {
			return false
		}
	}
	return true
}

func clampExactInSet(set []authority.Clamp, want authority.Clamp) bool {
	for _, c := range set {
		if clampsExactEqual(c, want) {
			return true
		}
	}
	return false
}

func clampsExactEqual(a, b authority.Clamp) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.RuleID != "" && b.RuleID != "" && a.RuleID != b.RuleID {
		return false
	}
	switch a.Kind {
	case authority.ClampMaxOutputTokens:
		return a.Value == b.Value
	default:
		return false
	}
}

func cloneAuthorityClamps(in []authority.Clamp) []authority.Clamp {
	if len(in) == 0 {
		return nil
	}
	out := make([]authority.Clamp, len(in))
	copy(out, in)
	return out
}

func maxOutputEqual(a, b aipapi.Call) bool {
	switch {
	case a.Options.MaxOutputTokens == nil && b.Options.MaxOutputTokens == nil:
		return true
	case a.Options.MaxOutputTokens == nil || b.Options.MaxOutputTokens == nil:
		return false
	default:
		return *a.Options.MaxOutputTokens == *b.Options.MaxOutputTokens
	}
}

// enforcePostAdmitClamps rejects exposure-changing admit clamps that were not
// exactly previewed. Matching clamps do not mutate the frozen call again.
func (e *Executor) enforcePostAdmitClamps(
	ctx context.Context,
	call *aipapi.Call,
	frozen aipapi.Call,
	previewed []authority.Clamp,
	previewRan bool,
	state attemptAuthorityState,
	c routing.AttemptCandidate,
	inputTokens int64,
) error {
	var admitClamps []authority.Clamp
	admitClamps = append(admitClamps, state.admitClamps...)
	if len(admitClamps) == 0 {
		return nil
	}
	if previewRan {
		for _, clamp := range admitClamps {
			if !clampExactInSet(previewed, clamp) {
				return fmt.Errorf("executor: unpreviewed exposure clamp rejected (compensate and deny before Open)")
			}
		}
		return nil
	}
	for _, clamp := range admitClamps {
		probe := aipapi.CloneCall(frozen)
		switch clamp.Kind {
		case authority.ClampMaxOutputTokens:
			maxOut := int(clamp.Value)
			probe.Options.MaxOutputTokens = &maxOut
		default:
			return fmt.Errorf("executor: unknown admit clamp kind %q", clamp.Kind)
		}
		if !maxOutputEqual(frozen, probe) {
			return fmt.Errorf("executor: legacy exposure-changing admit clamp rejected (compensate and deny before Open)")
		}
	}
	_ = call
	return nil
}
