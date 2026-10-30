package anthropicmessages

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Extension key for wire model stored by the Anthropic frontend decoder.
const extModelJSONKey = "anthropic.model"

const defaultMaxTokens int64 = 4096

func newSDKClientForSecret(cfg Config, apiSecret string) anthropic.Client {
	opts := make([]option.RequestOption, 0, 4)
	if cfg.CompatibleModeAuth {
		opts = append(opts, option.WithoutEnvironmentDefaults())
	}
	opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	if secret := strings.TrimSpace(apiSecret); secret != "" {
		opts = append(opts, option.WithAPIKey(secret))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	if cfg.SDKMaxRetries != nil {
		opts = append(opts, option.WithMaxRetries(*cfg.SDKMaxRetries))
	}
	return anthropic.NewClient(opts...)
}

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

// ParamsForCall builds an Anthropic message create payload from a canonical call
// using the default Anthropic behavior (no role normalization, no model
// normalization). It is retained for callers and tests that exercise the shared
// protocol directly; the runtime path uses paramsForCall.
func ParamsForCall(call *aipapi.Call, cand routing.AttemptCandidate) (anthropic.MessageNewParams, error) {
	return paramsForCall(call, cand, false, nil)
}

// paramsForCall builds the wire payload, applying optional connector-specific
// behavior: normalizeModel rewrites the resolved model (e.g. stripping a
// provider namespace) and normalizeRoles coerces non-system/non-tool messages to
// user for strict endpoints such as Alibaba Token Plan.
func paramsForCall(call *aipapi.Call, cand routing.AttemptCandidate, normalizeRoles bool, normalizeModel func(string) string) (anthropic.MessageNewParams, error) {
	if call == nil {
		return anthropic.MessageNewParams{}, fmt.Errorf("anthropic: nil call")
	}
	model := resolveModel(cand, *call)
	if normalizeModel != nil {
		model = normalizeModel(model)
	}
	if model == "" {
		return anthropic.MessageNewParams{}, fmt.Errorf("anthropic: model is required (route candidate or %s extension)", extModelJSONKey)
	}

	maxTok := defaultMaxTokens
	if call.Options.MaxOutputTokens != nil {
		maxTok = int64(*call.Options.MaxOutputTokens)
	}

	msgs, err := buildAnthropicMessagesWithRolePolicy(call, normalizeRoles)
	if err != nil {
		return anthropic.MessageNewParams{}, fmt.Errorf("anthropic: build messages: %w", err)
	}

	p := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTok,
		Messages:  msgs,
	}

	if sys := buildSystemBlocks(call, normalizeRoles); len(sys) > 0 {
		p.System = sys
	}

	if len(call.Tools) > 0 {
		tools, err := buildTools(call.Tools)
		if err != nil {
			return anthropic.MessageNewParams{}, fmt.Errorf("anthropic: build tools: %w", err)
		}
		p.Tools = tools
		p.ToolChoice = toolChoiceUnion(call, len(call.Tools))
	}

	o := call.Options
	if o.Temperature != nil {
		p.Temperature = param.NewOpt(*o.Temperature)
	}
	if o.TopP != nil {
		p.TopP = param.NewOpt(*o.TopP)
	}

	return p, nil
}

// thinkingRequestOptions maps a reasoning-effort hint onto Alibaba Token Plan's
// Anthropic-compatible thinking switch. "none" disables thinking; any other
// non-empty value enables it. When enabled is false (the connector did not opt
// in) or effort is empty, no option is returned and the provider default is left
// unchanged. The effort name only selects on/off, not graded intensity.
func thinkingRequestOptions(effort string, enabled bool) []option.RequestOption {
	if !enabled || strings.TrimSpace(effort) == "" {
		return nil
	}
	thinkingType := "disabled"
	if reasoningEffortEnablesThinking(effort) {
		thinkingType = "enabled"
	}
	return []option.RequestOption{option.WithJSONSet("thinking", map[string]string{"type": thinkingType})}
}

func reasoningEffortEnablesThinking(effort string) bool {
	effort = strings.TrimSpace(effort)
	return effort != "" && !strings.EqualFold(effort, "none")
}

func buildSystemBlocks(call *aipapi.Call, normalizeRoles bool) []anthropic.TextBlockParam {
	// Only top-level Instructions map to System for normal provider semantics.
	// When NormalizeRoles is true (compatible-mode such as Alibaba Token Plan Intl),
	// legacy lifting of RoleSystem Messages into System blocks is preserved explicitly
	// without reintroducing silent relocation for normal semantics.
	if normalizeRoles {
		capBlocks := 0
		if len(call.Instructions) > 0 {
			capBlocks++
		}
		for _, m := range call.Messages {
			if m.Role == aipapi.RoleSystem {
				capBlocks += len(m.Parts)
			}
		}
		var out []anthropic.TextBlockParam
		if capBlocks > 0 {
			out = make([]anthropic.TextBlockParam, 0, capBlocks)
		}
		if t := aipapi.JoinInstructionText(call.Instructions); t != "" {
			out = append(out, anthropic.TextBlockParam{Text: t})
		}
		for _, m := range call.Messages {
			if m.Role != aipapi.RoleSystem {
				continue
			}
			for _, p := range m.Parts {
				if p.Kind != aipapi.PartText || strings.TrimSpace(p.Text) == "" {
					continue
				}
				out = append(out, anthropic.TextBlockParam{Text: p.Text})
			}
		}
		return out
	}
	if len(call.Instructions) == 0 {
		return nil
	}
	t := aipapi.JoinInstructionText(call.Instructions)
	if t == "" {
		return nil
	}
	return []anthropic.TextBlockParam{{Text: t}}
}

// buildAnthropicMessages builds message params with default role handling.
func buildAnthropicMessages(call *aipapi.Call) ([]anthropic.MessageParam, error) {
	return buildAnthropicMessagesWithRolePolicy(call, false)
}

// buildAnthropicMessagesWithRolePolicy builds message params, optionally
// coercing messages whose role is neither system nor a structured tool exchange
// (assistant tool_use / tool tool_result) to user, for providers that reject
// other roles.
// Top-level Instructions are already mapped to System blocks. For normal
// provider semantics any RoleSystem in call.Messages is mid-conversation and
// explicitly unsupported (must reject before Open, not silently lift). For
// compatible-mode (NormalizeRoles=true, e.g. Alibaba Token Plan Intl) legacy
// lifting of RoleSystem Messages into System blocks is preserved explicitly.
func buildAnthropicMessagesWithRolePolicy(call *aipapi.Call, normalizeRoles bool) ([]anthropic.MessageParam, error) {
	out := make([]anthropic.MessageParam, 0, len(call.Messages))
	for _, m := range call.Messages {
		if m.Role == aipapi.RoleSystem {
			if normalizeRoles {
				continue
			}
		}
		if normalizeRoles && m.Role != aipapi.RoleUser {
			structuredToolMessage := (m.Role == aipapi.RoleAssistant && messageHasPartKind(m, aipapi.PartJSON)) ||
				(m.Role == aipapi.RoleTool && messageHasPartKind(m, aipapi.PartToolResult))
			if !structuredToolMessage {
				m.Role = aipapi.RoleUser
			}
		}
		u, err := messageToParam(m)
		if err != nil {
			return nil, fmt.Errorf("anthropic: message param: %w", err)
		}
		out = append(out, u)
	}
	return mergeConsecutiveAnthropicMessages(out), nil
}

func mergeConsecutiveAnthropicMessages(msgs []anthropic.MessageParam) []anthropic.MessageParam {
	if len(msgs) == 0 {
		return msgs
	}
	merged := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		if len(merged) == 0 {
			merged = append(merged, m)
			continue
		}
		prev := &merged[len(merged)-1]
		if prev.Role == m.Role {
			combined := make([]anthropic.ContentBlockParamUnion, 0, len(prev.Content)+len(m.Content))
			combined = append(combined, prev.Content...)
			combined = append(combined, m.Content...)
			prev.Content = combined
		} else {
			merged = append(merged, m)
		}
	}
	return merged
}

// messageHasPartKind reports whether any part of m has the given kind.
func messageHasPartKind(m aipapi.Message, kind aipapi.PartKind) bool {
	for _, part := range m.Parts {
		if part.Kind == kind {
			return true
		}
	}
	return false
}

func messageToParam(m aipapi.Message) (anthropic.MessageParam, error) {
	switch m.Role {
	case aipapi.RoleUser, aipapi.RoleDeveloper:
		// aipapi.RoleDeveloper carries proxy-owned continuation steering text
		// (internal/core/runtime/terminal_decision_continuation.go places it at
		// AfterIngressTail) and Anthropic's Messages role vocabulary has no
		// developer value, so it is deliberately coerced onto the user wire role:
		// the provider reads proxy-owned instruction text as a client utterance,
		// and the canonical role survives only in the proxy's own record, never
		// on the provider wire. This is the lossy-but-modeled downgrade; it is not
		// lifted into System, because a mid-conversation system block is
		// model-gated, placement-constrained, and would destroy the trajectory
		// ordering the generic runtime depends on. The NormalizeRoles
		// compatible-provider shim is intentionally not reused here: it also
		// rewrites assistant turns, which Anthropic's own wire accepts.
		return userMessageParam(m)
	case aipapi.RoleAssistant:
		return assistantMessageParam(m)
	case aipapi.RoleTool:
		if len(m.Parts) != 1 || m.Parts[0].Kind != aipapi.PartToolResult {
			return anthropic.MessageParam{}, fmt.Errorf("anthropic: tool message must have one tool_result part")
		}
		b, err := toolResultBlockFromPart(m.Parts[0])
		if err != nil {
			return anthropic.MessageParam{}, err
		}
		return anthropic.NewUserMessage(b), nil
	default:
		return anthropic.MessageParam{}, fmt.Errorf("anthropic: unsupported message role %q", m.Role)
	}
}

func userMessageParam(m aipapi.Message) (anthropic.MessageParam, error) {
	if len(m.Parts) == 1 && m.Parts[0].Kind == aipapi.PartText && strings.TrimSpace(m.Parts[0].Text) != "" {
		return anthropic.NewUserMessage(anthropic.NewTextBlock(m.Parts[0].Text)), nil
	}
	blocks, err := userPartsToBlocks(m.Parts)
	if err != nil {
		return anthropic.MessageParam{}, fmt.Errorf("anthropic: user parts: %w", err)
	}
	return anthropic.NewUserMessage(blocks...), nil
}

func assistantMessageParam(m aipapi.Message) (anthropic.MessageParam, error) {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Parts))
	for _, p := range m.Parts {
		switch p.Kind {
		case aipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			blocks = append(blocks, anthropic.NewTextBlock(p.Text))
		case aipapi.PartReasoning:
			blk, err := reasoningPartToAnthropicBlock(p)
			if err != nil {
				return anthropic.MessageParam{}, err
			}
			blocks = append(blocks, blk)
		case aipapi.PartJSON:
			if strings.TrimSpace(p.ToolCallID) == "" || strings.TrimSpace(p.ToolName) == "" {
				return anthropic.MessageParam{}, fmt.Errorf("anthropic: assistant tool_use part requires tool_call_id and tool_name")
			}
			var input any
			if len(p.Content) > 0 {
				if err := json.Unmarshal(p.Content, &input); err != nil {
					return anthropic.MessageParam{}, fmt.Errorf("anthropic: tool_use input: %w", err)
				}
			} else {
				input = map[string]any{}
			}
			blocks = append(blocks, anthropic.NewToolUseBlock(p.ToolCallID, input, p.ToolName))
		default:
			return anthropic.MessageParam{}, fmt.Errorf("anthropic: unsupported assistant part kind %q", p.Kind)
		}
	}
	if len(blocks) == 0 {
		return anthropic.MessageParam{}, fmt.Errorf("anthropic: assistant message is empty after trimming")
	}
	return anthropic.NewAssistantMessage(blocks...), nil
}

func reasoningPartToAnthropicBlock(p aipapi.Part) (anthropic.ContentBlockParamUnion, error) {
	if p.Reasoning == nil {
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: reasoning part missing payload")
	}
	d := aipapi.NormalizeReasoningDialect(p.Reasoning.Dialect)
	switch d {
	case aipapi.ReasoningDialectAnthropicThinkingV1:
		// Preserve empty signatures exactly; never fabricate.
		return anthropic.NewThinkingBlock(p.Reasoning.Signature, p.Reasoning.Text), nil
	case aipapi.ReasoningDialectAnthropicRedactedThinkingV1:
		data := ""
		if len(p.Reasoning.Opaque) > 0 {
			var opaque struct {
				Type string `json:"type"`
				Data string `json:"data"`
			}
			if err := json.Unmarshal(p.Reasoning.Opaque, &opaque); err != nil {
				return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: redacted_thinking opaque: %w", err)
			}
			data = opaque.Data
		}
		return anthropic.NewRedactedThinkingBlock(data), nil
	default:
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: reasoning dialect %q not supported for anthropic replay", d)
	}
}

func userPartsToBlocks(parts []aipapi.Part) ([]anthropic.ContentBlockParamUnion, error) {
	out := make([]anthropic.ContentBlockParamUnion, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case aipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, anthropic.NewTextBlock(p.Text))
		case aipapi.PartToolResult:
			blk, err := toolResultBlockFromPart(p)
			if err != nil {
				return nil, err
			}
			out = append(out, blk)
		case aipapi.PartImageRef:
			blk, err := imageBlockFromPart(p)
			if err != nil {
				return nil, err
			}
			out = append(out, blk)
		case aipapi.PartFileRef:
			blk, err := documentBlockFromPart(p)
			if err != nil {
				return nil, err
			}
			out = append(out, blk)
		default:
			return nil, fmt.Errorf("anthropic: unsupported part kind %q in user message", p.Kind)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("anthropic: user message has no mappable content blocks")
	}
	return out, nil
}

func toolResultBlockFromPart(p aipapi.Part) (anthropic.ContentBlockParamUnion, error) {
	if strings.TrimSpace(p.ToolCallID) == "" {
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: tool_result requires tool_call_id")
	}
	content := p.Text
	if content == "" && len(p.Content) > 0 {
		content = string(p.Content)
	}
	return anthropic.NewToolResultBlock(p.ToolCallID, content, false), nil
}

func imageBlockFromPart(p aipapi.Part) (anthropic.ContentBlockParamUnion, error) {
	ref := p.ImageRef
	if strings.HasPrefix(ref, "data:") {
		mime, b64, ok := aipapi.StripDataURLBase64(ref)
		if !ok {
			return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: invalid data URL in image part")
		}
		_ = mime
		return anthropic.NewImageBlock(anthropic.Base64ImageSourceParam{
			Data:      b64,
			MediaType: anthropic.Base64ImageSourceMediaType(pickImageMediaType(mime, p.ImageMIME)),
		}), nil
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return anthropic.NewImageBlock(anthropic.URLImageSourceParam{URL: ref}), nil
	}
	return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: imageRef must be a data URL or http(s) URL, got %q", ref)
}

func pickImageMediaType(fromDataURL, fromPart string) string {
	if s := strings.TrimSpace(fromPart); s != "" {
		return s
	}
	return fromDataURL
}

func documentBlockFromPart(p aipapi.Part) (anthropic.ContentBlockParamUnion, error) {
	ref := p.FileRef
	if !strings.HasPrefix(ref, "data:") {
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: file part requires a data URL, got %q", ref)
	}
	mime, b64, ok := aipapi.StripDataURLBase64(ref)
	if !ok {
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("anthropic: invalid data URL in file part")
	}
	_ = mime
	// v1: PDF and similar via base64 application/pdf
	return anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
		Data: b64,
	}), nil
}

func buildTools(tools []aipapi.ToolDef) ([]anthropic.ToolUnionParam, error) {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schema, err := toolInputSchema(t)
		if err != nil {
			return nil, err
		}
		tool := anthropic.ToolParam{
			Name:        t.Name,
			InputSchema: schema,
		}
		if strings.TrimSpace(t.Description) != "" {
			tool.Description = param.NewOpt(t.Description)
		}
		tool.Strict = anthropic.Bool(true)
		out = append(out, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return out, nil
}

func toolInputSchema(t aipapi.ToolDef) (anthropic.ToolInputSchemaParam, error) {
	if len(t.Parameters) == 0 {
		return anthropic.ToolInputSchemaParam{
			Type:       "object",
			Properties: map[string]any{},
		}, nil
	}
	var s anthropic.ToolInputSchemaParam
	if err := json.Unmarshal(t.Parameters, &s); err != nil {
		return anthropic.ToolInputSchemaParam{}, fmt.Errorf("anthropic: tool %q parameters: %w", t.Name, err)
	}
	return s, nil
}

func toolChoiceUnion(call *aipapi.Call, nTools int) anthropic.ToolChoiceUnionParam {
	if nTools == 0 {
		return anthropic.ToolChoiceUnionParam{}
	}
	dpar := disableParallelToolUseOpt(call)
	mode := call.ToolChoice.Mode
	if mode == "" {
		mode = aipapi.ToolChoiceAuto
	}
	switch mode {
	case aipapi.ToolChoiceNone:
		n := anthropic.NewToolChoiceNoneParam()
		return anthropic.ToolChoiceUnionParam{OfNone: &n}
	case aipapi.ToolChoiceAny:
		a := anthropic.ToolChoiceAnyParam{DisableParallelToolUse: dpar}
		return anthropic.ToolChoiceUnionParam{OfAny: &a}
	case aipapi.ToolChoiceRequired:
		if call.ToolChoice.Name != "" {
			t := anthropic.ToolChoiceToolParam{
				Name:                   call.ToolChoice.Name,
				DisableParallelToolUse: dpar,
			}
			return anthropic.ToolChoiceUnionParam{OfTool: &t}
		}
		a := anthropic.ToolChoiceAnyParam{DisableParallelToolUse: dpar}
		return anthropic.ToolChoiceUnionParam{OfAny: &a}
	case aipapi.ToolChoiceAuto, "":
		fallthrough
	default:
		au := anthropic.ToolChoiceAutoParam{DisableParallelToolUse: dpar}
		return anthropic.ToolChoiceUnionParam{OfAuto: &au}
	}
}

// When ParallelToolCalls is false, set Anthropic disable_parallel_tool_use to true.
func disableParallelToolUseOpt(call *aipapi.Call) param.Opt[bool] {
	if call == nil || call.Options.ParallelToolCalls == nil {
		return param.Opt[bool]{}
	}
	if *call.Options.ParallelToolCalls {
		return param.Opt[bool]{}
	}
	return param.NewOpt(true)
}
