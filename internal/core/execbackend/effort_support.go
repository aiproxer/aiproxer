package execbackend

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// EffortSupportState is the provider-owned answer for one reasoning-effort
// value on one candidate's model.
//
// It is a *query* result, not a vocabulary: the provider states what it
// carries, and the root never carries a global list of effort values. The
// three states never collapse — "the provider published no fact" is not
// "the provider supports nothing" (design §2; Requirement 1.4).
type EffortSupportState uint8

const (
	// EffortSupportUnknown means the provider published no graded fact for
	// this value and model. It is the zero value and the safe default: an
	// unknown answer is never a refusal.
	EffortSupportUnknown EffortSupportState = iota
	// EffortSupportSupported means the provider states it carries this value
	// for this model as a graded level.
	EffortSupportSupported
	// EffortSupportUnsupported means the provider states it does not carry
	// this value for this model.
	EffortSupportUnsupported
)

// EffectiveEffortSupport returns the provider's answer for one effort value
// on one candidate's model.
//
// It has the same "nil callback means unknown" shape as [EffectiveCaps]: a
// backend that publishes no per-value fact answers unknown, never a refusal.
// A provider that carries the value without grading it (a binary switch, for
// example) also answers unknown: carrying a value ungraded is not a
// graded-support claim.
func EffectiveEffortSupport(
	ctx context.Context,
	be Backend,
	call aipapi.Call,
	cand routing.AttemptCandidate,
	effort string,
) EffortSupportState {
	if be.ResolveEffortSupport == nil {
		return EffortSupportUnknown
	}
	return be.ResolveEffortSupport(ctx, call, cand, effort)
}
