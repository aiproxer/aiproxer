package anthropic

import (
	"encoding/json"
	"maps"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const defaultMaxTokens = 4096

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Messages    []anthropicMessage `json:"messages"`
	System      any                `json:"system,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	ToolChoice  any                `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

func buildRequestBody(call aipapi.Call, model string) ([]byte, error) {
	req := anthropicRequest{
		Model:       model,
		MaxTokens:   resolveMaxTokens(call),
		Messages:    mapMessages(call),
		Stream:      isStreaming(call),
		Temperature: call.Options.Temperature,
		TopP:        call.Options.TopP,
		System:      resolveSystem(call),
		Tools:       mapTools(call),
		ToolChoice:  mapToolChoice(call),
	}
	extraBody := collectExtraBody(call.Extensions)
	if len(extraBody) == 0 {
		return json.Marshal(req)
	}
	rawMap, err := structToMap(req)
	if err != nil {
		return nil, err
	}
	maps.Copy(rawMap, extraBody)
	return json.Marshal(rawMap)
}

func resolveMaxTokens(call aipapi.Call) int {
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		return *call.Options.MaxOutputTokens
	}
	return defaultMaxTokens
}

func resolveSystem(call aipapi.Call) any {
	var parts []string
	if t := aipapi.JoinInstructionText(call.Instructions); t != "" {
		parts = append(parts, t)
	}
	for _, msg := range call.Messages {
		if msg.Role != aipapi.RoleSystem {
			continue
		}
		for _, p := range msg.Parts {
			if p.Kind == aipapi.PartText && strings.TrimSpace(p.Text) != "" {
				parts = append(parts, p.Text)
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return strings.Join(parts, "\n\n")
}

func mapMessages(call aipapi.Call) []anthropicMessage {
	var out []anthropicMessage
	for _, msg := range call.Messages {
		if msg.Role == aipapi.RoleSystem {
			continue
		}
		if msg.Role == aipapi.RoleTool {
			if blocks := mapToolResultBlocks(msg.Parts); len(blocks) > 0 {
				out = append(out, anthropicMessage{Role: "user", Content: blocks})
			}
			continue
		}
		role := "user"
		if msg.Role == aipapi.RoleAssistant {
			role = "assistant"
		}
		blocks := mapContentBlocks(msg.Parts)
		if len(blocks) == 0 {
			continue
		}
		if len(blocks) == 1 {
			if txt, ok := blocks[0].(map[string]any); ok && txt["type"] == "text" {
				out = append(out, anthropicMessage{Role: role, Content: txt["text"]})
				continue
			}
		}
		out = append(out, anthropicMessage{Role: role, Content: blocks})
	}
	if len(out) == 0 {
		out = append(out, anthropicMessage{Role: "user", Content: "hi"})
	}
	return out
}

func mapToolResultBlocks(parts []aipapi.Part) []any {
	var blocks []any
	for _, p := range parts {
		if p.Kind != aipapi.PartToolResult {
			continue
		}
		blocks = append(blocks, toolResultBlock(p))
	}
	return blocks
}

func toolResultBlock(p aipapi.Part) map[string]any {
	content := string(p.Content)
	if content == "" {
		content = p.Text
	}
	return map[string]any{"type": "tool_result", "tool_use_id": p.ToolCallID, "content": content}
}

func mapContentBlocks(parts []aipapi.Part) []any {
	var blocks []any
	for _, p := range parts {
		switch p.Kind {
		case aipapi.PartText:
			if p.Text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
			}
		case aipapi.PartJSON:
			blocks = append(blocks, mapToolUseBlock(p))
		case aipapi.PartToolResult:
			blocks = append(blocks, toolResultBlock(p))
		case aipapi.PartReasoning:
			if p.Reasoning != nil {
				blocks = append(blocks, mapThinkingBlock(p))
			}
		case aipapi.PartImageRef:
			blocks = append(blocks, map[string]any{
				"type":   "image",
				"source": map[string]any{"type": "base64", "media_type": p.ImageMIME, "data": p.ImageRef},
			})
		}
	}
	return blocks
}

func mapToolUseBlock(p aipapi.Part) map[string]any {
	var input any
	if len(p.Content) > 0 {
		_ = json.Unmarshal(p.Content, &input)
	}
	if input == nil {
		input = make(map[string]any)
	}
	return map[string]any{"type": "tool_use", "id": p.ToolCallID, "name": p.ToolName, "input": input}
}

func mapThinkingBlock(p aipapi.Part) map[string]any {
	block := map[string]any{"type": "thinking", "thinking": p.Reasoning.Text}
	if p.Reasoning.Signature != "" {
		block["signature"] = p.Reasoning.Signature
	}
	return block
}

func mapTools(call aipapi.Call) []anthropicTool {
	if len(call.Tools) == 0 {
		return nil
	}
	out := make([]anthropicTool, 0, len(call.Tools))
	for _, t := range call.Tools {
		var schema any
		if len(t.Parameters) > 0 {
			_ = json.Unmarshal(t.Parameters, &schema)
		}
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, anthropicTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return out
}

func mapToolChoice(call aipapi.Call) any {
	switch call.ToolChoice.Mode {
	case aipapi.ToolChoiceAuto:
		return map[string]any{"type": "auto"}
	case aipapi.ToolChoiceAny:
		return map[string]any{"type": "any"}
	case aipapi.ToolChoiceRequired:
		if call.ToolChoice.Name != "" {
			return map[string]any{"type": "tool", "name": call.ToolChoice.Name}
		}
		return map[string]any{"type": "any"}
	default:
		return nil
	}
}

func structToMap(req anthropicRequest) (map[string]any, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	m := make(map[string]any)
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func collectExtraBody(ext map[string]json.RawMessage) map[string]any {
	if len(ext) == 0 {
		return nil
	}
	prefixes := []string{"commandcode.extra_body.", "anthropic.extra_body."}
	out := make(map[string]any)
	for k, v := range ext {
		for _, prefix := range prefixes {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			field := strings.TrimPrefix(k, prefix)
			if field == "" || len(field) > 64 {
				continue
			}
			var val any
			if json.Unmarshal(v, &val) == nil {
				out[field] = val
			}
		}
	}
	return out
}

func isStreaming(call aipapi.Call) bool {
	return call.Invocation.DeliveryMode != aipapi.DeliveryModeNonStreaming &&
		call.Invocation.TransportMode != aipapi.TransportModeNonStreaming
}
