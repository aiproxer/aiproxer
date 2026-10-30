package service

import (
	"crypto/rand"
	"fmt"
	"maps"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// AdaptRequestBody applies the 5 connector-local request adaptations required by Qwen Portal:
// 1. Normalize string message content to typed text parts ([{"type": "text", "text": ...}]).
// 2. Preserve image URL objects ({"type": "image_url", "image_url": ...}).
// 3. Inject cache_control: {"type": "ephemeral"} on the last part of the system message.
// 4. Set top-level vl_high_resolution_images: true.
// 5. Inject Qwen session metadata at top-level request location (body["metadata"] with camelCase sessionId and promptId).
func AdaptRequestBody(body map[string]any, inv backendplugin.Invocation, call aipapi.Call) error {
	if body == nil {
		return nil
	}

	// 1-3. Message adaptations
	if rawMsgs, ok := body["messages"]; ok && rawMsgs != nil {
		normalizedMsgs, err := normalizeMessages(rawMsgs)
		if err != nil {
			return fmt.Errorf("qwen-oauth: adapt messages: %w", err)
		}
		body["messages"] = normalizedMsgs
	}

	// 4. High-resolution images for Qwen VL models
	body["vl_high_resolution_images"] = true

	// 5. Qwen session metadata at top-level with camelCase keys: sessionId, promptId
	metadataMap := make(map[string]any)

	// Resolve sessionId:
	sessionID := strings.TrimSpace(inv.ProxyOwnedSessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(call.Session.CorrelationID())
	}
	if sessionID == "" && inv.SafeMetadata != nil {
		if s, ok := inv.SafeMetadata["sessionId"]; ok && strings.TrimSpace(s) != "" {
			sessionID = strings.TrimSpace(s)
		} else if s, ok := inv.SafeMetadata["session_id"]; ok && strings.TrimSpace(s) != "" {
			sessionID = strings.TrimSpace(s)
		}
	}
	if sessionID == "" && call.Session.Metadata != nil {
		if s, ok := call.Session.Metadata["sessionId"]; ok && strings.TrimSpace(s) != "" {
			sessionID = strings.TrimSpace(s)
		} else if s, ok := call.Session.Metadata["session_id"]; ok && strings.TrimSpace(s) != "" {
			sessionID = strings.TrimSpace(s)
		}
	}
	if sessionID != "" {
		metadataMap["sessionId"] = sessionID
	}

	// Resolve promptId:
	promptID := ""
	if inv.SafeMetadata != nil {
		if p, ok := inv.SafeMetadata["promptId"]; ok && strings.TrimSpace(p) != "" {
			promptID = strings.TrimSpace(p)
		} else if p, ok := inv.SafeMetadata["prompt_id"]; ok && strings.TrimSpace(p) != "" {
			promptID = strings.TrimSpace(p)
		}
	}
	if promptID == "" && call.Session.Metadata != nil {
		if p, ok := call.Session.Metadata["promptId"]; ok && strings.TrimSpace(p) != "" {
			promptID = strings.TrimSpace(p)
		} else if p, ok := call.Session.Metadata["prompt_id"]; ok && strings.TrimSpace(p) != "" {
			promptID = strings.TrimSpace(p)
		}
	}
	if promptID == "" {
		if inv.RequestID != "" {
			promptID = strings.TrimSpace(inv.RequestID)
		} else if call.ID != "" {
			promptID = strings.TrimSpace(call.ID)
		} else {
			promptID = newUUID()
		}
	}
	if promptID != "" {
		metadataMap["promptId"] = promptID
	}

	if len(metadataMap) > 0 {
		body["metadata"] = metadataMap
	}

	return nil
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func normalizeMessages(rawMsgs any) ([]map[string]any, error) {
	var msgsList []map[string]any
	switch ms := rawMsgs.(type) {
	case []map[string]any:
		msgsList = make([]map[string]any, len(ms))
		for i, m := range ms {
			msgsList[i] = copyMap(m)
		}
	case []any:
		msgsList = make([]map[string]any, 0, len(ms))
		for _, item := range ms {
			if m, ok := item.(map[string]any); ok {
				msgsList = append(msgsList, copyMap(m))
			} else {
				return nil, fmt.Errorf("expected message to be map, got %T", item)
			}
		}
	default:
		return nil, fmt.Errorf("unexpected messages container type %T", rawMsgs)
	}

	systemIdx := -1

	for idx, msg := range msgsList {
		role, _ := msg["role"].(string)
		if systemIdx == -1 && role == "system" {
			systemIdx = idx
		}

		content := msg["content"]
		switch c := content.(type) {
		case string:
			msg["content"] = []map[string]any{
				{
					"type": "text",
					"text": c,
				},
			}
		case []any:
			parts := make([]map[string]any, 0, len(c))
			for _, p := range c {
				switch part := p.(type) {
				case string:
					parts = append(parts, map[string]any{
						"type": "text",
						"text": part,
					})
				case map[string]any:
					partCopy := copyMap(part)
					// Preserve image_url objects
					if imgURL, hasImg := part["image_url"].(map[string]any); hasImg {
						partCopy["image_url"] = copyMap(imgURL)
					}
					parts = append(parts, partCopy)
				default:
					return nil, fmt.Errorf("unexpected content part type %T", p)
				}
			}
			msg["content"] = parts
		case []map[string]any:
			parts := make([]map[string]any, len(c))
			for i, p := range c {
				partCopy := copyMap(p)
				if imgURL, hasImg := p["image_url"].(map[string]any); hasImg {
					partCopy["image_url"] = copyMap(imgURL)
				}
				parts[i] = partCopy
			}
			msg["content"] = parts
		}
	}

	// 3. Inject cache_control on the last part of the system message
	if systemIdx >= 0 && systemIdx < len(msgsList) {
		sysMsg := msgsList[systemIdx]
		if parts, ok := sysMsg["content"].([]map[string]any); ok && len(parts) > 0 {
			lastIdx := len(parts) - 1
			lastPart := copyMap(parts[lastIdx])
			lastPart["cache_control"] = map[string]any{
				"type": "ephemeral",
			}
			parts[lastIdx] = lastPart
			sysMsg["content"] = parts
		}
	}

	return msgsList, nil
}

func copyMap(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	maps.Copy(dst, src)
	return dst
}
