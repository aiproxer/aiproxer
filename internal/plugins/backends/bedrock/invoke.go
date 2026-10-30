package bedrock

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/safecast"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Extension key for wire model id stored by a frontend decoder.
const extModelJSONKey = "bedrock.modelId"

func newRuntimeClient(ctx context.Context, cfg Config) (*bedrockruntime.Client, error) {
	if err := validateBedrockEndpointInput(cfg); err != nil {
		return nil, fmt.Errorf("bedrock: validate endpoint: %w", err)
	}
	if ctx == nil {
		return nil, fmt.Errorf("bedrock: %w", aipapi.ErrNilContext)
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("bedrock: aws config: %w", err)
	}
	opts := []func(*bedrockruntime.Options){}
	if u := strings.TrimSpace(cfg.BaseEndpoint); u != "" {
		opts = append(opts, func(o *bedrockruntime.Options) {
			o.BaseEndpoint = aws.String(u)
			if cfg.DisableHTTPS {
				o.EndpointOptions.DisableHTTPS = true
				slog.Default().Warn("bedrock: TLS verification disabled for custom base endpoint", "base_endpoint", u)
			}
		})
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, func(o *bedrockruntime.Options) {
			o.HTTPClient = cfg.HTTPClient
		})
	}
	return bedrockruntime.NewFromConfig(awsCfg, opts...), nil
}

// validateBedrockEndpointInput is plain input validation for the adapter: a plaintext
// endpoint override is meaningless without a base_endpoint, so reject it explicitly
// instead of silently ignoring DisableHTTPS. The standard distribution's endpoint
// security policy (loopback-only plaintext) is enforced at registration by
// internal/standardplugins; the adapter itself accepts any syntactically valid
// endpoint so embedders can apply their own policy.
func validateBedrockEndpointInput(cfg Config) error {
	if !cfg.DisableHTTPS {
		return nil
	}
	if strings.TrimSpace(cfg.BaseEndpoint) == "" {
		return fmt.Errorf("bedrock: disable_https requires a non-empty base_endpoint")
	}
	return nil
}

func resolveModelID(cand routing.AttemptCandidate, call aipapi.Call) string {
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

// ConverseStreamInputForCall maps a canonical call to Bedrock ConverseStream input.
// It runs [aipapi.Call.Validate] so numeric bounds (e.g. max_output_tokens for int32 fields) hold even when callers bypass the executor.
func ConverseStreamInputForCall(call *aipapi.Call, cand routing.AttemptCandidate) (*bedrockruntime.ConverseStreamInput, error) {
	if call == nil {
		return nil, fmt.Errorf("bedrock: nil call")
	}
	if err := call.Validate(); err != nil {
		return nil, fmt.Errorf("bedrock: validate call: %w", err)
	}
	modelID := resolveModelID(cand, *call)
	if modelID == "" {
		return nil, fmt.Errorf("bedrock: model id is required (route candidate or %s extension)", extModelJSONKey)
	}

	sys, err := buildSystemBlocks(call)
	if err != nil {
		return nil, fmt.Errorf("bedrock: build system: %w", err)
	}
	msgs, err := buildMessages(call)
	if err != nil {
		return nil, fmt.Errorf("bedrock: build messages: %w", err)
	}

	in := &bedrockruntime.ConverseStreamInput{
		ModelId:  aws.String(modelID),
		Messages: msgs,
	}
	if len(sys) > 0 {
		in.System = sys
	}

	if o := call.Options; o.Temperature != nil || o.TopP != nil || o.MaxOutputTokens != nil {
		ic := &types.InferenceConfiguration{}
		if o.MaxOutputTokens != nil {
			ic.MaxTokens = aws.Int32(safecast.Int32FromIntClamp(*o.MaxOutputTokens))
		}
		if o.Temperature != nil {
			ic.Temperature = aws.Float32(float32(*o.Temperature))
		}
		if o.TopP != nil {
			ic.TopP = aws.Float32(float32(*o.TopP))
		}
		in.InferenceConfig = ic
	}

	if len(call.Tools) > 0 {
		tc, err := buildToolConfig(call)
		if err != nil {
			return nil, fmt.Errorf("bedrock: build tool config: %w", err)
		}
		in.ToolConfig = tc
	}

	return in, nil
}

func buildSystemBlocks(call *aipapi.Call) ([]types.SystemContentBlock, error) {
	out := make([]types.SystemContentBlock, 0, len(call.Messages)+1)
	if t := aipapi.JoinInstructionText(call.Instructions); t != "" {
		out = append(out, &types.SystemContentBlockMemberText{Value: t})
	}
	for _, m := range call.Messages {
		if m.Role != aipapi.RoleSystem {
			continue
		}
		for _, p := range m.Parts {
			if p.Kind != aipapi.PartText || strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, &types.SystemContentBlockMemberText{Value: p.Text})
		}
	}
	return out, nil
}

func buildMessages(call *aipapi.Call) ([]types.Message, error) {
	out := make([]types.Message, 0, len(call.Messages))
	for _, m := range call.Messages {
		if m.Role == aipapi.RoleSystem {
			continue
		}
		msg, err := aipMessageToBedrock(m)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bedrock: no non-system messages")
	}
	return out, nil
}

func aipMessageToBedrock(m aipapi.Message) (types.Message, error) {
	switch m.Role {
	case aipapi.RoleUser:
		blocks, err := userPartsToContentBlocks(m.Parts)
		if err != nil {
			return types.Message{}, err
		}
		return types.Message{
			Role:    types.ConversationRoleUser,
			Content: blocks,
		}, nil
	case aipapi.RoleAssistant:
		blocks, err := assistantPartsToContentBlocks(m.Parts)
		if err != nil {
			return types.Message{}, err
		}
		return types.Message{
			Role:    types.ConversationRoleAssistant,
			Content: blocks,
		}, nil
	case aipapi.RoleTool:
		if len(m.Parts) != 1 || m.Parts[0].Kind != aipapi.PartToolResult {
			return types.Message{}, fmt.Errorf("bedrock: tool message must have one tool_result part")
		}
		p := m.Parts[0]
		tb := types.ToolResultBlock{
			ToolUseId: aws.String(p.ToolCallID),
			Content: []types.ToolResultContentBlock{
				&types.ToolResultContentBlockMemberText{Value: string(p.Content)},
			},
			Status: types.ToolResultStatusSuccess,
		}
		return types.Message{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberToolResult{Value: tb},
			},
		}, nil
	default:
		return types.Message{}, fmt.Errorf("bedrock: unsupported message role %q", m.Role)
	}
}

func assistantPartsToContentBlocks(parts []aipapi.Part) ([]types.ContentBlock, error) {
	out := make([]types.ContentBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case aipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, &types.ContentBlockMemberText{Value: p.Text})
		case aipapi.PartJSON:
			var payload map[string]any
			if err := json.Unmarshal(p.Content, &payload); err != nil {
				return nil, fmt.Errorf("bedrock: assistant json part: %w", err)
			}
			toolUseID, okID := payload["tool_use_id"].(string)
			toolName, okName := payload["name"].(string)
			if !okID || !okName || toolUseID == "" || toolName == "" {
				return nil, fmt.Errorf("bedrock: assistant json part requires tool_use_id and name")
			}
			input := payload["input"]
			if input == nil {
				input = map[string]any{}
			}
			out = append(out, &types.ContentBlockMemberToolUse{
				Value: types.ToolUseBlock{
					ToolUseId: aws.String(toolUseID),
					Name:      aws.String(toolName),
					Input:     bedrockdoc.NewLazyDocument(input),
				},
			})
		default:
			return nil, fmt.Errorf("bedrock: unsupported assistant part kind %q", p.Kind)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bedrock: assistant message is empty after mapping")
	}
	return out, nil
}

func userPartsToContentBlocks(parts []aipapi.Part) ([]types.ContentBlock, error) {
	out := make([]types.ContentBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Kind {
		case aipapi.PartText:
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			out = append(out, &types.ContentBlockMemberText{Value: p.Text})
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
			return nil, fmt.Errorf("bedrock: unsupported part kind %q in user message", p.Kind)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bedrock: user message has no mappable content blocks")
	}
	return out, nil
}

func imageBlockFromPart(p aipapi.Part) (types.ContentBlock, error) {
	ref := p.ImageRef
	if strings.HasPrefix(ref, "data:") {
		mime, b64, ok := aipapi.StripDataURLBase64(ref)
		if !ok {
			return nil, fmt.Errorf("bedrock: invalid data URL in image part")
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("bedrock: image base64: %w", err)
		}
		fmt := imageFormatFromMIME(pickImageMediaType(mime, p.ImageMIME))
		return &types.ContentBlockMemberImage{
			Value: types.ImageBlock{
				Format: fmt,
				Source: &types.ImageSourceMemberBytes{Value: raw},
			},
		}, nil
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return nil, fmt.Errorf("bedrock: image URL sources are not supported for Converse in this adapter; use a data URL")
	}
	return nil, fmt.Errorf("bedrock: imageRef must be a data URL, got %q", ref)
}

func documentBlockFromPart(p aipapi.Part) (types.ContentBlock, error) {
	ref := p.FileRef
	if !strings.HasPrefix(ref, "data:") {
		return nil, fmt.Errorf("bedrock: file part requires a data URL, got %q", ref)
	}
	mime, b64, ok := aipapi.StripDataURLBase64(ref)
	if !ok {
		return nil, fmt.Errorf("bedrock: invalid data URL in file part")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("bedrock: document base64: %w", err)
	}
	_ = mime
	name := strings.TrimSpace(p.FileName)
	if name == "" {
		name = "document.pdf"
	}
	return &types.ContentBlockMemberDocument{
		Value: types.DocumentBlock{
			Name:   aws.String(name),
			Format: types.DocumentFormatPdf,
			Source: &types.DocumentSourceMemberBytes{Value: raw},
		},
	}, nil
}

func pickImageMediaType(fromDataURL, fromPart string) string {
	if s := strings.TrimSpace(fromPart); s != "" {
		return s
	}
	return fromDataURL
}

func imageFormatFromMIME(mime string) types.ImageFormat {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return types.ImageFormatJpeg
	case "image/webp":
		return types.ImageFormatWebp
	case "image/gif":
		return types.ImageFormatGif
	default:
		return types.ImageFormatPng
	}
}

func buildToolConfig(call *aipapi.Call) (*types.ToolConfiguration, error) {
	tools := make([]types.Tool, 0, len(call.Tools))
	for _, t := range call.Tools {
		var schema any
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return nil, fmt.Errorf("bedrock: tool %q parameters: %w", t.Name, err)
			}
		} else {
			schema = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		spec := types.ToolSpecification{
			Name: aws.String(t.Name),
			InputSchema: &types.ToolInputSchemaMemberJson{
				Value: bedrockdoc.NewLazyDocument(schema),
			},
		}
		if strings.TrimSpace(t.Description) != "" {
			spec.Description = aws.String(t.Description)
		}
		tools = append(tools, &types.ToolMemberToolSpec{Value: spec})
	}
	cfg := &types.ToolConfiguration{Tools: tools}
	mode := call.ToolChoice.Mode
	if mode == "" {
		mode = aipapi.ToolChoiceAuto
	}
	switch mode {
	case aipapi.ToolChoiceAuto:
		cfg.ToolChoice = &types.ToolChoiceMemberAuto{Value: types.AutoToolChoice{}}
	case aipapi.ToolChoiceAny:
		cfg.ToolChoice = &types.ToolChoiceMemberAny{Value: types.AnyToolChoice{}}
	case aipapi.ToolChoiceRequired:
		cfg.ToolChoice = &types.ToolChoiceMemberTool{
			Value: types.SpecificToolChoice{Name: aws.String(call.ToolChoice.Name)},
		}
	case aipapi.ToolChoiceNone:
		// Call.Validate rejects tools+none; if we ever get here, omit tool config.
		return nil, fmt.Errorf("bedrock: ToolChoiceNone with tools is invalid")
	default:
		cfg.ToolChoice = &types.ToolChoiceMemberAuto{Value: types.AutoToolChoice{}}
	}
	return cfg, nil
}

// Config configures the Bedrock Runtime ConverseStream connector (AWS SDK v2).
type Config struct {
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	// BaseEndpoint is optional (e.g. httptest server URL for integration tests).
	BaseEndpoint string
	// DisableHTTPS must be true when BaseEndpoint is http:// (emulator).
	DisableHTTPS bool
	HTTPClient   *http.Client
}
