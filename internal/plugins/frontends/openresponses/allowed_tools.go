package openresponses

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// allowedToolsStream enforces the OpenResponses allowed_tools hard constraint on
// the canonical event stream before any client output path (HTTP streaming,
// HTTP non-streaming, and WebSocket turns) consumes it. Tool call events for
// tools outside the allowed subset are suppressed: the full started/args/
// finished lifecycle is swallowed so a forbidden call can never surface on
// streaming events, in a non-streaming resource, or on a WebSocket turn. All
// other events pass through untouched. Backends that cannot natively represent
// the subset keep the full tools list visible (cacheability) while this filter
// guarantees the constraint at the protocol output boundary.
type allowedToolsStream struct {
	stream aipapi.EventStream
	// allowed is the normalized allowed_tools subset (tool name set).
	allowed map[string]struct{}
	// suppressAll is true under ToolChoiceNone: no tool call may ever surface,
	// regardless of subset membership. Mode None with a subset is legal (the
	// subset is vacuous) but the mode still means "never call a tool".
	suppressAll bool
	// suppressed tracks in-flight tool call IDs whose lifecycle is being
	// dropped, so their later args deltas and finish events are also dropped.
	suppressed map[string]struct{}
	// openSuppressed counts open suppressed tool calls. It drives the
	// fail-closed empty-call-ID delta/finish fallback: whenever a suppressed
	// call could own an empty-ID event, that event is dropped.
	openSuppressed int
}

var _ aipapi.EventStream = (*allowedToolsStream)(nil)

// newAllowedToolsStream wraps stream with the allowed_tools filter when the
// call carries a subset or explicitly selects ToolChoiceNone; otherwise it
// returns the original stream.
func newAllowedToolsStream(call *aipapi.Call, stream aipapi.EventStream) aipapi.EventStream {
	if call == nil {
		return stream
	}
	return newAllowedToolsStreamFromChoice(call.ToolChoice, stream)
}

// newAllowedToolsStreamFromChoice wraps stream with the allowed_tools filter for a given ToolChoice.
func newAllowedToolsStreamFromChoice(choice aipapi.ToolChoice, stream aipapi.EventStream) aipapi.EventStream {
	if stream == nil || (len(choice.AllowedTools) == 0 && choice.Mode != aipapi.ToolChoiceNone) {
		return stream
	}
	allowed := make(map[string]struct{}, len(choice.AllowedTools))
	for _, name := range choice.AllowedTools {
		allowed[name] = struct{}{}
	}
	return &allowedToolsStream{
		stream:      stream,
		allowed:     allowed,
		suppressAll: choice.Mode == aipapi.ToolChoiceNone,
		suppressed:  make(map[string]struct{}),
	}
}

// Recv returns the next client-visible canonical event, transparently dropping
// suppressed tool call lifecycles.
func (s *allowedToolsStream) Recv(ctx context.Context) (aipapi.Event, error) {
	for {
		ev, err := s.stream.Recv(ctx)
		if err != nil {
			return ev, err
		}
		if s.filter(&ev) {
			return ev, nil
		}
	}
}

// filter reports whether ev must be delivered to the client, updating
// suppression bookkeeping as tool call lifecycles pass through.
func (s *allowedToolsStream) filter(ev *aipapi.Event) bool {
	switch ev.Kind {
	case aipapi.EventToolCallStarted:
		if !s.suppressAll {
			if _, ok := s.allowed[ev.ToolName]; ok {
				return true
			}
		}
		// Forbidden (or mode none forbids everything): swallow the whole
		// lifecycle. The call ID is tracked so its later args/finish events are
		// dropped even if other allowed calls interleave.
		s.openSuppressed++
		if ev.ToolCallID != "" {
			s.suppressed[ev.ToolCallID] = struct{}{}
		}
		return false
	case aipapi.EventToolCallArgsDelta:
		if ev.ToolCallID != "" {
			_, bad := s.suppressed[ev.ToolCallID]
			return !bad
		}
		// Fail closed: an empty-ID delta could belong to any open suppressed
		// call, so whenever one exists the delta must not reach the client.
		return s.openSuppressed == 0
	case aipapi.EventToolCallFinished:
		if ev.ToolCallID != "" {
			if _, bad := s.suppressed[ev.ToolCallID]; bad {
				delete(s.suppressed, ev.ToolCallID)
				if s.openSuppressed > 0 {
					s.openSuppressed--
				}
				return false
			}
			return true
		}
		// Fail closed: an empty-ID finish could belong to a suppressed call.
		// Conservatively assume it closed one and keep the count consistent so
		// no later empty-ID event is leaked.
		if s.openSuppressed > 0 {
			s.openSuppressed--
			return false
		}
		return true
	default:
		return true
	}
}

// Close closes the underlying canonical stream.
func (s *allowedToolsStream) Close() error {
	return s.stream.Close()
}
