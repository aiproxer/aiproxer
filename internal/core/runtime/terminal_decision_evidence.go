package runtime

import (
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// continuationIntentFacts is the immutable, bounded carry-forward of an
// accepted continuation intent. References stay opaque to core; set preserves
// the distinction between an explicit empty control reference and legacy
// requests that never entered the continuation path.
type continuationIntentFacts struct {
	trajectoryRef string
	controlRef    string
	attempt       uint8
	set           bool
}

// derivedContinuationIntentFacts is the bounded lineage an attempt reports when
// no continuation intent was ever accepted: the request's own trajectory, the
// attempt's own leg as the control reference, and its own sequence. The
// terminal-decision evidence projection uses it, and the preserve-lineage
// continuation policy stores it so B2 inherits exactly the lineage B1 had.
func derivedContinuationIntentFacts(call aipapi.Call, traceID string, attempt *attemptSession) continuationIntentFacts {
	trajectoryRef := strings.TrimSpace(call.ID)
	if trajectoryRef == "" {
		trajectoryRef = strings.TrimSpace(call.PreviousResponseID)
	}
	if trajectoryRef == "" {
		trajectoryRef = strings.TrimSpace(traceID)
	}
	progressRef := ""
	if attempt != nil {
		progressRef = strings.TrimSpace(attempt.bleg.BLegID)
	}
	if progressRef == "" {
		progressRef = trajectoryRef
	}
	attemptNumber := uint8(0)
	if attempt != nil && attempt.bleg.Seq > 0 {
		if attempt.bleg.Seq >= 1<<8 {
			attemptNumber = ^uint8(0)
		} else {
			attemptNumber = uint8(attempt.bleg.Seq)
		}
	}
	return continuationIntentFacts{
		trajectoryRef: trajectoryRef,
		controlRef:    progressRef,
		attempt:       attemptNumber,
		set:           true,
	}
}

// projectTerminalDecisionEvidence copies the small canonical view that a
// terminal-decision provider may inspect. It deliberately reads request and
// response facts only; it has no policy, provider, or terminal ownership.
//
// The two completion flags are independent canonical facts. The expectation is
// the attempt's frozen activation, so it survives disposal; the observation is
// the attempt's live private outcome, so it disappears with lifecycle ownership.
// The observation is the OR of the existing native completion fact and a valid
// proxy-owned control completion, which is exactly what an ordinary
// harness-owned completed execution already means. Neither flag is derived from
// the other, and neither carries a control name, call identity, result text, or
// argument byte.
func projectTerminalDecisionEvidence(request requestTerminalFacts, attempt *attemptSession, p *responsePipeline) terminaldecision.Evidence {
	items := aipapi.NormalizedItems(request.call)
	expectedCompletion, observedProxyCompletion := attempt.controlCompletionFacts()
	evidence := terminaldecision.Evidence{
		Objective:     boundedTerminalDecisionText(terminalDecisionObjective(items)),
		RecentText:    boundedTerminalDecisionText(terminalDecisionRecentText(items)),
		CandidateText: boundedTerminalDecisionText(terminalDecisionCandidateText(p)),
		ExplicitCompletion: aipapi.HasExplicitCompletion(
			terminalDecisionCompletionItems(items, p),
		) || observedProxyCompletion,
		ExplicitCompletionExpected: expectedCompletion,
	}

	trajectoryRef := strings.TrimSpace(request.continuationIntent.trajectoryRef)
	progressRef := strings.TrimSpace(request.continuationIntent.controlRef)
	attemptNumber := request.continuationIntent.attempt
	if !request.continuationIntent.set {
		derived := derivedContinuationIntentFacts(request.call, request.traceID, attempt)
		trajectoryRef = strings.TrimSpace(derived.trajectoryRef)
		progressRef = strings.TrimSpace(derived.controlRef)
		attemptNumber = derived.attempt
	}
	evidence.Lineage = terminaldecision.EvidenceLineage{
		TrajectoryRef: boundedTerminalDecisionIdentifier(trajectoryRef),
		ParentRef:     boundedTerminalDecisionIdentifier(request.call.PreviousResponseID),
		ProgressRef:   boundedTerminalDecisionIdentifier(progressRef),
		Attempt:       attemptNumber,
	}

	actions := terminalDecisionActions(items, p, attempt)
	evidence.ActionCount = uint8(len(actions))
	copy(evidence.Actions[:], actions)
	return evidence
}

func terminalDecisionObjective(items []aipapi.Item) string {
	var fallback string
	for _, item := range items {
		text := terminalDecisionItemText(item)
		if text == "" {
			continue
		}
		switch item.Role {
		case aipapi.RoleUser:
			fallback = text
		case aipapi.RoleDeveloper, aipapi.RoleSystem:
			if fallback == "" {
				fallback = text
			}
		}
	}
	return fallback
}

func terminalDecisionRecentText(items []aipapi.Item) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if text := terminalDecisionItemText(item); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func terminalDecisionCandidateText(p *responsePipeline) string {
	if p == nil {
		return ""
	}
	// Progress detection compares one attempt's own answer. The cumulative
	// released text would make identical answers on successive attempts look
	// like fresh evidence and reset the no-progress breaker every leg.
	return p.attemptLocalOutputText()
}

func terminalDecisionItemText(item aipapi.Item) string {
	if item.Kind != aipapi.ItemKindMessage {
		return ""
	}
	var parts []string
	for _, part := range item.Content {
		if part.Kind == aipapi.ContentPartText && strings.TrimSpace(part.Text) != "" {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func terminalDecisionCompletionItems(items []aipapi.Item, p *responsePipeline) []aipapi.Item {
	if p == nil {
		return items
	}
	out := append([]aipapi.Item(nil), items...)
	for _, event := range p.seenEventsCopy() {
		if event.Kind == aipapi.EventItem && event.Item != nil {
			out = append(out, *event.Item)
		}
	}
	return out
}

// terminalDecisionActions projects the bounded ordinary action history of this
// logical response.
//
// When the accepted-terminal publication path is holding a private candidate, its
// ordinary tool boundaries are not released yet and therefore are not in the
// released event history. Those held facts are projected first, inside the same
// fixed action capacity, so a current unresolved ordinary boundary is visible to
// the deciding provider instead of being hidden behind older facts. Everything
// else keeps its existing trusted sources and ordering.
func terminalDecisionActions(items []aipapi.Item, p *responsePipeline, attempt *attemptSession) []terminaldecision.ActionFact {
	held := p.pendingActionSupplement(attempt)
	if len(held) > 0 {
		return terminalDecisionMergeHeldActions(held, terminalDecisionReleasedActions(items, p))
	}
	return terminalDecisionReleasedActions(items, p)
}

// pendingActionSupplement returns the individually representable held ordinary
// action facts of a private publication candidate owned by attempt.
//
// Individually representable facts stay projected even when a DIFFERENT held action
// is orphaned, ambiguous, or over capacity: the first deciding provider must still
// see the current ordinary boundary. Ambiguity and truncation fail closed on result
// eligibility at the publication boundary instead, so this projection never invents
// a fact it could not represent.
func (p *responsePipeline) pendingActionSupplement(attempt *attemptSession) []terminaldecision.ActionFact {
	prepared := p.pendingPreparedFor(attempt)
	if prepared == nil {
		return nil
	}
	return prepared.supplemental
}

// terminalDecisionMergeHeldActions puts the held ordinary facts first, then fills
// the remaining existing capacity with the already-known history. Correlation is
// preserved: a held boundary that restates an earlier identity keeps the trusted
// name and current status; historical completion cannot resolve a current start.
func terminalDecisionMergeHeldActions(held, known []terminaldecision.ActionFact) []terminaldecision.ActionFact {
	out := make([]terminaldecision.ActionFact, 0, terminaldecision.MaxEvidenceActions)
	index := make(map[string]int, terminaldecision.MaxEvidenceActions)
	add := func(action terminaldecision.ActionFact) {
		if action.ItemID == "" && action.CallID == "" {
			return
		}
		if action.Kind != aipapi.ItemKindMessage && (action.CallID == "" || strings.TrimSpace(action.Name) == "") {
			return
		}
		key := terminalDecisionActionKey(action)
		if at, ok := index[key]; ok {
			if terminalDecisionActionRank(action.Status) > terminalDecisionActionRank(out[at].Status) {
				out[at].Status = action.Status
			}
			if out[at].Name == "" {
				out[at].Name = action.Name
			}
			return
		}
		if len(out) >= terminaldecision.MaxEvidenceActions {
			return
		}
		index[key] = len(out)
		out = append(out, action)
	}
	for _, action := range held {
		add(action)
	}
	for _, action := range known {
		// Current held status takes precedence over earlier completed history.
		if _, current := index[terminalDecisionActionKey(action)]; current {
			continue
		}
		add(action)
	}
	return out
}

func terminalDecisionActionKey(action terminaldecision.ActionFact) string {
	if action.Kind == aipapi.ItemKindMessage {
		return "message:" + action.ItemID
	}
	if action.Kind == aipapi.ItemKindToolResult {
		return "tool_result:" + action.CallID
	}
	return "tool_call:" + action.CallID
}

// terminalDecisionReleasedActions is the existing projection: normalized request
// items first, then the already-released canonical events.
func terminalDecisionReleasedActions(items []aipapi.Item, p *responsePipeline) []terminaldecision.ActionFact {
	actions := make([]terminaldecision.ActionFact, 0, terminaldecision.MaxEvidenceActions)
	positions := make(map[string]int, terminaldecision.MaxEvidenceActions)
	add := func(action terminaldecision.ActionFact, key string) {
		if action.ItemID == "" && action.CallID == "" {
			return
		}
		index, known := positions[key]
		// A nameless finished call is the ordinary provider shape: correlate it to
		// the already-known named start or item first and keep that trusted name,
		// then apply its canonical status. An orphan finish stays unprojected
		// rather than inventing a name for it.
		if known && action.Name == "" && action.Kind != aipapi.ItemKindMessage {
			action.Name = actions[index].Name
		}
		if action.Kind != aipapi.ItemKindMessage && (action.CallID == "" || strings.TrimSpace(action.Name) == "") {
			return
		}
		if known {
			if terminalDecisionActionRank(action.Status) > terminalDecisionActionRank(actions[index].Status) {
				actions[index].Status = action.Status
			}
			if actions[index].Name == "" {
				actions[index].Name = action.Name
			}
			return
		}
		if len(actions) >= terminaldecision.MaxEvidenceActions {
			return
		}
		positions[key] = len(actions)
		actions = append(actions, action)
	}
	for _, item := range items {
		if action, key, ok := terminalDecisionActionFromItem(item); ok {
			add(action, key)
		}
	}
	if p != nil {
		for _, event := range p.seenEventsCopy() {
			action, key, ok := terminalDecisionActionFromEvent(event)
			if ok {
				add(action, key)
			}
		}
	}
	return actions
}

func terminalDecisionActionFromItem(item aipapi.Item) (terminaldecision.ActionFact, string, bool) {
	status := item.Status
	if status == "" {
		status = aipapi.ItemStatusCompleted
	}
	action := terminaldecision.ActionFact{
		ItemID: boundedTerminalDecisionIdentifier(item.ID),
		Kind:   item.Kind,
		Status: status,
	}
	switch item.Kind {
	case aipapi.ItemKindMessage:
		return action, "message:" + item.ID, action.ItemID != ""
	case aipapi.ItemKindToolCall:
		if item.ToolCall == nil {
			return terminaldecision.ActionFact{}, "", false
		}
		action.CallID = boundedTerminalDecisionIdentifier(item.ToolCall.CallID)
		action.Name = boundedTerminalDecisionIdentifier(item.ToolCall.Name)
		return action, "tool_call:" + item.ToolCall.CallID, action.CallID != "" && action.Name != ""
	case aipapi.ItemKindToolResult:
		if item.ToolResult == nil {
			return terminaldecision.ActionFact{}, "", false
		}
		action.CallID = boundedTerminalDecisionIdentifier(item.ToolResult.CallID)
		action.Name = boundedTerminalDecisionIdentifier(item.ToolResult.Name)
		return action, "tool_result:" + item.ToolResult.CallID, action.CallID != "" && action.Name != ""
	default:
		return terminaldecision.ActionFact{}, "", false
	}
}

func terminalDecisionActionFromEvent(event aipapi.Event) (terminaldecision.ActionFact, string, bool) {
	switch event.Kind {
	case aipapi.EventToolCallStarted:
		return terminaldecision.ActionFact{
			CallID: boundedTerminalDecisionIdentifier(event.ToolCallID),
			Kind:   aipapi.ItemKindToolCall,
			Status: aipapi.ItemStatusInProgress,
			Name:   boundedTerminalDecisionIdentifier(event.ToolName),
		}, "tool_call:" + event.ToolCallID, event.ToolCallID != "" && event.ToolName != ""
	case aipapi.EventToolCallFinished:
		return terminaldecision.ActionFact{
			CallID: boundedTerminalDecisionIdentifier(event.ToolCallID),
			Kind:   aipapi.ItemKindToolCall,
			Status: aipapi.ItemStatusCompleted,
		}, "tool_call:" + event.ToolCallID, event.ToolCallID != ""
	case aipapi.EventItem:
		if event.Item == nil {
			return terminaldecision.ActionFact{}, "", false
		}
		return terminalDecisionActionFromItem(*event.Item)
	default:
		return terminaldecision.ActionFact{}, "", false
	}
}

func terminalDecisionActionRank(status aipapi.ItemStatus) int {
	switch status {
	case aipapi.ItemStatusCompleted:
		return 3
	case aipapi.ItemStatusIncomplete:
		return 2
	case aipapi.ItemStatusInProgress:
		return 1
	default:
		return 0
	}
}

func boundedTerminalDecisionIdentifier(value string) string {
	return boundedTerminalDecisionString(value, terminaldecision.MaxIdentifierBytes)
}

func boundedTerminalDecisionText(value string) string {
	return boundedTerminalDecisionString(value, terminaldecision.MaxEvidenceTextBytes)
}

func boundedTerminalDecisionString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}
