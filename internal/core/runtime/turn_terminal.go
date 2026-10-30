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

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// aLegEndMode names the one owner permitted to end the request's A-leg.
// Interleaved thinker/executor wrappers keep the A-leg open until their outer
// boundary; ordinary streams end it at their own terminal boundary.
type aLegEndMode uint8

const terminalDecisionEvaluationBudget = 250 * time.Millisecond

const (
	aLegEndBase aLegEndMode = iota
	aLegEndOuter
)

// aLegEndAuthority is the one concrete lifecycle owner shared by the base
// thinker and executor terminal views of an interleaved turn. Request/attempt
// terminal state stays separate; only A-leg lifecycle and end-once truth is
// shared across those views.
type aLegEndAuthority struct {
	scope *leglifecycle.ALeg
	mode  aLegEndMode
	once  sync.Once
}

// turnTerminal owns the logical request terminal, request-lifetime
// commitment/finished facts, and A-leg end authority. Attempt terminal
// ownership deliberately remains on each replaceable attemptSession;
// Terminalize composes with an explicitly snapshotted attempt instead of
// retaining one here.
type turnTerminal struct {
	request                  *streamTerminal
	aLegEndAuthority         *aLegEndAuthority
	commitment               atomic.Bool
	completion               atomic.Bool
	accountingFinalizedState atomic.Bool
	// acceptedCompletion latches the accepted receive-release completion of
	// the logical turn: it is set exactly once when the accepted finish is
	// released to the client at the receive-phase ownership boundary, and
	// never cleared by a later Close, Cancel, withdrawal, or replacement.
	// It is the scalar evidence that distinguishes an accepted completion
	// from a merely finished terminal, so Close-without-EOF after delivery
	// still audits success while a withdrawn claim cannot.
	acceptedCompletion atomic.Bool
	// releasedClientFrame is the turn-scoped latch of the strengthened
	// no-retry-after-client-output guarantee for the empty-visible-turn
	// corrective retry (requirement 2.3). It is set when the receive stream
	// actually returns a canonical client-visible frame to the client and is
	// never cleared by a later Close, cancellation, withdrawal, or replacement.
	releasedClientFrame atomic.Bool
	// correctiveRetryClaimed is this logical turn's one-shot corrective
	// allowance (requirement 4.1). It is claimed once, before the fallible
	// corrective open, and never refunded across replacement or continued
	// attempts.
	correctiveRetryClaimed atomic.Bool
	// correctionPendingState records that an accepted corrective replacement is
	// in flight: the allowance is spent and the empty attempt is retired, so the
	// receive loop's replacement block must run exactly one bounded open and end
	// the turn on failure instead of re-planning behind a keepalive
	// (requirements 4.2, 4.6). It is cleared when the successor publishes or the
	// open fails.
	correctionPendingState atomic.Bool
	// emptyVisibleTurnRetryGate is the generation-pinned value of the
	// `empty_visible_turn_retry` gate. bindTurnTerminalRuntime copies it from
	// the executor generation once per turn (requirements 1.1, 1.3), so a
	// reload can never change a turn admitted under its own generation.
	// emptyVisibleTurnRetryGateBound makes that capture first-write-wins,
	// matching the terminalDecisionAuxiliary capture pattern.
	emptyVisibleTurnRetryGate      bool
	emptyVisibleTurnRetryGateBound bool

	// interleavedThinker records the construction-time role of this terminal
	// view. It is terminal orchestration metadata, not facade state.
	interleavedThinker bool

	billingClosureMu      sync.Mutex
	billingClosureSuccess bool

	// Terminal-side runtime operations are injected individually at the
	// composition boundary. The terminal owner never receives the broad
	// Executor service surface.
	log                     *slog.Logger
	now                     func() time.Time
	billingEnabled          func() bool
	operatorRateRef         func(context.Context, routing.Primary) usageevidence.VersionRef
	billingWorkload         func(context.Context, string) usageevidence.WorkloadIdentity
	observeBillingLeg       func(context.Context, usageevidence.CallLegUsageRecord)
	appendBillingLeg        func(context.Context, usageevidence.BillingCallID, usageevidence.CallLegUsageRecord)
	appendBillingLegStrict  func(context.Context, usageevidence.BillingCallID, usageevidence.CallLegUsageRecord) error
	appendBillingCall       func(context.Context, usageevidence.CallUsageRecord) error
	logBillingAppendFailure func(context.Context, string, string, error)
	finalizeBilling         func(context.Context, execbackend.BillingFinalizationInput) (aipapi.Event, error)
	finalizeBillingV2       func(context.Context, execbackend.BillingFinalizationInput) (execbackend.BillingFinalizationResult, error)
	releaseRequestAuthority func(context.Context) error
	settleRequestAuthority  func(context.Context, []metering.Fact) error
	emitFrontendEgress      func(context.Context, string, aipapi.Event) (metering.Fact, bool)
	meteringRecorderPresent bool
	emitBackendEgress       func(context.Context, string, metering.AttemptOutcome, metering.SurfacedState, aipapi.Event)
	// recordEmptyVisibleTurn observes the bounded empty-visible-turn metric for
	// a surviving attempt whose clean terminal produced no visible output.
	recordEmptyVisibleTurn func(string)
	// recordEmptyVisibleTurnRetry observes one bounded corrective-retry
	// decision (outcome corrected|skipped|failed) for a candidate empty visible
	// turn through the optional metrics extension.
	recordEmptyVisibleTurnRetry func(backend, outcome string)

	// supportsContinuation indicates whether the A-side frontend can legally
	// stitch a continuation leg onto the same logical response. Zero value is
	// false (conservative unsupported) but all production constructors set it to
	// true for known stitchable frontends. Tests may set it to false to prove
	// a clean final without raw frame concatenation.
	supportsContinuation bool

	// toolNameCorrections counts the corrections this client turn has accepted.
	// It is turn-scoped rather than attempt-scoped, so a replacement attempt
	// cannot restart the budget; the single receive loop is its only producer.
	toolNameCorrections atomic.Int64

	// toolNameCorrectionOverlay is the correction message this turn published,
	// empty while this turn has none. It is set at publication and taken
	// exactly once at the response-end transition, where the overlay is
	// deactivated so a later turn never projects the hidden correction message
	// (requirement 2.5).
	toolNameCorrectionMu      sync.Mutex
	toolNameCorrectionOverlay correctionOverlayRef

	steeringWriterFactory SteeringWriterFactory
	conversationReader    conversationprojection.Reader
	conversationObserver  ConversationViewObserver

	terminalDecisionMu     sync.Mutex
	terminalDecisionFlight *terminalDecisionFlight

	// terminalDecisionProvider is generation-owned when wired by the
	// composition root. It remains nil until provider projection is implemented;
	// the evaluator's nil fast path preserves existing behavior.
	terminalDecisionProvider      terminaldecision.Provider
	terminalDecisionProviderID    string
	terminalDecisionProviderHasID bool
	// terminalDecisionAuxiliary is captured once from the immutable request
	// snapshot and copied into every terminal-decision input for this turn.
	terminalDecisionAuxiliary      auxiliary.Client
	terminalDecisionAuxiliaryBound bool

	// continuationTransaction is the one core-owned publication seam for a
	// provider continuation. It is installed when the receive stream is
	// assembled; the terminal owner only invokes it after a provisional
	// decision has returned Continue.
	continuationTransaction func(context.Context, terminaldecision.ContinuationIntent) (bool, error)
}

func bindTurnTerminalRuntime(t *turnTerminal, e *Executor) {
	if t == nil || e == nil {
		return
	}
	t.log = e.Log
	t.now = e.now
	t.billingEnabled = e.billingEnabled
	t.operatorRateRef = e.operatorRateRef
	t.billingWorkload = e.billingWorkloadIdentityForALeg
	t.observeBillingLeg = e.observeBillingLeg
	t.appendBillingLeg = e.appendIndependentCallLeg
	t.appendBillingLegStrict = e.appendIndependentCallLegStrict
	if e.TerminalUsageSink != nil {
		t.appendBillingCall = e.TerminalUsageSink.AppendCall
	}
	t.logBillingAppendFailure = e.logBillingUsageAppendFailure
	t.finalizeBilling = e.callFinalizeBilling
	t.finalizeBillingV2 = e.callFinalizeBillingResult
	t.releaseRequestAuthority = e.releaseRequestAuthority
	t.settleRequestAuthority = e.settleRequestAuthority
	t.emitFrontendEgress = e.emitFrontendEgressMeteringFact
	t.meteringRecorderPresent = e.MeteringRecorder != nil
	t.emitBackendEgress = e.emitBackendEgressMeteringFact
	t.recordEmptyVisibleTurn = e.recordEmptyVisibleTurn
	t.recordEmptyVisibleTurnRetry = e.recordEmptyVisibleTurnRetry
	if !t.emptyVisibleTurnRetryGateBound {
		t.emptyVisibleTurnRetryGate = e.EmptyVisibleTurnRetry.Enabled
		t.emptyVisibleTurnRetryGateBound = true
	}
	t.steeringWriterFactory = e.SteeringWriterFactory
	t.conversationReader = e.conversationViewReader()
	t.conversationObserver = e.conversationViewObserver()
	if !t.terminalDecisionAuxiliaryBound && e.RuntimeSnapshot != nil {
		t.terminalDecisionAuxiliary = e.RuntimeSnapshot.Aux()
		t.terminalDecisionAuxiliaryBound = true
	}
}

func (t *turnTerminal) nowTime() time.Time {
	if t != nil && t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t *turnTerminal) setInterleavedThinker() {
	if t != nil {
		t.interleavedThinker = true
	}
}

func (t *turnTerminal) isInterleavedThinker() bool {
	return t != nil && t.interleavedThinker
}

// toolCorrectionsAccepted reports how many corrections this client turn has
// accepted so far.
func (t *turnTerminal) toolCorrectionsAccepted() int64 {
	if t == nil {
		return 0
	}
	return t.toolNameCorrections.Load()
}

// acceptToolCorrection records one accepted correction for this client turn.
func (t *turnTerminal) acceptToolCorrection() {
	if t == nil {
		return
	}
	t.toolNameCorrections.Add(1)
}

// correctionOverlayRef names the correction overlay a turn published, so the
// response-end transition can remove it and report a failed removal with the
// same call lineage the message was planted under.
type correctionOverlayRef struct {
	aLegID  string
	traceID string
}

// publishToolNameCorrectionOverlay records that this turn persisted the hidden
// correction message under aLegID. The single receive loop sets it when the
// correction continuation publishes.
//
// Invariant: finishResponse marks the turn finished before taking the ref under
// the same mutex, so registration and cleanup are atomic: a publish that loses
// the race deactivates immediately instead of leaking.
func (t *turnTerminal) publishToolNameCorrectionOverlay(aLegID, traceID string) {
	if t == nil || aLegID == "" {
		return
	}
	t.toolNameCorrectionMu.Lock()
	if t.finished() {
		t.toolNameCorrectionMu.Unlock()
		if err := deactivateSteeringOverlay(context.Background(), t, aLegID, toolNameCorrectionOverlayID); err != nil {
			diag.LogError(diag.EnsureCallDiag(context.Background(), traceID, aLegID), t.log, "tool_name_correction_overlay_deactivate_failed", diag.AttrOpts{}, err)
		}
		return
	}
	t.toolNameCorrectionOverlay = correctionOverlayRef{aLegID: aLegID, traceID: traceID}
	t.toolNameCorrectionMu.Unlock()
}

// takeToolNameCorrectionOverlay returns the published correction overlay and
// clears it, so the response-end transition deactivates the overlay exactly
// once.
func (t *turnTerminal) takeToolNameCorrectionOverlay() correctionOverlayRef {
	if t == nil {
		return correctionOverlayRef{}
	}
	t.toolNameCorrectionMu.Lock()
	defer t.toolNameCorrectionMu.Unlock()
	ref := t.toolNameCorrectionOverlay
	t.toolNameCorrectionOverlay = correctionOverlayRef{}
	return ref
}

// deactivateToolNameCorrectionOverlay removes this turn's hidden correction
// message once the client response ends, so no later turn projects it
// (requirement 2.5). Deactivation is best-effort on a bounded cleanup context;
// a failure is logged and not retried.
func (t *turnTerminal) deactivateToolNameCorrectionOverlay(ctx context.Context) {
	ref := t.takeToolNameCorrectionOverlay()
	if ref.aLegID == "" {
		return
	}
	if err := deactivateSteeringOverlay(ctx, t, ref.aLegID, toolNameCorrectionOverlayID); err != nil {
		diag.LogError(diag.EnsureCallDiag(ctx, ref.traceID, ref.aLegID), t.log, "tool_name_correction_overlay_deactivate_failed", diag.AttrOpts{}, err)
	}
}

func newTurnTerminal() *turnTerminal {
	return newTurnTerminalWithALeg(nil, aLegEndBase)
}

func newTurnTerminalWithALeg(aLeg *leglifecycle.ALeg, endMode aLegEndMode) *turnTerminal {
	return &turnTerminal{
		request:          newStreamTerminal(sdkterminal.ScopeRequest),
		aLegEndAuthority: &aLegEndAuthority{scope: aLeg, mode: endMode},
	}
}

// newTurnTerminalWithSharedALeg gives a continuation its own request terminal
// while preserving one shared A-leg lifecycle/end authority for the complete
// thinker/executor turn.
func newTurnTerminalWithSharedALeg(parent *turnTerminal) *turnTerminal {
	terminal := &turnTerminal{request: newStreamTerminal(sdkterminal.ScopeRequest)}
	if parent != nil {
		terminal.aLegEndAuthority = parent.aLegEndAuthority
		terminal.supportsContinuation = parent.supportsContinuation
		terminal.steeringWriterFactory = parent.steeringWriterFactory
		terminal.conversationReader = parent.conversationReader
		terminal.conversationObserver = parent.conversationObserver
		terminal.terminalDecisionProvider = parent.terminalDecisionProvider
		terminal.terminalDecisionProviderID = parent.terminalDecisionProviderID
		terminal.terminalDecisionProviderHasID = parent.terminalDecisionProviderHasID
		terminal.terminalDecisionAuxiliary = parent.terminalDecisionAuxiliary
		terminal.terminalDecisionAuxiliaryBound = parent.terminalDecisionAuxiliaryBound
		terminal.continuationTransaction = parent.continuationTransaction
	}
	return terminal
}

// deferALegEndToOuter is a construction-time, one-way ownership handoff used
// by the interleaved wrapper when a stream was assembled with base ownership
// before the wrapper decision was applied. It cannot move ownership back to
// the base stream and must run before any terminal operation is exposed.
func (t *turnTerminal) deferALegEndToOuter() bool {
	if t == nil {
		return false
	}
	if t.aLegEndAuthority == nil {
		return false
	}
	if t.aLegEndAuthority.mode == aLegEndOuter {
		return true
	}
	if t.aLegEndAuthority.mode != aLegEndBase {
		return false
	}
	t.aLegEndAuthority.mode = aLegEndOuter
	return true
}

// aLegScope is a transitional construction seam for upstream open helpers.
// All lifecycle mutations and A-leg end ownership remain behind turnTerminal.
func (t *turnTerminal) aLegScope() *leglifecycle.ALeg {
	if t == nil || t.aLegEndAuthority == nil {
		return nil
	}
	return t.aLegEndAuthority.scope
}

func (t *turnTerminal) hasALeg() bool {
	return t != nil && t.aLegEndAuthority != nil && t.aLegEndAuthority.scope != nil
}

func (t *turnTerminal) aLegErr() error {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil {
		return nil
	}
	return t.aLegEndAuthority.scope.Err()
}

func (t *turnTerminal) registerBLeg(ctx context.Context, h leglifecycle.BLegHandle) error {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil {
		return nil
	}
	return t.aLegEndAuthority.scope.RegisterBLeg(ctx, h)
}

func (t *turnTerminal) cancelALeg(ctx context.Context, cause aipapi.CancelCause) error {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil {
		return nil
	}
	return t.aLegEndAuthority.scope.Cancel(ctx, cause)
}

// endALeg is the sole A-leg end authority. It returns true only to the caller
// that performed the once-only end. A base caller cannot end an outer-owned
// interleaved A-leg (and vice versa).
func (t *turnTerminal) endALeg(mode aLegEndMode) bool {
	if t == nil || t.aLegEndAuthority == nil || t.aLegEndAuthority.scope == nil || t.aLegEndAuthority.mode != mode {
		return false
	}
	ended := false
	t.aLegEndAuthority.once.Do(func() {
		t.aLegEndAuthority.scope.End()
		ended = true
	})
	return ended
}

// requestTerminal returns the request-scope stream terminal for narrow runtime
// integration assertions. Attempt ownership is intentionally not exposed here.
func (t *turnTerminal) requestTerminal() *streamTerminal {
	if t == nil {
		return nil
	}
	return t.request
}

// Committed reports the sole request-lifetime output-commit truth.
func (t *turnTerminal) committed() bool {
	return t != nil && t.commitment.Load()
}

// MarkCommitted publishes request-level commitment and sends a one-way
// notification to the explicitly snapshotted attempt authority. The attempt
// notification is intentionally not used as request truth and is repeated for
// idempotent marks so a newly current attempt can observe the commitment.
func (t *turnTerminal) markCommitted(attempt *attemptSession) {
	if t == nil {
		return
	}
	t.commitment.Store(true)
	if attempt != nil {
		attempt.authority.markOutputCommitted()
	}
}

// Finished reports whether request terminal completion has been published.
func (t *turnTerminal) finished() bool {
	return t != nil && t.completion.Load()
}

// MarkFinished publishes request completion exactly once and reports whether
// this caller performed that publication.
func (t *turnTerminal) markFinished() bool {
	return t != nil && t.completion.CompareAndSwap(false, true)
}

// noteReleasedClientFrame latches the turn-scoped released-frame fact for a
// canonical frame the receive stream actually returned to the client
// (requirement 2.3). A zero event and the synthetic keepalive are not client
// frames; staged, rejected, or failed-preflight events never reach this call,
// because only a successful Recv return is a release.
func (t *turnTerminal) noteReleasedClientFrame(ev aipapi.Event) {
	if t == nil || ev.Kind == "" || isKeepaliveEvent(ev) {
		return
	}
	t.releasedClientFrame.Store(true)
}

// releasedClientFrameLatched reports whether any canonical client-visible
// frame of this logical turn already reached the client.
func (t *turnTerminal) releasedClientFrameLatched() bool {
	return t != nil && t.releasedClientFrame.Load()
}

// claimCorrectiveRetry claims this turn's single corrective allowance and
// reports whether this caller won it. It is a one-shot compare-and-swap, so
// every later claim - across a replacement attempt or a continuation - loses
// and the allowance is never refunded (requirement 4.1).
func (t *turnTerminal) claimCorrectiveRetry() bool {
	return t != nil && t.correctiveRetryClaimed.CompareAndSwap(false, true)
}

// correctiveRetrySpent reports whether this turn's single corrective allowance
// was already claimed. The allowance is one-shot and never refunded
// (requirement 4.1), so a spent allowance declines every later candidate.
func (t *turnTerminal) correctiveRetrySpent() bool {
	return t != nil && t.correctiveRetryClaimed.Load()
}

// markCorrectionPending flags the accepted corrective replacement as in flight,
// so the receive loop's replacement block runs it in its bounded single-open
// mode (requirements 4.2, 4.6).
func (t *turnTerminal) markCorrectionPending() {
	if t != nil {
		t.correctionPendingState.Store(true)
	}
}

// consumeCorrectionPending atomically consumes the accepted correction's
// one-shot pending marker and reports whether this caller won it. Exactly one
// caller can win, so the candidate's single decision outcome is recorded at
// most once across publication, the bounded open's failure, and the withdrawal
// early exits.
func (t *turnTerminal) consumeCorrectionPending() bool {
	return t != nil && t.correctionPendingState.CompareAndSwap(true, false)
}

// correctionPending reports whether an accepted corrective replacement is still
// in flight.
func (t *turnTerminal) correctionPending() bool {
	return t != nil && t.correctionPendingState.Load()
}

// emptyVisibleTurnRetryEnabled reports the turn-pinned generation gate.
func (t *turnTerminal) emptyVisibleTurnRetryEnabled() bool {
	return t != nil && t.emptyVisibleTurnRetryGate
}

func (t *turnTerminal) terminalDecisionProviderIdentity() (string, bool) {
	if t == nil {
		return "", false
	}
	t.terminalDecisionMu.Lock()
	defer t.terminalDecisionMu.Unlock()
	return t.terminalDecisionProviderID, t.terminalDecisionProviderHasID
}

// finishResponse applies the request-terminal finished transition and clears
// attempt-local response assembly state at the same ownership boundary. The
// client response ends here, so the hidden correction message a published
// correction planted is removed at the same transition, exactly once, whatever
// terminal route reached it.
func (t *turnTerminal) finishResponse(response *responsePipeline, attempt *attemptSession) bool {
	if t == nil || !t.markFinished() {
		return false
	}
	clearAttemptToolState(response, attempt)
	t.deactivateToolNameCorrectionOverlay(context.Background())
	return true
}

// noteAcceptedLogicalCompletion latches this turn's accepted logical
// completion. It runs only at the successful receive-release boundary, after
// the interleaved-thinker bypass, so a B-leg-only completion never latches
// the logical turn outcome. It stores no attempt and performs no I/O.
func (t *turnTerminal) noteAcceptedLogicalCompletion() {
	if t == nil || t.isInterleavedThinker() {
		return
	}
	t.acceptedCompletion.Store(true)
}

// acceptedLogicalCompletionObserved reports whether the accepted logical
// completion was latched at the receive-release boundary.
func (t *turnTerminal) acceptedLogicalCompletionObserved() bool {
	return t != nil && t.acceptedCompletion.Load()
}

// wireLogicalOutcome is the scalar read-only outcome evidence the wire audit
// consumes. It carries the request terminal's claimed outcome code and state
// plus the latched accepted receive-release completion. It inspects no raw
// event kinds and retains no attempt: every field is a value copied under the
// existing terminal lock at observation time.
type wireLogicalOutcome struct {
	claimed  bool
	code     sdkterminal.OutcomeCode
	state    sdkterminal.State
	accepted bool
}

// wireLogicalOutcome snapshots this terminal's logical outcome evidence. The
// snapshot is read-only and scalar; a later Close, Cancel, withdrawal, or
// replacement cannot mutate it, and observing it pins no attempt. It lives on
// the terminal owner rather than the EventStream facade, which the Task 5.2
// facade gate keeps limited to Recv and Close receivers.
func (t *turnTerminal) wireLogicalOutcome() wireLogicalOutcome {
	var evidence wireLogicalOutcome
	if t == nil {
		return evidence
	}
	req := t.requestTerminal()
	if req == nil {
		return evidence
	}
	owner := req.Owner()
	if owner == nil {
		return evidence
	}
	evidence.state = owner.State()
	if outcome, ok := owner.Outcome(); ok {
		evidence.claimed = true
		evidence.code = outcome.Code
	}
	evidence.accepted = t.acceptedLogicalCompletionObserved()
	return evidence
}

// logicallySuccessful reports whether the snapshot proves an accepted logical
// success. Success requires all of: the request terminal claimed the success
// code, its effects ran to release, the accepted receive-release completion
// was latched at the delivery boundary, and (checked by the caller, which
// owns the receive-return evidence) no receive-return failure. A merely
// finished terminal, a claimed-but-unreleased command, EOF alone, an attempt
// winner, or a withdrawn publication is insufficient.
func (o wireLogicalOutcome) logicallySuccessful() bool {
	return o.claimed &&
		o.code == sdkterminal.OutcomeCodeSuccess &&
		o.state == sdkterminal.StateReleased &&
		o.accepted
}

// finishResponseAtBoundary completes a response event at the receive-phase
// ownership boundary. An interleaved thinker has only completed its B-leg;
// the outer stream still owns the shared request terminal until the executor
// phase (or an authoritative abort) completes it.
func (t *turnTerminal) finishResponseAtBoundary(response *responsePipeline, attempt *attemptSession, endALeg bool) bool {
	if t == nil {
		return false
	}
	if t.isInterleavedThinker() {
		clearAttemptToolState(response, attempt)
		return true
	}
	finished := t.finishResponse(response, attempt)
	if finished {
		t.noteAcceptedLogicalCompletion()
	}
	if endALeg {
		t.endALeg(aLegEndBase)
	}
	return finished
}

func (t *turnTerminal) settleOrReleaseRequestAuthority(ctx context.Context, p *responsePipeline, request requestTerminalFacts) {
	if t.committed() {
		if p == nil {
			return
		}
		_ = t.settleRequestAuthorityWithFrontendEgress(ctx, p.usageEvidenceOrEmpty(), request, p)
	} else if t.releaseRequestAuthority != nil {
		_ = t.releaseRequestAuthority(ctx)
	}
}

func (t *turnTerminal) terminalizeTurn(ctx context.Context, cmd sdkterminal.Command, intent attemptTerminalIntent, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, evidence attemptEvidence, snapshot coreterm.AccumulatorSnapshot) bool {
	input := t.terminalDecisionInput(cmd, request, attempt, p, snapshot)
	_, published := t.terminalizeTurnDecisionWithPublication(ctx, t.terminalDecisionProvider, input, cmd, intent, request, attempt, p, evidence, snapshot)
	return published
}

// terminalizeTurnWithDecision is the production terminal path used by the
// focused contract tests and by future generation wiring. Continue remains
// provisional until the installed core transaction publishes a B-leg.
func (t *turnTerminal) terminalizeTurnWithDecision(ctx context.Context, provider terminaldecision.Provider, input terminaldecision.Input, cmd sdkterminal.Command, attempt *attemptSession) coreterm.Result {
	if t == nil || t.request == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	if provider != nil {
		providerID := provider.ID()
		t.terminalDecisionMu.Lock()
		if !t.terminalDecisionProviderHasID {
			t.terminalDecisionProviderID = providerID
			t.terminalDecisionProviderHasID = true
		}
		t.terminalDecisionMu.Unlock()
	}
	snapshot := coreterm.NewAccumulatorSnapshot(nil, input.Candidate.OutputCommitted)
	evidence := attemptEvidence{
		Command:      cmd,
		LegOutcome:   decisionLegOutcome(cmd),
		Snapshot:     &snapshot,
		Committed:    input.Candidate.OutputCommitted,
		RecordReason: string(input.Candidate.Cause),
		TraceID:      input.Request.TraceID,
		ALegID:       input.Request.ALegID,
	}
	result, _ := t.terminalizeTurnDecisionWithPublication(ctx, provider, input, cmd, decisionIntent(cmd), requestTerminalFacts{}, attempt, nil, evidence, snapshot)
	return result
}

// terminalizeTurnDecision is the single bridge between provisional candidate
// evaluation and the existing request/attempt owners. No owner is claimed
// until evaluation has returned a final decision.
func (t *turnTerminal) terminalizeTurnDecision(ctx context.Context, provider terminaldecision.Provider, input terminaldecision.Input, cmd sdkterminal.Command, intent attemptTerminalIntent, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, evidence attemptEvidence, snapshot coreterm.AccumulatorSnapshot) coreterm.Result {
	result, _ := t.terminalizeTurnDecisionWithPublication(ctx, provider, input, cmd, intent, request, attempt, p, evidence, snapshot)
	return result
}

func (t *turnTerminal) terminalizeTurnDecisionWithPublication(ctx context.Context, provider terminaldecision.Provider, input terminaldecision.Input, cmd sdkterminal.Command, intent attemptTerminalIntent, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, evidence attemptEvidence, snapshot coreterm.AccumulatorSnapshot) (coreterm.Result, bool) {
	decision := t.sharedTerminalDecision(ctx, provider, input)
	return t.terminalizeTurnAfterDecision(ctx, decision, cmd, intent, request, attempt, p, evidence, snapshot)
}

func (t *turnTerminal) terminalizeTurnAfterDecision(ctx context.Context, decision terminalDecisionOutcome, cmd sdkterminal.Command, intent attemptTerminalIntent, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, evidence attemptEvidence, snapshot coreterm.AccumulatorSnapshot) (coreterm.Result, bool) {
	if decision.Decision.Kind == terminaldecision.DecisionContinue {
		if decision.Decision.Continue == nil || t.continuationTransaction == nil {
			return coreterm.Result{State: t.requestTerminal().Owner().State()}, false
		}
		published, err := t.continuationTransaction(ctx, *decision.Decision.Continue)
		if published {
			return coreterm.Result{State: t.requestTerminal().Owner().State()}, true
		}
		if err != nil && t.finished() {
			// The existing transaction owns pre-publication failure
			// finalization. Do not claim or settle the request a second time.
			return coreterm.Result{State: t.requestTerminal().Owner().State(), Err: err}, false
		}
		if err != nil {
			decision.Decision = allowStopDecision("continuation_failed")
			evidence.Err = err
		} else {
			return coreterm.Result{State: t.requestTerminal().Owner().State()}, false
		}
	}
	if decision.Decision.Kind == terminaldecision.DecisionSurfaceFailure {
		if cmd != sdkterminal.CommandCancel && cmd != sdkterminal.CommandClose && cmd != sdkterminal.CommandTimeout {
			cmd = sdkterminal.CommandPartialError
		}
		intent = IntentSurfacedFailure
		evidence.Command = cmd
		evidence.LegOutcome = usageevidence.LegOutcomeFailed
	}
	if decision.OutputCommitted {
		snapshot = coreterm.NewAccumulatorSnapshot(snapshot.Bytes(), true)
		evidence.Committed = true
	}

	deactErr := deactivateContinuationOverlay(ctx, t, request.aLegID)
	if deactErr != nil {
		if evidence.Err == nil {
			evidence.Err = deactErr
		}
		if cmd == sdkterminal.CommandNormalFinish {
			cmd = sdkterminal.CommandPartialError
		}
	}
	var attemptErr error
	if attempt != nil {
		attemptResult := attempt.TerminalizeAttempt(ctx, intent, evidence)
		attemptErr = attemptResult.Result.Err
	}
	result := t.claimRequestTerminal(ctx, cmd, snapshot, func(cctx context.Context, _ coreterm.Outcome) error {
		t.settleOrReleaseRequestAuthority(cctx, p, request)
		handoffErr := t.handoffBillingTurn(cctx, request, cmd)
		t.finishResponse(p, attempt)
		return errors.Join(deactErr, attemptErr, handoffErr)
	})
	if !t.finished() {
		t.finishResponse(p, attempt)
	}
	return result, false
}

func (t *turnTerminal) terminalDecisionInput(cmd sdkterminal.Command, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, snapshot coreterm.AccumulatorSnapshot) terminaldecision.Input {
	var bLegID string
	if attempt != nil {
		bLegID = attempt.bleg.BLegID
	}
	reference := strings.TrimSpace(bLegID)
	if reference == "" {
		reference = strings.TrimSpace(request.traceID)
	}
	if reference == "" {
		reference = string(cmd)
	}
	requestID := strings.TrimSpace(request.call.ID)
	if requestID == "" {
		requestID = strings.TrimSpace(request.traceID)
	}
	if requestID == "" {
		requestID = reference
	}
	policy := request.terminalDecisionPolicy
	if strings.TrimSpace(policy.Revision) == "" {
		policy.Revision = "0"
	}
	evidence := projectTerminalDecisionEvidence(request, attempt, p)
	return terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:           decisionCandidateCause(cmd),
			Reference:       reference,
			OutputCommitted: snapshot.OutputCommitted(),
		},
		Request: terminaldecision.RequestIdentity{
			RequestID: requestID,
			TraceID:   request.traceID,
			ALegID:    request.aLegID,
			BLegID:    bLegID,
		},
		Policy: policy,
		Continuation: terminaldecision.ContinuationEvidence{
			TrajectoryRef: evidence.Lineage.TrajectoryRef,
			Attempt:       evidence.Lineage.Attempt,
		},
		Evidence:  evidence,
		Auxiliary: t.terminalDecisionAuxiliary,
		Deadline:  time.Now().Add(terminalDecisionEvaluationBudget),
	}
}

func decisionCandidateCause(cmd sdkterminal.Command) terminaldecision.CandidateCause {
	switch cmd {
	case sdkterminal.CommandCancel, sdkterminal.CommandClose, sdkterminal.CommandTimeout:
		return terminaldecision.CandidateCauseCancellation
	case sdkterminal.CommandGateReplacement:
		return terminaldecision.CandidateCauseAuthorityDenied
	case sdkterminal.CommandNormalFinish:
		return terminaldecision.CandidateCauseNormal
	case sdkterminal.CommandEOF:
		return terminaldecision.CandidateCauseTransport
	default:
		return terminaldecision.CandidateCauseProviderError
	}
}

func decisionIntent(cmd sdkterminal.Command) attemptTerminalIntent {
	switch cmd {
	case sdkterminal.CommandCancel, sdkterminal.CommandClose:
		return IntentCancellation
	case sdkterminal.CommandTimeout:
		return IntentTimeout
	case sdkterminal.CommandNormalFinish:
		return IntentSuccess
	default:
		return IntentSurfacedFailure
	}
}

func decisionLegOutcome(cmd sdkterminal.Command) usageevidence.LegOutcome {
	switch cmd {
	case sdkterminal.CommandCancel, sdkterminal.CommandClose, sdkterminal.CommandTimeout:
		return usageevidence.LegOutcomeCanceled
	default:
		return usageevidence.LegOutcomeFailed
	}
}

func (t *turnTerminal) makeBaseEvidence(request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, snapshot *coreterm.AccumulatorSnapshot) attemptEvidence {
	var started time.Time
	if attempt != nil {
		started = attempt.accountingStartedAt()
	}
	return attemptEvidence{
		Usage:          p.operatorUsageForFinalize(),
		TraceID:        request.traceID,
		ALegID:         request.aLegID,
		Snapshot:       snapshot,
		StartedAt:      started,
		StreamFallback: p.billingEvidenceFallback(),
		BillingState:   request.billingState,
		BillingCallID:  request.billingCallID,
		Committed:      t.committed(),
	}
}

func (t *turnTerminal) terminalizeWithEvidence(
	ctx context.Context,
	request requestTerminalFacts,
	attempt *attemptSession,
	p *responsePipeline,
	cmd sdkterminal.Command,
	intent attemptTerminalIntent,
	legOutcome usageevidence.LegOutcome,
	obsOutcome response.StreamOutcome,
	recOutcome aipapi.AttemptOutcome,
	reason string,
	cause error,
	prep func(context.Context) (aipapi.Event, aipapi.Event, bool, error),
) bool {
	if t == nil || p == nil {
		return false
	}
	snapshot := p.accumulatorSnapshot()
	ev := t.makeBaseEvidence(request, attempt, p, &snapshot)
	ev.Command = cmd
	ev.LegOutcome = legOutcome
	ev.ObsOutcome = obsOutcome
	ev.RecordOutcome = recOutcome
	ev.RecordReason = reason
	ev.Err = cause
	ev.AuthorityPrepare = prep
	switch cmd {
	case sdkterminal.CommandCancel:
		ev.BillingReason = reason
		ev.CancelCause = &aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: reason}
	case sdkterminal.CommandTimeout:
		ev.CancelCause = &aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "timeout"}
	}
	return t.terminalizeTurn(ctx, cmd, intent, request, attempt, p, ev, snapshot)
}

func (t *turnTerminal) closeClose(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) {
	if t == nil || p == nil || t.finished() {
		return
	}
	snapshot := p.accumulatorSnapshot()
	ev := t.makeBaseEvidence(request, attempt, p, &snapshot)
	ev.Command = sdkterminal.CommandClose
	ev.LegOutcome = usageevidence.LegOutcomeCanceled
	ev.ObsOutcome = response.OutcomeClosed
	ev.RecordOutcome = aipapi.AttemptCancelled
	ev.RecordReason = "client closed"
	ev.BillingReason = "client closed"
	ev.CancelCause = &aipapi.CancelCause{Kind: aipapi.CancelClientGone, Detail: "client closed"}
	decision := t.sharedTerminalDecision(ctx, t.terminalDecisionProvider, t.terminalDecisionInput(sdkterminal.CommandClose, request, attempt, p, snapshot))
	if decision.Decision.Kind == terminaldecision.DecisionContinue {
		return
	}
	if attempt != nil {
		attempt.TerminalizeAttempt(ctx, IntentCancellation, ev)
	}
	if t.hasALeg() {
		_ = t.cancelALeg(ctx, aipapi.CancelCause{Kind: aipapi.CancelClientGone})
	}
	t.terminalizeTurnAfterDecision(ctx, decision, sdkterminal.CommandClose, IntentCancellation, request, nil, p, ev, snapshot)
}

func (t *turnTerminal) isALegCanceled(err error) bool {
	return errors.Is(err, leglifecycle.ErrALegCanceled)
}

// terminalizePartialFailure owns the terminal side of a response/encoder
// failure. Event transformation stays on responsePipeline; this method only
// applies the irreversible terminal and billing consequences.
func (t *turnTerminal) terminalizePartialFailure(ctx context.Context, p *responsePipeline, request requestTerminalFacts, attempt *attemptSession, cmd sdkterminal.Command, reason string, cause error) bool {
	if attempt == nil {
		return false
	}
	return t.terminalizeWithEvidence(ctx, request, attempt, p, cmd, IntentSurfacedFailure, usageevidence.LegOutcomeFailed, response.OutcomeFailed, aipapi.AttemptSurfacedFailure, reason, cause, nil)
}

func (t *turnTerminal) partialFailure(ctx context.Context, p *responsePipeline, request requestTerminalFacts, attempt *attemptSession, encoderFailure bool, cause error) bool {
	cmd := sdkterminal.CommandPartialError
	if encoderFailure {
		cmd = sdkterminal.CommandFrontendEncoderFailure
	}
	return t.terminalizePartialFailure(ctx, p, request, attempt, cmd, attemptReasonDetail(cause), cause)
}

func (t *turnTerminal) terminalizeEOF(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) bool {
	if attempt == nil {
		return false
	}
	return t.terminalizeWithEvidence(ctx, request, attempt, p, sdkterminal.CommandEOF, IntentSurfacedFailure, usageevidence.LegOutcomeFailed, response.OutcomeFailed, aipapi.AttemptSurfacedFailure, "stream ended without response_finished", io.EOF, nil)
}

func (t *turnTerminal) terminalizeTimeout(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) bool {
	if attempt == nil {
		return false
	}
	return t.terminalizeWithEvidence(ctx, request, attempt, p, sdkterminal.CommandTimeout, IntentTimeout, usageevidence.LegOutcomeCanceled, response.OutcomeFailed, aipapi.AttemptCancelled, "timeout", context.DeadlineExceeded, nil)
}

func (t *turnTerminal) terminalizeCancellation(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, reason string, timeout bool) bool {
	if attempt == nil {
		return false
	}
	cmd := sdkterminal.CommandCancel
	intent := IntentCancellation
	if timeout {
		cmd = sdkterminal.CommandTimeout
		intent = IntentTimeout
	}
	return t.terminalizeWithEvidence(ctx, request, attempt, p, cmd, intent, usageevidence.LegOutcomeCanceled, response.OutcomeCancelled, aipapi.AttemptCancelled, reason, nil, func(cctx context.Context) (aipapi.Event, aipapi.Event, bool, error) {
		if t.finalizeBillingAfterCancel(cctx, attempt, reason, request, p) {
			t.reconcileOrSettleCancellationAuthorityForAttempt(cctx, attempt, p)
		} else {
			t.settleCancellationAuthorityForAttempt(cctx, attempt, p)
		}
		usageEv := p.operatorUsageForFinalize()
		return usageEv, usageEv, true, nil
	})
}

func (t *turnTerminal) terminalizeSurfacedFailure(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, surfErr error, panicFailure bool) bool {
	cmd := sdkterminal.CommandPartialError
	if panicFailure {
		cmd = sdkterminal.CommandPanic
	}
	return t.terminalizePartialFailure(ctx, p, request, attempt, cmd, attemptReasonDetail(surfErr), surfErr)
}

func (t *turnTerminal) terminalizeReplacementFailure(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline, cause error) bool {
	return t.terminalizePartialFailure(ctx, p, request, attempt, sdkterminal.CommandPartialError, "replacement failure", cause)
}

// terminalizeGateReplacement owns the post-output mandatory-recording stop.
// Recv only sequences this terminal claim before asking recovery to open again.
func (t *turnTerminal) terminalizeGateReplacement(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) error {
	if t == nil || attempt == nil || p == nil {
		return nil
	}
	gateErr := &aipapi.UpstreamFailureError{
		Phase:        aipapi.PhasePostOutput,
		Recoverable:  false,
		Reason:       "secure session mandatory recorder failure after committed output",
		CandidateKey: strings.TrimSpace(attempt.cand.Key),
	}
	snapshot := p.accumulatorSnapshot()
	ev := t.makeBaseEvidence(request, attempt, p, &snapshot)
	ev.Command = sdkterminal.CommandGateReplacement
	ev.LegOutcome = usageevidence.LegOutcomeFailed
	ev.ObsOutcome = response.OutcomeFailed
	ev.RecordOutcome = aipapi.AttemptSurfacedFailure
	ev.RecordReason = gateErr.Reason
	ev.Err = gateErr
	decision := t.sharedTerminalDecision(ctx, t.terminalDecisionProvider, t.terminalDecisionInput(sdkterminal.CommandGateReplacement, request, attempt, p, snapshot))
	if decision.Decision.Kind == terminaldecision.DecisionContinue {
		return nil
	}
	attemptResult := attempt.TerminalizeAttempt(ctx, IntentSurfacedFailure, ev)
	requestResult := t.claimRequestTerminal(ctx, sdkterminal.CommandGateReplacement, snapshot, func(cctx context.Context, _ coreterm.Outcome) error {
		return t.handoffBillingTurn(cctx, request, sdkterminal.CommandGateReplacement)
	})
	return errors.Join(gateErr, attemptResult.Result.Err, requestResult.Err)
}

func (t *turnTerminal) registerReplacement(ctx context.Context, out replacementOpenResult, next *readyAttempt) error {
	if t == nil || next == nil || out.registered {
		return nil
	}
	if err := t.registerBLeg(ctx, leglifecycle.BLegHandle{ID: out.bleg.BLegID, Attempt: next.lifecycleHandle()}); err != nil {
		next.Dispose(ctx, err)
		return err
	}
	return nil
}

func (t *turnTerminal) cleanupUnpublishedReplacement(ctx context.Context, next *readyAttempt) {
	if next == nil {
		return
	}
	next.Dispose(context.Background(), fmt.Errorf("turn terminal: %w", errPublicationClosed))
}

// emitSynthesizedUsage is the terminal-owned handoff for usage reconstructed
// at response completion. Response observation remains delegated to the named
// pipeline; this owner applies the resulting output/commit consequences.
func (t *turnTerminal) emitSynthesizedUsage(ctx context.Context, ev aipapi.Event, request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) (aipapi.Event, error) {
	if t == nil || p == nil || attempt == nil {
		return aipapi.Event{}, nil
	}
	attempt.observeAccountingClientEvent(p.nowTime(), ev)
	pm := sdk.PartMeta{TraceID: request.traceID, ALegID: request.aLegID, BLegID: attempt.bleg.BLegID, AttemptSeq: attempt.bleg.Seq, BackendID: strings.TrimSpace(attempt.cand.Primary.Backend)}
	out, recording, err := p.observeSynthesizedUsage(ctx, ev, request, attempt, pm, t.committed())
	if err != nil {
		cmd := sdkterminal.CommandPartialError
		if recording.mandatory() {
			cmd = sdkterminal.CommandFrontendEncoderFailure
		}
		t.terminalizePartialFailure(ctx, p, request, attempt, cmd, attemptReasonDetail(err), err)
		return aipapi.Event{}, err
	}
	if aipapi.OutputCommitted(out) {
		t.markOutputCommittedForAttempt(out, attempt, nil)
	}
	p.emitUsageTerminal(ctx, request.responseEvidence(), request.call.Session.CorrelationID(), attempt, out)
	return out, nil
}

func (t *turnTerminal) markOutputCommittedForAttempt(ev aipapi.Event, attempt *attemptSession, recovery *recoveryController) {
	if t == nil || !aipapi.OutputCommitted(ev) {
		return
	}
	t.markCommitted(attempt)
	if recovery != nil && recovery.ttft != nil {
		recovery.ttft.markCommitted()
	}
}

func (t *turnTerminal) accountingFinalized() bool {
	return t != nil && t.accountingFinalizedState.Load()
}

func (t *turnTerminal) claimAccountingFinalization() bool {
	return t != nil && t.accountingFinalizedState.CompareAndSwap(false, true)
}

func (t *turnTerminal) unclaimAccountingFinalization() {
	if t != nil {
		t.accountingFinalizedState.Store(false)
	}
}

// terminalizeRequest coordinates logical request/A-leg terminal and request
// billing closure only. It MUST NOT accept attempt or attemptEffects; all
// attempt-owned effects go through attemptSession.TerminalizeAttempt.
func (t *turnTerminal) terminalizeRequest(
	ctx context.Context,
	cmd sdkterminal.Command,
	snapshot coreterm.AccumulatorSnapshot,
	requestEffects func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	return t.claimRequestTerminal(ctx, cmd, snapshot, requestEffects)
}

func (t *turnTerminal) claimRequestTerminal(
	ctx context.Context,
	cmd sdkterminal.Command,
	snapshot coreterm.AccumulatorSnapshot,
	requestEffects func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	if t == nil || t.request == nil {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	if !cmd.AllowsScope(sdkterminal.ScopeRequest) {
		return coreterm.Result{Err: sdkterminal.ErrInvalid}
	}
	if t.committed() && !snapshot.OutputCommitted() {
		snapshot = coreterm.NewAccumulatorSnapshot(snapshot.Bytes(), true)
	}
	r := t.request.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		return snapshot.Clone()
	}, func(cctx context.Context, out coreterm.Outcome) error {
		if requestEffects != nil {
			return requestEffects(cctx, out)
		}
		return nil
	})
	if !r.Won && cmd == sdkterminal.CommandGateReplacement && errors.Is(r.Err, sdkterminal.ErrOutputCommitted) && requestEffects != nil {
		_ = requestEffects(ctx, r.Outcome)
	}
	return r
}
