package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Tool-name correction rails.
//
// A finalizer that recognized a completed call to a tool the model was never
// offered asks the assembler to steer the turn back through ActionSteer. Before
// anything is withheld, core answers one bounded question - may this attempt's
// call be corrected at all? - in the fixed order below. The rails only ever
// fail open: every decline delivers the call exactly as it would have been
// delivered without the feature, so an unsafe or impossible correction can
// never make the response worse.
//
// Every decision emits exactly one bounded diagnostic record. Later outcomes of
// an accepted correction (a withheld-output overflow, or a follow-up that could
// not be published) add a second record through
// [responsePipeline.logToolNameCorrection] with outcome delivered.

const (
	// toolNameCorrectionLogMessage is this feature's decision-record message.
	toolNameCorrectionLogMessage = "tool_name_correction"

	// Decline reason codes, in evaluation order: when several rails apply, the
	// first one is the recorded reason.
	toolNameCorrectionReasonBudgetSpent             = "budget_spent"
	toolNameCorrectionReasonVisibleOutput           = "visible_output"
	toolNameCorrectionReasonReasoningOutput         = "reasoning_output"
	toolNameCorrectionReasonHistoryRepeat           = "history_repeat"
	toolNameCorrectionReasonContinuationUnsupported = "continuation_unsupported"
	toolNameCorrectionReasonSteeringUnavailable     = "steering_unavailable"

	// Decision outcomes.
	toolNameCorrectionOutcomeCorrected = "corrected"
	toolNameCorrectionOutcomeDelivered = "delivered"

	// toolNameCorrectionReasonWithholdOverflow is the second decision record's
	// reason when an accepted correction produced more than the withhold queue
	// can hold, so the original output was delivered instead.
	toolNameCorrectionReasonWithholdOverflow = "withhold_overflow"

	// The withhold queue's bound: an accepted correction may hold at most this
	// many events and this many bytes of retained payload before it must fail
	// open and deliver the original output.
	maxWithheldEvents = 256
	maxWithheldBytes  = 1 << 20

	// toolNameCorrectionOverlayID names the dedicated steering overlay that
	// carries the hidden correction message. It is deactivated when the client
	// response ends, so a later turn never projects the message.
	toolNameCorrectionOverlayID = "tool-name-correction"

	// toolNameCorrectionProvenance is the continuation intent's provenance and
	// reason code: the follow-up is an internal correction, not a provider
	// decision.
	toolNameCorrectionProvenance = "tool_name_correction"

	// toolNameCorrectionReasonFollowupFailed is the second decision record's
	// reason when the follow-up could not be published and the original output
	// was delivered instead.
	toolNameCorrectionReasonFollowupFailed = "followup_failed"
)

// errToolCorrectionNotPublished is the correction failure policy's bounded
// pre-publication failure: the correction overlay was deactivated and the
// request terminal was deliberately left open, so the receive loop still owns
// the original attempt's withheld output and its finish.
var errToolCorrectionNotPublished = errors.New("runtime: tool name correction not published")

// toolNameCorrectionGateForEvent returns the steer gate for one backend event,
// or nil for every event the assembler can never consult it for: only a
// completed tool call finalizes and asks about correction, so the receive hot
// path builds a gate only for EventToolCallFinished.
func (p *responsePipeline) toolNameCorrectionGateForEvent(ctx context.Context, facts recvTurnFacts, terminal *turnTerminal, attempt *attemptSession, ev aipapi.Event) func(name string, req toolcall.SteerRequest) bool {
	if ev.Kind != aipapi.EventToolCallFinished {
		return nil
	}
	return p.toolNameCorrectionGate(ctx, facts, terminal, attempt, ev.ToolCallID)
}

// toolNameCorrectionGate returns the synchronous steer gate for one prepared
// backend event. The gate is owned by the single receive loop: it performs no
// I/O, decides once per steered call, records one bounded diagnostic, and marks
// the attempt as withholding only when it accepts. The receive-loop coordinator
// supplies the turn terminal and the attempt; per-event preparation only
// forwards the built gate to the assembler.
//
// decidedCallID names the call this event finalizes, so the produced-output
// facts can exclude that call's own lifecycle. It is normalized here exactly
// as the tool-call assembler normalizes a call ID.
func (p *responsePipeline) toolNameCorrectionGate(ctx context.Context, facts recvTurnFacts, terminal *turnTerminal, attempt *attemptSession, decidedCallID string) func(name string, req toolcall.SteerRequest) bool {
	decidedCallID = strings.TrimSpace(decidedCallID)
	return func(name string, req toolcall.SteerRequest) bool {
		if reason := p.admitToolCorrection(ctx, facts, terminal, attempt, decidedCallID, name, req); reason != "" {
			p.logToolNameCorrection(ctx, facts, attempt, name, toolNameCorrectionOutcomeDelivered, reason)
			return false
		}
		terminal.acceptToolCorrection()
		attempt.markWithholding(name, req.Instruction)
		// The slice has exactly one bounded detection reason, and the
		// finalizer's own reason code travels with its Result, not with the
		// steer request the gate receives.
		p.logToolNameCorrection(ctx, facts, attempt, name, toolNameCorrectionOutcomeCorrected, toolcall.ReasonUnknownToolName)
		return true
	}
}

// admitToolCorrection evaluates the correction rails in their documented order
// and returns the bounded decline reason, or "" when the correction is
// admitted. It never consumes budget and never marks the attempt: only
// acceptance does that, and only after every rail passed.
func (p *responsePipeline) admitToolCorrection(ctx context.Context, facts recvTurnFacts, terminal *turnTerminal, attempt *attemptSession, decidedCallID, name string, req toolcall.SteerRequest) string {
	if terminal.toolCorrectionsAccepted() >= int64(req.TurnBudget) {
		return toolNameCorrectionReasonBudgetSpent
	}
	if attempt.producedVisibleBefore(decidedCallID) {
		return toolNameCorrectionReasonVisibleOutput
	}
	if attempt.producedReasoningOutput() && !req.AllowAfterReasoning {
		return toolNameCorrectionReasonReasoningOutput
	}
	if toolNameInIngressHistory(facts.ingressCall, name) {
		return toolNameCorrectionReasonHistoryRepeat
	}
	if terminal == nil || !terminal.supportsContinuation || terminal.isInterleavedThinker() || len(p.completionGatesFromContext(ctx)) > 0 {
		return toolNameCorrectionReasonContinuationUnsupported
	}
	if terminal.steeringWriterFactory == nil || terminal.conversationReader == nil {
		return toolNameCorrectionReasonSteeringUnavailable
	}
	return ""
}

// toolNameInIngressHistory reports whether the client's original request already
// contains a tool call with exactly this name. A repeat means the client has
// seen this call before, so delivering it unchanged is the only safe outcome.
func toolNameInIngressHistory(ingress aipapi.Call, name string) bool {
	if name == "" {
		return false
	}
	for _, item := range aipapi.NormalizedItems(ingress) {
		if item.Kind == aipapi.ItemKindToolCall && item.ToolCall != nil && item.ToolCall.Name == name {
			return true
		}
	}
	return false
}

// AttemptWithhold: an accepted correction holds the attempt's would-be client
// output in an ordered queue instead of dispatching it. The queue is bounded so
// a correction that keeps producing output can never grow without limit; past
// the bound the withhold ends by releasing everything it held, followed by the
// event that outgrew it, through the normal dispatch path (delivered /
// withhold_overflow).

// withheldQueue is one attempt's ordered hold. events are exactly the events
// that would have been dispatched, so releasing them through the assembler drain
// replays them on the normal routes; the drain's dispatch rebuilds the same
// preparation a fresh event gets, and the event value itself is unchanged.
// name is the steered call's effective name, kept only so the outcome record
// that ends the withhold can hash it; instruction is the accepted steer
// request's instruction, kept only so the follow-up continuation can carry it.
// Neither is ever recorded raw.
type withheldQueue struct {
	name         string
	instruction  string
	events       []aipapi.Event
	payloadBytes int
}

// enqueueWithheld appends one event that would otherwise have been dispatched
// to this attempt's withhold queue. It reports whether the event was held; a
// false result means the queue is at its bound, so the caller releases the queue
// plus this event through releaseWithheld.
func (a *attemptSession) enqueueWithheld(ev aipapi.Event) bool {
	if a == nil {
		return false
	}
	a.withholdMu.Lock()
	defer a.withholdMu.Unlock()
	size := ev.PayloadBytes()
	if len(a.withheld.events) >= maxWithheldEvents || a.withheld.payloadBytes > maxWithheldBytes-size {
		return false
	}
	// Clone after the bound check passes, so an overflowing event is not
	// copied: the queue retains a deep copy the producer cannot mutate.
	a.withheld.events = append(a.withheld.events, ev.Clone())
	a.withheld.payloadBytes += size
	return true
}

// releaseWithheld prepends this attempt's withheld events followed by tail to
// the front of the tool-call drain, in their original order, and clears the
// withhold state. Prepend is correct for every caller (the EOF, drain-error,
// prepared-error and follow-up-failed paths): held output is earlier than any
// assembler output still queued, and the finish tail still comes after the held
// events. The drain dispatches every released event through
// dispatchClientFacingEvent exactly once, so recording, gates, and the finish
// authority all run on the normal routes. It returns the steered name the
// withhold was accepted for, so the caller can record the outcome that ended
// it, or "" when nothing was withheld.
func releaseWithheld(attempt *attemptSession, tail ...aipapi.Event) string {
	if attempt == nil {
		return ""
	}
	attempt.withholdMu.Lock()
	events, name := attempt.withheld.events, attempt.withheld.name
	attempt.withheld = withheldQueue{}
	attempt.withholdMu.Unlock()
	if assembler := attempt.toolCallAssembler(); assembler != nil {
		assembler.prepend(append(events, tail...)...)
	}
	attempt.withholding.Store(false)
	return name
}

// divertWithheld routes one event that would otherwise be dispatched for a
// withholding attempt into that attempt's withhold queue and reports whether it
// was diverted. It is the single divert for both receive-loop sites: a fresh
// event after preparation, and an assembler-drain pop. A withholding attempt's
// backend finish never reaches this divert: runToolCorrection intercepts it at
// the event-preparation call site. When the queue is at its bound the divert
// ends the withhold by releasing the queue and this event, so the original
// output is delivered and the overflow recorded.
func (p *responsePipeline) divertWithheld(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev aipapi.Event) bool {
	if !attempt.isWithholding() {
		return false
	}
	if attempt.enqueueWithheld(ev) {
		return true
	}
	name := releaseWithheld(attempt, ev)
	p.logToolNameCorrection(ctx, facts, attempt, name, toolNameCorrectionOutcomeDelivered, toolNameCorrectionReasonWithholdOverflow)
	return true
}

// runToolCorrection is the correction finish intercept. A withholding attempt's
// backend finish triggers the hidden follow-up before that finish could reach
// the client: the transaction persists the accepted instruction in the
// dedicated correction overlay and publishes B2 as the continuation of the same
// client response.
//
// On publication the withheld queue is discarded and the receive loop continues
// on B2; the overlay is deactivated when the response ends. When the follow-up
// cannot be published (a rail, an admission, or a preparation failure), the
// withheld output and the finish are pushed onto the tool-call drain in their
// original order, so the client receives exactly what it would have received
// without the feature, each event recorded once. Cancellation and deadline
// causes keep the transaction's existing claim path and its error is returned
// as today.
func runToolCorrection(ctx context.Context, t *turnTerminal, s *retryRecvStream, attempt *attemptSession, finish aipapi.Event) error {
	if s == nil || attempt == nil {
		return nil
	}
	name, instruction := attempt.withheldSteer()
	intent := terminaldecision.ContinuationIntent{
		TrajectoryRef: strings.TrimSpace(attempt.bleg.BLegID),
		Instruction:   instruction,
		Provenance:    toolNameCorrectionProvenance,
		ReasonCode:    toolNameCorrectionProvenance,
	}
	published, err := continuationTransactionWithOverlay(ctx, t, s, intent, continuationPolicy{
		overlayID: toolNameCorrectionOverlayID,
		failure:   continuationFailureFailOpen,
		lineage:   continuationLineagePreserved,
	})
	if published {
		// The overlay outlives the transaction: B2 still carries the
		// correction message, and the response-end transition removes it. A
		// post-publication settlement diagnostic cannot reopen the response;
		// B2 stays published exactly as the transaction left it.
		t.publishToolNameCorrectionOverlay(s.facts.aLegID, s.facts.traceID)
		attempt.discardWithheld()
		return nil
	}
	if !errors.Is(err, errToolCorrectionNotPublished) {
		// Cancellation, deadline, or a transaction diagnostic already
		// finalized the request terminal through the existing claim path. A
		// canceled response delivers nothing, so the withheld queue is
		// discarded exactly as it is on any other cancellation.
		attempt.discardWithheld()
		return err
	}
	releaseWithheld(attempt, finish)
	s.responsePipeline.logToolNameCorrection(ctx, s.facts, attempt, name, toolNameCorrectionOutcomeDelivered, toolNameCorrectionReasonFollowupFailed)
	return nil
}

// logToolNameCorrection emits this feature's one bounded decision record. The
// record carries lineage, the bounded outcome and reason, and a SHA-256 prefix
// of the tool name; the raw name, arguments, and instruction text never reach
// it (requirements 5.1, 5.2).
func (p *responsePipeline) logToolNameCorrection(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, name, outcome, reason string) {
	if p == nil || p.log == nil {
		return
	}
	bLegID := ""
	if attempt != nil {
		bLegID = attempt.bleg.BLegID
	}
	p.log.LogAttrs(ctx, slog.LevelInfo, toolNameCorrectionLogMessage,
		slog.String("trace_id", facts.traceID),
		slog.String("b_leg_id", bLegID),
		slog.String("tool_name_hash", HashOpaqueIDForLog(name)),
		slog.String("outcome", outcome),
		slog.String("reason", reason),
	)
}
