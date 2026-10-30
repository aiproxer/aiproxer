package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	secureapp "github.com/aiproxer/aiproxer/internal/core/securesession/app"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
)

// responsePipeline owns mutable evidence and client-event state for one logical
// response. Recv is the only writer during normal streaming; eventsMu gives
// terminal/Close a short, coherent snapshot boundary. It has no terminal or
// commitment authority.
type responsePipeline struct {
	eventsMu sync.Mutex

	// Concrete response-side dependencies are captured at composition time. No
	// request operation needs the broad Executor service surface.
	bus                      *hooks.Bus
	log                      *slog.Logger
	now                      func() time.Time
	runtimeSnapshot          *extensions.RequestRuntimeSnapshot
	extensionMetrics         extensions.StageMetrics
	policyDiagnostics        bool
	policyEvidenceEmitter    func(*extensions.RequestRuntimeSnapshot) *extensions.EvidenceEmitter
	streamUsage              *accountingstream.Reconstructor
	secureSessionRecorder    secureapp.GateRecording
	secureSessionMetrics     SecureSessionMetrics
	secureRecordingMandatory bool
	backends                 map[string]execbackend.Backend
	detector                 CompactionDetector
	compactionObservers      []compaction.Observer
	compactionPreservers     []compaction.Preserver
	compactionServices       compaction.Services
	promptCacheMaintenance   PromptCacheMaintenance
	completionBufferLimits   completion.BufferLimits

	customer    *customerEvidenceAccumulator
	seenEvents  []aipapi.Event
	visibleText strings.Builder

	gateBuf   []aipapi.Event
	gateDrain []aipapi.Event
	gateLive  bool

	recoverDrain []aipapi.Event

	// hookEvent is the addressable copy of the in-flight client event that the
	// response-part hook seam may rewrite. Recv is the pipeline's single writer
	// during streaming, so the event is retained once per response instead of
	// being moved to the heap for every delivered event.
	hookEvent aipapi.Event

	// pending owns this turn's private pending-completion publication state as one
	// cohesive value: the prepared bounded candidate, the once-only accepted
	// publication claim, the refreshed customer usage, and the already-observed
	// events that still have to reach the client in canonical order. It is held
	// until the accepted normal request-terminal winner publishes it, and dropped
	// whole by every other path. The zero value is already correct, so a directly
	// constructed pipeline needs no allocation ceremony and every seam is total.
	pending pendingPublicationState

	// toolClass and committedTools live for the logical response. The active
	// attempt's assembler/finalizer remains on attemptSession and is replaced
	// with every B-leg.
	toolClass      toolEventClassificationState
	committedTools []aipapi.ToolEvent

	// compactionOpenMeta is request-side response correlation carried into the
	// final release seam. It is response evidence, not terminal authority.
	compactionOpenMeta compaction.PreservationMeta

	// recordingOutcome is the last typed secure-recording result. It lets the
	// caller apply replacement/terminal policy without a second hard-stop flag.
	recordingOutcome responseRecordingOutcome

	// continuationLifecyclePending marks the next B-leg as a continuation of
	// the already-started logical response. Its provider lifecycle markers must
	// not be published a second time to the client-facing stream.
	continuationLifecyclePending bool

	lastAuthorityUsage aipapi.Event
	lastCustomerUsage  aipapi.Event
	keepwarmArmOnce    sync.Once

	// recvProjection is the last frozen-facts receive projection and the parent
	// context it was derived from. Written only by Recv, which is
	// single-goroutine by contract.
	recvProjection recvProjection

	terminalSnapshot func() (committed, accountingFinalized bool)
	customerUsageFn  func(context.Context, string, []aipapi.Event) aipapi.Event
}

// recvProjection is the last context projection Recv built from the frozen
// request facts, together with the parent context it was derived from. It owns
// its own update so the memo stays one cohesive value on the pipeline.
type recvProjection struct {
	parent context.Context
	ctx    context.Context
}

// lookup returns the projection already built for parent, if any.
func (m *recvProjection) lookup(parent context.Context) (context.Context, bool) {
	if sameContextValue(m.parent, parent) {
		return m.ctx, true
	}
	return nil, false
}

// remember records the projection derived from parent.
func (m *recvProjection) remember(parent, projected context.Context) {
	m.parent, m.ctx = parent, projected
}

// projectedRecvContext returns the frozen-facts projection of parent, reusing the
// projection built for the previous receive when the caller passes the same
// parent context again.
//
// A projection is a pure function of the frozen request facts plus the parent it
// is derived from: every fact-owned key is overwritten with the frozen value, and
// every other key still resolves through the parent chain at read time. Facts are
// frozen for the logical response, so one projection serves every delivered event
// of that parent instead of re-cloning the request view maps and rebuilding the
// value chain per event. Cancellation still propagates, because the reused
// projection is derived from that same parent.
func (p *responsePipeline) projectedRecvContext(parent context.Context, facts recvTurnFacts) context.Context {
	if p == nil {
		return parent
	}
	if projected, ok := p.recvProjection.lookup(parent); ok {
		return projected
	}
	projected := facts.projectContext(parent, p.log)
	p.recvProjection.remember(parent, projected)
	return projected
}

// sameContextValue reports whether two contexts are the same value. Comparing
// interfaces panics when the dynamic type is not comparable, so a parent whose
// type is not comparable reports a miss and is projected again rather than
// risking that panic on the receive path.
func sameContextValue(a, b context.Context) bool {
	if a == nil || b == nil {
		return false
	}
	ta := reflect.TypeOf(a)
	return ta == reflect.TypeOf(b) && ta.Comparable() && a == b
}

func newResponsePipeline(openMeta ...compaction.PreservationMeta) *responsePipeline {
	p := &responsePipeline{customer: newCustomerEvidenceAccumulator()}
	if len(openMeta) > 0 {
		p.compactionOpenMeta = openMeta[0]
	}
	return p
}

func newResponsePipelineForExecutor(executor *Executor, openMeta ...compaction.PreservationMeta) *responsePipeline {
	p := newResponsePipeline(openMeta...)
	if executor == nil {
		return p
	}
	p.log = executor.Log
	p.bus = executor.Bus
	p.now = executor.now
	p.runtimeSnapshot = executor.RuntimeSnapshot
	p.extensionMetrics = executor.ExtensionMetrics
	p.policyDiagnostics = executor.PolicyDiagnosticsEnabled
	p.policyEvidenceEmitter = executor.policyEvidenceEmitter
	p.streamUsage = executor.StreamUsage
	p.secureSessionRecorder = executor.SecureSessionRecorder
	p.secureSessionMetrics = executor.SecureSessionMetrics
	p.secureRecordingMandatory = executor.SecureSessionRecordingMandatory
	p.backends = executor.Backends
	p.detector = executor.CompactionDetector
	if executor.RuntimeSnapshot != nil {
		p.compactionObservers = executor.RuntimeSnapshot.CompactionObservers()
		p.compactionPreservers = executor.RuntimeSnapshot.CompactionPreservers()
		p.runtimeSnapshot = executor.RuntimeSnapshot
		p.compactionServices.State = executor.RuntimeSnapshot.State()
	}
	p.compactionServices.BackgroundAux = executor.BackgroundAux
	p.promptCacheMaintenance = executor.PromptCacheMaintenance
	p.completionBufferLimits = executor.CompletionBufferLimits
	return p
}

func completionBufferLimitsFor(p *responsePipeline) completion.BufferLimits {
	if p == nil {
		return completion.BufferLimits{}
	}
	return p.completionBufferLimits
}

func (p *responsePipeline) ensure() {
	if p == nil {
		return
	}
	if p.customer == nil {
		p.customer = newCustomerEvidenceAccumulator()
	}
}

// bound reports whether the production stream hooks have been installed. The
// assembly paths bind them immediately after constructing the stream; the
// guarded fallback is only for focused tests that build retryRecvStream
// literals directly.
func (p *responsePipeline) bound() bool {
	if p == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.terminalSnapshot != nil
}

func (p *responsePipeline) bindTerminalSnapshot(snapshot func() (bool, bool)) {
	if p == nil || snapshot == nil {
		return
	}
	p.eventsMu.Lock()
	p.terminalSnapshot = snapshot
	p.eventsMu.Unlock()
}

func (p *responsePipeline) bindCustomerUsage(fn func(context.Context, string, []aipapi.Event) aipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.customerUsageFn = fn
	p.eventsMu.Unlock()
}

func (p *responsePipeline) rememberInternalUsage(ev aipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.seenEvents = append(p.seenEvents, ev)
	p.eventsMu.Unlock()
}

func (p *responsePipeline) rememberClientEvent(ev aipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.ensure()
	if ev.Kind == aipapi.EventResponseFinished {
		for _, seen := range p.seenEvents {
			if seen.Kind == aipapi.EventResponseFinished {
				p.eventsMu.Unlock()
				return
			}
		}
	}
	if p.customer != nil {
		p.customer.ObserveReleased(ev)
	} else if ev.Kind == aipapi.EventTextDelta {
		p.visibleText.WriteString(ev.Delta)
	}
	if tool, ok := aipapi.ToolEventFromEvent(ev); ok {
		p.committedTools = append(p.committedTools, tool)
	}
	p.seenEvents = append(p.seenEvents, ev)
	p.eventsMu.Unlock()
}

func (p *responsePipeline) usageEventsSnapshot() []aipapi.Event {
	if p == nil {
		return nil
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return tokenAccountingUsageEvents(append([]aipapi.Event(nil), p.seenEvents...))
}

func (p *responsePipeline) seenEventsCopy() []aipapi.Event {
	if p == nil {
		return nil
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return append([]aipapi.Event(nil), p.seenEvents...)
}

func (p *responsePipeline) clearClientAccumulators() {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	p.ensure()
	p.seenEvents = nil
	p.visibleText.Reset()
	if p.customer != nil {
		p.customer.resetContent()
	}
}

// clearToolClassification drops correlation for an attempt transition or
// terminal cleanup. The logical committed-tool evidence remains available for
// the successful-turn keep-warm handoff.
func (p *responsePipeline) clearToolClassification() {
	if p == nil {
		return
	}
	p.toolClass.clear()
}

func clearAttemptToolState(p *responsePipeline, attempt *attemptSession) {
	if attempt != nil {
		attempt.discardSidebandState()
	}
	if p != nil {
		p.clearToolClassification()
	}
}

// clearAttemptState is the attempt-retirement cleanup chokepoint. It extends the
// existing ordinary tool cleanup with the private pending-completion publication,
// so Close, cancellation, attempt loss, and replacement dispose the prepared
// candidate, the staged batch, and the undelivered drain together.
//
// It is deliberately NOT reached from the winning attempt's ordinary normal
// cleanup: a legitimately winning pending snapshot survives until its accepted
// callback, its activation, or its own retirement path disposes it.
func (p *responsePipeline) clearAttemptState(attempt *attemptSession) {
	clearAttemptToolState(p, attempt)
	p.discardPendingCompletion()
}

func (p *responsePipeline) enrichToolEvent(te *aipapi.ToolEvent) {
	if p != nil {
		p.toolClass.enrich(te)
	}
}

func (p *responsePipeline) forgetToolClassification(id string) {
	if p != nil {
		p.toolClass.forget(id)
	}
}

func (p *responsePipeline) observeToolFinalName(id string, ev aipapi.Event) {
	if p != nil {
		p.toolClass.observeFinalName(id, ev)
	}
}

func (p *responsePipeline) rememberEffectiveTool(id string, ev aipapi.ToolEvent) {
	if p != nil {
		p.toolClass.rememberEffective(id, ev)
	}
}

func (p *responsePipeline) committedToolEventsSnapshot() []aipapi.ToolEvent {
	if p == nil {
		return nil
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return append([]aipapi.ToolEvent(nil), p.committedTools...)
}

func (p *responsePipeline) recordingBlocksReplacement() bool {
	if p == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.recordingOutcome == responseRecordingMandatoryPostCommitFailure
}

func (p *responsePipeline) setRecordingOutcome(outcome responseRecordingOutcome) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.recordingOutcome = outcome
	p.eventsMu.Unlock()
}

func (p *responsePipeline) markContinuationLifecyclePending() {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.continuationLifecyclePending = true
	// Snapshot the attempt boundary now: the newly published attempt has
	// released nothing yet, so the current cumulative text is exactly the prior
	// attempts' share. Every continuation re-snapshots, so the boundary always
	// names the start of the current attempt. A nil customer is a no-op; the
	// boundary stays unset and the projection falls back to the cumulative
	// text.
	p.customer.markAttemptBoundary()
	p.eventsMu.Unlock()
}

func (p *responsePipeline) consumeContinuationLifecycleMarker(ev aipapi.Event) bool {
	if p == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if !p.continuationLifecyclePending {
		return false
	}
	if ev.Kind == aipapi.EventResponseStarted || ev.Kind == aipapi.EventMessageStarted {
		return true
	}
	p.continuationLifecyclePending = false
	return false
}

// resetForReplacement clears evidence whose lifetime is one B-leg. Gate and
// recovery queues intentionally remain untouched; their ordering spans the
// logical response even when the backend attempt changes.
func (p *responsePipeline) resetForReplacement() {
	if p == nil {
		return
	}
	// A replacement retires the current attempt, so its private candidate, staged
	// batch, and drain are disposed before the new B-leg is prepared.
	p.discardPendingCompletion()
	p.eventsMu.Lock()
	p.ensure()
	p.seenEvents = nil
	p.visibleText.Reset()
	p.lastAuthorityUsage = aipapi.Event{}
	p.lastCustomerUsage = aipapi.Event{}
	if p.customer != nil {
		p.customer.resetContent()
	}
	p.eventsMu.Unlock()
}

func (p *responsePipeline) releasedOutputText() string {
	if p == nil {
		return ""
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	p.ensure()
	if p.customer != nil {
		text, _, _, _ := p.customer.Snapshot()
		return text
	}
	return p.visibleText.String()
}

// attemptLocalOutputText reports the assistant text released since the latest
// continuation boundary, for progress detection only. Delivery, accounting,
// and history keep reading the cumulative accumulators.
//
// The customer buffer is append-only between a boundary snapshot and the next
// reset, so the boundary is always a prefix of the current text. A boundary
// that is not a prefix can only mean the buffer was replaced without a reset,
// in which case the cumulative text is returned unchanged.
func (p *responsePipeline) attemptLocalOutputText() string {
	if p == nil {
		return ""
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	p.ensure()
	if p.customer != nil {
		return p.customer.attemptText()
	}
	return p.visibleText.String()
}

// resolveCustomerUsage prefers a reconstruction from released customer
// content, then the last synthesized customer event, and finally a
// provider-stripped shell. Provider/operator evidence is never imported into
// the customer plane except for that empty shell.
func (p *responsePipeline) resolveCustomerUsage(ctx context.Context, usageEv aipapi.Event) aipapi.Event {
	if p == nil {
		return customerPlaneUsageEvent(usageEv)
	}
	p.eventsMu.Lock()
	reconstructor := p.customerUsageFn
	last := p.lastCustomerUsage
	p.eventsMu.Unlock()
	if reconstructor != nil {
		if ev := reconstructor(ctx, p.releasedOutputText(), p.contentEvents()); ev.Kind != "" {
			return ev
		}
	}
	if reconstructor == nil && p.streamUsage != nil {
		if result, err := p.streamUsage.Reconstruct(ctx, accountingstream.Input{OutputText: p.releasedOutputText(), Events: p.contentEvents()}); err == nil && len(result.Events) > 0 {
			return customerPlaneUsageEvent(mergeUsageEventsForClient(result.Events, true))
		}
	}
	if last.Kind != "" {
		return last
	}
	out := customerPlaneUsageEvent(usageEv)
	if out.Kind == "" && usageEv.Kind != "" {
		return aipapi.Event{Kind: aipapi.EventUsageDelta}
	}
	return out
}

func (p *responsePipeline) resolveCustomerUsageForTerminal(ctx context.Context, usageEv aipapi.Event, request requestTerminalFacts) aipapi.Event {
	if p == nil {
		return customerPlaneUsageEvent(usageEv)
	}
	p.eventsMu.Lock()
	reconstructor := p.customerUsageFn
	p.eventsMu.Unlock()
	if reconstructor != nil {
		return p.resolveCustomerUsage(ctx, usageEv)
	}
	if p.streamUsage != nil {
		result, err := p.streamUsage.Reconstruct(ctx, accountingstream.Input{
			Call: request.call, OutputText: p.releasedOutputText(), Events: p.contentEvents(),
		})
		if err != nil && p.log != nil {
			p.log.DebugContext(ctx, "customer stream usage reconstruction", "error", err)
		}
		if len(result.Events) > 0 {
			return applyFrontendIngressInput(request.metering, customerPlaneUsageEvent(mergeUsageEventsForClient(result.Events, true)))
		}
	}
	return p.resolveCustomerUsage(ctx, usageEv)
}

func (p *responsePipeline) contentEvents() []aipapi.Event {
	if p == nil || p.customer == nil {
		return nil
	}
	return p.customer.contentEvents()
}

func (p *responsePipeline) markCustomerSettled() bool {
	if p == nil {
		return true
	}
	p.eventsMu.Lock()
	p.ensure()
	customer := p.customer
	p.eventsMu.Unlock()
	return customer.MarkSettled()
}

func (p *responsePipeline) unmarkCustomerSettled() {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.ensure()
	customer := p.customer
	p.eventsMu.Unlock()
	customer.unmarkSettled()
}

func (p *responsePipeline) setLastAuthorityUsage(ev aipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.lastAuthorityUsage = ev
	p.eventsMu.Unlock()
}

func (p *responsePipeline) lastAuthorityUsageSnapshot() aipapi.Event {
	if p == nil {
		return aipapi.Event{}
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.lastAuthorityUsage
}

func (p *responsePipeline) setLastCustomerUsage(ev aipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.lastCustomerUsage = ev
	p.eventsMu.Unlock()
}

func (p *responsePipeline) lastCustomerUsageSnapshot() aipapi.Event {
	if p == nil {
		return aipapi.Event{}
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.lastCustomerUsage
}

func (p *responsePipeline) operatorUsageForFinalize() aipapi.Event {
	if p == nil {
		return emptyOperatorUsageShell()
	}
	if usage := p.lastAuthorityUsageSnapshot(); usage.Kind != "" {
		return usage
	}
	return operatorUsageOrShell(p.seenEventsCopy())
}

func (p *responsePipeline) usageEvidenceOrEmpty() aipapi.Event {
	if p == nil {
		return emptyOperatorUsageShell()
	}
	if usage := authorityUsageEvent(tokenAccountingUsageEvents(p.seenEventsCopy())); usage.Kind != "" {
		return usage
	}
	if usage := p.lastAuthorityUsageSnapshot(); usage.Kind != "" {
		return usage
	}
	return emptyOperatorUsageShell()
}

func (p *responsePipeline) billingEvidenceFallback() aipapi.Event {
	if p == nil {
		return emptyOperatorUsageShell()
	}
	if usage := p.lastAuthorityUsageSnapshot(); usage.Kind != "" {
		return usage
	}
	return lastUsageDeltaOrShell(p.seenEventsCopy())
}

type responseGateInput struct {
	meta      completion.Meta
	services  completion.Services
	stageLog  *slog.Logger
	committed bool
	limits    completion.BufferLimits
}

// completionGatedEmit owns gate buffers and drain state. It returns a gate
// replacement marker to the caller; response/terminal side effects remain at
// the caller boundary and are not owned by this pipeline.
func (p *responsePipeline) completionGatedEmit(
	ctx context.Context,
	gates []completion.Gate,
	ev aipapi.Event,
	in responseGateInput,
) (aipapi.Event, bool, error) {
	if p == nil {
		return aipapi.Event{}, false, errors.New("runtime: nil response pipeline")
	}
	p.eventsMu.Lock()
	p.ensure()
	if p.gateLive {
		p.eventsMu.Unlock()
		return ev, false, nil
	}
	limits := in.limits
	if limits.MaxEvents <= 0 {
		limits = completion.DefaultBufferLimits()
	}
	if len(p.gateBuf) == 0 {
		maxEv := limits.MaxEvents
		if maxEv <= 0 {
			maxEv = completion.DefaultBufferLimits().MaxEvents
		}
		const prealloc = 64
		p.gateBuf = make([]aipapi.Event, 0, min(prealloc, maxEv))
	}
	p.gateBuf = append(p.gateBuf, ev)
	if extensions.CompletionGateBufferExceeded(limits, len(p.gateBuf)) {
		p.gateLive = true
		p.gateDrain = slices.Clone(p.gateBuf)
		p.gateBuf = nil
		if len(p.gateDrain) == 0 {
			p.eventsMu.Unlock()
			return aipapi.Event{}, false, errors.New("runtime: completion gate overflow with empty buffer")
		}
		first := p.gateDrain[0]
		p.gateDrain = p.gateDrain[1:]
		p.eventsMu.Unlock()
		return first, false, nil
	}
	if ev.Kind != aipapi.EventResponseFinished {
		p.eventsMu.Unlock()
		return aipapi.Event{}, false, errGateContinueInner
	}
	buffered := slices.Clone(p.gateBuf)
	p.eventsMu.Unlock()

	gateResult, err := p.runCompletionGateChain(ctx, gates, buffered, in)
	committedForPanic := in.committed || gateBufHasCommittedOutput(buffered)
	if err != nil {
		p.eventsMu.Lock()
		p.gateBuf = nil
		var pe *safety.PanicError
		if errors.As(err, &pe) {
			p.gateDrain = nil
			p.gateLive = false
		}
		p.eventsMu.Unlock()
		if errors.As(err, &pe) {
			return aipapi.Event{}, false, mapStreamPanic(pe, committedForPanic)
		}
		return aipapi.Event{}, false, err
	}
	out := gateResult.Events
	if len(out) == 0 {
		p.eventsMu.Lock()
		p.gateBuf = nil
		p.eventsMu.Unlock()
		return aipapi.Event{}, false, errCompletionGateEmptyStream
	}
	p.eventsMu.Lock()
	p.gateBuf = nil
	p.gateDrain = slices.Clone(out[1:])
	p.eventsMu.Unlock()
	return out[0], gateResult.Replaced, nil
}

// errCompletionGateEmptyStream is the bounded failure for a completion-gate
// chain that produced no canonical stream at all.
var errCompletionGateEmptyStream = errors.New("runtime: completion gate produced empty stream")

// runCompletionGateChain is the single existing completion-gate evaluation. Both
// the ordinary buffered release and the private pending-publication preparation
// run the same chain through this one function, so a response is never gated
// twice and no second gate engine exists.
func (p *responsePipeline) runCompletionGateChain(
	ctx context.Context,
	gates []completion.Gate,
	buffered []aipapi.Event,
	in responseGateInput,
) (extensions.CompletionGateChainResult, error) {
	if len(gates) == 0 {
		return extensions.CompletionGateChainResult{Events: slices.Clone(buffered)}, nil
	}
	return safety.CallValue(safety.BoundaryStream, "completion_gate_chain", func() (extensions.CompletionGateChainResult, error) {
		return extensions.ApplyCompletionGateChain(ctx, gates, in.meta, buffered, in.committed, in.services, in.stageLog)
	})
}

// gateBufferSnapshot returns a coherent copy of the already-transformed
// completion-gate buffer and whether that buffer already failed open to live
// passthrough. It never appends, so preparing a private candidate cannot overflow
// the existing bounds or expose it early.
func (p *responsePipeline) gateBufferSnapshot() ([]aipapi.Event, bool) {
	if p == nil {
		return nil, false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return slices.Clone(p.gateBuf), p.gateLive
}

func (p *responsePipeline) popGateDrainHead() (aipapi.Event, bool) {
	if p == nil {
		return aipapi.Event{}, false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if len(p.gateDrain) == 0 {
		return aipapi.Event{}, false
	}
	ev := p.gateDrain[0]
	p.gateDrain = p.gateDrain[1:]
	return ev, true
}

type accumulatorSnapWire struct {
	Events int    `json:"e"`
	Text   string `json:"t,omitempty"`
	Final  bool   `json:"f"`
}

func (p *responsePipeline) accumulatorSnapshot(flags ...bool) coreterm.AccumulatorSnapshot {
	if p == nil {
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}
	committed := false
	finalized := false
	if len(flags) > 0 {
		committed = flags[0]
	}
	if len(flags) > 1 {
		finalized = flags[1]
	}
	if len(flags) == 0 {
		p.eventsMu.Lock()
		terminalSnapshot := p.terminalSnapshot
		p.eventsMu.Unlock()
		if terminalSnapshot != nil {
			// Read the terminal owner's flags before entering the response snapshot
			// boundary. No response lock may be held across an owner callback.
			committed, finalized = terminalSnapshot()
		}
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	p.ensure()
	w := accumulatorSnapWire{Events: len(p.seenEvents)}
	w.Final = finalized
	if p.customer != nil {
		w.Text, _, _, _ = p.customer.Snapshot()
	} else {
		w.Text = p.visibleText.String()
	}
	raw, _ := json.Marshal(w)
	return coreterm.NewAccumulatorSnapshot(raw, committed)
}

func (p *responsePipeline) setGateDrain(events []aipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	p.setGateDrainLocked(events)
}

// setGateDrainLocked is the one owner of the gate-drain value. The caller must
// hold [responsePipeline.eventsMu].
func (p *responsePipeline) setGateDrainLocked(events []aipapi.Event) {
	p.gateDrain = slices.Clone(events)
}

func (p *responsePipeline) popGateDrain() (aipapi.Event, bool) {
	return p.popGateDrainHead()
}

// abandonIncompleteGateBuffer drops an incomplete buffered completion at EOF. It
// is one half of releaseCompletionGateBuffers: a private pending publication owns
// the whole evaluated sequence instead, so it releases both the buffer and the
// drain unconditionally.
func (p *responsePipeline) abandonIncompleteGateBuffer() {
	p.releaseCompletionGateBuffers(false)
}

// releaseCompletionGateBuffers is the one owner of the completion-gate buffer and
// drain release. all releases an incomplete or completed buffer as well, which is
// what a prepared private candidate needs because it owns the evaluated sequence
// itself.
func (p *responsePipeline) releaseCompletionGateBuffers(all bool) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	release := all || (!p.gateLive && !extensions.StreamFinished(p.gateBuf))
	if release {
		p.gateBuf = nil
	}
	if all {
		p.setGateDrainLocked(nil)
	}
}

func (p *responsePipeline) setRecoveryDrain(events []aipapi.Event) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.recoverDrain = slices.Clone(events)
	p.eventsMu.Unlock()
}

func (p *responsePipeline) prependRecoveryDrain(events ...aipapi.Event) {
	if p == nil || len(events) == 0 {
		return
	}
	p.eventsMu.Lock()
	p.recoverDrain = append(slices.Clone(events), p.recoverDrain...)
	p.eventsMu.Unlock()
}

func (p *responsePipeline) appendRecoveryDrain(events ...aipapi.Event) {
	if p == nil || len(events) == 0 {
		return
	}
	p.eventsMu.Lock()
	p.recoverDrain = append(p.recoverDrain, events...)
	p.eventsMu.Unlock()
}

func (p *responsePipeline) popRecoveryDrain() (aipapi.Event, bool) {
	if p == nil {
		return aipapi.Event{}, false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if len(p.recoverDrain) == 0 {
		return aipapi.Event{}, false
	}
	ev := p.recoverDrain[0]
	p.recoverDrain = p.recoverDrain[1:]
	return ev, true
}
