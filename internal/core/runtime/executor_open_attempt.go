package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	coretraffic "github.com/aiproxer/aiproxer/internal/core/traffic"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type requestFacts struct {
	recvTurnFacts
	bus                 *hooks.Bus
	aScope              *leglifecycle.ALeg
	suppressThinker     bool
	suppressVisibleMemo bool
	// structuralCause is the request's publication claim set for structural
	// causes, shared with every other attempt of the request through its
	// recovery controller. See structuralCauseClaims.
	structuralCause *structuralCauseClaims
}

func (rf requestFacts) isWire() bool {
	return rf.wirePayload != nil && rf.wirePayload.src != nil
}

type openMode int

const (
	openModeInitial openMode = iota
	openModeRetry
	openModeGuardContinuation
)

type candidatePlan struct {
	cand            routing.AttemptCandidate
	nextCycle       *interleavedstate.CycleState
	stickyBackendID string
	stickyBinding   bool
	parallel        bool
	parentCtx       context.Context
}

type rejectionKind int

const (
	rejectNone rejectionKind = iota
	rejectExclude
	rejectAdmission
	rejectContextLimit
	rejectParallel
)

type candidateRejection struct {
	kind   rejectionKind
	detail any
}

type candidateEvaluationOutcome struct {
	accepted          bool
	rejection         candidateRejection
	attempt           aipapi.Call
	facts             modelcatalog.EffectiveFacts
	preflightDecision accountingpreflight.Decision
	admitOut          candidateAdmissionOutcome
	turn              InterleavedTurn
}
type openedAttempt struct {
	ready       *readyAttempt
	interleaved interleavedstate.State
	turn        InterleavedTurn
}
type attemptTx struct {
	e                *Executor
	reqFacts         requestFacts
	routeFacts       routeFacts
	cand             routing.AttemptCandidate
	bleg             b2bua.BLegRecord
	authState        attemptAuthorityState
	authLifecycle    authorityLifecycle
	stream           aipapi.ManagedEventStream
	streamDisposed   bool
	session          *attemptSession
	registered       bool
	openInvoked      bool
	openStartedAt    time.Time
	budget           *attemptBudget
	budgetAcquired   bool
	backendAttempted bool
	completed        bool
	failures         *candidateFailureHistory
	// structuralCause is the proxy-owned, content-free cause of a structural
	// change the proxy applied to the candidate request. It is read from the
	// committed candidate call, so it names only a change that reached the
	// provider-bound request.
	structuralCause string
	// promptCache carries the content-free observations this attempt feeds to
	// the prompt-cache collapse diagnostic: the per-item seam vector, the
	// provider-bound instruction identity, and the process-owned request launch
	// ordinal. The identity names only the request the backend actually
	// receives, per B-leg; the ordinal orders concurrent requests.
	promptCache promptCacheObservations
	// switchIdentity is the content-free downstream identity of the committed
	// B-leg. It rides every usage sample this attempt emits; the collapse
	// diagnostic owns comparison against its healthy reference.
	switchIdentity attemptSwitchIdentity

	launchPermit *leglifecycle.LaunchPermit

	// attempt-local resources
	boundary              *coremetering.BoundaryAccumulator
	accounting            attemptAccountingTracker
	controlTool           *controlToolActivation
	toolFinal             *toolCallAssembler
	promptCacheSource     promptcache.ObservationSource
	promptCacheController promptcache.Controller
	finalStreamObs        *extensions.FinalStreamObservationSession
	recordAttemptLoggedFn func(context.Context, recordAttemptParams, diag.AttrOpts)
}

func rollbackCommandToIntent(cmd sdkterminal.Command) attemptTerminalIntent {
	switch cmd {
	case sdkterminal.CommandParallelLoser:
		return IntentParallelLoser
	case sdkterminal.CommandBackendOpenFailure:
		return IntentOpenReadinessFailure
	case sdkterminal.CommandPreBackendDenial:
		return IntentOpenReadinessFailure
	case sdkterminal.CommandCancel, sdkterminal.CommandClose:
		return IntentCancellation
	case sdkterminal.CommandTimeout:
		return IntentTimeout
	case sdkterminal.CommandSwallowedAttempt:
		return IntentSwallowedFailure
	default:
		return IntentSurfacedFailure
	}
}

func (e *Executor) newAttemptSession(in attemptSessionInput) *attemptSession {
	if e != nil {
		in.emitBackendEgressFn = e.emitBackendEgressMeteringFact
		in.appendBillingLegFn = func(cctx context.Context, bleg b2bua.BLegRecord, primary routing.Primary, started, finished time.Time, outcome usageevidence.LegOutcome) {
			e.appendIndependentTerminalLeg(cctx, in.billingCallState, bleg.ALegID, bleg, primary, started, finished, outcome)
		}
		in.finalizeBilling = e.callFinalizeBilling
		in.finalizeBillingV2 = e.callFinalizeBillingResult
		in.billingEnabled = e.billingEnabled
		in.operatorRateRef = e.operatorRateRef
		in.billingWorkload = e.billingWorkloadIdentityForALeg
		in.observeBillingLeg = e.observeBillingLeg
		in.appendBillingLeg = e.appendIndependentCallLeg
		in.observationSink = e.MeteringObservationSink
	}
	sess := newAttemptSession(in)
	if e != nil {
		sess.now = e.now
		sess.recordCancellationFn = e.recordCancellation
		sess.appendBillingLegStrict = e.appendIndependentCallLegStrict
		if e.SystemPromptDigestEnabled && !e.SystemPromptDigestKey.IsZero() {
			sess.switchDigestKey = e.SystemPromptDigestKey
		}
	}
	return sess
}

func (tx *attemptTx) createSession() *attemptSession {
	if tx == nil {
		return nil
	}
	if tx.session != nil {
		return tx.session
	}
	tx.session = tx.e.newAttemptSession(withBillingStoreIDOnAttempt(attemptSessionInput{
		inner:                 tx.stream,
		streamDisposed:        tx.streamDisposed,
		bleg:                  tx.bleg,
		cand:                  tx.cand,
		authority:             tx.authLifecycle,
		aScope:                tx.reqFacts.aScope,
		traceID:               tx.reqFacts.traceID,
		billingCallID:         tx.reqFacts.billingCallID,
		submissionID:          tx.reqFacts.submissionID,
		billingCallState:      tx.reqFacts.billingCallState,
		accounting:            tx.accounting,
		boundary:              tx.boundary,
		requestID:             attemptRequestID(tx.reqFacts),
		structuralCause:       tx.structuralCause,
		promptCache:           tx.promptCache,
		boundaryScope:         tx.reqFacts.recvViews.Scope,
		controlTool:           tx.controlTool,
		toolFinal:             tx.toolFinal,
		promptCacheSource:     tx.promptCacheSource,
		promptCacheController: tx.promptCacheController,
		finalStreamObs:        tx.finalStreamObs,
		recordAttemptLoggedFn: tx.recordAttemptLoggedFn,
	}, tx.reqFacts.billingStoreID))
	tx.session.switchIdentity = tx.switchIdentity
	return tx.session
}

func attemptRequestID(facts requestFacts) string {
	if facts.wirePayload != nil && strings.TrimSpace(facts.wirePayload.requestID) != "" {
		return strings.TrimSpace(facts.wirePayload.requestID)
	}
	if requestID := strings.TrimSpace(facts.baseline.ID); requestID != "" {
		return requestID
	}
	return strings.TrimSpace(facts.traceID)
}

func (e *Executor) createSessionForParallelLeg(leg *parallelLeg, aScope *leglifecycle.ALeg) *attemptSession {
	if leg == nil {
		return nil
	}
	if leg.tx != nil {
		return leg.tx.createSession()
	}
	boundary := leg.boundary
	if boundary == nil {
		boundary = e.newLocalBoundaryAccumulator()
	}
	billingCallID := leg.billingCallID
	if billingCallID == "" && leg.billingCallState != nil {
		billingCallID = leg.billingCallState.callID
	}
	return e.newAttemptSession(withBillingStoreIDOnAttempt(attemptSessionInput{
		inner:            leg.stream,
		bleg:             leg.bleg,
		cand:             leg.cand,
		authority:        leg.authority,
		aScope:           aScope,
		requestID:        leg.requestID,
		boundaryScope:    leg.boundaryScope,
		billingCallID:    billingCallID,
		submissionID:     billingSubmissionID(leg.billingCallState),
		billingCallState: leg.billingCallState,
		boundary:         boundary,
		recordAttemptLoggedFn: func(cctx context.Context, p recordAttemptParams, attrs diag.AttrOpts) {
			if e != nil {
				e.recordAttemptLogged(cctx, p, attrs)
			}
		},
	}, leg.storeID))
}

func (tx *attemptTx) HandoffReady(pending pendingSelectionEffects) *readyAttempt {
	if tx == nil {
		panic("nil attemptTx handoff")
	}
	if tx.completed {
		panic("double handoff")
	}
	if tx.e == nil {
		panic("nil executor in handoff")
	}
	tx.completed = true
	return newReadyAttempt(tx.createSession(), pending)
}

func (tx *attemptTx) rollback(ctx context.Context, cmd sdkterminal.Command, evidence attemptEvidence) attemptTerminalResult {
	if tx == nil || tx.completed {
		return attemptTerminalResult{}
	}
	tx.abortLaunchPermit()
	if tx.budgetAcquired && tx.budget != nil && !tx.backendAttempted {
		tx.budget.release()
		tx.budgetAcquired = false
	}
	tx.completed = true
	session := tx.createSession()
	intent := rollbackCommandToIntent(cmd)
	return session.TerminalizeAttempt(ctx, intent, evidence)
}

func (tx *attemptTx) rollbackSimple(ctx context.Context, cmd sdkterminal.Command, rel usagelimit.ReleaseKind, outcome usageevidence.LegOutcome, err error, reason string) {
	if tx == nil || tx.completed {
		return
	}
	var rErr error
	if err != nil {
		rErr = err
	} else if ctx != nil {
		rErr = ctx.Err()
	}
	tx.rollback(ctx, cmd, attemptEvidence{
		Command:      cmd,
		ReleaseKind:  rel,
		LegOutcome:   outcome,
		Usage:        emptyOperatorUsageShell(),
		Err:          rErr,
		TraceID:      tx.reqFacts.traceID,
		ALegID:       tx.reqFacts.aLegID,
		StartedAt:    tx.openStartedAt,
		RecordReason: reason,
	})
}

func (tx *attemptTx) recordFailure(ctx context.Context, outcome aipapi.AttemptOutcome, reason string, err error) {
	tx.recordFailureOf(ctx, outcome, reason, err, false, false)
}

// recordOpenFailure records a failed backend Open: nothing reached the client,
// so the secure-session outcome carries the pre-output classification.
func (tx *attemptTx) recordOpenFailure(ctx context.Context, bindFailed bool, outcome aipapi.AttemptOutcome, reason string, err error) {
	tx.recordFailureOf(ctx, outcome, reason, err, !bindFailed, bindFailed)
}

func (tx *attemptTx) recordFailureOf(ctx context.Context, outcome aipapi.AttemptOutcome, reason string, err error, openFailed, bindFailed bool) {
	tx.e.recordAttemptLogged(ctx, recordAttemptParams{
		OpenFailed: openFailed,
		BindFailed: bindFailed,
		ALegID:     tx.reqFacts.aLegID,
		BLeg:       tx.bleg,
		Cand:       tx.cand,
		Outcome:    outcome,
		Reason:     reason,
		DetailErr:  err,
	}, diag.AttrOpts{CallID: tx.reqFacts.traceID, BLegID: tx.bleg.BLegID})
}

// abortLaunchPermit releases an uncommitted launch permit exactly once.
func (tx *attemptTx) abortLaunchPermit() {
	if tx == nil || tx.launchPermit == nil {
		return
	}
	tx.launchPermit.Abort()
	tx.launchPermit = nil
}

// commitLaunchOrRegister transfers a successful Open into A-leg authority:
// committing the launch permit when present, else registering directly with
// the A-leg scope. It terminalizes/disposes through the ready owner on every
// failure disposition and returns the disposition error:
//   - leglifecycle.ErrALegCanceled when the permit observed sticky cancellation;
//   - the commit error, when registration-through-permit failed;
//   - ctx.Err() when the context died between commit and use;
//   - the RegisterBLeg error for direct A-leg scope registration.
func (tx *attemptTx) commitLaunchOrRegister(ctx context.Context, ready *readyAttempt, aScope *leglifecycle.ALeg) error {
	if tx == nil {
		return nil
	}
	if tx.launchPermit != nil {
		commitRes, commitErr := tx.launchPermit.Commit(ready.lifecycleHandle())
		tx.registered = true
		tx.launchPermit = nil
		if commitRes.Canceled {
			cause := commitRes.Cause
			if cause.Kind == "" {
				cause = leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}
			}
			ready.cancelViaLifecycle(ctx, cause)
			return leglifecycle.ErrALegCanceled
		}
		if commitErr != nil {
			ready.Dispose(ctx, commitErr)
			return commitErr
		}
		if err := ctx.Err(); err != nil {
			ready.cancelViaLifecycle(ctx, aipapi.CancelCause{Kind: aipapi.CancelContextDone})
			return err
		}
		return nil
	}
	if aScope != nil {
		if err := aScope.RegisterBLeg(ctx, leglifecycle.BLegHandle{
			ID:      tx.bleg.BLegID,
			Attempt: ready.lifecycleHandle(),
		}); err != nil {
			ready.Dispose(ctx, err)
			return err
		}
		tx.registered = true
	}
	return nil
}

func (e *Executor) startAttemptTx(ctx context.Context, rf requestFacts, route routeFacts, cand routing.AttemptCandidate, budget *attemptBudget, failures *candidateFailureHistory) (*attemptTx, error) {
	bleg, err := e.Store.NextBLeg(ctx, rf.aLegID)
	if err != nil {
		return nil, fmt.Errorf("executor: next b-leg: %w", err)
	}

	if rf.billingCallState != nil {
		rf.billingCallState.noteAllocatedBLeg(bleg.BLegID, bleg.Seq)
	}
	if failures == nil && budget != nil {
		failures = budget.getFailures()
	}
	return &attemptTx{
		e:          e,
		reqFacts:   rf,
		routeFacts: route,
		cand:       cand,
		bleg:       bleg,
		budget:     budget,
		failures:   failures,
		boundary:   e.newLocalBoundaryAccumulator(),
		promptCache: promptCacheObservations{
			systemPromptLaunchSeq: systemPromptLaunchSeqFromContext(ctx),
		},
	}, nil
}

func (e *Executor) getOrBeginInterleavedTurn(ctx context.Context, rf requestFacts, c routing.AttemptCandidate) (InterleavedTurn, error) {
	if !e.interleavedEnabled() {
		return nil, nil
	}
	return e.InterleavedProcessor.BeginTurn(ctx, InterleavedTurnInput{
		ALegID:              rf.aLegID,
		Selector:            rf.baseline.Route.Selector,
		Backend:             c.Primary.Backend,
		Model:               c.Primary.Model,
		RequestID:           rf.traceID,
		SuppressVisibleMemo: rf.suppressVisibleMemo,
	})
}

func (e *Executor) evaluateCandidate(
	ctx context.Context,
	rf requestFacts,
	routeFacts routeFacts,
	plan candidatePlan,
	interleaved interleavedstate.State,
) (candidateEvaluationOutcome, error) {
	var zero candidateEvaluationOutcome
	// Full admission negotiation is bypassed for wire attempts:
	// (a) Wire eligibility is owned by the assessment layer (ProductionLargeBodyAssessor
	//     plus route-domain policy, already merged and reviewed).
	// (b) Admission negotiation operates on aipapi.Call, and wire has no Call by design —
	//     synthesizing one would reintroduce the payload-sized materialization that Phase 1
	//     (candidate.go, return res, nil, nil) eliminated.
	// (c) This preserves pre-collapse wire behavior (the legacy wire execution engine in
	//     executor_execute_large_body.go never ran candidate admission negotiation).
	if rf.isWire() {
		if _, ok := e.Backends[plan.cand.Primary.Backend]; !ok {
			return zero, fmt.Errorf("executor: unknown backend %q", plan.cand.Primary.Backend)
		}
		return candidateEvaluationOutcome{
			accepted: true,
		}, nil
	}
	attempt := aipapi.CloneCall(rf.baseline)
	if e.MaxPendingWireEvents > 0 {
		attempt.MaxPendingWireEvents = e.MaxPendingWireEvents
	}
	var turn InterleavedTurn
	if e.interleavedEnabled() && plan.cand.InterleavedRole != interleavedstate.RoleNone {
		var err error
		turn, err = e.getOrBeginInterleavedTurn(ctx, rf, plan.cand)
		if err != nil {
			return zero, fmt.Errorf("executor: interleaved turn: %w", err)
		}
		shaped, err := e.shapeAttemptCall(ctx, attempt, plan.cand, turn)
		if err != nil {
			return zero, fmt.Errorf("executor: interleaved shape: %w", err)
		}
		attempt = shaped
		var outcome string
		var turnsRemaining int
		if turn != nil {
			outcome, turnsRemaining = turn.ShapeDiagnostics()
		}
		e.logInterleavedMemoShape(ctx, rf.traceID, "", plan.cand, outcome, turnsRemaining)
	}
	be, ok := e.Backends[plan.cand.Primary.Backend]
	if !ok {
		return zero, fmt.Errorf("executor: unknown backend %q", plan.cand.Primary.Backend)
	}
	var transforms []request.AttemptTransform
	var atSvc request.Services
	if e.RuntimeSnapshot != nil {
		transforms = e.RuntimeSnapshot.AttemptTransforms()
		atSvc = request.Services{State: e.RuntimeSnapshot.State(), Aux: e.RuntimeSnapshot.Aux()}
	}
	atMeta := e.candidateAttemptMeta(ctx, rf, attempt, plan.cand, be)
	xformRes, xformErr := extensions.RunCandidateAttemptTransformStage(
		ctx, e.Log, e.ExtensionMetrics, transforms, &attempt, atMeta, atSvc,
	)
	if xformErr != nil {
		return zero, fmt.Errorf("executor: candidate attempt transform: %w", xformErr)
	}
	if xformRes.Excluded {
		return candidateEvaluationOutcome{
			accepted: false,
			rejection: candidateRejection{
				kind:   rejectExclude,
				detail: xformRes.ReasonCode,
			},
		}, nil
	}
	pinCandidateRouteIdentity(&attempt, rf.baseline)
	transportCtx, transportSpan := otel.Tracer(otelScopeExecutor).Start(
		ctx, "aip.executor.candidate_admission",
		trace.WithAttributes(
			attribute.String("aip.backend", plan.cand.Primary.Backend),
			attribute.String("aip.operation", string(attempt.Invocation.Operation)),
			attribute.String("aip.client_delivery_mode", string(attempt.Invocation.DeliveryMode)),
		),
	)
	defer transportSpan.End()
	var facts modelcatalog.EffectiveFacts
	var res aipapi.NegotiationResult
	admitOut, admitPanicErr := safety.CallValue(
		safety.BoundaryBackend,
		"backend_candidate_admission",
		func() (candidateAdmissionOutcome, error) {
			return e.evaluateCandidateAdmission(transportCtx, rf.traceID, attempt, plan.cand, be, routeFacts.failoverReq), nil
		},
	)
	if admitPanicErr != nil {
		if pe, ok := errors.AsType[*safety.PanicError](admitPanicErr); ok {
			if e != nil && e.Log != nil {
				attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{AttrOpts: diag.AttrOpts{CallID: rf.traceID}})
				attrs = diag.AppendIsolatedCrashStack(attrs, pe)
				e.Log.LogAttrs(ctx, slog.LevelError, "isolated_panic_candidate_admission", attrs...)
			}
			diag.LogDecision(
				ctx, e.Log, "candidate_admission_panic_exclude", diag.AttrOpts{CallID: rf.traceID},
				slog.String("candidate_key", plan.cand.Key),
				slog.String("backend", plan.cand.Primary.Backend),
			)
			return candidateEvaluationOutcome{
				accepted: false,
				rejection: candidateRejection{
					kind:   rejectExclude,
					detail: "panic",
				},
			}, nil
		}
		return zero, admitPanicErr
	}
	facts = admitOut.facts
	res = admitOut.admitRes.Capability
	transportRes := admitOut.admitRes.Transport
	transportMode := admitOut.transportMode
	transportSpan.SetAttributes(
		attribute.String("aip.transport_mode", string(transportMode)),
		attribute.String("aip.transport_negotiation_kind", string(transportRes.Kind)),
		attribute.String("aip.admission_kind", string(admitOut.admitRes.Kind)),
	)
	if admitOut.admitRes.Kind == aipapi.NegotiationReject {
		if transportRes.Kind == aipapi.NegotiationReject {
			transportSpan.RecordError(transportRes.Err())
			transportSpan.SetStatus(codes.Error, "transport negotiation rejected")
		}
		return candidateEvaluationOutcome{
			accepted: false,
			rejection: candidateRejection{
				kind:   rejectAdmission,
				detail: admitOut.admitRes,
			},
			admitOut: admitOut,
		}, nil
	}
	e.recordTransportNegotiation(attempt.Invocation.Operation, transportRes.Selected, "accept")
	attempt.Invocation.TransportMode = transportRes.Selected
	if res.Kind == aipapi.NegotiationDowngrade {
		// A bound feature guard may refuse the strip before it is applied.
		// The refusal is operation-level: the attempt never opens a
		// provider and the operation does not fall back to another
		// candidate (design §2; Requirement 1.4).
		if derr := e.checkAttemptDowngradeGuard(ctx, rf.baseline, rf.auxiliaryRole, plan.cand, attempt.Options.ReasoningEffort, res); derr != nil {
			return zero, derr
		}
		diag.LogDecision(
			ctx, e.Log, "capability_downgrade", diag.AttrOpts{CallID: rf.traceID},
			slog.String("candidate_key", plan.cand.Key),
			slog.String("backend", plan.cand.Primary.Backend),
		)
		aipapi.ApplyNegotiatedDowngrades(&attempt, res)
		e.stripDowngradedReasoningReplay(ctx, rf.traceID, &attempt, plan.cand, res)
	}
	preflightDecision, _ := e.runPreflight(ctx, rf.traceID, attempt, plan.cand, facts.Facts)
	eligRan := e.EligibilityResolver != nil
	if eligRan {
		facts = e.effectiveFactsForAttempt(ctx, be, attempt, plan.cand)
		d := e.EligibilityResolver.Check(ctx, plan.cand, attempt, facts)
		if !d.IsEligible {
			return candidateEvaluationOutcome{
				accepted: false,
				rejection: candidateRejection{
					kind: rejectContextLimit,
				},
				facts:             facts,
				preflightDecision: preflightDecision,
				admitOut:          admitOut,
			}, nil
		}
	}
	if res.Kind == aipapi.NegotiationDowngrade && !eligRan {
		facts = e.effectiveFactsForAttempt(ctx, be, attempt, plan.cand)
	}
	precheckState, err := e.admitAttemptAuthority(ctx, rf.traceID, rf.aLegID, b2bua.BLegRecord{}, attempt, plan.cand, preflightDecision, true)
	if err != nil {
		return zero, err
	}
	_ = precheckState
	return candidateEvaluationOutcome{
		accepted:          true,
		attempt:           attempt,
		facts:             facts,
		preflightDecision: preflightDecision,
		admitOut:          admitOut,
		turn:              turn,
	}, nil
}

func (e *Executor) applyRejection(failures *candidateFailureHistory, c routing.AttemptCandidate, rejection candidateRejection) {
	if failures == nil {
		return
	}
	switch rejection.kind {
	case rejectExclude:
		if failures.TransformExcludes != nil {
			if reason, ok := rejection.detail.(string); ok && reason != "" && reason != "panic" {
				failures.TransformExcludes.noteTransform(reason)
			} else {
				failures.TransformExcludes.noteOther()
			}
		}
	case rejectAdmission:
		if admit, ok := rejection.detail.(aipapi.CandidateAdmissionResult); ok {
			if admit.Kind == aipapi.NegotiationReject {
				failures.CapabilityReject = admit.Capability
			}
			if admit.Transport.Kind == aipapi.NegotiationReject {
				failures.TransportReject = admit.Transport
			}
		}
	case rejectContextLimit:
		failures.ContextLimit = true
		if failures.TransformExcludes != nil {
			failures.TransformExcludes.noteOther()
		}
	case rejectParallel:
		if err, ok := rejection.detail.(error); ok {
			failures.ParallelFailure = err
		}
	}
}

// successfulOpenBinding is the trusted lineage projected onto one successfully
// opened provider stream. Canonical acquisition supplies the call identity as
// the request identity; wire acquisition supplies its frozen wire request
// identity. Both owners share this projection so their bindings cannot drift.
type successfulOpenBinding struct {
	requestID     string
	storeID       string
	billingCallID string
	aLegID        string
	bleg          b2bua.BLegRecord
	openedAt      time.Time
	receivedAt    time.Time
	// providerAccountID is the credited billing account frozen for this call.
	// It is empty when no account was admitted.
	providerAccountID string
	// providerScope is the customer scope frozen at exposure admission.
	providerScope scope.PrincipalScopeView
	// providerScopePresent reports whether a trusted scope was frozen. It is
	// read from the freeze's own source, never inferred from the view.
	providerScopePresent bool
}

// bindSuccessfulProviderStream binds one successful non-nil provider stream to
// the current trusted request, billing call, store, and attempt sequence
// before adapters wrap it and before downstream publication. Streams without
// evidence capability keep their existing behavior; error and nil opens never
// reach this projection. A panicking binder is isolated through the existing
// backend safety boundary and returned, so the acquisition owner rolls the
// started attempt back instead of leaking its stream.
func bindSuccessfulProviderStream(stream aipapi.ManagedEventStream, binding successfulOpenBinding) error {
	if stream == nil {
		return nil
	}
	binder, ok := stream.(coremetering.ProviderEvidenceBinder)
	if !ok || binder == nil {
		return nil
	}
	callID := binding.billingCallID
	return safety.Call(safety.BoundaryBackend, "bind_economic_evidence", func() error {
		binder.BindEconomicEvidence(coremetering.ObservationIdentity{
			StoreID: binding.storeID, RequestID: binding.requestID,
			CallID: callID, BillingCallID: callID,
			ALegID: binding.aLegID, BLegID: binding.bleg.BLegID,
			AttemptID: binding.bleg.BLegID, AttemptSeq: uint64(maxInt(binding.bleg.Seq, 0)),
			ObservedAt: binding.openedAt.UTC(), ReceivedAt: binding.receivedAt.UTC(),
			Provider: coremetering.ProviderBinding{
				AccountID:    binding.providerAccountID,
				Scope:        binding.providerScope,
				ScopePresent: binding.providerScopePresent,
			},
		})
		return nil
	})
}

// successfulOpen carries what one successful backend Open hands to
// commitSuccessfulOpen. Canonical and wire acquisition differ only in these
// values; the steps and their order are shared.
type successfulOpen struct {
	cand routing.AttemptCandidate
	plan candidatePlan
	// traceCall is the call recorded in the open trace; wire opens record the
	// zero Call.
	traceCall aipapi.Call
	// traceCtx is the context the best-effort trace write detaches from.
	traceCtx context.Context
	// stream is the stream the evidence binder sees: the raw stream on the
	// canonical path, the cleanup-owning wrapper on the wire path. It is
	// always a real stream: a backend Open that returned none is a failed
	// open and never reaches this commit.
	stream    aipapi.ManagedEventStream
	requestID string
	openStart time.Time
	// openCtx and wire are passed to failOpen when binding fails.
	openCtx  context.Context
	wire     bool
	openSpan trace.Span
}

// commitSuccessfulOpen runs the steps that follow every successful backend
// Open on both payload paths, in their required order: persist the immutable
// open trace, then bind provider economic evidence.
//
// The trace is written BEFORE the provider-controlled binder runs, so a binder
// panic still leaves exactly one trace for the swallowed Failed outcome to land
// on instead of ErrSessionNotFound. Error opens and opens that returned no
// stream never reach this function: they are failed opens, and the acquisition
// sites record them with a failed-open trace. Trace persistence is best effort
// and its fields, context, and clocks are unchanged.
//
// The caller must already own the stream for cleanup, so a binder panic rolls
// back through failOpen instead of leaking. When stop is true the attempt has
// ended in failOpen and the caller must return err unchanged, including a nil
// err, which means the failure was swallowed for failover.
func (tx *attemptTx) commitSuccessfulOpen(ctx context.Context, so successfulOpen) (stop bool, err error) {
	e := tx.e
	tx.persistOpenTrace(so.traceCtx, so.cand, so.traceCall, so.openStart, false)
	bindErr := bindSuccessfulProviderStream(so.stream, successfulOpenBinding{
		requestID:            so.requestID,
		storeID:              tx.reqFacts.billingStoreID,
		billingCallID:        tx.reqFacts.billingCallID.String(),
		aLegID:               tx.reqFacts.aLegID,
		bleg:                 tx.bleg,
		openedAt:             so.openStart,
		receivedAt:           e.now(),
		providerAccountID:    tx.reqFacts.billingAccountID,
		providerScope:        tx.reqFacts.billingCallState.frozenScope(),
		providerScopePresent: tx.reqFacts.billingCallState.frozenScopePresent(),
	})
	if bindErr == nil {
		return false, nil
	}
	if pe, ok := errors.AsType[*safety.PanicError](bindErr); ok {
		bindErr = mapBackendPanic(pe, false, so.cand.Key)
	}
	return true, tx.failOpen(ctx, so.cand, so.plan, openFailure{
		err:        bindErr,
		openErr:    contextErr(so.openCtx),
		wire:       so.wire,
		postOpen:   true,
		bindFailed: true,
	}, so.openSpan)
}

// persistOpenTrace writes the best-effort secure-session attempt trace for one
// backend Open. openFailed marks an Open that acquired no stream, so the
// record never reads as a successful acquisition and the terminal failure
// outcome has a trace to land on.
func (tx *attemptTx) persistOpenTrace(traceCtx context.Context, cand routing.AttemptCandidate, call aipapi.Call, openStart time.Time, openFailed bool) {
	e := tx.e
	m := e.secureSessionForAttempt()
	if m == nil || !tx.reqFacts.secureTurnOK {
		return
	}
	tr := buildAttemptTrace(tx.reqFacts.secureTurn, tx.reqFacts.aLegID, tx.bleg, cand, call, openStart)
	tr.OpenFailed = openFailed
	persistCtx := context.WithoutCancel(traceCtx)
	if rerr := m.RecordAttemptOpened(persistCtx, tr); rerr != nil && e.Log != nil {
		e.Log.DebugContext(persistCtx, "secure_session_attempt_trace_failed", "error", rerr)
	}
}

func (e *Executor) openAttemptTx(
	ctx context.Context,
	tx *attemptTx,
	evalOutcome candidateEvaluationOutcome,
	plan candidatePlan,
) error {
	c := plan.cand
	attempt := evalOutcome.attempt
	be, beOK := e.Backends[c.Primary.Backend]
	if tx.reqFacts.isWire() {
		return e.openWireAttemptTx(ctx, tx, be, beOK, c, plan)
	}
	if tx.budget != nil {
		if !tx.budget.tryAcquire() {
			return fmt.Errorf("executor: %w", aipapi.ErrMaxRouteAttempts)
		}
		tx.budgetAcquired = true
	}
	hookCtx := tx.reqFacts.projectContext(ctx, e.Log)
	if err := tx.reqFacts.bus.RunRequestPartHooks(hookCtx, &attempt, sdk.PartMeta{
		TraceID:    tx.reqFacts.traceID,
		ALegID:     tx.reqFacts.aLegID,
		BLegID:     tx.bleg.BLegID,
		AttemptSeq: tx.bleg.Seq,
		BackendID:  strings.TrimSpace(c.Primary.Backend),
	}); err != nil {
		return fmt.Errorf("executor: request hooks: %w", err)
	}
	// Two-Point Projection point 1: ordinary request mutation is complete, so the
	// projection inserts the approved control contract before the authoritative
	// post-hook capability, context, and accounting rederive below.
	if err := e.projectCandidateControlTool(hookCtx, tx, be, &attempt, c); err != nil {
		return err
	}
	postHook, postErr := e.rederiveAfterRequestHooks(ctx, tx.reqFacts, tx.routeFacts, &attempt, c, be, plan.stickyBackendID, plan.stickyBinding, tx.failures, plan.parallel)
	if postErr != nil {
		return postErr
	}
	if postHook.excluded {
		tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, nil, "")
		return nil
	}
	preflightDecision := evalOutcome.preflightDecision
	if postHook.preflightOK {
		preflightDecision = postHook.preflight
	}
	facts := postHook.facts
	openCall, err := backendCallWithRouteParams(attempt, c)
	if err != nil {
		return fmt.Errorf("executor: %w", err)
	}
	// Task 4.1: final conversation-view reassertion at shared candidate-open choke point.
	// Uses frozen snapshot/provenance/filteredBaseline (no store read) to remove reintroduced never_backend and
	// rebuild steering exactly once at frozen placement, handling late transforms based on projected baseline.
	var reassertProvenance []conversationprojection.OverlayProvenance
	if len(tx.reqFacts.conversationSnapshot.NeverBackend) > 0 || len(tx.reqFacts.conversationSnapshot.Steering) > 0 || len(tx.reqFacts.conversationProvenance) > 0 {
		reasserted, reEv, rerr := conversationprojection.Reassert(openCall, tx.reqFacts.conversationSnapshot, tx.reqFacts.conversationProvenance, tx.reqFacts.conversationFilteredBaseline)
		if rerr != nil {
			if obs := e.conversationViewObserver(); obs != nil {
				safe := safeObserver{obs: obs}
				safe.OnProjectionFailure(conversationprojection.StageFinal)
				if errors.Is(rerr, conversationprojection.ErrAnchorMissing) || errors.Is(rerr, conversationprojection.ErrAnchorNotFound) {
					safe.OnAnchorFailure(conversationprojection.AnchorFailClosed)
				}
			}
			tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, nil, "")
			return fmt.Errorf("executor: conversation view reassert: %w", rerr)
		}
		openCall = reasserted
		if reEv != nil && len(reEv.Provenance) > 0 {
			reassertProvenance = reEv.Provenance
		} else {
			reassertProvenance = tx.reqFacts.conversationProvenance
		}
		if obs := e.conversationViewObserver(); obs != nil {
			safe := safeObserver{obs: obs}
			summary := conversationprojection.NewProjectionSummary(tx.reqFacts.conversationSnapshot, reEv)
			safe.OnProjection(conversationprojection.StageFinal, summary)
			if reEv != nil {
				for range reEv.Fallbacks {
					safe.OnAnchorFallback(conversationprojection.StageFinal, conversationprojection.AnchorStablePrefixFallback)
				}
			}
		}
	} else {
		reassertProvenance = tx.reqFacts.conversationProvenance
	}
	// Two-Point Projection point 2: replay the already-approved projection after
	// the final conversation-view reconstruction and before any clamp, ingress,
	// capture, adaptation, or Backend.Open. Eligibility is never reconsidered here
	// and the spec is never re-read: only the approved bytes are replayed, and a
	// candidate that cannot reproduce them fails closed before upstream open.
	if tx.controlTool.active() {
		reassertedControl, cerr := controltool.Reassert(openCall, tx.controlTool.projection)
		if cerr != nil {
			tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, nil, "")
			return fmt.Errorf("executor: control tool reassertion: %w", cerr)
		}
		openCall = reassertedControl
	}
	previewedClamps, previewRan, perr := e.previewAndApplyAttemptClamps(ctx, &openCall, c, tx.reqFacts.aLegID, tx.bleg.BLegID)
	if perr != nil {
		return perr
	}
	authorizedFreeze := aipapi.CloneCall(openCall)
	admitDecision := preflightDecision
	holder := tx.reqFacts.metering
	scopeVal := tx.reqFacts.recvViews.Scope
	if holder != nil {
		if _, cerr := holder.StoreBackendIngress(checkpoint.BackendIngressInput{
			Call:         authorizedFreeze,
			Scope:        scopeVal,
			AttemptID:    tx.bleg.BLegID,
			BLegID:       tx.bleg.BLegID,
			ALegID:       tx.reqFacts.aLegID,
			BackendID:    strings.TrimSpace(c.Primary.Backend),
			Model:        strings.TrimSpace(c.Primary.Model),
			CheckpointID: "operator-attempt:" + tx.bleg.BLegID,
			StreamID:     "operator-attempt:" + tx.bleg.BLegID,
			TraceID:      strings.TrimSpace(tx.reqFacts.traceID),
			Now:          e.now(),
		}); cerr != nil {
			return fmt.Errorf("executor: metering backend ingress: %w", cerr)
		}
		if beDecision, ok := e.runPreflight(ctx, tx.reqFacts.traceID, openCall, c, facts.Facts); ok {
			if !beDecision.Allowed {
				return fmt.Errorf("executor: token accounting preflight: %w", beDecision.Err)
			}
			admitDecision = beDecision
			e.enrichBackendIngressQuantitiesWithDecision(holder, tx.bleg.BLegID, beDecision)
			if beDecision.AdjustedMaxOutputTokens != nil {
				adjusted := *beDecision.AdjustedMaxOutputTokens
				openCall.Options.MaxOutputTokens = &adjusted
			}
		} else {
			e.enrichBackendIngressQuantitiesWithDecision(holder, tx.bleg.BLegID, admitDecision)
		}
		if _, ferr := e.persistBackendIngressFact(ctx, holder, tx.bleg.BLegID); ferr != nil {
			return fmt.Errorf("executor: metering backend ingress fact: %w", ferr)
		}
	}
	authState, err := e.admitAttemptAuthority(ctx, tx.reqFacts.traceID, tx.reqFacts.aLegID, tx.bleg, openCall, c, admitDecision, false)
	if err != nil {
		if authState.admissionResult.Reserved {
			cleanup := e.newAttemptAuthorityLifecycle(authState, c)
			_ = terminalizeAttemptEphemeral(ctx, sdkterminal.CommandPreBackendDenial, false, func(cctx context.Context) error {
				cleanup.Release(cctx, usagelimit.ReleaseKindAdmissionFailure)
				return nil
			})
		}
		return err
	}
	tx.authState = authState
	tx.authLifecycle = e.newAttemptAuthorityLifecycle(authState, c)
	if err := e.enforcePostAdmitClamps(ctx, &openCall, authorizedFreeze, previewedClamps, previewRan, authState, c, int64(admitDecision.Count.InputTokens)); err != nil {
		return err
	}
	if len(previewedClamps) > 0 && !backendCanEnforceAuthorityClamp(be, &openCall) {
		tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, nil, "")
		return nil
	}
	if admitDecision.RequireMaxOutputEnforcement && !backendCanEnforceAuthorityClamp(be, &openCall) {
		tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, nil, "")
		return nil
	}
	if werr := checkpoint.AssertNotWidened(authorizedFreeze, openCall); werr != nil {
		return fmt.Errorf("executor: %w", werr)
	}
	replay := execbackend.EffectiveReplaySupport(ctx, be, openCall, c)
	projTarget := aipapi.LegacyProjectionTargetFromCaps(facts.EffectiveCaps, replay)
	projTarget.SupportedExtensions = append([]aipapi.ExtensionRequirement(nil), execbackend.EffectiveDialectSupport(ctx, be, openCall, c).ExtensionTypes...)
	adaptedCall, adaptErr := aipapi.AdaptCallForCandidate(aipapi.CloneCall(openCall), projTarget)
	if adaptErr != nil {
		tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, nil, "")
		return adaptErr
	}
	// Verify candidate adaptation preserved full projection (never_backend absent, steering exact count/order/placement).
	// The same pass returns the adapted positions of injected entries, so the
	// instruction-identity feed never reuses pre-adaptation indexes or rescans
	// the call to locate volatile emissions.
	var adaptedInjectedPositions []conversationprojection.InjectedPosition
	if len(tx.reqFacts.conversationSnapshot.NeverBackend) > 0 || len(tx.reqFacts.conversationProvenance) > 0 || len(reassertProvenance) > 0 {
		provForVerify := reassertProvenance
		if len(provForVerify) == 0 {
			provForVerify = tx.reqFacts.conversationProvenance
		}
		positions, verr := conversationprojection.VerifyAdaptationPreservesProjectionWithPositions(openCall, adaptedCall, tx.reqFacts.conversationSnapshot, provForVerify)
		if verr != nil {
			if obs := e.conversationViewObserver(); obs != nil {
				safeObserver{obs: obs}.OnProjectionFailure(conversationprojection.StageFinal)
			}
			tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, nil, "")
			return fmt.Errorf("executor: conversation view adaptation integrity: %w", verr)
		}
		adaptedInjectedPositions = positions
	}
	wireCall := adaptedCall
	wireCall.Session.ClientSessionID = ""
	wireCall.Session.ContinuityKey = ""
	wireCall.Session.AuthoritativeSessionID = ""
	wireCall.Session.ResumeToken = ""
	if tx.boundary != nil {
		// This is only a bounded canonical estimate. Essential adapters replace
		// it through the context observer after constructing their final wire
		// representation (including provider-specific transforms).
		tx.boundary.PrepareCall(wireCall)
	}
	// The backend open starts billable provider work, so an expired bound must
	// not reach it. The auxiliary path refuses an expired context before it
	// invokes the executor, but the preparation between that refusal and this
	// boundary can outlive the bound; the boundary rejects the expired bound
	// independently instead of relying on the caller to check.
	//
	// Only an expired deadline is refused here. A cancellation keeps its
	// established meaning: the attempt is still opened and classified, which
	// is what the parallel-race loser and the client-disconnect paths rely on.
	//
	// This first check runs before the traffic observation and the provider
	// call together: a leg whose bound already expired leaves no traffic
	// record behind. The observation itself is synchronous and unbounded, so
	// the bound is validated once more at the last practical boundary below.
	if err := ctx.Err(); err != nil && errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if e.RuntimeSnapshot != nil {
		bundle := coretraffic.PortBundleFromSnapshot(e.RuntimeSnapshot)
		if !bundle.EmitIsNoop() {
			if rawPayload, jerr := json.Marshal(wireCall); jerr == nil {
				sc := tx.reqFacts.recvViews.Scope
				meta := sdktraffic.CaptureMeta{
					TraceID:     tx.reqFacts.traceID,
					ALegID:      tx.reqFacts.aLegID,
					BLegID:      tx.bleg.BLegID,
					AttemptSeq:  tx.bleg.Seq,
					BackendID:   strings.TrimSpace(c.Primary.Backend),
					PrincipalID: strings.TrimSpace(sc.PrincipalID.String()),
					Scope:       sc,
				}
				bundle.Emit(
					ctx,
					sdktraffic.LegPTB,
					meta,
					"aip/canonical+json",
					"application/json",
					rawPayload,
				)
			}
		}
	}
	baseOpenCtx := ctx
	var cancelOpen context.CancelFunc = func() {}
	ttftDeadline := ttftContextDeadline{}
	if tx.failures != nil && tx.failures.progress != nil && tx.failures.progress.ttft != nil {
		baseOpenCtx, cancelOpen, ttftDeadline = tx.failures.progress.ttft.scopedContext(ctx, e.now(), c.Key, c.Primary.TTFTTimeout)
	}
	defer cancelOpen()

	if tx.reqFacts.aScope != nil {
		permitCtx, permit, perr := tx.reqFacts.aScope.BeginBLegLaunch(baseOpenCtx, tx.bleg.BLegID)
		if perr != nil {
			if errors.Is(perr, leglifecycle.ErrALegCanceled) {
				tx.recordFailure(ctx, aipapi.AttemptCancelled, "a-leg canceled before launch", perr)
				tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, perr, "a-leg canceled before launch")
				return nil
			}
			return perr
		}
		tx.launchPermit = permit
		baseOpenCtx = permitCtx
	}

	// The pre-open invariant: the attempt must still carry the frozen target
	// and the frozen options of the candidate it is about to open. The guard
	// runs after every mutating transform, after the post-hook re-derive and
	// its negotiated downgrade, after the route-parameter merge and after the
	// projection adaptation, and before any provider I/O.
	//
	// A refusal closes the attempt as never started and leaves the open path.
	// On the sequential path the operation fails explicitly instead of
	// falling back to another candidate (design §2; Requirement 1.4, 1.5).
	// In a parallel round the refusal fails this arm only: every arm passes
	// the same guard before it opens, so a sibling that serves was checked
	// too. A guard-owning feature that needs whole-operation refusal keeps
	// its governed calls on single-target routes.
	if gerr := e.checkAttemptGuard(ctx, tx, c, openCall, be); gerr != nil {
		tx.rollbackSimple(ctx, sdkterminal.CommandPreBackendDenial, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, gerr, "")
		return gerr
	}

	openCtx, openSpan := otel.Tracer(otelScopeExecutor).Start(
		baseOpenCtx, "aip.executor.backend_open",
		trace.WithAttributes(
			attribute.String("aip.backend", c.Primary.Backend),
			attribute.Int("aip.b_leg_seq", int(tx.bleg.Seq)),
		),
	)
	defer openSpan.End()
	openStart := e.now()
	if aerr := e.assertSecureSessionActiveBeforeOpen(openCtx); aerr != nil {
		tx.abortLaunchPermit()
		return aerr
	}
	// The last practical boundary before the provider request. The traffic
	// observation above, the launch permit and the secure-session check are
	// all synchronous and unbounded, so a bound that was live at the check
	// above can have expired while they ran. The provider receives openCtx,
	// not ctx, so the effective context is what this boundary validates: it
	// also carries the per-attempt TTFT deadline, which can be narrower than
	// the caller's.
	//
	// The refusal is classified exactly like a failed open, so an expired TTFT
	// scope keeps its established leaf/global timeout classification and
	// settlement instead of surfacing as an anonymous deadline error. A
	// cancelled context is not refused here: it keeps flowing into the open and
	// is classified by the established cancellation paths.
	//
	// A traffic record may already exist for this leg when the refusal
	// happens here. That is the established shape of every pre-open refusal
	// after the observation (the secure-session denial included): the record
	// documents the request the proxy prepared, and the leg is still
	// classified as never started.
	if openErr := openCtx.Err(); openErr != nil && errors.Is(openErr, context.DeadlineExceeded) {
		return tx.failOpen(ctx, c, plan, openFailure{
			err:         openErr,
			ttftExpired: ttftDeadline.expired(openCtx, openErr),
			ttftScope:   ttftDeadline.scope,
			openErr:     openErr,
		}, openSpan)
	}
	tx.openStartedAt = openStart
	tx.openInvoked = true
	tx.backendAttempted = true
	tx.authLifecycle.backendAttempted.Store(true)
	openCtx = identity.WithClientUserAgent(openCtx, wireCall.Invocation.ClientUserAgent)
	openCtx = promptcache.WithObservationLineage(openCtx, promptcache.ObservationLineage{
		ALegID: tx.reqFacts.aLegID, BLegID: tx.bleg.BLegID,
		BackendInstanceID: c.Primary.Backend, CanonicalModelID: c.Primary.Model,
	})
	// The collector receives the backend's content-free downstream identity for
	// this attempt. It is read only after Open commits a usable stream; the
	// tokens stay in-process and are never emitted on a usage sample.
	openCtx, attemptIdentity := execbackend.WithAttemptIdentityCollector(openCtx)
	if tx.boundary != nil {
		tx.boundary.MarkAttempted()
		openCtx = coremetering.WithPreparedInputObserver(openCtx, tx.boundary.ObservePreparedInput)
	}
	// The structural cause is captured at the final boundary: it names a
	// change the proxy applied to the request the backend is about to receive,
	// after every request mutation, reassertion, clamp and adaptation. A
	// transformation that undoes the change it describes clears the label, so
	// the diagnostic never attributes a change the provider-bound request no
	// longer carries.
	//
	// The request publishes the cause at most once per cache key. The change
	// it names happened once, so a later leg of the same request, or a retry of
	// the same candidate, must not re-arm a cause that a healthy sample already
	// proved stale. The claim belongs to the request execution, so it holds
	// across parallel candidates, replacements and continuations of that
	openCtx = execbackend.WithAttemptMeta(openCtx, execbackend.AttemptMeta{
		ID:  tx.bleg.BLegID,
		Seq: tx.bleg.Seq,
	})
	stream, err := safety.CallValue(safety.BoundaryBackend, "backend_open", func() (aipapi.ManagedEventStream, error) {
		return be.Open(openCtx, wireCall, routing.BackendFacingCandidate(c))
	})
	// openReturned is the raw acquisition fact: the Open call completed
	// without an error. It is kept apart from the classified error because a
	// call that returned may have been dispatched upstream, so the leg failed
	// rather than never started - whatever the error says.
	openReturned := err == nil
	var nilStreamErr error
	// A backend Open that succeeds without a usable stream opened nothing:
	// there is no output to publish, no provider evidence to bind and no
	// attempt to attribute to that backend. It is a failed open, not a
	// successful one, so it is failed over like any other open failure
	// instead of being committed as an open that only breaks at the first
	// Recv.
	if err == nil && nilManagedStream(stream) {
		// The unusable stream must not become cleanup ownership: the terminal
		// path would call its lifecycle and cancellation-telemetry methods on
		// a nil receiver.
		stream = nil
		nilStreamErr = nilStreamOpenFailure(c.Key)
		err = classifyNilStreamOpen(openCtx, nilStreamErr)
	}
	// Capture the cancellation provenance before any local cleanup: the open
	// context is done only when the request, the A-leg or the core's own
	// budget ended the attempt, never when the adapter returned its own error.
	openErr := openCtx.Err()
	tx.stream = stream
	// Snapshot backend-open latency before audit persistence so slow
	// secure-session storage never inflates the provider duration metric.
	openDur := time.Since(openStart).Seconds()
	// Trace then evidence binding: see commitSuccessfulOpen for the ordering rule.
	if err == nil {
		if stop, ferr := tx.commitSuccessfulOpen(ctx, successfulOpen{
			cand:      c,
			plan:      plan,
			traceCall: openCall,
			traceCtx:  openCtx,
			stream:    stream,
			requestID: wireCall.ID,
			openStart: openStart,
			openCtx:   openCtx,
			openSpan:  openSpan,
		}); stop {
			return ferr
		}
	}
	if tx.boundary != nil {
		// The unusable-stream check above turned a stream-less open into a
		// failure, so a nil error means a usable stream was acquired.
		tx.boundary.MarkAccepted(err == nil)
	}
	if err != nil {
		tx.abortLaunchPermit()
		if pe, ok := errors.AsType[*safety.PanicError](err); ok {
			err = mapBackendPanic(pe, false, c.Key)
		}
	}
	if e.Metrics != nil {
		e.Metrics.OnBackendOpenDuration(c.Primary.Backend, openDur)
	}
	if err != nil {
		tx.persistOpenTrace(openCtx, c, openCall, openStart, true)
		return tx.failOpen(ctx, c, plan, openFailure{
			err:          err,
			ttftExpired:  ttftDeadline.expired(openCtx, err),
			ttftScope:    ttftDeadline.scope,
			openErr:      openErr,
			postOpen:     openReturned,
			nilStreamErr: nilStreamErr,
		}, openSpan)
	}
	diag.LogDecision(
		ctx, e.Log, "backend_attempt_opened", diag.AttrOpts{CallID: tx.reqFacts.traceID, BLegID: tx.bleg.BLegID},
		slog.String("candidate_key", c.Key),
		slog.String("backend", c.Primary.Backend),
		slog.String("model", c.Primary.Model),
		slog.String("operation", string(openCall.Invocation.Operation)),
		slog.String("client_delivery_mode", string(openCall.Invocation.DeliveryMode)),
		slog.String("upstream_transport_mode", string(openCall.Invocation.TransportMode)),
		slog.String("reasoning_effort", openCall.Options.ReasoningEffort),
		slog.String("verbosity", string(openCall.Options.Verbosity)),
		slog.Int64("open_duration_ms", time.Since(openStart).Milliseconds()),
	)
	// Initialize attempt-local resources on the acquisition owner
	fs, maxArgs := e.resolveToolCallFinalizers()
	tx.accounting = newAttemptAccountingTracker(e.now())
	tx.toolFinal = newToolCallAssembler(fs, maxArgs, wireCall.Tools)
	tx.promptCacheSource = promptCacheObservationSource(stream)
	tx.promptCacheController = promptCacheControllerFor(e.Backends[c.Primary.Backend])
	tx.finalStreamObs = &extensions.FinalStreamObservationSession{Log: e.Log, Metrics: e.ExtensionMetrics}
	tx.recordAttemptLoggedFn = e.recordAttemptLogged

	if c.MarkedFirst && !plan.parallel {
		if err := e.Store.SetWeightedFirstConsumed(ctx, tx.reqFacts.aLegID, true); err != nil {
			return fmt.Errorf("executor: set weighted first consumed: %w", err)
		}
	}
	// The request publishes the cause at most once per cache key, so a later leg
	// of the same request, or a retry of the same candidate, carries no cause
	// once this attempt has published it. Only an attempt that produced a stream
	// and completed every fallible step above claims the key: a nil-stream open
	// is deferred to Recv and may be retried on the same key, and a failed open
	// or step published nothing.
	if stream != nil {
		// Capture the keyed, content-free switch identity at the
		// committed-open boundary. It rides every usage sample this attempt
		// emits; the collapse diagnostic compares it with its healthy
		// reference. The same process-keyed diagnostic gate that arms the
		// instruction feed also arms this feed and stamps the request launch
		// ordinal, so attribution always has comparable chronology. A
		// disabled gate or a keyless executor publishes zero evidence rather
		// than a publicly reproducible fingerprint.
		if e != nil && e.SystemPromptDigestEnabled && !e.SystemPromptDigestKey.IsZero() {
			tx.switchIdentity = newAttemptSwitchIdentity(c, attemptIdentity.Identity(), e.SystemPromptDigestKey)
		}
		tx.structuralCause = tx.reqFacts.claimStructuralCause(wireCall.StructuralCause, c.Primary.Backend, c.Primary.Model)
		// The seam-digest feed is per B-leg: every attempt feeds its own
		// vector, with no once-per-request claim. It is computed only when
		// the gate is enabled and a process key is present, and only from
		// the provider-bound request, so a disabled gate pays no hashing
		// cost, a keyless executor publishes no predictable digests, and
		// the wire path (no canonical call) feeds nothing.
		if e != nil && e.SeamDigestsEnabled && !e.SeamDigestKey.IsZero() {
			tx.promptCache.seamDigests = seamItemDigests(wireCall, e.SeamDigestKey)
		}
		// The instruction-identity feed is per B-leg and runs only when the
		// collapse diagnostic can consume it and a process key is present.
		// It is computed from the final provider-bound call, after candidate
		// transforms, request hooks, conversation-view reassertion and
		// authority adaptation, because each of those can change the
		// instruction prefix the backend caches. Volatile steering emissions
		// are excluded by positions re-resolved in that final call, never by
		// pre-adaptation indexes. A failure to prove the placement, or to
		// marshal an instruction, yields no identity rather than a fabricated
		// one, so a leg with no identity leaves the diagnostic's comparison
		// baseline untouched.
		if e != nil && e.SystemPromptDigestEnabled && !e.SystemPromptDigestKey.IsZero() {
			if volatile, ok := volatileEmissionIndices(adaptedInjectedPositions, tx.reqFacts.conversationSnapshot); ok {
				if digest, ok := systemPromptDigest(wireCall, volatile, e.SystemPromptDigestKey); ok {
					tx.promptCache.systemPromptDigest = digest
				}
			}
		}
	}
	return nil
}

func (e *Executor) openNext(ctx context.Context, req openNextRequest) (openedAttempt, error) {
	ctx = req.reqFacts.projectContext(ctx, nil)
	p := req.progress
	failures := p.getFailures()
	p.interleaved = req.interleaved
	stickyBackendID, stickyBinding, err := e.lookupAffinityBinding(ctx, req.reqFacts.traceID, req.routeFacts.sel, req.routeFacts.affinityKey, req.routeFacts.affinitySet)
	if err != nil {
		return openedAttempt{interleaved: req.interleaved}, err
	}
	modeRetry := req.mode == openModeRetry
	opts := routing.PlanOptions{
		Excluded:               p.excluded,
		Unhealthy:              e.mergePlannerHealth(),
		RequestSize:            req.routeFacts.requestSize,
		Session:                p.session,
		PreferredCandidateKeys: slices.Clone(req.reqFacts.routePrefs),
		StickyBackendID:        stickyBackendID,
		Rand:                   req.routeFacts.rng,
		IsRetryPath:            modeRetry,
		ThinkerCycle:           p.interleaved.Cycle,
		SuppressThinker:        req.reqFacts.suppressThinker,
	}
	groups, err := routing.ExpandFailoverGroups(req.routeFacts.sel, opts)
	if stickyBinding && stickyBackendID != "" &&
		(err != nil || len(groups) == 0 || len(groups[0].Candidates) == 0 || groups[0].Candidates[0].Primary.Backend != stickyBackendID) {
		e.clearAffinityBinding(ctx, req.reqFacts.traceID, req.routeFacts.affinityKey, req.routeFacts.affinitySet, "ineligible")
		stickyBackendID = ""
		stickyBinding = false
		opts.StickyBackendID = ""
		groups, err = routing.ExpandFailoverGroups(req.routeFacts.sel, opts)
	}
	if err != nil {
		if errors.Is(err, routing.ErrNoEligibleCandidate) {
			if finalErr := failures.FinalError(err); finalErr != err {
				return openedAttempt{interleaved: req.interleaved}, finalErr
			}
		}
		return openedAttempt{interleaved: req.interleaved}, fmt.Errorf("executor: expand failover: %w", err)
	}
	var lastNoOpen openedAttempt
	lastNoOpen.interleaved = req.interleaved
	for gi, group := range groups {
		candidates := group.Candidates
		if len(candidates) == 0 {
			continue
		}
		if candidates[0].IsParallel {
			out, err := e.tryOpenParallelGroup(ctx, req, candidates, group.NextThinkerCycle, stickyBackendID, stickyBinding)
			if err != nil {
				return openedAttempt{interleaved: req.interleaved}, resolveTerminalOpenError(failures, err)
			}
			p.interleaved = out.interleaved
			if out.ready != nil {
				failures.ParallelFailure = nil
				return out, nil
			}
			lastNoOpen = out
			if gi+1 < len(groups) {
				continue
			}
			return lastNoOpen, nil
		}
		c := candidates[0]
		plan := candidatePlan{
			cand:            c,
			nextCycle:       group.NextThinkerCycle,
			stickyBackendID: stickyBackendID,
			stickyBinding:   stickyBinding,
		}
		out, err := e.evaluateAndOpenCandidate(ctx, req, plan)
		if err == nil && out.ready != nil {
			failures.ParallelFailure = nil
		}
		return out, resolveTerminalOpenError(failures, err)
	}
	return lastNoOpen, nil
}

// resolveTerminalOpenError resolves the error that ends a route open.
//
// A route that ran out of attempt budget reports the upstream fault it
// observed instead of the budget exhaustion, when that fault is what the
// history ranks as the reason the route failed: the budget is a proxy for "the
// route gave up", while the upstream fault names the backend that did. Every
// other recorded verdict keeps the pre-existing budget-error mapping, so no
// unrelated routing error changes behavior.
//
// Both payload paths and both candidate shapes (sequential and parallel) use
// this one resolution, so a route cannot report a budget error on one shape
// and the observed failure on another.
func resolveTerminalOpenError(failures *candidateFailureHistory, err error) error {
	if err == nil || !errors.Is(err, aipapi.ErrMaxRouteAttempts) {
		return err
	}
	upstream := failures.SwallowedUpstreamFailure
	if upstream != nil && failures.FinalError(err) == upstream {
		return upstream
	}
	return err
}

func (e *Executor) evaluateAndOpenCandidate(ctx context.Context, req openNextRequest, plan candidatePlan) (openedAttempt, error) {
	evalOutcome, err := e.evaluateCandidate(ctx, req.reqFacts, req.routeFacts, plan, req.interleaved)
	if err != nil {
		return openedAttempt{interleaved: req.interleaved}, err
	}
	if !evalOutcome.accepted {
		failures := req.progress.getFailures()
		e.applyRejection(failures, plan.cand, evalOutcome.rejection)
		switch evalOutcome.rejection.kind {
		case rejectAdmission:
			e.noteCandidateAdmissionReject(ctx, req.reqFacts.traceID, req.routeFacts.affinityKey, req.routeFacts.affinitySet, plan.cand, plan.stickyBackendID, plan.stickyBinding, evalOutcome.admitOut, "pre_open", failures)
		case rejectContextLimit:
			diag.LogDecision(
				ctx, e.Log, "context_limit_exclude", diag.AttrOpts{CallID: req.reqFacts.traceID},
				slog.String("candidate_key", plan.cand.Key),
				slog.String("backend", plan.cand.Primary.Backend),
			)
			cat := catalogRouteTraceIfEnabled(e, evalOutcome.facts, evalOutcome.admitOut.admitRes.Capability, nil, true)
			e.notePlanCandidate(ctx, req.reqFacts.traceID, plan.cand.Key, cat)
		case rejectExclude:
			reason, _ := evalOutcome.rejection.detail.(string)
			diag.LogDecision(ctx, e.Log, "attempt_transform_exclude", diag.AttrOpts{CallID: req.reqFacts.traceID},
				slog.String("decision", "exclude_candidate"), slog.String("candidate_key", plan.cand.Key),
				slog.String("backend", plan.cand.Primary.Backend), slog.String("reason_code", reason))
			e.notePlanCandidate(ctx, req.reqFacts.traceID, plan.cand.Key, nil)
		}
		req.progress.excluded[plan.cand.Key] = struct{}{}
		return openedAttempt{ready: nil, interleaved: req.interleaved}, nil
	}
	ranContextEligibility := e.EligibilityResolver != nil
	cat := catalogRouteTraceIfEnabled(e, evalOutcome.facts, evalOutcome.admitOut.admitRes.Capability, &modelcatalog.EligibilityDecision{IsEligible: true, Facts: evalOutcome.facts}, ranContextEligibility)
	e.notePlanCandidate(ctx, req.reqFacts.traceID, plan.cand.Key, cat)
	tx, err := e.startAttemptTx(ctx, req.reqFacts, req.routeFacts, plan.cand, req.progress.budget, req.progress.getFailures())
	if err != nil {
		return openedAttempt{interleaved: req.interleaved}, err
	}
	defer func() {
		if !tx.completed {
			tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, nil, "")
		}
	}()
	err = e.openAttemptTx(ctx, tx, evalOutcome, plan)
	if err != nil {
		return openedAttempt{interleaved: req.interleaved}, err
	}
	if tx.completed {
		req.progress.excluded[plan.cand.Key] = struct{}{}
		return openedAttempt{ready: nil, interleaved: req.interleaved}, nil
	}

	ready := tx.HandoffReady(pendingSelectionEffects{
		interleaved: req.interleaved,
	})

	if req.mode == openModeRetry {
		ready.setDefaultEvidence(usagelimit.ReleaseKindSwallowed, sdkterminal.CommandSwallowedAttempt, usageevidence.LegOutcomeSwallowed)
	}

	if err := tx.commitLaunchOrRegister(ctx, ready, req.reqFacts.aScope); err != nil {
		if errors.Is(err, leglifecycle.ErrALegCanceled) {
			req.progress.excluded[plan.cand.Key] = struct{}{}
			return openedAttempt{ready: nil, interleaved: req.interleaved}, err
		}
		return openedAttempt{interleaved: req.interleaved}, err
	}

	if err := e.commitInterleavedExecutorTurn(ctx, req.reqFacts.aLegID, plan.cand.InterleavedRole, evalOutcome.turn); err != nil {
		ready.Dispose(ctx, err)
		return openedAttempt{interleaved: req.interleaved}, err
	}
	interleaved, err := e.advanceInterleavedCycle(ctx, req.reqFacts.aLegID, req.interleaved, plan.nextCycle)
	if err != nil {
		ready.Dispose(ctx, err)
		return openedAttempt{interleaved: req.interleaved}, err
	}
	e.logInterleavedRouteSelected(ctx, req.reqFacts.traceID, tx.bleg.BLegID, plan.cand, req.interleaved.Cycle, interleaved.Cycle)
	ready.setPending(pendingSelectionEffects{
		interleaved: interleaved,
	})
	return openedAttempt{
		ready:       ready,
		interleaved: interleaved,
		turn:        evalOutcome.turn,
	}, nil
}

func (e *Executor) lookupAffinityBinding(ctx context.Context, traceID string, sel *routing.Selector, key affinity.Key, keyOK bool) (string, bool, error) {
	if e == nil || e.AffinityStore == nil || sel == nil || sel.Affinity == routing.AffinityNone || !keyOK {
		return "", false, nil
	}
	b, ok, err := e.AffinityStore.Get(ctx, key)
	if err != nil {
		return "", false, fmt.Errorf("executor: affinity lookup: %w", err)
	}
	backend := strings.TrimSpace(b.BackendID)
	if !ok || backend == "" {
		return "", false, nil
	}
	e.noteRouteDecision(ctx, traceID, "affinity_hit", backend)
	return backend, true, nil
}

func (e *Executor) clearAffinityBinding(ctx context.Context, traceID string, key affinity.Key, keyOK bool, reason string) {
	if e == nil || e.AffinityStore == nil || !keyOK {
		return
	}
	if err := e.AffinityStore.Delete(ctx, key); err != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "affinity binding delete failed", "error", err)
		}
		return
	}
	e.noteRouteDecision(ctx, traceID, "affinity_reset", strings.TrimSpace(reason))
}

func (e *Executor) requestSizeEstimateForRouting(ctx context.Context, sel *routing.Selector, call aipapi.Call) routing.RequestSizeEstimate {
	if e.Preflight != nil && routing.SelectorHasRequestSizeConstraints(sel) {
		var model, backend string
		if primary := firstSelectorPrimary(sel); primary != nil {
			model, backend = primary.Model, primary.Backend
		}
		if decision := e.Preflight.Check(ctx, accountingpreflight.Input{Backend: backend, Model: model, CallID: call.ID, Call: call}); decision.Err == nil && decision.Reason != accountingpreflight.ReasonDisabled {
			return routing.RequestSizeEstimate{Available: true, Tokens: int64(decision.Count.InputTokens) + 1, Basis: "token_accounting_preflight"}
		}
	}
	if !routing.SelectorHasRequestSizeConstraints(sel) || e.RequestTokenEstimator == nil {
		return routing.RequestSizeEstimate{}
	}
	est := e.RequestTokenEstimator.EstimateRequestTokens(ctx, call)
	return routing.RequestSizeEstimate{Available: est.Available, Tokens: est.Input, Basis: est.Basis}
}

func (e *Executor) runPreflight(ctx context.Context, traceID string, call aipapi.Call, c routing.AttemptCandidate, facts modelcatalog.ModelFacts) (accountingpreflight.Decision, bool) {
	if e.Preflight == nil {
		return accountingpreflight.Decision{}, false
	}
	return e.Preflight.Check(ctx, accountingpreflight.Input{
		Backend:                  c.Primary.Backend,
		Model:                    c.Primary.Model,
		CallID:                   traceID,
		Call:                     call,
		RequestedMaxOutputTokens: call.Options.MaxOutputTokens,
		Facts:                    facts,
	}), true
}

func firstSelectorPrimary(sel *routing.Selector) *routing.Primary {
	if sel == nil || len(sel.Alternatives) == 0 {
		return nil
	}
	alt := sel.Alternatives[0]
	if alt.Primary != nil {
		return alt.Primary
	}
	if alt.Weighted != nil {
		for i := range alt.Weighted.Branches {
			b := &alt.Weighted.Branches[i]
			if b.Parallel != nil {
				for j := range b.Parallel.Branches {
					if b.Parallel.Branches[j].Target.Model != "" {
						return &b.Parallel.Branches[j].Target
					}
				}
				continue
			}
			if b.Target.Model != "" {
				return &b.Target
			}
		}
	}
	if alt.Parallel != nil {
		for i := range alt.Parallel.Branches {
			if alt.Parallel.Branches[i].Target.Model != "" {
				return &alt.Parallel.Branches[i].Target
			}
		}
	}
	return nil
}
