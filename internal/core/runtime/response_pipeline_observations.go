package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	coretraffic "github.com/aiproxer/aiproxer/internal/core/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// responseEventInput is intentionally a narrow per-event boundary. It carries
// only the immutable request facts/current attempt and observer dependencies
// needed for one emitted response event; it is not a general runtime bag.
type responseEventInput struct {
	facts     recvTurnFacts
	attempt   *attemptSession
	recovery  *recoveryController
	pm        sdk.PartMeta
	committed bool
	now       time.Time

	// recorded is true for response_finished paths that ran mandatory recorder
	// preflight before authority finalization.
	recorded bool
	// finalObserved is true for private-publication events whose existing
	// fail-closed final-stream observation already ran as part of the staged batch
	// preflight. Only that observation is skipped; every other release effect —
	// local customer observation, PTC traffic, the client accumulator, affinity,
	// and compaction notification — still runs exactly once, here.
	finalObserved       bool
	finishBeforeRelease bool
	finishAfterRemember bool
}

// recvEventPreparation is the response-side portion of a backend event. It
// carries only values back to Recv; terminal and recovery decisions remain at
// that explicit coordination boundary.
type recvEventPreparation struct {
	event          aipapi.Event
	partMeta       sdk.PartMeta
	isTool         bool
	sourceID       string
	sourceFinished bool
	swallowed      bool
	err            error
}

// prepareRecvEvent accounts for and enriches one backend event. Attempt-local
// accounting is updated through the explicit attempt value, while response
// evidence and hooks stay owned by this pipeline. steerGate is the turn's
// correction gate for this event's call, built by the receive-loop coordinator
// from the production rails; it is nil for events that can never finalize a
// call, and a nil gate declines.
func (p *responsePipeline) prepareRecvEvent(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev aipapi.Event, steerGate func(name string, req toolcall.SteerRequest) bool) recvEventPreparation {
	prepared := recvEventPreparation{event: ev}
	if p == nil || attempt == nil {
		prepared.err = errNilRetryRecvStream
		return prepared
	}
	at := p.nowTime()
	attempt.observeAccountingBackendEvent(at, ev)
	attempt.observeLocalProviderEvent(ev)
	// Produced-output facts describe what this attempt produced so far,
	// whether or not it was ever released: the correction rails and the
	// empty-visible-turn terminal signal both read them. They are recorded
	// exactly once per prepared event, before any later stage can swallow it,
	// and no tool-call assembler is required for them to be complete (3.3, 3.4).
	attempt.observeProducedOutput(ev)
	if ev.Kind == aipapi.EventUsageDelta && ev.Accounting.DedupeKey != "" && !attempt.hasHostOnlyEconomicEvidenceSource() && !attempt.rememberUsageEvidenceOnceAs(ev, billingEvidenceRoleStream) {
		prepared.swallowed = true
		return prepared
	}
	attempt.observeAccountingUsage(ev)
	prepared.partMeta = facts.partMeta(attempt.bleg, attempt.cand)
	p.emitTraffic(ctx, attempt, sdktraffic.LegBTP, ev, prepared.partMeta)
	p.emitUsage(ctx, facts, attempt, ev)
	// Design Response Interception / Placement binds this order: BTP and provider
	// usage observation see the upstream control traffic first, then the private
	// capture consumes whatever it claims, and only what it does not claim may
	// reach the ordinary assembler and the client path below.
	if swallowed, err := p.divertControlCall(ctx, attempt, ev); swallowed || err != nil {
		prepared.swallowed, prepared.err = swallowed, err
		return prepared
	}
	if toolFinal := attempt.toolCallAssembler(); toolFinal != nil && toolFinal.enabled() {
		meta := toolcall.Meta{TraceID: facts.traceID, ALegID: facts.aLegID, BLegID: attempt.bleg.BLegID, AttemptSeq: attempt.bleg.Seq}
		// Completed tool-call finalizers read the same authoritative request
		// views the tool policy plane, the tool reactor plane, and the completion
		// gate plane read. All four planes derive them from the one frozen
		// recvViews snapshot through cloneRecvViews: completion gates here,
		// tool policies in applyToolPolicies, and reactors via the hookMeta copy
		// plus the projected context ApplyToolReactors re-reads. viewsFor returns
		// a fresh detached copy per call, so a finalizer cannot reach this
		// request's producer snapshot or a sibling consumer through the maps and
		// slices it is handed. Nothing here is derived from client-supplied
		// metadata.
		//
		// An absent snapshot is a supported state, not a failure: viewsFor then
		// reports false, the additive views stay zero, and that is exactly what a
		// finalizer already observed for such a request. Failing the request here
		// would also diverge from the sibling population sites elsewhere in this
		// file, which guard viewsFor the same way.
		if views, ok := facts.viewsFor(ctx); ok {
			meta.Scope, meta.Session, meta.Workspace = views.Scope, views.Session, views.Workspace
		}
		held, err := toolFinal.ingest(ctx, ev, meta, steerGate)
		if err != nil {
			p.clearToolClassification()
			prepared.err = err
			return prepared
		}
		if held {
			prepared.swallowed = true
			return prepared
		}
	}
	return prepared
}

// hasHostOnlyEconomicEvidenceSource identifies streams whose provider-owned
// host-only sideband (V1 or V2) is the authoritative durable evidence path.
// Canonical usage events remain available to observers and clients, but must
// not also enter the legacy V1 terminal accumulator for the same quantities.
func hasHostOnlyEconomicEvidenceSource(inner aipapi.ManagedEventStream) bool {
	if inner == nil {
		return false
	}
	if state, ok := inner.(interface{ AccountingEvidenceEnabled() bool }); ok {
		return state.AccountingEvidenceEnabled()
	}
	if _, ok := inner.(aipapi.UsageEvidenceSource); ok {
		return true
	}
	if _, ok := inner.(execbackend.EconomicEvidenceSource); ok {
		return true
	}
	_, ok := inner.(metering.ObservationSource)
	return ok
}

// clientEventTransformation applies response-side tool policy/reactors and
// hooks. Gate buffering is returned as data so Recv can sequence
// terminal/recovery ownership explicitly.
type clientEventTransformation struct {
	event          aipapi.Event
	partMeta       sdk.PartMeta
	isTool         bool
	sourceID       string
	sourceFinished bool
	gates          []completion.Gate
	swallowed      bool
	err            error
}

type gatedEventResult struct {
	event           aipapi.Event
	replaced        bool
	finishPreflight bool
	pending         *pendingCompletion
	recording       responseRecordingResult
	err             error
}

// applyCompletionGates owns gate buffering and response-side preflight. It
// deliberately returns terminal work as data for Recv to sequence.
func (p *responsePipeline) applyCompletionGates(ctx context.Context, gates []completion.Gate, facts recvTurnFacts, attempt *attemptSession, ev aipapi.Event, committed bool) gatedEventResult {
	result := gatedEventResult{}
	if p == nil || attempt == nil {
		result.err = errNilRetryRecvStream
		return result
	}
	if ev.Kind == aipapi.EventResponseFinished {
		// A valid proxy-owned completion prepares its whole evaluated candidate
		// here, while the already-transformed buffer is still intact. Nothing is
		// exposed and nothing is recorded: the accepted normal request-terminal
		// winner owns both, and it records the complete canonical batch itself.
		prep, err := p.preparePendingCompletion(ctx, facts, attempt, ev, gates, committed)
		if err != nil {
			result.err = err
			return result
		}
		if prep.ordinary.Kind != "" {
			// The evaluated completion-gate output is authoritative and carries no
			// accepted finish. It is released through the ordinary authoritative
			// gate-drain path; the chain is never evaluated a second time.
			return gatedEventResult{event: prep.ordinary, replaced: prep.replaced}
		}
		if prep.prepared != nil {
			// The terminal chokepoint and every downstream effect need the ACTUAL
			// accepted finish, never a zero event.
			return gatedEventResult{
				event: prep.prepared.finish, pending: prep.prepared, finishPreflight: true,
			}
		}
	}
	snap := p.completionSnapshot(ctx)
	meta := completion.Meta{TraceID: facts.traceID, ALegID: facts.aLegID, BLegID: attempt.bleg.BLegID, AttemptSeq: attempt.bleg.Seq}
	if views, ok := facts.viewsFor(ctx); ok {
		meta.Scope, meta.Session, meta.Workspace = views.Scope, views.Session, views.Workspace
	}
	services := completion.Services{}
	if snap != nil {
		services.State, services.Aux = snap.State(), snap.Aux()
	}
	out, replaced, err := p.completionGatedEmit(ctx, gates, ev, responseGateInput{
		meta: meta, services: services, stageLog: p.log, committed: committed, limits: completionBufferLimitsFor(p),
	})
	if errors.Is(err, errGateContinueInner) {
		result.finishPreflight = false
		result.err = errGateContinueInner
		return result
	}
	if err != nil {
		result.err = err
		return result
	}
	result.event, result.replaced = out, replaced
	if replaced {
		views, ok := facts.viewsFor(ctx)
		if err := p.cycleFinalStreamObservation(ctx, facts, attempt, views, ok, response.OutcomeGateReplaced, committed); err != nil {
			result.err = err
			return result
		}
	}
	if out.Kind == aipapi.EventResponseFinished {
		result.recording = p.recordClientFacing(ctx, facts, attempt, out, committed)
		if result.recording.mandatory() {
			p.finishFinalStreamObservation(ctx, attempt, response.OutcomeFailed)
			result.err = result.recording.err
			return result
		}
		result.finishPreflight = true
	}
	return result
}

func (p *responsePipeline) transformClientEvent(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev aipapi.Event, prepared recvEventPreparation) clientEventTransformation {
	// Part metadata carries request and attempt identity only, so it is rebuilt
	// from the frozen facts rather than read out of the tool metadata's detached
	// view clone. A caller that hands over a bare preparation still gets it.
	partMeta := prepared.partMeta
	if attempt != nil {
		partMeta = facts.partMeta(attempt.bleg, attempt.cand)
	}
	out := clientEventTransformation{event: ev, partMeta: partMeta, sourceID: prepared.sourceID, sourceFinished: prepared.sourceFinished, err: prepared.err, swallowed: prepared.swallowed}
	if out.err != nil || out.swallowed || p == nil || attempt == nil {
		return out
	}
	if te, ok := aipapi.ToolEventFromEvent(ev); ok {
		// Only a tool event reaches the tool policies and reactors that read the
		// frozen request views, so only a tool event pays for the detached copy.
		_, tm := facts.hookMeta(attempt.bleg, attempt.cand)
		out.isTool = true
		out.sourceID = te.ToolCallID
		out.sourceFinished = te.Kind == aipapi.ToolEventFinished
		p.enrichToolEvent(&te)
		if err := p.applyToolPolicies(ctx, facts, te, tm); err != nil {
			out.err = err
			return out
		}
		res := p.bus.ApplyToolReactors(ctx, te, tm)
		if res.Err != nil {
			out.err = res.Err
			return out
		}
		if !res.Emit {
			if te.Kind == aipapi.ToolEventFinished {
				p.forgetToolClassification(te.ToolCallID)
			}
			out.swallowed = true
			return out
		}
		p.rememberEffectiveTool(te.ToolCallID, res.Event)
		if res.Event.Kind != "" {
			out.event = aipapi.MergeToolEventInto(ev, res.Event)
		}
	}
	// The hook seam takes the event by pointer because a response-part hook may
	// replace it. The pipeline-owned copy keeps that pointer off the heap: a
	// function-local event is moved to the heap for every delivered event, and
	// an address taken from the returned struct moves every field of it there
	// instead. Recv is the only writer of this scratch.
	hookEvent := &p.hookEvent
	*hookEvent = out.event
	if err := p.bus.RunResponsePartHooks(ctx, hookEvent, out.partMeta); err != nil {
		out.err = err
		return out
	}
	out.event = *hookEvent
	if out.isTool {
		p.observeToolFinalName(out.sourceID, out.event)
		if out.sourceFinished {
			p.forgetToolClassification(out.sourceID)
		}
	}
	out.gates = p.completionGatesFromContext(ctx)
	return out
}

// observeClientFacing runs the response-owned observation/release sequence.
// It never mutates terminal authority; callers apply terminal/recovery policy
// from the returned recording result and error.
func (p *responsePipeline) observeClientFacing(ctx context.Context, ev aipapi.Event, in responseEventInput) (aipapi.Event, responseRecordingResult, error) {
	if p == nil {
		return aipapi.Event{}, responseRecordingResult{}, errNilRetryRecvStream
	}
	if in.attempt == nil {
		return aipapi.Event{}, responseRecordingResult{}, errNilRetryRecvStream
	}
	// ev is already the post-hook/customer-projection event at this boundary.
	// Capture only bounded properties; the provider-origin plane was captured in
	// prepareRecvEvent before any customer transformation.
	in.attempt.observeLocalCustomerEvent(ev)
	if !in.finalObserved {
		if err := extensions.RunFinalStreamObservationStage(ctx, p.log, p.extensionMetrics, in.attempt.finalStreamObs, ev, in.committed); err != nil {
			p.finishFinalStreamObservation(ctx, in.attempt, response.OutcomeFailed)
			return aipapi.Event{}, responseRecordingResult{}, err
		}
	}

	recording := responseRecordingResult{outcome: responseRecordingSkipped}
	if !in.recorded {
		recording = p.recordClientFacing(ctx, in.facts, in.attempt, ev, in.committed)
		if recording.failed() && recording.mandatory() {
			p.finishFinalStreamObservation(ctx, in.attempt, response.OutcomeFailed)
			return aipapi.Event{}, recording, recording.err
		}
		if recording.err != nil && p.log != nil {
			p.log.DebugContext(ctx, "secure_session recorder stream", "error", recording.err)
		}
	}

	// Release tail: the effects that must run exactly once per DELIVERED event and
	// never while staging. PTC traffic, the client accumulator, affinity, and
	// compaction notification run here for the first time, in the established
	// order, after the final observation. A compaction preserver may rewrite the
	// event inside that order, so the event the client receives is the released
	// one.
	releaseDispatch, released := p.emitTrafficPTCFinal(ctx, in.facts, in.attempt, ev, in.pm)
	ev = released
	p.rememberClientEvent(ev)
	p.commitAffinityIfOutput(ctx, in.recovery, in.facts.traceID, in.attempt, in.now, ev)
	p.notifyCompactionAfterRelease(ctx, ev, releaseDispatch)
	return ev, recording, nil
}

func (p *responsePipeline) observeSynthesizedUsage(ctx context.Context, ev aipapi.Event, request requestTerminalFacts, attempt *attemptSession, pm sdk.PartMeta, committed bool) (aipapi.Event, responseRecordingResult, error) {
	if p == nil || attempt == nil {
		return aipapi.Event{}, responseRecordingResult{}, errNilRetryRecvStream
	}
	ctx = request.toRecvTurnFacts(ctx).projectContext(ctx, p.log)
	if err := extensions.RunFinalStreamObservationStage(ctx, p.log, p.extensionMetrics, attempt.finalStreamObs, ev, committed); err != nil {
		p.finishFinalStreamObservation(ctx, attempt, response.OutcomeFailed)
		return aipapi.Event{}, responseRecordingResult{}, err
	}
	recording := p.recordClientFacingTerminal(ctx, request, attempt, ev, committed)
	if recording.failed() && recording.mandatory() {
		p.finishFinalStreamObservation(ctx, attempt, response.OutcomeFailed)
		return aipapi.Event{}, recording, recording.err
	}
	releaseDispatch, released := p.emitTrafficPTCFinalEvidence(ctx, request.responseEvidence(), attempt, ev, pm)
	ev = released
	p.rememberClientEvent(ev)
	p.notifyCompactionAfterRelease(ctx, ev, releaseDispatch)
	return ev, recording, nil
}

// commitAffinityIfOutput commits the first affinity binding once output is committed.
// It takes the request identity rather than a terminal snapshot: this runs on every
// delivered event, and the only read below is the trace id used for the route-decision note.
func (p *responsePipeline) commitAffinityIfOutput(ctx context.Context, recovery *recoveryController, traceID string, attempt *attemptSession, now time.Time, ev aipapi.Event) {
	if aipapi.OutputCommitted(ev) && recovery != nil {
		if now.IsZero() {
			now = time.Now()
		}
		recovery.commitAffinity(ctx, traceID, attempt, now, "output_committed")
	}
}

func (p *responsePipeline) emitTrafficPTCFinal(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev aipapi.Event, pm sdk.PartMeta) (compactionReleaseDispatch, aipapi.Event) {
	return p.emitTrafficPTCFinalEvidence(ctx, facts.responseEvidence(), attempt, ev, pm)
}

// emitTrafficPTCFinalEvidence runs the final PTC traffic observation for one
// released event and returns the event a compaction preserver may have
// rewritten. The event travels by value so that the common path, which has no
// preserver to apply, never takes its address and heap-allocates the 640-byte
// canonical event per delivered event.
func (p *responsePipeline) emitTrafficPTCFinalEvidence(ctx context.Context, evidence responseRequestEvidence, attempt *attemptSession, ev aipapi.Event, pm sdk.PartMeta) (compactionReleaseDispatch, aipapi.Event) {
	if ev.Kind == aipapi.EventWarning && ev.WarningCode == stream.KeepaliveEventCode {
		return compactionReleaseDispatch{}, ev
	}
	dispatch, ev := p.observeCompactionReleaseFinalEvidence(ctx, evidence, attempt, ev)
	p.emitTraffic(ctx, attempt, sdktraffic.LegPTC, ev, pm)
	return dispatch, ev
}

func (p *responsePipeline) emitTraffic(ctx context.Context, attempt *attemptSession, leg sdktraffic.Leg, ev aipapi.Event, pm sdk.PartMeta) {
	if p == nil || p.runtimeSnapshot == nil || attempt == nil {
		return
	}
	bundle := coretraffic.PortBundleFromSnapshot(p.runtimeSnapshot)
	if bundle.EmitIsNoop() {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		if p.log != nil {
			p.log.DebugContext(ctx, "response pipeline traffic marshal skipped", "leg", leg, "error", err)
		}
		return
	}
	sc := scopeFromCtx(ctx)
	meta := sdktraffic.CaptureMeta{
		TraceID:     pm.TraceID,
		ALegID:      pm.ALegID,
		BLegID:      pm.BLegID,
		AttemptSeq:  pm.AttemptSeq,
		BackendID:   strings.TrimSpace(attempt.cand.Primary.Backend),
		PrincipalID: strings.TrimSpace(sc.PrincipalID.String()),
		Scope:       sc,
	}
	bundle.Emit(ctx, leg, meta, "aip/canonical+json", "application/json", b)
}

// emitUsage observes provider/customer usage without importing settlement or
// terminal authority. Attempt identity is explicit so replacement evidence
// cannot be attributed to the current slot accidentally.
func (p *responsePipeline) emitUsage(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev aipapi.Event) {
	p.emitUsageEvidence(ctx, facts.responseEvidence(), facts.baseline.Session.CorrelationID(), attempt, ev)
}

// emitUsageTerminal observes terminal usage from already-extracted request evidence.
// Callers on the per-event drain path pass the frozen facts projection directly so the
// hot path never builds a terminal snapshot (three full call clones) just to read
// trace id, A-leg id, session correlation and secure-turn evidence.
func (p *responsePipeline) emitUsageTerminal(ctx context.Context, evidence responseRequestEvidence, sessionID string, attempt *attemptSession, ev aipapi.Event) {
	p.emitUsageEvidence(ctx, evidence, sessionID, attempt, ev)
}

func (p *responsePipeline) emitUsageEvidence(ctx context.Context, evidence responseRequestEvidence, sessionID string, attempt *attemptSession, ev aipapi.Event) {
	if p == nil || p.runtimeSnapshot == nil || ev.Kind != aipapi.EventUsageDelta || attempt == nil {
		return
	}
	obs := p.runtimeSnapshot.UsageObserver()
	if obs == nil {
		return
	}
	scopeView := scopeFromCtx(ctx)
	principalID := ""
	if scopeView.PrincipalID.IsKnown() {
		principalID = strings.TrimSpace(scopeView.PrincipalID.String())
	}
	// Clone the digest slice per emission: the usage fan-out shares the
	// Event value across observers, and a mutated shared backing array
	// would corrupt another observer's diagnostic evidence. Nil stays nil,
	// so the disabled gate pays no allocation.
	var seamDigests [][32]byte
	if len(attempt.promptCache.seamDigests) > 0 {
		seamDigests = append([][32]byte(nil), attempt.promptCache.seamDigests...)
	}
	if err := obs.OnUsage(ctx, usage.Event{
		TraceID: strings.TrimSpace(evidence.traceID), ALegID: strings.TrimSpace(evidence.aLegID),
		BLegID: strings.TrimSpace(attempt.bleg.BLegID), PrincipalID: principalID,
		SessionID: strings.TrimSpace(sessionID), AttemptSeq: int(attempt.bleg.Seq),
		BackendID: strings.TrimSpace(attempt.cand.Primary.Backend), Model: strings.TrimSpace(attempt.cand.Primary.Model),
		StructuralCause:          strings.TrimSpace(attempt.structuralCause),
		SeamDigests:              seamDigests,
		SystemPromptDigest:       attempt.promptCache.systemPromptDigest,
		PromptCacheLaunchSeq:     attempt.promptCache.systemPromptLaunchSeq,
		CacheIdentityDigest:      attempt.switchIdentity.core,
		CacheTargetDigest:        attempt.switchIdentity.cacheTarget,
		CredentialIdentityDigest: attempt.switchIdentity.openCredential,
		ProviderAccountDigest:    providerAccountSwitchDigest(attempt.switchDigestKey, ev.Accounting.ProviderAccountKey),
		Scope:                    scopeView.Clone(), InputTokens: ev.InputTokens, OutputTokens: ev.OutputTokens,
		CacheReadTokens: ev.CacheReadTokens, CacheWriteTokens: ev.CacheWriteTokens,
		ReasoningTokens: ev.ReasoningTokens, TotalTokens: ev.TotalTokens,
		CostNanoUnits: ev.CostNanoUnits, Currency: strings.TrimSpace(ev.Currency),
		CostSource: strings.TrimSpace(ev.CostSource), RawUsageJSON: strings.TrimSpace(ev.RawUsageJSON),
		RecordedAt: p.nowTime(),
	}); err != nil && p.log != nil {
		p.log.DebugContext(ctx, "usage observer error", "error", err)
	}
}

// consumeBackendUsageEvidenceForAttempt keeps provider sideband evidence
// attached to the attempt that produced the source stream. The response owner
// records and emits the evidence; the attempt owns dedupe and accounting.
func (p *responsePipeline) consumeBackendUsageEvidenceForAttempt(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, inner aipapi.ManagedEventStream) {
	if p == nil || attempt == nil || inner == nil {
		return
	}
	if source, ok := inner.(aipapi.UsageEvidenceSource); ok {
		for _, ev := range source.DrainUsageEvidence() {
			if ev.Kind != aipapi.EventUsageDelta || !attempt.rememberUsageEvidenceOnceAs(ev, billingEvidenceRoleSideband) {
				continue
			}
			p.rememberInternalUsage(ev)
			attempt.observeAccountingUsage(ev)
			p.emitUsage(ctx, facts, attempt, ev)
		}
	}
	if source, ok := inner.(execbackend.EconomicEvidenceSource); ok {
		for _, evidence := range source.DrainEconomicEvidenceRecords() {
			if reason := strings.TrimSpace(evidence.RejectionReason); reason != "" {
				attempt.rememberEconomicEvidenceOnce(evidence)
				logConnectorEvidenceRejection(ctx, p.log, attempt, reason)
				continue
			}
			attempt.rememberEconomicEvidenceOnce(evidence)
		}
	} else if source, ok := inner.(metering.ObservationSource); ok {
		for _, observation := range source.DrainEconomicObservations() {
			attempt.rememberEconomicObservationOnce(observation)
		}
	}
}

func (p *responsePipeline) nowTime() time.Time {
	if p != nil && p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *responsePipeline) keepaliveEvent() aipapi.Event {
	return stream.DefaultKeepaliveEvent()
}

// withDecisionEvidence projects the response-stage evidence seam into a
// receive context. Terminal commitment remains owned by turnTerminal.
func (p *responsePipeline) withDecisionEvidence(ctx context.Context, terminal *turnTerminal) context.Context {
	if p == nil || p.runtimeSnapshot == nil {
		return ctx
	}
	snap := p.runtimeSnapshot
	emitter := p.policyEvidenceEmitter(snap)
	ev := &extensions.DecisionEvidence{
		Emitter:               emitter,
		TimeoutBudget:         snap.TimeoutBudgetSource(),
		TimeoutGuard:          snap.ProviderTimeoutGuard(),
		OutputCommittedSource: func() bool { return terminal != nil && terminal.committed() },
	}
	ctx = extensions.WithDecisionEvidence(ctx, ev)
	ctx = hooks.WithToolReactorEvidence(ctx, extensions.NewToolReactorEvidenceFunc(ev))
	ctx = extensions.WithAttemptEvidence(ctx, extensions.NewAttemptEvidenceFunc(ev))
	return ctx
}

func (p *responsePipeline) completionSnapshot(ctx context.Context) *extensions.RequestRuntimeSnapshot {
	if snap := extensions.RequestRuntimeSnapshotFromContext(ctx); snap != nil {
		return snap
	}
	if p != nil {
		return p.runtimeSnapshot
	}
	return nil
}

func (p *responsePipeline) completionGatesFromContext(ctx context.Context) []completion.Gate {
	var fallback extensions.CompletionGatesView
	if p != nil {
		fallback = p.runtimeSnapshot
	}
	return extensions.CompletionGatesFromContext(ctx, fallback)
}

func (p *responsePipeline) applyToolPolicies(ctx context.Context, facts recvTurnFacts, te aipapi.ToolEvent, meta sdk.ToolMeta) error {
	if p == nil || p.runtimeSnapshot == nil {
		return nil
	}
	policies := p.runtimeSnapshot.ToolCallPoliciesExecution()
	if len(policies) == 0 {
		return nil
	}
	polMeta := toolpolicy.Meta{
		TraceID: strings.TrimSpace(meta.TraceID), ALegID: strings.TrimSpace(meta.ALegID),
		BLegID: strings.TrimSpace(meta.BLegID), AttemptSeq: meta.AttemptSeq,
		Principal: meta.Principal, Scope: meta.Scope, Session: meta.Session, Workspace: meta.Workspace,
	}
	if v, ok := facts.viewsFor(ctx); ok {
		polMeta.Principal, polMeta.Scope, polMeta.Session, polMeta.Workspace = v.Principal, v.Scope, v.Session, v.Workspace
	}
	return extensions.RunToolPolicyStage(extensions.ToolPolicyStageInput{
		Ctx: ctx, Log: p.log, Obs: p.extensionMetrics, Policies: policies, Event: te, Meta: polMeta,
		Svc: toolpolicy.Services{State: p.runtimeSnapshot.State(), Aux: p.runtimeSnapshot.Aux()},
	})
}
