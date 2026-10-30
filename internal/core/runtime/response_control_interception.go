// Private, attempt-local control-call interception for
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Response
// Interception / Placement, Handler Semantics, and Concurrency and Lifecycle;
// requirements 3.5, 4.7, 5.1-5.7, 8.2-8.7, 10.3, 11.1-11.3, 12.3-12.5).
//
// This file owns the interception seam only. It runs on the backend-event
// boundary after BTP and provider usage observation and before the ordinary
// tool-call assembler, so a proxy-owned control call never reaches ordinary
// tool-call finalizers, tool policy, tool reactors, response hooks, the client
// recorder, or client release, while operator BTP capture and provider
// accounting keep seeing the legitimate upstream control traffic. Ordinary events
// are returned unchanged, undelayed, and unbuffered.
//
// Ownership comes exclusively from the trusted activation the request path
// already froze. Nothing here re-reads a runtime snapshot, a provider ID() or
// Spec(), capability eligibility, request Extensions, or a mutable context value
// to decide response ownership.
//
// The capture and its normalized outcome are attempt-local mutable state, owned
// by the attempt session under one short-held control lock (attemptSession.controlMu,
// documented at its declaration). The single backend Recv loop observes and hands
// off; attempt lifecycle cleanup — cancellation, Close, attempt loss, and
// replacement — disposes that same state from whichever goroutine owns the
// transition. The lock therefore covers capture observation, handoff bookkeeping,
// outcome storage, and disposal, and nothing else: no Provider.Handle call, no
// backend Recv/Cancel/Close, and no terminal effect ever runs while it is held, so
// cleanup can never wait on an in-flight handler.
//
// Disposal is one-way. A released attempt keeps its immutable activation and a
// visibly released capture, so a late event fails closed instead of reopening the
// capture or falling through to ordinary client tool execution. This file adds no
// timer, no goroutine, no global map, and no public extension metadata.
package runtime

import (
	"context"
	"errors"
	"log/slog"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// errControlHandlerUnavailable is the bounded failure for an attempt whose
// trusted activation cannot address a handler. It is a wiring defect rather than
// a protocol state: a live capture exists only for an active activation.
var errControlHandlerUnavailable = errors.New("runtime: control tool handler unavailable for this attempt")

// errControlAttemptReleased is the bounded failure for a control event, or a
// provider result, that reaches an attempt whose private control state was
// already released by cancellation, Close, attempt loss, or replacement. The
// attempt no longer owns a classifiable protocol, so the response fails closed
// rather than reopening the capture or executing the call as ordinary client
// tool traffic.
var errControlAttemptReleased = errors.New("runtime: control tool state released for this attempt")

// Bounded, content-free control-call outcomes. Every value is a fixed
// classification well inside the telemetry bound and never carries result text,
// tool arguments, prompts, raw identifiers, or provider payload.
//
// This is the existing generic control-tool observability vocabulary. It is
// deliberately generic: nothing here names a concrete feature, tool, or provider.
const (
	// controlOutcomeObserved reports that the private path claimed an event that
	// carries no verdict of its own. It is the default classification of a claimed
	// event, so it is recorded once per such claimed event.
	controlOutcomeObserved = "observed"
	// controlOutcomeValid reports a validated completion outcome.
	controlOutcomeValid = "valid"
	// controlOutcomeInvalid reports a bounded invalid outcome, either from the
	// capture lifecycle or from the provider's own invalid classification.
	controlOutcomeInvalid = "invalid"
	// controlOutcomeArgsTooLarge reports arguments beyond the frozen budget.
	controlOutcomeArgsTooLarge = "args_too_large"
	// controlOutcomeMultipleCalls reports more than one control call in one
	// response, which this protocol never admits.
	controlOutcomeMultipleCalls = "multiple_calls"
	// controlOutcomeHandlerError reports a provider-side handler failure, which
	// the generic boundary already collapsed into one static sentinel.
	controlOutcomeHandlerError = "handler_error"
)

// controlReasonObserved is the bounded reason for a claimed event that carries no
// capture classification of its own.
const controlReasonObserved = "control_call_observed"

// controlReasonUnknown is the single static collapse bucket for any
// classification this vocabulary does not recognise. It keeps the emitted reason
// bounded even if a future capture classification is added without being added
// here, which is what makes the dimension provably finite.
const controlReasonUnknown = "control_call_reason_unknown"

// controlReasonVocabulary is the closed set of bounded control-call reasons this
// seam may report: the sticky capture classifications, the two validated provider
// outcome reasons, the bare observed reason, and the single collapse bucket.
var controlReasonVocabulary = map[string]bool{
	controlReasonUnknown:         true,
	controlReasonObserved:        true,
	"completion_complete":        true,
	"completion_invalid":         true,
	controlReasonIDInvalid:       true,
	controlReasonDuplicateStart:  true,
	controlReasonDuplicateFinish: true,
	controlReasonMultipleCalls:   true,
	controlReasonNameConflict:    true,
	controlReasonArgsAfterFinish: true,
	controlReasonArgsOverflow:    true,
	controlReasonArgsMalformed:   true,
	controlReasonBeforeStart:     true,
	controlReasonResultObserved:  true,
	controlReasonMalformedItem:   true,
	controlReasonUnterminated:    true,
}

// boundedControlReasonCode collapses any unrecognised classification into the one
// static unknown bucket, so the reason dimension can never carry unbounded text.
// An empty reason is not a classification: it is the shape of a claimed event that
// carries no verdict of its own, so it collapses to the bare observed reason
// rather than to the invalid bucket.
func boundedControlReasonCode(reason string) string {
	switch reason {
	case "":
		return controlReasonObserved
	case controlReasonUnknown:
		return controlReasonUnknown
	}
	if controlReasonVocabulary[reason] {
		return reason
	}
	return controlReasonUnknown
}

// controlOutcomeForReason classifies one claimed event into the bounded outcome
// and the bounded reason of the SAME record, in one decision over the raw
// classification, so the two emitted dimensions can never contradict each other.
//
// The raw reason is examined before it is collapsed: a claimed event that carries
// no classification of its own — the start, each buffered args delta — is the
// design's default `observed`, which is also what makes an ordinary private-path
// claim distinguishable from a malformed control call. Collapsing first would
// report that same event as `invalid` with the unknown bucket and leave the
// `observed` outcome unreachable.
//
// A classification this vocabulary does not recognise, and the released, fatal,
// and handler-error paths that deliberately report the single collapse bucket,
// are invalid control calls: the unknown bucket is therefore the invalid outcome's
// reason, never the observed outcome's.
func controlOutcomeForReason(reason string) (outcome, boundedReason string) {
	switch reason {
	case "":
		return controlOutcomeObserved, controlReasonObserved
	case controlReasonArgsOverflow:
		return controlOutcomeArgsTooLarge, controlReasonArgsOverflow
	case controlReasonMultipleCalls:
		return controlOutcomeMultipleCalls, controlReasonMultipleCalls
	default:
		return controlOutcomeInvalid, boundedControlReasonCode(reason)
	}
}

// logControlCallObservation records the bounded classification of ONE CLAIMED
// EVENT of the private control path. This seam is per claimed event, not per
// handled call: a start, each buffered args delta, and the completion verdict are
// separate records, which is what makes the design's `observed` outcome
// observable at all.
//
// The record volume is bounded per attempt rather than unbounded: a capture can
// claim at most one call start, one args delta per buffered argument byte under
// the frozen args budget, and one finish, and its correlation window is a fixed
// number of call identities. Ordinary traffic pays nothing, because
// divertControlCall returns before any logging when there is no capture or the
// observation is unclaimed.
//
// Only the frozen provider identity and the bounded outcome/reason
// classification are attached: no result text, tool arguments, prompt text, call
// id, item id, or any other raw identifier can reach this record, because none of
// them is ever passed in. The reason is bounded once more here at the sink, so a
// caller that ever regressed to an unbounded classification still could not widen
// the emitted dimension.
func logControlCallObservation(ctx context.Context, log *slog.Logger, providerID, outcome, reason string) {
	if log == nil {
		return
	}
	diag.LogDecision(ctx, log, "control_tool_call", diag.AttrOpts{},
		slog.String("provider_id", boundedTerminalDecisionString(providerID, terminaldecision.MaxProviderIDBytes)),
		slog.String("outcome", outcome),
		slog.String("reason_code", boundedControlReasonCode(reason)),
	)
}

// divertControlCall runs the private control-call interception for one backend
// event.
//
// swallowed=true means the event belongs to the private protocol path and must
// not continue on the ordinary tool path or reach the client. err is the bounded
// protocol failure the existing receive error ownership already handles. An
// absent capture — the ordinary no-provider, no-activation, or no-eligible-tool
// case — returns immediately with neither, so that path performs no hashing, no
// allocation, and no comparison per event.
func (p *responsePipeline) divertControlCall(ctx context.Context, attempt *attemptSession, ev aipapi.Event) (swallowed bool, err error) {
	if p == nil || attempt == nil || attempt.controlCapture == nil {
		return false, nil
	}
	if ev.Kind == aipapi.EventResponseFinished {
		attempt.closeControlCapture()
		return false, nil
	}
	obs, owned := attempt.observeControlCall(ev)
	if !owned {
		// A released attempt keeps its capture visible but unusable. A control
		// event that reaches this point cannot be classified, so it fails closed
		// instead of reopening the capture or continuing as ordinary client
		// tool traffic.
		p.logControlCall(ctx, attempt, controlOutcomeInvalid, controlReasonUnknown)
		return true, errControlAttemptReleased
	}
	if !obs.Claimed() {
		return false, nil
	}
	if obs.Fatal() != nil {
		attempt.clearControlOutcome()
		p.logControlCall(ctx, attempt, controlOutcomeInvalid, controlReasonUnknown)
		return true, obs.Fatal()
	}
	if obs.Completed() {
		return true, p.handleAndLogControlCall(ctx, attempt, obs)
	}
	if obs.Invalid() {
		attempt.clearControlOutcome()
	}
	outcome, reason := controlOutcomeForReason(obs.Reason())
	p.logControlCall(ctx, attempt, outcome, reason)
	return true, nil
}

// logControlCall emits the bounded classification of one claimed control event
// for this attempt, attaching only the frozen provider identity the activation
// already froze. outcome and reason are the pair one classification produced, so
// a caller never passes them independently.
func (p *responsePipeline) logControlCall(ctx context.Context, attempt *attemptSession, outcome, reason string) {
	if p == nil || attempt == nil {
		return
	}
	var providerID string
	if attempt.controlTool != nil {
		providerID = attempt.controlTool.providerID
	}
	logControlCallObservation(ctx, p.log, providerID, outcome, reason)
}

// handleAndLogControlCall runs the one bounded handler invocation for a completed
// control call and records its bounded outcome. A provider error is reported as
// the generic handler-error classification, never as provider text: the boundary
// already collapsed the concrete cause into one static sentinel.
func (p *responsePipeline) handleAndLogControlCall(ctx context.Context, attempt *attemptSession, obs controlCallObservation) error {
	err := attempt.handleControlCall(ctx, obs.Call())
	if err != nil {
		p.logControlCall(ctx, attempt, controlOutcomeHandlerError, controlReasonUnknown)
		return err
	}
	// The stored outcome is the only verdict the private path publishes. Reading it
	// back under the attempt's own lock copies one bounded classification; the
	// result text itself is never inspected here.
	outcome, reason := controlCompletionClassification(attempt)
	p.logControlCall(ctx, attempt, outcome, reason)
	return nil
}

// controlCompletionClassification reads the one stored control outcome and
// classifies it into the bounded outcome and reason of the same record inside a
// SINGLE locked region.
//
// The pair can never be torn: a concurrent cancellation, Close, attempt loss, or
// replacement that releases this attempt between two reads could otherwise report
// a valid outcome beside the unknown collapse bucket, which would contradict
// itself. Only the bounded classification leaves the lock; the outcome's result
// text is never inspected.
//
// The provider's own bounded reason survives the classification: an outcome this
// attempt no longer owns, or one it never received, is the invalid collapse bucket,
// while an outcome the handler did return keeps the provider's own bounded verdict
// — the valid completion reason, or the bounded invalid reason of a malformed one.
func controlCompletionClassification(a *attemptSession) (outcome, reason string) {
	if a == nil {
		return controlOutcomeInvalid, controlReasonUnknown
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlOutcome == nil {
		return controlOutcomeInvalid, controlReasonUnknown
	}
	reason = boundedControlReasonCode(a.controlOutcome.ReasonCode)
	if a.controlOutcome.Kind != controltool.OutcomeComplete {
		return controlOutcomeInvalid, reason
	}
	return controlOutcomeValid, reason
}

// observeControlCall runs the capture's per-event decision under the attempt's
// control lock.
//
// The bool reports whether this attempt still owns its control state. A released
// attempt answers false without touching the capture, so cleanup and observation
// are ordered rather than racy, and a late event cannot observe or revive state
// that cleanup already released. The control lock is held only for this bounded
// classification: no provider, backend, or terminal call happens inside it.
func (a *attemptSession) observeControlCall(ev aipapi.Event) (controlCallObservation, bool) {
	if a == nil || a.controlCapture == nil {
		return controlCallObservation{}, false
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlReleased {
		return controlCallObservation{}, false
	}
	return a.controlCapture.observe(ev), true
}

// handleControlCall invokes the pinned generation control provider for the one
// captured, bounded completion and stores only the validated outcome on this
// attempt.
//
// The handoff is claim-then-call-then-store, and the claim and the store are the
// only locked regions. The provider call runs unlocked, so cancellation, Close,
// or attempt loss can dispose this attempt's state while the handler is still
// in flight; the store then re-checks live ownership and drops the result
// instead of resurrecting state the attempt no longer owns.
//
// The invocation never falls through to ordinary tool execution and never
// publishes client output: a validated completion stays private on this attempt
// until the later terminal owner reads it, and a provider failure clears any
// pending outcome before returning a bounded error to existing receive error
// ownership.
func (a *attemptSession) handleControlCall(ctx context.Context, call controltool.CompletedCall) error {
	if a == nil {
		return errControlHandlerUnavailable
	}
	// The call arrives already owned: the capture hands off its argument buffer and
	// then only nils its own fields, so concurrent cleanup drops capture references
	// without ever mutating the bytes this value points at.
	activation, ok := a.claimControlHandoff()
	if !ok {
		return errControlHandlerUnavailable
	}
	outcome, err := extensions.HandleControlTool(ctx, extensions.ControlToolHandleRequest{
		ProviderID:   activation.providerID,
		Provider:     activation.provider,
		Call:         call,
		Meta:         controlHandleMeta(activation.meta),
		MaxArgsBytes: activation.projection.MaxArgsBytes(),
	})
	if err != nil {
		a.clearControlOutcome()
		return err
	}
	if !a.storeControlOutcome(outcome) {
		// The attempt was released while the provider was in flight, so this
		// result belongs to state the attempt no longer owns and is discarded with
		// it. The release was itself a legitimate lifecycle transition — the owner
		// that released it already decides the response outcome — so the handoff
		// stays a private protocol event and does not raise a second, competing
		// terminal cause over that owner's decision. A *later* control event on a
		// released attempt still fails closed, because then nothing can classify it.
		return nil
	}
	return nil
}

// claimControlHandoff records the one permitted completion handoff and returns the
// pinned immutable activation the provider call addresses. The activation is
// request/attempt provenance frozen at open, so the provider reads no live
// snapshot, identity, or spec; the returned pointer names that frozen value and
// nothing about it changes for the rest of the attempt. It reports false when this
// attempt may not hand off at all: it was already handed off, its activation is not
// active, or its control state was released.
func (a *attemptSession) claimControlHandoff() (*controlToolActivation, bool) {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlReleased || !a.controlTool.active() {
		return nil, false
	}
	if a.controlHandled {
		// One completion handoff at most. The capture already guarantees a single
		// Completed observation per response, so a second one is a wiring defect,
		// not a protocol state: revoke any outcome rather than re-invoke.
		a.controlOutcome = nil
		return nil, false
	}
	a.controlHandled = true
	return a.controlTool, true
}

// storeControlOutcome records a validated outcome only while this attempt still
// owns its control state, and reports whether it did. A handler that returns
// after cancellation, Close, or attempt loss therefore cannot restore a result.
func (a *attemptSession) storeControlOutcome(outcome controltool.Outcome) bool {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlReleased {
		return false
	}
	a.controlOutcome = &outcome
	return true
}

// controlHandleMeta hands the provider its own deep-owned copy of the frozen
// provenance.
//
// controltool.Meta carries views, and those views own backing slices and maps.
// Passing the activation's value straight to the provider would let a provider
// that writes through the view it was handed corrupt the activation's own
// provenance for the rest of the attempt. The existing request-path clone helpers
// are reused here rather than a second cloning convention, so the wire-level
// facts stay owned exactly as they were when the request path froze them.
func controlHandleMeta(meta controltool.Meta) controltool.Meta {
	meta.Scope = meta.Scope.Clone()
	meta.Session = cloneSessionView(meta.Session)
	meta.Workspace = cloneWorkspaceView(meta.Workspace)
	return meta
}

// closeControlCapture is the normal end of one response stream. A started control
// call that never finished is invalid, so any pending outcome is dropped; a call
// that already completed stays valid so the later terminal owner can still read
// it. The terminal event itself is ordinary and keeps streaming to the client.
func (a *attemptSession) closeControlCapture() {
	if a == nil || a.controlCapture == nil {
		return
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlReleased {
		return
	}
	if a.controlCapture.closeResponse().Invalid() {
		a.controlOutcome = nil
	}
}

// controlCompletionFacts reports the two bounded, ownership-derived completion
// booleans the terminal owner may project as canonical evidence: whether this
// attempt successfully activated a proxy-owned control protocol, and whether it
// observed a valid completion of that protocol.
//
// expected is the immutable fact. It is exactly "this attempt had a successfully
// active proxy control protocol", read from the frozen activation that survives
// disposal on purpose, so it needs no lock and never re-reads a provider
// identity, spec, capability, snapshot, or request extension.
//
// observed is the live fact. It is the one mutable read, so it happens under the
// attempt control lock: that orders it against cleanup, and a disposed attempt —
// whose cancellation, Close, loss, or replacement already decided the response —
// answers false rather than contributing completion evidence into a logical
// response it no longer owns. Only the boolean is copied out. The outcome, its
// result text, and every argument byte stay private on the attempt, and no
// provider, backend, or terminal work — and no other lock — is taken here.
func (a *attemptSession) controlCompletionFacts() (expected, observed bool) {
	if a == nil {
		return false, false
	}
	expected = a.controlTool.active()
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	return expected, !a.controlReleased && a.controlOutcome != nil && a.controlOutcome.Kind == controltool.OutcomeComplete
}

// clearControlOutcome drops the private pending outcome. A malformed, duplicate,
// or multiple control sequence after a valid completion revokes that completion
// without ever invoking the provider a second time.
func (a *attemptSession) clearControlOutcome() {
	if a == nil {
		return
	}
	a.controlMu.Lock()
	a.controlOutcome = nil
	a.controlMu.Unlock()
}

// discardControlState releases every private control buffer, raw ID, correlation
// digest, and pending result this attempt retained, and marks the state released
// so a late event or a returning handler cannot revive it.
//
// It is the cancellation, Close, attempt-loss, and replacement cleanup path. The
// immutable activation is deliberately left in place: it is request/attempt
// provenance, and erasing it would only hide who owned the released state. The
// capture object itself is kept for the same reason — a released attempt stays
// visible to late callers, which then fail closed instead of quietly behaving
// like an attempt that never had a control protocol.
//
// The operation is idempotent, holds the control lock for a bounded field release
// only, and never runs provider, backend, or terminal work while holding it.
func (a *attemptSession) discardControlState() {
	if a == nil {
		return
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlReleased {
		return
	}
	a.controlReleased = true
	if a.controlCapture != nil {
		a.controlCapture.discard()
	}
	a.controlOutcome = nil
}
