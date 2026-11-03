package bridge

import (
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// turnEvents tracks the canonical ordering of one HTTP response. Every response
// the converter serves is a complete canonical turn even when the native SDK
// turn continues behind it.
type turnEvents struct {
	started        bool
	messageStarted bool
	// sanitize redacts connector secrets from warning text. It is never applied
	// to caller content.
	sanitize func(string) string
}

func (t *turnEvents) clean(value string) string {
	if t.sanitize == nil {
		return boundedText(value)
	}
	return t.sanitize(value)
}

// mapFrame maps one text/usage/terminal sidecar frame to canonical events. The
// terminal flag reports a frame that ends this HTTP response; the caller updates
// the parked or completed session state before emitting the returned events.
func (t *turnEvents) mapFrame(frame Frame) ([]aipapi.Event, bool, error) {
	switch frame.Kind {
	case "hello", "ready", "tool_released":
		return nil, false, nil
	case "response_started":
		if t.started {
			return nil, false, errors.New("claudeagentmodel: duplicate response start")
		}
		t.started = true
		return []aipapi.Event{{Kind: aipapi.EventResponseStarted}}, false, nil
	case "message_started":
		if !t.started || t.messageStarted {
			return nil, false, errors.New("claudeagentmodel: invalid message start")
		}
		t.messageStarted = true
		return []aipapi.Event{{Kind: aipapi.EventMessageStarted}}, false, nil
	case "text_delta":
		if !t.messageStarted || frame.Text == "" {
			return nil, false, errors.New("claudeagentmodel: text delta before message start")
		}
		return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: frame.Text}}, false, nil
	case "usage":
		if !t.started {
			return nil, false, errors.New("claudeagentmodel: usage before response start")
		}
		return []aipapi.Event{frame.usageEvent()}, false, nil
	case "warning":
		if !t.started {
			return nil, false, errors.New("claudeagentmodel: warning before response start")
		}
		return []aipapi.Event{{
			Kind:           aipapi.EventWarning,
			WarningCode:    t.clean(frame.Code),
			WarningMessage: t.clean(frame.Message),
		}}, false, nil
	case "response_finished":
		if !t.started {
			return nil, false, errors.New("claudeagentmodel: response finished before start")
		}
		return []aipapi.Event{{
			Kind:         aipapi.EventResponseFinished,
			FinishReason: frame.StopReason,
		}}, true, nil
	case "error":
		return nil, false, newSidecarError(frame.Code)
	default:
		return nil, false, errors.New("claudeagentmodel: unknown sidecar frame")
	}
}

// toolCallEvents renders one complete validated native tool call as the
// canonical start/args/finished sequence. The original caller name and the exact
// argument bytes are preserved; the native ID stays the opaque correlation key.
func toolCallEvents(pending *PendingCall) []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: pending.CallID, ToolName: pending.Name},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: pending.CallID, Delta: string(pending.Args)},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: pending.CallID, ToolName: pending.Name},
	}
}

func (f Frame) usageEvent() aipapi.Event {
	event := aipapi.Event{Kind: aipapi.EventUsageDelta}
	if f.InputTokens != nil {
		event.InputTokens = int(*f.InputTokens)
		event.UsagePresence.InputTokens = true
	}
	if f.OutputTokens != nil {
		event.OutputTokens = int(*f.OutputTokens)
		event.UsagePresence.OutputTokens = true
	}
	if f.CacheReadTokens != nil {
		event.CacheReadTokens = int(*f.CacheReadTokens)
		event.UsagePresence.CacheReadTokens = true
	}
	if f.CacheWriteTokens != nil {
		event.CacheWriteTokens = int(*f.CacheWriteTokens)
		event.UsagePresence.CacheWriteTokens = true
	}
	if f.TotalTokens != nil {
		event.TotalTokens = int(*f.TotalTokens)
		event.UsagePresence.TotalTokens = true
	}
	if f.ReasoningTokens != nil {
		event.ReasoningTokens = int(*f.ReasoningTokens)
		event.UsagePresence.ReasoningTokens = true
	}
	return event
}
