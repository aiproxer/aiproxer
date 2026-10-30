package acp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func sessionIDFromExtensions(call *aipapi.Call) string {
	if call == nil || call.Extensions == nil {
		return ""
	}
	raw, ok := call.Extensions[extSessionJSONKey]
	if !ok || len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func validateACPCall(call *aipapi.Call) error {
	if call == nil {
		return fmt.Errorf("acp: nil call")
	}
	if len(call.Tools) > 0 {
		return fmt.Errorf("acp: tools are not supported in the v1 prompt-turn subset")
	}
	return nil
}

// promptBlocksForCall maps the canonical call to ACP prompt content blocks (prompt-turn subset).
func promptBlocksForCall(call *aipapi.Call) ([]map[string]any, error) {
	if call == nil {
		return nil, fmt.Errorf("acp: nil call")
	}
	blocks := []map[string]any{}
	if t := aipapi.JoinInstructionText(call.Instructions); t != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": t})
	}
	for _, m := range call.Messages {
		switch m.Role {
		case aipapi.RoleSystem:
			for _, p := range m.Parts {
				if p.Kind != aipapi.PartText || strings.TrimSpace(p.Text) == "" {
					continue
				}
				blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
			}
		case aipapi.RoleUser:
			bs, err := userPartsToPromptBlocks(m.Parts)
			if err != nil {
				return nil, fmt.Errorf("acp: user prompt blocks: %w", err)
			}
			blocks = append(blocks, bs...)
		case aipapi.RoleAssistant:
			bs, err := assistantPartsToPromptBlocks(m.Parts)
			if err != nil {
				return nil, fmt.Errorf("acp: assistant prompt blocks: %w", err)
			}
			blocks = append(blocks, bs...)
		case aipapi.RoleTool:
			return nil, fmt.Errorf("acp: tool result messages are not supported in the v1 subset")
		default:
			return nil, fmt.Errorf("acp: unsupported message role %q", m.Role)
		}
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("acp: empty prompt after mapping")
	}
	return blocks, nil
}

func userPartsToPromptBlocks(parts []aipapi.Part) ([]map[string]any, error) {
	out := []map[string]any{}
	for _, p := range parts {
		switch p.Kind {
		case aipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, map[string]any{"type": "text", "text": p.Text})
		case aipapi.PartImageRef:
			res := map[string]any{"uri": p.ImageRef}
			if p.ImageMIME != "" {
				res["mimeType"] = p.ImageMIME
			}
			out = append(out, map[string]any{"type": "resource", "resource": res})
		case aipapi.PartFileRef:
			res := map[string]any{"uri": p.FileRef}
			if p.FileMIME != "" {
				res["mimeType"] = p.FileMIME
			}
			if p.FileName != "" {
				res["text"] = p.FileName
			}
			out = append(out, map[string]any{"type": "resource", "resource": res})
		case aipapi.PartJSON, aipapi.PartToolResult:
			return nil, fmt.Errorf("acp: unsupported user part kind %q in v1 subset", p.Kind)
		default:
			return nil, fmt.Errorf("acp: unsupported user part kind %q", p.Kind)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("acp: user message has no mappable parts")
	}
	return out, nil
}

func assistantPartsToPromptBlocks(parts []aipapi.Part) ([]map[string]any, error) {
	out := []map[string]any{}
	for _, p := range parts {
		switch p.Kind {
		case aipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, map[string]any{"type": "text", "text": p.Text})
		case aipapi.PartJSON:
			return nil, fmt.Errorf("acp: assistant json parts are not supported in the v1 subset")
		default:
			return nil, fmt.Errorf("acp: unsupported assistant part kind %q", p.Kind)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("acp: assistant message has no mappable text parts")
	}
	return out, nil
}
