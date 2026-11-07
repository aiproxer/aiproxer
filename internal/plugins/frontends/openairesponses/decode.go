package openairesponses

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
	"github.com/aiproxer/aiproxer/internal/plugins/protocols/openairesponsesitem"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// errSkipMalformedHistory marks replayed garbage (empty-name function_call /
// empty call_id outputs) that should be dropped instead of failing decode.
var errSkipMalformedHistory = errors.New("skip malformed history fragment")

// Extension keys used by this frontend. extModelJSONKey is round-trip metadata;
// extWebSearchDeclarationsKey is a deliberately protocol-facing opaque carrier.
const (
	extModelJSONKey = "openairesponses.model"

	// extWebSearchDeclarationsKey is the documented cross-module wire contract:
	// the OpenAI Responses frontend preserves the closed hosted web-search
	// declaration table under this opaque extension key, and the SIWC connector
	// consumes the same literal. Both modules define it privately; the staged
	// end-to-end proof pins the two definitions together.
	extWebSearchDeclarationsKey = "openairesponses.web_search_declarations"

	// maxWebSearchDeclarationsBytes bounds the carrier value to the same 64 KiB
	// the backend-plugin extension metadata carrier accepts.
	maxWebSearchDeclarationsBytes = 64 << 10
)

// supportedWebSearchDeclarationTypes is the closed OpenAI Responses hosted
// web-search declaration table. A new/unknown variant fails closed until a
// reviewed table update; a function or custom tool that happens to be named
// web_search is not in this table and stays a caller-executed tool.
var supportedWebSearchDeclarationTypes = map[string]struct{}{
	"web_search":                    {},
	"web_search_preview":            {},
	"web_search_2025_08_26":         {},
	"web_search_preview_2025_03_11": {},
}

func isSupportedWebSearchDeclarationType(typ string) bool {
	_, ok := supportedWebSearchDeclarationTypes[typ]
	return ok
}

// isHostedWebSearchDeclarationCandidate reports whether a raw tool type belongs
// to the hosted web-search family, including an unknown/new variant. The proof
// fast path uses this broad check to decline to canonical processing before
// parseTools runs; canonical decode then decides whether the exact type is in
// the closed table. It is intentionally name-blind and namespace-blind.
func isHostedWebSearchDeclarationCandidate(typ string) bool {
	return strings.HasPrefix(typ, "web_search")
}

// hasHostedWebSearchDeclaration scans raw top-level tools bytes for a hosted
// web-search declaration without calling parseTools.
func hasHostedWebSearchDeclaration(raw json.RawMessage) bool {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return false
	}
	for _, item := range items {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &probe); err != nil {
			continue
		}
		if isHostedWebSearchDeclarationCandidate(probe.Type) {
			return true
		}
	}
	return false
}

// DecodeOptions configures decoding of a Responses API create request.
type DecodeOptions struct {
	// RouteSelector is required (e.g. "stub:gpt-4o-mini"); usually from X-AIP-Route header.
	RouteSelector string
	// Headers carries optional HTTP carriers (e.g. AIProxer session resume headers).
	Headers http.Header
	// HTTPHeaders carries configured header aliases; zero value uses defaults.
	HTTPHeaders aipsdk.HTTPHeaders
	// NamespaceTools selects the operator policy for OpenAI Responses namespace
	// tool declarations: "project" (default) or "reject".
	NamespaceTools string
}

// DecodedCreate is the result of decoding POST /v1/responses JSON.
type DecodedCreate struct {
	Call   *aipapi.Call
	Stream bool
	Model  string
	// namespaceTools is the per-request reversible projection built from the
	// request's namespace declarations. It is consumed by the same handler's
	// response encoder and never persisted or sent upstream.
	namespaceTools *namespaceToolMapping
}

type wireCreate struct {
	Model         string          `json:"model"`
	Stream        bool            `json:"stream"`
	Input         json.RawMessage `json:"input"`
	Instructions  json.RawMessage `json:"instructions"`
	Tools         json.RawMessage `json:"tools"`
	ToolChoice    json.RawMessage `json:"tool_choice"`
	ParallelTools *bool           `json:"parallel_tool_calls"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	MaxOut        *int            `json:"max_output_tokens"`
	Text          json.RawMessage `json:"text"`
	// Metadata may include AIProxer session keys ([sessionwire.MetaKeyAuthoritativeSessionID]).
	Metadata       map[string]string `json:"metadata,omitempty"`
	PromptCacheKey string            `json:"prompt_cache_key"`
}

var responsesKnownBodyKeys = map[string]bool{
	"model": true, "stream": true, "input": true, "instructions": true,
	"tools": true, "tool_choice": true, "parallel_tool_calls": true,
	"temperature": true, "top_p": true, "max_output_tokens": true, "metadata": true,
	"text":                 true,
	"previous_response_id": true, "store": true, "truncation": true,
	"prompt_cache_key": true,
}

// DecodeCreateRequest maps an OpenAI Responses create JSON body into a canonical call.
func DecodeCreateRequest(body []byte, opts DecodeOptions) (*DecodedCreate, error) {
	sel := strings.TrimSpace(opts.RouteSelector)
	if sel == "" {
		return nil, errors.New("openairesponses: route selector is required: send a model with a route prefix (for example \"backend:model\")")
	}
	var w wireCreate
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("openairesponses: invalid json: %w", err)
	}
	model := strings.TrimSpace(w.Model)
	if model == "" {
		return nil, errors.New("openairesponses: model is required")
	}
	if len(w.Input) == 0 {
		return nil, errors.New("openairesponses: input is required")
	}
	namespacePolicy, err := normalizeNamespaceToolPolicy(opts.NamespaceTools)
	if err != nil {
		return nil, err
	}
	if err := frontendlimits.Count("metadata", len(w.Metadata), frontendlimits.MaxMetadata); err != nil {
		return nil, fmt.Errorf("openairesponses: %w", err)
	}
	if err := sessionwire.ValidateMetadata(w.Metadata); err != nil {
		return nil, fmt.Errorf("openairesponses: %w", err)
	}

	instructions, err := parseInstructions(w.Instructions)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: instructions: %w", err)
	}
	parsedToolSet, err := parseTools(w.Tools, namespacePolicy)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: tools: %w", err)
	}
	msgs, err := parseInput(w.Input, parsedToolSet.Namespace, namespacePolicy)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: input: %w", err)
	}
	toolChoice, err := parseToolChoice(w.ToolChoice, parsedToolSet.Namespace)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: tool_choice: %w", err)
	}
	verbosity, remainingText, err := parseTextConfig(w.Text)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: text: %w", err)
	}

	modelRaw, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("openairesponses: marshal model extension: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}
	if b, err := json.Marshal(openrouterwire.FlavorResponses); err == nil {
		ext[openrouterwire.ExtUpstreamFlavor] = b
	}
	if len(remainingText) > 0 {
		ext[openrouterwire.ExtraBodyExtPrefix+"text"] = remainingText
	}
	if len(parsedToolSet.WebSearchDeclarations) > 0 {
		carrier, err := json.Marshal(parsedToolSet.WebSearchDeclarations)
		if err != nil {
			return nil, fmt.Errorf("openairesponses: marshal web-search declarations: %w", err)
		}
		ext[extWebSearchDeclarationsKey] = carrier
	}

	var rawBody map[string]json.RawMessage
	if json.Unmarshal(body, &rawBody) == nil {
		openrouterwire.CaptureBodyFields(rawBody, ext)
		openrouterwire.CaptureExtraBodyFields(rawBody, ext, responsesKnownBodyKeys)
	}
	if opts.Headers != nil {
		openrouterwire.CaptureHeaders(opts.Headers, ext)
	}

	call := &aipapi.Call{
		Route:          aipapi.RouteIntent{Selector: sel},
		Instructions:   instructions,
		Messages:       msgs,
		Tools:          parsedToolSet.Tools,
		ToolChoice:     toolChoice,
		Extensions:     ext,
		PromptCacheKey: strings.TrimSpace(w.PromptCacheKey),
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIResponses,
			DeliveryMode: aipapi.DeliveryModeFromClientStream(w.Stream),
		},
		Options: aipapi.GenerationOptions{
			Temperature:       w.Temperature,
			TopP:              w.TopP,
			MaxOutputTokens:   w.MaxOut,
			ParallelToolCalls: w.ParallelTools,
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
		return nil, fmt.Errorf("openairesponses: %w", err)
	}
	return &DecodedCreate{Call: call, Stream: w.Stream, Model: model, namespaceTools: parsedToolSet.Namespace}, nil
}

// parseTextConfig extracts the canonical Responses text verbosity while
// returning any other text configuration for provider-specific passthrough.
func parseTextConfig(raw json.RawMessage) (aipapi.VerbosityLevel, json.RawMessage, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return "", nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err == nil {
			err = errors.New("must be a JSON object")
		}
		return "", nil, err
	}
	var verbosity aipapi.VerbosityLevel
	if rawVerbosity, ok := fields["verbosity"]; ok {
		if jsonpresence.IsPresentNonNullJSON(rawVerbosity) {
			var value string
			if err := json.Unmarshal(rawVerbosity, &value); err != nil {
				return "", nil, fmt.Errorf("verbosity must be a string: %w", err)
			}
			parsed, err := aipapi.ParseVerbosityLevel(value)
			if err != nil {
				return "", nil, err
			}
			verbosity = parsed
		}
		delete(fields, "verbosity")
	}
	if len(fields) == 0 {
		return verbosity, nil, nil
	}
	remaining, err := json.Marshal(fields)
	if err != nil {
		return "", nil, fmt.Errorf("preserve fields: %w", err)
	}
	return verbosity, remaining, nil
}

func parseInstructions(raw json.RawMessage) ([]aipapi.Message, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return []aipapi.Message{}, nil
	}
	// String instructions -> single system message.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("openairesponses: instructions: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return []aipapi.Message{}, nil
		}
		return []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart(s)},
		}}, nil
	}
	return nil, errors.New("openairesponses: instructions must be a JSON string in this adapter")
}

func parseInput(raw json.RawMessage, namespaceTools *namespaceToolMapping, policy string) ([]aipapi.Message, error) {
	if len(raw) == 0 {
		return nil, errors.New("openairesponses: input is empty")
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("openairesponses: input string: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, errors.New("openairesponses: input string is empty")
		}
		return []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(s)},
		}}, nil
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("openairesponses: input array: %w", err)
		}
		if err := frontendlimits.Count("input", len(items), frontendlimits.MaxMessages); err != nil {
			return nil, err
		}
		out := make([]aipapi.Message, 0, len(items))
		for i, it := range items {
			m, err := parseInputItem(it, namespaceTools, policy)
			if err != nil {
				if errors.Is(err, errSkipMalformedHistory) {
					continue
				}
				return nil, fmt.Errorf("openairesponses: input[%d]: %w", i, err)
			}
			out = append(out, m)
		}
		return out, nil
	default:
		return nil, errors.New("openairesponses: input must be a string or array")
	}
}

func parseInputItem(raw json.RawMessage, namespaceTools *namespaceToolMapping, policy string) (aipapi.Message, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return aipapi.Message{}, fmt.Errorf("openairesponses: input item json: %w", err)
	}
	switch strings.TrimSpace(probe.Type) {
	case "function_call_output":
		return parseFunctionCallOutputItem(raw, policy)
	case "function_call":
		return parseFunctionCallInputItem(raw, namespaceTools, policy)
	case "reasoning":
		return parseReasoningInputItem(raw)
	case "", "message":
		return parseMessageInputItem(raw)
	default:
		return aipapi.Message{}, fmt.Errorf("openairesponses: unsupported input item type %q", probe.Type)
	}
}

func parseReasoningInputItem(raw json.RawMessage) (aipapi.Message, error) {
	if err := frontendlimits.Bytes("reasoning", len(raw), aipapi.MaxReasoningOpaqueBytes); err != nil {
		return aipapi.Message{}, err
	}
	canon, err := openairesponsesitem.CanonizeReasoningItemOpaque(raw)
	if err != nil {
		return aipapi.Message{}, err
	}
	var wire struct {
		Summary json.RawMessage `json:"summary"`
	}
	if err := json.Unmarshal(canon, &wire); err != nil {
		return aipapi.Message{}, fmt.Errorf("openairesponses: invalid reasoning item")
	}
	return aipapi.Message{
		Role: aipapi.RoleAssistant,
		Parts: []aipapi.Part{{
			Kind: aipapi.PartReasoning,
			Reasoning: &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
				Text:    reasoningSummaryText(wire.Summary),
				Opaque:  append(json.RawMessage(nil), canon...),
			},
		}},
	}, nil
}

func reasoningSummaryText(summary json.RawMessage) string {
	if !jsonpresence.IsPresentNonNullJSON(summary) {
		return ""
	}
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(summary, &items); err != nil {
		return ""
	}
	var b strings.Builder
	for _, it := range items {
		if strings.TrimSpace(it.Type) != "" && it.Type != "summary_text" {
			continue
		}
		if t := strings.TrimSpace(it.Text); t != "" {
			b.WriteString(t)
		}
	}
	return b.String()
}

func parseMessageInputItem(raw json.RawMessage) (aipapi.Message, error) {
	var m struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return aipapi.Message{}, fmt.Errorf("openairesponses: message input json: %w", err)
	}
	role, err := mapRole(m.Role)
	if err != nil {
		return aipapi.Message{}, fmt.Errorf("openairesponses: message input role: %w", err)
	}
	parts, err := parseContent(m.Content, role)
	if err != nil {
		return aipapi.Message{}, fmt.Errorf("openairesponses: message input content: %w", err)
	}
	return aipapi.Message{Role: role, Parts: parts}, nil
}

func parseFunctionCallInputItem(raw json.RawMessage, namespaceTools *namespaceToolMapping, policy string) (aipapi.Message, error) {
	var v struct {
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Namespace string          `json:"namespace"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return aipapi.Message{}, fmt.Errorf("openairesponses: function_call json: %w", err)
	}
	callID := strings.TrimSpace(v.CallID)
	if callID == "" {
		return aipapi.Message{}, errors.New("openairesponses: function_call requires call_id")
	}
	name := strings.TrimSpace(v.Name)
	namespace := strings.TrimSpace(v.Namespace)
	if namespace != "" {
		if policy == namespaceToolPolicyReject {
			return aipapi.Message{}, fmt.Errorf("openairesponses: namespaced function_call is refused by namespace_tools=%s", namespaceToolPolicyReject)
		}
		if name == "" {
			return aipapi.Message{}, errors.New("openairesponses: namespaced function_call requires a name")
		}
		projected, err := namespaceTools.mapHistory(namespace, name)
		if err != nil {
			return aipapi.Message{}, fmt.Errorf("openairesponses: namespaced function_call: %w", err)
		}
		name = projected
	}
	if name == "" {
		return aipapi.Message{}, errSkipMalformedHistory
	}
	arguments, err := canonicalFunctionCallArguments(v.Arguments)
	if err != nil {
		return aipapi.Message{}, err
	}
	return aipapi.Message{
		Role: aipapi.RoleAssistant,
		Parts: []aipapi.Part{{
			Kind:       aipapi.PartJSON,
			ToolCallID: callID,
			ToolName:   name,
			Content:    arguments,
		}},
	}, nil
}

// canonicalFunctionCallArguments maps the Responses wire argument carrier to
// the canonical tool-call argument document. The Responses API sends a JSON
// string holding the document; some clients send the document directly.
// Valid documents keep their exact bytes, including a JSON string document
// that is its own argument text. Non-JSON text is stored as a JSON string, the
// only valid canonical representation, and absent, null, or whitespace-only
// arguments become the established empty-object document used by the other
// frontends and openaiwire, because an empty string is not a valid argument
// document. The Responses item id is a separate identifier and never becomes
// call authority.
func canonicalFunctionCallArguments(raw json.RawMessage) (json.RawMessage, error) {
	if !jsonpresence.IsPresentNonNullJSON(raw) {
		return json.RawMessage(`{}`), nil
	}
	if err := frontendlimits.Bytes("function_call.arguments", len(raw), frontendlimits.MaxRawJSONPayload); err != nil {
		return nil, err
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("openairesponses: function_call arguments: %w", err)
		}
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return json.RawMessage(`{}`), nil
		}
		if json.Valid([]byte(s)) {
			return append(json.RawMessage(nil), s...), nil
		}
		encoded, err := json.Marshal(s)
		if err != nil {
			return nil, fmt.Errorf("openairesponses: function_call arguments: %w", err)
		}
		return encoded, nil
	}
	if !json.Valid(raw) {
		return nil, errors.New("openairesponses: function_call arguments must be JSON")
	}
	return append(json.RawMessage(nil), raw...), nil
}

func parseFunctionCallOutputItem(raw json.RawMessage, policy string) (aipapi.Message, error) {
	var v struct {
		CallID    string          `json:"call_id"`
		Namespace string          `json:"namespace"`
		Output    json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return aipapi.Message{}, fmt.Errorf("openairesponses: function_call_output json: %w", err)
	}
	if strings.TrimSpace(v.CallID) == "" {
		return aipapi.Message{}, errSkipMalformedHistory
	}
	if strings.TrimSpace(v.Namespace) != "" && policy == namespaceToolPolicyReject {
		return aipapi.Message{}, fmt.Errorf("openairesponses: namespaced function_call_output is refused by namespace_tools=%s", namespaceToolPolicyReject)
	}
	out := v.Output
	if jsonpresence.IsAbsentOrJSONNull(out) {
		return aipapi.Message{}, errors.New("openairesponses: function_call_output requires output")
	}
	if err := frontendlimits.Bytes("function_call_output.output", len(out), frontendlimits.MaxRawJSONPayload); err != nil {
		return aipapi.Message{}, err
	}
	if out[0] == '"' {
		var s string
		if err := json.Unmarshal(out, &s); err != nil {
			return aipapi.Message{}, fmt.Errorf("openairesponses: function_call_output string: %w", err)
		}
		var err error
		out, err = json.Marshal(s)
		if err != nil {
			return aipapi.Message{}, fmt.Errorf("openairesponses: function_call_output re-marshal: %w", err)
		}
	}
	if !json.Valid(out) {
		return aipapi.Message{}, errors.New("openairesponses: function_call_output output must be JSON")
	}
	return aipapi.Message{
		Role: aipapi.RoleTool,
		Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: strings.TrimSpace(v.CallID),
			Content:    append(json.RawMessage(nil), out...),
		}},
	}, nil
}

func mapRole(r string) (aipapi.Role, error) {
	switch strings.TrimSpace(strings.ToLower(r)) {
	case "user":
		return aipapi.RoleUser, nil
	case "assistant":
		return aipapi.RoleAssistant, nil
	case "system", "developer":
		return aipapi.RoleSystem, nil
	case "tool":
		return aipapi.RoleTool, nil
	case "":
		return aipapi.RoleUser, nil
	default:
		return "", fmt.Errorf("openairesponses: unsupported role %q", r)
	}
}

func parseContent(raw json.RawMessage, role aipapi.Role) ([]aipapi.Part, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, errors.New("message content is required")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("openairesponses: message content string: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, errors.New("message content string is empty")
		}
		return []aipapi.Part{aipapi.TextPart(s)}, nil
	}
	if raw[0] != '[' {
		return nil, errors.New("message content must be a string or array")
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("openairesponses: message content array: %w", err)
	}
	if err := frontendlimits.Count("content", len(blocks), frontendlimits.MaxParts); err != nil {
		return nil, err
	}
	parts := make([]aipapi.Part, 0, len(blocks))
	for i, blk := range blocks {
		p, err := parseContentBlock(blk, role)
		if err != nil {
			return nil, fmt.Errorf("openairesponses: content[%d]: %w", i, err)
		}
		parts = append(parts, p)
	}
	return parts, nil
}

func parseContentBlock(blk map[string]json.RawMessage, role aipapi.Role) (aipapi.Part, error) {
	tRaw, ok := blk["type"]
	if !ok {
		return aipapi.Part{}, errors.New("content block missing type")
	}
	var typ string
	if err := json.Unmarshal(tRaw, &typ); err != nil {
		return aipapi.Part{}, fmt.Errorf("openairesponses: content block type: %w", err)
	}
	typ = strings.TrimSpace(typ)
	switch typ {
	case "input_text", "output_text":
		if typ == "output_text" && role != aipapi.RoleAssistant {
			return aipapi.Part{}, errors.New("unsupported content block type output_text for non-assistant role")
		}
		var s struct {
			Text string `json:"text"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return aipapi.Part{}, fmt.Errorf("openairesponses: input_text wire: %w", err)
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return aipapi.Part{}, fmt.Errorf("openairesponses: input_text json: %w", err)
		}
		if strings.TrimSpace(s.Text) == "" {
			return aipapi.Part{}, errors.New("input_text requires text")
		}
		return aipapi.TextPart(s.Text), nil
	case "input_image":
		var s struct {
			ImageURL string `json:"image_url"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return aipapi.Part{}, fmt.Errorf("openairesponses: input_image wire: %w", err)
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return aipapi.Part{}, fmt.Errorf("openairesponses: input_image json: %w", err)
		}
		if strings.TrimSpace(s.ImageURL) == "" {
			return aipapi.Part{}, errors.New("input_image requires image_url")
		}
		return openaiwire.ImagePartFromURL(s.ImageURL)
	case "input_file":
		var s struct {
			FileData string `json:"file_data"`
			Filename string `json:"filename"`
		}
		raw, err := openaiwire.MarshalBlock(blk)
		if err != nil {
			return aipapi.Part{}, fmt.Errorf("openairesponses: input_file wire: %w", err)
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return aipapi.Part{}, fmt.Errorf("openairesponses: input_file json: %w", err)
		}
		if strings.TrimSpace(s.FileData) == "" {
			return aipapi.Part{}, errors.New("input_file requires file_data")
		}
		if err := frontendlimits.StringBytes("input_file.file_data", s.FileData, frontendlimits.MaxBase64Data); err != nil {
			return aipapi.Part{}, err
		}
		return openaiwire.FilePartFromBase64(s.Filename, s.FileData), nil
	default:
		return aipapi.Part{}, fmt.Errorf("openairesponses: unsupported content block type %q", typ)
	}
}

// parsedTools is the result of one top-level tools parse: canonical function
// tools, the preserved hosted web-search declaration types, and the per-request
// reversible namespace projection (always non-nil, possibly empty).
type parsedTools struct {
	Tools                 []aipapi.ToolDef
	WebSearchDeclarations []string
	Namespace             *namespaceToolMapping
}

func parseTools(raw json.RawMessage, policy string) (parsedTools, error) {
	mapping := newNamespaceToolMapping()
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return parsedTools{Tools: []aipapi.ToolDef{}, Namespace: mapping}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return parsedTools{}, fmt.Errorf("openairesponses: tools: %w", err)
	}
	if err := frontendlimits.Count("tools", len(items), frontendlimits.MaxTools); err != nil {
		return parsedTools{}, err
	}
	out := parsedTools{Tools: make([]aipapi.ToolDef, 0, len(items)), Namespace: mapping}
	for i, it := range items {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(it, &probe); err != nil {
			return parsedTools{}, fmt.Errorf("openairesponses: tools[%d]: %w", i, err)
		}
		typ := strings.TrimSpace(probe.Type)
		switch {
		case typ == "" || typ == "function":
			name, desc, params, err := parseFunctionToolWire(it)
			if err != nil {
				return parsedTools{}, fmt.Errorf("openairesponses: tools[%d]: %w", i, err)
			}
			if name == "" {
				return parsedTools{}, fmt.Errorf("openairesponses: tools[%d]: function name is required", i)
			}
			if len(params) == 0 {
				params = json.RawMessage(`{}`)
			}
			if err := frontendlimits.Bytes("tool parameters", len(params), frontendlimits.MaxToolSchema); err != nil {
				return parsedTools{}, fmt.Errorf("openairesponses: tools[%d]: %w", i, err)
			}
			if err := mapping.addTopLevel(name); err != nil {
				return parsedTools{}, fmt.Errorf("openairesponses: tools[%d]: %w", i, err)
			}
			out.Tools = append(out.Tools, aipapi.ToolDef{
				Name:        name,
				Description: desc,
				Parameters:  params,
			})
		case typ == "namespace":
			if policy == namespaceToolPolicyReject {
				return parsedTools{}, fmt.Errorf("openairesponses: tools[%d]: namespace tool declarations are refused by namespace_tools=%s", i, namespaceToolPolicyReject)
			}
			if err := expandNamespaceTool(it, i, mapping, &out.Tools); err != nil {
				return parsedTools{}, err
			}
		case isSupportedWebSearchDeclarationType(typ):
			// Type strings only: every other declaration attribute is
			// deliberately discarded and never reaches the carrier.
			out.WebSearchDeclarations = append(out.WebSearchDeclarations, typ)
		default:
			return parsedTools{}, fmt.Errorf("openairesponses: tools[%d]: unsupported type %q", i, probe.Type)
		}
	}
	if len(out.WebSearchDeclarations) > 0 {
		carrier, err := json.Marshal(out.WebSearchDeclarations)
		if err != nil {
			return parsedTools{}, fmt.Errorf("openairesponses: marshal web-search declarations: %w", err)
		}
		if err := frontendlimits.Bytes("tools web search declarations", len(carrier), maxWebSearchDeclarationsBytes); err != nil {
			return parsedTools{}, err
		}
	}
	return out, nil
}

func parseToolChoice(raw json.RawMessage, namespaceTools *namespaceToolMapping) (aipapi.ToolChoice, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return aipapi.ToolChoice{}, fmt.Errorf("openairesponses: tool_choice string json: %w", err)
		}
		switch strings.TrimSpace(strings.ToLower(s)) {
		case "auto", "":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
		case "none":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, nil
		case "required":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, nil
		default:
			return aipapi.ToolChoice{}, fmt.Errorf("openairesponses: unsupported tool_choice string %q", s)
		}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return aipapi.ToolChoice{}, fmt.Errorf("openairesponses: tool_choice: %w", err)
	}
	switch strings.TrimSpace(obj.Type) {
	case "function":
		name := strings.TrimSpace(obj.Function.Name)
		if name == "" {
			return aipapi.ToolChoice{}, errors.New("openairesponses: tool_choice function name is required")
		}
		resolved, err := namespaceTools.resolveToolChoiceName(name)
		if err != nil {
			return aipapi.ToolChoice{}, fmt.Errorf("openairesponses: tool_choice: %w", err)
		}
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: resolved}, nil
	default:
		return aipapi.ToolChoice{}, fmt.Errorf("openairesponses: unsupported tool_choice type %q", obj.Type)
	}
}

// ModelFromCall returns the wire model string stored during decode.
func ModelFromCall(c *aipapi.Call) string {
	return openaiwire.ModelFromExtensions(c, extModelJSONKey)
}
