package openresponses

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/jsonpresence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	MaxRequestBytes = 8 * 1024 * 1024 // 8 MiB max request body
	MaxJSONDepth    = 64
)

// isPrefixedType returns true if the discriminator is a vendor-prefixed extension (containing ':' or '/').
func isPrefixedType(t string) bool {
	return strings.Contains(t, ":") || strings.Contains(t, "/")
}

// DecodeRequest unmarshals JSON request bytes into WireResponseParam and converts it to a canonical aipapi.Call.
// An omitted limits argument uses DefaultLimits for compatibility with existing
// internal callers; production frontends may pass their validated limits.
func DecodeRequest(data []byte, configured ...Limits) (*WireResponseParam, aipapi.Call, error) {
	limits := DefaultLimits()
	if len(configured) > 0 && configured[0] != (Limits{}) {
		limits = configured[0]
	}
	if limits.MaxItemDepth <= 0 {
		limits.MaxItemDepth = MaxJSONDepth
	}
	if len(data) == 0 {
		return nil, aipapi.Call{}, fmt.Errorf("%w: empty request payload", ErrDecodeFailed)
	}
	if err := ValidateRequestBytes(data, limits); err != nil {
		return nil, aipapi.Call{}, err
	}

	// Perform strict JSON validation for UTF-8, duplicate keys, depth, and trailing data.
	if err := validateJSONStrictWithLimits(data, limits); err != nil {
		return nil, aipapi.Call{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var rawMap map[string]json.RawMessage
	if err := decoder.Decode(&rawMap); err != nil {
		return nil, aipapi.Call{}, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}

	// Unmarshal into WireResponseParam
	var param WireResponseParam
	if err := json.Unmarshal(data, &param); err != nil {
		return nil, aipapi.Call{}, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}

	// Check for conflicting legacy fields (e.g. "messages" or "instructions" alongside "input")
	if _, hasMessages := rawMap["messages"]; hasMessages {
		if _, hasInput := rawMap["input"]; hasInput {
			return nil, aipapi.Call{}, ErrDuplicateAuthority
		}
	}

	// Reject background=true
	if param.Background != nil && *param.Background {
		return nil, aipapi.Call{}, ErrUnsupportedBackground
	}

	// Collect extra top-level fields
	param.ExtraFields = make(map[string]json.RawMessage)
	knownFields := map[string]bool{
		"model": true, "input": true, "instructions": true, "tools": true,
		"tool_choice": true, "parallel_tool_calls": true, "temperature": true,
		"top_p": true, "max_output_tokens": true, "max_tool_calls": true,
		"truncation": true, "text": true, "reasoning": true, "store": true,
		"background": true, "previous_response_id": true, "metadata": true,
		"service_tier": true, "safety_identifier": true, "prompt_cache_key": true,
		"prompt_cache_retention": true, "messages": true,
		// Pinned standard create controls are recognized typed fields. The
		// canonical call cannot represent them losslessly (no speculative
		// generation fields), so frontend admission rejects non-null values as
		// unsupported standard controls before any network work.
		"include": true, "presence_penalty": true, "frequency_penalty": true,
		"stream_options": true, "top_logprobs": true,
	}

	extensions := make(map[string]json.RawMessage)
	for k, v := range rawMap {
		if !knownFields[k] {
			param.ExtraFields[k] = cloneBytes(v)
			if isPrefixedType(k) {
				extensions[k] = cloneBytes(v)
			} else {
				return nil, aipapi.Call{}, fmt.Errorf("%w: unknown top-level field %q", ErrUnknownDiscriminator, k)
			}
		}
	}

	// Decode input into canonical items
	var canonicalItems []aipapi.Item
	if jsonpresence.IsPresentNonNullJSON(param.Input) {
		trimmed := bytes.TrimSpace(param.Input)
		if len(trimmed) > 0 && trimmed[0] == '"' {
			// Input is a string shortcut
			var textInput string
			if err := json.Unmarshal(trimmed, &textInput); err != nil {
				return nil, aipapi.Call{}, fmt.Errorf("%w: invalid string input", ErrDecodeFailed)
			}
			canonicalItems = []aipapi.Item{
				{
					Kind:   aipapi.ItemKindMessage,
					Status: aipapi.ItemStatusCompleted,
					Role:   aipapi.RoleUser,
					Content: []aipapi.ContentPart{
						{
							Kind: aipapi.ContentPartText,
							Text: textInput,
						},
					},
				},
			}
		} else if len(trimmed) > 0 && trimmed[0] == '[' {
			// Input is an item array
			var wireItems []WireItem
			if err := json.Unmarshal(trimmed, &wireItems); err != nil {
				return nil, aipapi.Call{}, fmt.Errorf("%w: invalid item array input: %v", ErrDecodeFailed, err)
			}
			if err := ValidateItemCount(len(wireItems), limits); err != nil {
				return nil, aipapi.Call{}, err
			}
			continuationRefs := 0
			for _, w := range wireItems {
				if w.Type == "item_reference" {
					continuationRefs++
				}
			}
			if err := ValidateContinuationRefCount(continuationRefs, limits); err != nil {
				return nil, aipapi.Call{}, err
			}
			for i, w := range wireItems {
				item, err := DecodeItem(w, limits)
				if err != nil {
					return nil, aipapi.Call{}, fmt.Errorf("item[%d]: %w", i, err)
				}
				canonicalItems = append(canonicalItems, item)
			}
		} else {
			return nil, aipapi.Call{}, fmt.Errorf("%w: input must be string or item array", ErrDecodeFailed)
		}
	} else {
		// Input absent/null; allowed only if previous_response_id is set. Keep a
		// non-nil empty slice so the canonical call remains item-authoritative
		// and its tools/options still undergo Validate below.
		if param.PreviousResponseID == nil || strings.TrimSpace(*param.PreviousResponseID) == "" {
			return nil, aipapi.Call{}, fmt.Errorf("%w: input is required when previous_response_id is absent", ErrDecodeFailed)
		}
		if err := ValidateContinuationRef(strings.TrimSpace(*param.PreviousResponseID), limits); err != nil {
			return nil, aipapi.Call{}, err
		}
		canonicalItems = []aipapi.Item{}
	}
	if param.PreviousResponseID != nil && strings.TrimSpace(*param.PreviousResponseID) != "" {
		if err := ValidateContinuationRef(strings.TrimSpace(*param.PreviousResponseID), limits); err != nil {
			return nil, aipapi.Call{}, err
		}
	}

	// The pinned instructions control maps losslessly into a leading canonical
	// system message item so it forwards through the ordered item trajectory on
	// both create and compact. Null and empty instructions are treated as
	// absent; the exact instruction text is preserved in the item.
	if param.Instructions != nil && strings.TrimSpace(*param.Instructions) != "" {
		canonicalItems = append([]aipapi.Item{{
			Kind:    aipapi.ItemKindMessage,
			Status:  aipapi.ItemStatusCompleted,
			Role:    aipapi.RoleSystem,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: *param.Instructions}},
		}}, canonicalItems...)
	}

	// Decode tools
	var canonicalTools []aipapi.ToolDef
	for i, wt := range param.Tools {
		if wt.Type != "function" {
			return nil, aipapi.Call{}, fmt.Errorf("%w: tool[%d] has unsupported type %q", ErrDecodeFailed, i, wt.Type)
		}
		if wt.Name == "" {
			return nil, aipapi.Call{}, fmt.Errorf("%w: tool[%d] missing name", ErrDecodeFailed, i)
		}
		if err := ValidateSchemaSize(len(wt.Parameters), limits); err != nil {
			return nil, aipapi.Call{}, fmt.Errorf("tool[%d]: %w", i, err)
		}
		canonicalTools = append(canonicalTools, aipapi.ToolDef{
			Name:        wt.Name,
			Description: wt.Description,
			Parameters:  cloneBytes(wt.Parameters),
		})
	}

	responseMIME, err := decodeTextFormat(param.Text)
	if err != nil {
		return nil, aipapi.Call{}, err
	}

	// Decode tool_choice
	var toolChoice aipapi.ToolChoice
	if jsonpresence.IsPresentNonNullJSON(param.ToolChoice) {
		tc, err := decodeToolChoice(param.ToolChoice)
		if err != nil {
			return nil, aipapi.Call{}, err
		}
		toolChoice = tc
	}

	// Decode GenerationOptions
	reasoningEffort, err := decodeReasoningControl(param.Reasoning)
	if err != nil {
		return nil, aipapi.Call{}, err
	}
	options := aipapi.GenerationOptions{
		Temperature:       param.Temperature,
		TopP:              param.TopP,
		MaxOutputTokens:   param.MaxOutputTokens,
		ParallelToolCalls: param.ParallelToolCalls,
		ReasoningEffort:   reasoningEffort,
		ResponseMIMEType:  responseMIME,
	}

	call := aipapi.Call{
		Items:              canonicalItems,
		PreviousResponseID: strings.TrimSpace(stringValue(param.PreviousResponseID)),
		Tools:              canonicalTools,
		ToolChoice:         toolChoice,
		Options:            options,
		Extensions:         extensions,
	}

	if err := call.Validate(); err != nil {
		return nil, aipapi.Call{}, fmt.Errorf("%w: canonical call validation failed: %w", ErrDecodeFailed, err)
	}

	return &param, call, nil
}

// decodeReasoningControl accepts only the lossless subset represented by the
// canonical contract: an object containing only a string effort. Null and
// omission are equivalent because GenerationOptions has no presence carrier.
func decodeReasoningControl(raw json.RawMessage) (string, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return "", nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err == nil {
			err = errors.New("must be a JSON object")
		}
		return "", fmt.Errorf("%w: reasoning must be an object: %v", ErrDecodeFailed, err)
	}
	for key := range fields {
		if key != "effort" {
			return "", fmt.Errorf("%w: reasoning field %q is unsupported", ErrDecodeFailed, key)
		}
	}
	rawEffort, ok := fields["effort"]
	if !ok || jsonpresence.IsAbsentOrJSONNull(rawEffort) {
		return "", nil
	}
	var effort string
	if err := json.Unmarshal(rawEffort, &effort); err != nil {
		return "", fmt.Errorf("%w: reasoning.effort must be a string", ErrDecodeFailed)
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return "", fmt.Errorf("%w: reasoning.effort must not be empty", ErrDecodeFailed)
	}
	if !validReasoningEffort(effort) {
		return "", fmt.Errorf("%w: reasoning.effort %q is not supported", ErrDecodeFailed, effort)
	}
	return effort, nil
}

// validReasoningEffort mirrors the pinned OpenResponses create schema rather
// than accepting arbitrary provider-specific effort names.
func validReasoningEffort(effort string) bool {
	switch effort {
	case "none", "low", "medium", "high", "xhigh":
		return true
	default:
		return false
	}
}

// decodeTextFormat validates the pinned text control and returns its canonical
// MIME type. Empty text controls and null format are equivalent to omission.
// The canonical contract has no carrier for any other text-level or format
// fields, so they are rejected before execution.
func decodeTextFormat(raw json.RawMessage) (string, error) {
	if !jsonpresence.IsPresentNonNullJSON(raw) {
		return "", nil
	}
	var text map[string]json.RawMessage
	if err := json.Unmarshal(raw, &text); err != nil || text == nil {
		if err == nil {
			err = fmt.Errorf("text must be a JSON object")
		}
		return "", fmt.Errorf("%w: %v", ErrDecodeFailed, err)
	}
	if len(text) == 0 {
		return "", nil
	}
	formatRaw, ok := text["format"]
	if !ok {
		return "", fmt.Errorf("%w: text contains unsupported fields", ErrDecodeFailed)
	}
	if jsonpresence.IsJSONNull(bytes.TrimSpace(formatRaw)) {
		if len(text) == 1 {
			return "", nil
		}
		return "", fmt.Errorf("%w: text contains unsupported fields", ErrDecodeFailed)
	}
	if len(text) != 1 {
		return "", fmt.Errorf("%w: text contains unsupported fields", ErrDecodeFailed)
	}
	var format map[string]json.RawMessage
	if err := json.Unmarshal(formatRaw, &format); err != nil || format == nil {
		if err == nil {
			err = fmt.Errorf("text.format must be a JSON object")
		}
		return "", fmt.Errorf("%w: %v", ErrDecodeFailed, err)
	}
	if len(format) != 1 {
		return "", fmt.Errorf("%w: text.format contains unsupported fields", ErrDecodeFailed)
	}
	typeRaw, ok := format["type"]
	if !ok {
		return "", fmt.Errorf("%w: text.format.type is required", ErrDecodeFailed)
	}
	var typ string
	if err := json.Unmarshal(typeRaw, &typ); err != nil {
		return "", fmt.Errorf("%w: text.format.type is required", ErrDecodeFailed)
	}
	switch typ {
	case "text":
		return "text/plain", nil
	case "json_object":
		return "application/json", nil
	default:
		return "", fmt.Errorf("%w: text.format.type %q is not supported", ErrDecodeFailed, typ)
	}
}

// decodeToolChoice decodes wire tool_choice JSON into aipapi.ToolChoice.
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func decodeToolChoice(raw []byte) (aipapi.ToolChoice, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return aipapi.ToolChoice{}, nil
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return aipapi.ToolChoice{}, fmt.Errorf("%w: invalid string tool_choice", ErrDecodeFailed)
		}
		switch s {
		case "auto":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
		case "none":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, nil
		case "required":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, nil
		default:
			return aipapi.ToolChoice{}, fmt.Errorf("%w: unknown string tool_choice %q", ErrDecodeFailed, s)
		}
	}

	if trimmed[0] == '{' {
		var rawObj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &rawObj); err != nil {
			return aipapi.ToolChoice{}, fmt.Errorf("%w: invalid object tool_choice", ErrDecodeFailed)
		}

		typeVal := ""
		if t, ok := rawObj["type"]; ok {
			if err := json.Unmarshal(t, &typeVal); err != nil {
				return aipapi.ToolChoice{}, fmt.Errorf("%w: invalid object tool_choice type", ErrDecodeFailed)
			}
		}

		if typeVal == "function" {
			// The pinned OpenResponses shape names the function directly. Accept
			// the historical nested form as a compatibility input, but never
			// emit it (the encoder uses the official direct form).
			name := ""
			if rawName, ok := rawObj["name"]; ok {
				if err := json.Unmarshal(rawName, &name); err != nil {
					return aipapi.ToolChoice{}, fmt.Errorf("%w: invalid function tool_choice name", ErrDecodeFailed)
				}
			}
			if name == "" {
				if fn, ok := rawObj["function"]; ok {
					var funcObj WireToolChoiceFunctionName
					if err := json.Unmarshal(fn, &funcObj); err != nil {
						return aipapi.ToolChoice{}, fmt.Errorf("%w: invalid function tool_choice object", ErrDecodeFailed)
					}
					name = funcObj.Name
				}
			}
			if name == "" {
				return aipapi.ToolChoice{}, fmt.Errorf("%w: invalid function tool_choice object missing name", ErrDecodeFailed)
			}
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: name}, nil
		}

		if typeVal == "allowed_tools" {
			return decodeAllowedTools(rawObj)
		}

		return aipapi.ToolChoice{}, fmt.Errorf("%w: unknown object tool_choice type %q", ErrDecodeFailed, typeVal)
	}

	return aipapi.ToolChoice{}, fmt.Errorf("%w: invalid tool_choice format", ErrDecodeFailed)
}

// decodeAllowedTools parses the pinned allowed_tools tool_choice object into the
// canonical subset. The subset is a hard constraint: only the named tools may be
// invoked, while the full Tools list stays visible. The optional mode is one of
// "auto" (default), "none", or "required".
func decodeAllowedTools(rawObj map[string]json.RawMessage) (aipapi.ToolChoice, error) {
	mode := aipapi.ToolChoiceAuto
	if raw, ok := rawObj["mode"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return aipapi.ToolChoice{}, fmt.Errorf("%w: invalid allowed_tools mode", ErrDecodeFailed)
		}
		switch s {
		case "auto":
			mode = aipapi.ToolChoiceAuto
		case "none":
			mode = aipapi.ToolChoiceNone
		case "required":
			mode = aipapi.ToolChoiceAny
		default:
			return aipapi.ToolChoice{}, fmt.Errorf("%w: unknown allowed_tools mode %q", ErrDecodeFailed, s)
		}
	}

	rawTools, ok := rawObj["tools"]
	if !ok {
		return aipapi.ToolChoice{}, fmt.Errorf("%w: allowed_tools requires a tools array", ErrDecodeFailed)
	}
	var refs []WireToolChoiceAllowedToolRef
	if err := json.Unmarshal(rawTools, &refs); err != nil {
		return aipapi.ToolChoice{}, fmt.Errorf("%w: invalid allowed_tools tools array", ErrDecodeFailed)
	}
	if len(refs) == 0 {
		return aipapi.ToolChoice{}, fmt.Errorf("%w: allowed_tools tools must not be empty", ErrDecodeFailed)
	}
	if len(refs) > aipapi.MaxAllowedToolRefs {
		return aipapi.ToolChoice{}, fmt.Errorf("%w: allowed_tools tools array exceeds %d references", ErrDecodeFailed, aipapi.MaxAllowedToolRefs)
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Type != "function" {
			return aipapi.ToolChoice{}, fmt.Errorf("%w: allowed_tools tool reference has unsupported type %q", ErrDecodeFailed, ref.Type)
		}
		if ref.Name == "" || ref.Name != strings.TrimSpace(ref.Name) {
			return aipapi.ToolChoice{}, fmt.Errorf("%w: allowed_tools tool reference requires a valid name", ErrDecodeFailed)
		}
		names = append(names, ref.Name)
	}
	return aipapi.ToolChoice{Mode: mode, AllowedTools: names}, nil
}
