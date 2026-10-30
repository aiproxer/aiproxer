package runtime

// Recv-phase inner-loop control for retryRecvStream. Stream lifecycle
// helpers (Close, handleRecvSuccess, handleRecvEOF,
// etc.) remain in executor_retry_stream.go; this file owns the inner-loop
// state machine that drives per-recv failover within an attempt's budget.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

// errGateContinueInner signals Recv to pull another inner event without returning to the client yet.
var errGateContinueInner = errors.New("runtime: completion gate continue buffering")

// pendingOrdinaryHead picks the release head for one finish route: the
// authoritative completion-gate output when preparation produced one, and the
// accepted finish otherwise. Candidate existence is independent of the ordinary
// head, so a suppressed result still releases its own evaluated sequence.
func pendingOrdinaryHead(finish, ordinary aipapi.Event) aipapi.Event {
	if ordinary.Kind != "" {
		return ordinary
	}
	return finish
}

func cancellationAttemptReason(ctx context.Context, recvErr error) string {
	if recvErr != nil {
		if errors.Is(recvErr, context.Canceled) {
			return "context canceled"
		}
		if errors.Is(recvErr, context.DeadlineExceeded) {
			return "context deadline exceeded"
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.Canceled) {
			return "context canceled"
		}
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return "context deadline exceeded"
		}
		return "context done"
	}
	return "cancelled"
}

// shouldDiscardWithheldOnError reports whether a backend receive error while
// withholding keeps discarding the queue: caller cancellation or deadline.
// Any other error (including idle and TTFT timeouts, whose caller context is
// still alive) releases first so the original output marks commitment.
func shouldDiscardWithheldOnError(ctx context.Context, recvErr error) bool {
	if errors.Is(recvErr, context.Canceled) {
		return true
	}
	return ctx.Err() != nil
}

// Recv returns the next canonical frame to the client and feeds the
// turn-scoped released-frame latch of the empty-visible-turn corrective retry
// (requirement 2.3). Every dispatch path and every drain release tail leaves
// through this boundary - the private publication drain, the recovery and
// completion-gate drains, synthesized usage, the finish routes, and the EOF
// fallback - so the latch can never miss a path the client observed. Only a
// successful return is a release: an errored return, a zero event, and the
// synthetic keepalive (the existing representation the terminal owner
// classifies) do not latch, so staged or failed-preflight work cannot mark the
// turn as already visible.
func (s *retryRecvStream) Recv(ctx context.Context) (ev aipapi.Event, err error) {
	if s == nil {
		return aipapi.Event{}, errNilRetryRecvStream
	}
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	facts := s.facts
	slot := &s.attempt
	p := s.responsePipeline
	terminal := s.terminal
	recovery := s.recovery
	// The one client-release seam of this stream: every explicit return below
	// assigns these results before the deferred latch observes them, so an
	// event that never became a released frame can never latch.
	defer func() {
		if err == nil && terminal != nil {
			terminal.noteReleasedClientFrame(ev)
		}
	}()
	// pendingExpectedFence is the ONE lexical publication fence for this receive
	// call. It decides, for one expected candidate and the origin it was retained
	// under, whether the private publication may still be staged, activated, or
	// physically delivered.
	//
	// It checks the complete identity, not a partial one: the response still
	// retains the SAME candidate value, that value's origin is the SAME attempt,
	// the frozen B-leg ID and sequence the candidate was copied under still match
	// that attempt, the live slot still publishes that same attempt, the live
	// caller context is alive, the shared A-leg carries no authoritative cause, and
	// the attempt publication window is still open. An ordinary request owner that
	// already reached Released is EXPECTED here and is not a withdrawal fence.
	//
	// The same predicate is handed to the finish authority seams and consulted at
	// every publication boundary, so staging, activation, and delivery can never
	// disagree about which identity they are publishing. It performs no I/O and
	// holds no owner, pipeline, or control mutex.
	pendingExpectedFence := func(expected *pendingCompletion, origin *attemptSession) bool {
		if p == nil || expected == nil || origin == nil {
			return false
		}
		if p.pendingPreparedSnapshot() != expected {
			return false
		}
		if expected.origin != origin {
			return false
		}
		if !pendingOriginOwns(expected, origin) {
			return false
		}
		if ctx != nil && ctx.Err() != nil {
			return false
		}
		if slot.publicationIsClosed() {
			return false
		}
		if slot.snapshot() != origin {
			return false
		}
		if terminal != nil && terminal.hasALeg() && terminal.aLegErr() != nil {
			return false
		}
		return true
	}
	// drainPending delivers exactly one already-observed publication event to the
	// client under the FULL identity fence, and converges the accepted-finish
	// bookkeeping at the ACTUAL finish delivery.
	//
	// One delivery sequences several owners: the response owner that owns the
	// retained candidate and its queue, the terminal owner that converges the
	// accepted finish, the recovery owner whose policy must observe the delivery,
	// and the live attempt slot. It therefore stays a lexical coordinator here, over
	// the references Recv already holds, rather than becoming a new carrier, a
	// receiver, or a helper that repackages the same owner graph. It carries no new
	// state: the live Recv context and every owner it sequences are captured for
	// this call only, so no persistent drain state holds a context.
	//
	// Release-tail effects run outside every mutex, and the fence is rechecked
	// after them so a withdrawal discards the remainder instead of returning content
	// the client must never see.
	drainPending := func(expected *pendingCompletion, origin *attemptSession) (aipapi.Event, bool, error) {
		if p == nil || expected == nil || origin == nil {
			return aipapi.Event{}, false, nil
		}
		if !pendingExpectedFence(expected, origin) {
			// A failed fence is the withdrawal of the candidate THIS delivery was
			// pinned to, and only of that one. A caller that owns no private
			// publication, or that is delivering some other live candidate, must not
			// erase it: the identity fence and the ownership rule are separate facts.
			if p.pendingPreparedSnapshot() == expected {
				p.withdrawPendingCompletion(origin)
			}
			return aipapi.Event{}, false, nil
		}
		ev, ok := p.pendingPublicationHead()
		if !ok {
			return aipapi.Event{}, false, nil
		}
		isUsage := p.pendingPublicationNextUsage()
		p.popPendingCompletionRelease()

		// The private drain owns its own physical delivery, so the ordinary dispatch
		// seam's client-event effects must run here exactly once per DELIVERED event,
		// including the accepted finish. Without them the attempt accounting and the
		// recovery policy would never see the published result or the physical finish,
		// because the raw finish was recorded and finalized without being released.
		// They run on delivery only, never while staging, and the event is consumed
		// above so a requeued finish is never observed twice.
		origin.observeAccountingClientEvent(p.nowTime(), ev)
		if recovery != nil && recovery.recoverPolicy != nil {
			recovery.recoverPolicy.ObserveClientEvent(ev, p.nowTime())
		}

		// Release tail: the effects that must run exactly once per DELIVERED event.
		// Local customer observation, PTC traffic, the client accumulator, affinity,
		// and compaction notification run here for the first time; the mandatory
		// recorder and the fail-closed final observer already ran once during
		// preflight, so neither repeats.
		out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{
			facts: facts, attempt: origin, recovery: recovery,
			pm:        pendingReleasePartMeta(facts, origin),
			committed: terminal.committed(), now: p.nowTime(),
			recorded: true, finalObserved: true,
		})
		if err != nil {
			p.withdrawPendingCompletion(origin)
			return aipapi.Event{}, false, err
		}
		if isUsage {
			p.emitUsageTerminal(ctx, facts.responseEvidence(), facts.baseline.Session.CorrelationID(), origin, out)
		}
		// The fence is rechecked AFTER the external release effects so a Close, an A-leg
		// cancellation, or a caller cancellation that won while they ran discards the
		// remainder instead of returning content.
		if !pendingExpectedFence(expected, origin) {
			p.withdrawPendingCompletion(origin)
			return aipapi.Event{}, false, nil
		}
		if ev.Kind == aipapi.EventResponseFinished {
			origin.recordAttemptLogged(ctx, recordAttemptParams{
				ALegID: facts.aLegID, BLeg: origin.bleg, Cand: origin.cand, Outcome: aipapi.AttemptSuccess,
			}, facts.attemptDiagAttrs(origin))
			p.commitSuccessfulTurn(facts, origin, terminal.committed())
			// A fully delivered publication closes its observer successfully, exactly
			// once, at this boundary.
			p.finishFinalStreamObservation(ctx, origin, response.OutcomeSuccessReleased)
			terminal.finishResponseAtBoundary(p, origin, p.pendingPublicationEndALeg())
			// The accepted finish is the real end of the owned window: the retained
			// candidate, its reservation, and its queue are disposed HERE and only here,
			// so no later Recv can deliver a second copy and a competing attempt can
			// never inherit a finished publication.
			p.completePendingPublication(origin)
			return out, true, nil
		}
		if aipapi.OutputCommitted(out) {
			terminal.markOutputCommittedForAttempt(out, origin, recovery)
		}
		return out, true, nil
	}
	// drainPendingFinish is the finish-route adapter over drainPending: an error
	// propagates, an exhausted drain means the caller ends the stream instead of
	// releasing the accepted finish a second time, and one drained event is returned
	// to the client. Its zero event is strictly this internal result paired with
	// continueInner=true and never a public (zero, nil) return.
	drainPendingFinish := func(expected *pendingCompletion, origin *attemptSession) (aipapi.Event, bool, error) {
		ev, more, err := drainPending(expected, origin)
		if err != nil {
			return aipapi.Event{}, false, err
		}
		if !more {
			return aipapi.Event{}, true, nil
		}
		return ev, false, nil
	}
	dispatchClientFacingEvent := func(ev aipapi.Event, prepared recvEventPreparation) (aipapi.Event, bool, error) {
		attempt := slot.require()
		transformed := p.transformClientEvent(ctx, facts, attempt, ev, prepared)
		if transformed.err != nil {
			if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, transformed.err) {
				return aipapi.Event{}, true, nil
			}
			return aipapi.Event{}, false, transformed.err
		}
		if transformed.swallowed {
			if transformed.sourceFinished {
				p.forgetToolClassification(transformed.sourceID)
			}
			return aipapi.Event{}, true, nil
		}
		ev = transformed.event
		if len(transformed.gates) > 0 {
			gated := p.applyCompletionGates(ctx, transformed.gates, facts, attempt, ev, terminal.committed())
			if errors.Is(gated.err, errGateContinueInner) {
				return aipapi.Event{}, true, nil
			}
			if gated.err != nil {
				if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, gated.recording.mandatory(), gated.err) {
					return aipapi.Event{}, true, nil
				}
				return aipapi.Event{}, false, gated.err
			}
			ev = gated.event
			if gated.finishPreflight {
				// The expected candidate and origin are captured here, before the
				// terminal runs, and the SAME pair pins the physical drain below.
				hooks := recvFinishAuthorityInput(ctx, s, attempt, gated.pending, false, pendingExpectedFence)
				usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p, hooks)
				if errors.Is(err, errTerminalDecisionContinuationPublished) {
					return aipapi.Event{}, true, nil
				}
				if err != nil {
					return aipapi.Event{}, false, err
				}
				if p.pendingPublicationActive() {
					// The accepted normal terminal staged and activated the private
					// batch. The turn is NOT physically finished yet: the accepted
					// finish bookkeeping converges at the actual finish delivery.
					return drainPendingFinish(hooks.expectedPrepared, attempt)
				}
				if ok {
					p.prependRecoveryDrain(ev)
					emitted, emitErr := terminal.emitSynthesizedUsage(ctx, usageEv, facts.terminalFacts(), attempt, p)
					return emitted, false, emitErr
				}
			}
			if aipapi.OutputCommitted(ev) {
				terminal.markCommitted(slot.snapshot())
			}
			if ev.Kind == aipapi.EventResponseFinished {
				if p != nil {
					attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: aipapi.AttemptSuccess}, facts.attemptDiagAttrs(attempt))
				}
				terminal.finishResponseAtBoundary(p, attempt, false)
			}
			attempt.observeAccountingClientEvent(p.nowTime(), ev)
			if recovery != nil && recovery.recoverPolicy != nil {
				recovery.recoverPolicy.ObserveClientEvent(ev, p.nowTime())
			}
			if gated.finishPreflight {
				out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishAfterRemember: true})
				if err != nil {
					if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err) {
						return aipapi.Event{}, true, nil
					}
					return aipapi.Event{}, false, err
				}
				if out.Kind == aipapi.EventResponseFinished {
					p.commitSuccessfulTurn(facts, attempt, terminal.committed())
				}
				return out, false, nil
			}
			out, recording, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), finishBeforeRelease: true})
			if err != nil {
				if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), err) {
					return aipapi.Event{}, true, nil
				}
				return aipapi.Event{}, false, err
			}
			return out, false, nil
		}
		// The ordinary client-event effects of the accepted finish run only when this
		// response owns NO private candidate, and preparation decides that: while a
		// candidate exists the raw finish is still a CANDIDATE, and a rejected,
		// continued, or withdrawn publication must not stamp a client finish, complete
		// the proxy, or report finished output for a batch that is never released. The
		// private drain runs those same effects exactly once at the physical delivery.
		var prep pendingPreparation
		pendingOwned := false
		if ev.Kind == aipapi.EventResponseFinished {
			prepared, pendingErr := p.preparePendingCompletion(ctx, facts, attempt, ev, nil, terminal.committed())
			if pendingErr != nil {
				if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, pendingErr) {
					return aipapi.Event{}, true, nil
				}
				return aipapi.Event{}, false, pendingErr
			}
			prep, pendingOwned = prepared, prepared.prepared.holds()
			// An authoritative completion-gate output that carries no accepted finish
			// becomes the ordinary release head here, so the ordinary gate path drains
			// it and the chain is never evaluated a second time.
			ev = pendingOrdinaryHead(ev, prep.ordinary)
		}
		if !pendingOwned {
			attempt.observeAccountingClientEvent(p.nowTime(), ev)
			if recovery != nil && recovery.recoverPolicy != nil {
				recovery.recoverPolicy.ObserveClientEvent(ev, p.nowTime())
			}
		}
		if ev.Kind == aipapi.EventResponseFinished {
			recording := responseRecordingResult{}
			if !prep.prepared.holds() {
				recording = p.recordClientFacing(ctx, facts, attempt, ev, terminal.committed())
				if recording.mandatory() {
					if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, recording.err) {
						return aipapi.Event{}, true, nil
					}
					return aipapi.Event{}, false, recording.err
				}
			}
			// The expected candidate and origin are captured here, before the terminal
			// runs, and the SAME pair pins the physical drain below.
			hooks := recvFinishAuthorityInput(ctx, s, attempt, prep.prepared, true, pendingExpectedFence)
			usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p, hooks)
			if errors.Is(err, errTerminalDecisionContinuationPublished) {
				return aipapi.Event{}, true, nil
			}
			if err != nil {
				if !terminal.finished() {
					terminal.finishResponse(p, attempt)
				}
				return aipapi.Event{}, false, err
			}
			if p.pendingPublicationActive() {
				return drainPendingFinish(hooks.expectedPrepared, attempt)
			}
			if ok {
				p.rememberClientEvent(ev)
				p.prependRecoveryDrain(ev)
				emitted, emitErr := terminal.emitSynthesizedUsage(ctx, usageEv, facts.terminalFacts(), attempt, p)
				return emitted, false, emitErr
			}
			attempt.recordAttemptLogged(ctx, recordAttemptParams{ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: aipapi.AttemptSuccess}, facts.attemptDiagAttrs(attempt))
			p.commitSuccessfulTurn(facts, attempt, terminal.committed())
			terminal.finishResponseAtBoundary(p, attempt, true)
			out, _, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), recorded: true, finishAfterRemember: true})
			if err != nil {
				if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err) {
					return aipapi.Event{}, true, nil
				}
				return aipapi.Event{}, false, err
			}
			return out, false, nil
		}
		out, recording, err := p.observeClientFacing(ctx, ev, responseEventInput{facts: facts, attempt: attempt, recovery: recovery, pm: transformed.partMeta, committed: terminal.committed(), now: p.nowTime(), finishBeforeRelease: true})
		if err != nil {
			if terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), err) {
				return aipapi.Event{}, true, nil
			}
			return aipapi.Event{}, false, err
		}
		if aipapi.OutputCommitted(out) {
			terminal.markOutputCommittedForAttempt(out, attempt, recovery)
		}
		return out, false, nil
	}
	handleEOF := func() (aipapi.Event, bool, error) {
		attempt := slot.require()
		// An end of stream while withholding: release the held output first so
		// the client sees the original call, its later output, and only then the
		// end the stream would have produced. The drain dispatches the release in
		// order, and the existing EOF handling runs on the next iteration.
		if attempt.isWithholding() {
			releaseWithheld(attempt)
			return aipapi.Event{}, true, nil
		}
		// An end of stream while the empty-visible-turn prefix is still held
		// retains that end as this handler's own pending transition, releases the
		// prefix, and re-runs this handler once the release queue drains: the
		// original end is delivered unchanged and the ended backend is never read
		// again.
		if attempt.correctionHoldOpen() {
			attempt.storeDeferredEOF()
			attempt.releaseCorrectionHold()
			return aipapi.Event{}, true, nil
		}
		if assembler := attempt.toolCallAssembler(); assembler != nil {
			if err := assembler.completionError(); err != nil {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err)
				return aipapi.Event{}, false, err
			}
		}
		clearAttemptToolState(p, attempt)
		if gates := p.completionGatesFromContext(ctx); len(gates) > 0 {
			p.abandonIncompleteGateBuffer()
		}
		if recovery != nil && recovery.recoverPolicy != nil {
			dec := recovery.eofRecvDecision(p.nowTime())
			if dec.finish {
				if dec.warning.Kind != "" {
					p.appendRecoveryDrain(dec.warning)
				}
				p.appendRecoveryDrain(dec.finishEvent)
				head, _ := p.popRecoveryDrain()
				if head.Kind == aipapi.EventResponseFinished {
					p.prependRecoveryDrain(head)
					return aipapi.Event{}, false, nil
				}
				prepared := recvEventPreparation{event: head}
				out, cont, err := dispatchClientFacingEvent(head, prepared)
				if cont {
					return aipapi.Event{}, false, nil
				}
				return out, false, err
			}
			if dec.recover {
				attempt.terminalizeSwallowed(ctx, facts, p, terminal.committed(), dec.reason, dec.err)
				recovery.exclude(attempt.cand.Key)
				return aipapi.Event{}, true, nil
			}
			if dec.continuePostOutput {
				if ctx.Err() != nil {
					reason := cancellationAttemptReason(ctx, ctx.Err())
					terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, reason, errors.Is(ctx.Err(), context.DeadlineExceeded))
					if terminal.hasALeg() {
						_ = terminal.cancelALeg(ctx, aipapi.CancelCause{Kind: aipapi.CancelContextDone})
					}
					terminal.endALeg(aLegEndBase)
					return aipapi.Event{}, false, ctx.Err()
				}
				if terminal.terminalizeEOF(ctx, facts.terminalFacts(), attempt, p) {
					return aipapi.Event{}, true, nil
				}
				fallback := aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "post_output_interrupted"}
				p.appendRecoveryDrain(fallback)
				head, _ := p.popRecoveryDrain()
				if head.Kind == aipapi.EventResponseFinished {
					p.prependRecoveryDrain(head)
					return aipapi.Event{}, false, nil
				}
				prepared := recvEventPreparation{event: head}
				out, cont, err := dispatchClientFacingEvent(head, prepared)
				if cont {
					return aipapi.Event{}, false, nil
				}
				return out, false, err
			}
		}
		if terminal.terminalizeEOF(ctx, facts.terminalFacts(), attempt, p) {
			return aipapi.Event{}, true, nil
		}
		if !terminal.finished() {
			terminal.finishResponse(p, attempt)
		}
		terminal.endALeg(aLegEndBase)
		return aipapi.Event{}, false, io.EOF
	}
	handleError := func(recvCtx context.Context, recvErr error, idleDeadline idleContextDeadline, ttftDeadline ttftContextDeadline) (aipapi.Event, bool, error) {
		attempt := slot.require()
		if attempt.isWithholding() && !shouldDiscardWithheldOnError(ctx, recvErr) {
			// A withheld attempt's backend failure must not drop the held
			// output: release it through the drain first so it is dispatched
			// in order and marks output committed, then process the same
			// receive error once the drain is empty. Never read from the
			// inner stream after its error. Cancellation and deadline keep
			// discarding the queue.
			releaseWithheld(attempt)
			attempt.storeDeferredRecvErr(recvErr, idleDeadline, ttftDeadline)
			return aipapi.Event{}, true, nil
		}
		// A backend failure while the empty-visible-turn prefix is held (and the
		// failure is not a cancellation or deadline) retains the same failure,
		// releases the prefix through the ordered queue first, and processes the
		// failure once the drain is empty, exactly as a withheld release does: an
		// attempt failure must not drop output the client would have seen
		// without the feature. Cancellation and deadline keep discarding - the
		// shared attempt cleanup frees the hold with the withhold queue
		// (requirement 3.5).
		if attempt.correctionHoldOpen() && !shouldDiscardWithheldOnError(ctx, recvErr) {
			attempt.storeDeferredRecvErr(recvErr, idleDeadline, ttftDeadline)
			attempt.releaseCorrectionHold()
			return aipapi.Event{}, true, nil
		}
		clearAttemptToolState(p, attempt)
		if idleDeadline.expired(recvCtx, recvErr) && recovery != nil && recovery.recoverPolicy != nil {
			dec := recovery.idleRecvDecision(p.nowTime())
			if dec.finish {
				attempt.setPendingCancelCause(aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: dec.reason})
				if dec.warning.Kind != "" {
					p.appendRecoveryDrain(dec.warning)
				}
				p.appendRecoveryDrain(dec.finishEvent)
				out, _ := p.popRecoveryDrain()
				if out.Kind == aipapi.EventResponseFinished {
					p.prependRecoveryDrain(out)
					return aipapi.Event{}, false, nil
				}
				out, cont, emitErr := dispatchClientFacingEvent(out, recvEventPreparation{event: out})
				if cont {
					return aipapi.Event{}, false, nil
				}
				return out, false, emitErr
			}
			if dec.recover {
				attempt.terminalizeSwallowed(ctx, facts, p, terminal.committed(), dec.reason, dec.err)
				recovery.exclude(attempt.cand.Key)
				return aipapi.Event{}, true, nil
			}
			if dec.continuePostOutput {
				if ctx.Err() != nil {
					reason := cancellationAttemptReason(ctx, ctx.Err())
					terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, reason, errors.Is(ctx.Err(), context.DeadlineExceeded))
					if terminal.hasALeg() {
						_ = terminal.cancelALeg(ctx, aipapi.CancelCause{Kind: aipapi.CancelContextDone})
					}
					terminal.endALeg(aLegEndBase)
					return aipapi.Event{}, false, ctx.Err()
				}
				attempt.setPendingCancelCause(aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: dec.reason})
				fallback := aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "post_output_interrupted"}
				p.appendRecoveryDrain(fallback)
				head, _ := p.popRecoveryDrain()
				if head.Kind == aipapi.EventResponseFinished {
					p.prependRecoveryDrain(head)
					return aipapi.Event{}, false, nil
				}
				prepared := recvEventPreparation{event: head}
				out, cont, err := dispatchClientFacingEvent(head, prepared)
				if cont {
					return aipapi.Event{}, false, nil
				}
				return out, false, err
			}
		}
		if ttftDeadline.expired(recvCtx, recvErr) && !terminal.committed() {
			ttftScope := ttftDeadline.scope
			if ttftScope == ttftTimeoutLeaf {
				tf := ttftFailure(ttftScope, attempt.cand.Key)
				attempt.terminalizeSwallowed(ctx, facts, p, terminal.committed(), ttftAttemptReason(ttftScope), tf)
				recovery.exclude(attempt.cand.Key)
				return aipapi.Event{}, true, nil
			}
			terminal.terminalizeTimeout(ctx, facts.terminalFacts(), attempt, p)
			terminal.endALeg(aLegEndBase)
			return aipapi.Event{}, false, aipapi.ErrTTFTTimeout
		}
		if errors.Is(recvErr, context.Canceled) || errors.Is(recvErr, context.DeadlineExceeded) || ctx.Err() != nil {
			reason := cancellationAttemptReason(ctx, recvErr)
			if p != nil && p.log != nil && recvErr != nil {
				p.log.DebugContext(ctx, "retry_recv context cancellation", "reason", reason)
			}
			terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, reason, errors.Is(recvErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded))
			if terminal != nil && terminal.hasALeg() {
				_ = terminal.cancelALeg(ctx, aipapi.CancelCause{Kind: aipapi.CancelContextDone})
			}
			terminal.endALeg(aLegEndBase)
			return aipapi.Event{}, false, recvErr
		}
		if terminal.committed() || !aipapi.IsRecoverablePreOutput(recvErr) {
			surfErr := recvErr
			if terminal.committed() && aipapi.IsRecoverablePreOutput(recvErr) {
				surfErr = &aipapi.UpstreamFailureError{Phase: aipapi.PhasePostOutput, Recoverable: false, Reason: attemptReasonDetail(recvErr), CandidateKey: attempt.cand.Key}
			}
			if terminal.terminalizeSurfacedFailure(ctx, facts.terminalFacts(), attempt, p, surfErr, backendReceivePanic(recvErr)) {
				return aipapi.Event{}, true, nil
			}
			return aipapi.Event{}, false, surfErr
		}
		facts.logRecoverablePreOutput(ctx, p.log, attempt.cand.Key, recvErr)
		attempt.terminalizeSwallowed(ctx, facts, p, terminal.committed(), "recoverable pre-output (recv)", recvErr)
		recovery.exclude(attempt.cand.Key)
		return aipapi.Event{}, true, nil
	}
	// The private publication drains completely before the stream may report EOF,
	// so an accepted result, its refreshed customer usage, and its finish are never
	// stranded. The first drain of this receive ALWAYS selects the retained
	// ORIGINAL origin, never the live slot, and it reads the expected candidate and
	// its frozen origin as one pair under the response-state lock. Every live fence
	// is applied by the drain itself: a withdrawn drain discards its remainder and
	// lets the existing end-of-stream behavior stand.
	if expected, expectedOrigin := p.pendingPublicationExpectation(); expected != nil {
		if ev, more, derr := drainPending(expected, expectedOrigin); derr != nil {
			return aipapi.Event{}, derr
		} else if more {
			return ev, nil
		}
	}
	if terminal.finished() || (terminal.isInterleavedThinker() && terminal.accountingFinalized()) {
		return aipapi.Event{}, io.EOF
	}
	attempt := slot.require()
	ctx = p.withDecisionEvidence(p.projectedRecvContext(ctx, facts), terminal)
	if err := ctx.Err(); err != nil {
		if terminal.finished() {
			return aipapi.Event{}, err
		}
		if attempt.hasInner() {
			attempt.drainSidebandEvidence(ctx, facts, p)
			ev, _, herr := handleError(ctx, err, idleContextDeadline{}, ttftContextDeadline{})
			if herr != nil {
				return ev, herr
			}
			return aipapi.Event{}, err
		}
		reason := cancellationAttemptReason(ctx, err)
		attempt.terminalizeEarlyCancellation(ctx, facts, p, terminal.committed(), reason, err)
		terminal.finishResponse(p, attempt)
		terminal.endALeg(aLegEndBase)
		return aipapi.Event{}, err
	}
	if ev, hasRecoveryDrain := p.popRecoveryDrain(); hasRecoveryDrain {
		if ev.Kind == aipapi.EventResponseFinished && (terminal == nil || !terminal.accountingFinalized()) {
			prep, pendingErr := p.preparePendingCompletion(ctx, facts, attempt, ev, nil, terminal.committed())
			if pendingErr != nil {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, pendingErr)
				return aipapi.Event{}, pendingErr
			}
			recording := responseRecordingResult{}
			if !prep.prepared.holds() {
				recording = p.recordClientFacing(ctx, facts, attempt, ev, terminal.committed())
				if recording.mandatory() {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, recording.err)
					return aipapi.Event{}, recording.err
				}
			}
			// The expected candidate and origin are captured here, before the terminal
			// runs, and the SAME pair pins the physical drain below.
			hooks := recvFinishAuthorityInput(ctx, s, attempt, prep.prepared, true, pendingExpectedFence)
			usageEv, ok, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p, hooks)
			if errors.Is(err, errTerminalDecisionContinuationPublished) {
				return aipapi.Event{}, nil
			}
			if err != nil {
				if !terminal.finished() {
					terminal.finishResponseAtBoundary(p, attempt, true)
				}
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, err)
				return aipapi.Event{}, err
			}
			if p.pendingPublicationActive() {
				ev, more, drainErr := drainPending(hooks.expectedPrepared, attempt)
				if drainErr != nil {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, drainErr)
					return aipapi.Event{}, drainErr
				}
				if !more {
					return aipapi.Event{}, io.EOF
				}
				return ev, nil
			}
			if ok {
				p.prependRecoveryDrain(ev)
				return terminal.emitSynthesizedUsage(ctx, usageEv, facts.terminalFacts(), attempt, p)
			}
		}
		pm := facts.partMeta(attempt.bleg, attempt.cand)
		out, recording, emitErr := p.observeClientFacing(ctx, ev, responseEventInput{
			facts: facts, attempt: attempt, recovery: recovery,
			pm: pm, committed: terminal.committed(), now: p.nowTime(), recorded: ev.Kind == aipapi.EventResponseFinished,
			finishBeforeRelease: true,
		})
		if emitErr != nil {
			terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), emitErr)
		}
		if emitErr == nil && ev.Kind == aipapi.EventResponseFinished {
			terminal.noteAcceptedLogicalCompletion()
			terminal.finishResponse(p, attempt)
		}
		if emitErr == nil && aipapi.OutputCommitted(out) {
			terminal.markOutputCommittedForAttempt(out, attempt, recovery)
		}
		return out, emitErr
	}
	for {
		// Replacement installs a new attempt session while this Recv call
		// continues. Refresh the short-lived snapshot before any attempt-local
		// receive or terminal decision; never carry the retired B-leg identity
		// into the replacement.
		attempt = slot.require()
		// A continuation published inside this Recv also updates immutable request
		// facts. Its next candidate must use the same current lineage as its attempt.
		facts = s.facts
		if ev, more, drainErr := drainPending(p.pendingPreparedSnapshot(), attempt); drainErr != nil {
			return aipapi.Event{}, drainErr
		} else if more {
			return ev, nil
		}
		// A retained prefix must never escape after the turn was withdrawn: a
		// backend read that already obtained a buffered frame and still returned
		// it successfully does not make that frame released output
		// (requirement 3.5). The fence consults the caller's context and the
		// shared A-leg's cancellation - the two withdrawal authorities this loop
		// already mirrors - and routes each through its existing owner, so the
		// held frames and any pending terminal transition are discarded and no
		// later drain or deferred consumption can revive them. It guards only the
		// hold's release/drain: ordinary dispatch and a gate-off turn are
		// unchanged.
		if attempt.correctionHoldOpen() {
			if terminal.hasALeg() {
				if scopeErr := terminal.aLegErr(); terminal.isALegCanceled(scopeErr) {
					clearAttemptToolState(p, attempt)
					terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, "a-leg canceled", false)
					terminal.endALeg(aLegEndBase)
					return aipapi.Event{}, scopeErr
				}
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				ev, cont, err := handleError(ctx, ctxErr, idleContextDeadline{}, ttftContextDeadline{})
				if cont {
					continue
				}
				return ev, err
			}
		}
		// The empty-visible-turn prefix hold releases through its own ordered
		// queue before any other already-produced source: a held frame was
		// produced earlier than anything the assembler or a gate drain can
		// offer, and the frame that dissolved the hold was queued exactly once
		// (requirement 3.6).
		if prepared, held := attempt.popCorrectionHeld(); held {
			out, cont, err := dispatchClientFacingEvent(prepared.event, prepared)
			if cont {
				continue
			}
			return out, err
		}
		// A tool-name correction accepted on this attempt owns the rest of its
		// output, but the prefix it produced before acceptance must still reach
		// the client in order (requirement 3.2): the hold is released with no
		// tail, because the frame that triggered acceptance belongs to the
		// withhold queue.
		if attempt.isWithholding() && attempt.correctionHoldOpen() {
			attempt.releaseCorrectionHold()
			continue
		}
		if toolFinal := attempt.toolCallAssembler(); toolFinal != nil {
			if ev, ok := toolFinal.popDrain(); ok {
				// A withheld attempt's released lifecycle is held in its queue
				// rather than dispatched here; only a released queue reaches the
				// client path, and then through the same dispatcher.
				if p.divertWithheld(ctx, facts, attempt, ev) {
					continue
				}
				// Produced tool-call output dissolves the empty-visible-turn
				// hold first: the held prefix is released in order, followed by
				// this frame, so the client sees exactly the gate-off sequence.
				if attempt.routeCorrectionHeldFrame(recvEventPreparation{event: ev}) {
					continue
				}
				out, cont, err := dispatchClientFacingEvent(ev, recvEventPreparation{event: ev})
				if cont {
					continue
				}
				return out, err
			}
			// A finalizer error is about to surface. The withheld output must
			// reach the client before it, so release the queue and let the drain
			// deliver it in order; the error surfaces on a later iteration once
			// the drain is empty.
			if attempt.isWithholding() && toolFinal.hasDrainError() {
				releaseWithheld(attempt)
				continue
			}
			if err := toolFinal.popDrainError(); err != nil {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, err)
				return aipapi.Event{}, err
			}
		}
		if ev, ok := p.popGateDrainHead(); ok {
			// A gate-drain finish is finalized through the same centralized chokepoint as the other
			// response_finished completion paths, before emitGateDrained marks the stream finished, so
			// a reconstructed-usage (ok) result can re-queue the finish and emit the synthesized
			// usage_delta without stranding the finish behind a finished stream. The non-ok result
			// falls through to emitGateDrained + the standard client-event emit. Without this the
			// gate-drain site leaked its reserved authority (it had no finalization at all before
			// centralization).
			if ev.Kind == aipapi.EventResponseFinished && (terminal == nil || !terminal.accountingFinalized()) {
				prep, pendingErr := p.preparePendingCompletion(ctx, facts, attempt, ev, nil, terminal.committed())
				if pendingErr != nil {
					terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, pendingErr)
					return aipapi.Event{}, pendingErr
				}
				recording := responseRecordingResult{}
				if !prep.prepared.holds() {
					recording = p.recordClientFacing(ctx, facts, attempt, ev, terminal.committed())
					if recording.mandatory() {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, true, recording.err)
						return aipapi.Event{}, recording.err
					}
				}
				// The expected candidate and origin are captured here, before the
				// terminal runs, and the SAME pair pins the physical drain below.
				hooks := recvFinishAuthorityInput(ctx, s, attempt, prep.prepared, false, pendingExpectedFence)
				usageEv, usageOk, err := terminal.finalizeResponseFinishedAuthority(ctx, ev, facts.terminalFacts(), attempt, p, hooks)
				if errors.Is(err, errTerminalDecisionContinuationPublished) {
					continue
				}
				if err != nil {
					if !terminal.finished() {
						terminal.finishResponse(p, attempt)
					}
					return aipapi.Event{}, err
				}
				if p.pendingPublicationActive() {
					out, more, drainErr := drainPending(hooks.expectedPrepared, attempt)
					if drainErr != nil {
						terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, drainErr)
						return aipapi.Event{}, drainErr
					}
					if !more {
						continue
					}
					return out, nil
				}
				if usageOk {
					p.prependRecoveryDrain(ev)
					emitted, emitErr := terminal.emitSynthesizedUsage(ctx, usageEv, facts.terminalFacts(), attempt, p)
					return emitted, emitErr
				}
			}
			if aipapi.OutputCommitted(ev) {
				terminal.markCommitted(slot.snapshot())
			}
			if ev.Kind == aipapi.EventResponseFinished {
				if p != nil {
					attempt.recordAttemptLogged(ctx, recordAttemptParams{
						ALegID: facts.aLegID, BLeg: attempt.bleg, Cand: attempt.cand, Outcome: aipapi.AttemptSuccess,
					}, facts.attemptDiagAttrs(attempt))
				}
				terminal.finishResponseAtBoundary(p, attempt, false)
			}
			attempt.observeAccountingClientEvent(p.nowTime(), ev)
			pm := facts.partMeta(attempt.bleg, attempt.cand)
			out, recording, emitErr := p.observeClientFacing(ctx, ev, responseEventInput{
				facts: facts, attempt: attempt, recovery: recovery,
				pm: pm, committed: terminal.committed(), now: p.nowTime(), recorded: ev.Kind == aipapi.EventResponseFinished,
				finishBeforeRelease: true,
			})
			if emitErr != nil {
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, recording.mandatory(), emitErr)
			}
			if emitErr == nil && aipapi.OutputCommitted(out) {
				terminal.markOutputCommittedForAttempt(out, attempt, recovery)
			}
			return out, emitErr
		}
		for {
			attempt = slot.require()
			if attempt.hasInner() {
				break
			}
			if slot.publicationIsClosed() {
				// A Close (or any owner that closed the publication window) won
				// while the accepted correction was retiring the empty attempt:
				// the withdrawal keeps its existing error/EOF claim and nothing
				// is published, while the correction still finalizes its single
				// bounded outcome record exactly once (requirements 4.6, 5.2).
				terminal.finalizeCorrectionDecision(ctx, facts, attempt, emptyVisibleTurnRetryOutcomeFailed)
				if err := terminal.aLegErr(); err != nil {
					return aipapi.Event{}, err
				}
				return aipapi.Event{}, io.EOF
			}
			if terminal != nil && terminal.hasALeg() {
				if scopeErr := terminal.aLegErr(); terminal.isALegCanceled(scopeErr) {
					// Same finalization before the existing A-leg cancellation
					// claim: the candidate's single `failed` record is never
					// lost, and no fabricated upstream failure replaces the
					// withdrawal that already owns the turn.
					terminal.finalizeCorrectionDecision(ctx, facts, attempt, emptyVisibleTurnRetryOutcomeFailed)
					terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, "a-leg canceled", false)
					terminal.endALeg(aLegEndBase)
					return aipapi.Event{}, scopeErr
				}
			}
			if terminal.committed() && p.recordingBlocksReplacement() && p.secureRecordingMandatory {
				if err := terminal.terminalizeGateReplacement(ctx, facts.terminalFacts(), slot.require(), p); err != nil {
					return aipapi.Event{}, err
				}
			}
			// The pending-correction marker binds this iteration to the single
			// bounded corrective open (requirements 4.2, 4.6): its failures
			// terminalize the turn instead of returning a keepalive that would
			// re-plan another candidate, and its publication is the one
			// `corrected` decision for the corrected candidate.
			corrective := terminal.correctionPending()
			plan, err := recovery.tryReplacementIteration(ctx, facts.terminalFacts(), attempt, terminal.committed())
			if err != nil {
				if corrective {
					return aipapi.Event{}, terminal.failCorrectiveEmptyVisibleTurnRetry(ctx, s, attempt, err)
				}
				terminal.terminalizeReplacementFailure(ctx, facts.terminalFacts(), attempt, p, nil)
				terminal.endALeg(aLegEndBase)
				return aipapi.Event{}, err
			}
			if !plan.opened {
				if corrective {
					return aipapi.Event{}, terminal.failCorrectiveEmptyVisibleTurnRetry(ctx, s, attempt, nil)
				}
				return p.keepaliveEvent(), nil
			}
			ready := plan.next
			if err := ready.Prepare(ctx, facts, p, terminal.committed()); err != nil {
				if corrective {
					return aipapi.Event{}, terminal.failCorrectiveEmptyVisibleTurnRetry(ctx, s, attempt, err)
				}
				terminal.terminalizeReplacementFailure(ctx, facts.terminalFacts(), attempt, p, nil)
				terminal.endALeg(aLegEndBase)
				return aipapi.Event{}, err
			}
			if err := terminal.registerReplacement(ctx, plan.open, ready); err != nil {
				ready.Dispose(ctx, err)
				if corrective {
					return aipapi.Event{}, terminal.failCorrectiveEmptyVisibleTurnRetry(ctx, s, attempt, err)
				}
				terminal.terminalizeReplacementFailure(ctx, facts.terminalFacts(), attempt, p, nil)
				terminal.endALeg(aLegEndBase)
				return aipapi.Event{}, err
			}
			clearAttemptToolState(p, attempt)

			_, published := slot.swapIfOpen(ready)
			if !published {
				// Disposal of unconsumed ready attempt must invoke complete attempt terminalization
				ready.Dispose(ctx, fmt.Errorf("recv loop replacement: %w", errPublicationClosed))
				if corrective {
					return aipapi.Event{}, terminal.failCorrectiveEmptyVisibleTurnRetry(ctx, s, attempt, errPublicationClosed)
				}
				return p.keepaliveEvent(), nil
			}

			p.resetForReplacement()
			recovery.resetPolicy(p.nowTime)
			if corrective {
				// Published: exactly one `corrected` decision for the corrected
				// candidate (the record belongs to that candidate decision, so
				// it carries the discarded attempt's lineage; the successor's
				// own empty finish, if any, is its own candidate decision),
				// then this turn is ordinary again. The bounded corrective
				// open never re-plans (requirements 4.2, 5.2).
				terminal.finalizeCorrectionDecision(ctx, facts, attempt, emptyVisibleTurnRetryOutcomeCorrected)
				break
			}
		}
		attempt = slot.require()
		// The empty-visible-turn prefix hold is armed once per attempt, before
		// its first backend read, so no produced frame of a correctable turn can
		// reach the client before the correction decision.
		armCorrectionHoldForAttempt(ctx, terminal, facts, p, attempt)
		// Connector sideband frames can arrive after Open returns. Drain immediately
		// before each receive so pre-first-event evidence is accounted even when the
		// transport reports its first read error or cancellation.
		attempt.drainSidebandEvidence(ctx, facts, p)
		// A deferred terminal transition waits until every release queue has
		// drained: the drain checks above already passed empty in this
		// iteration, so the prefix and any withheld output were delivered, and
		// the transition this attempt already observed can run its own existing
		// handling exactly once - without reading, preparing, or re-classifying
		// the backend again.
		if deferred, hasDeferredWork := attempt.takeDeferredTerminalWork(); hasDeferredWork {
			switch deferred.kind {
			case deferredTerminalEOF:
				// The backend already ended: run the original end-of-stream
				// handling directly.
				ev, cont, err := handleEOF()
				if cont {
					continue
				}
				return ev, err
			case deferredTerminalPrepareError:
				// The prepared-event failure is already observed: terminalize
				// through the same partial-failure path the immediate branch
				// uses, with the original error identity.
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, deferred.err)
				return aipapi.Event{}, deferred.err
			default:
				ev, cont, err := handleError(ctx, deferred.err, deferred.idle, deferred.ttft)
				if cont {
					continue
				}
				return ev, err
			}
		}
		recvCtx := ctx
		var cancelRecv context.CancelFunc = func() {}
		ttftDeadline := ttftContextDeadline{}
		if !terminal.committed() && recovery != nil && recovery.ttft != nil {
			recvCtx, cancelRecv, ttftDeadline = recovery.ttft.scopedContext(ctx, p.nowTime(), attempt.cand.Key, attempt.cand.Primary.TTFTTimeout)
		}
		recvCtx, cancelRecv, idleDeadline := recovery.scopedIdleContext(recvCtx, cancelRecv, p.nowTime())
		ev, err := attempt.receive(recvCtx, terminal.committed())
		cancelRecv()
		// Evidence may be published during the receive itself. Drain after the
		// call so a final event, EOF, or error cannot discard that evidence.
		attempt.drainSidebandEvidence(ctx, facts, p)
		// Close/cancel may have terminalized while we were blocked. Do not run
		// NormalFinish (or surface bare context.Canceled) after that owner won.
		if terminal.finished() {
			if terminal != nil && terminal.hasALeg() {
				if scopeErr := terminal.aLegErr(); terminal.isALegCanceled(scopeErr) {
					return aipapi.Event{}, scopeErr
				}
			}
			return aipapi.Event{}, io.EOF
		}
		if err != nil && terminal != nil && terminal.hasALeg() {
			if scopeErr := terminal.aLegErr(); terminal.isALegCanceled(scopeErr) {
				terminal.terminalizeCancellation(ctx, facts.terminalFacts(), attempt, p, "a-leg canceled", false)
				terminal.endALeg(aLegEndBase)
				return aipapi.Event{}, scopeErr
			}
		}
		if err == nil {
			if p.consumeContinuationLifecycleMarker(ev) {
				continue
			}
			// The steer gate is built by the receive-loop coordinator for the
			// only event that can consult it: a completed tool call.
			steerGate := p.toolNameCorrectionGateForEvent(ctx, facts, terminal, attempt, ev)
			prepared := p.prepareRecvEvent(ctx, facts, attempt, ev, steerGate)
			if prepared.err != nil {
				if ev.Kind == aipapi.EventResponseFinished && (attempt.isWithholding() || attempt.correctionHoldOpen()) {
					// A withheld attempt's output must reach the client before
					// the assembly boundary's own completion failure ends the
					// attempt: the queue is released and the same partial
					// failure surfaces once the drain has delivered it, exactly
					// as it does at an end of stream. A held empty-visible-turn
					// prefix releases the same way, and the already-observed
					// preparation failure is retained exactly so it terminalizes
					// with its original identity instead of being rediscovered
					// through another backend read.
					if attempt.isWithholding() {
						releaseWithheld(attempt)
					}
					if attempt.correctionHoldOpen() {
						attempt.storeDeferredPrepareError(prepared.err)
						attempt.releaseCorrectionHold()
					}
					continue
				}
				terminal.partialFailure(ctx, p, facts.terminalFacts(), attempt, false, prepared.err)
				return aipapi.Event{}, prepared.err
			}
			if prepared.swallowed {
				continue
			}
			// The empty-visible-turn finish intercept runs after preparation -
			// the same position as the tool-name correction trigger - and
			// before any dispatch: the decision needs the prepared finish, and
			// a held prefix must be released before a finish the correction does
			// not take (requirement 3.4). An admitted decision claims the
			// one-shot allowance BEFORE the fallible open, discards the held
			// prefix, marks the correction pending, and retires the empty
			// attempt through the existing non-surfacing terminal; the single
			// bounded corrective open then runs in the replacement block below
			// and this empty finish is never dispatched (requirements 4.1, 4.4,
			// 4.6).
			if ev.Kind == aipapi.EventResponseFinished {
				correcting, decisionErr := maybeCorrectEmptyVisibleTurn(ctx, terminal, s, attempt, prepared.event)
				if decisionErr != nil {
					return aipapi.Event{}, decisionErr
				}
				if correcting && acceptEmptyVisibleTurnRetry(ctx, terminal, facts, p, attempt) {
					continue
				}
			}
			// A withholding attempt's backend finish is the correction
			// trigger. It runs after preparation, so the backend-side
			// observation a finish always gets - accounting, provider metering,
			// BTP traffic, usage, control-call closure, and the assembler's
			// completion check - still runs, and before any client dispatch, so
			// the finish never enters the withhold queue.
			if attempt.isWithholding() && ev.Kind == aipapi.EventResponseFinished {
				if correctionErr := runToolCorrection(ctx, terminal, s, attempt, prepared.event); correctionErr != nil {
					return aipapi.Event{}, correctionErr
				}
				continue
			}
			// While the turn can still be corrected, produced frames are held
			// instead of dispatched (requirement 3.1). The first visible output,
			// a bound, or any finish releases them in their original order
			// (requirements 3.2, 3.3, 3.4); the release queue drains through the
			// client-release seam at the top of this loop, before the backend is
			// read again, so each frame is transformed and recorded exactly once
			// (requirement 3.6).
			if !attempt.isWithholding() && attempt.routeCorrectionHeldFrame(prepared) {
				continue
			}
			// A withholding attempt's events belong to its withhold queue until
			// the queue is released.
			if p.divertWithheld(ctx, facts, attempt, ev) {
				continue
			}
			ev, cont, err := dispatchClientFacingEvent(ev, prepared)
			if cont {
				continue
			}
			return ev, err
		}
		if errors.Is(err, io.EOF) {
			ev, cont, err := handleEOF()
			if cont {
				continue
			}
			return ev, err
		}
		ev, cont, err := handleError(recvCtx, err, idleDeadline, ttftDeadline)
		if cont {
			continue
		}
		return ev, err
	}
}
