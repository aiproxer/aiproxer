package openailegacy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/jsonpresence"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/identitywire"
	frontendlimits "github.com/aiproxer/aiproxer/internal/plugins/frontends/limits"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openaiwire"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/plugins/openrouterwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Extension keys for round-trip metadata on the canonical call.
const (
	extModelJSONKey      = "openailegacy.model"
	extStreamOptsJSONKey = "openailegacy.stream_options"
)

// DecodeOptions configures decoding of a Chat Completions request.
type DecodeOptions struct {
	// RouteSelector is required (e.g. "stub:gpt-4o-mini"); usually from X-AIP-Route header.
	RouteSelector string
	Headers       http.Header
	// HTTPHeaders carries configured header aliases; zero value uses defaults.
	HTTPHeaders aipsdk.HTTPHeaders
}

// DecodedChat is the result of decoding POST /v1/chat/completions JSON.
type DecodedChat struct {
	Call   *aipapi.Call
	Stream bool
	Model  string
}

type wireCreate struct {
	Model             string            `json:"model"`
	Stream            bool              `json:"stream"`
	Messages          []json.RawMessage `json:"messages"`
	Tools             json.RawMessage   `json:"tools"`
	ToolChoice        json.RawMessage   `json:"tool_choice"`
	Temperature       *float64          `json:"temperature"`
	TopP              *float64          `json:"top_p"`
	MaxTokens         *int              `json:"max_tokens"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls"`
	ReasoningEffort   string            `json:"reasoning_effort"`
	Verbosity         string            `json:"verbosity"`
	StreamOptions     json.RawMessage   `json:"stream_options"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	PromptCacheKey    string            `json:"prompt_cache_key"`
}

var legacyKnownBodyKeys = map[string]bool{
	"model": true, "stream": true, "messages": true, "tools": true,
	"tool_choice": true, "temperature": true, "top_p": true, "max_tokens": true,
	"parallel_tool_calls": true, "stream_options": true, "metadata": true,
	"max_completion_tokens": true, "n": true, "stop": true, "presence_penalty": true,
	"frequency_penalty": true, "logit_bias": true, "logprobs": true, "top_logprobs": true,
	"seed": true, "suffix": true, "reasoning_effort": true, "verbosity": true,
	"prompt_cache_key": true,
}

var (
	errEmptyAssistantMessage = errors.New("empty assistant message")
	errEmptyChatContent      = errors.New("message content string is empty")
	// errSkipMalformedHistory marks replayed garbage that clients sometimes
	// persist (empty-name tool_calls / empty tool_call_id results). Drop these
	// instead of failing the whole request so agent harness sessions continue.
	errSkipMalformedHistory = errors.New("skip malformed history fragment")
)

// DecodeChatRequest maps a Chat Completions JSON body into a canonical call.
func DecodeChatRequest(body []byte, opts DecodeOptions) (*DecodedChat, error) {
	sel := strings.TrimSpace(opts.RouteSelector)
	if sel == "" {
		return nil, errors.New("openailegacy: route selector is required: send a model with a route prefix (for example \"backend:model\")")
	}
	var w wireCreate
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("openailegacy: invalid json: %w", err)
	}
	model := strings.TrimSpace(w.Model)
	if model == "" {
		return nil, errors.New("openailegacy: model is required")
	}
	if len(w.Messages) == 0 {
		return nil, errors.New("openailegacy: messages is required")
	}
	if err := frontendlimits.Count("messages", len(w.Messages), frontendlimits.MaxMessages); err != nil {
		return nil, fmt.Errorf("openailegacy: %w", err)
	}
	if err := frontendlimits.Count("metadata", len(w.Metadata), frontendlimits.MaxMetadata); err != nil {
		return nil, fmt.Errorf("openailegacy: %w", err)
	}
	if err := sessionwire.ValidateMetadata(w.Metadata); err != nil {
		return nil, fmt.Errorf("openailegacy: %w", err)
	}

	msgs, err := parseMessages(w.Messages)
	if err != nil {
		return nil, fmt.Errorf("openailegacy: messages: %w", err)
	}
	tools, err := parseTools(w.Tools)
	if err != nil {
		return nil, fmt.Errorf("openailegacy: tools: %w", err)
	}
	toolChoice, err := parseToolChoice(w.ToolChoice)
	if err != nil {
		return nil, fmt.Errorf("openailegacy: tool_choice: %w", err)
	}
	verbosity, err := aipapi.ParseVerbosityLevel(w.Verbosity)
	if err != nil {
		return nil, fmt.Errorf("openailegacy: %w", err)
	}

	modelRaw, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("openailegacy: marshal model extension: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}
	if b, err := json.Marshal(openrouterwire.FlavorChat); err == nil {
		ext[openrouterwire.ExtUpstreamFlavor] = b
	}
	if jsonpresence.IsPresentNonNullJSON(w.StreamOptions) {
		ext[extStreamOptsJSONKey] = w.StreamOptions
	}

	var rawBody map[string]json.RawMessage
	if json.Unmarshal(body, &rawBody) == nil {
		openrouterwire.CaptureBodyFields(rawBody, ext)
		openrouterwire.CaptureExtraBodyFields(rawBody, ext, legacyKnownBodyKeys)
	}
	if opts.Headers != nil {
		openrouterwire.CaptureHeaders(opts.Headers, ext)
	}

	call := &aipapi.Call{
		Route:          aipapi.RouteIntent{Selector: sel},
		Messages:       msgs,
		Tools:          tools,
		ToolChoice:     toolChoice,
		Extensions:     ext,
		PromptCacheKey: strings.TrimSpace(w.PromptCacheKey),
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeFromClientStream(w.Stream),
		},
		Options: aipapi.GenerationOptions{
			Temperature:       w.Temperature,
			TopP:              w.TopP,
			MaxOutputTokens:   w.MaxTokens,
			ParallelToolCalls: w.ParallelToolCalls,
			ReasoningEffort:   strings.TrimSpace(w.ReasoningEffort),
			Verbosity:         verbosity,
		},
	}
	if len(w.Metadata) > 0 {
		sessionwire.ApplyMetadata(&call.Session, w.Metadata)
	}
	if opts.Headers != nil {
		sessionwire.ApplyAuthoritativeHeaders(&call.Session, opts.Headers)
		identitywire.CaptureClientUserAgent(&call.Invocation, opts.Headers)
	}
	if err := sessionwire.ApplyWorkspaceHint(call, opts.Headers, opts.HTTPHeaders); err != nil {
		return nil, fmt.Errorf("openailegacy: %w", err)
	}
	return &DecodedChat{Call: call, Stream: w.Stream, Model: model}, nil
}

func parseMessages(raw []json.RawMessage) ([]aipapi.Message, error) {
	out := make([]aipapi.Message, 0, len(raw))
	for i, it := range raw {
		m, err := parseMessage(it)
		if err != nil {
			if errors.Is(err, errEmptyAssistantMessage) || errors.Is(err, errSkipMalformedHistory) {
				continue
			}
			return nil, fmt.Errorf("openailegacy: messages[%d]: %w", i, err)
		}
		out = append(out, m)
	}
	return out, nil
}

func parseMessage(raw json.RawMessage) (aipapi.Message, error) {
	var probe struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		ToolCallID       string          `json:"tool_call_id"`
		ToolCalls        json.RawMessage `json:"tool_calls"`
		FunctionCall     json.RawMessage `json:"function_call"`
		ReasoningContent *string         `json:"reasoning_content"`
		Reasoning        *string         `json:"reasoning"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return aipapi.Message{}, fmt.Errorf("openailegacy: message json: %w", err)
	}
	role, err := mapRole(probe.Role)
	if err != nil {
		return aipapi.Message{}, fmt.Errorf("openailegacy: role: %w", err)
	}

	switch role {
	case aipapi.RoleTool:
		if strings.TrimSpace(probe.ToolCallID) == "" {
			// Orphan tool result for a dropped unnamed tool_call (e.g. pi
			// "Tool  not found" stub). Skip rather than fail the request.
			return aipapi.Message{}, errSkipMalformedHistory
		}
		content, err := parseToolMessageContent(probe.Content)
		if err != nil {
			return aipapi.Message{}, fmt.Errorf("openailegacy: tool message content: %w", err)
		}
		rawJSON, err := json.Marshal(content)
		if err != nil {
			return aipapi.Message{}, fmt.Errorf("openailegacy: tool message marshal: %w", err)
		}
		return aipapi.Message{
			Role: aipapi.RoleTool,
			Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: strings.TrimSpace(probe.ToolCallID),
				Content:    rawJSON,
			}},
		}, nil
	case aipapi.RoleAssistant:
		parts, err := parseAssistantParts(probe.Content, probe.ToolCalls, probe.FunctionCall, probe.ReasoningContent, probe.Reasoning)
		if err != nil {
			return aipapi.Message{}, fmt.Errorf("openailegacy: assistant message: %w", err)
		}
		return aipapi.Message{Role: aipapi.RoleAssistant, Parts: parts}, nil
	}

	parts, err := parseChatContent(probe.Content)
	if err != nil {
		return aipapi.Message{}, fmt.Errorf("openailegacy: message content: %w", err)
	}
	return aipapi.Message{Role: role, Parts: parts}, nil
}

func parseAssistantParts(content, toolCalls, functionCall json.RawMessage, reasoningContent, reasoningAlias *string) ([]aipapi.Part, error) {
	contentParts := []aipapi.Part{}
	toolCallParts := []json.RawMessage{}
	functionCallPart := json.RawMessage(nil)

	if jsonpresence.IsPresentNonNullJSON(content) {
		cp, err := parseChatContent(content)
		switch {
		case err == nil:
			contentParts = cp
		case errors.Is(err, errEmptyChatContent):
			// treat empty content as absent for tool-call-only turns.
		default:
			return nil, fmt.Errorf("openailegacy: assistant content: %w", err)
		}
	}
	if jsonpresence.IsPresentNonNullJSON(toolCalls) {
		if err := frontendlimits.Bytes("tool_calls", len(toolCalls), frontendlimits.MaxRawJSONPayload); err != nil {
			return nil, err
		}
		var rawCalls []json.RawMessage
		if err := json.Unmarshal(toolCalls, &rawCalls); err != nil {
			return nil, fmt.Errorf("openailegacy: tool_calls: %w", err)
		}
		toolCallParts = rawCalls
	}
	if jsonpresence.IsPresentNonNullJSON(functionCall) {
		if err := frontendlimits.Bytes("function_call", len(functionCall), frontendlimits.MaxRawJSONPayload); err != nil {
			return nil, err
		}
		if !json.Valid(functionCall) {
			return nil, errors.New("openailegacy: invalid function_call")
		}
		functionCallPart = append(json.RawMessage(nil), functionCall...)
	}

	reasoningText, _ := chatAssistantReasoningText(reasoningContent, reasoningAlias)

	capHint := len(contentParts) + len(toolCallParts)
	if len(functionCallPart) > 0 {
		capHint++
	}
	if reasoningText != "" {
		capHint++
	}
	parts := make([]aipapi.Part, 0, capHint)
	if reasoningText != "" {
		if err := frontendlimits.StringBytes("reasoning", reasoningText, aipapi.MaxReasoningTextBytes); err != nil {
			return nil, err
		}
		parts = append(parts, aipapi.Part{
			Kind: aipapi.PartReasoning,
			Reasoning: &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
				Text:    reasoningText,
			},
		})
	}
	parts = append(parts, contentParts...)
	for _, rc := range toolCallParts {
		if !json.Valid(rc) {
			return nil, errors.New("openailegacy: invalid tool_calls entry")
		}
		part, err := assistantToolCallPart(rc)
		if err != nil {
			if errors.Is(err, errSkipMalformedHistory) {
				continue
			}
			return nil, err
		}
		parts = append(parts, part)
	}
	if len(functionCallPart) > 0 {
		parts = append(parts, aipapi.Part{Kind: aipapi.PartJSON, Content: functionCallPart})
	}
	if len(parts) == 0 {
		return nil, errEmptyAssistantMessage
	}
	return parts, nil
}

func chatAssistantReasoningText(reasoningContent, reasoningAlias *string) (text string, officialPresent bool) {
	if reasoningContent != nil {
		return *reasoningContent, true
	}
	if reasoningAlias != nil {
		return *reasoningAlias, false
	}
	return "", false
}

// assistantToolCallPart normalizes a wire tool_calls entry into PartJSON matching
// stream-observer shape: ToolCallID/ToolName set and Content holding arguments JSON.
func assistantToolCallPart(rc json.RawMessage) (aipapi.Part, error) {
	var wire struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(rc, &wire); err != nil {
		return aipapi.Part{}, fmt.Errorf("openailegacy: tool_calls entry: %w", err)
	}
	id := strings.TrimSpace(wire.ID)
	name := strings.TrimSpace(wire.Function.Name)
	if id == "" || name == "" {
		return aipapi.Part{}, errSkipMalformedHistory
	}
	args := json.RawMessage(`{}`)
	if jsonpresence.IsPresentNonNullJSON(wire.Function.Arguments) {
		switch wire.Function.Arguments[0] {
		case '"':
			var s string
			if err := json.Unmarshal(wire.Function.Arguments, &s); err != nil {
				return aipapi.Part{}, fmt.Errorf("openailegacy: tool_calls.arguments: %w", err)
			}
			if strings.TrimSpace(s) == "" {
				args = json.RawMessage(`{}`)
			} else if json.Valid([]byte(s)) {
				args = json.RawMessage(s)
			} else {
				b, err := json.Marshal(s)
				if err != nil {
					return aipapi.Part{}, err
				}
				args = b
			}
		default:
			if !json.Valid(wire.Function.Arguments) {
				return aipapi.Part{}, errors.New("openailegacy: tool_calls.arguments invalid json")
			}
			args = append(json.RawMessage(nil), wire.Function.Arguments...)
		}
	}
	return aipapi.Part{
		Kind:       aipapi.PartJSON,
		ToolCallID: id,
		ToolName:   name,
		Content:    args,
	}, nil
}

func parseToolMessageContent(raw json.RawMessage) (any, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, errors.New("tool message content is required")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("openailegacy: tool message content string: %w", err)
		}
		if strings.TrimSpace(s) == "" {
			return nil, errors.New("tool message content is required")
		}
		return s, nil
	}
	var v any
	if err := frontendlimits.Bytes("tool message content", len(raw), frontendlimits.MaxRawJSONPayload); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("openailegacy: tool message content json: %w", err)
	}
	return v, nil
}

func mapRole(r string) (aipapi.Role, error) {
	switch strings.TrimSpace(strings.ToLower(r)) {
	case "user":
		return aipapi.RoleUser, nil
	case "assistant":
		return aipapi.RoleAssistant, nil
	case "system":
		return aipapi.RoleSystem, nil
	case "developer":
		return aipapi.RoleSystem, nil
	case "tool":
		return aipapi.RoleTool, nil
	case "":
		return "", errors.New("message role is required")
	default:
		return "", fmt.Errorf("openailegacy: unsupported role %q", r)
	}
}

func parseChatContent(raw json.RawMessage) ([]aipapi.Part, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, errors.New("message content is required")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("openailegacy: message content string: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, errEmptyChatContent
		}
		return []aipapi.Part{aipapi.TextPart(s)}, nil
	}
	if raw[0] != '[' {
		return nil, errors.New("message content must be a string or array")
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("openailegacy: message content array: %w", err)
	}
	if err := frontendlimits.Count("content", len(blocks), frontendlimits.MaxParts); err != nil {
		return nil, err
	}
	parts := make([]aipapi.Part, 0, len(blocks))
	for i, blk := range blocks {
		p, err := parseChatContentBlock(blk)
		if err != nil {
			return nil, fmt.Errorf("content[%d]: %w", i, err)
		}
		parts = append(parts, p)
	}
	return parts, nil
}

func parseChatContentBlock(blk map[string]json.RawMessage) (aipapi.Part, error) {
	tRaw, ok := blk["type"]
	if !ok {
		return aipapi.Part{}, errors.New("content block missing type")
	}
	var typ string
	if err := json.Unmarshal(tRaw, &typ); err != nil {
		return aipapi.Part{}, fmt.Errorf("openailegacy: content block type: %w", err)
	}
	switch strings.TrimSpace(typ) {
	case "text":
		var s struct {
			Text string `json:"text"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return aipapi.Part{}, fmt.Errorf("openailegacy: text block wire: %w", err)
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return aipapi.Part{}, fmt.Errorf("openailegacy: text block json: %w", err)
		}
		if strings.TrimSpace(s.Text) == "" {
			return aipapi.Part{}, errors.New("text part requires text")
		}
		return aipapi.TextPart(s.Text), nil
	case "image_url":
		var s struct {
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return aipapi.Part{}, fmt.Errorf("openailegacy: image_url block wire: %w", err)
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return aipapi.Part{}, fmt.Errorf("openailegacy: image_url block json: %w", err)
		}
		u := strings.TrimSpace(s.ImageURL.URL)
		if u == "" {
			return aipapi.Part{}, errors.New("image_url requires url")
		}
		return openaiwire.ImagePartFromURL(u)
	case "file":
		var s struct {
			File struct {
				FileData string `json:"file_data"`
				Filename string `json:"filename"`
			} `json:"file"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return aipapi.Part{}, fmt.Errorf("openailegacy: file block wire: %w", err)
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return aipapi.Part{}, fmt.Errorf("openailegacy: file block json: %w", err)
		}
		fd := strings.TrimSpace(s.File.FileData)
		if fd == "" {
			return aipapi.Part{}, errors.New("file part requires file_data")
		}
		if err := frontendlimits.StringBytes("file.file_data", fd, frontendlimits.MaxBase64Data); err != nil {
			return aipapi.Part{}, err
		}
		return openaiwire.FilePartFromBase64(s.File.Filename, fd), nil
	default:
		return aipapi.Part{}, fmt.Errorf("openailegacy: unsupported content block type %q", typ)
	}
}

func parseTools(raw json.RawMessage) ([]aipapi.ToolDef, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return []aipapi.ToolDef{}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("openailegacy: tools: %w", err)
	}
	if err := frontendlimits.Count("tools", len(items), frontendlimits.MaxTools); err != nil {
		return nil, err
	}
	out := make([]aipapi.ToolDef, 0, len(items))
	for i, it := range items {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(it, &probe); err != nil {
			return nil, fmt.Errorf("openailegacy: tools[%d]: %w", i, err)
		}
		if probe.Type != "" && probe.Type != "function" {
			return nil, fmt.Errorf("openailegacy: tools[%d]: unsupported type %q", i, probe.Type)
		}
		var w struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(it, &w); err != nil {
			return nil, fmt.Errorf("openailegacy: tools[%d]: %w", i, err)
		}
		if strings.TrimSpace(w.Function.Name) == "" {
			return nil, fmt.Errorf("openailegacy: tools[%d]: function name is required", i)
		}
		params := w.Function.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		if err := frontendlimits.Bytes("function parameters", len(params), frontendlimits.MaxToolSchema); err != nil {
			return nil, fmt.Errorf("openailegacy: tools[%d]: %w", i, err)
		}
		out = append(out, aipapi.ToolDef{
			Name:        w.Function.Name,
			Description: w.Function.Description,
			Parameters:  params,
		})
	}
	return out, nil
}

func parseToolChoice(raw json.RawMessage) (aipapi.ToolChoice, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return aipapi.ToolChoice{}, fmt.Errorf("openailegacy: tool_choice string json: %w", err)
		}
		switch strings.TrimSpace(strings.ToLower(s)) {
		case "auto", "":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
		case "none":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, nil
		case "required":
			// OpenAI: model must call one or more tools. Canonical ToolChoiceRequired
			// always carries a specific tool name; use ToolChoiceAny for "any declared tool".
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, nil
		default:
			return aipapi.ToolChoice{}, fmt.Errorf("openailegacy: unsupported tool_choice string %q", s)
		}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return aipapi.ToolChoice{}, fmt.Errorf("openailegacy: tool_choice: %w", err)
	}
	switch strings.TrimSpace(obj.Type) {
	case "function":
		name := strings.TrimSpace(obj.Function.Name)
		if name == "" {
			return aipapi.ToolChoice{}, errors.New("openailegacy: tool_choice function name is required")
		}
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: name}, nil
	default:
		return aipapi.ToolChoice{}, fmt.Errorf("openailegacy: unsupported tool_choice type %q", obj.Type)
	}
}

// ModelFromCall returns the wire model string stored during decode.
func ModelFromCall(c *aipapi.Call) string {
	return openaiwire.ModelFromExtensions(c, extModelJSONKey)
}
