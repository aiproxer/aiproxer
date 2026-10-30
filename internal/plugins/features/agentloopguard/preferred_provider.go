package agentloopguard

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolpolicy"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolstate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// preferredProvider is the explicit-completion-strategy terminal policy
// receiver.
//
// It is deliberately a separate implementation from the legacy semantic
// verifier provider: it holds only the two protocol numeric limits, constructs
// no verifier, keeps no legacy progress state, and has no auxiliary dependency,
// so no verifier or auxiliary request is reachable from it. All canonical
// cause, safety, expectation, state, and budget classification belongs to the
// pure protocol policy, and all protocol state belongs to protocolstate. This
// receiver only adapts the canonical SDK input and context to those packages.
//
// It has no enable/disable authority: the enabled contribution gate is owned by
// feature composition.
type preferredProvider struct {
	maxProtocolReprompts int
	noProgressLimit      int
}

var _ terminaldecision.Provider = preferredProvider{}

// ID returns the existing ALG provider identity. The identity is shared with
// the legacy strategy; the selected strategy, not a second id, distinguishes
// the two implementations.
func (preferredProvider) ID() string { return providerID }

// Decide evaluates one canonical terminal candidate with the preferred
// missing-completion-signal policy.
//
// A nil context is treated as context.Background, and an already canceled or
// expired context stops conservatively with a bounded reason and without any
// policy, state, or verifier work. Malformed SDK input is rejected before any
// state load or policy evaluation. An unusable protocol state stops
// conservatively without resetting or rewriting the carried reference. The
// platform owns committing protocolstate; this receiver only returns the
// bounded decision.
func (p preferredProvider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return allowStop(reasonDeadline), nil
		}
		return allowStop(reasonCanceled), nil
	}
	if err := in.Validate(); err != nil {
		return allowStop(reasonInvalidInput), nil
	}
	prior, err := protocolpolicy.LoadState(in)
	if err != nil {
		// The reference is never reset or rewritten here; an unusable reference
		// simply cannot establish resumable protocol continuity.
		return allowStop(protocolpolicy.ReasonInvalidState), nil
	}
	result, err := protocolpolicy.Evaluate(in, prior, p.limits(), platformSnapshot(in))
	if err != nil {
		// The policy always pairs an error with a conservative stop decision.
		return result.Decision, err
	}
	return result.Decision, nil
}

// limits returns the validated protocol bounds owned by this receiver.
func (p preferredProvider) limits() protocolstate.Limits {
	return protocolstate.Limits{
		MaxReprompts:    p.maxProtocolReprompts,
		NoProgressLimit: p.noProgressLimit,
	}
}

// platformSnapshot maps the canonical policy and continuation views onto the
// platform snapshot. A zero policy cap is an unresolved platform default that
// this package must not reinterpret; core owns that default.
func platformSnapshot(in terminaldecision.Input) protocolstate.Platform {
	return protocolstate.Platform{
		Cap:     int(in.Policy.MaxContinuationAttempts),
		Attempt: int(in.Continuation.Attempt),
	}
}
