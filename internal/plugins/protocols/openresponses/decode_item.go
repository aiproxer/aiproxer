package openresponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/jsonpresence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Standard item type discriminators in official OpenResponses 2026-04-24.
var standardItemTypes = map[string]bool{
	"message":              true,
	"item_reference":       true,
	"function_call":        true,
	"function_call_output": true,
	"function_output":      true,
	"reasoning":            true,
	"compaction":           true,
}

// DecodeReplayItem decodes one item from a client-replayed history and applies
// the proxy's own output normalization. A tool-only turn can announce an
// assistant message item before the model emits its tool call, so the proxy's
// completed output may contain an assistant message whose only content is an
// empty text part. Replaying that output unchanged must not fail canonical
// validation: empty text parts are omitted, and an assistant message left with
// no content is dropped entirely. Nonempty content, every non-text part, and
// every non-assistant message are preserved in order and stay subject to
// canonical validation.
func DecodeReplayItem(w WireItem, limits Limits) (aipapi.Item, bool, error) {
	item, err := DecodeItem(w, limits)
	if err != nil {
		return aipapi.Item{}, false, err
	}
	if item.Kind != aipapi.ItemKindMessage || item.Role != aipapi.RoleAssistant || item.Phase != "" {
		return item, true, nil
	}
	if len(item.Content) == 0 {
		return aipapi.Item{}, false, nil
	}
	kept := item.Content[:0]
	for _, part := range item.Content {
		if part.Kind == aipapi.ContentPartText && part.Text == "" {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		return aipapi.Item{}, false, nil
	}
	item.Content = kept
	return item, true, nil
}

// DecodeItem converts a WireItem into a canonical aipapi.Item under the
// caller-provided operational limits.
func DecodeItem(wire WireItem, limits Limits) (aipapi.Item, error) {
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	if wire.Type == "" {
		return aipapi.Item{}, fmt.Errorf("%w: item missing type discriminator", ErrDecodeFailed)
	}
	if wire.Type == "item_reference" {
		if err := ValidateContinuationRef(wire.ID, limits); err != nil {
			return aipapi.Item{}, err
		}
	}

	// Validate discriminator
	if !standardItemTypes[wire.Type] {
		if !isPrefixedType(wire.Type) {
			return aipapi.Item{}, fmt.Errorf("%w: %q", ErrUnknownDiscriminator, wire.Type)
		}
		// Treat non-standard vendor-prefixed discriminator as Extension item
		status := aipapi.ItemStatus(wire.Status)
		if status == "received" {
			status = aipapi.ItemStatusCompleted
		}
		if err := ValidateOpaquePayloadSize(len(wire.Data), limits); err != nil {
			return aipapi.Item{}, err
		}
		return aipapi.Item{
			Kind:   aipapi.ItemKindExtension,
			ID:     wire.ID,
			Status: status,
			Extension: &aipapi.OpaqueExtension{
				Namespace:   wire.Namespace,
				Type:        wire.Type,
				Implementor: wire.Implementor,
				Direction:   wire.Direction,
				Data:        cloneBytes(wire.Data),
			},
		}, nil
	}

	status := aipapi.ItemStatus(wire.Status)
	if status == "received" {
		status = aipapi.ItemStatusCompleted
	}

	item := aipapi.Item{
		ID:     wire.ID,
		Status: status,
	}

	switch wire.Type {
	case "message":
		item.Kind = aipapi.ItemKindMessage
		item.Role = aipapi.Role(wire.Role)
		item.Phase = aipapi.AssistantPhase(wire.Phase)

		if jsonpresence.IsPresentNonNullJSON(wire.Content) {
			parts, err := decodeContentParts(wire.Content)
			if err != nil {
				return aipapi.Item{}, err
			}
			item.Content = parts
		}

	case "item_reference":
		item.Kind = aipapi.ItemKindItemReference
		item.Reference = &aipapi.ItemReference{
			ID: wire.ID,
		}

	case "function_call":
		item.Kind = aipapi.ItemKindToolCall
		callID := wire.CallID
		if callID == "" {
			callID = wire.ID
		}
		item.ToolCall = &aipapi.ToolCallItem{
			CallID:    callID,
			Name:      wire.Name,
			Arguments: cloneBytes(wire.Arguments),
		}

	case "function_call_output", "function_output":
		item.Kind = aipapi.ItemKindToolResult
		callID := wire.CallID
		if callID == "" {
			callID = wire.ID
		}

		tr := &aipapi.ToolResultItem{
			CallID: callID,
			Name:   wire.Name,
		}

		if len(bytes.TrimSpace(wire.Output)) > 0 && jsonpresence.IsJSONNull(bytes.TrimSpace(wire.Output)) {
			return aipapi.Item{}, fmt.Errorf("%w: tool_result output cannot be null", ErrDecodeFailed)
		}
		if jsonpresence.IsPresentNonNullJSON(wire.Output) {
			trimmed := bytes.TrimSpace(wire.Output)
			if len(trimmed) > 0 && trimmed[0] == '"' {
				var outStr string
				if err := json.Unmarshal(trimmed, &outStr); err == nil {
					tr.Output = outStr
				} else {
					// A malformed JSON string is not raw text. Reject it rather than
					// persisting syntax quotes or an injected structural payload.
					return aipapi.Item{}, fmt.Errorf("%w: tool_result output must be a valid string", ErrDecodeFailed)
				}
			} else if len(trimmed) > 0 && trimmed[0] == '[' {
				parts, err := decodeContentParts(wire.Output)
				if err != nil {
					return aipapi.Item{}, fmt.Errorf("tool_result parts: %w", err)
				}
				tr.Parts = parts
			} else {
				if json.Valid(trimmed) {
					return aipapi.Item{}, fmt.Errorf("%w: tool_result output cannot be a non-string JSON primitive", ErrDecodeFailed)
				}
				if len(trimmed) > 0 && trimmed[0] == '{' {
					return aipapi.Item{}, fmt.Errorf("%w: tool_result output object is not a string or content array", ErrDecodeFailed)
				}
				tr.Output = string(trimmed)
			}
		}
		item.ToolResult = tr

	case "reasoning":
		item.Kind = aipapi.ItemKindReasoning
		rItem := &aipapi.ReasoningItem{
			Reasoning: &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialect("openresponses.reasoning.v1"),
			},
		}

		if len(bytes.TrimSpace(wire.Reasoning)) > 0 && jsonpresence.IsJSONNull(bytes.TrimSpace(wire.Reasoning)) {
			return aipapi.Item{}, fmt.Errorf("%w: reasoning cannot be null", ErrDecodeFailed)
		}
		if wire.Signature != "" {
			rItem.Reasoning.Signature = wire.Signature
		}
		if jsonpresence.IsPresentNonNullJSON(wire.Opaque) {
			rItem.Reasoning.Opaque = cloneBytes(wire.Opaque)
		}
		if len(wire.Summary) > 0 {
			rItem.Reasoning.Summary = cloneBytes(wire.Summary)
			rItem.Reasoning.SummaryPresent = wire.SummaryPresent || jsonpresence.IsPresentNonNullJSON(wire.Summary)
		}
		if len(wire.Content) > 0 {
			rItem.Reasoning.Content = cloneBytes(wire.Content)
			rItem.Reasoning.ContentPresent = wire.ContentPresent || jsonpresence.IsPresentNonNullJSON(wire.Content)
		}
		if wire.ReasoningEncryptedContentPresent {
			rItem.Reasoning.EncryptedContentPresent = true
			rItem.Reasoning.EncryptedContent = cloneBytes(wire.ReasoningEncryptedContent)
		}
		if jsonpresence.IsPresentNonNullJSON(wire.Reasoning) {
			text, signature, opaque, err := decodeReasoningPayload(wire.Reasoning)
			if err != nil {
				return aipapi.Item{}, err
			}
			rItem.Reasoning.Text = text
			if signature != "" {
				rItem.Reasoning.Signature = signature
			}
			if len(opaque) > 0 {
				rItem.Reasoning.Opaque = opaque
			}
		} else if len(bytes.TrimSpace(wire.Content)) > 0 && jsonpresence.IsJSONNull(bytes.TrimSpace(wire.Content)) {
			return aipapi.Item{}, fmt.Errorf("%w: reasoning content cannot be null", ErrDecodeFailed)
		} else if jsonpresence.IsPresentNonNullJSON(wire.Content) {
			trimmed := bytes.TrimSpace(wire.Content)
			if len(trimmed) > 0 && trimmed[0] == '"' {
				var cStr string
				if err := json.Unmarshal(trimmed, &cStr); err != nil {
					return aipapi.Item{}, fmt.Errorf("%w: reasoning content is not a string union", ErrDecodeFailed)
				}
				rItem.Reasoning.Text = cStr
			} else if len(trimmed) > 0 && trimmed[0] == '[' {
				parts, err := decodeContentParts(wire.Content)
				if err != nil {
					return aipapi.Item{}, fmt.Errorf("%w: reasoning content: %v", ErrDecodeFailed, err)
				}
				var sb strings.Builder
				for _, p := range parts {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(p.Text)
				}
				rItem.Reasoning.Text = sb.String()
			} else {
				if json.Valid(trimmed) {
					return aipapi.Item{}, fmt.Errorf("%w: reasoning content cannot be a non-string JSON primitive", ErrDecodeFailed)
				}
				return aipapi.Item{}, fmt.Errorf("%w: reasoning content must be a string or content array", ErrDecodeFailed)
			}
		}
		item.Reasoning = rItem

	case "compaction":
		if err := ValidateOpaquePayloadSize(len(wire.Opaque), limits); err != nil {
			return aipapi.Item{}, err
		}
		if len(wire.EncryptedContent) > aipapi.MaxCompactionEncryptedContentBytes {
			return aipapi.Item{}, fmt.Errorf("%w: compaction encrypted_content exceeds %d bytes", ErrDecodeFailed, aipapi.MaxCompactionEncryptedContentBytes)
		}
		item.Kind = aipapi.ItemKindCompaction
		item.Compaction = &aipapi.CompactionItem{
			EncapsulatedID:   wire.EncapsulatedID,
			Dialect:          wire.Dialect,
			Implementor:      wire.Implementor,
			EncryptedContent: wire.EncryptedContent,
			Opaque:           cloneBytes(wire.Opaque),
		}
	}

	return item, nil
}

func decodeReasoningPayload(raw []byte) (text, signature string, opaque json.RawMessage, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", "", nil, fmt.Errorf("%w: reasoning must be a valid string", ErrDecodeFailed)
		}
		return text, "", nil, nil
	}
	var obj struct {
		Text      string            `json:"text"`
		Signature string            `json:"signature"`
		Opaque    json.RawMessage   `json:"opaque"`
		Summary   []WireContentPart `json:"summary"`
		Content   []WireContentPart `json:"content"`
		Encrypted string            `json:"encrypted_content"`
	}
	if !json.Valid(trimmed) {
		// Older OpenResponses adapters carried plain reasoning text in the raw
		// field. Preserve that established compatibility form.
		return string(trimmed), "", nil, nil
	}
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return "", "", nil, fmt.Errorf("%w: reasoning must be a valid string or object", ErrDecodeFailed)
	}
	text, signature, opaque = obj.Text, obj.Signature, cloneBytes(obj.Opaque)
	for _, part := range append(obj.Summary, obj.Content...) {
		if part.Text == "" {
			continue
		}
		if text != "" {
			text += "\n"
		}
		text += part.Text
	}
	if len(opaque) == 0 && obj.Encrypted != "" {
		opaque, _ = json.Marshal(obj.Encrypted)
	}
	return text, signature, opaque, nil
}
