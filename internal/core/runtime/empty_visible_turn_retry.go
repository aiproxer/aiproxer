package runtime

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Bounded prefix hold for the empty-visible-turn corrective retry
// (spec empty-visible-turn-retry, task 3; requirements 3.1, 3.3, 3.5, 3.6;
// design "Prefix hold").
//
// While the corrective retry is enabled and the turn can still be corrected,
// the receive loop holds the attempt's produced canonical client-visible
// frames instead of releasing them. The hold is attempt-local, bounded by the
// shared retention budget (maxWithheldEvents, maxWithheldBytes), guarded by
// the same withholdMu as the tool-name withhold queue, and discarded by the
// same discardWithheld/discardSidebandState lifecycle chokepoint. It keeps its
// own ordered release queue instead of reusing the tool-call assembler drain:
// held frames must survive a nil assembler, and each frame is dispatched
// exactly once after its single pop.

// correctionHoldState is the semantic phase of one attempt's prefix hold. The
// zero value is unarmed: this attempt never started a hold, so no frame was
// ever retained. A hold moves armed -> released or armed -> overflowed while
// its frames wait in the release queue, and any phase -> discarded on the
// shared attempt cleanup.
type correctionHoldState string

const (
	correctionHoldUnarmed    correctionHoldState = ""
	correctionHoldArmed      correctionHoldState = "armed"
	correctionHoldReleased   correctionHoldState = "released"
	correctionHoldOverflowed correctionHoldState = "overflowed"
	correctionHoldDiscarded  correctionHoldState = "discarded"
)

// correctionHold is one attempt's bounded prefix hold. It is guarded by the
// attempt's withholdMu and discarded with the withhold queue. While armed,
// events are the prepared frames retained instead of released and bytes is
// their retained payload total. After a release or overflow, events is the
// ordered release queue popCorrectionHeld drains one frame per call.
type correctionHold struct {
	state  correctionHoldState
	events []recvEventPreparation
	bytes  int
}

// armCorrectionHold starts the hold for one attempt. The receive loop arms it
// once per attempt, when the gate is on, the corrective allowance is intact,
// and no client-visible frame was released. Arming a hold that already moved
// on is a no-op, so a late call can never revive a released, overflowed, or
// discarded queue.
func (a *attemptSession) armCorrectionHold() {
	if a == nil {
		return
	}
	a.withholdMu.Lock()
	if a.correctionHold.state == correctionHoldUnarmed {
		a.correctionHold.state = correctionHoldArmed
	}
	a.withholdMu.Unlock()
}

// holdCorrectionFrame retains one prepared frame for later release. It reports
// whether the frame was retained. False means the frame is not in the hold:
// either the hold is not armed, or this frame exceeded a bound and the hold
// has already been released with every retained frame followed by this one
// pending in the release queue (state overflowed). The caller drains the queue
// through the single client-release path and never dispatches a queued frame
// twice; routeCorrectionHeldFrame is the atomic routing the receive loop uses
// so that false is never mistaken for "the frame is lost".
func (a *attemptSession) holdCorrectionFrame(prepared recvEventPreparation) bool {
	if a == nil {
		return false
	}
	a.withholdMu.Lock()
	defer a.withholdMu.Unlock()
	return a.holdCorrectionFrameLocked(prepared)
}

// holdCorrectionFrameLocked is holdCorrectionFrame under withholdMu.
func (a *attemptSession) holdCorrectionFrameLocked(prepared recvEventPreparation) bool {
	if a.correctionHold.state != correctionHoldArmed {
		return false
	}
	size := prepared.event.PayloadBytes()
	if len(a.correctionHold.events) >= maxWithheldEvents || a.correctionHold.bytes > maxWithheldBytes-size {
		// Bound exceeded: stop correcting. Every retained frame is released
		// unchanged, followed by this frame, in order; nothing is dropped and
		// nothing is copied for the queue the caller is about to drain.
		a.correctionHold.state = correctionHoldOverflowed
		a.correctionHold.events = append(a.correctionHold.events, prepared)
		a.correctionHold.bytes = 0
		return false
	}
	// Clone after the bound check passes, so an overflowing frame is not
	// copied: the hold retains a deep copy the producer cannot mutate.
	prepared.event = prepared.event.Clone()
	a.correctionHold.events = append(a.correctionHold.events, prepared)
	a.correctionHold.bytes += size
	return true
}

// releaseCorrectionHold stops retaining and hands every retained frame,
// followed by the optional tail frames, to the ordered release queue drained
// by popCorrectionHeld. The receive loop releases before the frame that
// dissolves the hold (the first visible output or an uncorrected finish), and
// with no tail on the end-of-stream path. A release of a hold already draining
// appends the tail in order; an unarmed or discarded hold ignores the call, so
// a discarded hold can never release a frame.
func (a *attemptSession) releaseCorrectionHold(tail ...recvEventPreparation) {
	if a == nil {
		return
	}
	a.withholdMu.Lock()
	defer a.withholdMu.Unlock()
	a.releaseCorrectionHoldLocked(tail...)
}

// releaseCorrectionHoldLocked is releaseCorrectionHold under withholdMu.
func (a *attemptSession) releaseCorrectionHoldLocked(tail ...recvEventPreparation) {
	switch a.correctionHold.state {
	case correctionHoldArmed, correctionHoldReleased, correctionHoldOverflowed:
	default:
		return
	}
	if a.correctionHold.state == correctionHoldArmed {
		a.correctionHold.state = correctionHoldReleased
	}
	a.correctionHold.events = append(a.correctionHold.events, tail...)
	a.correctionHold.bytes = 0
}

// popCorrectionHeld returns the next released frame, exactly one per call, in
// the order the hold retained it. It reports false while the hold is still
// armed (frames are retained, not released) and once the release queue is
// empty, so the receive loop can never dispatch a held frame before its
// release.
func (a *attemptSession) popCorrectionHeld() (recvEventPreparation, bool) {
	if a == nil {
		return recvEventPreparation{}, false
	}
	a.withholdMu.Lock()
	defer a.withholdMu.Unlock()
	switch a.correctionHold.state {
	case correctionHoldReleased, correctionHoldOverflowed:
	default:
		return recvEventPreparation{}, false
	}
	if len(a.correctionHold.events) == 0 {
		return recvEventPreparation{}, false
	}
	prepared := a.correctionHold.events[0]
	a.correctionHold.events[0] = recvEventPreparation{}
	a.correctionHold.events = a.correctionHold.events[1:]
	if len(a.correctionHold.events) == 0 {
		a.correctionHold.events = nil
	}
	return prepared, true
}

// correctionHoldPending reports whether the hold still owns frames: retained
// while armed, or pending release after a release or overflow.
func (a *attemptSession) correctionHoldPending() bool {
	if a == nil {
		return false
	}
	a.withholdMu.Lock()
	defer a.withholdMu.Unlock()
	return len(a.correctionHold.events) > 0
}

// correctionHoldState returns the hold's phase for the correction rails and
// the lifecycle: overflowed means the bound was exceeded, armed means frames
// may still be retained, and released, overflowed, or discarded mean the hold
// can never retain again.
func (a *attemptSession) correctionHoldState() correctionHoldState {
	if a == nil {
		return correctionHoldUnarmed
	}
	a.withholdMu.Lock()
	defer a.withholdMu.Unlock()
	return a.correctionHold.state
}

// discardCorrectionHold drops every retained frame without releasing it. It is
// the acceptance path's discard and the shared lifecycle cleanup reached by
// discardWithheld, so cancellation, close, attempt loss, and replacement free
// the queue. A discarded hold never releases, retains, or re-arms again.
func (a *attemptSession) discardCorrectionHold() {
	if a == nil {
		return
	}
	a.withholdMu.Lock()
	a.discardCorrectionHoldLocked()
	a.withholdMu.Unlock()
}

// discardCorrectionHoldLocked is the discard shared by discardCorrectionHold
// and discardWithheld. The caller holds withholdMu.
func (a *attemptSession) discardCorrectionHoldLocked() {
	a.correctionHold = correctionHold{state: correctionHoldDiscarded}
}

// correctionHoldOpen reports whether this attempt's hold still governs its
// produced frames: armed, or owning frames for release. A released and drained
// or discarded hold is closed. The receive loop reads it to decide whether a
// frame, an end of stream, or a backend failure must go through the hold.
func (a *attemptSession) correctionHoldOpen() bool {
	if a == nil {
		return false
	}
	a.withholdMu.Lock()
	defer a.withholdMu.Unlock()
	return a.correctionHold.state == correctionHoldArmed || len(a.correctionHold.events) > 0
}

// correctionHoldRetainsEvent reports whether one produced frame keeps the
// empty-visible-turn correction possible. Lifecycle, usage, warning, and
// reasoning frames stay in the hold (requirement 3.1; the hold deliberately
// holds reasoning frames). Produced tool-call output dissolves it, and a
// finish is never retained: the correction decision runs on the prepared
// finish, and a finish the correction does not take is delivered after the
// held prefix, in order (requirement 3.4).
func correctionHoldRetainsEvent(ev aipapi.Event) bool {
	switch ev.Kind {
	case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished,
		aipapi.EventResponseFinished:
		return false
	case aipapi.EventReasoningDelta, aipapi.EventReasoningOpaqueDelta, aipapi.EventReasoningPart:
		return true
	default:
		return !aipapi.OutputCommitted(ev)
	}
}

// routeCorrectionHeldFrame routes one produced frame through this attempt's
// prefix hold and reports whether the hold owns it, so the receive loop reads
// the backend again instead of dispatching the frame. The hold owns a frame
// when it retains it, when retaining it exceeded a bound (the frame is then
// already the release queue's tail, queued exactly once), and when a release is
// still draining (the frame follows it in order). A frame the hold does not
// govern - the hold is unarmed or was discarded by lifecycle cleanup - is
// dispatched by the caller exactly as it is without the feature.
//
// The routing is one atomic decision under withholdMu: cleanup may discard the
// hold from another goroutine, and a frame must then either be retained and
// released once or dispatched once, never dropped.
func (a *attemptSession) routeCorrectionHeldFrame(prepared recvEventPreparation) bool {
	if a == nil {
		return false
	}
	a.withholdMu.Lock()
	defer a.withholdMu.Unlock()
	switch a.correctionHold.state {
	case correctionHoldArmed:
		if !correctionHoldRetainsEvent(prepared.event) {
			// The first produced visible output, or a finish: release every
			// retained frame in order, followed by this frame.
			a.releaseCorrectionHoldLocked(prepared)
			return true
		}
		// Retained, or queued as the tail that exceeded a bound.
		a.holdCorrectionFrameLocked(prepared)
		return true
	case correctionHoldReleased, correctionHoldOverflowed:
		// A release is still draining: this frame follows it in order.
		a.correctionHold.events = append(a.correctionHold.events, prepared)
		return true
	default:
		return false
	}
}

// armCorrectionHoldForAttempt arms the attempt-local prefix hold when the turn
// can still be corrected (task 6; design "Corrective open mode" and "Terminal
// facts"). The receive loop calls it with the turn's terminal, request facts,
// and response pipeline for every attempt before the first backend read that
// attempt can make, so no produced frame can escape the hold.
//
// Arming is one-way per attempt and requires the turn to still be correctable:
// the generation-pinned gate is on, the one-shot allowance is intact, no
// canonical frame reached the client, and the request is not committed by an
// outer owner - the interleaved handoff records a wrapper-visible prefix on
// this terminal without this stream's Recv ever returning it. A hidden thinker
// leg, a large-body/wire turn, a completion-gate turn, and a turn with a wired
// terminal-decision provider can never be corrected, so they skip the hold
// instead of buffering output they would only release again.
func armCorrectionHoldForAttempt(ctx context.Context, t *turnTerminal, facts recvTurnFacts, p *responsePipeline, attempt *attemptSession) {
	if t == nil || attempt == nil {
		return
	}
	if !t.emptyVisibleTurnRetryEnabled() {
		return
	}
	if t.correctiveRetrySpent() || t.releasedClientFrameLatched() || t.committed() {
		return
	}
	if t.isInterleavedThinker() || t.terminalDecisionProvider != nil {
		return
	}
	if facts.wirePayload != nil {
		return
	}
	if p != nil && len(p.completionGatesFromContext(ctx)) > 0 {
		return
	}
	attempt.armCorrectionHold()
}

// Correction decision (spec empty-visible-turn-retry, task 5; requirements
// 2.1, 2.2, 2.4, 4.7, 5.2, 5.4; design "Correction decision" and "Retry
// observation").
//
// maybeCorrectEmptyVisibleTurn is the one receive-loop decision of this
// feature. It is decision-only in this slice: candidacy, the declared rails in
// their fixed order, and the bounded decline record. Claiming the one-shot
// allowance, discarding the hold, marking the correction pending, retiring the
// empty attempt, and running the single bounded corrective open belong to the
// corrective replacement; nothing here performs client I/O or mutates the
// conversation.

const (
	// emptyVisibleTurnRetryLogMessage is this feature's bounded decision-record
	// message.
	emptyVisibleTurnRetryLogMessage = "empty_visible_turn_retry"

	// Decline reason codes, in evaluation order: when several rails apply, the
	// first one is the recorded reason. gate_disabled deliberately has no
	// reason: a disabled generation writes no decision record at all
	// (requirement 1.2), so it emits no new signal.
	emptyVisibleTurnRetryReasonBudgetSpent              = "budget_spent"
	emptyVisibleTurnRetryReasonReleasedFrames           = "released_frames"
	emptyVisibleTurnRetryReasonWithholding              = "withholding"
	emptyVisibleTurnRetryReasonHiddenThinker            = "hidden_thinker"
	emptyVisibleTurnRetryReasonWirePath                 = "wire_path"
	emptyVisibleTurnRetryReasonCompletionGates          = "completion_gates"
	emptyVisibleTurnRetryReasonParallelRace             = "parallel_race"
	emptyVisibleTurnRetryReasonTerminalDecisionProvider = "terminal_decision_provider"
	emptyVisibleTurnRetryReasonReplacementBlocked       = "replacement_blocked"
	emptyVisibleTurnRetryReasonAttemptBudget            = "attempt_budget"
	emptyVisibleTurnRetryReasonHoldOverflow             = "hold_overflow"
	emptyVisibleTurnRetryReasonHoldNotArmed             = "hold_not_armed"

	// Decision outcomes for the optional retry observer; the closed set is
	// corrected|skipped|failed. A decline records `skipped` with its reason; an
	// accepted corrective replacement records `corrected` once its successor
	// publishes, or `failed` once when the bounded open cannot publish it.
	emptyVisibleTurnRetryOutcomeSkipped   = "skipped"
	emptyVisibleTurnRetryOutcomeCorrected = "corrected"
	emptyVisibleTurnRetryOutcomeFailed    = "failed"

	// emptyVisibleTurnRetrySwallowedReason is the bounded attempt-record reason
	// of the discarded empty attempt retired for the corrective replacement. It
	// carries no content, instruction, or model identifier.
	emptyVisibleTurnRetrySwallowedReason = "empty visible turn correction"
)

// maybeCorrectEmptyVisibleTurn decides whether the accepted clean empty finish
// of the surviving attempt is admitted for one corrective replacement (design
// "Correction decision"). Candidacy precedes the rails: only a finish with a
// clean terminal taxonomy and no produced visible output (reasoning-only
// counts as empty) is a candidate, and every other shape returns silently
// without a decision record. The rails then evaluate in their declared fixed
// order, and the first decline is recorded as exactly one bounded `skipped`
// decision. A disabled gate declines without any record at all
// (requirement 1.2), so a gate-off generation emits no new signal.
//
// The returned flag is the admission decision only: the receive loop then
// performs the acceptance (allowance claim before the fallible open, hold
// discard, pending-correction marker, and the existing non-surfacing retirement
// of the empty attempt) and runs exactly one bounded corrective open in its
// replacement block. This call performs no client I/O and mutates no
// conversation.
func maybeCorrectEmptyVisibleTurn(ctx context.Context, t *turnTerminal, s *retryRecvStream, attempt *attemptSession, finish aipapi.Event) (bool, error) {
	if t == nil || s == nil || attempt == nil {
		return false, nil
	}
	if !emptyVisibleTurn(attempt, finish) {
		return false, nil
	}
	// gate_disabled is the only rail that records nothing: a disabled
	// generation must emit no new signal at all (requirement 1.2).
	if !t.emptyVisibleTurnRetryEnabled() {
		return false, nil
	}
	if t.correctiveRetrySpent() {
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonBudgetSpent)
	}
	if t.releasedClientFrameLatched() || t.committed() {
		// Any canonical frame of the logical turn already released forbids the
		// correction (requirement 2.3). The latch covers this stream's release
		// seam; the request commitment additionally covers an outer owner - the
		// interleaved handoff records a wrapper-visible prefix on this terminal
		// without this stream's Recv ever returning it.
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonReleasedFrames)
	}
	if attempt.isWithholding() {
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonWithholding)
	}
	if t.isInterleavedThinker() {
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonHiddenThinker)
	}
	if s.facts.wirePayload != nil {
		// The large-body/wire execution path is deliberately out of this
		// slice's correction scope (deferred follow-up).
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonWirePath)
	}
	if p := s.responsePipeline; p != nil && len(p.completionGatesFromContext(ctx)) > 0 {
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonCompletionGates)
	}
	if attempt.cand.IsParallel || routeCanStartParallelRace(s.recovery) {
		// The corrective open re-plans from the same selector, and one open
		// call there can race a parallel group, opening several provider
		// attempts; the whole route declines, not only the arm this attempt
		// came from. The current candidate's own flag still covers fixtures
		// without a selector.
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonParallelRace)
	}
	if t.terminalDecisionProvider != nil {
		// Conservative coexistence: a wired provider disables the correction
		// entirely and is never invoked here - its evaluation path stays
		// byte-identical to a gate-off deployment (requirement 4.7).
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonTerminalDecisionProvider)
	}
	if p := s.responsePipeline; p != nil && p.recordingBlocksReplacement() {
		// The correction is a replacement, so it declines on the same live
		// pipeline fact the receive loop already consults before replacing
		// (mandatory secure-recording failure after committed output).
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonReplacementBlocked)
	}
	if s.recovery != nil && s.recovery.budget.exhausted() {
		// The one shared attempt budget, read under its own mutex; the
		// corrective open acquires its own slot, so a full budget declines.
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonAttemptBudget)
	}
	switch attempt.correctionHoldState() {
	case correctionHoldOverflowed:
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonHoldOverflow)
	case correctionHoldArmed:
	default:
		return t.declineEmptyVisibleTurnRetry(ctx, s.facts, attempt, emptyVisibleTurnRetryReasonHoldNotArmed)
	}
	// Admitted: the bounded corrective replacement claims the one-shot
	// allowance, discards the hold, marks the correction pending, retires the
	// empty attempt, and opens exactly one successor. No decision record is
	// written yet; that record carries the corrected/failed outcome.
	return true, nil
}

// routeCanStartParallelRace reports whether the route behind the recovery
// controller lists any parallel-group candidate in its compiled selector. The
// corrective open re-plans from that same selector, so the parallel_race rail
// must decline the whole route even when the surviving attempt's own candidate
// came from a sequential arm (requirement 2.4).
func routeCanStartParallelRace(r *recoveryController) bool {
	if r == nil || r.sel == nil {
		return false
	}
	return slices.ContainsFunc(routing.InitialCandidates(r.sel), func(c routing.AttemptCandidate) bool {
		return c.IsParallel
	})
}

// declineEmptyVisibleTurnRetry records this candidate's one bounded `skipped`
// decision and reports the decline.
func (t *turnTerminal) declineEmptyVisibleTurnRetry(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, reason string) (bool, error) {
	t.recordEmptyVisibleTurnRetryDecision(ctx, facts, attempt, emptyVisibleTurnRetryOutcomeSkipped, reason)
	return false, nil
}

// acceptEmptyVisibleTurnRetry performs the acceptance side of an admitted
// correction decision, before the fallible open: claim the one-shot allowance
// BEFORE anything can fail (requirement 4.1), discard the held prefix without
// releasing a frame (requirements 3.5, 4.4), mark the correction pending so the
// replacement block runs in its bounded mode, and retire the empty attempt
// through the existing non-surfacing terminal so its provider usage evidence
// stays available to operator accounting (requirement 4.4). It reports whether
// the turn was accepted; a lost allowance claim leaves the finish to its
// ordinary delivery.
func acceptEmptyVisibleTurnRetry(ctx context.Context, t *turnTerminal, facts recvTurnFacts, p *responsePipeline, attempt *attemptSession) bool {
	if t == nil || attempt == nil {
		return false
	}
	if !t.claimCorrectiveRetry() {
		return false
	}
	attempt.discardCorrectionHold()
	t.markCorrectionPending()
	attempt.terminalizeSwallowed(ctx, facts, p, t.committed(), emptyVisibleTurnRetrySwallowedReason, nil)
	return true
}

// correctiveReplacementFailureError is the explicit, recoverable pre-output
// upstream failure an admitted correction surfaces when its single bounded open,
// preparation, registration, or publication cannot publish (requirement 4.6).
// It carries a bounded reason and the candidate key only: the raw open error
// never reaches the client, and the wire shape stays the frontend's.
func correctiveReplacementFailureError(attempt *attemptSession) *aipapi.UpstreamFailureError {
	key := ""
	if attempt != nil {
		key = attempt.cand.Key
	}
	return &aipapi.UpstreamFailureError{
		Phase:        aipapi.PhasePreOutput,
		Recoverable:  true,
		Reason:       "empty visible turn correction could not open a replacement",
		CandidateKey: key,
	}
}

// correctiveWithdrawalError returns the withdrawal that already claimed the
// turn while the corrective open failed: a canceled shared A-leg surfacing as
// the open's own error, the caller's cancellation or deadline, or an A-leg
// cancellation observed on the terminal. Those owners keep their existing claim
// paths, so the correction never fabricates an upstream failure for a turn its
// client already withdrew (design Error Handling).
func correctiveWithdrawalError(ctx context.Context, t *turnTerminal, cause error) error {
	if errors.Is(cause, leglifecycle.ErrALegCanceled) {
		return cause
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if t != nil && t.hasALeg() {
		if scopeErr := t.aLegErr(); t.isALegCanceled(scopeErr) {
			return scopeErr
		}
	}
	return nil
}

// finalizeCorrectionDecision consumes the accepted correction's one-shot
// pending marker and records this candidate's single decision outcome exactly
// once, reporting whether this caller recorded it. Publication, the bounded
// open's failure, and the withdrawal early exits all funnel through this one
// operation, so the outcome can never be duplicated or silently lost to a
// competing lifecycle owner (requirements 4.2, 5.2).
func (t *turnTerminal) finalizeCorrectionDecision(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, outcome string) bool {
	if t == nil || !t.consumeCorrectionPending() {
		return false
	}
	t.recordEmptyVisibleTurnRetryDecision(ctx, facts, attempt, outcome, "")
	return true
}

// failCorrectiveEmptyVisibleTurnRetry ends the turn for an admitted correction
// whose single bounded open, preparation, registration, or publication could
// not publish (requirement 4.6): the pending marker is consumed, exactly one
// `failed` decision is recorded for the candidate, and the turn goes through
// the existing replacement-failure terminal with the typed recoverable
// pre-output cause. No held frame is released and no further corrective attempt
// follows. A withdrawal keeps its existing cancellation claim instead.
func (t *turnTerminal) failCorrectiveEmptyVisibleTurnRetry(ctx context.Context, s *retryRecvStream, attempt *attemptSession, cause error) error {
	if t == nil || s == nil {
		return correctiveReplacementFailureError(attempt)
	}
	t.finalizeCorrectionDecision(ctx, s.facts, attempt, emptyVisibleTurnRetryOutcomeFailed)
	if withdrawal := correctiveWithdrawalError(ctx, t, cause); withdrawal != nil {
		t.terminalizeCancellation(ctx, s.facts.terminalFacts(), attempt, s.responsePipeline, cancellationAttemptReason(ctx, withdrawal), errors.Is(withdrawal, context.DeadlineExceeded))
		if t.hasALeg() {
			_ = t.cancelALeg(ctx, aipapi.CancelCause{Kind: aipapi.CancelContextDone})
		}
		t.endALeg(aLegEndBase)
		return withdrawal
	}
	typed := correctiveReplacementFailureError(attempt)
	t.terminalizeReplacementFailure(ctx, s.facts.terminalFacts(), attempt, s.responsePipeline, typed)
	t.endALeg(aLegEndBase)
	return typed
}

// recordEmptyVisibleTurnRetryDecision emits this feature's one bounded decision
// record for one candidate and forwards the closed outcome to the optional
// retry observer. The record carries lineage, the bounded outcome, and - for a
// decline - the bounded reason; no message content, reasoning text,
// instruction, tool name, model identifier, or candidate key can reach it
// (requirements 5.2, 5.4). The backend label stays on the counter seam, whose
// adapter labels by backend and the closed outcome set.
func (t *turnTerminal) recordEmptyVisibleTurnRetryDecision(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, outcome, reason string) {
	if t == nil {
		return
	}
	backend := ""
	bLegID := ""
	if attempt != nil {
		backend = strings.TrimSpace(attempt.cand.Primary.Backend)
		bLegID = attempt.bleg.BLegID
	}
	if t.recordEmptyVisibleTurnRetry != nil {
		t.recordEmptyVisibleTurnRetry(backend, outcome)
	}
	if t.log == nil {
		return
	}
	attrs := make([]slog.Attr, 0, 4)
	attrs = append(attrs,
		slog.String("trace_id", facts.traceID),
		slog.String("b_leg_id", bLegID),
		slog.String("outcome", outcome),
	)
	if reason != "" {
		attrs = append(attrs, slog.String("reason", reason))
	}
	t.log.LogAttrs(ctx, slog.LevelInfo, emptyVisibleTurnRetryLogMessage, attrs...)
}
