// Pending completion-result publication for
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Completion
// Evidence and Pending Result / Response Interception Placement / Concurrency and
// Lifecycle; requirements 6.3-6.7, 11.3, 11.5, 12.5).
//
// This file owns one private, bounded terminal-publication value for a valid
// proxy-owned completion. It is not a gate engine, a terminal owner, a registry,
// or a side channel to a frontend: the accepted request-terminal owner calls it
// from inside the winning request effects, and every effect it performs runs
// through the existing response observation seam.
//
// Ownership of each fact:
//
//   - The bounded lexical result is a copy taken from the winning attempt's
//     private control state under that attempt's control lock, before attempt
//     cleanup disposes it. It is dropped by every losing, errored, continued,
//     cancelled, or closed path.
//   - The canonical events are ordinary legal response/message/text events. They
//     run the existing response-part hooks exactly once each and participate in
//     the existing completion-gate chain exactly once. No synthetic client tool
//     call, tool result, or item is ever produced.
//   - Eligibility uses trimmed released assistant text plus post-hook assistant
//     text already inside the gate buffer. The general output-committed bit is
//     deliberately not used, because reasoning, tool, and media output also commit
//     while proving nothing about an assistant answer existing.
//   - An unresolved, incomplete, ambiguous, or over-capacity ordinary tool
//     boundary in the held candidate suppresses the automatic result even when
//     the terminal provider allowed the stop. Ordinary canonical output is never
//     suppressed with it.
//
// The prepared value is not released while it is prepared. Every effect it needs
// runs exactly once at the accepted publication boundary, and the private release
// drain below exists only to deliver already-observed events to the client in
// canonical order.
package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"

	accountingstream "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/streamusage"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// pendingCompletionText is the bounded lexical value one attempt's private
// control state may contribute as the final client-visible assistant answer,
// together with the frozen attempt identity that owned it.
type pendingCompletionText struct {
	result string
	blegID string
	seq    uint32
}

// pendingCompletion is the prepared, held publication value. Nothing in it is
// released while it is merely prepared.
type pendingCompletion struct {
	// origin is the attempt session that actually prepared this value. Every
	// reuse, evidence projection, accepted callback, activation, and drain
	// delivery revalidates it against the live attempt slot, so a continued or
	// replaced attempt can never publish a previous attempt's candidate.
	origin *attemptSession
	// text is the bounded lexical result copied out of the winning attempt.
	text pendingCompletionText
	// events is the whole evaluated canonical sequence: any synthesized
	// lifecycle plus the assistant text, in order, and never the finish.
	events []aipapi.Event
	// finish is the accepted finish that must follow the sequence.
	finish aipapi.Event
	// supplemental is the bounded current ordinary action supplement the first
	// terminal decision must see, ahead of older request history.
	supplemental []terminaldecision.ActionFact
	// boundarySafe reports that the final held ordinary boundary is fully
	// correlated and complete. It gates result display only.
	boundarySafe bool
	// supplementSafe reports that the held ordinary facts are representable within
	// the existing evidence contract, with no orphan, ambiguity, or truncation. It
	// gates projection only, so an incomplete-but-representable boundary is still
	// visible to the deciding provider while the result stays suppressed.
	supplementSafe bool
	// syntheticFrom is the index in events where the synthesized original suffix
	// starts. It is len(events) when the effective gate output is authoritative
	// and no known synthetic position survives.
	syntheticFrom int
	// suppressed records that an unresolved, incomplete, ambiguous, or
	// over-capacity ordinary boundary removed the synthesized original suffix. The
	// ordinary canonical output around it is untouched, and the result stays
	// completion evidence only.
	suppressed bool
}

// holds reports that this response owns an evaluated canonical candidate: the
// authoritative ordinary output plus the accepted finish. Candidate existence is
// independent of result eligibility, so a suppressed result still owns the
// authoritative ordinary stream and its real finish.
func (c *pendingCompletion) holds() bool {
	return c != nil && c.origin != nil && len(c.events) > 0 && c.finish.Kind == aipapi.EventResponseFinished
}

// publishing reports whether this value contributes client-visible assistant text.
// An ordinary boundary that suppressed the synthesized suffix owns a prepared value
// for its authoritative output and evidence alone, which is not a result
// publication.
func (c *pendingCompletion) publishing() bool {
	return c.holds() && !c.suppressed && strings.TrimSpace(c.text.result) != ""
}

// --- attempt-owned snapshot ----------------------------------------------------

// snapshotPendingCompletionText copies the one valid bounded result out of this
// attempt's private control state.
//
// It runs under the attempt control lock and only copies a complete outcome this
// attempt still owns, so it is ordered against cancellation, Close, attempt loss,
// and replacement cleanup. Nothing else is read: no provider identity, no spec, no
// request metadata, and no SDK tool name. The frozen attempt identity travels
// with the value so the publication can prove which attempt owned it.
func (a *attemptSession) snapshotPendingCompletionText() (pendingCompletionText, bool) {
	if a == nil {
		return pendingCompletionText{}, false
	}
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	if a.controlReleased || a.controlOutcome == nil || a.controlOutcome.Kind != controltool.OutcomeComplete {
		return pendingCompletionText{}, false
	}
	result := strings.TrimSpace(a.controlOutcome.ResultText)
	if result == "" {
		return pendingCompletionText{}, false
	}
	return pendingCompletionText{result: result, blegID: a.bleg.BLegID, seq: uint32(a.bleg.Seq)}, true
}

// --- preparation ---------------------------------------------------------------

// pendingPreparation is the bounded outcome of preparing one response_finished
// for a private publication. It separates three independent facts: whether this
// response owns an evaluated canonical candidate, whether that candidate may
// contribute the private result text, and whether the effective completion-gate
// output carried no accepted finish at all.
type pendingPreparation struct {
	// prepared is the owned candidate, or nil when this response has nothing
	// private to hold.
	prepared *pendingCompletion
	// ordinary is the authoritative completion-gate output head when the
	// effective output carried no accepted finish. The remaining events are
	// already installed as the ordinary gate drain, so the caller must release
	// them through the existing authoritative path and must not evaluate the
	// chain again.
	ordinary aipapi.Event
	// replaced mirrors the ordinary completion-gate replacement marker for the
	// ordinary head.
	replaced bool
	// gateOutput reports that the ordinary head came from the evaluated gate
	// output rather than from raw provider input.
	gateOutput bool
}

// preparePendingCompletion builds the bounded publication value for a
// response_finished event that is about to be recorded and finalized.
//
// It is idempotent per logical response AND per origin attempt: the first finish
// that owns a valid result prepares the value and every later finish route reads
// that same prepared value, but only while the live attempt still is the attempt
// that prepared it. A mismatching attempt disposes the stale value first, so a
// continued or replaced B-leg can never publish its predecessor's candidate.
//
// A zero pendingPreparation with a nil error means this response has nothing
// private to hold, so the ordinary route stays exactly as it was.
func (p *responsePipeline) preparePendingCompletion(
	ctx context.Context,
	facts recvTurnFacts,
	attempt *attemptSession,
	ev aipapi.Event,
	gates []completion.Gate,
	committed bool,
) (pendingPreparation, error) {
	if p == nil || attempt == nil || ev.Kind != aipapi.EventResponseFinished {
		return pendingPreparation{}, nil
	}
	if prepared := p.pendingPreparedFor(attempt); prepared != nil {
		return pendingPreparation{prepared: prepared}, nil
	}
	text, ok := attempt.snapshotPendingCompletionText()
	if !ok {
		return pendingPreparation{}, nil
	}
	base, live := p.gateBufferSnapshot()
	buffering := len(gates) > 0 && !live
	// Eligibility is decided before augmentation, from released assistant text and
	// the post-hook assistant text already inside the gate buffer, so a duplicate
	// answer is suppressed even though that ordinary text has not been committed
	// yet.
	if pendingPriorAssistantText(p.releasedOutputText(), base) {
		return pendingPreparation{}, nil
	}
	suffix, err := p.pendingCompletionSuffix(ctx, facts, attempt, base, text.result)
	if err != nil {
		return pendingPreparation{}, err
	}
	candidate := make([]aipapi.Event, 0, len(base)+len(suffix)+1)
	candidate = append(candidate, base...)
	candidate = append(candidate, suffix...)
	candidate = append(candidate, ev)
	evaluated := candidate
	effectiveReplacement := false
	if buffering {
		res, chainErr := p.runCompletionGateChain(ctx, gates, candidate,
			p.pendingCompletionGateInput(ctx, facts, attempt, committed))
		if chainErr != nil {
			p.releaseCompletionGateBuffers(true)
			return pendingPreparation{}, chainErr
		}
		if len(res.Events) == 0 {
			p.releaseCompletionGateBuffers(true)
			return pendingPreparation{}, errCompletionGateEmptyStream
		}
		// This one chain evaluation is the only evaluation of this response. The
		// evaluated output is now authoritative: either it carries the accepted
		// finish and becomes the private candidate, or it does not and is released
		// through the existing authoritative gate-drain path.
		p.releaseCompletionGateBuffers(true)
		if res.Events[len(res.Events)-1].Kind != aipapi.EventResponseFinished {
			p.setGateDrain(res.Events[1:])
			return pendingPreparation{ordinary: res.Events[0], replaced: res.Replaced, gateOutput: true}, nil
		}
		evaluated = res.Events
		effectiveReplacement = res.EffectiveReplacement
	}
	if len(evaluated) == 0 || evaluated[len(evaluated)-1].Kind != aipapi.EventResponseFinished {
		// An effective gate output that does not end with the accepted finish is
		// the gate's authoritative stream, so the private result stays evidence.
		return pendingPreparation{}, nil
	}
	held := slices.Clone(evaluated[:len(evaluated)-1])
	prepared := &pendingCompletion{
		origin:        attempt,
		text:          text,
		finish:        evaluated[len(evaluated)-1],
		events:        held,
		syntheticFrom: len(base),
		boundarySafe:  true,
	}
	// A synthesized suffix only survives in a known position while the effective
	// output is the augmented original. Once a gate replaced the stream, that
	// stream is authoritative and this value must never rewrite it. Final
	// provenance decides this, not the historical replacement bit: a Replace
	// followed by a Replay restores the augmented original, whose synthetic
	// position is still known.
	if effectiveReplacement {
		prepared.syntheticFrom = len(held)
	}
	// The supplement is built from the FINAL evaluated candidate, so ordinary
	// output a gate removed is never resurrected as projected evidence.
	actions, supplementSafe, boundarySafe := p.pendingOrdinaryActions(facts, attempt, held)
	prepared.supplemental, prepared.supplementSafe, prepared.boundarySafe = actions, supplementSafe, boundarySafe
	if !prepared.boundarySafe && prepared.syntheticFrom < len(held) {
		prepared.events = slices.Clone(held[:prepared.syntheticFrom])
		prepared.syntheticFrom = len(prepared.events)
		prepared.suppressed = true
	}
	p.setPendingPrepared(prepared)
	return pendingPreparation{prepared: prepared}, nil
}

// pendingCompletionSuffix builds the minimal legal canonical assistant lifecycle
// and text for one bounded result and runs the existing response-part hooks on
// each new event exactly once.
//
// Only existing legal event fields are used. No fake tool call, tool result, or
// item is synthesized, and the result never carries a control name, call ID, or
// argument byte.
func (p *responsePipeline) pendingCompletionSuffix(
	ctx context.Context,
	facts recvTurnFacts,
	attempt *attemptSession,
	base []aipapi.Event,
	result string,
) ([]aipapi.Event, error) {
	pm := facts.partMeta(attempt.bleg, attempt.cand)
	// Lifecycle frames already released to the client count exactly like frames
	// still inside the gate buffer: the synthesized suffix must add only what the
	// client has not already seen, so the canonical sequence stays legal.
	_, haveResponse, haveMessage := pendingCompletionLifecycleState(p.lifecycleReleases())
	bufferedIndex, bufferedResponse, bufferedMessage := pendingCompletionLifecycleState(base)
	if bufferedResponse {
		haveResponse = true
	}
	if bufferedMessage {
		haveMessage = true
	}
	messageIndex := bufferedIndex
	out := make([]aipapi.Event, 0, 3)
	if !haveResponse {
		out = append(out, aipapi.Event{Kind: aipapi.EventResponseStarted, MessageIndex: messageIndex})
	}
	if !haveMessage {
		out = append(out, aipapi.Event{Kind: aipapi.EventMessageStarted, MessageIndex: messageIndex})
	}
	out = append(out, aipapi.Event{Kind: aipapi.EventTextDelta, MessageIndex: messageIndex, Delta: result})
	if p.bus == nil {
		return out, nil
	}
	for i := range out {
		ev := out[i]
		if err := p.bus.RunResponsePartHooks(ctx, &ev, pm); err != nil {
			return nil, err
		}
		out[i] = ev
	}
	return out, nil
}

// lifecycleReleases returns only the already-released canonical lifecycle frames.
// It is a narrow projection of the released event history rather than a second
// accumulator, so the client-facing lifecycle truth keeps exactly one owner.
func (p *responsePipeline) lifecycleReleases() []aipapi.Event {
	released := p.seenEventsCopy()
	out := make([]aipapi.Event, 0, 2)
	for _, ev := range released {
		switch ev.Kind {
		case aipapi.EventResponseStarted, aipapi.EventMessageStarted:
			out = append(out, ev)
		}
	}
	return out
}

// pendingCompletionLifecycleState reports the canonical lifecycle frames already
// present in the held candidate and the message index they address.
func pendingCompletionLifecycleState(base []aipapi.Event) (messageIndex int, haveResponse, haveMessage bool) {
	for _, ev := range base {
		switch ev.Kind {
		case aipapi.EventResponseStarted:
			haveResponse = true
			messageIndex = ev.MessageIndex
		case aipapi.EventMessageStarted:
			haveMessage = true
			if !haveResponse {
				messageIndex = ev.MessageIndex
			}
		}
	}
	return messageIndex, haveResponse, haveMessage
}

// pendingHeldAssistantText concatenates the post-hook assistant text already
// inside the gate buffer. Whitespace, reasoning, tool, and media output are not
// assistant answers and never suppress the result.
func pendingHeldAssistantText(base []aipapi.Event) string {
	var b strings.Builder
	for _, ev := range base {
		if ev.Kind == aipapi.EventTextDelta {
			b.WriteString(ev.Delta)
		}
	}
	return b.String()
}

// pendingPriorAssistantText reports whether the logical response already has a
// meaningful client-visible assistant answer, from released content or from the
// post-hook content still inside the completion-gate buffer.
func pendingPriorAssistantText(released string, base []aipapi.Event) bool {
	return strings.TrimSpace(released) != "" || strings.TrimSpace(pendingHeldAssistantText(base)) != ""
}

func (p *responsePipeline) pendingCompletionGateInput(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, committed bool) responseGateInput {
	meta := completion.Meta{TraceID: facts.traceID, ALegID: facts.aLegID, BLegID: attempt.bleg.BLegID, AttemptSeq: attempt.bleg.Seq}
	if views, ok := facts.viewsFor(ctx); ok {
		meta.Scope, meta.Session, meta.Workspace = views.Scope, views.Session, views.Workspace
	}
	services := completion.Services{}
	if snap := p.completionSnapshot(ctx); snap != nil {
		services.State, services.Aux = snap.State(), snap.Aux()
	}
	return responseGateInput{
		meta: meta, services: services, stageLog: p.log,
		committed: committed, limits: completionBufferLimitsFor(p),
	}
}

// --- held ordinary action evidence --------------------------------------------

// pendingOrdinaryActions projects bounded current ordinary action facts from
// released output and the final held candidate, and reports whether that boundary
// is fully correlated and complete.
//
// Correlation runs across the request facts, the already-released events, and the
// held events, so a nameless finished call inherits the trusted name of a start
// that was released before the gate buffer began. Only existing ActionFact fields
// are projected: a bounded item/call reference, the canonical kind and status,
// and the ordinary effective name. No arguments, result payload, private result
// text, control call identity, or synthetic lifecycle is projected.
//
// Individually representable facts are always projected, even when a different
// held action is orphaned, ambiguous, or over capacity: the first deciding
// provider must still see the current ordinary boundary. Ambiguity, truncation,
// and an active assembler fail closed on RESULT ELIGIBILITY only.
func (p *responsePipeline) pendingOrdinaryActions(
	facts recvTurnFacts,
	attempt *attemptSession,
	held []aipapi.Event,
) (actions []terminaldecision.ActionFact, supplementSafe, boundarySafe bool) {
	collector := newPendingActionCollector(p.pendingActionNameIndex(facts, held))
	for _, ev := range held {
		collector.observe(ev)
	}
	supplementSafe = !collector.unrepresentable()
	boundarySafe = supplementSafe && !collector.unresolved()
	// Released output remains part of the current ordinary boundary after
	// buffer overflow. History supplies names, never lifecycle completion.
	released := newPendingActionCollector(p.pendingActionNameIndex(facts, held))
	for _, ev := range p.seenEventsCopy() {
		released.observe(ev)
	}
	// Set latest held identities before an absent held fact can evict a
	// completed released entry. Within-held promotion already ran above.
	for _, key := range collector.order {
		if current := released.byCall[key]; current != nil {
			*current = *collector.byCall[key]
		}
	}
	for _, key := range collector.order {
		if released.byCall[key] == nil {
			released.upsert(key, *collector.byCall[key])
		}
	}
	actions = released.facts()
	supplementSafe = supplementSafe && !released.unrepresentable()
	boundarySafe = boundarySafe && !released.unresolved()
	if toolFinal := attempt.toolCallAssembler(); toolFinal != nil && toolFinal.hasActiveCalls() {
		// An ordinary call the enabled assembler is still holding produced no gated
		// output at all, so it cannot be represented as evidence and must suppress
		// the automatic result.
		return actions, supplementSafe, false
	}
	return actions, supplementSafe, boundarySafe
}

// pendingActionNameIndex collects the trusted ordinary tool names already
// established for this logical response, so a nameless finished call in the held
// candidate inherits the name of a start that crossed the gate-buffer boundary.
// A conflicting identity is recorded as ambiguous and resolves to no name.
func (p *responsePipeline) pendingActionNameIndex(facts recvTurnFacts, held []aipapi.Event) map[string]string {
	names := make(map[string]string)
	note := func(callID, name string) {
		callID, name = strings.TrimSpace(callID), strings.TrimSpace(name)
		if callID == "" || name == "" {
			return
		}
		if prior, ok := names[callID]; ok && prior != name {
			names[callID] = ""
			return
		}
		names[callID] = name
	}
	for _, item := range aipapi.NormalizedItems(facts.terminalFacts().call) {
		if item.Kind == aipapi.ItemKindToolCall && item.ToolCall != nil {
			note(item.ToolCall.CallID, item.ToolCall.Name)
		}
		if item.Kind == aipapi.ItemKindToolResult && item.ToolResult != nil {
			note(item.ToolResult.CallID, item.ToolResult.Name)
		}
	}
	if p != nil {
		for _, ev := range p.seenEventsCopy() {
			if ev.Kind == aipapi.EventToolCallStarted {
				note(ev.ToolCallID, ev.ToolName)
			}
			if ev.Kind == aipapi.EventItem && ev.Item != nil {
				pendingNoteItemToolName(note, ev.Item)
			}
		}
	}
	for _, ev := range held {
		if ev.Kind == aipapi.EventToolCallStarted {
			note(ev.ToolCallID, ev.ToolName)
		}
		if ev.Kind == aipapi.EventItem && ev.Item != nil {
			pendingNoteItemToolName(note, ev.Item)
		}
	}
	return names
}

func pendingNoteItemToolName(note func(string, string), item *aipapi.Item) {
	switch {
	case item.Kind == aipapi.ItemKindToolCall && item.ToolCall != nil:
		note(item.ToolCall.CallID, item.ToolCall.Name)
	case item.Kind == aipapi.ItemKindToolResult && item.ToolResult != nil:
		note(item.ToolResult.CallID, item.ToolResult.Name)
	}
}

// pendingActionCollector accumulates the held ordinary actions in one pass. It is
// single-goroutine, private to preparation, and never retained.
//
// It separates two independent properties. An action is REPRESENTABLE when its
// identity resolves to a trusted ordinary name, which is what lets it be projected
// at all. The boundary is SAFE when every held action is representable and fully
// correlated, which is what gates result display. An orphan, an ambiguity, or an
// over-capacity boundary fails the boundary closed without erasing the facts that
// are individually representable.
type pendingActionCollector struct {
	names     map[string]string
	order     []string
	byCall    map[string]*terminaldecision.ActionFact
	safe      bool
	truncated bool
}

func newPendingActionCollector(names map[string]string) *pendingActionCollector {
	return &pendingActionCollector{names: names, byCall: make(map[string]*terminaldecision.ActionFact), safe: true}
}

// facts returns the individually representable held facts. Unresolved current
// actions come first so they occupy the existing fixed capacity ahead of facts
// that are already complete; within each group canonical observation order holds.
func (c *pendingActionCollector) facts() []terminaldecision.ActionFact {
	if c == nil || len(c.order) == 0 {
		return nil
	}
	out := make([]terminaldecision.ActionFact, 0, len(c.order))
	seen := make(map[string]bool, len(c.order))
	appendUnresolved := func() {
		for _, key := range c.order {
			if seen[key] {
				continue
			}
			if pendingActionResolved(c.byCall[key]) {
				continue
			}
			seen[key] = true
			out = append(out, *c.byCall[key])
		}
	}
	appendUnresolved()
	for _, key := range c.order {
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, *c.byCall[key])
	}
	return out
}

// pendingActionResolved reports that one held action already reached a completed
// boundary, so it no longer describes an unresolved ordinary boundary.
func pendingActionResolved(action *terminaldecision.ActionFact) bool {
	if action == nil || action.Kind == aipapi.ItemKindMessage {
		return true
	}
	return terminalDecisionActionRank(action.Status) >= terminalDecisionActionRank(aipapi.ItemStatusCompleted)
}

// unrepresentable reports that the held ordinary boundary cannot be trusted as
// complete because of an orphan, an ambiguity, a conflicting identity, or a
// truncation that could hide an unsafe fact. Facts that are individually
// representable are still projected; nothing is invented to fill the gap.
func (c *pendingActionCollector) unrepresentable() bool {
	return c == nil || !c.safe || c.truncated
}

// unresolved reports that the held boundary cannot be trusted as fully correlated
// and complete. An ordinary call that never reached a completed boundary is
// unresolved even when its facts are perfectly representable.
func (c *pendingActionCollector) unresolved() bool {
	if c.unrepresentable() {
		return true
	}
	for _, action := range c.byCall {
		if !pendingActionResolved(action) {
			return true
		}
	}
	return false
}

// resolve returns the effective trusted name for one call identity. A false
// report means the identity is unknown, ambiguous, or missing.
func (c *pendingActionCollector) resolve(callID, own string) (string, bool) {
	callID = strings.TrimSpace(callID)
	own = strings.TrimSpace(own)
	if callID == "" {
		return "", false
	}
	if trusted, known := c.names[callID]; known && (trusted == "" || (own != "" && own != trusted)) {
		return "", false
	}
	if own != "" {
		return boundedTerminalDecisionIdentifier(own), true
	}
	name, ok := c.names[callID]
	if !ok || name == "" {
		return "", false
	}
	return boundedTerminalDecisionIdentifier(name), true
}

func (c *pendingActionCollector) upsert(key string, fact terminaldecision.ActionFact) {
	if existing, ok := c.byCall[key]; ok {
		// Two different trusted names for one identity are ambiguous. Neither is
		// invented into the fact and the whole boundary fails closed.
		if existing.Name != "" && fact.Name != "" && existing.Name != fact.Name {
			c.safe = false
			return
		}
		if terminalDecisionActionRank(fact.Status) > terminalDecisionActionRank(existing.Status) {
			existing.Status = fact.Status
		}
		if existing.Name == "" {
			existing.Name = fact.Name
		}
		return
	}
	if len(c.order) >= terminaldecision.MaxEvidenceActions {
		// The existing evidence capacity is fixed. A new unresolved current action
		// displaces an already-resolved one so the boundary that actually needs a
		// decision keeps its fact; the displacement still fails the boundary closed
		// because a dropped fact could have been unsafe.
		if !pendingActionResolved(&fact) {
			if at := c.resolvedIndexLocked(); at >= 0 {
				evicted := c.order[at]
				delete(c.byCall, evicted)
				c.order = append(c.order[:at], c.order[at+1:]...)
			} else {
				c.truncated = true
				return
			}
		} else {
			c.truncated = true
			return
		}
		c.truncated = true
	}
	stored := fact
	c.byCall[key] = &stored
	c.order = append(c.order, key)
}

// resolvedIndexLocked returns the position of the first already-resolved stored
// fact, or -1 when every stored fact is still unresolved.
func (c *pendingActionCollector) resolvedIndexLocked() int {
	for i, key := range c.order {
		if pendingActionResolved(c.byCall[key]) {
			return i
		}
	}
	return -1
}

func (c *pendingActionCollector) observe(ev aipapi.Event) {
	switch ev.Kind {
	case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
		c.observeToolEvent(ev)
	case aipapi.EventItem:
		c.observeItem(ev.Item)
	}
}

func (c *pendingActionCollector) observeToolEvent(ev aipapi.Event) {
	name, named := c.resolve(ev.ToolCallID, ev.ToolName)
	if !named {
		// A nameless finish or orphan arguments fragment that cannot be correlated
		// to a trusted ordinary start leaves the boundary unsafe.
		c.safe = false
		return
	}
	key := "tool_call:" + strings.TrimSpace(ev.ToolCallID)
	if _, known := c.byCall[key]; !known {
		if _, started := c.names[strings.TrimSpace(ev.ToolCallID)]; !started {
			c.safe = false
			return
		}
	}
	status := aipapi.ItemStatusInProgress
	if ev.Kind == aipapi.EventToolCallFinished {
		// A finished ordinary call is completed under the existing ActionFact
		// convention. It does not assert external execution and does not invent a
		// tool result.
		status = aipapi.ItemStatusCompleted
	}
	c.upsert(key, terminaldecision.ActionFact{
		CallID: boundedTerminalDecisionIdentifier(ev.ToolCallID),
		Kind:   aipapi.ItemKindToolCall,
		Status: status,
		Name:   name,
	})
}

func (c *pendingActionCollector) observeItem(item *aipapi.Item) {
	if item == nil {
		return
	}
	status := item.Status
	if status == "" {
		status = aipapi.ItemStatusCompleted
	}
	switch item.Kind {
	case aipapi.ItemKindMessage:
		id := boundedTerminalDecisionIdentifier(item.ID)
		if id == "" {
			return
		}
		c.upsert("message:"+item.ID, terminaldecision.ActionFact{ItemID: id, Kind: aipapi.ItemKindMessage, Status: status})
	case aipapi.ItemKindToolCall:
		if item.ToolCall == nil {
			c.safe = false
			return
		}
		c.observeItemTool("tool_call", aipapi.ItemKindToolCall, item.ToolCall.CallID, item.ToolCall.Name, status)
	case aipapi.ItemKindToolResult:
		if item.ToolResult == nil {
			c.safe = false
			return
		}
		c.observeItemTool("tool_result", aipapi.ItemKindToolResult, item.ToolResult.CallID, item.ToolResult.Name, status)
	}
}

// observeItemTool projects one item tool call or tool result. A tool RESULT keeps
// its own canonical kind and correlation identity: it is a completed outcome of a
// distinct boundary, not a restatement of the call that produced it. An incomplete
// item is still individually representable — its real status is projected and the
// boundary simply stays unresolved.
func (c *pendingActionCollector) observeItemTool(
	kindPrefix string,
	kind aipapi.ItemKind,
	ownCallID string,
	own string,
	status aipapi.ItemStatus,
) {
	name, named := c.resolve(ownCallID, own)
	if !named {
		c.safe = false
		return
	}
	callID := strings.TrimSpace(ownCallID)
	c.upsert(kindPrefix+":"+callID, terminaldecision.ActionFact{
		CallID: boundedTerminalDecisionIdentifier(callID),
		Kind:   kind,
		Status: status,
		Name:   name,
	})
}

// --- pipeline-held preparation, staged batch, and active drain -----------------

// pendingPublicationState is the single cohesive owner of this turn's private
// pending-completion publication facts. Keeping them in one value gives the
// publication one clear lifetime: prepared while held, reserved by the accepted
// normal terminal, staged and preflighted by it, activated only after the owning
// effects succeeded, drained to the client, and discarded whole on any losing,
// errored, continued, cancelled, closed, or preflight-failure path. It is guarded
// by the pipeline's existing response-state lock, so it adds no new lock and no
// I/O ever runs while that lock is held.
//
// Reservation is NOT acceptance. The prepared value is reserved by the accepted
// normal terminal and its complete canonical batch is staged and preflighted
// there, but the drain becomes deliverable only once the owning effects reported
// a real winner.
//
// The prepared value is RETAINED for that whole window instead of being handed
// away at the claim. It is the only owner of the frozen origin identity, so the
// drain, activation, withdrawal, and completion seams all revalidate the same
// attempt pointer and the same frozen B-leg identity against it. `reserved` is
// what prevents a second claim, not a nulled pointer.
type pendingPublicationState struct {
	// prepared is the evaluated candidate owned by its frozen origin attempt. It
	// stays retained from preparation until the physical finish delivery or a
	// withdrawal disposes it.
	prepared *pendingCompletion
	// reserved records that the accepted normal terminal claimed the prepared
	// value for staging. A second terminal call, a competing request command, and
	// the generic losing GateReplacement effect exception all find nothing left to
	// stage, and a repeated finish route re-prepares nothing.
	reserved bool
	// staged is the preflighted canonical batch awaiting activation. Nothing has
	// been released, remembered, observed, or queued for it yet.
	staged []aipapi.Event
	// stagedCustomer is the privately previewed customer usage event for the
	// staged batch, or the zero event when the real preparation produced none. The
	// winning request owner settles from this exact quantity, before the batch is
	// ever released.
	stagedCustomer aipapi.Event
	// customerAt is the EXPLICIT position of that customer entry, and the remaining
	// cursor the drain advances. It is supplied by the response owner while it
	// builds the batch, never discovered afterwards by comparing event values: an
	// ordinary provider or gate usage delta can carry exactly the same kind and
	// the same three token counters as the previewed customer quantity, so equal
	// values are a coincidence and not event identity. Recognition ends at the
	// customer entry, so an equal-counter ordinary entry after it is never emitted.
	customerAt pendingCustomerPosition
	// endALeg records the A-leg end intent the owning finish route carries, so the
	// actual finish delivery converges the same successful bookkeeping.
	endALeg bool
	// activated marks that the drain is deliverable. A staged-but-unactivated
	// batch is discarded instead of released.
	activated bool
	// deferredObs records that the attempt-owned final-stream observer Finish was
	// deferred to this publication. A withdrawal must then close it conservatively,
	// exactly once, and a full delivery must close it successfully, exactly once.
	deferredObs bool
	// active is the activated drain queue.
	active []aipapi.Event
}

// pendingCustomerPosition is the EXPLICIT position of the privately previewed
// customer usage entry inside the staged canonical batch, together with the
// remaining cursor the physical drain advances.
//
// The position is a fact the response owner knows while it appends the entry, not
// something the drain infers later from event values. Ordinary provider or gate
// usage deltas can carry the same kind and the same three token counters as the
// refreshed customer quantity, so value comparison cannot identify the entry: it
// recognizes a coincidence. The valid bit is separate from the index so the zero
// value of this type, and the zero value of the state that holds it, name NO
// customer entry instead of accidentally naming position zero.
type pendingCustomerPosition struct {
	// index is the entry's position in the staged batch while it is being built,
	// and the REMAINING number of entries ahead of it while the drain advances.
	index int
	// valid reports that a customer entry really exists at that position.
	valid bool
}

// pendingCustomerAt names the position of a real customer entry.
func pendingCustomerAt(index int) pendingCustomerPosition {
	return pendingCustomerPosition{index: index, valid: true}
}

// pendingCustomerAbsent is the explicit "this batch holds no customer entry"
// marker every caller without a previewed customer quantity passes.
var pendingCustomerAbsent = pendingCustomerPosition{}

// pendingState returns the live publication state. The zero value is already a
// correct "no private publication" state, so a directly constructed pipeline needs
// no construction ceremony and every seam is total. Callers must hold
// [responsePipeline.eventsMu].
func (p *responsePipeline) pendingState() *pendingPublicationState {
	if p == nil {
		return nil
	}
	return &p.pending
}

// customerUsageReconstructorLocked reads the bound customer usage callback.
// Callers must hold [responsePipeline.eventsMu].
func (p *responsePipeline) customerUsageReconstructorLocked() func(context.Context, string, []aipapi.Event) aipapi.Event {
	return p.customerUsageFn
}

func (p *responsePipeline) pendingPreparedSnapshot() *pendingCompletion {
	if p == nil {
		return nil
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.pending.prepared
}

// pendingPreparedFor returns the prepared candidate only while attempt is still
// the attempt that prepared it, so a B2 finish can never publish B1's lexical
// candidate.
//
// A foreign read is a READ, not a retirement: it reports nothing and clears
// nothing. Another live candidate that already owns a reservation is never erased
// by a seam that merely looked at it. Genuine retirement — a Close, an attempt
// loss, a replacement, a continuation, or a surfaced failure — still disposes the
// whole retained state through the existing unconditional cleanup owners.
func (p *responsePipeline) pendingPreparedFor(attempt *attemptSession) *pendingCompletion {
	if p == nil {
		return nil
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if p.pending.prepared == nil {
		return nil
	}
	if pendingOriginOwns(p.pending.prepared, attempt) {
		return p.pending.prepared
	}
	return nil
}

// pendingOriginOwns is PUBLICATION ELIGIBILITY: the attempt pointer itself plus
// the B-leg identity the prepared value was copied under must both still match,
// and the frozen result must still be non-empty. A value is only publishable by
// the attempt that produced it, under the identity it was produced under.
func pendingOriginOwns(prepared *pendingCompletion, attempt *attemptSession) bool {
	if prepared == nil || attempt == nil || prepared.origin == nil || prepared.origin != attempt {
		return false
	}
	return prepared.text.blegID == attempt.bleg.BLegID &&
		prepared.text.seq == uint32(attempt.bleg.Seq) &&
		prepared.text.result != ""
}

// pendingOriginRetains is CLEANUP OWNERSHIP, deliberately separate from
// publication eligibility above: it compares the prepared value's ORIGINAL origin
// pointer alone.
//
// The split is what makes a same-owner withdrawal permanent and truthful. A frozen
// B-leg identity that no longer matches must still let the OWNER of the retained
// candidate dispose it, so a withdrawn publication clears its queue and releases
// its deferred observer instead of stranding them forever, while a read that only
// checks eligibility can never report success. Pointer identity still cannot name
// another candidate, so a foreign caller can never dispose somebody else's
// publication, and restoring the identity afterwards can never resurrect a value
// this rule already removed.
func pendingOriginRetains(prepared *pendingCompletion, origin *attemptSession) bool {
	return prepared != nil && origin != nil && prepared.origin != nil && prepared.origin == origin
}

// reset makes this state own exactly one freshly prepared candidate. It REPLACES
// the whole value rather than clearing a previous candidate field by field, so a
// second preparation can never inherit a leftover reservation, staged batch,
// explicit customer cursor, queue, or deferred observer.
func (s *pendingPublicationState) reset(prepared *pendingCompletion) {
	*s = pendingPublicationState{prepared: prepared}
}

// clear drops every retained private publication fact.
func (s *pendingPublicationState) clear() {
	*s = pendingPublicationState{}
}

// setPendingPrepared installs a fresh candidate as this response's whole private
// publication state. Callers must hold [responsePipeline.eventsMu].
func (p *responsePipeline) setPendingPrepared(prepared *pendingCompletion) {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.pendingState().reset(prepared)
	p.eventsMu.Unlock()
}

// pendingPublicationAccepted reports that this turn already reserved a private
// pending completion publication for its accepted normal terminal. The four finish
// routes read this one fact, so they never stage it twice and a repeated terminal
// call never republishes the result.
func (p *responsePipeline) pendingPublicationAccepted() bool {
	if p == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.pending.reserved
}

// pendingPublicationActive reports that the private drain is deliverable. It is
// only true after the owning effects succeeded and the live origin/publication
// fence still permits release.
func (p *responsePipeline) pendingPublicationActive() bool {
	if p == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.pending.activated
}

// takePendingPublication reserves the one publication of this turn's prepared
// value. Every later terminal call finds nothing to publish, so a repeated
// terminal call, a competing request command, and the generic losing
// GateReplacement effect exception can never publish the result twice.
//
// The candidate itself is NOT handed away: it stays owned by its frozen origin so
// the drain, activation, withdrawal, and completion seams can revalidate the same
// identity. `reserved` is the once-only guard.
func (p *responsePipeline) takePendingPublication() (*pendingCompletion, bool) {
	if p == nil {
		return nil, false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if p.pending.reserved || p.pending.prepared == nil {
		return nil, false
	}
	p.pending.reserved = true
	return p.pending.prepared, true
}

// pendingPublicationOriginSnapshot returns the frozen origin of the currently
// retained private publication, or nil when this response owns none. It is the
// ORIGINAL attempt, so a replacement never reinterprets an older preparation with
// fresh state, and the live slot is never manufactured into an old origin.
func (p *responsePipeline) pendingPublicationOriginSnapshot() *attemptSession {
	if p == nil {
		return nil
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if p.pending.prepared == nil {
		return nil
	}
	return p.pending.prepared.origin
}

// pendingPublicationExpectation returns the retained candidate together with its
// frozen origin as ONE pair read under the response-state lock, so a physical
// drain is always pinned to the exact identity the publication started with
// instead of a candidate and an origin sampled at two different instants.
func (p *responsePipeline) pendingPublicationExpectation() (*pendingCompletion, *attemptSession) {
	if p == nil {
		return nil, nil
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if p.pending.prepared == nil {
		return nil, nil
	}
	return p.pending.prepared, p.pending.prepared.origin
}

// activateReservedPendingPublication moves the staged batch into the deliverable
// drain, but only for the EXACT expected candidate and its EXACT expected frozen
// origin, and only while that same candidate is still reserved, still carries the
// deferred observer finish, is still staged, and is not activated yet.
//
// Every one of those facts is re-read under [responsePipeline.eventsMu] here, so a
// reservation withdrawn, replaced, or already delivered while the owning effects
// ran cannot be made deliverable by a late activation. The report is the whole
// answer: a false report is a real withdrawal the caller must propagate, never an
// ignored no-op.
func (p *responsePipeline) activateReservedPendingPublication(
	expected *pendingCompletion,
	origin *attemptSession,
) bool {
	if p == nil || expected == nil || origin == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	state := p.pendingState()
	if state == nil || state.prepared != expected {
		return false
	}
	// The same candidate is only ACTIVATABLE by the attempt it was retained under,
	// and only while that attempt still carries the B-leg identity the candidate
	// was copied under. A foreign or a mismatching attempt is refused here even
	// when every live fence is open.
	if !pendingOriginRetains(expected, origin) || !pendingOriginOwns(expected, origin) {
		return false
	}
	if !state.reserved || !state.deferredObs || len(state.staged) == 0 || state.activated {
		return false
	}
	state.active = state.staged
	state.staged = nil
	state.activated = true
	return true
}

// stagedCustomerUsage returns the privately previewed customer usage event the
// winning request owner must settle from before activation. It is the same event
// the drain releases, so settlement and the client cannot disagree.
func (p *responsePipeline) stagedCustomerUsage() (aipapi.Event, bool) {
	if p == nil {
		return aipapi.Event{}, false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if p.pending.stagedCustomer.Kind == "" {
		return aipapi.Event{}, false
	}
	return p.pending.stagedCustomer, true
}

// pendingPublicationEndALeg reports the A-leg end intent recorded by the owning
// finish route for the activated publication.
func (p *responsePipeline) pendingPublicationEndALeg() bool {
	if p == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return p.pending.endALeg
}

// pendingPublicationHead returns the next event of the active drain without
// consuming it. Every effect for that event already ran exactly once while the
// batch was staged and released, so the entry never re-runs a hook, a gate, the
// recorder, or the final observer.
func (p *responsePipeline) pendingPublicationHead() (aipapi.Event, bool) {
	if p == nil {
		return aipapi.Event{}, false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if !p.pending.activated || len(p.pending.active) == 0 {
		return aipapi.Event{}, false
	}
	return p.pending.active[0], true
}

// pendingPublicationNextUsage reports whether the head of the active drain IS the
// refreshed customer usage event, so the physical delivery runs the usage emission
// exactly once at its canonical position.
//
// The decision comes from the EXPLICIT position the response owner recorded when
// it staged the batch, advanced once per popped entry. It never compares event
// kind, counters, RawUsageJSON, or whole values: an ordinary backend usage delta
// a gate or the provider contributed can carry exactly the same kind and the same
// three token counters as the previewed customer quantity, so value equality is a
// coincidence rather than event identity. Lifecycle and content entries are
// therefore skipped without emitting, and recognition ends at the customer entry
// itself, so an equal-counter ordinary usage delta before OR after it never emits.
func (p *responsePipeline) pendingPublicationNextUsage() bool {
	if p == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if !p.pending.activated || len(p.pending.active) == 0 {
		return false
	}
	return p.pending.customerAt.valid && p.pending.customerAt.index == 0
}

// popPendingCompletionRelease consumes the next event of the active drain. The
// caller has already revalidated the live fences through
// [pendingPublicationFenceOpen].
//
// EVERY successful pop advances the explicit customer cursor by exactly one
// entry, and popping the customer entry itself ends recognition, so no later
// entry can be reported as the customer usage again.
func (p *responsePipeline) popPendingCompletionRelease() (aipapi.Event, bool) {
	if p == nil {
		return aipapi.Event{}, false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	state := p.pendingState()
	if state == nil || !state.activated || len(state.active) == 0 {
		return aipapi.Event{}, false
	}
	ev := state.active[0]
	state.active[0] = aipapi.Event{}
	state.active = state.active[1:]
	if len(state.active) == 0 {
		state.active = nil
	}
	if state.customerAt.valid {
		if state.customerAt.index == 0 {
			// The customer entry is delivered here, so recognition ends with it.
			state.customerAt = pendingCustomerAbsent
		} else {
			state.customerAt.index--
		}
	}
	return ev, true
}

// completePendingPublication disposes the whole retained private publication once
// its accepted finish has been physically delivered, and reports whether this call
// was the one that owned it. The winner is the ORIGINAL origin attempt, so a
// competing attempt can never clear a live candidate it does not own.
func (p *responsePipeline) completePendingPublication(origin *attemptSession) bool {
	if p == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if !pendingOriginRetains(p.pending.prepared, origin) {
		return false
	}
	p.clearPendingLocked()
	return true
}

// clearPendingLocked drops every retained private publication fact. Callers must
// hold [responsePipeline.eventsMu].
func (p *responsePipeline) clearPendingLocked() {
	p.pendingState().clear()
}

// discardPendingCompletion drops every retained private publication fact. It runs
// on the continued, replaced, losing, errored, cancelled, closed, and preflight
// failure paths so no lexical candidate, staged batch, or queued event survives the
// attempt that owned it.
func (p *responsePipeline) discardPendingCompletion() {
	if p == nil {
		return
	}
	p.eventsMu.Lock()
	p.clearPendingLocked()
	p.eventsMu.Unlock()
}

// pendingPublicationReservation is the identity of one reserved publication. It is
// the prepared value itself, so no counter, goroutine, or stored context is needed
// to prove that a returning preflight still owns the reservation it started with.
type pendingPublicationReservation struct {
	prepared *pendingCompletion
	endALeg  bool
}

// reservePendingPublication records the deferred observer finish and the A-leg end
// intent for the publication [takePendingPublication] already claimed, BEFORE any
// external recorder or observer work.
//
// Recording the reservation first is what makes an external preflight race safe: a
// Close, cancellation, or replaced attempt that wins while a blocked preflight is
// still running finds a live reservation whose deferred observer finish it can
// perform exactly once, so the returning callback cannot recreate private state
// behind it. It performs no I/O and holds no lock across an external call.
//
// The identity is the prepared value itself, so a returning callback can prove it
// still owns the reservation it started with: a withdrawal cleared `prepared`, and
// only a fresh preparation can install a different candidate, which this call
// refuses.
func (p *responsePipeline) reservePendingPublication(
	prepared *pendingCompletion,
	endALeg bool,
) (pendingPublicationReservation, bool) {
	if p == nil || prepared == nil {
		return pendingPublicationReservation{}, false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if !p.pending.reserved || p.pending.prepared != prepared {
		return pendingPublicationReservation{}, false
	}
	p.pending.deferredObs = true
	p.pending.endALeg = endALeg
	return pendingPublicationReservation{prepared: prepared, endALeg: endALeg}, true
}

// stageReservedPublication installs the complete preflighted canonical batch, but
// only for the exact reservation that started the preflight and only while that
// reservation is still live under the response-state lock.
//
// This is the second half of the external-preflight fence: a Close, cancellation,
// or competing owner that withdrew the state while the recorder or observer ran
// must never be undone by the returning callback, which would otherwise recreate
// private state through this allocating step.
//
// customerAt is the EXPLICIT position of the customer usage entry inside batch,
// and pendingCustomerAbsent when the real preparation previewed none. The caller
// owns that fact; this seam never re-derives it from event values, so an
// ordinary usage delta sharing the customer counters can never be mistaken for the
// customer entry.
func (p *responsePipeline) stageReservedPublication(
	reservation pendingPublicationReservation,
	origin *attemptSession,
	batch []aipapi.Event,
	customer aipapi.Event,
	customerAt pendingCustomerPosition,
) bool {
	if p == nil || reservation.prepared == nil {
		return false
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	if p.pending.prepared != reservation.prepared || !p.pending.reserved {
		return false
	}
	if !p.pending.deferredObs || !pendingOriginOwns(reservation.prepared, origin) {
		return false
	}
	// A position that does not name a real entry inside the batch it is installed
	// for is not a position at all, so it collapses to the explicit absent marker
	// instead of naming an entry the drain would never deliver.
	if !customerAt.valid || customerAt.index < 0 || customerAt.index >= len(batch) {
		customerAt = pendingCustomerAbsent
	}
	p.pending.staged = slices.Clone(batch)
	p.pending.stagedCustomer = customer
	p.pending.customerAt = customerAt
	p.pending.endALeg = reservation.endALeg
	p.pending.activated = false
	p.pending.active = nil
	return true
}

// withdrawPendingCompletion is the live-fence withdrawal for a physical drain. It
// discards every prepared, staged, and undelivered fact, finishes a deferred
// final-stream observer conservatively exactly once under the bounded cleanup
// budget, and never reopens it. A later Recv therefore cannot release the
// remainder.
func (p *responsePipeline) withdrawPendingCompletion(origin *attemptSession) {
	if p == nil {
		return
	}
	if p.discardPendingPublication(origin) {
		p.finishPendingObservationConservatively(origin)
	}
}

// discardPendingPublication clears the private publication state OWNED BY ORIGIN
// and reports whether that state held anything that has to be finished
// conservatively afterwards.
//
// Ownership is the whole point: state belonging to another live candidate is left
// untouched, and an origin that owns nothing can never be reported as the holder of
// another candidate's deferred observer. A nil origin owns nothing.
//
// The ownership test is the ORIGINAL origin POINTER alone, never publication
// eligibility: a same-owner candidate whose frozen B-leg identity no longer matches
// must still be disposable, so a withdrawal is permanent and releases its queue and
// its observer instead of stranding them. A foreign origin still matches no pointer
// and therefore still disposes nothing.
//
// The report is deliberately not limited to the deferred-observer flag. A merely
// prepared candidate is dropped here too, and the abandoning caller is still the one
// that must close the attempt-owned observer conservatively: preparation never
// released anything, so a later abandoning terminal has no other owner for it.
func (p *responsePipeline) discardPendingPublication(origin *attemptSession) (discarded bool) {
	if p == nil {
		return false
	}
	p.eventsMu.Lock()
	if pendingOriginRetains(p.pending.prepared, origin) {
		p.clearPendingLocked()
		discarded = true
	}
	p.eventsMu.Unlock()
	return discarded
}

// abandonOwnedPendingPublication discards the publication owned by origin and closes
// its final-stream observer conservatively, exactly once. It is the single cleanup
// entry for a withdrawal during staging, where the caller already owns a bounded
// cleanup context and must not open a second budget.
//
// A candidate that was merely prepared, with no reservation recorded, is dropped
// too: preparation alone published nothing, and the abandoning caller is the one
// that must still close the attempt-owned observer conservatively so no session is
// left open. A candidate this origin does not own is left completely alone.
func (p *responsePipeline) abandonOwnedPendingPublication(ctx context.Context, origin *attemptSession) {
	if !p.discardPendingPublication(origin) {
		return
	}
	if origin != nil && origin.finalStreamObs != nil {
		p.finishFinalStreamObservation(ctx, origin, response.OutcomeFailed)
	}
}

// --- publication ---------------------------------------------------------------

// errPendingPublicationWithdrawn reports that a private pending completion was
// withdrawn while its external preflight was running: a Close, a client
// cancellation, an authoritative A-leg cause, or a replaced attempt won, so no
// batch is staged, no customer authority is settled, and no billing call is handed
// off for a publication that will never be released.
var errPendingPublicationWithdrawn = errors.New("runtime: pending completion publication withdrawn before staging")

// pendingPublication is the narrow private value the accepted request-terminal
// owner hands to the response publication. It carries no terminal authority: the
// caller already owns it.
type pendingPublication struct {
	prepared    *pendingCompletion
	usage       aipapi.Event
	usageOK     bool
	facts       recvTurnFacts
	recovery    *recoveryController
	callerCtx   context.Context
	publishable func() bool
	endALeg     bool
}

// stagePendingCompletion performs the accepted publication's staging half.
//
// It runs inside the winning effects of an accepted normal terminal, BEFORE
// customer settlement and billing handoff. ownerCtx is the terminal owner's
// bounded cleanup context: the request owner supplies its existing one and the
// thinker supplies the existing bounded cleanup budget, so recorder, observer,
// and PTC publication are never unbounded. pub.callerCtx is the original live
// receive context, captured on purpose: the fence re-checks it, plus the shared
// A-leg and the attempt publication window, so a client cancellation or an
// authoritative Close that already won is never masked by the detached context.
//
// Staging builds the complete correctly ordered canonical batch, then prefights
// it as a whole — mandatory recording first, then the existing fail-closed final
// observer — before any PTC, remember, accumulator, usage emission, or queue
// activation. Nothing is released and no release-tail fact exists yet, so a
// rejected terminal effect cannot leave phantom release evidence behind.
//
// A lost, continued, errored, cancelled, or closed fence is a real WITHDRAWAL of
// an expected publication and is reported as one, so customer settlement and
// billing handoff stop. Only a candidate this caller does not own is a silent
// no-op: a foreign candidate is neither staged nor disposed, so one attempt can
// never withdraw, publish, or erase another attempt's result.
func (t *turnTerminal) stagePendingCompletion(
	ctx context.Context,
	attempt *attemptSession,
	p *responsePipeline,
	request requestTerminalFacts,
	pub pendingPublication,
) error {
	if t == nil || p == nil || attempt == nil || pub.prepared == nil {
		return nil
	}
	if !pendingOriginRetains(pub.prepared, attempt) {
		// The candidate belongs to another attempt, so this caller owns nothing to
		// dispose: a previous leg's result is never published and never erased by a
		// seam that does not own it.
		return nil
	}
	if !pendingPublicationFenceOpen(pub.callerCtx, t, pub.publishable) {
		// The expected publication is lost before it is reserved, so nothing is
		// staged and no state is recreated behind a withdrawal that already won.
		// Only the expected owner's state is disposed, and its observer is closed
		// conservatively exactly once under the caller's existing bounded budget.
		p.abandonOwnedPendingPublication(ctx, attempt)
		return errPendingPublicationWithdrawn
	}
	// Reserve the publication and its deferred observer finish BEFORE any external
	// work. I/O stays unlocked, and a Close or cancellation that wins while the
	// recorder or the fail-closed observer is blocked can still clear the
	// reservation and close the observer exactly once.
	reservation, reserved := p.reservePendingPublication(pub.prepared, pub.endALeg)
	if !reserved {
		// The expected publication lost its reservation between the fence and the
		// reserve, so this is a withdrawal to report, never a silent success that
		// would let settlement and handoff continue for a batch that will not be
		// released. The reservation is already gone, so only the observer is closed.
		p.finishPendingObservationConservatively(attempt)
		return errPendingPublicationWithdrawn
	}
	// Build the complete canonical batch BEFORE recording anything: the ordinary
	// and result lifecycle/content first, the refreshed customer usage when the
	// real preparation produced usage, then the actual accepted finish.
	batch := make([]aipapi.Event, 0, len(pub.prepared.events)+2)
	batch = append(batch, pub.prepared.events...)
	var customer aipapi.Event
	// The customer position is a fact THIS owner knows while it appends the entry,
	// so it is read from the batch length immediately BEFORE the append. A real
	// preparation that produced no customer usage leaves the explicit absent
	// marker, and nothing downstream ever rediscovers the position by comparing
	// event kind, counters, RawUsageJSON, or whole values.
	customerAt := pendingCustomerAbsent
	if pub.usageOK {
		customer = p.previewCustomerUsage(ctx, pub.prepared.events, request)
		if customer.Kind == "" {
			// Preparation already supplied genuine usage. A failed private preview
			// must not erase it or import operator/provider authority into the client.
			customer = customerPlaneUsageEvent(pub.usage)
		}
		if customer.Kind != "" {
			customerAt = pendingCustomerAt(len(batch))
			batch = append(batch, customer)
		}
	}
	batch = append(batch, pub.prepared.finish)

	// Mandatory recorder preflight runs for the ENTIRE batch, in canonical order,
	// before any PTC, remember, accumulator, usage emission, or queue activation.
	// A mandatory failure on any event, including the finish, leaves no client
	// result at all. Partial durable per-event records may already exist, exactly
	// as for any other multi-event release; nothing falls through.
	committed := t.committed()
	for _, ev := range batch {
		recording := p.recordClientFacing(ctx, pub.facts, attempt, ev, committed)
		if recording.mandatory() {
			p.abandonOwnedPendingPublication(ctx, attempt)
			return recording.err
		}
	}
	// Preflight the existing fail-closed final observer over the same whole batch,
	// in the same order, so a rejected observer cannot leave a partial client
	// result either. Success is deliberately NOT marked here: the observer is
	// finished once, conservatively or successfully, at the real delivery
	// boundary.
	for _, ev := range batch {
		if err := p.preflightFinalStreamObservation(ctx, attempt, ev, committed); err != nil {
			p.abandonOwnedPendingPublication(ctx, attempt)
			return err
		}
	}
	// The external calls above can each be overtaken by a Close, a client
	// cancellation, an authoritative A-leg cause, or a replaced attempt. Re-check
	// the live fence and install the batch only for the same still-live
	// reservation, so a withdrawal that won while the preflight ran is never undone
	// by this returning callback. Success is deliberately not reported afterwards:
	// the owning effects must not settle customer authority or hand off billing for
	// a batch that will never be released.
	if !pendingPublicationFenceOpen(pub.callerCtx, t, pub.publishable) ||
		!p.stageReservedPublication(reservation, attempt, batch, customer, customerAt) {
		p.abandonOwnedPendingPublication(ctx, attempt)
		return errPendingPublicationWithdrawn
	}
	return nil
}

// previewCustomerUsage privately previews the customer-visible usage quantity for
// one accepted candidate without touching live evidence.
//
// It replays consistent COPIES of the already-released customer content deltas and
// of the candidate's own content deltas into a FRESH accumulator, so no builder,
// mutex, or live accumulator is copied or mutated. The fresh accumulator is the
// same existing customer projection used for ordinary releases, which counts text,
// reasoning, and tool-argument DELTAS only: a candidate Item is a canonical drain
// event and never a second copy of its associated text deltas.
//
// The quantity is computed through the existing customer reconstruction seam with
// the same call and frontend-ingress projection as ordinary settlement. Operator,
// provider, and B-leg authority evidence is never imported here, and no token
// accounting, backend observation, money rating, or ledger write runs a second time.
func (p *responsePipeline) previewCustomerUsage(
	ctx context.Context,
	candidate []aipapi.Event,
	request requestTerminalFacts,
) aipapi.Event {
	if p == nil {
		return aipapi.Event{}
	}
	preview := newCustomerEvidenceAccumulator()
	for _, ev := range p.contentEvents() {
		preview.ObserveReleased(ev)
	}
	for _, ev := range candidate {
		preview.ObserveReleased(ev)
	}
	text, _, _, _ := preview.Snapshot()
	events := preview.contentEvents()

	p.eventsMu.Lock()
	reconstructor := p.customerUsageReconstructorLocked()
	p.eventsMu.Unlock()
	if reconstructor != nil {
		if ev := reconstructor(ctx, text, events); ev.Kind != "" {
			return customerPlaneUsageEvent(ev)
		}
	}
	if p.streamUsage != nil {
		result, err := p.streamUsage.Reconstruct(ctx, accountingstream.Input{
			Call: request.call, OutputText: text, Events: events,
		})
		if err != nil && p.log != nil {
			p.log.DebugContext(ctx, "pending customer stream usage reconstruction", "error", err)
		}
		if len(result.Events) > 0 {
			return applyFrontendIngressInput(request.metering,
				customerPlaneUsageEvent(mergeUsageEventsForClient(result.Events, true)))
		}
	}
	return aipapi.Event{}
}

// pendingPublicationFenceOpen re-checks the live caller, the shared A-leg, the
// request terminal, and the attempt publication window before staging, activating,
// or delivering any pending result event. It performs no I/O and holds no lock
// across an external call.
//
// An ordinary request owner that already reached Released is EXPECTED here and is
// not a withdrawal fence; only caller cancellation, an authoritative A-leg cause,
// and a closed publication window withdraw a prepared result.
func pendingPublicationFenceOpen(callerCtx context.Context, t *turnTerminal, publishable func() bool) bool {
	if callerCtx != nil && callerCtx.Err() != nil {
		return false
	}
	if t != nil {
		if t.hasALeg() && t.aLegErr() != nil {
			return false
		}
	}
	if publishable != nil {
		return publishable()
	}
	return true
}

// pendingReleasePartMeta builds the release-side part metadata from the same frozen
// attempt identity the recv path already uses. It deliberately takes no terminal
// snapshot: it runs once per delivered pending event, and partMeta reads the identical
// trace id, A-leg id, B-leg id and attempt sequence straight from the frozen facts
// without reading the frozen request views.
func pendingReleasePartMeta(facts recvTurnFacts, attempt *attemptSession) sdk.PartMeta {
	return facts.partMeta(attempt.bleg, attempt.cand)
}
