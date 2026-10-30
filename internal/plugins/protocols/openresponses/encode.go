package openresponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func encodeRequestParam(call aipapi.Call, includeReasoning bool) (map[string]json.RawMessage, error) {
	var wireItems []WireItem
	for i, item := range call.Items {
		wItem, err := EncodeItem(item)
		if err != nil {
			return nil, fmt.Errorf("encode item[%d]: %w", i, err)
		}
		wireItems = append(wireItems, wItem)
	}

	inputBytes, err := json.Marshal(ensureNonNilSlice(wireItems))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal input items: %v", ErrEncodeFailed, err)
	}

	wireTools := encodeTools(call.Tools)

	toolChoiceBytes, err := encodeToolChoice(call.ToolChoice)
	if err != nil {
		return nil, err
	}

	textBytes, err := encodeTextFormat(call.Options.ResponseMIMEType)
	if err != nil {
		return nil, err
	}

	var reasoningBytes json.RawMessage
	if includeReasoning {
		reasoningBytes, err = encodeReasoningEffort(call.Options.ReasoningEffort)
		if err != nil {
			return nil, err
		}
	}

	param := WireResponseParam{
		Input:             inputBytes,
		Tools:             wireTools,
		ToolChoice:        toolChoiceBytes,
		Temperature:       call.Options.Temperature,
		TopP:              call.Options.TopP,
		MaxOutputTokens:   call.Options.MaxOutputTokens,
		ParallelToolCalls: call.Options.ParallelToolCalls,
		Text:              textBytes,
		Reasoning:         reasoningBytes,
	}

	if value, err := call.PromptCacheKeyValue(); err != nil {
		return nil, fmt.Errorf("%w: prompt cache key: %v", ErrEncodeFailed, err)
	} else if value != "" {
		v := value
		param.PromptCacheKey = &v
	}

	rawMap := make(map[string]json.RawMessage)
	paramBytes, err := json.Marshal(param)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodeFailed, err)
	}
	if err := json.Unmarshal(paramBytes, &rawMap); err != nil {
		// Unreachable: paramBytes was produced by json.Marshal(param) above.
		return nil, fmt.Errorf("%w: %v", ErrEncodeFailed, err)
	}
	return rawMap, nil
}

// EncodeRequest converts a canonical aipapi.Call into WireResponseParam and marshals it to JSON bytes.
func EncodeRequest(call aipapi.Call) ([]byte, error) {
	rawMap, err := encodeRequestParam(call, false)
	if err != nil {
		return nil, err
	}

	// Merge top-level extensions
	for k, v := range call.Extensions {
		if !json.Valid(v) {
			return nil, fmt.Errorf("%w: invalid extension %q", ErrEncodeFailed, k)
		}
		rawMap[k] = cloneBytes(v)
	}

	return json.Marshal(rawMap)
}

func encodeTools(tools []aipapi.ToolDef) []WireTool {
	var wireTools []WireTool
	for _, t := range tools {
		wireTools = append(wireTools, WireTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  cloneBytes(t.Parameters),
		})
	}
	return wireTools
}

func encodeToolChoice(tc aipapi.ToolChoice) (json.RawMessage, error) {
	if len(tc.AllowedTools) > 0 {
		refs := make([]WireToolChoiceAllowedToolRef, 0, len(tc.AllowedTools))
		for _, name := range tc.AllowedTools {
			refs = append(refs, WireToolChoiceAllowedToolRef{Type: "function", Name: name})
		}
		mode := "auto"
		switch tc.Mode {
		case aipapi.ToolChoiceNone:
			mode = "none"
		case aipapi.ToolChoiceAny:
			mode = "required"
		}
		tcBytes, err := json.Marshal(WireToolChoiceAllowedTools{
			Type:  "allowed_tools",
			Tools: refs,
			Mode:  mode,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: failed to marshal tool_choice: %v", ErrEncodeFailed, err)
		}
		return tcBytes, nil
	} else if tc.Mode != "" {
		switch tc.Mode {
		case aipapi.ToolChoiceAuto:
			return json.RawMessage(`"auto"`), nil
		case aipapi.ToolChoiceNone:
			return json.RawMessage(`"none"`), nil
		case aipapi.ToolChoiceAny:
			return json.RawMessage(`"required"`), nil
		case aipapi.ToolChoiceRequired:
			if tc.Name != "" {
				tcBytes, err := json.Marshal(WireToolChoiceFunction{
					Type: "function",
					Name: tc.Name,
				})
				if err != nil {
					return nil, fmt.Errorf("%w: failed to marshal tool_choice: %v", ErrEncodeFailed, err)
				}
				return tcBytes, nil
			}
			return json.RawMessage(`"required"`), nil
		}
	}
	return nil, nil
}

func encodeTextFormat(mime string) (json.RawMessage, error) {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if mime == "" {
		return nil, nil
	}
	var typ string
	switch mime {
	case "text/plain":
		typ = "text"
	case "application/json":
		typ = "json_object"
	default:
		return nil, fmt.Errorf("%w: response MIME type %q is not representable", ErrEncodeFailed, mime)
	}
	textBytes, err := json.Marshal(map[string]any{"format": map[string]string{"type": typ}})
	if err != nil {
		return nil, fmt.Errorf("%w: marshal text: %v", ErrEncodeFailed, err)
	}
	return textBytes, nil
}

func encodeReasoningEffort(effort string) (json.RawMessage, error) {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return nil, nil
	}
	b, err := json.Marshal(struct {
		Effort string `json:"effort"`
	}{Effort: effort})
	if err != nil {
		return nil, fmt.Errorf("%w: marshal reasoning: %v", ErrEncodeFailed, err)
	}
	return b, nil
}

// EncodeItem converts a canonical aipapi.Item into WireItem.
func EncodeItem(item aipapi.Item) (WireItem, error) {
	status := string(item.Status)
	if status == "" {
		status = "completed"
	}

	switch item.Kind {
	case aipapi.ItemKindMessage:
		var wireParts []WireContentPart
		for _, cp := range item.Content {
			wPart := encodeContentPart(cp, item.Role)
			wireParts = append(wireParts, wPart)
		}
		partsBytes, err := json.Marshal(ensureNonNilSlice(wireParts))
		if err != nil {
			return WireItem{}, fmt.Errorf("marshal message content: %w", err)
		}

		return WireItem{
			ID:      item.ID,
			Type:    "message",
			Status:  status,
			Role:    string(item.Role),
			Phase:   string(item.Phase),
			Content: partsBytes,
		}, nil

	case aipapi.ItemKindItemReference:
		refID := ""
		if item.Reference != nil {
			refID = item.Reference.ID
		}
		return WireItem{
			ID:     refID,
			Type:   "item_reference",
			Status: status,
		}, nil

	case aipapi.ItemKindToolCall:
		callID := ""
		name := ""
		var args json.RawMessage
		if item.ToolCall != nil {
			callID = item.ToolCall.CallID
			name = item.ToolCall.Name
			args = wireToolArguments(item.ToolCall.Arguments)
		}
		return WireItem{
			ID:        item.ID,
			Type:      "function_call",
			Status:    status,
			CallID:    callID,
			Name:      name,
			Arguments: args,
		}, nil

	case aipapi.ItemKindToolResult:
		callID := ""
		name := ""
		outputBytes := json.RawMessage(`""`)
		if item.ToolResult != nil {
			callID = item.ToolResult.CallID
			name = item.ToolResult.Name
			if item.ToolResult.Output != "" {
				b, _ := json.Marshal(item.ToolResult.Output)
				outputBytes = b
			} else if len(item.ToolResult.Parts) > 0 {
				var wireParts []WireContentPart
				for _, cp := range item.ToolResult.Parts {
					wireParts = append(wireParts, encodeContentPart(cp, aipapi.RoleTool))
				}
				b, _ := json.Marshal(wireParts)
				outputBytes = b
			}
		}
		return WireItem{
			ID:     item.ID,
			Type:   "function_call_output",
			Status: status,
			CallID: callID,
			Name:   name,
			Output: outputBytes,
		}, nil

	case aipapi.ItemKindReasoning:
		var reasoningBytes json.RawMessage
		var summary, content json.RawMessage
		var signature string
		var opaque json.RawMessage
		var encrypted json.RawMessage
		var encryptedPresent bool
		if item.Reasoning != nil && item.Reasoning.Reasoning != nil {
			r := item.Reasoning.Reasoning
			if aipapi.ReasoningHasExactResponsesFields(r) {
				if r.SummaryPresent || len(r.Summary) > 0 {
					summary = cloneBytes(r.Summary)
				}
				if r.ContentPresent || len(r.Content) > 0 {
					content = cloneBytes(r.Content)
				}
				encrypted = cloneBytes(r.EncryptedContent)
				encryptedPresent = r.EncryptedContentPresent
				signature = r.Signature
				opaque = cloneBytes(r.Opaque)
				if r.Text != "" || r.Signature != "" || len(r.Opaque) > 0 {
					obj := map[string]any{}
					if r.Text != "" {
						obj["text"] = r.Text
					}
					if r.Signature != "" {
						obj["signature"] = r.Signature
					}
					if len(r.Opaque) > 0 {
						obj["opaque"] = json.RawMessage(r.Opaque)
					}
					reasoningBytes, _ = json.Marshal(obj)
				}
			} else if r.Text != "" && r.Signature == "" && len(r.Opaque) == 0 {
				b, _ := json.Marshal(r.Text)
				reasoningBytes = b
			} else {
				obj := map[string]any{}
				if r.Text != "" {
					obj["text"] = r.Text
				}
				if r.Signature != "" {
					signature = r.Signature
					obj["signature"] = r.Signature
				}
				if len(r.Opaque) > 0 {
					opaque = cloneBytes(r.Opaque)
					obj["opaque"] = json.RawMessage(r.Opaque)
				}
				reasoningBytes, _ = json.Marshal(obj)
			}
		}
		return WireItem{
			ID:                               item.ID,
			Type:                             "reasoning",
			Status:                           status,
			Reasoning:                        reasoningBytes,
			Summary:                          summary,
			Content:                          content,
			Signature:                        signature,
			Opaque:                           opaque,
			ReasoningEncryptedContent:        encrypted,
			ReasoningEncryptedContentPresent: encryptedPresent,
		}, nil

	case aipapi.ItemKindCompaction:
		encID := ""
		dialect := ""
		implementor := ""
		encrypted := ""
		var opaque json.RawMessage
		if item.Compaction != nil {
			encID = item.Compaction.EncapsulatedID
			dialect = item.Compaction.Dialect
			implementor = item.Compaction.Implementor
			encrypted = item.Compaction.EncryptedContent
			opaque = cloneBytes(item.Compaction.Opaque)
		}
		return WireItem{
			ID:               item.ID,
			Type:             "compaction",
			Status:           status,
			EncapsulatedID:   encID,
			Dialect:          dialect,
			Implementor:      implementor,
			EncryptedContent: encrypted,
			Opaque:           opaque,
		}, nil

	case aipapi.ItemKindExtension:
		wireType := "extension"
		ns := ""
		direction := ""
		var data json.RawMessage
		if item.Extension != nil {
			wireType = item.Extension.Type
			ns = item.Extension.Namespace
			direction = item.Extension.Direction
			data = cloneBytes(item.Extension.Data)
		}
		return WireItem{
			ID:        item.ID,
			Type:      wireType,
			Status:    status,
			Namespace: ns,
			Direction: direction,
			Data:      data,
		}, nil
	}

	return WireItem{}, fmt.Errorf("%w: unknown item kind %q", ErrEncodeFailed, item.Kind)
}

// wireToolArguments normalizes canonical tool call arguments to the pinned
// profile's JSON-string wire form: a JSON string stays verbatim; any other
// canonical argument document (or partial argument text) is wrapped into a
// JSON string. This matches the official 2026-04-24 function_call contract,
// where `arguments` is always a JSON string.
func wireToolArguments(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage(`""`)
	}
	if trimmed[0] == '"' && json.Valid(trimmed) {
		return json.RawMessage(trimmed)
	}
	b, _ := json.Marshal(string(trimmed))
	return b
}

// encodeExtensionContentPart emits opaque extension data without parsing or
// re-marshaling it. Identity is carried by the enclosing wire discriminator.
func encodeExtensionContentPart(e *aipapi.ExtensionContentPart) json.RawMessage {
	if e == nil {
		return nil
	}
	return cloneBytes(e.Data)
}

// encodeContentPart converts canonical aipapi.ContentPart into WireContentPart.
func encodeContentPart(cp aipapi.ContentPart, role aipapi.Role) WireContentPart {
	switch cp.Kind {
	case aipapi.ContentPartText:
		partType := "input_text"
		if role == aipapi.RoleAssistant {
			partType = "output_text"
		}
		part := WireContentPart{
			Type: partType,
			Text: cp.Text,
		}
		if partType == "output_text" {
			// The pinned profile requires the annotations array on output_text
			// content parts in a response resource.
			part.Annotations = json.RawMessage(`[]`)
		}
		return part

	case aipapi.ContentPartImageRef:
		imgBytes, _ := json.Marshal(cp.ImageRef)
		return WireContentPart{
			Type:     "input_image",
			ImageURL: imgBytes,
		}

	case aipapi.ContentPartFileRef:
		part := WireContentPart{Type: "input_file", Filename: cp.FileName}
		if cp.FileRef != "" {
			part.FileURL, _ = json.Marshal(cp.FileRef)
		}
		if cp.FileData != "" {
			part.FileData, _ = json.Marshal(cp.FileData)
		}
		return part

	case aipapi.ContentPartVideoRef:
		videoBytes, _ := json.Marshal(cp.VideoRef)
		return WireContentPart{
			Type:     "input_video",
			VideoURL: videoBytes,
		}

	case aipapi.ContentPartExtension:
		if cp.Extension == nil {
			return WireContentPart{Type: "input_text", Text: cp.Text}
		}
		return WireContentPart{
			rawExtension: encodeExtensionContentPart(cp.Extension),
		}

	case aipapi.ContentPartRefusal:
		return WireContentPart{
			Type:    "refusal",
			Refusal: cp.Refusal,
		}
	}

	partType := "input_text"
	if role == aipapi.RoleAssistant {
		partType = "output_text"
	}
	return WireContentPart{
		Type: partType,
		Text: cp.Text,
	}
}

// OutboundEncodeOptions holds options for encoding an outbound request.
type OutboundEncodeOptions struct {
	Model             string
	Stream            bool
	IncludeExtensions bool
}

// EncodeOutboundRequest encodes a canonical aipapi.Call into an outbound request JSON payload.
func EncodeOutboundRequest(call aipapi.Call, opts OutboundEncodeOptions) ([]byte, error) {
	if call.Options.Verbosity != "" {
		return nil, fmt.Errorf("%w: verbosity is not representable on the pinned OpenResponses profile", ErrEncodeFailed)
	}

	rawMap, err := encodeRequestParam(call, true)
	if err != nil {
		return nil, err
	}

	// Model key
	modelBytes, err := json.Marshal(opts.Model)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal model: %v", ErrEncodeFailed, err)
	}
	rawMap["model"] = modelBytes

	// Stream key
	if opts.Stream {
		rawMap["stream"] = json.RawMessage("true")
	} else {
		delete(rawMap, "stream")
	}

	// Merge top-level extensions
	if opts.IncludeExtensions {
		for k, v := range call.Extensions {
			if !json.Valid(v) {
				return nil, fmt.Errorf("%w: invalid extension %q", ErrEncodeFailed, k)
			}
			if k == "model" || k == "stream" {
				return nil, fmt.Errorf("%w: extension key %q collides with standard parameter", ErrEncodeFailed, k)
			}
			rawMap[k] = cloneBytes(v)
		}
	}

	return json.Marshal(rawMap)
}
