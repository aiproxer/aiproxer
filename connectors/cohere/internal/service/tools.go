package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Cohere v2 chat tool wire shapes (https://docs.cohere.com/reference/chat).
//
//   - Request tools use {type:"function", function:{name, description?,
//     parameters (JSON schema object, required)}}.
//   - Assistant tool calls use {id, type:"function", function:{name,
//     arguments (JSON string)}} with ID correlation to tool results.
//   - Tool results are {role:"tool", tool_call_id, content} messages.
//   - tool_choice supports only REQUIRED (force ≥1 tool call) and NONE
//     (force no tool call); omitting it leaves the choice to the model.

type cohereToolFunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type cohereToolDef struct {
	Type     string                `json:"type"`
	Function cohereToolFunctionDef `json:"function"`
}

type cohereToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type cohereToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function cohereToolCallFunction `json:"function"`
}

// buildCohereTools maps canonical tool definitions onto Cohere v2 function
// definitions. Parameters default to an empty object schema when unset;
// malformed schemas fail closed naming the offending tool.
func buildCohereTools(tools []aipapi.ToolDef) ([]cohereToolDef, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]cohereToolDef, 0, len(tools))
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			return nil, fmt.Errorf("cohere: tool name is required")
		}
		params := map[string]any{"type": "object"}
		if len(t.Parameters) > 0 {
			var schema map[string]any
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return nil, fmt.Errorf("cohere: tool %q parameters: %w", name, err)
			}
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			params = schema
		}
		out = append(out, cohereToolDef{
			Type: "function",
			Function: cohereToolFunctionDef{
				Name:        name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return out, nil
}

// cohereToolChoiceValue maps the canonical tool choice onto the Cohere v2
// tool_choice enum. Auto (or empty) is omitted so the model decides. Any maps
// to REQUIRED (must call at least one tool), matching the OpenAI-compatible
// precedent. A required choice naming one specific tool cannot be represented
// (Cohere offers no per-tool choice) and fails closed naming the tool. The
// AllowedTools subset has no Cohere wire carrier: the full catalog is sent
// and the frontend allowed-tools filter enforces the subset at the protocol
// output boundary.
func cohereToolChoiceValue(tc aipapi.ToolChoice, nTools int) (*string, error) {
	mode := tc.Mode
	if mode == "" {
		mode = aipapi.ToolChoiceAuto
	}
	switch mode {
	case aipapi.ToolChoiceAuto:
		return nil, nil
	case aipapi.ToolChoiceNone:
		v := "NONE"
		return &v, nil
	case aipapi.ToolChoiceAny:
		if nTools == 0 {
			return nil, fmt.Errorf("cohere: tool_choice %q requires at least one tool definition", mode)
		}
		v := "REQUIRED"
		return &v, nil
	case aipapi.ToolChoiceRequired:
		if name := strings.TrimSpace(tc.Name); name != "" {
			return nil, fmt.Errorf("cohere: tool_choice required for tool %q is not supported (Cohere v2 tool_choice supports only REQUIRED/NONE, no per-tool choice)", tc.Name)
		}
		if nTools == 0 {
			return nil, fmt.Errorf("cohere: tool_choice %q requires at least one tool definition", mode)
		}
		v := "REQUIRED"
		return &v, nil
	default:
		return nil, fmt.Errorf("cohere: unsupported tool_choice mode %q", mode)
	}
}

// buildAssistantMessage maps a canonical assistant message onto a Cohere v2
// assistant message, carrying prior tool calls (PartJSON parts) as tool_calls
// with ID correlation alongside any text content. Text-only reasoning
// (PartReasoning with Text and no signature/opaque/exact fields) replays as
// tool_plan, mirroring the tool_plan → EventReasoningDelta → PartReasoning
// round-trip; non-text reasoning cannot round-trip through tool_plan and
// fails closed.
func buildAssistantMessage(msg aipapi.Message) (cohereMessage, error) {
	var sb strings.Builder
	var plan strings.Builder
	var calls []cohereToolCall
	for _, p := range msg.Parts {
		switch p.Kind {
		case aipapi.PartText:
			sb.WriteString(p.Text)
		case aipapi.PartReasoning:
			if p.Reasoning == nil {
				return cohereMessage{}, fmt.Errorf("cohere: reasoning part requires Reasoning payload")
			}
			rp := p.Reasoning
			if rp.Signature != "" || len(rp.Opaque) > 0 || aipapi.ReasoningHasExactResponsesFields(rp) || len(rp.EncryptedContent) > 0 {
				return cohereMessage{}, fmt.Errorf("cohere: reasoning with signature/opaque cannot round-trip through tool_plan (text-only reasoning is supported)")
			}
			plan.WriteString(rp.Text)
		case aipapi.PartJSON:
			tc, err := cohereToolCallFromPart(p)
			if err != nil {
				return cohereMessage{}, err
			}
			calls = append(calls, tc)
		default:
			return cohereMessage{}, fmt.Errorf("cohere: unsupported part kind %q in assistant message (vision and non-text are not supported)", p.Kind)
		}
	}
	out := cohereMessage{Role: "assistant", Content: sb.String(), ToolCalls: calls, ToolPlan: plan.String()}
	if out.Content == "" && out.ToolPlan == "" && len(calls) == 0 {
		return cohereMessage{}, fmt.Errorf("cohere: assistant message is empty")
	}
	return out, nil
}

// cohereToolCallFromPart maps one canonical assistant tool-call part onto a
// Cohere v2 tool_call. The part content carries either raw arguments JSON
// (legacy projection form) or a {id, type, function:{name, arguments}}
// envelope; both are accepted.
func cohereToolCallFromPart(p aipapi.Part) (cohereToolCall, error) {
	id := strings.TrimSpace(p.ToolCallID)
	name := strings.TrimSpace(p.ToolName)
	args := ""
	if len(p.Content) > 0 {
		var env struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function *struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		}
		if err := json.Unmarshal(p.Content, &env); err == nil && env.Type == "function" && env.Function != nil &&
			(env.ID != "" || env.Function.Name != "" || len(env.Function.Arguments) > 0) {
			if id == "" {
				id = strings.TrimSpace(env.ID)
			}
			if name == "" {
				name = strings.TrimSpace(env.Function.Name)
			}
			if len(env.Function.Arguments) > 0 {
				a, err := cohereArgumentsString(env.Function.Arguments)
				if err != nil {
					return cohereToolCall{}, fmt.Errorf("cohere: assistant tool_call arguments: %w", err)
				}
				args = a
			}
		} else if json.Valid(p.Content) {
			// Canonical message authority stores the argument document itself,
			// including a JSON string document. Keep those bytes as the wire
			// argument text instead of decoding the document a second time.
			args = string(p.Content)
		} else {
			return cohereToolCall{}, fmt.Errorf("cohere: assistant tool_call content is not valid JSON")
		}
	}
	if id == "" || name == "" {
		return cohereToolCall{}, fmt.Errorf("cohere: assistant tool_call requires id and name")
	}
	return cohereToolCall{
		ID:   id,
		Type: "function",
		Function: cohereToolCallFunction{
			Name:      name,
			Arguments: args,
		},
	}, nil
}

// cohereArgumentsString renders raw arguments JSON as the arguments string the
// Cohere v2 wire format carries, unwrapping one layer of JSON string encoding
// when the arguments were double-encoded.
func cohereArgumentsString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	if !json.Valid(raw) {
		return "", fmt.Errorf("invalid JSON")
	}
	return compactJSON(raw), nil
}

func compactJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// buildToolMessages maps one canonical tool message onto Cohere v2 tool
// messages (one wire message per tool_result part) preserving the
// tool_call_id correlation.
func buildToolMessages(msg aipapi.Message) ([]cohereMessage, error) {
	var out []cohereMessage
	for _, p := range msg.Parts {
		if p.Kind != aipapi.PartToolResult {
			return nil, fmt.Errorf("cohere: unsupported part kind %q in tool message (only tool_result parts are supported)", p.Kind)
		}
		id := strings.TrimSpace(p.ToolCallID)
		if id == "" {
			return nil, fmt.Errorf("cohere: tool_result part requires ToolCallID")
		}
		content := cohereToolResultContent(p)
		if content == "" {
			return nil, fmt.Errorf("cohere: tool result for call %q has empty content", id)
		}
		out = append(out, cohereMessage{Role: "tool", Content: content, ToolCallID: id})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cohere: tool message has no tool_result parts")
	}
	return out, nil
}

// cohereToolResultContent renders a canonical tool_result part payload,
// accepting both the Content carrier (ABI path, JSON string or raw JSON) and
// the Text carrier (legacy projection path).
func cohereToolResultContent(p aipapi.Part) string {
	if len(p.Content) > 0 {
		var s string
		if err := json.Unmarshal(p.Content, &s); err == nil {
			return s
		}
		return string(p.Content)
	}
	return p.Text
}

// cohereResponseEvents maps a Cohere v2 non-streaming chat message (text,
// tool plan, tool calls) onto canonical events with ID-correlated tool call
// lifecycles.
func cohereResponseEvents(text, toolPlan string, toolCalls []cohereToolCall, finishReason string) ([]aipapi.Event, error) {
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
	}
	if text != "" {
		events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: text})
	}
	if toolPlan != "" {
		events = append(events, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: toolPlan})
	}
	for _, tc := range toolCalls {
		id := strings.TrimSpace(tc.ID)
		if id == "" {
			return nil, fmt.Errorf("cohere: tool_call without id")
		}
		events = append(events, aipapi.Event{
			Kind:       aipapi.EventToolCallStarted,
			ToolCallID: id,
			ToolName:   tc.Function.Name,
		})
		if tc.Function.Arguments != "" {
			events = append(events, aipapi.Event{
				Kind:       aipapi.EventToolCallArgsDelta,
				ToolCallID: id,
				Delta:      tc.Function.Arguments,
			})
		}
		events = append(events, aipapi.Event{
			Kind:       aipapi.EventToolCallFinished,
			ToolCallID: id,
		})
	}
	events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: finishReason})
	return events, nil
}

// parseCohereStreamToolCalls parses the tool_calls payload of a Cohere v2
// stream delta, which carries a single tool_call object per event.
func parseCohereStreamToolCalls(raw json.RawMessage) []cohereToolCall {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var single cohereToolCall
	if err := json.Unmarshal(raw, &single); err == nil &&
		(single.ID != "" || single.Function.Name != "" || single.Function.Arguments != "") {
		return []cohereToolCall{single}
	}
	var multi []cohereToolCall
	if err := json.Unmarshal(raw, &multi); err == nil {
		return multi
	}
	return nil
}

// normalizeCohereStreamType lowercases the stream event type and unifies
// separators so hyphen/underscore variants route identically.
func normalizeCohereStreamType(t string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(t)), "_", "-")
}
