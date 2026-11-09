package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/terminal"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// persistCancellationBilling settles non-money usage-authority reservations for a
// canceled attempt. Monetary cancellation work belongs exclusively to post-usage
// current call/leg rating after terminal handoff.
//
// Evidence recovery is first: a mid-stream EventUsageDelta is enough, otherwise
// FinalizeBilling may recover provider evidence (shared with call-leg usage via finalizeOnce).
// Authoritative evidence reconciles an already-settled reservation (requirement
// 7.6, 8.4-8.6); without it the path only settles Cancellation and never
// re-opens a prior Partial/Final. One tail then applies advisory usage and
// request settle/release. Settlement uses a non-canceled context so post-output
// accounting completes after client cancellation (requirement 11.7).
func (t *turnTerminal) persistCancellationBilling(ctx context.Context, attempt *attemptSession, reason string, request requestTerminalFacts, p *responsePipeline) {
	ctx = request.toRecvTurnFacts(ctx).projectContext(ctx, nil)
	if t == nil || attempt == nil {
		return
	}
	if attempt.accounting.usageObserved || t.finalizeBillingAfterCancel(ctx, attempt, reason, request, p) {
		t.reconcileOrSettleCancellationAuthorityForAttempt(ctx, attempt, p)
	} else {
		t.settleCancellationAuthorityForAttempt(ctx, attempt, p)
	}
	t.finishCancellationAuthorityForAttempt(ctx, attempt, request, p)
}

func (t *turnTerminal) finishCancellationAuthorityForAttempt(ctx context.Context, attempt *attemptSession, request requestTerminalFacts, p *responsePipeline) {
	if t == nil || attempt == nil {
		return
	}
	attempt.authority.ApplyUnreservedUsage(ctx, usagelimit.SettlementKindCancellation, p.operatorUsageForFinalize())
	t.settleOrReleaseRequestAuthority(ctx, p, request)
}

// reconcileOrSettleCancellationAuthority routes the cancellation settlement based
// on whether the reservation is already settled. When already settled AND
// authoritative usage is available (the caller guarantees usageObserved or
// finalizeBilling succeeded), it calls ReconcileAuthoritative to adjust the prior
// estimated settlement with the authoritative usage event. When not yet settled,
// it routes to settleCancellationAuthority which settles as a Cancellation.
func (t *turnTerminal) reconcileOrSettleCancellationAuthorityForAttempt(ctx context.Context, attempt *attemptSession, p *responsePipeline) {
	if t == nil || attempt == nil {
		return
	}
	if attempt.authority.Settled() {
		attempt.authority.ReconcileAuthoritative(ctx, p.operatorUsageForFinalize())
		return
	}
	t.settleCancellationAuthorityForAttempt(ctx, attempt, p)
}

// settleCancellationAuthority settles the usage-authority reservation for a canceled
// attempt with the observed usage as a Cancellation. It is a no-op when the
// reservation is already settled (preventing a double settle of a strict
// reservation, e.g. after a prior partial/final settle). The losing-attempt
// release (ReleaseKindLosing when the settle fails) now lives inside the authorityLifecycle
// owner's Settle, mirroring the finalizeResponseFinishedAuthority path. It passes
// a non-canceled context to Settle so cancellation of the client request does not
// abort the post-output settlement (requirement 11.7).
func (t *turnTerminal) settleCancellationAuthorityForAttempt(ctx context.Context, attempt *attemptSession, p *responsePipeline) {
	if t == nil || attempt == nil || attempt.authority.Settled() {
		return
	}
	usageEv := p.operatorUsageForFinalize()
	attempt.authority.Settle(ctx, usagelimit.SettlementKindCancellation, usageEv, true)
	t.emitBackendEgressMeteringFactForAttempt(ctx, attempt, metering.AttemptOutcomeCanceled, metering.SurfacedNo, usageEv)
}

func (t *turnTerminal) finalizeBillingAfterCancel(ctx context.Context, attempt *attemptSession, reason string, request requestTerminalFacts, p *responsePipeline) bool {
	if t == nil || (t.finalizeBilling == nil && t.finalizeBillingV2 == nil) {
		return false
	}
	if attempt == nil {
		return false
	}
	billingState := request.billingState
	if billingState == nil {
		billingState = attempt.billingCallState
	}
	if billingState == nil {
		return false
	}
	traceID := strings.TrimSpace(request.traceID)
	if traceID == "" {
		traceID = strings.TrimSpace(attempt.traceID)
	}
	aLegID := strings.TrimSpace(request.aLegID)
	if aLegID == "" {
		aLegID = strings.TrimSpace(attempt.bleg.ALegID)
	}
	result, ok := t.finalizeOnceWithEvidence(ctx, billingState, execbackend.BillingFinalizationInput{
		TraceID: traceID,
		ALegID:  aLegID,
		BLegID:  strings.TrimSpace(attempt.bleg.BLegID),
		Backend: strings.TrimSpace(attempt.cand.Primary.Backend),
		Model:   strings.TrimSpace(attempt.cand.Primary.Model),
		Reason:  strings.TrimSpace(reason),
	})
	if !ok {
		return false
	}
	ev := result.Usage
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), billingFinalizeTimeout)
	defer cancel()
	// Cancellation finalization claims the call-level finalizer before the
	// ordinary terminal record callback. Retain that provider source on this
	// attempt so the later B-leg closure cannot silently fall back to the
	// stream projection when finalizeOnce reports the claim is already spent.
	attempt.rememberUsageEvidenceOnceAs(ev, billingEvidenceRoleFinalizer)
	attempt.observeAccountingUsage(ev)
	for _, economic := range result.EconomicEvidence {
		attempt.rememberEconomicEvidenceOnce(economic)
	}
	p.rememberClientEvent(ev)
	recording := p.recordClientFacingTerminal(persistCtx, request, attempt, ev, t.committed())
	if recording.err != nil && p.log != nil {
		p.log.DebugContext(persistCtx, "secure_session billing finalizer marker", "error", recording.err)
	}
	p.emitUsageTerminal(persistCtx, request.responseEvidence(), request.call.Session.CorrelationID(), attempt, ev)
	return true
}

func (t *turnTerminal) finalizeTokenAccounting(ctx context.Context, attempt *attemptSession, finish aipapi.Event, request requestTerminalFacts, p *responsePipeline) (aipapi.Event, bool, error) {
	if t == nil || p == nil {
		return aipapi.Event{}, false, nil
	}
	if attempt == nil {
		return aipapi.Event{}, false, nil
	}
	if p.streamUsage == nil {
		p.setLastAuthorityUsage(aipapi.Event{})
		attempt.authority.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false)
		return aipapi.Event{}, false, nil
	}
	events := append(p.seenEventsCopy(), finish)
	result, err := p.streamUsage.Reconstruct(ctx, accountingstream.Input{
		Backend:    strings.TrimSpace(attempt.cand.Primary.Backend),
		Model:      strings.TrimSpace(attempt.cand.Primary.Model),
		Call:       request.call,
		OutputText: p.releasedOutputText(),
		Events:     events,
	})
	if err != nil && p.log != nil {
		p.log.DebugContext(ctx, "token accounting stream reconstruction", "error", err)
	}
	if len(result.Events) == 0 {
		p.setLastAuthorityUsage(aipapi.Event{})
		attempt.authority.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false)
		return aipapi.Event{}, false, nil
	}
	authorityEv := authorityUsageEvent(result.Events)
	clientUsageEv := mergeUsageEventsForClient(result.Events, tokenAccountingHasProviderUsage(p.seenEventsCopy()))
	// Strip any residual monetary fields: protocol usage is a read-side projection
	// only. Customer/operator money is owned exclusively by sealed current-record rating.
	clientUsageEv.CostNanoUnits = 0
	clientUsageEv.Currency = ""
	clientUsageEv.CostSource = ""
	clientUsageEv.CostPresent = false
	p.setLastAuthorityUsage(authorityEv)
	p.setLastCustomerUsage(customerPlaneUsageEvent(clientUsageEv))
	// The legacy token ledger is intentionally not written here. Client-visible
	// usage remains a protocol/read-side projection; monetary settlement is owned
	// by the sealed current-record post-usage processor.
	attempt.authority.Settle(ctx, usagelimit.SettlementKindFinal, authorityEv, false)
	return clientUsageEv, true, nil
}

// finalizeResponseFinishedAuthority is the single authority-finalization chokepoint for
// response_finished completion paths. It runs token-accounting finalization, which settles
// the usage-authority reservation via the authorityLifecycle owner (the owner folds the
// losing-attempt release into Settle, so a failed settle releases ReleaseKindLosing and
// marks the lifecycle settled). Idempotent via the turn terminal's request-level
// accounting-finalized claim (which gates
// usage-delta re-queue, not authority idempotency — the owner owns that via settled). It
// does NOT mark the stream finished and does NOT queue the event — callers own
// emission/finish timing.
//
// After token-accounting finalization it also applies advisory usage (requirement 7.7) so
// advisory windows accumulate actual usage even when the request was not reserved. The
// advisory apply runs on a non-canceled context so post-output accounting completes after
// client cancellation, and is idempotent via the store source key (duplicate finalize calls
// are no-ops at the runtime guard and at the store).
// finishAuthorityInput is the narrow private typed optional input of the
// response_finished authority chokepoint. It carries no terminal authority of its
// own: it only names the publication seams the receive stream already owns. Every
// field is optional, and a zero value preserves the provisional behavior callers
// without a receive transaction already had.
type finishAuthorityInput struct {
	// continuation is the one core-owned publication seam for a provider
	// continuation. It is unchanged.
	continuation func(context.Context, terminaldecision.ContinuationIntent) (bool, error)
	// publishable re-checks the receive-side publication fence for a private
	// pending completion result: the attempt publication window. It performs no I/O
	// and takes no terminal ownership. The receive loop binds it to the ONE lexical
	// identity fence over expectedPrepared and expectedOrigin, so the same
	// predicate decides staging, activation, and delivery.
	publishable func() bool
	// callerFence re-checks the live caller context of the receive loop that
	// accepted the terminal. The terminal owner runs its effects on a DETACHED
	// bounded cleanup context, so a client cancellation that already won is only
	// visible through this original caller. It performs no I/O and takes no
	// terminal ownership.
	callerFence func() bool
	// expectedPrepared is the EXACT candidate this terminal call captured BEFORE any
	// terminal work, and expectedOrigin the attempt it was retained under.
	//
	// They are private IDENTITY FACTS, not an owner graph: no pipeline, terminal,
	// attempt slot, or context is carried here. The claim, the reservation, the
	// activation, and the physical drain are all checked against this pair, so a
	// publication that disappeared after the capture is a withdrawal instead of
	// silently becoming "no publication was expected".
	expectedPrepared *pendingCompletion
	expectedOrigin   *attemptSession
	// acceptedNormal stages this turn's one private pending completion
	// publication: it builds and prefights the complete canonical batch and
	// nothing else. It runs only inside the winning effects of an accepted
	// normal terminal, before customer settlement and billing handoff, and never
	// on a surfaced failure, a continuation, an attempt or request loser, an
	// attempt error, or an authoritative cancel or Close.
	//
	// ownerCtx is the winning owner's bounded cleanup context, so recorder,
	// observer, and PTC work is never unbounded. usage and usageOK are the REAL
	// values AuthorityPrepare just computed in this very terminal call; no
	// pipeline round trip may replace them with an empty shell.
	acceptedNormal func(ctx context.Context, usage aipapi.Event, usageOK bool) error
}

// publishesPendingCompletion reports whether this turn carries a prepared
// candidate that must be published at an accepted normal terminal. Candidate
// EXISTENCE is independent of result eligibility, so a suppressed result still
// owns the authoritative ordinary stream and its real finish and drains through
// the same accepted path.
func (f finishAuthorityInput) publishesPendingCompletion(prepared *pendingCompletion) bool {
	return prepared.holds() && f.acceptedNormal != nil
}

// pendingPublicationFenceOpen reports whether the FULL live publication fence is
// still open at activation: the caller-supplied identity and window fence, the
// live caller, and the shared A-leg cause. A zero input preserves the provisional
// behavior callers without a receive transaction already had.
func (f finishAuthorityInput) pendingPublicationFenceOpen(t *turnTerminal) bool {
	if t != nil && t.hasALeg() && t.aLegErr() != nil {
		return false
	}
	if f.publishable != nil && !f.publishable() {
		return false
	}
	if f.callerFence != nil && !f.callerFence() {
		return false
	}
	return true
}

func (t *turnTerminal) finalizeResponseFinishedAuthority(ctx context.Context, ev aipapi.Event, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, in ...finishAuthorityInput) (aipapi.Event, bool, error) {
	if attempt == nil || t == nil || p == nil {
		return aipapi.Event{}, false, nil
	}
	var hooks finishAuthorityInput
	if len(in) > 0 {
		hooks = in[0]
	}
	if t.accountingFinalized() && t.requestTerminal().Owner().State().IsTerminal() {
		return aipapi.Event{}, false, nil
	}
	snapshot := p.accumulatorSnapshot()
	decision := t.sharedTerminalDecision(ctx, t.terminalDecisionProvider, t.terminalDecisionEvalInput(sdkterminal.CommandNormalFinish, request, attempt, p, snapshot))
	if decision.Decision.Kind == terminaldecision.DecisionContinue {
		// A continuation retires this attempt's private candidate BEFORE the
		// existing transaction runs, so the replacement B-leg can never publish the
		// continued attempt's lexical result.
		p.discardPendingCompletion()
		if hooks.continuation == nil || decision.Decision.Continue == nil {
			// Callers without a receive transaction preserve the provisional
			// behavior until they can supply the generic publication boundary.
			return aipapi.Event{}, false, nil
		}
		published, err := hooks.continuation(ctx, *decision.Decision.Continue)
		if published {
			if err != nil {
				return aipapi.Event{}, false, fmt.Errorf("%w: %v", errTerminalDecisionContinuationPublished, err)
			}
			return aipapi.Event{}, false, errTerminalDecisionContinuationPublished
		}
		return aipapi.Event{}, false, err
	}
	terminalCommand := sdkterminal.CommandNormalFinish
	terminalIntent := IntentSuccess
	if decision.Decision.Kind == terminaldecision.DecisionSurfaceFailure {
		terminalCommand = sdkterminal.CommandPartialError
		terminalIntent = IntentSurfacedFailure
	}
	// Token accounting and observe must happen inside the attempt terminal winner
	// so concurrent losers wait for the winner's effects via streamTerminal.
	var preparedUsageEv aipapi.Event
	var preparedAuthorityEv aipapi.Event
	var preparedOK bool
	var preparedErr error
	// A prepared pending completion publication owns the final-client observation of
	// its own staged sequence. Only that deferral is private here: attempt
	// accounting, usage authority, billing, and teardown ownership stay exactly
	// where they already are, and the attempt-owned final-stream observer is never
	// moved onto the pipeline or reopened.
	//
	// The expected candidate is the one the receive route captured BEFORE this
	// terminal call started, and it stays expected for the whole call: if the
	// claim, the reservation, the activation, or the physical drain later finds it
	// gone, that is a withdrawal of an expected publication and never the optional
	// "nothing to publish" outcome.
	publishPending := hooks.publishesPendingCompletion(hooks.expectedPrepared) &&
		terminalIntent == IntentSuccess
	evidence := attemptEvidence{
		Command:                     terminalCommand,
		LegOutcome:                  usageevidence.LegOutcomeWinner,
		Usage:                       aipapi.Event{},
		ObsOutcome:                  response.OutcomeSuccessReleased,
		TraceID:                     request.traceID,
		ALegID:                      request.aLegID,
		Snapshot:                    &snapshot,
		RecordOutcome:               aipapi.AttemptSuccess,
		StartedAt:                   attempt.accountingStartedAt(),
		StreamFallback:              p.billingEvidenceFallback(),
		BillingState:                request.billingState,
		BillingCallID:               request.billingCallID,
		Committed:                   t.committed(),
		ObserveEvent:                &ev,
		DeferFinalStreamObservation: publishPending,
		AuthorityPrepare: func(cctx context.Context) (aipapi.Event, aipapi.Event, bool, error) {
			if !t.claimAccountingFinalization() {
				return aipapi.Event{}, aipapi.Event{}, false, nil
			}
			usageEv, ok, err := t.finalizeTokenAccounting(cctx, attempt, ev, request, p)
			if err != nil {
				t.unclaimAccountingFinalization()
				return aipapi.Event{}, aipapi.Event{}, false, err
			}
			authorityEv := p.lastAuthorityUsageSnapshot()
			if authorityEv.Kind == "" {
				authorityEv = usageEv
			}
			preparedUsageEv = usageEv
			preparedAuthorityEv = authorityEv
			preparedOK = ok
			// Do not settle request authority here; it will be done in the request winner
			return usageEv, authorityEv, ok, nil
		},
	}
	if terminalIntent == IntentSurfacedFailure {
		evidence.LegOutcome = usageevidence.LegOutcomeFailed
		evidence.ObsOutcome = response.OutcomeFailed
		evidence.RecordOutcome = aipapi.AttemptSurfacedFailure
		// A surfaced failure never publishes a private result. Invalidate the
		// candidate BEFORE any success return on this path, so no lexical candidate
		// survives for a later finish route or a later attempt to pick up. A normal
		// accepted owner is untouched here: it retains its candidate until the
		// physical finish.
		p.discardPendingCompletion()
	}
	resOuter := attempt.TerminalizeAttempt(ctx, terminalIntent, evidence)
	if !resOuter.Result.Won {
		t.abandonDeferredFinalObservation(p, attempt)
		return aipapi.Event{}, false, terminalLossError(resOuter.Result)
	}
	if resOuter.Result.Err != nil {
		t.abandonDeferredFinalObservation(p, attempt)
		return preparedUsageEv, preparedOK, resOuter.Result.Err
	}
	// Use the prepared authorityEv for request settlement; if Prepare didn't run (loser), use evidence.Usage
	authorityEv := preparedAuthorityEv
	if authorityEv.Kind == "" {
		authorityEv = preparedUsageEv
	}
	// Also need to handle the case where Prepare error was already propagated
	if preparedErr != nil {
		return preparedUsageEv, preparedOK, preparedErr
	}
	// Request authority settlement for non-thinker is now handled inside the attempt terminal winner via typed seams;
	// for thinker the attempt-only path keeps request open, so we still need to handle request-side effects here if needed.
	// However billing leg is now owned by the attempt terminal winner, so we only handoff the call closure here.
	var r terminal.Result
	if t.isInterleavedThinker() {
		r = terminal.Result{Won: true, Outcome: terminal.Outcome{Command: terminalCommand}, State: sdkterminal.StateReleased}
		if publishPending {
			// An interleaved thinker terminalizes only its own B-leg, so its accepted
			// normal publication runs here under the EXISTING bounded cleanup budget:
			// the attempt-effects context must not be retained after
			// TerminalizeAttempt returns. The internal thinker canonical stream
			// consumes the result through the existing wrapper path; request
			// NormalFinish, customer request settlement, and billing handoff stay with
			// the executor that owns them.
			cleanupCtx, cleanupCancel := cleanupContext(ctx, attemptCleanupTimeout(attempt))
			stageErr := t.stagePendingAcceptedNormal(cleanupCtx, hooks, attempt, p, preparedUsageEv, preparedOK)
			if stageErr == nil {
				// Activation shares the stage's bounded budget, so a withdrawal at this
				// boundary closes the deferred observer inside the same one.
				stageErr = t.activatePendingPublication(cleanupCtx, p, hooks)
			}
			cleanupCancel()
			if stageErr != nil {
				r = terminal.Result{Err: stageErr}
			}
		}
	} else {
		r = t.claimRequestTerminal(ctx, terminalCommand, snapshot, func(cctx context.Context, _ terminal.Outcome) error {
			var stageErr error
			if publishPending {
				// The winning request owner supplies its OWN existing bounded cleanup
				// context, so recorder, observer, and PTC publication inherit exactly
				// the request terminal's budget.
				stageErr = t.stagePendingAcceptedNormal(cctx, hooks, attempt, p, preparedUsageEv, preparedOK)
				if stageErr != nil {
					// A rejected publication preflight stops settlement and handoff: no
					// customer authority is settled and no billing call is handed off
					// for a batch that will never be released.
					return stageErr
				}
				// The SAME full expected-publication fence that gated staging and will
				// gate activation is consulted BEFORE the first not-yet-admitted success
				// effect. A client cancellation, an authoritative A-leg cause, a closed
				// publication window, or a replaced origin that wins after the batch was
				// staged must stop settlement here instead of charging a customer for a
				// publication that can never be released.
				if !hooks.pendingPublicationFenceOpen(t) {
					p.abandonOwnedPendingPublication(cctx, hooks.expectedOrigin)
					return errPendingPublicationWithdrawn
				}
			}
			customer, _ := p.stagedCustomerUsage()
			if err := t.settleRequestAuthorityWithCustomerOverride(cctx, authorityEv, customer, request, p); err != nil {
				return err
			}
			if publishPending {
				// Settlement above is an ALREADY-ADMITTED private effect: it may complete
				// and is never rejected for existing, because nothing here can undo it.
				// The next not-yet-admitted effect is the normal-success billing handoff,
				// so the SAME fence is consulted again immediately before it. A withdrawal
				// that landed during settlement must therefore be observed here, and only
				// the owned captured state is disposed, conservatively and exactly once,
				// through the existing withdrawal cleanup.
				if !hooks.pendingPublicationFenceOpen(t) {
					p.abandonOwnedPendingPublication(cctx, hooks.expectedOrigin)
					return errPendingPublicationWithdrawn
				}
			}
			return t.handoffBillingTurn(cctx, request, terminalCommand)
		})
		if r.Won && r.Err == nil && publishPending {
			// The private drain becomes deliverable only after the owning effects
			// reported a real winner and the live origin/publication fence still
			// permits release. Activation runs after the request terminal released
			// its own effects context, so it uses the attempt's existing bounded
			// cleanup budget rather than an unbounded or retained context.
			activateCtx, activateCancel := cleanupContext(ctx, attemptCleanupTimeout(attempt))
			activateErr := t.activatePendingPublication(activateCtx, p, hooks)
			activateCancel()
			if activateErr != nil {
				r.Err = activateErr
			}
		}
	}
	if !r.Won {
		// Another exit path already terminalized; surface cancel/error consistently.
		t.abandonDeferredFinalObservation(p, attempt)
		return aipapi.Event{}, false, terminalLossError(r)
	}
	if r.Err != nil {
		t.abandonDeferredFinalObservation(p, attempt)
		return preparedUsageEv, preparedOK, r.Err
	}
	if terminalIntent == IntentSuccess && !t.isInterleavedThinker() {
		// The surviving attempt's clean terminal won here. A clean terminal with
		// no produced visible output is the degenerate empty turn operators need
		// a signal for; truncated, refusal, and error terminals never count, and
		// reasoning-only output still counts as empty.
		t.observeEmptyVisibleTurn(ctx, request, attempt, ev)
	}
	return preparedUsageEv, preparedOK, nil
}

// attemptCleanupTimeout reports the established bounded cleanup budget of one
// attempt's authority lifecycle, falling back to the existing authority default.
func attemptCleanupTimeout(attempt *attemptSession) time.Duration {
	if attempt != nil && attempt.authority.control != nil {
		attempt.authority.control.mu.Lock()
		timeout := attempt.authority.control.state.cleanupTimeout
		attempt.authority.control.mu.Unlock()
		if timeout > 0 {
			return timeout
		}
	}
	return defaultAuthorityCleanupTimeout
}

// stagePendingAcceptedNormal reserves and stages this turn's one private pending
// completion publication through the accepted normal terminal path.
//
// The pipeline claims the publication, so a repeated terminal call, a competing
// request command, and the generic losing GateReplacement effect exception can
// never publish it twice. The claim must return the EXACT expected candidate: a
// lost or replaced claim is a real withdrawal and is returned as one, so the caller
// stops settlement, handoff, and activation instead of failing the terminal
// truthfully on a publication that will never be released. A foreign candidate is
// neither staged nor discarded.
func (t *turnTerminal) stagePendingAcceptedNormal(
	ctx context.Context,
	hooks finishAuthorityInput,
	attempt *attemptSession,
	p *responsePipeline,
	usage aipapi.Event,
	usageOK bool,
) error {
	if hooks.acceptedNormal == nil || p == nil {
		return nil
	}
	expected := hooks.expectedPrepared
	if expected == nil {
		return nil
	}
	prepared, claimed := p.takePendingPublication()
	if !claimed || prepared == nil {
		// The expected publication lost its claim, so the caller must stop
		// settlement and billing handoff rather than continue for a batch that will
		// never be released.
		return errPendingPublicationWithdrawn
	}
	if prepared != expected {
		// A DIFFERENT candidate is live. It is neither claimed nor discarded here:
		// this caller reports its own expected publication as withdrawn and leaves
		// the foreign reservation to its real owner.
		return errPendingPublicationWithdrawn
	}
	return hooks.acceptedNormal(ctx, usage, usageOK)
}

// activatePendingPublication releases the staged batch for delivery. It runs only
// after the owning effects reported Won with no error and only while the live
// origin/publication fence still permits release, so a Close that already closed
// the window can never make the batch deliverable again.
//
// Activation is pinned to the EXACT expected candidate and expected origin the
// terminal captured before it started, and the response owner re-checks under
// [responsePipeline.eventsMu] that this same candidate is still reserved, still
// carries its deferred observer finish, is still staged, and is not activated yet.
// A refused activation is a withdrawal the caller propagates; its bool is never
// ignored. Cleanup disposes the CAPTURED expected identity, never an origin
// snapshot chosen after the failure, so a foreign owner can never have its own
// publication erased by a foreign activation.
//
// ctx is the winning owner's existing bounded cleanup context, so a withdrawal at
// this boundary closes the deferred observer inside the same budget as the rest of
// the owning effects. It opens no new one.
func (t *turnTerminal) activatePendingPublication(ctx context.Context, p *responsePipeline, hooks finishAuthorityInput) error {
	if p == nil {
		return nil
	}
	// Activation uses the FULL lexical live fence, not the publication window
	// alone: the caller context, the shared A-leg cause, the frozen origin, and the
	// window. A Close, a client cancellation, an authoritative A-leg cause, or a
	// replaced attempt between staging and activation must stop the release here.
	if !hooks.pendingPublicationFenceOpen(t) {
		p.abandonOwnedPendingPublication(ctx, hooks.expectedOrigin)
		return errPendingPublicationWithdrawn
	}
	if !p.activateReservedPendingPublication(hooks.expectedPrepared, hooks.expectedOrigin) {
		p.abandonOwnedPendingPublication(ctx, hooks.expectedOrigin)
		return errPendingPublicationWithdrawn
	}
	return nil
}

// abandonDeferredFinalObservation finishes the attempt-owned final-stream
// observer conservatively when a prepared pending publication never reached its
// accepted normal publication. Reservation and staging are NOT full acceptance:
// a staged-but-unactivated batch is discarded whole so no queued event and no
// already-successful observer can survive a rejected terminal effect. The
// observer is never reopened afterwards.
//
// The observer's existence controls ONLY the Finish. The owned reserved, staged,
// and retained publication state is always discarded, so a settlement, handoff, or
// attempt/request loser failure on an attempt with no observer leaves no phantom
// release effect behind either.
func (t *turnTerminal) abandonDeferredFinalObservation(p *responsePipeline, attempt *attemptSession) {
	if t == nil || p == nil || attempt == nil {
		return
	}
	if p.pendingPublicationActive() {
		// The activated drain owns the observer finish at its real delivery
		// boundary, including a conservative finish on an interrupted partial drain.
		return
	}
	// Drop the owned reserved, staged, and retained state, and close the observer
	// conservatively exactly once. The observer's existence gates only the Finish,
	// never the state, so an attempt without one leaves no phantom release effect.
	p.withdrawPendingCompletion(attempt)
}

// finishPendingObservationConservatively closes the deferred final-stream observer
// exactly once under the bounded cleanup budget, never reopening it.
func (p *responsePipeline) finishPendingObservationConservatively(attempt *attemptSession) {
	if p == nil || attempt == nil || attempt.finalStreamObs == nil {
		return
	}
	cleanupCtx, cancel := cleanupContext(context.Background(), attemptCleanupTimeout(attempt))
	defer cancel()
	p.finishFinalStreamObservation(cleanupCtx, attempt, response.OutcomeFailed)
}

// settleRequestAuthorityWithFrontendEgress emits the frontend-egress fact for the
// delivered/committed customer usage and passes that fact into request settlement
// for non-money quota/lease coordination (4.2). Monetary rating is exclusively a
// post-usage current-record concern and is never attached here. Durable-pending and
// durable-intent-rejected errors are returned so stream terminal effects fail
// truthfully (Phase 4.5 / D9).
func (t *turnTerminal) settleRequestAuthorityWithFrontendEgress(ctx context.Context, usageEv aipapi.Event, request requestTerminalFacts, p *responsePipeline) error {
	return t.settleRequestAuthorityWithCustomerOverride(ctx, usageEv, aipapi.Event{}, request, p)
}

// settleRequestAuthorityWithCustomerOverride is the one narrow seam that lets a
// winning request owner settle from a customer usage event it already holds.
//
// It exists only for the accepted pending completion publication, whose customer
// quantity is previewed privately from the accepted post-hook, post-gate candidate
// BEFORE the batch is released. Passing that same event here keeps settlement and
// the client from ever disagreeing. A zero override preserves the existing
// reconstruction from released content exactly.
func (t *turnTerminal) settleRequestAuthorityWithCustomerOverride(
	ctx context.Context,
	usageEv aipapi.Event,
	customerOverride aipapi.Event,
	request requestTerminalFacts,
	p *responsePipeline,
) error {
	if t == nil {
		return nil
	}
	if !p.markCustomerSettled() {
		return nil
	}
	customerEv := customerOverride
	if customerEv.Kind == "" {
		customerEv = p.resolveCustomerUsageForTerminal(ctx, usageEv, request)
	}
	var egressFacts []metering.Fact
	var fact metering.Fact
	var persisted bool
	if t.emitFrontendEgress != nil {
		fact, persisted = t.emitFrontendEgressMeteringFact(ctx, request.traceID, customerEv)
	}
	if persisted {
		egressFacts = []metering.Fact{fact}
	} else if t.meteringRecorderPresent {
		// Required settlement evidence was not persisted. Keep request authority
		// open so a later terminal/reconciliation attempt can retry the append.
		p.unmarkCustomerSettled()
		return fmt.Errorf("%w: frontend egress fact not persisted", terminalworkapp.ErrDurableIntentRejected)
	}
	if t.settleRequestAuthority == nil {
		return nil
	}
	// Monetary rating is exclusively a post-usage current-record concern. Runtime
	// settlement receives only the non-money authority/egress evidence.
	err := t.settleRequestAuthority(ctx, egressFacts)
	if request.requestAuth != nil && !request.requestAuth.Settled {
		// Provider settlement failed: keep customer once-only open for retry.
		p.unmarkCustomerSettled()
	}
	return err
}

// Customer FE quantities are reconstructed by responsePipeline from released
// accumulator content through StreamUsage.Reconstruct / CountOutput. Provider-
// preferring usageEv scopes are never imported; they only seed an empty shell
// when no customer evidence can be reconstructed.
// reconstructCustomerUsageForResponse reconstructs client-visible usage from
// released response evidence. It is a callback target for responsePipeline so
// provider/runtime adapters remain outside that owner.
func reconstructCustomerUsageForResponse(ctx context.Context, streamUsage *accountingstream.Reconstructor, log *slog.Logger, facts recvTurnFacts, attempt *attemptSession, text string, events []aipapi.Event) aipapi.Event {
	if streamUsage == nil {
		return aipapi.Event{}
	}
	call := facts.baseline
	var backend, model string
	if attempt != nil {
		backend = strings.TrimSpace(attempt.cand.Primary.Backend)
		model = strings.TrimSpace(attempt.cand.Primary.Model)
	}
	if holder := facts.metering; holder != nil && holder.FrontendIngress != nil && holder.FrontendIngress.Call.ID != "" {
		call = holder.FrontendIngress.Call
	}
	result, err := streamUsage.Reconstruct(ctx, accountingstream.Input{
		Backend: backend, Model: model, Call: call, OutputText: text, Events: events,
	})
	if err != nil && log != nil {
		log.DebugContext(ctx, "customer stream usage reconstruction", "error", err)
	}
	out := customerPlaneUsageEvent(mergeUsageEventsForClient(result.Events, true))
	if out.Kind == "" {
		return aipapi.Event{}
	}
	return applyFrontendIngressInput(facts.metering, out)
}

func applyFrontendIngressInput(holder *checkpoint.RequestHolder, ev aipapi.Event) aipapi.Event {
	if holder == nil || holder.FrontendIngress == nil {
		return ev
	}
	in, ok := checkpoint.QuantityComponentValue(holder.FrontendIngress.Public.Quantities, metering.ComponentInputToken)
	if !ok {
		return ev
	}
	ev.InputTokens = int(in)
	if len(ev.UsageScopes) > 0 {
		scopes := append([]aipapi.ScopedUsageDelta(nil), ev.UsageScopes...)
		for i := range scopes {
			if scopes[i].Accounting.Plane == aipapi.UsagePlaneClientVisible || scopes[i].Accounting.Plane == "" {
				scopes[i].InputTokens = int(in)
				scopes[i].TotalTokens = scopes[i].InputTokens + scopes[i].OutputTokens
			}
		}
		ev.UsageScopes = scopes
	}
	ev.TotalTokens = ev.InputTokens + ev.OutputTokens
	return ev
}

func mergeUsageEvents(events []aipapi.Event) aipapi.Event {
	return mergeUsageEventsForClient(events, false)
}

func authorityUsageEvent(events []aipapi.Event) aipapi.Event {
	authoritative := authoritativeProviderUsageEvents(events)
	if len(authoritative) > 0 {
		return mergeUsageEventsForClient(coalesceAuthoritativeProviderSnapshots(authoritative), false)
	}
	return mergeUsageEvents(events)
}

// coalesceAuthoritativeProviderSnapshots keeps one source snapshot per stable
// provider event key for the authority projection. Provider adapters use a
// stable key for a cumulative stream source, while the host-only V2 path keeps
// every immutable revision for durable replay. Overlaying only explicitly
// present fields preserves start/cache fields when a later delta contains only
// output and prevents a terminal cumulative snapshot from being counted again.
// Events without a key, or with scoped usage, retain the ordinary additive
// projection because their source semantics are not known at this boundary.
func coalesceAuthoritativeProviderSnapshots(events []aipapi.Event) []aipapi.Event {
	out := make([]aipapi.Event, 0, len(events))
	indices := make(map[string]int, len(events))
	for _, event := range events {
		key := authoritativeProviderSnapshotKey(event)
		if key == "" {
			out = append(out, event)
			continue
		}
		if index, ok := indices[key]; ok {
			out[index] = mergeProviderUsageSnapshot(out[index], event)
			continue
		}
		indices[key] = len(out)
		out = append(out, event)
	}
	return out
}

func authoritativeProviderSnapshotKey(event aipapi.Event) string {
	if event.Kind != aipapi.EventUsageDelta || len(event.UsageScopes) != 0 {
		return ""
	}
	key := strings.TrimSpace(event.Accounting.DedupeKey)
	if key == "" {
		return ""
	}
	return strings.Join([]string{
		key,
		string(event.Accounting.Plane),
		string(event.Accounting.Source),
		string(event.Accounting.Authority),
	}, "\x00")
}

func mergeProviderUsageSnapshot(previous, next aipapi.Event) aipapi.Event {
	out := previous
	copyCounter := func(present bool, value int, target *int, targetPresent *bool) {
		if !present {
			return
		}
		*target = value
		*targetPresent = true
	}
	copyCounter(next.UsagePresence.InputTokens, next.InputTokens, &out.InputTokens, &out.UsagePresence.InputTokens)
	copyCounter(next.UsagePresence.OutputTokens, next.OutputTokens, &out.OutputTokens, &out.UsagePresence.OutputTokens)
	copyCounter(next.UsagePresence.CacheReadTokens, next.CacheReadTokens, &out.CacheReadTokens, &out.UsagePresence.CacheReadTokens)
	copyCounter(next.UsagePresence.CacheWriteTokens, next.CacheWriteTokens, &out.CacheWriteTokens, &out.UsagePresence.CacheWriteTokens)
	copyCounter(next.UsagePresence.ReasoningTokens, next.ReasoningTokens, &out.ReasoningTokens, &out.UsagePresence.ReasoningTokens)
	copyCounter(next.UsagePresence.TotalTokens, next.TotalTokens, &out.TotalTokens, &out.UsagePresence.TotalTokens)
	if next.CostPresent {
		out.CostNanoUnits = next.CostNanoUnits
		out.CostPresent = true
		out.Currency = next.Currency
		out.CostSource = next.CostSource
	}
	if next.RawUsageJSON != "" {
		out.RawUsageJSON = next.RawUsageJSON
	}
	if next.Accounting.ProviderAccountKey != "" {
		out.Accounting.ProviderAccountKey = next.Accounting.ProviderAccountKey
	}
	if next.Accounting.ProviderRequestID != "" {
		out.Accounting.ProviderRequestID = next.Accounting.ProviderRequestID
	}
	if next.Accounting.ProviderChargeID != "" {
		out.Accounting.ProviderChargeID = next.Accounting.ProviderChargeID
	}
	if next.Accounting.ServiceContext != "" {
		out.Accounting.ServiceContext = next.Accounting.ServiceContext
	}
	if next.Accounting.Tokenizer != (aipapi.TokenizerRef{}) {
		out.Accounting.Tokenizer = next.Accounting.Tokenizer
	}
	return out
}

// authoritativeProviderUsageEvents keeps only provider scopes whose metadata
// proves billable authority. Costs are retained only when the event-level
// metadata makes their scope unambiguous; a mixed scoped event otherwise
// contributes token counters but not an ambiguous provider cost.
func authoritativeProviderUsageEvents(events []aipapi.Event) []aipapi.Event {
	out := make([]aipapi.Event, 0, len(events))
	for _, ev := range events {
		if ev.Kind != aipapi.EventUsageDelta {
			continue
		}
		if len(ev.UsageScopes) == 0 {
			if authoritativeProviderAccounting(ev.Accounting) {
				out = append(out, ev)
			}
			continue
		}
		filtered := ev
		filtered.UsageScopes = nil
		explicitScopeMetadata := false
		for _, scope := range ev.UsageScopes {
			if authoritativeProviderAccounting(scope.Accounting) {
				filtered.UsageScopes = append(filtered.UsageScopes, scope)
				continue
			}
			if scope.Accounting != (aipapi.UsageAccountingMetadata{}) {
				explicitScopeMetadata = true
			}
		}
		// A provider may put one event-level accounting record around otherwise
		// unannotated scopes. Accept those scopes only when none carries an
		// explicit conflicting classification; never let a local/client scope
		// ride along with an authoritative provider scope.
		if len(filtered.UsageScopes) == 0 && authoritativeProviderAccounting(ev.Accounting) && !explicitScopeMetadata {
			for _, scope := range ev.UsageScopes {
				scope.Accounting = ev.Accounting
				scope.UsagePresence = scope.UsagePresence.Union(ev.UsagePresence)
				filtered.UsageScopes = append(filtered.UsageScopes, scope)
			}
		}
		if len(filtered.UsageScopes) == 0 {
			continue
		}
		if !authoritativeProviderAccounting(ev.Accounting) {
			filtered.CostNanoUnits = 0
			filtered.Currency = ""
			filtered.CostSource = ""
			filtered.CostPresent = false
		}
		out = append(out, filtered)
	}
	return out
}

func mergeUsageEventsForClient(events []aipapi.Event, skipProviderBillable bool) aipapi.Event {
	out := aipapi.Event{UsageScopes: []aipapi.ScopedUsageDelta{}}
	found := false
	for _, ev := range events {
		if ev.Kind != aipapi.EventUsageDelta {
			continue
		}
		included := false
		if len(ev.UsageScopes) > 0 {
			for _, scope := range ev.UsageScopes {
				if skipProviderBillable && scope.Accounting.Plane == aipapi.UsagePlaneProviderBillable {
					continue
				}
				out.UsageScopes = append(out.UsageScopes, scope)
				included = true
			}
		} else {
			if skipProviderBillable && ev.Accounting.Plane == aipapi.UsagePlaneProviderBillable {
				continue
			}
			out.UsageScopes = append(out.UsageScopes, aipapi.ScopedUsageDelta{
				InputTokens:      ev.InputTokens,
				OutputTokens:     ev.OutputTokens,
				CacheReadTokens:  ev.CacheReadTokens,
				CacheWriteTokens: ev.CacheWriteTokens,
				ReasoningTokens:  ev.ReasoningTokens,
				TotalTokens:      ev.TotalTokens,
				UsagePresence:    ev.UsagePresence,
				Accounting:       ev.Accounting,
			})
			included = true
		}
		if !included {
			continue
		}
		found = true
		out.CostNanoUnits += ev.CostNanoUnits
		out.CostPresent = out.CostPresent || ev.CostPresent
		if ev.Currency != "" {
			out.Currency = ev.Currency
		}
		if ev.CostSource != "" {
			out.CostSource = ev.CostSource
		}
		if ev.RawUsageJSON != "" {
			out.RawUsageJSON = ev.RawUsageJSON
		}
	}
	if len(out.UsageScopes) > 0 {
		projectAggregatedUsageCounters(&out)
	}
	if !found {
		return aipapi.Event{}
	}
	out.Kind = aipapi.EventUsageDelta
	return out
}

// projectAggregatedUsageCounters copies per-unit totals from every included
// scope onto the event top-level fields used by settlement. Presence is the
// union across scopes so a unit reported only in a later scope still settles.
func projectAggregatedUsageCounters(out *aipapi.Event) {
	if out == nil || len(out.UsageScopes) == 0 {
		return
	}
	var (
		input, output, cacheRead, cacheWrite, reasoning, total int
		presence                                               aipapi.UsagePresence
		accounting                                             aipapi.UsageAccountingMetadata
		haveAccounting                                         bool
	)
	for _, scope := range out.UsageScopes {
		input += scope.InputTokens
		output += scope.OutputTokens
		cacheRead += scope.CacheReadTokens
		cacheWrite += scope.CacheWriteTokens
		reasoning += scope.ReasoningTokens
		total += scope.TotalTokens
		presence = presence.Union(scope.UsagePresence)
		if !haveAccounting || (!authoritativeProviderAccounting(accounting) && authoritativeProviderAccounting(scope.Accounting)) {
			accounting = scope.Accounting
			haveAccounting = true
		}
	}
	out.InputTokens = input
	out.OutputTokens = output
	out.CacheReadTokens = cacheRead
	out.CacheWriteTokens = cacheWrite
	out.ReasoningTokens = reasoning
	out.TotalTokens = total
	out.UsagePresence = presence
	out.Accounting = accounting
}

func tokenAccountingHasProviderUsage(events []aipapi.Event) bool {
	for _, ev := range events {
		if ev.Kind != aipapi.EventUsageDelta {
			continue
		}
		if ev.Accounting.Plane == aipapi.UsagePlaneProviderBillable {
			return true
		}
		for _, scope := range ev.UsageScopes {
			if scope.Accounting.Plane == aipapi.UsagePlaneProviderBillable {
				return true
			}
		}
	}
	return false
}

func (t *turnTerminal) recordPartialTokenAccounting(ctx context.Context, attempt *attemptSession, reason string, err error, request requestTerminalFacts, p *responsePipeline) {
	if t == nil || attempt == nil {
		return
	}
	// Keep non-money attempt/request coordination only. Do not write the legacy
	// token ledger or settle monetary exposure from stream usage.
	usageEv := p.operatorUsageForFinalize()
	attempt.authority.Settle(ctx, usagelimit.SettlementKindPartial, usageEv, false)
	attempt.authority.ApplyUnreservedUsage(ctx, usagelimit.SettlementKindPartial, usageEv)
	t.emitBackendEgressMeteringFactForAttempt(ctx, attempt, metering.AttemptOutcomeFailed, metering.SurfacedYes, usageEv)
	if t.committed() {
		_ = t.settleRequestAuthorityWithFrontendEgress(ctx, p.usageEvidenceOrEmpty(), request, p)
	}
}

func tokenAccountingUsageEvents(events []aipapi.Event) []aipapi.Event {
	out := []aipapi.Event{}
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			out = append(out, ev)
		}
	}
	return out
}
