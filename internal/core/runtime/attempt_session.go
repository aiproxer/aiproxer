package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

var errPublicationClosed = errors.New("publication closed")

func (a *attemptSession) receive(ctx context.Context, committed bool) (aipapi.Event, error) {
	if a == nil {
		return aipapi.Event{}, io.EOF
	}
	inner := a.loadInner()
	if inner == nil {
		return aipapi.Event{}, io.EOF
	}
	ev, err := safety.CallValue(safety.BoundaryBackend, "backend_recv", func() (aipapi.Event, error) {
		return inner.Recv(ctx)
	})
	if err != nil {
		if pe, ok := errors.AsType[*safety.PanicError](err); ok {
			err = mapStreamPanic(pe, committed)
		}
	}
	return ev, err
}

func backendReceivePanic(err error) bool {
	var pe *safety.PanicError
	return errors.As(err, &pe)
}

func runGuarded(name string, errorsList *[]error, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			*errorsList = append(*errorsList, fmt.Errorf("runtime: %s panicked: %v", name, r))
		}
	}()
	fn()
}

// attemptSession is the owner of one opened B-leg. Every field in this type
// has the same lifetime as the backend attempt and is discarded on replacement.
// The inner lock protects only the backend stream pointer; callers snapshot it
// and perform Cancel/Close without holding this lock.
type attemptSession struct {
	innerMu            sync.Mutex
	inner              aipapi.ManagedEventStream
	streamDisposed     bool
	pendingCancelCause atomic.Pointer[aipapi.CancelCause]
	terminalizing      atomic.Bool
	forceCloseOnce     sync.Once
	forceClose         chan struct{}
	usageMu            sync.Mutex
	billingMu          sync.Mutex
	accountingMu       sync.Mutex
	sidebandMu         sync.Mutex
	// controlMu guards the private control-call state only: the capture, the
	// pending outcome, the handoff bookkeeping, and the released marker. It is held
	// for bounded field work and never across Provider.Handle, backend
	// Recv/Cancel/Close, or a terminal effect.
	controlMu         sync.Mutex
	economicMu        sync.Mutex
	checkpointMu      sync.Mutex
	checkpointFlushMu sync.Mutex
	checkpointLocalMu sync.Mutex

	internalUsageKeys                     map[string]struct{}
	accumulatedUsage                      []aipapi.Event
	usageEvidence                         map[string]capturedBillingEvidence
	usageEvidenceOrder                    []string
	usageConflicts                        []usageevidence.EvidenceConflict
	economicObservations                  []execbackend.EconomicEvidence
	economicIdentities                    map[string]*economicIdentityRecord
	economicConflicts                     []usageevidence.EvidenceConflict
	economicRetentionConflicts            []retentionEvidenceConflict
	evidenceCaptureLoss                   evidenceCaptureLoss
	checkpointPending                     map[string]metering.Observation
	checkpointOrder                       []string
	checkpointDeferred                    map[string]metering.Observation
	checkpointDeferredOrder               []string
	checkpointDurableHeads                map[string]checkpointDurableHead
	localCheckpointHeads                  map[string]metering.Observation
	localCheckpointHashes                 map[string]string
	checkpointLastFlush                   time.Time
	checkpointErr                         error
	checkpointCapacityDiagnosticPending   bool
	checkpointCapacityDiagnosticEmitted   bool
	checkpointCapacityDiagnosticSemantics string
	checkpointCapacityDiagnosticRevision  uint64
	billingLegRecorded                    bool
	boundaryDrained                       bool

	cancelResult aipapi.CancelResult

	bleg            b2bua.BLegRecord
	cand            routing.AttemptCandidate
	structuralCause string
	// promptCache carries the content-free evidence this attempt publishes to
	// the prompt-cache collapse diagnostic on its own usage samples.
	promptCache promptCacheObservations
	// switchIdentity is the keyed, content-free downstream identity of this
	// attempt. It rides every usage sample; the collapse diagnostic owns
	// comparison.
	switchIdentity attemptSwitchIdentity
	// switchDigestKey is the process-stable HMAC key used to fold the
	// provider account carried by this attempt's usage accounting into the
	// credential component. It is copied from the executor only when the
	// diagnostic feed is armed and the key is present; a zero key publishes
	// no account fingerprint. It is never logged or persisted.
	switchDigestKey   SeamDigestKey
	authority         authorityLifecycle
	terminal          *streamTerminal
	aScope            *leglifecycle.ALeg
	releaseKind       usagelimit.ReleaseKind
	defaultCommand    sdkterminal.Command
	defaultLegOutcome usageevidence.LegOutcome
	traceID           string
	requestID         string
	boundaryScope     scope.PrincipalScopeView
	billingCallID     usageevidence.BillingCallID
	submissionID      string
	billingStoreID    string
	billingCallState  *billingCallState

	accounting  attemptAccountingTracker
	boundary    *coremetering.BoundaryAccumulator
	controlTool *controlToolActivation
	// controlCapture is the private, mutable response-side state machine for the
	// one proxy-owned control call. It is derived from the immutable
	// controlToolActivation above at session construction and never outlives this
	// attempt. The backend Recv loop is its only observation owner, and controlMu
	// orders that observation against lifecycle cleanup. The object itself outlives
	// cleanup so a released attempt stays visible to a late caller, which then fails
	// closed instead of looking like an attempt that never had a control protocol.
	controlCapture *controlCallCapture
	// controlOutcome is the normalized, validated provider outcome awaiting the
	// later terminal owner. It is private to this attempt: it never publishes
	// result text, synthesizes a client ToolCall/ToolResult, or asserts native
	// completion. Losing a race with cleanup means losing it permanently, so a
	// handler that returns after release cannot restore it.
	controlOutcome *controltool.Outcome
	// controlHandled records that the one permitted completion handoff already
	// happened, so a malformed or duplicate sequence can revoke an outcome without
	// ever invoking the pinned provider twice. The claim is taken and read under
	// controlMu, so exactly one handoff can be in flight per attempt.
	controlHandled bool
	// controlReleased marks the private control state as disposed. Lifecycle
	// cleanup — cancellation, Close, attempt loss, replacement — sets it once and
	// never clears it, so a late backend event or a provider result that arrives
	// after cleanup cannot revive the capture, re-invoke the provider, or fall
	// through to ordinary client tool execution.
	controlReleased bool

	toolFinal             *toolCallAssembler
	promptCacheSource     promptcache.ObservationSource
	promptCacheController promptcache.Controller
	finalStreamObs        *extensions.FinalStreamObservationSession
	recordAttemptLoggedFn func(context.Context, recordAttemptParams, diag.AttrOpts)
	recordCancellationFn  func(obs CancellationObservation)

	// withholding records an accepted steer outcome: this attempt must withhold
	// the rest of its output until the correction decision is made. The steer
	// gate sets it on acceptance; the correction rails and the receive loop read
	// it. The queue beside it holds the events the receive loop diverts while
	// this is set.
	withholding atomic.Bool
	// withholdMu guards the withhold queue and the correction prefix hold. The
	// steer gate and the receive loop are its only appenders and normal
	// releasers, both on the single receive loop; attempt cleanup (Close,
	// cancellation, disposal, replacement) may discard either queue from
	// another goroutine.
	withholdMu sync.Mutex
	withheld   withheldQueue
	// correctionHold retains the produced client-visible prefix of an attempt
	// whose turn can still be corrected, instead of releasing it. It shares the
	// withhold queue's lock and lifecycle cleanup; see correctionHold.
	correctionHold correctionHold

	// deferredRecvErr is the terminal transition the receive loop has already
	// observed but must run only after every release queue has drained: a
	// backend receive error, an end of stream, or an already-observed
	// prepared-event failure. deferredKind tags which existing owner consumes
	// it, so the loop never re-reads, re-prepares, or re-classifies a
	// transition it has already seen. It survives across Recv calls; the loop
	// consumes it exactly once once the drains are empty and before reading the
	// backend again, and the shared lifecycle cleanup discards whatever is
	// still pending. Guarded by withholdMu with the queues.
	deferredRecvErr  error
	deferredIdle     idleContextDeadline
	deferredTTFT     ttftContextDeadline
	deferredKind     deferredTerminalKind
	hasDeferredError bool

	// The produced-output facts below describe what this attempt produced so
	// far, whether or not it was released. prepareRecvEvent records them on the
	// single receive loop before the assembler sees an event, and the
	// correction rails read them; the decided call's own tool events are
	// excluded by call ID. They are attempt-local, so a replacement attempt
	// starts clean.
	producedVisible   bool
	producedReasoning bool
	toolOutputCallIDs map[string]struct{}

	emitBackendEgressFn func(context.Context, string, metering.AttemptOutcome, metering.SurfacedState, aipapi.Event)
	appendBillingLegFn  func(context.Context, b2bua.BLegRecord, routing.Primary, time.Time, time.Time, usageevidence.LegOutcome)
	now                 func() time.Time

	billingEnabled         func() bool
	operatorRateRef        func(context.Context, routing.Primary) usageevidence.VersionRef
	billingWorkload        func(context.Context, string) usageevidence.WorkloadIdentity
	observeBillingLeg      func(context.Context, usageevidence.CallLegUsageRecord)
	appendBillingLeg       func(context.Context, usageevidence.BillingCallID, usageevidence.CallLegUsageRecord)
	appendBillingLegStrict func(context.Context, usageevidence.BillingCallID, usageevidence.CallLegUsageRecord) error
	finalizeBilling        func(context.Context, execbackend.BillingFinalizationInput) (aipapi.Event, error)
	finalizeBillingV2      func(context.Context, execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, error)
	observationSink        metering.ObservationSink
	// terminalFlushBudget overrides the bounded terminal economic checkpoint
	// flush budget for this attempt. Zero means the production
	// economicCheckpointFlushTimeout; production code never sets it. Tests that
	// drive a real durable store may raise it so the assertion covers flush
	// behavior rather than host storage speed, without weakening the bound that
	// production requests depend on.
	terminalFlushBudget time.Duration
}

func (a *attemptSession) claimBillingLegRecord() bool {
	if a == nil {
		return false
	}
	a.billingMu.Lock()
	defer a.billingMu.Unlock()
	if a.billingLegRecorded {
		return false
	}
	a.billingLegRecorded = true
	return true
}

// billingLegRecordRequired reports whether this attempt owns a terminal
// billing leg consumer. When every accounting seam is unbound the record has
// no observer or sink, so the terminal path must not build and discard it.
// A nil predicate keeps the historical record path for direct test wiring
// that assigns explicit callbacks without an executor.
func (a *attemptSession) billingLegRecordRequired() bool {
	if a == nil {
		return false
	}
	if a.billingEnabled == nil {
		return true
	}
	return a.billingEnabled()
}

func (a *attemptSession) accountingStartedAt() time.Time {
	if a == nil {
		return time.Time{}
	}
	a.accountingMu.Lock()
	defer a.accountingMu.Unlock()
	return a.accounting.requestStartedAt
}

func (a *attemptSession) accountingSnapshot() attemptAccountingSnapshot {
	if a == nil {
		return attemptAccountingSnapshot{}
	}
	a.accountingMu.Lock()
	defer a.accountingMu.Unlock()
	return a.accounting.snapshot()
}

func (a *attemptSession) observeAccountingBackendEvent(at time.Time, ev aipapi.Event) {
	if a == nil {
		return
	}
	a.accountingMu.Lock()
	a.accounting.observeBackendEvent(at, ev)
	a.accountingMu.Unlock()
}

func (a *attemptSession) observeAccountingClientEvent(at time.Time, ev aipapi.Event) {
	if a == nil {
		return
	}
	a.accountingMu.Lock()
	a.accounting.observeClientEvent(at, ev)
	a.accountingMu.Unlock()
}

func (a *attemptSession) observeAccountingUsage(ev aipapi.Event) {
	if a == nil {
		return
	}
	a.accountingMu.Lock()
	a.accounting.observeUsage(ev)
	a.accountingMu.Unlock()
}

func (a *attemptSession) toolCallAssembler() *toolCallAssembler {
	if a == nil {
		return nil
	}
	a.sidebandMu.Lock()
	defer a.sidebandMu.Unlock()
	return a.toolFinal
}

// markWithholding records that this attempt accepted a steer outcome for the
// steered call's effective name and must withhold the rest of its output. The
// accepted instruction travels with the withhold state, because the correction
// continuation is the only place that may carry it. Acceptance is the only
// producer: the steer gate calls it synchronously on the single receive loop.
// The name is kept with the withhold state - never recorded raw - so the
// outcome that ends the withhold can hash it.
func (a *attemptSession) markWithholding(name, instruction string) {
	if a == nil {
		return
	}
	a.withholdMu.Lock()
	a.withheld.name = name
	a.withheld.instruction = instruction
	a.withholdMu.Unlock()
	a.withholding.Store(true)
}

// withheldSteer returns the accepted correction's effective name and
// instruction. The finish intercept reads them once, before it publishes or
// replays the queue.
func (a *attemptSession) withheldSteer() (name, instruction string) {
	if a == nil {
		return "", ""
	}
	a.withholdMu.Lock()
	defer a.withholdMu.Unlock()
	return a.withheld.name, a.withheld.instruction
}

// discardWithheld clears the withhold queue and the withhold mark, and the
// correction prefix hold beside it, so every attempt lifecycle cleanup frees
// both bounded queues together. It is the attempt lifecycle cleanup used by
// disposal, cancellation, and replacement; a queue that must still reach the
// client goes through releaseWithheld instead, and a correction hold delivers
// through releaseCorrectionHold.
func (a *attemptSession) discardWithheld() {
	if a == nil {
		return
	}
	a.withholdMu.Lock()
	a.withheld = withheldQueue{}
	a.discardCorrectionHoldLocked()
	a.deferredRecvErr = nil
	a.deferredIdle = idleContextDeadline{}
	a.deferredTTFT = ttftContextDeadline{}
	a.deferredKind = deferredTerminalNone
	a.hasDeferredError = false
	a.withholdMu.Unlock()
	a.withholding.Store(false)
}

// deferredTerminalKind tags the one terminal transition the receive loop has
// already observed but must run only after every release queue has drained. The
// tag selects the existing owner that consumes it; it is not a state machine:
// the loop stores at most one transition between consumptions.
type deferredTerminalKind uint8

const (
	// deferredTerminalNone is the empty slot.
	deferredTerminalNone deferredTerminalKind = iota
	// deferredTerminalRecvError is a backend receive failure: the loop's
	// existing receive-error handling owns its recovery classification.
	deferredTerminalRecvError
	// deferredTerminalEOF is an observed end of stream: the loop's existing
	// end-of-stream handling owns it, and the ended backend is never read again.
	deferredTerminalEOF
	// deferredTerminalPrepareError is an already-observed prepared-event
	// failure: it terminalizes through the same partial-failure path the
	// immediate branch uses, with its original type and cause, and never
	// through receive recovery.
	deferredTerminalPrepareError
)

// deferredTerminalWork is the deferred slot as the receive loop consumes it: the
// transition kind, its error, and the receive deadlines only a backend receive
// failure carries.
type deferredTerminalWork struct {
	kind deferredTerminalKind
	err  error
	idle idleContextDeadline
	ttft ttftContextDeadline
}

// storeDeferredTerminalWork records the one terminal transition the receive
// loop must run after it has released everything it observed. A later store
// replaces an earlier pending one: the slot is a single-transition handoff, and
// the loop stores at most one between consumptions.
func (a *attemptSession) storeDeferredTerminalWork(kind deferredTerminalKind, err error, idle idleContextDeadline, ttft ttftContextDeadline) {
	if a == nil || kind == deferredTerminalNone {
		return
	}
	if err == nil && kind != deferredTerminalEOF {
		return
	}
	a.withholdMu.Lock()
	a.deferredRecvErr = err
	a.deferredIdle = idle
	a.deferredTTFT = ttft
	a.deferredKind = kind
	a.hasDeferredError = true
	a.withholdMu.Unlock()
}

// storeDeferredRecvErr records a backend receive error to process after the
// withheld release drains. The release that precedes it already cleared the
// withhold mark, so the drain delivers in order and marks commitment first.
func (a *attemptSession) storeDeferredRecvErr(err error, idle idleContextDeadline, ttft ttftContextDeadline) {
	a.storeDeferredTerminalWork(deferredTerminalRecvError, err, idle, ttft)
}

// storeDeferredEOF records an observed end of stream as pending terminal work:
// the held prefix releases first and the original end of stream runs afterwards,
// so the ended backend is never read again.
func (a *attemptSession) storeDeferredEOF() {
	a.storeDeferredTerminalWork(deferredTerminalEOF, nil, idleContextDeadline{}, ttftContextDeadline{})
}

// storeDeferredPrepareError records an already-observed prepared-event failure
// as pending terminal work: the held prefix releases first and the same failure
// terminalizes, with its original precedence and identity, once the queue
// drains.
func (a *attemptSession) storeDeferredPrepareError(err error) {
	a.storeDeferredTerminalWork(deferredTerminalPrepareError, err, idleContextDeadline{}, ttftContextDeadline{})
}

// takeDeferredTerminalWork returns the pending terminal transition, if any, and
// clears the slot, so the receive loop runs the identical transition exactly
// once, once the release queues are empty and before reading the backend again.
func (a *attemptSession) takeDeferredTerminalWork() (deferredTerminalWork, bool) {
	if a == nil {
		return deferredTerminalWork{}, false
	}
	a.withholdMu.Lock()
	defer a.withholdMu.Unlock()
	if !a.hasDeferredError {
		return deferredTerminalWork{}, false
	}
	work := deferredTerminalWork{kind: a.deferredKind, err: a.deferredRecvErr, idle: a.deferredIdle, ttft: a.deferredTTFT}
	a.deferredRecvErr = nil
	a.deferredIdle = idleContextDeadline{}
	a.deferredTTFT = ttftContextDeadline{}
	a.deferredKind = deferredTerminalNone
	a.hasDeferredError = false
	return work, true
}

// isWithholding reports whether this attempt accepted a steer outcome. It is a
// read-only query for the correction rails and the receive loop.
func (a *attemptSession) isWithholding() bool {
	return a != nil && a.withholding.Load()
}

// observeProducedOutput classifies one backend event into this attempt's
// produced-output facts. It runs in prepareRecvEvent before the assembler sees
// the event, so it counts output whether or not the event is ever released.
func (a *attemptSession) observeProducedOutput(ev aipapi.Event) {
	if a == nil {
		return
	}
	switch ev.Kind {
	case aipapi.EventReasoningDelta, aipapi.EventReasoningOpaqueDelta, aipapi.EventReasoningPart:
		a.producedReasoning = true
	case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta:
		id := strings.TrimSpace(ev.ToolCallID)
		if id == "" {
			return
		}
		if a.toolOutputCallIDs == nil {
			a.toolOutputCallIDs = make(map[string]struct{}, 1)
		}
		a.toolOutputCallIDs[id] = struct{}{}
	default:
		if aipapi.OutputCommitted(ev) {
			a.producedVisible = true
		}
	}
}

// producedVisibleBefore reports whether this attempt produced visible output
// other than the decided call's own lifecycle: any non-tool committed output
// counts, and tool output counts when another call produced it.
func (a *attemptSession) producedVisibleBefore(decidedCallID string) bool {
	if a == nil {
		return false
	}
	if a.producedVisible {
		return true
	}
	for id := range a.toolOutputCallIDs {
		if id != decidedCallID {
			return true
		}
	}
	return false
}

// producedReasoningOutput reports whether this attempt produced reasoning.
func (a *attemptSession) producedReasoningOutput() bool {
	return a != nil && a.producedReasoning
}

func (a *attemptSession) promptCacheSideband() (promptcache.ObservationSource, promptcache.Controller) {
	if a == nil {
		return nil, nil
	}
	a.sidebandMu.Lock()
	defer a.sidebandMu.Unlock()
	return a.promptCacheSource, a.promptCacheController
}

// discardSidebandState is the attempt lifecycle cleanup chokepoint. It detaches
// the ordinary sideband assemblers and releases the private control-call state
// and the correction withhold queue together, so every cancellation, Close,
// attempt loss, and replacement path releases all of them. The locks are taken
// and released separately: no provider, backend, or terminal work runs while
// any of them is held.
//
// The prompt-cache observation source and controller deliberately survive this
// cleanup: the successful-turn keep-warm handoff (commitSuccessfulTurn) runs
// after the attempt terminal winner finalizes, and it is the only consumer of
// those two attempt-local values. They are plain interface references whose
// streams are closed by the attempt teardown, so retaining them until the
// attempt is released adds no lifecycle owner and cannot deliver after Close.
func (a *attemptSession) discardSidebandState() {
	if a == nil {
		return
	}
	a.sidebandMu.Lock()
	a.toolFinal = nil
	a.sidebandMu.Unlock()
	a.discardControlState()
	a.discardWithheld()
}

// promptCacheObservations carries the content-free evidence this attempt
// publishes for the prompt-cache collapse diagnostic. The seam vector and the
// instruction identity are computed from the final provider-bound canonical
// call; the launch ordinal is the request's process-owned ordering stamp. The
// instruction identity excludes volatile steering emissions by positions
// re-resolved in that final call, so a candidate or authority adaptation cannot
// leave the exclusion pointing at a different entry. All ride the attempt's own
// usage samples. A nil vector or zero identity means the corresponding feed is
// disabled, the path carried no canonical call, or no identity could be
// computed; a zero launch ordinal means the request did not travel through the
// executor's armed feed. Observers ignore absent evidence.
type promptCacheObservations struct {
	// seamDigests carries one content-free per-item hash per model-bound
	// request item (a bounded prefix), so the diagnostic can report the first
	// divergent item index on a collapse episode.
	seamDigests [][32]byte
	// systemPromptDigest is the content-free identity of the provider-bound
	// system/developer instruction prefix, so the diagnostic attributes a
	// cross-turn instruction change at the leg where it happened.
	systemPromptDigest [32]byte
	// systemPromptLaunchSeq is the request launch ordinal drawn from the
	// process-owned sequence, captured when the attempt starts and published
	// with the identity so the diagnostic can order the healthy reference
	// against the triggering leg. It is request-scoped, not attempt-scoped:
	// every leg of the request carries the same value.
	systemPromptLaunchSeq uint64
}

// attemptSessionInput carries one attempt's construction inputs.
type attemptSessionInput struct {
	inner          aipapi.ManagedEventStream
	streamDisposed bool
	bleg           b2bua.BLegRecord
	cand           routing.AttemptCandidate
	authority      authorityLifecycle
	aScope         *leglifecycle.ALeg

	traceID   string
	requestID string
	// structuralCause is the proxy-owned, content-free cause of a structural
	// change applied to this attempt's request. It rides the attempt's own
	// usage samples so an observer attributes the change to exactly the
	// request that carried it.
	structuralCause string
	// promptCache carries the content-free observations that ride this
	// attempt's own usage samples for the prompt-cache collapse diagnostic.
	promptCache           promptCacheObservations
	boundaryScope         scope.PrincipalScopeView
	billingCallID         usageevidence.BillingCallID
	submissionID          string
	billingStoreID        string
	billingCallState      *billingCallState
	accounting            attemptAccountingTracker
	boundary              *coremetering.BoundaryAccumulator
	controlTool           *controlToolActivation
	toolFinal             *toolCallAssembler
	promptCacheSource     promptcache.ObservationSource
	promptCacheController promptcache.Controller
	finalStreamObs        *extensions.FinalStreamObservationSession
	recordAttemptLoggedFn func(context.Context, recordAttemptParams, diag.AttrOpts)

	emitBackendEgressFn func(context.Context, string, metering.AttemptOutcome, metering.SurfacedState, aipapi.Event)
	appendBillingLegFn  func(context.Context, b2bua.BLegRecord, routing.Primary, time.Time, time.Time, usageevidence.LegOutcome)
	now                 func() time.Time

	billingEnabled    func() bool
	operatorRateRef   func(context.Context, routing.Primary) usageevidence.VersionRef
	billingWorkload   func(context.Context, string) usageevidence.WorkloadIdentity
	observeBillingLeg func(context.Context, usageevidence.CallLegUsageRecord)
	appendBillingLeg  func(context.Context, usageevidence.BillingCallID, usageevidence.CallLegUsageRecord)
	finalizeBilling   func(context.Context, execbackend.BillingFinalizationInput) (aipapi.Event, error)
	finalizeBillingV2 func(context.Context, execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, error)
	observationSink   metering.ObservationSink
}

func withBillingStoreIDOnAttempt(input attemptSessionInput, storeID string) attemptSessionInput {
	input.billingStoreID = storeID
	return input
}

func newAttemptSession(in attemptSessionInput) *attemptSession {
	return &attemptSession{
		inner: in.inner, streamDisposed: in.streamDisposed, bleg: in.bleg, cand: in.cand, authority: in.authority,
		forceClose: make(chan struct{}),
		terminal:   newStreamTerminal(sdkterminal.ScopeAttempt), aScope: in.aScope,
		releaseKind: usagelimit.ReleaseKindSwallowed, defaultCommand: sdkterminal.CommandBackendOpenFailure, defaultLegOutcome: usageevidence.LegOutcomeFailed,
		traceID: in.traceID, requestID: in.requestID, structuralCause: in.structuralCause, promptCache: in.promptCache, boundaryScope: in.boundaryScope, billingCallID: in.billingCallID, submissionID: in.submissionID, billingStoreID: in.billingStoreID, billingCallState: in.billingCallState,
		accounting: in.accounting, boundary: in.boundary, controlTool: in.controlTool, toolFinal: in.toolFinal, promptCacheSource: in.promptCacheSource,
		// The capture is derived state, not relayed input: newControlCallCapture
		// returns nil for an absent or inactive activation, so the ordinary
		// no-provider path performs zero response work with no extra input field.
		controlCapture:        newControlCallCapture(in.controlTool),
		promptCacheController: in.promptCacheController, finalStreamObs: in.finalStreamObs,
		recordAttemptLoggedFn: in.recordAttemptLoggedFn, emitBackendEgressFn: in.emitBackendEgressFn,
		appendBillingLegFn: in.appendBillingLegFn, now: in.now, billingEnabled: in.billingEnabled,
		operatorRateRef: in.operatorRateRef, billingWorkload: in.billingWorkload,
		observeBillingLeg: in.observeBillingLeg, appendBillingLeg: in.appendBillingLeg, finalizeBilling: in.finalizeBilling, finalizeBillingV2: in.finalizeBillingV2,
		observationSink: in.observationSink,
	}
}

func (a *attemptSession) recordCancellation(obs CancellationObservation) {
	if a != nil && a.recordCancellationFn != nil {
		a.recordCancellationFn(obs)
	}
}

func (a *attemptSession) recordAttemptLogged(ctx context.Context, p recordAttemptParams, attrs diag.AttrOpts) {
	if a == nil || a.recordAttemptLoggedFn == nil {
		return
	}
	// Deduplicate by attempt terminal CAS (Req 9.1): if attempt already terminalized, suppress replay.
	if a.terminal != nil && a.terminal.Owner() != nil {
		if st := a.terminal.Owner().State(); st != "" && st != sdkterminal.StateOpen {
			return
		}
	}
	a.recordAttemptLoggedFn(ctx, p, attrs)
}

func (a *attemptSession) BLeg() b2bua.BLegRecord {
	if a == nil {
		return b2bua.BLegRecord{}
	}
	return a.bleg
}

func (a *attemptSession) Candidate() routing.AttemptCandidate {
	if a == nil {
		return routing.AttemptCandidate{}
	}
	return a.cand
}

func (a *attemptSession) loadInner() aipapi.ManagedEventStream {
	if a == nil {
		return nil
	}
	a.innerMu.Lock()
	defer a.innerMu.Unlock()
	return a.inner
}

// hasHostOnlyEconomicEvidenceSource keeps lifecycle-sensitive stream access
// inside the attempt owner while exposing only the neutral authority query
// needed by response preparation.
func (a *attemptSession) hasHostOnlyEconomicEvidenceSource() bool {
	if a == nil {
		return false
	}
	return hasHostOnlyEconomicEvidenceSource(a.loadInner())
}

func (a *attemptSession) storeInner(stream aipapi.ManagedEventStream) {
	if a == nil {
		return
	}
	a.innerMu.Lock()
	a.inner = stream
	a.innerMu.Unlock()
}

func (a *attemptSession) installBridgeStream(bridge aipapi.ManagedEventStream) {
	if a == nil {
		return
	}
	a.storeInner(bridge)
}

func (a *attemptSession) takeInner() aipapi.ManagedEventStream {
	if a == nil {
		return nil
	}
	a.innerMu.Lock()
	defer a.innerMu.Unlock()
	inner := a.inner
	a.inner = nil
	return inner
}

func (a *attemptSession) hasInner() bool {
	if a == nil {
		return false
	}
	a.innerMu.Lock()
	defer a.innerMu.Unlock()
	return a.inner != nil
}

type attemptLifecycleHandle struct {
	session *attemptSession
}

func (a *attemptSession) terminalizeForCancel(ctx context.Context, cause aipapi.CancelCause, cmd sdkterminal.Command, relKind usagelimit.ReleaseKind, legOutcome usageevidence.LegOutcome) attemptTerminalResult {
	if a == nil {
		return attemptTerminalResult{Cancellation: aipapi.CancelResult{Mode: aipapi.CancelModeNone}}
	}
	intent := IntentCancellation
	if cmd == "" {
		cmd = a.defaultCommand
		if cmd == "" {
			cmd = sdkterminal.CommandCancel
		}
	}
	if relKind == "" {
		relKind = a.releaseKind
	}
	if legOutcome == "" {
		legOutcome = a.defaultLegOutcome
		if legOutcome == "" {
			legOutcome = usageevidence.LegOutcomeCanceled
		}
	}
	if cause.Kind == aipapi.CancelRaceLoser || cmd == sdkterminal.CommandParallelLoser {
		intent, relKind, cmd, legOutcome = IntentParallelLoser, usagelimit.ReleaseKindLosing, sdkterminal.CommandParallelLoser, usageevidence.LegOutcomeFailed
	} else if cmd == sdkterminal.CommandBackendOpenFailure {
		intent, relKind, cmd = IntentSwallowedFailure, usagelimit.ReleaseKindSwallowed, sdkterminal.CommandBackendOpenFailure
		if (ctx != nil && ctx.Err() != nil) || cause.Kind == aipapi.CancelContextDone {
			legOutcome = usageevidence.LegOutcomeCanceled
		} else {
			legOutcome = usageevidence.LegOutcomeFailed
		}
	}
	detail := cause.Detail
	if detail == "" {
		detail = string(cause.Kind)
	}
	return a.TerminalizeAttempt(ctx, intent, attemptEvidence{
		Command:       cmd,
		ReleaseKind:   relKind,
		CancelCause:   &cause,
		LegOutcome:    legOutcome,
		ObsOutcome:    response.OutcomeCancelled,
		RecordOutcome: aipapi.AttemptCancelled,
		RecordReason:  detail,
		BillingReason: detail,
		TraceID:       a.traceID,
		ALegID:        a.bleg.ALegID,
		StartedAt:     a.accountingStartedAt(),
	})
}

func (a *attemptSession) cancelViaLifecycle(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	return a.terminalizeForCancel(ctx, cause, a.defaultCommand, a.releaseKind, a.defaultLegOutcome).Cancellation
}

func (a *attemptSession) terminalizeForClose(cmd sdkterminal.Command, relKind usagelimit.ReleaseKind, legOutcome usageevidence.LegOutcome) {
	if a == nil {
		return
	}
	if cmd == "" {
		cmd = a.defaultCommand
		if cmd == "" {
			cmd = sdkterminal.CommandClose
		}
	}
	if relKind == "" {
		relKind = a.releaseKind
	}
	if legOutcome == "" {
		legOutcome = a.defaultLegOutcome
		if legOutcome == "" {
			legOutcome = usageevidence.LegOutcomeCanceled
		}
	}
	a.TerminalizeAttempt(context.Background(), IntentCancellation, attemptEvidence{
		Command:       cmd,
		ReleaseKind:   relKind,
		LegOutcome:    legOutcome,
		ObsOutcome:    response.OutcomeClosed,
		RecordOutcome: aipapi.AttemptCancelled,
		RecordReason:  "aleg close",
		BillingReason: "aleg close",
		TraceID:       a.traceID,
		ALegID:        a.bleg.ALegID,
		StartedAt:     a.accountingStartedAt(),
	})
}

func (a *attemptSession) closeViaLifecycle() error {
	if a.terminalizing.Load() && a.forceClose != nil {
		a.forceCloseOnce.Do(func() { close(a.forceClose) })
		return nil
	}
	a.terminalizeForClose(a.defaultCommand, a.releaseKind, a.defaultLegOutcome)
	return nil
}

func (h *attemptLifecycleHandle) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	if h == nil || h.session == nil {
		return aipapi.CancelResult{Mode: aipapi.CancelModeNone}
	}
	return h.session.cancelViaLifecycle(ctx, cause)
}

func (h *attemptLifecycleHandle) Close() error {
	if h == nil || h.session == nil {
		return nil
	}
	return h.session.closeViaLifecycle()
}

func (h *attemptLifecycleHandle) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (a *attemptSession) lifecycleHandle() leglifecycle.BLegAttempt {
	if a == nil {
		return nil
	}
	return &attemptLifecycleHandle{session: a}
}

func (a *attemptSession) setPendingCancelCause(cause aipapi.CancelCause) {
	if a == nil {
		return
	}
	a.pendingCancelCause.Store(&cause)
}

// drainSidebandEvidence drains provider sideband usage evidence attached to
// the attempt that produced the source stream. Safe for nil receiver/pipeline.
func (a *attemptSession) drainSidebandEvidence(ctx context.Context, facts recvTurnFacts, p *responsePipeline) {
	if a == nil || p == nil {
		return
	}
	inner := a.loadInner()
	if inner == nil {
		return
	}
	p.consumeBackendUsageEvidenceForAttempt(ctx, facts, a, inner)
	if p.log != nil {
		if diagnostic, ok := a.takeEconomicCheckpointCapacityDiagnostic(); ok {
			facts.logEconomicCheckpointCapacityRejection(ctx, p.log, a, diagnostic)
		}
	}
	a.checkpointLocalBoundaryObservations(a.economicCheckpointNow())
	if err := a.flushEconomicCheckpoints(ctx, false); err != nil && p.log != nil {
		p.log.DebugContext(ctx, "economic pre-terminal checkpoint append failed", "error", err)
	}
}

// attemptSlot protects only the current attempt pointer. It never holds its
// lock while receiving, cancelling, closing, terminalizing, or persisting.
type attemptSlot struct {
	mu                sync.Mutex
	current           *attemptSession
	publicationClosed bool
}

func (s *attemptSlot) snapshot() *attemptSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// require returns the installed attempt. Production stream construction and
// replacement install a complete session before exposing the stream; a
// missing session is therefore an internal construction error.
func (s *attemptSlot) require() *attemptSession {
	if s == nil {
		panic("runtime: attempt slot is nil")
	}
	if attempt := s.snapshot(); attempt != nil {
		return attempt
	}
	panic("runtime: attempt session not installed")
}

func (s *attemptSlot) publishReady(ready *readyAttempt) (old *attemptSession, published bool) {
	return s.swapIfOpen(ready)
}

// closePublicationAndSnapshot closes the replacement publication window and
// returns the attempt that was current at that boundary. Replacement Open may
// continue outside this lock, but swapIfOpen rejects its result after this point.
func (s *attemptSlot) closePublicationAndSnapshot() *attemptSession {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.publicationClosed = true
	current := s.current
	s.mu.Unlock()
	return current
}

// publicationClosed reports that Close has won the replacement publication
// boundary. Recv uses this narrow slot fact to avoid opening recovery work
// after Close has detached the current inner stream but before terminal
// completion becomes visible.
func (s *attemptSlot) publicationIsClosed() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publicationClosed
}

// swapIfOpen publishes a complete replacement only while the request slot is
// still live. It returns the detached prior attempt for callers that need to
// retain its ownership while performing effects outside the slot lock.
func (s *attemptSlot) swapIfOpen(ready *readyAttempt) (old *attemptSession, published bool) {
	if s == nil || ready == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publicationClosed {
		return s.current, false
	}
	next, err := ready.Consume()
	if err != nil {
		return s.current, false
	}
	old, s.current = s.current, next
	return old, true
}

// pendingSelectionEffects captures winner/interleaved effects as data until publication accepts.
type pendingSelectionEffects struct {
	interleaved interleavedstate.State
}

type pendingInvalidationKind int

const (
	invalidationNone pendingInvalidationKind = iota
	invalidationCancel
	invalidationClose
	invalidationDispose
)

type pendingInvalidation struct {
	kind        pendingInvalidationKind
	cancelCause *aipapi.CancelCause
	intent      attemptTerminalIntent
	evidence    *attemptEvidence
	err         error
}

type readyState int

const (
	readyStateActive readyState = iota
	readyStatePreparing
	readyStatePrepared
	readyStateConsumed
	readyStateDisposed
)

// readyAttempt is a private single-use capability that gates publication.
// It owns the unpublished attemptSession until publication or disposal.
type readyAttempt struct {
	mu                  sync.Mutex
	cond                *sync.Cond
	session             *attemptSession
	boundSess           *attemptSession
	pending             pendingSelectionEffects
	state               readyState
	opInFlight          bool
	pendingInvalidation *pendingInvalidation
	prepErr             error
	defaultReleaseKind  usagelimit.ReleaseKind
	defaultCommand      sdkterminal.Command
	defaultLegOutcome   usageevidence.LegOutcome
}

func newReadyAttempt(session *attemptSession, pending pendingSelectionEffects) *readyAttempt {
	r := &readyAttempt{
		session:   session,
		boundSess: session,
		pending:   pending,
		state:     readyStateActive,
	}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *readyAttempt) hasPendingInvalidation() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pendingInvalidation != nil
}

func (r *readyAttempt) getCond() *sync.Cond {
	if r.cond == nil {
		r.cond = sync.NewCond(&r.mu)
	}
	return r.cond
}

func (r *readyAttempt) Pending() pendingSelectionEffects {
	if r == nil {
		return pendingSelectionEffects{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending
}

func (r *readyAttempt) Candidate() routing.AttemptCandidate {
	if r == nil {
		return routing.AttemptCandidate{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil {
		return routing.AttemptCandidate{}
	}
	return r.session.cand
}

func (r *readyAttempt) BLeg() b2bua.BLegRecord {
	if r == nil {
		return b2bua.BLegRecord{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil {
		return b2bua.BLegRecord{}
	}
	return r.session.bleg
}

func (r *readyAttempt) AuthorityState() attemptAuthorityState {
	if r == nil {
		return attemptAuthorityState{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session == nil || r.session.authority.control == nil {
		return attemptAuthorityState{}
	}
	return r.session.authority.control.state
}

func (r *readyAttempt) InstallBridgeStream(bridge aipapi.ManagedEventStream) error {
	if r == nil {
		return errors.New("runtime: nil readyAttempt")
	}
	r.mu.Lock()
	cond := r.getCond()
	for r.opInFlight {
		cond.Wait()
	}
	if r.state == readyStateConsumed || r.state == readyStateDisposed || r.pendingInvalidation != nil {
		r.mu.Unlock()
		return errors.New("runtime: readyAttempt already consumed or disposed")
	}
	sess := r.session
	if sess == nil {
		r.mu.Unlock()
		return errors.New("runtime: nil session for bridge install")
	}
	r.opInFlight = true
	r.mu.Unlock()

	sess.installBridgeStream(bridge)

	r.mu.Lock()
	r.opInFlight = false
	cond.Broadcast()
	if r.pendingInvalidation != nil || r.state == readyStateDisposed {
		r.mu.Unlock()
		return errors.New("runtime: readyAttempt canceled or disposed during bridge install")
	}
	r.mu.Unlock()
	return nil
}

// Prepare completes all fallible attempt-local readiness work (sideband drain and
// final stream observation) while the attempt remains unpublished. On readiness failure,
// the capability transitions to disposed and terminalizes the unpublished session exactly once.
func (r *readyAttempt) Prepare(ctx context.Context, facts recvTurnFacts, p *responsePipeline, committed bool) error {
	if r == nil {
		return errors.New("runtime: nil readyAttempt")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	cond := r.getCond()
	for r.opInFlight {
		cond.Wait()
	}
	if r.state == readyStateConsumed || r.state == readyStateDisposed || r.pendingInvalidation != nil {
		r.mu.Unlock()
		if r.prepErr != nil {
			return r.prepErr
		}
		return errors.New("runtime: readyAttempt already consumed or disposed")
	}
	if r.state == readyStatePrepared {
		r.mu.Unlock()
		return nil
	}
	sess := r.session
	if sess == nil || p == nil {
		r.state = readyStateDisposed
		r.session = nil
		r.pending = pendingSelectionEffects{}
		err := errors.New("runtime: nil session for readyAttempt prepare")
		if p == nil {
			err = errors.New("runtime: nil responsePipeline for readyAttempt prepare")
		}
		r.prepErr = err
		cond.Broadcast()
		r.mu.Unlock()

		if sess != nil {
			sess.abortPreReturn(ctx, err)
		}
		return err
	}
	r.opInFlight = true
	r.state = readyStatePreparing
	r.mu.Unlock()

	// 1. Immediate sideband evidence consumption outside mutex
	sess.drainSidebandEvidence(ctx, facts, p)

	// 2. Open final stream observation outside mutex
	pCtx := facts.projectContext(ctx, p.log)
	views, viewsOK := facts.viewsFor(pCtx)
	err := p.openFinalStreamObservation(pCtx, facts, sess, views, viewsOK, committed)

	r.mu.Lock()
	r.opInFlight = false
	if r.pendingInvalidation != nil {
		if err != nil {
			r.prepErr = err
		} else {
			r.prepErr = errors.New("runtime: attempt canceled during prepare")
		}
		cond.Broadcast()
		r.mu.Unlock()
		return r.prepErr
	}
	if err != nil {
		r.state = readyStateDisposed
		r.prepErr = err
		r.session = nil
		r.pending = pendingSelectionEffects{}
		cond.Broadcast()
		r.mu.Unlock()

		if sess != nil {
			sess.abortPreReturn(ctx, err)
		}
		return err
	}

	r.state = readyStatePrepared
	r.prepErr = nil
	cond.Broadcast()
	r.mu.Unlock()
	return nil
}

func (r *readyAttempt) IsConsumed() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state == readyStateConsumed || r.state == readyStateDisposed
}

func (r *readyAttempt) Consume() (*attemptSession, error) {
	if r == nil {
		return nil, errors.New("runtime: nil readyAttempt")
	}
	r.mu.Lock()
	cond := r.getCond()
	for r.opInFlight {
		cond.Wait()
	}
	if r.state == readyStateConsumed || r.state == readyStateDisposed || r.pendingInvalidation != nil {
		r.mu.Unlock()
		return nil, errors.New("runtime: readyAttempt already consumed")
	}
	if r.state == readyStateActive {
		r.mu.Unlock()
		return nil, errors.New("runtime: readyAttempt not prepared")
	}
	if r.state != readyStatePrepared {
		r.mu.Unlock()
		return nil, fmt.Errorf("runtime: readyAttempt not prepared (state=%v)", r.state)
	}
	r.state = readyStateConsumed
	sess := r.session
	r.session = nil
	r.pending = pendingSelectionEffects{}
	if sess != nil {
		sess.releaseKind = ""
		sess.defaultCommand = sdkterminal.CommandCancel
		sess.defaultLegOutcome = usageevidence.LegOutcomeCanceled
	}
	cond.Broadcast()
	r.mu.Unlock()
	if sess == nil {
		return nil, errors.New("runtime: readyAttempt session is nil")
	}
	return sess, nil
}

func (r *readyAttempt) markStreamDisposed() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		r.session.streamDisposed = true
	}
}

type readyLifecycleHandle struct {
	ready *readyAttempt
}

func (h *readyLifecycleHandle) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	if h == nil || h.ready == nil {
		return aipapi.CancelResult{Mode: aipapi.CancelModeNone}
	}
	return h.ready.cancelViaLifecycle(ctx, cause)
}

func (h *readyLifecycleHandle) Close() error {
	if h == nil || h.ready == nil {
		return nil
	}
	return h.ready.closeViaLifecycle()
}

func (h *readyLifecycleHandle) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (r *readyAttempt) lifecycleHandle() leglifecycle.BLegAttempt {
	if r == nil {
		return nil
	}
	return &readyLifecycleHandle{ready: r}
}

func (r *readyAttempt) setDefaultEvidence(releaseKind usagelimit.ReleaseKind, cmd sdkterminal.Command, legOutcome usageevidence.LegOutcome) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultReleaseKind = releaseKind
	r.defaultCommand = cmd
	r.defaultLegOutcome = legOutcome
	if r.session != nil {
		r.session.releaseKind = releaseKind
		r.session.defaultCommand = cmd
		r.session.defaultLegOutcome = legOutcome
	}
}

func (r *readyAttempt) setPending(p pendingSelectionEffects) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.pending = p
	r.mu.Unlock()
}

// armPendingInvalidationLocked records a pending lifecycle invalidation and
// immediately detaches the session under the readyAttempt mutex. Detaching at
// record time -- before waiting for the in-flight publication op -- guarantees
// that any competing caller that inspects the readyAttempt (for example the
// post-open terminal fallback in Execute) observes no session it could
// terminalize. The lifecycle invalidation path owns the returned session as the
// single terminal owner once the in-flight op finishes.
func (r *readyAttempt) armPendingInvalidationLocked(inv pendingInvalidation) *attemptSession {
	r.pendingInvalidation = &inv
	r.state = readyStateDisposed
	sess := r.session
	r.session = nil
	return sess
}

func (r *readyAttempt) cancelViaLifecycle(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	if r == nil {
		return aipapi.CancelResult{Mode: aipapi.CancelModeNone}
	}
	r.mu.Lock()
	if r.state == readyStateConsumed {
		sess := r.boundSess
		r.mu.Unlock()
		if sess != nil {
			return sess.cancelViaLifecycle(ctx, cause)
		}
		return aipapi.CancelResult{Mode: aipapi.CancelModeNone}
	}
	if r.state == readyStateDisposed {
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		sess := r.boundSess
		r.mu.Unlock()
		if cr, ok := storedCancelResult(sess); ok {
			return cr
		}
		return aipapi.CancelResult{Mode: aipapi.CancelModeNone}
	}
	if r.opInFlight {
		if r.pendingInvalidation == nil {
			sess := r.armPendingInvalidationLocked(pendingInvalidation{
				kind:        invalidationCancel,
				cancelCause: &cause,
			})
			cond := r.getCond()
			for r.opInFlight {
				cond.Wait()
			}
			r.pending = pendingSelectionEffects{}
			relKind := r.defaultReleaseKind
			cmd := r.defaultCommand
			legOutcome := r.defaultLegOutcome
			cond.Broadcast()
			r.mu.Unlock()

			if sess != nil {
				termRes := r.terminalizeSessionForCancel(ctx, sess, cause, cmd, relKind, legOutcome)
				return termRes.Cancellation
			}
			return aipapi.CancelResult{Mode: aipapi.CancelModeNone}
		}
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		sess := r.boundSess
		r.mu.Unlock()
		if cr, ok := storedCancelResult(sess); ok {
			return cr
		}
		return aipapi.CancelResult{Mode: aipapi.CancelModeNone}
	}

	r.state = readyStateDisposed
	sess := r.session
	r.session = nil
	r.pending = pendingSelectionEffects{}
	relKind := r.defaultReleaseKind
	cmd := r.defaultCommand
	legOutcome := r.defaultLegOutcome
	cond := r.getCond()
	cond.Broadcast()
	r.mu.Unlock()

	if sess != nil {
		termRes := r.terminalizeSessionForCancel(ctx, sess, cause, cmd, relKind, legOutcome)
		return termRes.Cancellation
	}
	return aipapi.CancelResult{Mode: aipapi.CancelModeNone}
}

func (r *readyAttempt) terminalizeSessionForCancel(
	ctx context.Context,
	sess *attemptSession,
	cause aipapi.CancelCause,
	cmd sdkterminal.Command,
	relKind usagelimit.ReleaseKind,
	legOutcome usageevidence.LegOutcome,
) attemptTerminalResult {
	return sess.terminalizeForCancel(ctx, cause, cmd, relKind, legOutcome)
}

func (r *readyAttempt) closeViaLifecycle() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.state == readyStateConsumed {
		sess := r.boundSess
		r.mu.Unlock()
		if sess != nil {
			return sess.closeViaLifecycle()
		}
		return nil
	}
	if r.state == readyStateDisposed {
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return nil
	}
	if r.opInFlight {
		if r.pendingInvalidation == nil {
			sess := r.armPendingInvalidationLocked(pendingInvalidation{
				kind: invalidationClose,
			})
			cond := r.getCond()
			for r.opInFlight {
				cond.Wait()
			}
			r.pending = pendingSelectionEffects{}
			relKind, cmd, legOutcome := r.defaultReleaseKind, r.defaultCommand, r.defaultLegOutcome
			cond.Broadcast()
			r.mu.Unlock()

			if sess != nil {
				r.terminalizeSessionForClose(sess, cmd, relKind, legOutcome)
			}
			return nil
		}
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return nil
	}

	r.state = readyStateDisposed
	sess := r.session
	r.session = nil
	r.pending = pendingSelectionEffects{}
	relKind, cmd, legOutcome := r.defaultReleaseKind, r.defaultCommand, r.defaultLegOutcome
	cond := r.getCond()
	cond.Broadcast()
	r.mu.Unlock()

	if sess != nil {
		r.terminalizeSessionForClose(sess, cmd, relKind, legOutcome)
	}
	return nil
}

func (r *readyAttempt) terminalizeSessionForClose(
	sess *attemptSession,
	cmd sdkterminal.Command,
	relKind usagelimit.ReleaseKind,
	legOutcome usageevidence.LegOutcome,
) {
	sess.terminalizeForClose(cmd, relKind, legOutcome)
}

func (r *readyAttempt) DisposeWithEvidence(ctx context.Context, intent attemptTerminalIntent, evidence attemptEvidence) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.state == readyStateConsumed || r.state == readyStateDisposed {
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return
	}
	if r.opInFlight {
		if r.pendingInvalidation == nil {
			sess := r.armPendingInvalidationLocked(pendingInvalidation{
				kind:     invalidationDispose,
				intent:   intent,
				evidence: &evidence,
			})
			cond := r.getCond()
			for r.opInFlight {
				cond.Wait()
			}
			r.pending = pendingSelectionEffects{}
			cond.Broadcast()
			r.mu.Unlock()

			if sess != nil {
				if evidence.TraceID == "" {
					evidence.TraceID = sess.traceID
				}
				if evidence.ALegID == "" {
					evidence.ALegID = sess.bleg.ALegID
				}
				if evidence.StartedAt.IsZero() {
					evidence.StartedAt = sess.accountingStartedAt()
				}
				sess.TerminalizeAttempt(ctx, intent, evidence)
			}
			return
		}
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return
	}

	r.state = readyStateDisposed
	sess := r.session
	r.session = nil
	r.pending = pendingSelectionEffects{}
	cond := r.getCond()
	cond.Broadcast()
	r.mu.Unlock()

	if sess != nil {
		if evidence.TraceID == "" {
			evidence.TraceID = sess.traceID
		}
		if evidence.ALegID == "" {
			evidence.ALegID = sess.bleg.ALegID
		}
		if evidence.StartedAt.IsZero() {
			evidence.StartedAt = sess.accountingStartedAt()
		}
		sess.TerminalizeAttempt(ctx, intent, evidence)
	}
}

func (r *readyAttempt) Dispose(ctx context.Context, err error) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.state == readyStateConsumed || r.state == readyStateDisposed {
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return
	}
	if r.opInFlight {
		if r.pendingInvalidation == nil {
			sess := r.armPendingInvalidationLocked(pendingInvalidation{
				kind: invalidationDispose,
				err:  err,
			})
			cond := r.getCond()
			for r.opInFlight {
				cond.Wait()
			}
			r.pending = pendingSelectionEffects{}
			cond.Broadcast()
			r.mu.Unlock()

			if sess != nil {
				if err == nil {
					err = errors.New("runtime: attempt aborted before return")
				}
				intent := IntentPreReturnAbort
				cmd := sdkterminal.CommandBackendOpenFailure
				outcome := usageevidence.LegOutcomeFailed
				var rel usagelimit.ReleaseKind
				var obsOutcome response.StreamOutcome
				var recOutcome aipapi.AttemptOutcome

				if errors.Is(err, context.Canceled) {
					outcome = usageevidence.LegOutcomeCanceled
					cmd = sdkterminal.CommandCancel
				} else if err != nil && errors.Is(err, errPublicationClosed) {
					intent = IntentSwallowedFailure
					cmd = sdkterminal.CommandSwallowedAttempt
					outcome = usageevidence.LegOutcomeSwallowed
					rel = usagelimit.ReleaseKindSwallowed
					obsOutcome = response.OutcomeReplaced
					recOutcome = aipapi.AttemptSwallowedFailure
				}

				evidence := attemptEvidence{
					Command:       cmd,
					LegOutcome:    outcome,
					ReleaseKind:   rel,
					ObsOutcome:    obsOutcome,
					RecordOutcome: recOutcome,
					Usage:         emptyOperatorUsageShell(),
					Err:           err,
					RecordReason:  err.Error(),
					TraceID:       sess.traceID,
					ALegID:        sess.bleg.ALegID,
					StartedAt:     sess.accountingStartedAt(),
				}
				sess.TerminalizeAttempt(ctx, intent, evidence)
			}
			return
		}
		cond := r.getCond()
		for r.opInFlight {
			cond.Wait()
		}
		r.mu.Unlock()
		return
	}

	r.state = readyStateDisposed
	sess := r.session
	r.session = nil
	r.pending = pendingSelectionEffects{}
	cond := r.getCond()
	cond.Broadcast()
	r.mu.Unlock()

	if sess != nil {
		if err == nil {
			err = errors.New("runtime: attempt aborted before return")
		}
		intent := IntentPreReturnAbort
		cmd := sdkterminal.CommandBackendOpenFailure
		outcome := usageevidence.LegOutcomeFailed
		var rel usagelimit.ReleaseKind
		var obsOutcome response.StreamOutcome
		var recOutcome aipapi.AttemptOutcome

		if errors.Is(err, context.Canceled) {
			outcome = usageevidence.LegOutcomeCanceled
			cmd = sdkterminal.CommandCancel
		} else if err != nil && errors.Is(err, errPublicationClosed) {
			intent = IntentSwallowedFailure
			cmd = sdkterminal.CommandSwallowedAttempt
			outcome = usageevidence.LegOutcomeSwallowed
			rel = usagelimit.ReleaseKindSwallowed
			obsOutcome = response.OutcomeReplaced
			recOutcome = aipapi.AttemptSwallowedFailure
		}

		evidence := attemptEvidence{
			Command:       cmd,
			LegOutcome:    outcome,
			ReleaseKind:   rel,
			ObsOutcome:    obsOutcome,
			RecordOutcome: recOutcome,
			Usage:         emptyOperatorUsageShell(),
			Err:           err,
			RecordReason:  err.Error(),
			TraceID:       sess.traceID,
			ALegID:        sess.bleg.ALegID,
			StartedAt:     sess.accountingStartedAt(),
		}
		sess.TerminalizeAttempt(ctx, intent, evidence)
	}
}

func (sess *attemptSession) abortPreReturn(ctx context.Context, err error) {
	if sess == nil {
		return
	}
	if err == nil {
		err = errors.New("runtime: attempt aborted before return")
	}
	outcome := usageevidence.LegOutcomeFailed
	if errors.Is(err, context.Canceled) {
		outcome = usageevidence.LegOutcomeCanceled
	}
	evidence := attemptEvidence{
		Command:      sdkterminal.CommandBackendOpenFailure,
		LegOutcome:   outcome,
		Usage:        emptyOperatorUsageShell(),
		Err:          err,
		RecordReason: err.Error(),
	}
	sess.TerminalizeAttempt(ctx, IntentPreReturnAbort, evidence)
}

// attemptTerminalIntent describes the reason/intent for terminalizing the attempt.
type attemptTerminalIntent string

const (
	IntentSuccess              attemptTerminalIntent = "success"
	IntentSwallowedFailure     attemptTerminalIntent = "swallowed_failure"
	IntentSurfacedFailure      attemptTerminalIntent = "surfaced_failure"
	IntentCancellation         attemptTerminalIntent = "cancellation"
	IntentTimeout              attemptTerminalIntent = "timeout"
	IntentReplacement          attemptTerminalIntent = "replacement"
	IntentParallelLoser        attemptTerminalIntent = "parallel_loser"
	IntentOpenReadinessFailure attemptTerminalIntent = "open_readiness_failure"
	IntentPublicationDenied    attemptTerminalIntent = "publication_denied"
	IntentPreReturnAbort       attemptTerminalIntent = "pre_return_abort"
)

type attemptEvidence struct {
	Command        sdkterminal.Command
	ReleaseKind    usagelimit.ReleaseKind
	LegOutcome     usageevidence.LegOutcome
	Usage          aipapi.Event
	Err            error
	ObsOutcome     response.StreamOutcome
	TraceID        string
	ALegID         string
	Snapshot       *coreterm.AccumulatorSnapshot
	RecordOutcome  aipapi.AttemptOutcome
	RecordReason   string
	BillingReason  string
	StartedAt      time.Time
	StreamFallback aipapi.Event
	BillingState   *billingCallState
	BillingCallID  usageevidence.BillingCallID
	Committed      bool
	BillingLegFn   func(ctx context.Context, started, finished time.Time, outcome usageevidence.LegOutcome)
	ObserveEvent   *aipapi.Event
	// DeferFinalStreamObservation hands the final-client observation of the
	// prepared usage and the finish, plus the finalStreamObs Finish, to the
	// accepted normal publication that is about to observe additional canonical
	// events. Attempt accounting, usage authority, billing, and teardown ownership
	// are unchanged; only the final-client observation is deferred. The observer
	// stays attempt-owned, is never reopened, and is finished conservatively by
	// the abandoning caller otherwise.
	DeferFinalStreamObservation bool
	AuthorityPrepare            func(context.Context) (usageEv aipapi.Event, authorityEv aipapi.Event, ok bool, err error)
	CancelCause                 *aipapi.CancelCause
}

type attemptTerminalResult struct {
	Result       coreterm.Result
	Cancellation aipapi.CancelResult
}

func (a *attemptSession) makeSwallowedEvidence(facts recvTurnFacts, p *responsePipeline, committed bool, reason string, err error) attemptEvidence {
	var snapshot *coreterm.AccumulatorSnapshot
	var usage aipapi.Event
	var fallback aipapi.Event
	if p != nil {
		snap := p.accumulatorSnapshot()
		snapshot = &snap
		usage = p.operatorUsageForFinalize()
		fallback = p.billingEvidenceFallback()
	}
	tFacts := facts.terminalFacts()
	return attemptEvidence{
		ReleaseKind:    usagelimit.ReleaseKindSwallowed,
		Usage:          usage,
		TraceID:        facts.traceID,
		ALegID:         facts.aLegID,
		Snapshot:       snapshot,
		RecordReason:   reason,
		Err:            err,
		StartedAt:      a.accountingStartedAt(),
		StreamFallback: fallback,
		BillingState:   tFacts.billingState,
		BillingCallID:  tFacts.billingCallID,
		Committed:      committed,
	}
}

func (a *attemptSession) terminalizeSwallowed(ctx context.Context, facts recvTurnFacts, p *responsePipeline, committed bool, reason string, err error) {
	if a == nil {
		return
	}
	ev := a.makeSwallowedEvidence(facts, p, committed, reason, err)
	ev.Command = sdkterminal.CommandSwallowedAttempt
	ev.LegOutcome = usageevidence.LegOutcomeSwallowed
	ev.ObsOutcome = response.OutcomeReplaced
	ev.RecordOutcome = aipapi.AttemptSwallowedFailure
	a.TerminalizeAttempt(ctx, IntentSwallowedFailure, ev)
}

func (a *attemptSession) terminalizeEarlyCancellation(ctx context.Context, facts recvTurnFacts, p *responsePipeline, committed bool, reason string, err error) {
	if a == nil {
		return
	}
	ev := a.makeSwallowedEvidence(facts, p, committed, reason, err)
	ev.Command = sdkterminal.CommandCancel
	ev.LegOutcome = usageevidence.LegOutcomeCanceled
	ev.ObsOutcome = response.OutcomeCancelled
	ev.RecordOutcome = aipapi.AttemptCancelled
	ev.BillingReason = reason
	a.TerminalizeAttempt(ctx, IntentCancellation, ev)
}

func (a *attemptSession) TerminalizeAttempt(ctx context.Context, intent attemptTerminalIntent, evidence attemptEvidence) attemptTerminalResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if a == nil {
		return attemptTerminalResult{Result: coreterm.Result{Err: sdkterminal.ErrInvalid}}
	}
	// Every terminal exit, including a competing claim or an invalid owner,
	// must release the attempt-owned evidence; otherwise a construction/close
	// failure can leave it available to a replacement invocation.
	defer func() {
		_, _ = a.billingEvidenceDrain()
		_, _ = a.economicEvidenceDrain()
		_ = a.drainLocalBoundaryObservations(time.Now().UTC())
		_ = a.flushEconomicCheckpointsAtTerminal(ctx)
	}()
	if a.terminal == nil {
		return attemptTerminalResult{Result: coreterm.Result{Err: sdkterminal.ErrInvalid}}
	}
	a.terminalizing.Store(true)
	defer a.terminalizing.Store(false)

	cmd := evidence.Command
	if cmd == "" || !cmd.AllowsScope(sdkterminal.ScopeAttempt) {
		cmd = mapIntentToCommand(intent)
	}

	res := a.terminal.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		if evidence.Snapshot != nil {
			return *evidence.Snapshot
		}
		return coreterm.NewAccumulatorSnapshot(nil, false)
	}, func(cctx context.Context, out coreterm.Outcome) error {
		// Every terminal callback owns the attempt accumulator. The normal leg
		// append path drains it below; this defer also covers legacy callbacks,
		// missing call identities, and a duplicate terminal callback that cannot
		// claim a second leg record.
		defer func() {
			_, _ = a.billingEvidenceDrain()
			_, _ = a.economicEvidenceDrain()
		}()
		var errorsList []error
		innerStream := a.takeInner()
		var cancelRes aipapi.CancelResult
		contextsCanceled := errors.Is(evidence.Err, context.Canceled) || errors.Is(cctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled)
		isCancel, cause := deriveAttemptCancellation(intent, evidence, a.pendingCancelCause.Load(), contextsCanceled)

		// 2. Cancel/close when intent requires.
		if innerStream != nil && !a.streamDisposed {
			a.drainStreamUsageEvidence(innerStream)
			closeInner := func() error {
				return safety.Call(safety.BoundaryBackend, "backend_stream_close", func() error { return innerStream.Close() })
			}
			shouldCancel := (intent != IntentSuccess) || cause.Kind != ""
			var closeErr error
			if shouldCancel {
				if cause.Kind == "" {
					cause = aipapi.CancelCause{Kind: aipapi.CancelContextDone}
				}
				cancelRes, closeErr = leglifecycle.BoundedCancelAndClose(cctx, defaultAuthorityCleanupTimeout,
					func(cancelCtx context.Context) aipapi.CancelResult {
						var result aipapi.CancelResult
						_ = safety.Call(safety.BoundaryBackend, "backend_stream_cancel", func() error {
							result = innerStream.Cancel(cancelCtx, cause)
							return nil
						})
						return result
					},
					closeInner,
					a.forceClose,
				)
				if cancelRes.Err != nil && !errors.Is(cancelRes.Err, context.Canceled) {
					errorsList = append(errorsList, cancelRes.Err)
				}
				termObs := a.recordCancellationTelemetry(cause, cancelRes, innerStream, isCancel)
				a.logAttemptCanceled(cctx, evidence, termObs)
				a.drainStreamUsageEvidence(innerStream)
			} else {
				closeErr = closeInner()
			}
			if closeErr != nil {
				if pe, ok := errors.AsType[*safety.PanicError](closeErr); ok {
					// Isolate close panic, log at debug, do not fail terminal effect
					if a.finalStreamObs != nil && a.finalStreamObs.Log != nil {
						traceID := strings.TrimSpace(evidence.TraceID)
						if traceID == "" {
							traceID = strings.TrimSpace(a.traceID)
						}
						aLegID := strings.TrimSpace(evidence.ALegID)
						if aLegID == "" {
							aLegID = strings.TrimSpace(a.bleg.ALegID)
						}
						logCtx := diag.EnsureCallDiag(cctx, traceID, aLegID)
						attrs := diag.IsolatedCrashAttrs(logCtx, pe, diag.CrashAttrOpts{
							AttrOpts:   diag.AttrOpts{CallID: traceID, BLegID: a.bleg.BLegID},
							AttemptSeq: int(a.bleg.Seq),
						})
						attrs = diag.AppendIsolatedCrashStack(attrs, pe)
						a.finalStreamObs.Log.LogAttrs(logCtx, slog.LevelError, "isolated_panic_backend_stream_close", attrs...)
					}
				} else if !errors.Is(closeErr, context.Canceled) {
					errorsList = append(errorsList, fmt.Errorf("runtime: failed to close inner stream: %w", closeErr))
				}
			}
			a.drainStreamUsageEvidence(innerStream)
		} else if isCancel {
			termObs := a.recordCancellationTelemetry(cause, cancelRes, nil, isCancel)
			a.logAttemptCanceled(cctx, evidence, termObs)
		} else if innerStream != nil {
			a.drainStreamUsageEvidence(innerStream)
		}

		a.innerMu.Lock()
		a.cancelResult = cancelRes
		a.innerMu.Unlock()

		// 3. Pre-terminal preparation (AuthorityPrepare / token accounting).
		var preparedUsageEv aipapi.Event
		var preparedOK bool
		if evidence.AuthorityPrepare != nil {
			runGuarded("authority prepare", &errorsList, func() {
				usageEv, authorityEv, ok, err := evidence.AuthorityPrepare(cctx)
				if err != nil {
					errorsList = append(errorsList, err)
				}
				preparedUsageEv, preparedOK = usageEv, ok
				if ok {
					evidence.Usage = authorityEv
					if authorityEv.Kind == "" {
						evidence.Usage = usageEv
					}
				}
			})
		}

		// 4. Finish final-stream observation (Finish) - observe before finish inside winner.
		// A deferred accepted normal publication owns this observation instead, so it
		// can observe its own prepared result before the usage and the finish.
		if a.finalStreamObs != nil && !evidence.DeferFinalStreamObservation {
			obsOutcome := response.OutcomeFailed
			if intent == IntentSuccess {
				obsOutcome = response.OutcomeSuccessReleased
			} else if intent == IntentCancellation || intent == IntentTimeout {
				obsOutcome = response.OutcomeCancelled
			} else if intent == IntentReplacement || intent == IntentSwallowedFailure {
				obsOutcome = response.OutcomeReplaced
			} else if cmd == sdkterminal.CommandClose {
				obsOutcome = response.OutcomeClosed
			}
			if evidence.ObsOutcome != "" {
				obsOutcome = evidence.ObsOutcome
			}
			if len(errorsList) > 0 {
				obsOutcome = response.OutcomeFailed
			}

			// If synthesized usage was generated, observe it before finishing.
			if preparedOK && preparedUsageEv.Kind != "" {
				runGuarded("finalStreamObs.Observe", &errorsList, func() {
					if err := extensions.RunFinalStreamObservationStage(cctx, a.finalStreamObs.Log, a.finalStreamObs.Metrics, a.finalStreamObs, preparedUsageEv, evidence.Committed); err != nil {
						errorsList = append(errorsList, err)
						obsOutcome = response.OutcomeFailed
					}
				})
			}

			if evidence.ObserveEvent != nil {
				runGuarded("finalStreamObs.Observe", &errorsList, func() {
					if err := extensions.RunFinalStreamObservationStage(cctx, a.finalStreamObs.Log, a.finalStreamObs.Metrics, a.finalStreamObs, *evidence.ObserveEvent, evidence.Committed); err != nil {
						errorsList = append(errorsList, err)
						obsOutcome = response.OutcomeFailed
					}
				})
			}
			runGuarded("finalStreamObs.Finish", &errorsList, func() {
				a.finalStreamObs.Finish(cctx, obsOutcome)
			})
		}

		// 5. Finalize/release attempt authority.
		if a.authority.control != nil && !a.authority.Settled() {
			runGuarded("authority finalize", &errorsList, func() {
				usage := a.usageOrAccumulated(evidence.Usage)
				switch intent {
				case IntentSuccess:
					_ = a.authority.Settle(cctx, usagelimit.SettlementKindFinal, usage, false)
					a.authority.ApplyUnreservedUsage(cctx, usagelimit.SettlementKindFinal, usage)
				case IntentCancellation:
					if a.authority.Settled() {
						a.authority.ReconcileAuthoritative(cctx, usage)
					} else if evidence.ReleaseKind != "" {
						a.authority.finalizeIncurredOrRelease(cctx, evidence.ReleaseKind, usage)
					} else {
						_ = a.authority.Settle(cctx, usagelimit.SettlementKindCancellation, usage, true)
					}
					a.authority.ApplyUnreservedUsage(cctx, usagelimit.SettlementKindCancellation, usage)
				case IntentTimeout:
					kind := evidence.ReleaseKind
					if kind == "" {
						kind = usagelimit.ReleaseKindLosing
					}
					a.authority.finalizeIncurredOrRelease(cctx, kind, usage)
					a.authority.ApplyUnreservedUsage(cctx, usagelimit.SettlementKindLosing, usage)
				case IntentSwallowedFailure, IntentPublicationDenied, IntentReplacement:
					kind := evidence.ReleaseKind
					if kind == "" {
						kind = usagelimit.ReleaseKindSwallowed
					}
					a.authority.finalizeIncurredOrRelease(cctx, kind, usage)
					a.authority.ApplyUnreservedUsage(cctx, usagelimit.SettlementKindPartial, usage)
				case IntentSurfacedFailure:
					_ = a.authority.Settle(cctx, usagelimit.SettlementKindPartial, usage, false)
					a.authority.ApplyUnreservedUsage(cctx, usagelimit.SettlementKindPartial, usage)
				case IntentParallelLoser:
					a.authority.finalizeIncurredOrRelease(cctx, usagelimit.ReleaseKindLosing, usage)
				default:
					kind := evidence.ReleaseKind
					if kind == "" {
						kind = mapIntentToReleaseKind(intent, a.authority.backendAttempted)
					}
					a.authority.finalizeIncurredOrRelease(cctx, kind, usage)
				}
			})
		}

		// 6. Emit attempt egress metering (emitBackendEgressMeteringFact).
		if a.emitBackendEgressFn != nil && a.authority.backendAttempted != nil && a.authority.backendAttempted.Load() {
			runGuarded("emitBackendEgress", &errorsList, func() {
				outcomeMeter := metering.AttemptOutcomeFailed
				switch intent {
				case IntentSuccess:
					outcomeMeter = metering.AttemptOutcomeWinner
				case IntentCancellation, IntentTimeout:
					outcomeMeter = metering.AttemptOutcomeCanceled
				case IntentParallelLoser:
					outcomeMeter = metering.AttemptOutcomeLoser
				}
				surfaced := metering.SurfacedNo
				if intent == IntentSuccess {
					surfaced = metering.SurfacedYes
				}
				usage := a.usageOrAccumulated(evidence.Usage)
				a.emitBackendEgressFn(cctx, a.bleg.BLegID, outcomeMeter, surfaced, usage)
			})
		}

		// 7. Release/end B-leg lifecycle (ReleaseBLeg / ALeg scope).
		if a.aScope != nil && a.bleg.BLegID != "" {
			runGuarded("ReleaseBLeg", &errorsList, func() {
				a.aScope.ReleaseBLeg(a.bleg.BLegID)
			})
		}

		// 8. Append billing leg evidence exactly once.
		if a.claimBillingLegRecord() {
			runGuarded("append billing leg", &errorsList, func() {
				started := evidence.StartedAt
				if started.IsZero() {
					started = a.accountingStartedAt()
				}
				var nowFn func() time.Time
				if a.now != nil {
					nowFn = a.now
				} else {
					nowFn = time.Now
				}
				finished := nowFn()
				if started.IsZero() {
					started = finished
				}
				legOutcome := evidence.LegOutcome
				if legOutcome == "" {
					legOutcome = mapCommandToLegOutcome(cmd)
				}
				if evidence.BillingLegFn != nil {
					a.checkpointLocalBoundaryObservations(finished)
					if err := a.flushEconomicCheckpointsAtTerminal(cctx); err != nil {
						errorsList = append(errorsList, err)
					}
					evidence.BillingLegFn(cctx, started, finished, legOutcome)
					return
				}
				traceID := strings.TrimSpace(evidence.TraceID)
				if traceID == "" {
					traceID = strings.TrimSpace(a.traceID)
				}
				aLegID := strings.TrimSpace(evidence.ALegID)
				if aLegID == "" {
					aLegID = strings.TrimSpace(a.bleg.ALegID)
				}
				billingState := evidence.BillingState
				if billingState == nil {
					billingState = a.billingCallState
				}
				callID := evidence.BillingCallID
				if callID == "" {
					callID = a.billingCallID
				}
				if callID == "" && billingState != nil {
					callID = billingState.callID
				}
				blegID := strings.TrimSpace(a.bleg.BLegID)
				if blegID == "" {
					blegID = billingSyntheticBLegID(a.bleg.Seq)
				}
				if billingState != nil {
					if a.bleg.Seq > 0 {
						billingState.noteAllocatedBLeg(blegID, a.bleg.Seq)
					}
					billingState.noteLegTimes(started, finished)
				}
				surfaced := usageevidence.SurfacedNo
				if cmd == sdkterminal.CommandNormalFinish || evidence.Committed {
					surfaced = usageevidence.SurfacedYes
				}
				recordRequired := a.billingLegRecordRequired()
				var localObservations []metering.Observation
				if recordRequired {
					localObservations = a.drainLocalBoundaryObservations(finished)
				}
				finalizeReason := "record_leg"
				if evidence.BillingReason != "" {
					finalizeReason = evidence.BillingReason
				} else if evidence.RecordReason != "" {
					finalizeReason = evidence.RecordReason
				}
				finalizeEv := evidence.StreamFallback
				finalizeApplied := false
				if (recordRequired || a.observationSink != nil) && billingState != nil && (a.finalizeBilling != nil || a.finalizeBillingV2 != nil) {
					if result, ok := a.finalizeBillingResult(cctx, billingState, execbackend.BillingFinalizationInput{
						TraceID: traceID,
						ALegID:  aLegID,
						BLegID:  strings.TrimSpace(a.bleg.BLegID),
						Backend: strings.TrimSpace(a.cand.Primary.Backend),
						Model:   strings.TrimSpace(a.cand.Primary.Model),
						Reason:  finalizeReason,
					}); ok {
						finalizeEv = result.Usage
						finalizeApplied = true
						for _, economic := range result.EconomicEvidence {
							a.rememberEconomicEvidenceOnce(economic)
						}
					}
				}
				if err := a.flushEconomicCheckpointsAtTerminal(cctx); err != nil {
					errorsList = append(errorsList, err)
				}
				if !recordRequired {
					// No observer or terminal sink is bound: the terminal record
					// has no consumer, so do not build and discard it.
					return
				}
				streamEv := a.augmentBillingUsage(evidence.StreamFallback, evidence.Usage)
				if !finalizeApplied {
					finalizeEv = streamEv
				}
				evidenceEvents, evidenceConflicts := a.billingEvidenceDrain()
				economicObservations, economicOrdinaryConflicts, economicRetentionConflicts := a.economicEvidenceDrainPartitioned()
				var opRef usageevidence.VersionRef
				if a.operatorRateRef != nil {
					opRef = a.operatorRateRef(cctx, a.cand.Primary)
				}
				var workload usageevidence.WorkloadIdentity
				if a.billingWorkload != nil {
					workload = a.billingWorkload(cctx, aLegID)
				}
				legRecord := billingLegRecord(billingLegDraft{
					callID:                    callID,
					submissionID:              a.submissionID,
					aLegID:                    aLegID,
					storeID:                   a.billingStoreID,
					bLegID:                    a.bleg.BLegID,
					seq:                       a.bleg.Seq,
					primary:                   a.cand.Primary,
					startedAt:                 started,
					finishedAt:                finished,
					command:                   cmd,
					outcome:                   legOutcome,
					surfaced:                  surfaced,
					finalize:                  finalizeEv,
					stream:                    streamEv,
					evidenceEvents:            evidenceEvents,
					evidenceConflicts:         evidenceConflicts,
					economicObservations:      economicObservations,
					economicConflicts:         economicRetentionConflicts,
					economicOrdinaryConflicts: economicOrdinaryConflicts,
					localObservations:         localObservations,
					operatorRateRef:           opRef,
					workload:                  workload,
				})
				if a.observeBillingLeg != nil {
					a.observeBillingLeg(cctx, legRecord)
				}
				if callID != "" {
					// The session-frozen scope rides the handoff even when
					// terminalization runs detached (A-leg close/cancel).
					scopeCtx := withTerminalScope(cctx, a.boundaryScope)
					if a.appendBillingLegStrict != nil {
						if err := a.appendBillingLegStrict(scopeCtx, callID, legRecord); err != nil {
							errorsList = append(errorsList, fmt.Errorf("runtime: terminal billing leg durability: %w", err))
						}
					} else if a.appendBillingLeg != nil {
						a.appendBillingLeg(scopeCtx, callID, legRecord)
					}
				}
				if a.observeBillingLeg == nil && a.appendBillingLeg == nil && a.appendBillingLegStrict == nil && a.appendBillingLegFn != nil {
					a.appendBillingLegFn(cctx, a.bleg, a.cand.Primary, started, finished, legOutcome)
				}
			})
		}

		// 9. Record attempt outcome/evidence (recordAttemptLogged).
		if a.recordAttemptLoggedFn != nil && a.bleg.BLegID != "" {
			runGuarded("recordAttemptLogged", &errorsList, func() {
				var outcomeLog aipapi.AttemptOutcome
				if errors.Is(evidence.Err, context.Canceled) || errors.Is(cctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
					outcomeLog = aipapi.AttemptCancelled
				} else {
					switch intent {
					case IntentSuccess:
						outcomeLog = aipapi.AttemptSuccess
					case IntentCancellation, IntentTimeout, IntentParallelLoser:
						outcomeLog = aipapi.AttemptCancelled
					case IntentSwallowedFailure, IntentPublicationDenied:
						outcomeLog = aipapi.AttemptSwallowedFailure
					default:
						outcomeLog = aipapi.AttemptSurfacedFailure
					}
				}
				if evidence.RecordOutcome != "" {
					outcomeLog = evidence.RecordOutcome
				}
				reasonLog := string(intent)
				if evidence.RecordReason != "" {
					reasonLog = evidence.RecordReason
				}
				a.recordAttemptLoggedFn(cctx, recordAttemptParams{
					ALegID:    evidence.ALegID,
					BLeg:      a.bleg,
					Cand:      a.cand,
					Outcome:   outcomeLog,
					Reason:    reasonLog,
					DetailErr: evidence.Err,
				}, diag.AttrOpts{CallID: evidence.TraceID, BLegID: a.bleg.BLegID})
			})
		}

		// Detach sideband state without mutating an assembler that a concurrent
		// Recv may already have snapshotted.
		a.discardSidebandState()

		if evidence.Err != nil && intent == IntentSurfacedFailure {
			errorsList = append(errorsList, evidence.Err)
		}

		if len(errorsList) > 0 {
			return errors.Join(errorsList...)
		}
		return nil
	})

	a.innerMu.Lock()
	finalCancel := a.cancelResult
	a.innerMu.Unlock()
	if finalCancel.Mode == "" {
		finalCancel.Mode = aipapi.CancelModeNone
	}
	return attemptTerminalResult{Result: res, Cancellation: finalCancel}
}

func mapIntentToCommand(intent attemptTerminalIntent) sdkterminal.Command {
	switch intent {
	case IntentSuccess:
		return sdkterminal.CommandNormalFinish
	case IntentSwallowedFailure, IntentPublicationDenied, IntentReplacement:
		return sdkterminal.CommandSwallowedAttempt
	case IntentSurfacedFailure:
		return sdkterminal.CommandPartialError
	case IntentCancellation:
		return sdkterminal.CommandCancel
	case IntentTimeout:
		return sdkterminal.CommandTimeout
	case IntentParallelLoser:
		return sdkterminal.CommandParallelLoser
	case IntentOpenReadinessFailure, IntentPreReturnAbort:
		return sdkterminal.CommandBackendOpenFailure
	default:
		return sdkterminal.CommandPartialError
	}
}

func mapIntentToReleaseKind(intent attemptTerminalIntent, backendAttempted *atomic.Bool) usagelimit.ReleaseKind {
	incurred := backendAttempted != nil && backendAttempted.Load()
	switch intent {
	case IntentSuccess:
		return usagelimit.ReleaseKindLosing
	case IntentSwallowedFailure, IntentPublicationDenied:
		return usagelimit.ReleaseKindSwallowed
	case IntentParallelLoser:
		return usagelimit.ReleaseKindLosing
	case IntentOpenReadinessFailure, IntentPreReturnAbort:
		if incurred {
			return usagelimit.ReleaseKindLosing
		}
		return usagelimit.ReleaseKindAdmissionFailure
	default:
		return usagelimit.ReleaseKindLosing
	}
}

func mapCommandToLegOutcome(cmd sdkterminal.Command) usageevidence.LegOutcome {
	switch cmd {
	case sdkterminal.CommandNormalFinish:
		return usageevidence.LegOutcomeWinner
	case sdkterminal.CommandSwallowedAttempt:
		return usageevidence.LegOutcomeSwallowed
	case sdkterminal.CommandParallelLoser:
		return usageevidence.LegOutcomeLoser
	case sdkterminal.CommandCancel, sdkterminal.CommandClose:
		return usageevidence.LegOutcomeCanceled
	default:
		return usageevidence.LegOutcomeFailed
	}
}
