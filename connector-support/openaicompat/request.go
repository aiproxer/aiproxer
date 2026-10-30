package openaicompat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func buildRequestBody(call aipapi.Call, model string, flavor Flavor, stream bool, hooks RequestHooks) ([]byte, error) {
	body := map[string]any{
		"model":  model,
		"stream": stream,
	}
	switch flavor {
	case FlavorChat:
		msgs, err := chatMessages(call)
		if err != nil {
			return nil, err
		}
		body["messages"] = msgs
		if stream {
			body["stream_options"] = map[string]any{"include_usage": true}
		}
	case FlavorResponses:
		input, err := responsesInput(call)
		if err != nil {
			return nil, err
		}
		body["input"] = input
	default:
		return nil, fmt.Errorf("openaicompat: unknown flavor %q", flavor)
	}
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		if flavor == FlavorChat {
			body["max_completion_tokens"] = *call.Options.MaxOutputTokens
		} else {
			body["max_output_tokens"] = *call.Options.MaxOutputTokens
		}
	}
	if call.Options.Temperature != nil {
		body["temperature"] = *call.Options.Temperature
	}
	if len(call.Tools) > 0 {
		tools := make([]map[string]any, 0, len(call.Tools))
		for _, t := range call.Tools {
			fn := map[string]any{"name": t.Name}
			if t.Description != "" {
				fn["description"] = t.Description
			}
			if len(t.Parameters) > 0 {
				var params any
				if err := json.Unmarshal(t.Parameters, &params); err == nil {
					fn["parameters"] = params
				}
			}
			tools = append(tools, map[string]any{"type": "function", "function": fn})
		}
		body["tools"] = tools
	}
	for k, raw := range hooks.ExtraBody {
		k = strings.TrimSpace(k)
		if k == "" || len(raw) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("openaicompat: extra body %q: %w", k, err)
		}
		body[k] = v
	}
	if hooks.MutateBody != nil {
		if err := hooks.MutateBody(body, call, model, flavor); err != nil {
			return nil, err
		}
	}
	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: marshal body: %w", err)
	}
	return out, nil
}

func chatMessages(call aipapi.Call) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(call.Instructions)+len(call.Messages))
	for _, m := range call.Instructions {
		msg, err := chatMessage(m)
		if err != nil {
			return nil, err
		}
		if msg["role"] == "" {
			msg["role"] = "system"
		}
		out = append(out, msg)
	}
	for _, m := range call.Messages {
		msg, err := chatMessage(m)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("openaicompat: no messages")
	}
	return out, nil
}

func chatMessage(m aipapi.Message) (map[string]any, error) {
	role := string(m.Role)
	if role == "" {
		role = "user"
	}
	var textParts []string
	for _, p := range m.Parts {
		switch p.Kind {
		case aipapi.PartText:
			if p.Text != "" {
				textParts = append(textParts, p.Text)
			}
		case aipapi.PartToolResult:
			return map[string]any{
				"role":         "tool",
				"tool_call_id": p.ToolCallID,
				"content":      string(p.Content),
			}, nil
		default:
			return nil, unsupportedPartError(p.Kind)
		}
	}
	return map[string]any{
		"role":    role,
		"content": strings.Join(textParts, ""),
	}, nil
}

func responsesInput(call aipapi.Call) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(call.Messages))
	for _, m := range call.Messages {
		role := string(m.Role)
		if role == "" {
			role = "user"
		}
		var text strings.Builder
		for _, p := range m.Parts {
			switch p.Kind {
			case aipapi.PartText:
				text.WriteString(p.Text)
			case aipapi.PartToolResult:
				return nil, unsupportedPartError(p.Kind)
			default:
				return nil, unsupportedPartError(p.Kind)
			}
		}
		out = append(out, map[string]any{
			"role":    role,
			"content": text.String(),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("openaicompat: no input")
	}
	return out, nil
}

func unsupportedPartError(kind aipapi.PartKind) error {
	return fmt.Errorf("openaicompat: unsupported canonical content part %q; conversion is not implemented", kind)
}
