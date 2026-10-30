package aipapi

// MergeToolEventInto applies tool-reactor output onto a single canonical stream event.
// The event kind must match the tool event kind; only tool-call fields are updated.
func MergeToolEventInto(orig Event, te ToolEvent) Event {
	out := orig
	switch te.Kind {
	case ToolEventStarted:
		if te.ToolCallID != "" {
			out.ToolCallID = te.ToolCallID
		}
		out.ToolName = te.ToolName
	case ToolEventArgsDelta:
		if te.ToolCallID != "" {
			out.ToolCallID = te.ToolCallID
		}
		out.ToolName = te.ToolName
		out.Delta = te.ArgsDelta
	case ToolEventFinished:
		if te.ToolCallID != "" {
			out.ToolCallID = te.ToolCallID
		}
		out.ToolName = te.ToolName
	default:
		return orig
	}
	return out
}
