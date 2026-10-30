package aipapi

// ToolEventKind classifies tool-call lifecycle items passed to tool reactors.
type ToolEventKind string

const (
	ToolEventStarted   ToolEventKind = "tool_call_started"
	ToolEventArgsDelta ToolEventKind = "tool_call_args_delta"
	ToolEventFinished  ToolEventKind = "tool_call_finished"
)

// ToolEvent is the canonical tool-call subset exposed to tool reactors.
type ToolEvent struct {
	Kind ToolEventKind

	ToolCallID string
	ToolName   string

	// Category is the coarse category derived from ToolName (or from lifecycle
	// correlation when a later fragment omits ToolName). It is informational
	// metadata, never an allow/deny decision by itself.
	Category ToolCategory
	// MayMutateLocalFS is a conservative potential-capability hint: true when the
	// named tool family can mutate the local filesystem. It is not evidence that
	// a specific invocation mutated anything.
	//
	// The zero value is false and is not a classified result. Unknown names
	// classify as true; project via ToolEventFromEvent or ClassifyToolName
	// rather than inspecting an unprojected ToolEvent literal.
	MayMutateLocalFS bool

	// ArgsDelta carries incremental JSON/tool arguments fragments for ToolEventArgsDelta.
	ArgsDelta string
}

// ToolEventFromEvent maps a single stream Event to a ToolEvent when applicable.
// The second return value is false for non-tool event kinds.
func ToolEventFromEvent(ev Event) (ToolEvent, bool) {
	switch ev.Kind {
	case EventToolCallStarted:
		if ev.ToolCallID == "" {
			return ToolEvent{}, false
		}
		cat, mut := ClassifyToolName(ev.ToolName)
		return ToolEvent{
			Kind:             ToolEventStarted,
			ToolCallID:       ev.ToolCallID,
			ToolName:         ev.ToolName,
			Category:         cat,
			MayMutateLocalFS: mut,
		}, true
	case EventToolCallArgsDelta:
		if ev.ToolCallID == "" {
			return ToolEvent{}, false
		}
		cat, mut := ClassifyToolName(ev.ToolName)
		return ToolEvent{
			Kind:             ToolEventArgsDelta,
			ToolCallID:       ev.ToolCallID,
			ToolName:         ev.ToolName,
			Category:         cat,
			MayMutateLocalFS: mut,
			ArgsDelta:        ev.Delta,
		}, true
	case EventToolCallFinished:
		if ev.ToolCallID == "" {
			return ToolEvent{}, false
		}
		cat, mut := ClassifyToolName(ev.ToolName)
		return ToolEvent{
			Kind:             ToolEventFinished,
			ToolCallID:       ev.ToolCallID,
			ToolName:         ev.ToolName,
			Category:         cat,
			MayMutateLocalFS: mut,
		}, true
	default:
		return ToolEvent{}, false
	}
}
