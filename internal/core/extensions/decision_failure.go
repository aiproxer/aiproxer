package extensions

import (
	"context"
	"time"

	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// failureBehaviorFromMode maps a hook failure mode to the shared
// policydecision.FailureBehavior so error records distinguish fail-open skips
// from fail-closed failures (requirement 6.1, 6.2). FailureModeUnspecified maps
// to the zero FailureBehavior, matching helpers that previously did not set it.
func failureBehaviorFromMode(mode sdkhooks.FailureMode) policydecision.FailureBehavior {
	switch mode {
	case sdkhooks.FailOpen:
		return policydecision.FailureBehaviorFailOpen
	case sdkhooks.FailClosed:
		return policydecision.FailureBehaviorFailClosed
	default:
		return policydecision.FailureBehaviorUnspecified
	}
}

// policyErrorEvidenceSpec describes a projected provider error/timeout evidence
// record emitted by a stage runner. It is the collapsed shape shared by every
// per-stage emit*TimeoutEvidence / emit*FailureEvidence / emit*MalformedEvidence
// helper (requirements 6.1, 6.2, 6.3, 6.6).
//
//   - Stage and ProviderID identify the decision provider.
//   - BackendAttempted is set by the caller according to the stage family's
//     lifecycle position (false for pre-backend stages, true for stream/attempt
//     stages).
//   - OutputCommitted is projected onto the record. Pre-backend stages pass
//     false, which equals the value already lifted from the decision context;
//     stream stages pass the runtime's authoritative flag.
//   - ReasonCode and ClientCategory are bounded safe tokens.
//   - FailureMode drives FailureBehavior; Unspecified yields the zero value,
//     matching helpers that previously did not set it (e.g. malformed emits).
//   - Deadline is the exact evaluation deadline used to bound the provider call
//     (requirement 6.3); zero preserves legacy behavior (direct path, malformed
//     emits). It is projected onto policydecision.Context.EvaluationDeadline.
type policyErrorEvidenceSpec struct {
	Stage            string
	ProviderID       string
	BackendAttempted bool
	OutputCommitted  bool
	ReasonCode       string
	ClientCategory   string
	FailureMode      sdkhooks.FailureMode
	Deadline         time.Time
}

// emitPolicyErrorEvidence projects a provider error or timeout outcome as a
// single shared error/none record (requirements 6.1, 6.2, 6.3, 6.6). It is the
// common body of the per-stage emit*TimeoutEvidence / emit*FailureEvidence /
// emit*MalformedEvidence helpers; callers supply the stage-specific stage,
// backend-attempted position, reason code, category, and failure mode. The supplied
// deadline is projected onto the record's EvaluationDeadline so timeout and
// bounded-path failure evidence carry the exact deadline that bounded the provider.
// No-op when no seam is attached, preserving the no-observer/non-interference
// default (requirements 9.1, 10.5).
func emitPolicyErrorEvidence(ctx context.Context, ev *DecisionEvidence, spec policyErrorEvidenceSpec) {
	if ev == nil || ev.Emitter == nil {
		return
	}
	dctx := decisionContextForDeadline(ctx, ev, spec.Stage, spec.ProviderID, spec.OutputCommitted, spec.Deadline)
	rec := recordFromContext(dctx, spec.ProviderID, spec.BackendAttempted)
	rec.Outcome = policydecision.OutcomeError
	rec.Effect = policydecision.EffectNone
	rec.ReasonCode = spec.ReasonCode
	rec.ClientCategory = spec.ClientCategory
	rec.FailureBehavior = failureBehaviorFromMode(spec.FailureMode)
	rec.OutputCommitted = spec.OutputCommitted
	emitDecisionRecord(ctx, ev, rec)
}
