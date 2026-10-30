package gemini

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
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

const extModelJSONKey = "gemini.model"

// DecodeOptions configures decoding of a generateContent request body.
type DecodeOptions struct {
	// RouteSelector is required (e.g. "stub:gemini-2.0-flash"); usually from X-AIP-Route header.
	RouteSelector string
	// Model is taken from the URL path (…/models/{model}:generateContent); must be non-empty.
	Model string
	// Stream is true when the request targets :streamGenerateContent.
	Stream  bool
	Headers http.Header
	// HTTPHeaders carries configured header aliases; zero value uses defaults.
	HTTPHeaders aipsdk.HTTPHeaders
}

// DecodedGenerate is the result of decoding a generateContent JSON body.
type DecodedGenerate struct {
	Call   *aipapi.Call
	Stream bool
	Model  string
}

type wireGenerate struct {
	Contents          []json.RawMessage `json:"contents"`
	SystemInstruction json.RawMessage   `json:"systemInstruction"`
	GenerationConfig  json.RawMessage   `json:"generationConfig"`
	Tools             json.RawMessage   `json:"tools"`
	ToolConfig        json.RawMessage   `json:"toolConfig"`
}

type wireContent struct {
	Role  string            `json:"role"`
	Parts []json.RawMessage `json:"parts"`
}

type wireBlob struct {
	MIMEType      string `json:"mimeType"`
	MIMETypeSnake string `json:"mime_type"`
	Data          string `json:"data"`
}

// DecodeGenerateContentRequest maps a Gemini generateContent JSON body into a canonical call.
func DecodeGenerateContentRequest(body []byte, opts DecodeOptions) (*DecodedGenerate, error) {
	sel := strings.TrimSpace(opts.RouteSelector)
	if sel == "" {
		return nil, errors.New("gemini: route selector is required: send a model with a route prefix (for example \"backend:model\")")
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		return nil, errors.New("gemini: model is required")
	}

	var w wireGenerate
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("gemini: invalid json: %w", err)
	}
	if len(w.Contents) == 0 {
		return nil, errors.New("gemini: contents is required")
	}
	if err := frontendlimits.Count("contents", len(w.Contents), frontendlimits.MaxMessages); err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}

	instructions, err := parseSystemInstruction(w.SystemInstruction)
	if err != nil {
		return nil, fmt.Errorf("gemini: system instruction: %w", err)
	}
	msgs, err := parseContents(w.Contents)
	if err != nil {
		return nil, fmt.Errorf("gemini: contents: %w", err)
	}
	genOpts, err := parseGenerationConfig(w.GenerationConfig)
	if err != nil {
		return nil, fmt.Errorf("gemini: generation config: %w", err)
	}
	tools, err := parseTools(w.Tools)
	if err != nil {
		return nil, fmt.Errorf("gemini: tools: %w", err)
	}
	toolChoice, err := parseToolConfig(w.ToolConfig, len(tools))
	if err != nil {
		return nil, fmt.Errorf("gemini: tool config: %w", err)
	}

	modelRaw, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("gemini: marshal model extension: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}

	call := &aipapi.Call{
		Route:        aipapi.RouteIntent{Selector: sel},
		Instructions: instructions,
		Messages:     msgs,
		Tools:        tools,
		ToolChoice:   toolChoice,
		Options:      genOpts,
		Extensions:   ext,
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationGeminiGenerateContent,
			DeliveryMode: aipapi.DeliveryModeFromClientStream(opts.Stream),
		},
	}
	if opts.Headers != nil {
		sessionwire.ApplyAuthoritativeHeaders(&call.Session, opts.Headers)
		identitywire.CaptureClientUserAgent(&call.Invocation, opts.Headers)
	}
	if err := sessionwire.ApplyWorkspaceHint(call, opts.Headers, opts.HTTPHeaders); err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}
	return &DecodedGenerate{Call: call, Stream: opts.Stream, Model: model}, nil
}

func parseSystemInstruction(raw json.RawMessage) ([]aipapi.Message, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, nil
	}
	var wc wireContent
	if err := json.Unmarshal(raw, &wc); err != nil {
		return nil, fmt.Errorf("gemini: systemInstruction: %w", err)
	}
	if len(wc.Parts) == 0 {
		return nil, errors.New("gemini: systemInstruction requires parts")
	}
	if err := frontendlimits.Count("systemInstruction.parts", len(wc.Parts), frontendlimits.MaxParts); err != nil {
		return nil, err
	}
	parts, err := parseParts(wc.Parts)
	if err != nil {
		return nil, fmt.Errorf("gemini: systemInstruction: %w", err)
	}
	return []aipapi.Message{{Role: aipapi.RoleSystem, Parts: parts}}, nil
}

func parseContents(items []json.RawMessage) ([]aipapi.Message, error) {
	out := make([]aipapi.Message, 0, len(items))
	for i, raw := range items {
		var wc wireContent
		if err := json.Unmarshal(raw, &wc); err != nil {
			return nil, fmt.Errorf("gemini: contents[%d]: %w", i, err)
		}
		role, err := mapGeminiRole(wc.Role)
		if err != nil {
			return nil, fmt.Errorf("gemini: contents[%d]: %w", i, err)
		}
		if len(wc.Parts) == 0 {
			return nil, fmt.Errorf("gemini: contents[%d]: parts is required", i)
		}
		if err := frontendlimits.Count("parts", len(wc.Parts), frontendlimits.MaxParts); err != nil {
			return nil, fmt.Errorf("gemini: contents[%d]: %w", i, err)
		}
		parts, err := parseParts(wc.Parts)
		if err != nil {
			return nil, fmt.Errorf("gemini: contents[%d]: %w", i, err)
		}
		out = append(out, aipapi.Message{Role: role, Parts: parts})
	}
	return out, nil
}

func mapGeminiRole(r string) (aipapi.Role, error) {
	switch strings.TrimSpace(strings.ToLower(r)) {
	case "user":
		return aipapi.RoleUser, nil
	case "model":
		return aipapi.RoleAssistant, nil
	case "":
		return aipapi.RoleUser, nil
	default:
		return "", fmt.Errorf("unsupported role %q", r)
	}
}

func parseParts(raw []json.RawMessage) ([]aipapi.Part, error) {
	out := make([]aipapi.Part, 0, len(raw))
	for i, p := range raw {
		part, err := parsePart(p)
		if err != nil {
			return nil, fmt.Errorf("parts[%d]: %w", i, err)
		}
		out = append(out, part)
	}
	return out, nil
}

func parsePart(raw json.RawMessage) (aipapi.Part, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return aipapi.Part{}, err
	}
	if t, ok := keys["text"]; ok {
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return aipapi.Part{}, err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return aipapi.Part{}, errors.New("text part requires non-empty text")
		}
		return aipapi.TextPart(s), nil
	}
	// functionResponse: tool result sent back by the client in a "user" turn.
	if fr, ok := keys["functionResponse"]; ok {
		return parseFunctionResponsePart(fr)
	}
	if fr, ok := keys["function_response"]; ok {
		return parseFunctionResponsePart(fr)
	}
	// functionCall: model-generated tool call in an "model" turn (multi-turn history).
	if fc, ok := keys["functionCall"]; ok {
		return parseFunctionCallPart(fc)
	}
	if fc, ok := keys["function_call"]; ok {
		return parseFunctionCallPart(fc)
	}
	var inlineRaw json.RawMessage
	if v, ok := keys["inlineData"]; ok {
		inlineRaw = v
	} else if v, ok := keys["inline_data"]; ok {
		inlineRaw = v
	}
	if len(inlineRaw) > 0 {
		return parseInlineDataPart(inlineRaw)
	}
	return aipapi.Part{}, errors.New("unsupported part (need text or inlineData)")
}

func parseFunctionCallPart(raw json.RawMessage) (aipapi.Part, error) {
	var w struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return aipapi.Part{}, err
	}
	if strings.TrimSpace(w.Name) == "" {
		return aipapi.Part{}, errors.New("functionCall requires name")
	}
	args := w.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if err := frontendlimits.Bytes("functionCall.args", len(args), frontendlimits.MaxRawJSONPayload); err != nil {
		return aipapi.Part{}, err
	}
	return aipapi.Part{
		Kind:     aipapi.PartJSON,
		ToolName: w.Name,
		Content:  args,
	}, nil
}

func parseFunctionResponsePart(raw json.RawMessage) (aipapi.Part, error) {
	var w struct {
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return aipapi.Part{}, err
	}
	if strings.TrimSpace(w.Name) == "" {
		return aipapi.Part{}, errors.New("functionResponse requires name")
	}
	resp := w.Response
	if len(resp) == 0 {
		resp = json.RawMessage(`{}`)
	}
	if err := frontendlimits.Bytes("functionResponse.response", len(resp), frontendlimits.MaxRawJSONPayload); err != nil {
		return aipapi.Part{}, err
	}
	// Flatten response to text for canonical PartToolResult.
	// Store the raw JSON as text so downstream can use it.
	return aipapi.Part{
		Kind:       aipapi.PartToolResult,
		ToolCallID: w.Name, // Gemini uses name as the correlation key
		ToolName:   w.Name,
		Text:       string(resp),
	}, nil
}

func parseInlineDataPart(raw json.RawMessage) (aipapi.Part, error) {
	var b wireBlob
	if err := json.Unmarshal(raw, &b); err != nil {
		return aipapi.Part{}, err
	}
	mime := strings.TrimSpace(b.MIMEType)
	if mime == "" {
		mime = strings.TrimSpace(b.MIMETypeSnake)
	}
	data := strings.TrimSpace(b.Data)
	if data == "" {
		return aipapi.Part{}, errors.New("inlineData requires data")
	}
	if err := frontendlimits.StringBytes("inlineData.data", data, frontendlimits.MaxBase64Data); err != nil {
		return aipapi.Part{}, err
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	ref := "data:" + mime + ";base64," + data
	if strings.HasPrefix(mime, "image/") {
		return aipapi.Part{Kind: aipapi.PartImageRef, ImageRef: ref, ImageMIME: mime}, nil
	}
	name := "attachment"
	if mime == "application/pdf" {
		name = "document.pdf"
	}
	return aipapi.FilePart(ref, mime, name), nil
}

func parseGenerationConfig(raw json.RawMessage) (aipapi.GenerationOptions, error) {
	var o aipapi.GenerationOptions
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return o, nil
	}
	var w struct {
		Temperature     *float64 `json:"temperature"`
		TopP            *float64 `json:"topP"`
		MaxOutputTokens *int     `json:"maxOutputTokens"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return o, fmt.Errorf("generationConfig: %w", err)
	}
	o.Temperature = w.Temperature
	o.TopP = w.TopP
	o.MaxOutputTokens = w.MaxOutputTokens
	return o, nil
}

func parseTools(raw json.RawMessage) ([]aipapi.ToolDef, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("tools: %w", err)
	}
	if err := frontendlimits.Count("tools", len(items), frontendlimits.MaxTools); err != nil {
		return nil, err
	}
	out := make([]aipapi.ToolDef, 0, len(items))
	declarations := 0
	for i, it := range items {
		var w struct {
			FunctionDeclarations []json.RawMessage `json:"functionDeclarations"`
		}
		if err := json.Unmarshal(it, &w); err != nil {
			return nil, fmt.Errorf("tools[%d]: %w", i, err)
		}
		declarations += len(w.FunctionDeclarations)
		if err := frontendlimits.Count("functionDeclarations", declarations, frontendlimits.MaxTools); err != nil {
			return nil, err
		}
		for j, fd := range w.FunctionDeclarations {
			var fn struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			}
			if err := json.Unmarshal(fd, &fn); err != nil {
				return nil, fmt.Errorf("tools[%d].functionDeclarations[%d]: %w", i, j, err)
			}
			if strings.TrimSpace(fn.Name) == "" {
				return nil, fmt.Errorf("tools[%d].functionDeclarations[%d]: name is required", i, j)
			}
			params := fn.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{}`)
			}
			if err := frontendlimits.Bytes("functionDeclaration.parameters", len(params), frontendlimits.MaxToolSchema); err != nil {
				return nil, fmt.Errorf("tools[%d].functionDeclarations[%d]: %w", i, j, err)
			}
			out = append(out, aipapi.ToolDef{
				Name:        fn.Name,
				Description: fn.Description,
				Parameters:  params,
			})
		}
	}
	return out, nil
}

func parseToolConfig(raw json.RawMessage, toolCount int) (aipapi.ToolChoice, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		if toolCount == 0 {
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
		}
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
	}
	var w struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return aipapi.ToolChoice{}, fmt.Errorf("toolConfig: %w", err)
	}
	if w.FunctionCallingConfig == nil {
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
	}
	switch strings.TrimSpace(w.FunctionCallingConfig.Mode) {
	case "", "MODE_UNSPECIFIED", "AUTO":
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
	case "ANY":
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, nil
	case "NONE":
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, nil
	default:
		return aipapi.ToolChoice{}, fmt.Errorf("unsupported toolConfig.functionCallingConfig.mode %q", w.FunctionCallingConfig.Mode)
	}
}

// ModelFromCall returns the wire model string stored during decode.
func ModelFromCall(c *aipapi.Call) string {
	return openaiwire.ModelFromExtensions(c, extModelJSONKey)
}
