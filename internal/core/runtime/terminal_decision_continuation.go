package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

const (
	continuationOverlayID     = "terminal-decision-continuation"
	continuationPendingReason = "terminal_decision_continuation_pending"
)

// continuationFailurePolicy selects how a pre-publication transaction failure
// settles the request terminal. The zero value is the terminal-decision
// behaviour: the failure claims the request terminal and finalizes the
// original attempt.
type continuationFailurePolicy uint8

const (
	continuationFailureClaimsTerminal continuationFailurePolicy = iota
	// continuationFailureFailOpen is the correction policy: the failure only
	// deactivates the continuation's overlay and reports
	// [errToolCorrectionNotPublished] without claiming the request terminal,
	// so the caller can still deliver the original attempt's output on the
	// normal routes. Cancellation and deadline causes keep the claim path.
	continuationFailureFailOpen
)

// continuationLineagePolicy selects the continuation facts a published B2
// inherits. The zero value is the terminal-decision behaviour: the attempt
// advances past the intent's own references.
type continuationLineagePolicy uint8

const (
	continuationLineageAdvances continuationLineagePolicy = iota
	// continuationLineagePreserved keeps B1's continuation lineage unchanged
	// (or stores the lineage B1 would have derived), so a terminal-decision
	// provider evaluates B2's finish exactly as it would have evaluated B1's.
	continuationLineagePreserved
)

// continuationPolicy carries the two behaviours the correction continuation
// needs from the one publication transaction. The zero value reproduces the
// terminal-decision caller exactly.
type continuationPolicy struct {
	overlayID string
	failure   continuationFailurePolicy
	lineage   continuationLineagePolicy
}

// runContinuationTransaction is the single publication boundary for a
// semantic continuation. All fallible B2 work happens while B1 remains the
// current attempt. Once the slot accepts B2, B1 is only settled as a detached
// prior attempt; no post-publication error can roll the slot back.
func runContinuationTransaction(ctx context.Context, t *turnTerminal, s *retryRecvStream, intent terminaldecision.ContinuationIntent) (bool, error) {
	return continuationTransactionWithOverlay(ctx, t, s, intent, continuationPolicy{overlayID: continuationOverlayID})
}

func continuationTransactionWithOverlay(ctx context.Context, t *turnTerminal, s *retryRecvStream, intent terminaldecision.ContinuationIntent, policy continuationPolicy) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	failure := func(stage string, cause error) error {
		return continuationPrePublicationFailureWithOverlay(ctx, t, s, stage, cause, policy)
	}
	if t == nil || s == nil || s.responsePipeline == nil {
		return false, errors.New("continuation: invalid transaction")
	}
	if err := intent.Validate(); err != nil {
		return false, failure("invalid intent", err)
	}
	if !t.supportsContinuation {
		return false, failure("unsupported continuation", nil)
	}
	b1 := s.attempt.snapshot()
	if b1 == nil {
		return false, failure("missing current attempt", nil)
	}
	if err := continuationContextError(ctx, t); err != nil {
		return false, failure("canceled before preparation", err)
	}

	aLegID := s.facts.aLegID
	if t.steeringWriterFactory == nil {
		return false, failure("steering unavailable", nil)
	}
	if s.recovery == nil || s.recovery.opener == nil {
		return false, failure("continuation admission unavailable", nil)
	}

	resolver := func(rctx context.Context) (aipapi.Call, conversationprojection.Snapshot, error) {
		snap := s.facts.conversationSnapshot
		if snap.StateRevision == 0 && t.conversationReader != nil {
			if current, err := t.conversationReader.Snapshot(rctx, aLegID); err == nil {
				snap = current
			}
		}
		ingress := s.facts.ingressCall
		if len(ingress.Items) == 0 && len(ingress.Messages) == 0 {
			ingress = s.facts.baseline
		}
		return ingress, snap, nil
	}
	writer, err := t.steeringWriterFactory(ctx, aLegID, resolver)
	if err != nil {
		return false, failure("steering unavailable", err)
	}
	_, err = writer.Put(ctx, steering.PutRequest{
		OverlayID: steering.OverlayID(policy.overlayID),
		Message: steering.Message{
			Role: aipapi.RoleDeveloper,
			Text: intent.Instruction,
		},
		Placement:           steering.AfterIngressTail,
		AnchorMissingPolicy: steering.FailClosed,
		Reason:              steering.ReasonCode(intent.ReasonCode),
	})
	if err != nil {
		return false, failure("steering persistence failed", err)
	}

	reader := t.conversationReader
	if reader == nil {
		return false, failure("conversation reader unavailable", nil)
	}
	snapN1, err := reader.Snapshot(ctx, aLegID)
	if err != nil {
		return false, failure("conversation snapshot failed", err)
	}
	ingress := s.facts.ingressCall
	if len(ingress.Items) == 0 && len(ingress.Messages) == 0 {
		ingress = s.facts.baseline
	}
	projectedBaseline, projection, err := conversationprojection.Project(ingress, snapN1)
	if err != nil {
		if observer := t.conversationObserver; observer != nil {
			safeObserver{obs: observer}.OnProjectionFailure(conversationprojection.StageEarly)
		}
		return false, failure("conversation materialization failed", err)
	}
	filteredBaseline, err := conversationprojection.FilterNeverBackend(ingress, snapN1)
	if err != nil {
		return false, failure("conversation materialization failed", err)
	}
	newFacts := s.facts.clone()
	newFacts.baseline = projectedBaseline
	newFacts.conversationSnapshot = snapN1
	if projection != nil {
		newFacts.conversationProvenance = projection.Provenance
	}
	newFacts.conversationFilteredBaseline = filteredBaseline
	newFacts.ingressCall = s.facts.ingressCall
	newFacts.continuationIntent = continuationIntentFactsFor(policy.lineage, s.facts, b1, intent)
	if err := continuationContextError(ctx, t); err != nil {
		return false, failure("canceled before admission", err)
	}

	out, openErr := s.recovery.openContinuation(ctx, replacementOpenRequest{
		facts:       newFacts.terminalFacts(),
		pinnedFacts: newFacts,
		recovery:    s.recovery.openSnapshot(),
		prior:       priorAttemptOutcome{attempt: b1, retired: true},
		isRetryPath: false,
		interleaved: s.recovery.interleaved,
	})
	if openErr != nil || !out.opened || out.ready == nil {
		if out.ready != nil {
			out.ready.Dispose(ctx, continuationCause(openErr, context.Canceled))
		}
		return false, failure("continuation admission failed", openErr)
	}
	ready := out.ready
	if err := t.registerReplacement(ctx, out, ready); err != nil {
		ready.Dispose(ctx, err)
		return false, failure("continuation registration failed", err)
	}
	if ready.state != readyStatePrepared {
		if err := ready.Prepare(ctx, newFacts, s.responsePipeline, t.committed()); err != nil {
			ready.Dispose(ctx, err)
			return false, failure("continuation preparation failed", err)
		}
	}
	if err := continuationContextError(ctx, t); err != nil {
		ready.Dispose(ctx, err)
		return false, failure("canceled before publication", err)
	}
	_, published := s.attempt.swapIfOpen(ready)
	if !published {
		ready.Dispose(ctx, fmt.Errorf("continuation %w", errPublicationClosed))
		return false, failure("continuation publication closed", nil)
	}
	// B2 is a continuation of the same client-facing logical response. The
	// backend may emit a fresh response/message prefix, but those lifecycle
	// markers are not a second response and must not reach the frontend.
	s.responsePipeline.markContinuationLifecyclePending()

	// The slot is now irrevocably B2. Publish the new view before touching B1.
	s.facts.conversationSnapshot = snapN1
	if projection != nil {
		s.facts.conversationProvenance = projection.Provenance
	}
	s.facts.conversationFilteredBaseline = filteredBaseline
	s.facts.continuationIntent = newFacts.continuationIntent
	t.markCommitted(b1)
	settled := b1.TerminalizeAttempt(ctx, IntentSwallowedFailure, attemptEvidence{
		Command:       sdkterminal.CommandSwallowedAttempt,
		LegOutcome:    usageevidence.LegOutcomeSwallowed,
		ObsOutcome:    response.OutcomeReplaced,
		RecordOutcome: aipapi.AttemptSwallowedFailure,
		RecordReason:  continuationPendingReason,
		TraceID:       b1.traceID,
		ALegID:        b1.bleg.ALegID,
		StartedAt:     b1.accountingStartedAt(),
	})
	if !settled.Result.Won || settled.Result.Err != nil || !continuationSettlementSucceeded(b1) {
		return true, errors.New("continuation: prior attempt settlement unavailable")
	}
	return true, nil
}

// continuationIntentFactsFor applies the policy's lineage rule to the published
// B2. The advance rule is today's terminal-decision behaviour: the intent's own
// references plus the next attempt number. The preserve rule makes the
// correction continuation inherit B1's lineage, so a terminal-decision provider
// evaluates B2's finish exactly as it would have evaluated B1's.
func continuationIntentFactsFor(policy continuationLineagePolicy, facts recvTurnFacts, b1 *attemptSession, intent terminaldecision.ContinuationIntent) continuationIntentFacts {
	if policy != continuationLineagePreserved {
		return nextContinuationIntentFacts(facts.continuationIntent, b1, intent)
	}
	if facts.continuationIntent.set {
		return facts.continuationIntent
	}
	return derivedContinuationIntentFacts(facts.baseline, facts.traceID, b1)
}

func nextContinuationIntentFacts(prior continuationIntentFacts, b1 *attemptSession, intent terminaldecision.ContinuationIntent) continuationIntentFacts {
	attempt := prior.attempt
	if !prior.set && b1 != nil && b1.bleg.Seq > 0 {
		if b1.bleg.Seq >= 1<<8 {
			attempt = ^uint8(0)
		} else {
			attempt = uint8(b1.bleg.Seq)
		}
	}
	if attempt < ^uint8(0) {
		attempt++
	}
	return continuationIntentFacts{
		trajectoryRef: intent.TrajectoryRef,
		controlRef:    intent.ControlRef,
		attempt:       attempt,
		set:           true,
	}
}

func continuationSettlementSucceeded(attempt *attemptSession) bool {
	if attempt == nil || attempt.authority.control == nil {
		return true
	}
	attempt.authority.control.mu.Lock()
	defer attempt.authority.control.mu.Unlock()
	return attempt.authority.control.terminal == authorityTerminalSettled
}

// Keep the transaction's terminal diagnostic deliberately classified. Upstream
// and persistence details are useful for logs at their owning boundary, but
// must not become a continuation control-plane payload.
func continuationPrePublicationFailureWithOverlay(ctx context.Context, t *turnTerminal, s *retryRecvStream, stage string, cause error, policy continuationPolicy) error {
	if policy.overlayID == toolNameCorrectionOverlayID {
		if err := deactivateSteeringOverlay(ctx, t, s.facts.aLegID, policy.overlayID); err != nil {
			diag.LogError(diag.EnsureCallDiag(ctx, s.facts.traceID, s.facts.aLegID), t.log, "tool_name_correction_overlay_deactivate_failed", diag.AttrOpts{}, err)
		}
	} else {
		_ = deactivateSteeringOverlay(ctx, t, s.facts.aLegID, policy.overlayID)
	}
	if policy.failure == continuationFailureFailOpen && !continuationCancellationCause(ctx, cause) {
		// A correction whose follow-up cannot be published fails open: the
		// request terminal is left to the caller, which still owns the
		// original attempt's withheld output and its finish.
		return errToolCorrectionNotPublished
	}
	cmd := sdkterminal.CommandNormalFinish
	if errors.Is(cause, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		cmd = sdkterminal.CommandCancel
	}
	request := s.facts.terminalFacts()
	requestResult := t.claimRequestTerminal(ctx, cmd, coreterm.NewAccumulatorSnapshot(nil, t.committed()), func(cctx context.Context, _ coreterm.Outcome) error {
		t.settleOrReleaseRequestAuthority(cctx, s.responsePipeline, request)
		handoffErr := t.handoffBillingTurn(cctx, request, cmd)
		t.finishResponse(s.responsePipeline, s.attempt.snapshot())
		return handoffErr
	})
	if requestResult.Err != nil {
		return requestResult.Err
	}
	if errors.Is(cause, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("continuation: " + stage)
}

// continuationCancellationCause reports whether a transaction failure is the
// caller's own cancellation or deadline, the causes that keep the existing
// claim path under every failure policy.
func continuationCancellationCause(ctx context.Context, cause error) bool {
	return errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) ||
		errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// deactivateContinuationOverlay removes the platform continuation overlay.
func deactivateContinuationOverlay(ctx context.Context, t *turnTerminal, aLegID string) error {
	return deactivateSteeringOverlay(ctx, t, aLegID, continuationOverlayID)
}

// deactivateSteeringOverlay removes one steering overlay by ID. Deactivation
// runs on the overlay's own bounded cleanup context, so a canceled request
// still cleans up the message it planted.
func deactivateSteeringOverlay(ctx context.Context, t *turnTerminal, aLegID, overlayID string) error {
	if t == nil || t.steeringWriterFactory == nil || aLegID == "" || overlayID == "" {
		return nil
	}
	deactCtx, cancel := cleanupContext(ctx, defaultAuthorityCleanupTimeout)
	defer cancel()
	writer, err := t.steeringWriterFactory(deactCtx, aLegID, nil)
	if err != nil {
		return err
	}
	_, err = writer.Deactivate(deactCtx, steering.OverlayID(overlayID))
	return err
}

func continuationContextError(ctx context.Context, t *turnTerminal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.finished() || (t.hasALeg() && t.aLegErr() != nil) {
		return errors.New("continuation terminal closed")
	}
	return nil
}

func continuationCause(err, fallback error) error {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
	}
	return fallback
}
