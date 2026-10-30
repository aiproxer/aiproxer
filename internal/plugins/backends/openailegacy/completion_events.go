package openailegacy

import (
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaiusage"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/respjson"
)

// CompletionEvents converts a non-streaming ChatCompletion response into canonical events.
func CompletionEvents(comp openai.ChatCompletion) []aipapi.Event {
	events := []aipapi.Event{{Kind: aipapi.EventResponseStarted}}

	for _, choice := range comp.Choices {
		msg := choice.Message
		sawMsg := false

		if reasoning := reasoningTextFromMessage(msg); reasoning != "" {
			if !sawMsg {
				sawMsg = true
				events = append(events, aipapi.Event{Kind: aipapi.EventMessageStarted})
			}
			events = append(events, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: reasoning})
		}

		if msg.Content != "" {
			if !sawMsg {
				sawMsg = true
				events = append(events, aipapi.Event{Kind: aipapi.EventMessageStarted})
			}
			events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: msg.Content})
		}

		if len(msg.ToolCalls) > 0 {
			if !sawMsg {
				sawMsg = true
				events = append(events, aipapi.Event{Kind: aipapi.EventMessageStarted})
			}
			for _, tc := range msg.ToolCalls {
				fn := tc.AsFunction()
				if fn.ID == "" {
					continue
				}
				events = append(events, aipapi.Event{
					Kind:       aipapi.EventToolCallStarted,
					ToolCallID: fn.ID,
					ToolName:   fn.Function.Name,
				})
				if fn.Function.Arguments != "" {
					events = append(events, aipapi.Event{
						Kind:       aipapi.EventToolCallArgsDelta,
						ToolCallID: fn.ID,
						Delta:      fn.Function.Arguments,
					})
				}
				events = append(events, aipapi.Event{
					Kind:       aipapi.EventToolCallFinished,
					ToolCallID: fn.ID,
				})
			}
		}
	}

	finishReason := ""
	for _, choice := range comp.Choices {
		if fr := string(choice.FinishReason); fr != "" {
			finishReason = fr
			break
		}
	}

	if comp.JSON.Usage.Valid() {
		usage := openaiusage.ChatUsageEvent(comp.Usage)
		openaiusage.AnnotateProviderContext(&usage, comp.ID, string(comp.ServiceTier))
		events = append(events, usage)
	}

	// Only output-token-limit and content-filter finish_reasons are classified.
	// Forwarding any other raw vendor lexeme would mark a clean tool_calls
	// turn incomplete, because the continuation recorder treats every
	// FinishReason other than "", "stop" and "end_turn" as a truncation.
	// A content_filter refusal is a complete upstream decision carried
	// verbatim (never length taxonomy, never dropped to clean-stop).
	fin := aipapi.Event{Kind: aipapi.EventResponseFinished}
	switch finishReason {
	case "length":
		fin.ResponseStatus = "incomplete"
		fin.FinishReason = finishReason
	case "content_filter":
		fin.FinishReason = finishReason
	}
	events = append(events, fin)
	return events
}

func firstReasoningField(fields map[string]respjson.Field, requireNonSpace bool, keys ...string) (string, bool) {
	for _, key := range keys {
		f, ok := fields[key]
		if !ok {
			continue
		}
		raw := f.Raw()
		if raw == "" || raw == respjson.Null {
			continue
		}
		var s string
		if json.Unmarshal([]byte(raw), &s) != nil {
			continue
		}
		if requireNonSpace && strings.TrimSpace(s) == "" {
			continue
		}
		return s, true
	}
	return "", false
}

func reasoningTextFromMessage(msg openai.ChatCompletionMessage) string {
	if msg.JSON.ExtraFields == nil {
		return ""
	}
	if s, ok := firstReasoningField(msg.JSON.ExtraFields, false, "reasoning", "reasoning_content"); ok {
		return s
	}
	s, _ := firstReasoningField(msg.JSON.ExtraFields, true, "reasoning_summary")
	return s
}
