package extensions

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// CompletionGateChainResult is the outcome of running completion gates over a buffered stream.
type CompletionGateChainResult struct {
	Events   []aipapi.Event
	Replaced bool // true when OutcomeReplace was applied (not ignored after output commitment)
	// EffectiveReplacement is the FINAL provenance of Events, not the historical
	// fact that some gate once replaced the stream.
	//
	// Replaced stays true once any effective replacement happened, because
	// existing consumers read it as historical evidence that replacement was
	// requested. It cannot describe the effective output, though: a later
	// ReplayOriginal restores the augmented original while Replaced stays true.
	// EffectiveReplacement therefore tracks the last effective action: true after
	// an applied Replace, false after a ReplayOriginal, and unchanged after
	// PassOriginal. It is private provenance for the internal response owner; no
	// public tag, event field, or wire projection depends on it.
	EffectiveReplacement bool
}

// ApplyCompletionGateChain runs sorted gates over the buffered completion (design §6, §17).
// When outputCommitted is true, replacement outcomes are ignored (original buffer preserved).
// Handler errors honor per-gate FailureMode; the stage default is fail-open (see DefaultFailurePolicyForStage).
// log may be nil; when set, completion-gate panics are logged via [logFailOpenExtensionPanic] before
// they are returned to the runtime stream boundary (panics are not swallowed by fail-open policy).
func ApplyCompletionGateChain(ctx context.Context, gates []completion.Gate, meta completion.Meta, original []aipapi.Event, outputCommitted bool, svc completion.Services, log *slog.Logger) (CompletionGateChainResult, error) {
	if len(gates) == 0 {
		return CompletionGateChainResult{Events: slices.Clone(original)}, nil
	}
	sorted := completion.MaterializeSorted(gates)
	originalCopy := slices.Clone(original)
	current := slices.Clone(original)
	replaced := false
	effectiveReplacement := false
	ev := DecisionEvidenceFromContext(ctx)
	// completionGateFailureCfg carries outputCommitted (constant for the whole chain)
	// into the shared timeout helper's evidence emitter. Failure handling stays inline
	// to preserve the completion-gate panic special case (panics return through the
	// stream boundary as fail-closed policy provider failures).
	completionGateFailureCfg := stageFailureConfig{
		Stage:        feature.StageIDCompletionGating,
		MetricsStage: "",
		TimeoutMsg:   "",
		FailureMsg:   "",
		PanicStage:   "completion_gate",
		ProviderAttr: "gate",
		EmitTimeout: func(ctx context.Context, ev *DecisionEvidence, providerID string, deadline time.Time, mode sdkhooks.FailureMode) {
			emitCompletionGateTimeoutEvidence(ctx, ev, providerID, mode, outputCommitted, deadline)
		},
		EmitFailure: nil,
	}
	for _, g := range sorted {
		if g == nil {
			continue
		}
		mode := g.FailureMode()
		zeroOutcome := completion.Outcome{}
		buf := completion.NewBuffered(current)
		res := runBoundedProvider(ctx, ev, feature.StageIDCompletionGating, g.ID(), func(c context.Context) (completion.Outcome, error) {
			return safety.CallValue(safety.BoundaryExtension, "completion_gate_handle", func() (completion.Outcome, error) {
				return g.Handle(c, meta, buf, svc)
			})
		})
		if res.ParentCanceled {
			return CompletionGateChainResult{}, res.Err
		}
		if res.TimedOut {
			cont, terr := handleProviderTimeout(ctx, log, nil, ev, completionGateFailureCfg, res.IterCtx, g.ID(), res.Deadline, mode)
			if cont {
				continue
			}
			return CompletionGateChainResult{}, terr
		}
		iterCtx := res.IterCtx
		deadline := res.Deadline
		out, err := res.Value, res.Err
		if err != nil {
			if _, ok := errors.AsType[*safety.PanicError](err); ok {
				emitCompletionGateEvidence(iterCtx, ev, g.ID(), zeroOutcome, err, sdkhooks.FailClosed, outputCommitted, false, deadline)
				logFailOpenExtensionPanic(ctx, log, "completion_gate", g.ID(), err)
				return CompletionGateChainResult{}, PolicyErrorFromProviderFailure(feature.StageIDCompletionGating, g.ID(), policydecision.FailureBehaviorFailClosed, err)
			}
			emitCompletionGateEvidence(iterCtx, ev, g.ID(), zeroOutcome, err, mode, outputCommitted, false, deadline)
			if mode == sdkhooks.FailClosed {
				if IsContextCancellation(ctx, err) {
					return CompletionGateChainResult{}, err
				}
				return CompletionGateChainResult{}, PolicyErrorFromProviderFailure(feature.StageIDCompletionGating, g.ID(), policydecision.FailureBehaviorFailClosed, err)
			}
			continue
		}
		if vErr := out.Validate(); vErr != nil {
			emitCompletionGateEvidence(iterCtx, ev, g.ID(), out, vErr, mode, outputCommitted, true, deadline)
			if mode == sdkhooks.FailClosed {
				return CompletionGateChainResult{}, PolicyErrorFromMalformed(feature.StageIDCompletionGating, g.ID(), vErr)
			}
			continue
		}
		emitCompletionGateEvidence(iterCtx, ev, g.ID(), out, nil, mode, outputCommitted, false, deadline)
		switch out.Kind {
		case completion.OutcomePassOriginal:
			// unchanged
		case completion.OutcomeReplayOriginal:
			// Replay restores the augmented original, so the effective provenance is
			// the original again even though Replaced stays historically true.
			current = slices.Clone(originalCopy)
			effectiveReplacement = false
		case completion.OutcomeReplace:
			if outputCommitted {
				continue
			}
			current = slices.Clone(out.Events)
			replaced = true
			effectiveReplacement = true
		case completion.OutcomeReject:
			return CompletionGateChainResult{}, aipapi.NewPolicyDeniedError(feature.StageIDCompletionGating, g.ID(), ReasonCompletionReject, CategoryDenied, "completion rejected by policy", out.Err)
		default:
			if mode == sdkhooks.FailClosed {
				return CompletionGateChainResult{}, PolicyErrorFromMalformed(feature.StageIDCompletionGating, g.ID(), errors.New("extensions: unknown completion outcome"))
			}
		}
	}
	return CompletionGateChainResult{Events: current, Replaced: replaced, EffectiveReplacement: effectiveReplacement}, nil
}

// emitCompletionGateEvidence projects one completion-gate outcome into shared
// evidence (requirements 3.4, 4.3, 5.2, 8.2, 9.1, 9.5). The completion gate runs
// after backend attempt is committed, so BackendAttempted is true. The gate
// stage runs at stream completion; outputCommitted is the runtime's authoritative
// per-event output-committed flag and is preserved on the emitted record so a
// pre-output decision is not misrecorded as post-output. The projector still
// represents ignored post-output replacement as skip/none, preserving the
// no-failover-after-output invariant. No-op when no seam is attached.
//
// err != nil records a provider failure (handler error or panic) when malformed
// is false, and a malformed-outcome validation failure when malformed is true
// (requirements 6.6). Validation failures are malformed regardless of outcome
// kind/fields; provider handler errors/panics are failures. Otherwise the gate's
// outcome is projected through ProjectCompletionOutcome.
func emitCompletionGateEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, out completion.Outcome, err error, mode sdkhooks.FailureMode, outputCommitted bool, malformed bool, deadline time.Time) {
	if ev == nil || ev.Emitter == nil {
		return
	}
	if err != nil {
		reason, category := ReasonCompletionFailure, CategoryFailure
		if malformed {
			reason, category = ReasonCompletionMalformed, CategoryMalformed
		}
		emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
			Stage:            feature.StageIDCompletionGating,
			ProviderID:       providerID,
			BackendAttempted: true,
			OutputCommitted:  outputCommitted,
			ReasonCode:       reason,
			ClientCategory:   category,
			FailureMode:      mode,
			Deadline:         deadline,
		})
		return
	}
	dctx := decisionContextForDeadline(ctx, ev, feature.StageIDCompletionGating, providerID, outputCommitted, deadline)
	rec := ProjectCompletionOutcome(dctx, providerID, out)
	rec.OutputCommitted = outputCommitted
	emitDecisionRecord(ctx, ev, rec)
}

// emitCompletionGateTimeoutEvidence projects a completion-gate evaluation timeout into
// shared evidence with the gate's failure behavior (requirements 6.1, 6.3). The
// completion gate runs after backend attempt is committed, so BackendAttempted is
// true. outputCommitted is preserved on the record. deadline is the exact evaluation
// deadline used to bound the provider call; it is projected onto the record's
// EvaluationDeadline. No-op when no seam is attached.
func emitCompletionGateTimeoutEvidence(ctx context.Context, ev *DecisionEvidence, providerID string, mode sdkhooks.FailureMode, outputCommitted bool, deadline time.Time) {
	emitPolicyErrorEvidence(ctx, ev, policyErrorEvidenceSpec{
		Stage:            feature.StageIDCompletionGating,
		ProviderID:       providerID,
		BackendAttempted: true,
		OutputCommitted:  outputCommitted,
		ReasonCode:       PolicyReasonTimeout,
		ClientCategory:   CategoryFailure,
		FailureMode:      mode,
		Deadline:         deadline,
	})
}

// CompletionGateBufferExceeded reports whether buffering should fail open to live passthrough (R8).
func CompletionGateBufferExceeded(limits completion.BufferLimits, n int) bool {
	return limits.OverCapacity(n)
}

// StreamFinished reports whether the canonical stream reached a terminal completion marker.
func StreamFinished(events []aipapi.Event) bool {
	if len(events) == 0 {
		return false
	}
	return events[len(events)-1].Kind == aipapi.EventResponseFinished
}
