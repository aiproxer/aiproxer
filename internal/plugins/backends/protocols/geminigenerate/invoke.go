package geminigenerate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/safecast"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"google.golang.org/genai"
)

// Extension key for wire model stored by the Gemini frontend decoder.
const extModelJSONKey = "gemini.model"

// StreamParams holds inputs for [genai.Models.GenerateContentStream].
type StreamParams struct {
	Model    string
	Contents []*genai.Content
	Config   *genai.GenerateContentConfig
}

func newGenaiClient(ctx context.Context, cfg Config, apiKey string) (*genai.Client, error) {
	cc := genai.ClientConfig{APIKey: apiKey}
	if cfg.HTTPClient != nil {
		cc.HTTPClient = cfg.HTTPClient
	}
	if u := strings.TrimSpace(cfg.BaseURL); u != "" {
		cc.HTTPOptions.BaseURL = u
	}
	return genai.NewClient(ctx, &cc)
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

// StreamParamsForCall builds model, contents, and generate config from a canonical call.
// It runs [aipapi.Call.Validate] so numeric bounds (e.g. max_output_tokens for int32 fields) hold even when callers bypass the executor.
func StreamParamsForCall(call *aipapi.Call, cand routing.AttemptCandidate) (StreamParams, error) {
	if call == nil {
		return StreamParams{}, fmt.Errorf("gemini: nil call")
	}
	if err := call.Validate(); err != nil {
		return StreamParams{}, fmt.Errorf("gemini: validate call: %w", err)
	}
	model := resolveModel(cand, *call)
	if model == "" {
		return StreamParams{}, fmt.Errorf("gemini: model is required (route candidate or %s extension)", extModelJSONKey)
	}

	contents, err := buildContents(call)
	if err != nil {
		return StreamParams{}, fmt.Errorf("gemini: build contents: %w", err)
	}

	cfg := &genai.GenerateContentConfig{}
	if sys := buildSystemInstruction(call); sys != nil {
		cfg.SystemInstruction = sys
	}

	o := call.Options
	if o.Temperature != nil {
		t := float32(*o.Temperature)
		cfg.Temperature = &t
	}
	if o.TopP != nil {
		p := float32(*o.TopP)
		cfg.TopP = &p
	}
	if o.MaxOutputTokens != nil {
		cfg.MaxOutputTokens = safecast.Int32FromIntClamp(*o.MaxOutputTokens)
	}

	if len(call.Tools) > 0 {
		tools, err := buildTools(call.Tools)
		if err != nil {
			return StreamParams{}, fmt.Errorf("gemini: build tools: %w", err)
		}
		cfg.Tools = tools
		cfg.ToolConfig = toolConfigFromChoice(call.ToolChoice, len(call.Tools))
	}

	return StreamParams{Model: model, Contents: contents, Config: cfg}, nil
}

func buildSystemInstruction(call *aipapi.Call) *genai.Content {
	// Only top-level Instructions map to Gemini system instruction.
	// Mid-conversation system messages in call.Messages are not silently lifted;
	// they flow to buildContents and explicitly return unsupported role error.
	if len(call.Instructions) == 0 {
		return nil
	}
	t := aipapi.JoinInstructionText(call.Instructions)
	if t == "" {
		return nil
	}
	return &genai.Content{Parts: []*genai.Part{genai.NewPartFromText(t)}}
}

func buildContents(call *aipapi.Call) ([]*genai.Content, error) {
	out := make([]*genai.Content, 0, len(call.Messages))
	for _, m := range call.Messages {
		c, err := messageToContent(m)
		if err != nil {
			return nil, fmt.Errorf("gemini: message content: %w", err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("gemini: no contents")
	}
	return out, nil
}

func messageToContent(m aipapi.Message) (*genai.Content, error) {
	switch m.Role {
	case aipapi.RoleUser, aipapi.RoleDeveloper:
		// aipapi.RoleDeveloper carries proxy-owned continuation steering text
		// (internal/core/runtime/terminal_decision_continuation.go places it at
		// AfterIngressTail) and Gemini's Content.role is a closed user|model enum
		// with no developer value, so it is deliberately coerced onto the user
		// wire role: the provider reads proxy-owned instruction text as a client
		// utterance, and the canonical role survives only in the proxy's own
		// record, never on the provider wire. This is the lossy-but-modeled
		// downgrade; it is not hoisted into systemInstruction, because that is
		// the top-level instruction slot and hoisting a mid-conversation message
		// there would destroy the trajectory ordering the generic runtime depends
		// on.
		parts, err := userPartsToGenaiParts(m.Parts)
		if err != nil {
			return nil, fmt.Errorf("gemini: user parts: %w", err)
		}
		return genai.NewContentFromParts(parts, genai.RoleUser), nil
	case aipapi.RoleAssistant:
		parts, err := assistantPartsToGenaiParts(m.Parts)
		if err != nil {
			return nil, fmt.Errorf("gemini: assistant parts: %w", err)
		}
		return genai.NewContentFromParts(parts, genai.RoleModel), nil
	case aipapi.RoleTool:
		if len(m.Parts) != 1 || m.Parts[0].Kind != aipapi.PartToolResult {
			return nil, fmt.Errorf("gemini: tool message must have one tool_result part")
		}
		p := m.Parts[0]
		resp := map[string]any{}
		if len(p.Content) > 0 {
			if err := json.Unmarshal(p.Content, &resp); err != nil {
				return nil, fmt.Errorf("gemini: tool result JSON: %w", err)
			}
		}
		part := genai.NewPartFromFunctionResponse(p.ToolName, resp)
		part.FunctionResponse.ID = p.ToolCallID
		return genai.NewContentFromParts([]*genai.Part{part}, genai.RoleUser), nil
	default:
		return nil, fmt.Errorf("gemini: unsupported message role %q", m.Role)
	}
}

func userPartsToGenaiParts(parts []aipapi.Part) ([]*genai.Part, error) {
	out := make([]*genai.Part, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case aipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, genai.NewPartFromText(p.Text))
		case aipapi.PartImageRef:
			pt, err := imagePartFromCanonical(p)
			if err != nil {
				return nil, fmt.Errorf("gemini: image part: %w", err)
			}
			out = append(out, pt)
		case aipapi.PartFileRef:
			pt, err := filePartFromCanonical(p)
			if err != nil {
				return nil, fmt.Errorf("gemini: file part: %w", err)
			}
			out = append(out, pt)
		default:
			return nil, fmt.Errorf("gemini: unsupported part kind %q in user message", p.Kind)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("gemini: user message has no mappable parts")
	}
	return out, nil
}

func assistantPartsToGenaiParts(parts []aipapi.Part) ([]*genai.Part, error) {
	out := make([]*genai.Part, 0, len(parts))
	for _, p := range parts {
		if p.Kind == aipapi.PartReasoning {
			return nil, fmt.Errorf("gemini: reasoning replay unsupported (capability reasoning_replay)")
		}
		if p.Kind != aipapi.PartText {
			return nil, fmt.Errorf("gemini: assistant message may only contain text parts in this adapter")
		}
		if strings.TrimSpace(p.Text) == "" {
			continue
		}
		out = append(out, genai.NewPartFromText(p.Text))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("gemini: assistant message is empty after trimming")
	}
	return out, nil
}

func imagePartFromCanonical(p aipapi.Part) (*genai.Part, error) {
	ref := p.ImageRef
	if strings.HasPrefix(ref, "data:") {
		mime, b64, ok := aipapi.StripDataURLBase64(ref)
		if !ok {
			return nil, fmt.Errorf("gemini: invalid data URL in image part")
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("gemini: image base64: %w", err)
		}
		mt := pickImageMediaType(mime, p.ImageMIME)
		return genai.NewPartFromBytes(raw, mt), nil
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		mt := pickImageMediaType("", p.ImageMIME)
		if mt == "" {
			mt = "image/png"
		}
		return genai.NewPartFromURI(ref, mt), nil
	}
	return nil, fmt.Errorf("gemini: imageRef must be a data URL or http(s) URL, got %q", ref)
}

func filePartFromCanonical(p aipapi.Part) (*genai.Part, error) {
	ref := p.FileRef
	if !strings.HasPrefix(ref, "data:") {
		return nil, fmt.Errorf("gemini: file part requires a data URL, got %q", ref)
	}
	mime, b64, ok := aipapi.StripDataURLBase64(ref)
	if !ok {
		return nil, fmt.Errorf("gemini: invalid data URL in file part")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("gemini: file base64: %w", err)
	}
	if mime == "" {
		mime = p.FileMIME
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	return genai.NewPartFromBytes(raw, mime), nil
}

func pickImageMediaType(fromDataURL, fromPart string) string {
	if s := strings.TrimSpace(fromPart); s != "" {
		return s
	}
	return fromDataURL
}

func buildTools(tools []aipapi.ToolDef) ([]*genai.Tool, error) {
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		fd := &genai.FunctionDeclaration{Name: t.Name}
		if strings.TrimSpace(t.Description) != "" {
			fd.Description = t.Description
		}
		if len(t.Parameters) > 0 {
			var raw any
			if err := json.Unmarshal(t.Parameters, &raw); err != nil {
				return nil, fmt.Errorf("gemini: tool %q parameters: %w", t.Name, err)
			}
			fd.ParametersJsonSchema = raw
		}
		decls = append(decls, fd)
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}, nil
}

func toolConfigFromChoice(tc aipapi.ToolChoice, nTools int) *genai.ToolConfig {
	if nTools == 0 {
		return nil
	}
	mode := tc.Mode
	if mode == "" {
		mode = aipapi.ToolChoiceAuto
	}
	var m genai.FunctionCallingConfigMode
	switch mode {
	case aipapi.ToolChoiceNone:
		m = genai.FunctionCallingConfigModeNone
	case aipapi.ToolChoiceAny:
		m = genai.FunctionCallingConfigModeAny
	case aipapi.ToolChoiceRequired:
		m = genai.FunctionCallingConfigModeAny
		if tc.Name != "" {
			return &genai.ToolConfig{
				FunctionCallingConfig: &genai.FunctionCallingConfig{
					Mode:                 m,
					AllowedFunctionNames: []string{tc.Name},
				},
			}
		}
	case aipapi.ToolChoiceAuto:
		fallthrough
	default:
		m = genai.FunctionCallingConfigModeAuto
	}
	return &genai.ToolConfig{
		FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: m},
	}
}
