package openailegacy

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// Extension key for wire model stored by the legacy OpenAI Chat frontend decoder.
const (
	extModelJSONKey      = "openailegacy.model"
	extStreamOptsJSONKey = "openailegacy.stream_options"
)

func resolveModel(cand routing.AttemptCandidate, call aipapi.Call) string {
	m := strings.TrimSpace(cand.Primary.Model)
	if m != "" {
		return m
	}
	if call.Extensions != nil {
		raw, ok := call.Extensions[extModelJSONKey]
		if ok && len(raw) > 0 {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func resolveIncludeUsage(call aipapi.Call) bool {
	_ = call
	return true
}

// ParamsForCall builds an OpenAI Chat Completions create payload from a canonical call.
func ParamsForCall(call *aipapi.Call, cand routing.AttemptCandidate) (openai.ChatCompletionNewParams, error) {
	if call == nil {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("openailegacy: nil call")
	}
	model := resolveModel(cand, *call)
	if model == "" {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("openailegacy: model is required (route candidate or %s extension)", extModelJSONKey)
	}

	msgs, err := buildChatMessages(call)
	if err != nil {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("openailegacy: build chat messages: %w", err)
	}

	p := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(model),
		Messages: msgs,
	}
	pck, err := call.PromptCacheKeyValue()
	if err != nil {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("openailegacy: %w", err)
	}
	if pck != "" {
		p.PromptCacheKey = openai.String(pck)
	}

	if includeUsage := resolveIncludeUsage(*call); includeUsage {
		p.StreamOptions = openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		}
	}

	if len(call.Tools) > 0 {
		tools, err := buildChatTools(call.Tools)
		if err != nil {
			return openai.ChatCompletionNewParams{}, fmt.Errorf("openailegacy: build chat tools: %w", err)
		}
		p.Tools = tools
		p.ToolChoice = chatToolChoiceUnion(call.ToolChoice, len(call.Tools))
	}

	o := call.Options
	if o.Temperature != nil {
		p.Temperature = openai.Float(*o.Temperature)
	}
	if o.TopP != nil {
		p.TopP = openai.Float(*o.TopP)
	}
	if o.MaxOutputTokens != nil {
		p.MaxTokens = openai.Int(int64(*o.MaxOutputTokens))
	}
	if o.ParallelToolCalls != nil {
		p.ParallelToolCalls = openai.Bool(*o.ParallelToolCalls)
	}
	if e := strings.TrimSpace(o.ReasoningEffort); e != "" {
		p.ReasoningEffort = shared.ReasoningEffort(e)
	}
	if o.Verbosity != "" {
		p.Verbosity = openai.ChatCompletionNewParamsVerbosity(o.Verbosity)
	}

	return p, nil
}

func joinInstructionText(insts []aipapi.Message) string {
	var b strings.Builder
	for _, m := range insts {
		for _, p := range m.Parts {
			if p.Kind != aipapi.PartText {
				continue
			}
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(b.String())
}

func buildChatMessages(call *aipapi.Call) ([]openai.ChatCompletionMessageParamUnion, error) {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(call.Messages)+1)

	if inst := joinInstructionText(call.Instructions); inst != "" {
		out = append(out, openai.SystemMessage(inst))
	}

	for _, m := range call.Messages {
		u, err := messageToChatParam(m)
		if err != nil {
			return nil, fmt.Errorf("openailegacy: chat message: %w", err)
		}
		out = append(out, u)
	}
	return out, nil
}

func messageToChatParam(m aipapi.Message) (openai.ChatCompletionMessageParamUnion, error) {
	switch m.Role {
	case aipapi.RoleTool:
		if len(m.Parts) != 1 || m.Parts[0].Kind != aipapi.PartToolResult {
			return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: tool message must contain one tool_result part")
		}
		p := m.Parts[0]
		return openai.ToolMessage(toolResultString(p), p.ToolCallID), nil

	case aipapi.RoleAssistant:
		return assistantChatMessage(m)

	case aipapi.RoleUser, aipapi.RoleSystem:
		return userOrSystemChatMessage(m)

	case aipapi.RoleDeveloper:
		return developerChatMessage(m)

	default:
		return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: unsupported message role %q", m.Role)
	}
}

func assistantChatMessage(m aipapi.Message) (openai.ChatCompletionMessageParamUnion, error) {
	var textParts []string
	var reasoningText string
	var toolCalls []openai.ChatCompletionMessageToolCallUnionParam
	for _, p := range m.Parts {
		switch p.Kind {
		case aipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			textParts = append(textParts, p.Text)
		case aipapi.PartReasoning:
			if p.Reasoning == nil {
				return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: reasoning part missing payload")
			}
			d := aipapi.NormalizeReasoningDialect(p.Reasoning.Dialect)
			if d != aipapi.ReasoningDialectOpenAIChatTextV1 {
				return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: reasoning dialect %q not supported for chat replay (want %s)", d, aipapi.ReasoningDialectOpenAIChatTextV1)
			}
			if t := strings.TrimSpace(p.Reasoning.Text); t != "" {
				if reasoningText != "" {
					reasoningText += t
				} else {
					reasoningText = t
				}
			}
		case aipapi.PartJSON:
			tc, err := assistantPartJSONToToolCall(p)
			if err != nil {
				return openai.ChatCompletionMessageParamUnion{}, err
			}
			toolCalls = append(toolCalls, tc)
		default:
			return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: assistant message part kind %q not supported", p.Kind)
		}
	}
	if len(textParts) == 0 && reasoningText == "" && len(toolCalls) == 0 {
		return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: assistant message is empty after trimming")
	}
	u := openai.AssistantMessage(strings.Join(textParts, ""))
	if u.OfAssistant == nil {
		return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: assistant message param missing")
	}
	if len(toolCalls) > 0 {
		u.OfAssistant.ToolCalls = toolCalls
	}
	if reasoningText != "" {
		u.OfAssistant.SetExtraFields(map[string]any{
			"reasoning_content": reasoningText,
		})
	}
	return u, nil
}

func assistantPartJSONToToolCall(p aipapi.Part) (openai.ChatCompletionMessageToolCallUnionParam, error) {
	id := strings.TrimSpace(p.ToolCallID)
	name := strings.TrimSpace(p.ToolName)
	args := "{}"
	if len(p.Content) > 0 {
		var wire struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"function"`
		}
		if err := json.Unmarshal(p.Content, &wire); err == nil && (wire.ID != "" || wire.Function.Name != "" || len(wire.Function.Arguments) > 0) {
			if id == "" {
				id = strings.TrimSpace(wire.ID)
			}
			if name == "" {
				name = strings.TrimSpace(wire.Function.Name)
			}
			if len(wire.Function.Arguments) > 0 {
				switch wire.Function.Arguments[0] {
				case '"':
					var s string
					if err := json.Unmarshal(wire.Function.Arguments, &s); err != nil {
						return openai.ChatCompletionMessageToolCallUnionParam{}, fmt.Errorf("openailegacy: tool_calls.arguments: %w", err)
					}
					args = s
				default:
					if !json.Valid(wire.Function.Arguments) {
						return openai.ChatCompletionMessageToolCallUnionParam{}, fmt.Errorf("openailegacy: tool_calls.arguments invalid json")
					}
					args = string(wire.Function.Arguments)
				}
			}
		} else if json.Valid(p.Content) {
			args = string(p.Content)
		} else {
			return openai.ChatCompletionMessageToolCallUnionParam{}, fmt.Errorf("openailegacy: assistant tool part content invalid")
		}
	}
	if id == "" || name == "" {
		return openai.ChatCompletionMessageToolCallUnionParam{}, fmt.Errorf("openailegacy: assistant tool_call requires id and name")
	}
	return openai.ChatCompletionMessageToolCallUnionParam{
		OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
			ID: id,
			Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
				Name:      name,
				Arguments: args,
			},
		},
	}, nil
}

// developerChatMessage encodes a canonical developer-role message.
//
// Chat Completions carries OpenAI's own "developer" wire role, so the canonical
// role maps literally instead of being rejected or re-labelled as a user turn.
// The generic continuation steering transaction writes its instruction as a
// developer-role message, so this is what makes a bounded protocol-repair leg
// encodable on this adapter. The wire role accepts text content only, so a
// non-text developer message fails explicitly instead of losing parts.
func developerChatMessage(m aipapi.Message) (openai.ChatCompletionMessageParamUnion, error) {
	if len(m.Parts) == 1 && m.Parts[0].Kind == aipapi.PartText {
		return openai.DeveloperMessage(m.Parts[0].Text), nil
	}
	return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: developer message must be plain text for this adapter")
}

func userOrSystemChatMessage(m aipapi.Message) (openai.ChatCompletionMessageParamUnion, error) {
	if m.Role == aipapi.RoleSystem {
		if len(m.Parts) == 1 && m.Parts[0].Kind == aipapi.PartText {
			return openai.SystemMessage(m.Parts[0].Text), nil
		}
		return openai.ChatCompletionMessageParamUnion{}, fmt.Errorf("openailegacy: system message must be plain text for this adapter")
	}

	if len(m.Parts) == 1 && m.Parts[0].Kind == aipapi.PartText {
		return openai.UserMessage(m.Parts[0].Text), nil
	}
	parts, err := userPartsToContentUnion(m.Parts)
	if err != nil {
		return openai.ChatCompletionMessageParamUnion{}, err
	}
	return openai.UserMessage(parts), nil
}

func userPartsToContentUnion(parts []aipapi.Part) ([]openai.ChatCompletionContentPartUnionParam, error) {
	out := make([]openai.ChatCompletionContentPartUnionParam, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case aipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, openai.TextContentPart(p.Text))
		case aipapi.PartImageRef:
			out = append(out, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
				URL: p.ImageRef,
			}))
		case aipapi.PartFileRef:
			b64, fname, err := fileDataFromPart(p)
			if err != nil {
				return nil, err
			}
			out = append(out, openai.FileContentPart(openai.ChatCompletionContentPartFileFileParam{
				FileData: openai.String(b64),
				Filename: openai.String(fname),
			}))
		default:
			return nil, fmt.Errorf("openailegacy: unsupported part kind %q in user message", p.Kind)
		}
	}
	return out, nil
}

func toolResultString(p aipapi.Part) string {
	if len(p.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(p.Content, &s); err == nil {
		return s
	}
	return string(p.Content)
}

func fileDataFromPart(p aipapi.Part) (dataB64, filename string, err error) {
	filename = strings.TrimSpace(p.FileName)
	ref := p.FileRef
	if strings.HasPrefix(ref, "data:") {
		_, b64, ok := stripDataURLBase64(ref)
		if !ok {
			return "", "", fmt.Errorf("openailegacy: invalid data URL in file part")
		}
		return b64, filename, nil
	}
	return ref, filename, nil
}

func stripDataURLBase64(dataURL string) (mime, b64 string, ok bool) {
	rest, ok := strings.CutPrefix(dataURL, "data:")
	if !ok {
		return "", "", false
	}
	mime, enc, found := strings.Cut(rest, ";")
	if !found {
		return "", "", false
	}
	const prefix = "base64,"
	encBody, ok := strings.CutPrefix(enc, prefix)
	if !ok {
		return "", "", false
	}
	return mime, encBody, true
}

func buildChatTools(tools []aipapi.ToolDef) ([]openai.ChatCompletionToolUnionParam, error) {
	out := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		var schema map[string]any
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return nil, fmt.Errorf("openailegacy: tool %q parameters: %w", t.Name, err)
			}
		}
		if schema == nil {
			schema = map[string]any{}
		}
		fn := shared.FunctionDefinitionParam{
			Name:       t.Name,
			Parameters: schema,
			Strict:     param.NewOpt(true),
		}
		if strings.TrimSpace(t.Description) != "" {
			fn.Description = param.NewOpt(t.Description)
		}
		out = append(out, openai.ChatCompletionFunctionTool(fn))
	}
	return out, nil
}

func chatToolChoiceUnion(tc aipapi.ToolChoice, nTools int) openai.ChatCompletionToolChoiceOptionUnionParam {
	mode := tc.Mode
	if mode == "" {
		mode = aipapi.ToolChoiceAuto
	}
	switch mode {
	case aipapi.ToolChoiceAuto:
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("auto")}
	case aipapi.ToolChoiceNone:
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("none")}
	case aipapi.ToolChoiceAny:
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("required")}
	case aipapi.ToolChoiceRequired:
		if tc.Name != "" && nTools > 0 {
			return openai.ToolChoiceOptionFunctionToolChoice(openai.ChatCompletionNamedToolChoiceFunctionParam{
				Name: tc.Name,
			})
		}
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("required")}
	default:
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("auto")}
	}
}
