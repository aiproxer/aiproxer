package tiktoken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/infra/tokenizers/imageestimator"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	tiktokenlib "github.com/tiktoken-go/tokenizer"
)

const (
	encodingCL100KBase = "cl100k_base"
	encodingO200KBase  = "o200k_base"
	tokenizerType      = "tiktoken"
	tokenizerSource    = "github.com/tiktoken-go/tokenizer"

	chatTokensPerMessage = 3
	chatReplyPriming     = 3
)

// Config controls local tiktoken counting. Empty DefaultEncoding uses cl100k_base.
type Config struct {
	DefaultEncoding string
	ModelMappings   map[string]string
	Image           ImageConfig
}

type ImageConfig struct {
	BaseTokens       int
	MaxDecodedBytes  int
	UseDefaultTokens bool
	DefaultTokens    int
}

// Counter implements app.LocalCounter with local tiktoken encodings.
type Counter struct {
	defaultEncoding string
	modelMappings   map[string]string
	imageEstimator  imageestimator.Estimator
	mu              sync.Mutex
	codecs          map[string]tiktokenlib.Codec
}

func NewCounter(cfg Config) (*Counter, error) {
	defaultEncoding := cfg.DefaultEncoding
	if defaultEncoding == "" {
		defaultEncoding = encodingCL100KBase
	}
	resolved, ok := normalizeEncoding(defaultEncoding)
	if !ok {
		return nil, fmt.Errorf("%w: unsupported default tiktoken encoding %q", app.ErrLocalUnavailable, cfg.DefaultEncoding)
	}
	imageEstimator := imageestimator.New(imageestimator.Config{
		BaseTokens:       cfg.Image.BaseTokens,
		MaxDecodedBytes:  cfg.Image.MaxDecodedBytes,
		UseDefaultTokens: cfg.Image.UseDefaultTokens,
		DefaultTokens:    cfg.Image.DefaultTokens,
	})
	mappings := make(map[string]string, len(cfg.ModelMappings))
	for model, encoding := range cfg.ModelMappings {
		mapped, ok := normalizeEncoding(encoding)
		if !ok {
			return nil, fmt.Errorf("%w: unsupported tiktoken encoding %q for model %q", app.ErrLocalUnavailable, encoding, model)
		}
		mappings[strings.ToLower(strings.TrimSpace(model))] = mapped
	}
	return &Counter{defaultEncoding: resolved, modelMappings: mappings, imageEstimator: imageEstimator, codecs: make(map[string]tiktokenlib.Codec)}, nil
}

func (c *Counter) CountText(ctx context.Context, input app.CountTextInput) (app.CountResult, error) {
	if err := ctx.Err(); err != nil {
		return app.CountResult{}, err
	}
	return c.count(ctx, input.Model, input.Text, true)
}

func (c *Counter) CountOutput(ctx context.Context, input app.CountOutputInput) (app.CountResult, error) {
	if err := ctx.Err(); err != nil {
		return app.CountResult{}, err
	}
	return c.count(ctx, input.Model, input.Text, false)
}

func (c *Counter) CountCall(ctx context.Context, input app.CountCallInput) (app.CountResult, error) {
	if err := ctx.Err(); err != nil {
		return app.CountResult{}, err
	}
	encoding, fallback, err := c.resolveEncoding(input.Model)
	if err != nil {
		return app.CountResult{}, err
	}
	codec, err := c.codec(encoding)
	if err != nil {
		return app.CountResult{}, err
	}
	tokens, err := countCallTokens(ctx, codec, c.imageEstimator, input.Call)
	if err != nil {
		return app.CountResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return app.CountResult{}, err
	}

	result := app.CountResult{
		InputTokens:        tokens,
		TotalTokens:        tokens,
		TotalTokensPresent: true,
		Accounting: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceLocalTokenizer,
			Authority: aipapi.UsageAuthorityEstimated,
			Tokenizer: aipapi.TokenizerRef{
				Type:      tokenizerType,
				ID:        encoding,
				Source:    tokenizerSource,
				ModelUsed: input.Model,
			},
		},
	}
	if fallback != nil {
		result.Fallbacks = append(result.Fallbacks, *fallback)
	}
	return result, nil
}

func (c *Counter) count(ctx context.Context, model, text string, input bool) (app.CountResult, error) {
	encoding, fallback, err := c.resolveEncoding(model)
	if err != nil {
		return app.CountResult{}, err
	}
	codec, err := c.codec(encoding)
	if err != nil {
		return app.CountResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return app.CountResult{}, err
	}
	tokens, err := codec.Count(text)
	if err != nil {
		return app.CountResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return app.CountResult{}, err
	}

	result := app.CountResult{
		TotalTokens:        tokens,
		TotalTokensPresent: true,
		Accounting: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceLocalTokenizer,
			Authority: aipapi.UsageAuthorityEstimated,
			Tokenizer: aipapi.TokenizerRef{
				Type:      tokenizerType,
				ID:        encoding,
				Source:    tokenizerSource,
				ModelUsed: model,
			},
		},
	}
	if input {
		result.InputTokens = tokens
	} else {
		result.OutputTokens = tokens
	}
	if fallback != nil {
		result.Fallbacks = append(result.Fallbacks, *fallback)
	}
	return result, nil
}

func countCallTokens(ctx context.Context, codec tiktokenlib.Codec, imageEstimator imageestimator.Estimator, call aipapi.Call) (int, error) {
	// OpenAI-compatible chat framing is an estimator: per-item overhead plus assistant reply priming.
	tokens := chatReplyPriming
	items := aipapi.NormalizedItems(call)
	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		path := fmt.Sprintf("Items[%d]", i)
		itemTokens, err := countItemTokens(ctx, codec, imageEstimator, item, path)
		if err != nil {
			return 0, err
		}
		tokens += itemTokens
	}
	for i, tool := range call.Tools {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		formatted, err := formatToolDef(tool)
		if err != nil {
			return 0, fmt.Errorf("%w: Tools[%d] %v", app.ErrLocalUnavailable, i, err)
		}
		toolTokens, err := countText(codec, formatted)
		if err != nil {
			return 0, err
		}
		tokens += toolTokens
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if choice := formatToolChoice(call.ToolChoice); choice != "" {
		choiceTokens, err := countText(codec, choice)
		if err != nil {
			return 0, err
		}
		tokens += choiceTokens
	}
	return tokens, nil
}

func countItemTokens(ctx context.Context, codec tiktokenlib.Codec, imageEstimator imageestimator.Estimator, item aipapi.Item, path string) (int, error) {
	tokens := chatTokensPerMessage
	if item.Role != "" {
		roleTokens, err := countText(codec, string(item.Role))
		if err != nil {
			return 0, err
		}
		tokens += roleTokens
	}
	for j, cp := range item.Content {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		cpTokens, err := countContentPartTokens(codec, imageEstimator, cp, fmt.Sprintf("%s.Content[%d]", path, j))
		if err != nil {
			return 0, err
		}
		tokens += cpTokens
	}
	if item.ToolCall != nil {
		if item.ToolCall.Name != "" {
			n, err := countText(codec, item.ToolCall.Name)
			if err != nil {
				return 0, err
			}
			tokens += n
		}
		if len(item.ToolCall.Arguments) > 0 {
			n, err := countText(codec, string(item.ToolCall.Arguments))
			if err != nil {
				return 0, err
			}
			tokens += n
		}
	}
	if item.ToolResult != nil {
		if item.ToolResult.Output != "" {
			n, err := countText(codec, item.ToolResult.Output)
			if err != nil {
				return 0, err
			}
			tokens += n
		}
		for j, cp := range item.ToolResult.Parts {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			cpTokens, err := countContentPartTokens(codec, imageEstimator, cp, fmt.Sprintf("%s.ToolResult.Parts[%d]", path, j))
			if err != nil {
				return 0, err
			}
			tokens += cpTokens
		}
	}
	if item.Reasoning != nil && item.Reasoning.Reasoning != nil {
		r := item.Reasoning.Reasoning
		if r.Text != "" {
			n, err := countText(codec, r.Text)
			if err != nil {
				return 0, err
			}
			tokens += n
		}
		if r.Signature != "" {
			n, err := countText(codec, r.Signature)
			if err != nil {
				return 0, err
			}
			tokens += n
		}
		if len(r.Opaque) > 0 {
			n, err := countText(codec, string(r.Opaque))
			if err != nil {
				return 0, err
			}
			tokens += n
		}
	}
	if item.Compaction != nil && len(item.Compaction.Opaque) > 0 {
		n, err := countText(codec, string(item.Compaction.Opaque))
		if err != nil {
			return 0, err
		}
		tokens += n
	}
	return tokens, nil
}

func countContentPartTokens(codec tiktokenlib.Codec, imageEstimator imageestimator.Estimator, cp aipapi.ContentPart, path string) (int, error) {
	switch cp.Kind {
	case aipapi.ContentPartText:
		return countText(codec, cp.Text)
	case aipapi.ContentPartJSON:
		var raw json.RawMessage
		if cp.Annotation != nil && len(cp.Annotation.Data) > 0 {
			raw = cp.Annotation.Data
		} else {
			raw = json.RawMessage(cp.Text)
		}
		text, err := canonicalJSON(raw)
		if err != nil {
			return 0, fmt.Errorf("%w: %s contains invalid json part content: %v", app.ErrLocalUnavailable, path, err)
		}
		return countText(codec, text)
	case aipapi.ContentPartImageRef:
		var detail string
		if cp.Annotation != nil && len(cp.Annotation.Data) > 0 {
			detail = imageDetail(cp.Annotation.Data)
		}
		tokens, err := imageEstimator.Count(imageestimator.Input{Ref: cp.ImageRef, Detail: detail})
		if err != nil {
			return 0, fmt.Errorf("%w: %s image estimate unavailable: %v", app.ErrLocalUnavailable, path, err)
		}
		return tokens, nil
	case aipapi.ContentPartRefusal:
		return countText(codec, cp.Refusal)
	case aipapi.ContentPartSummary:
		return countText(codec, cp.Summary)
	case aipapi.ContentPartReasoning:
		if cp.Reasoning == nil {
			return 0, fmt.Errorf("%w: %s reasoning part requires Reasoning payload", app.ErrLocalUnavailable, path)
		}
		tokens := 0
		if cp.Reasoning.Text != "" {
			n, err := countText(codec, cp.Reasoning.Text)
			if err != nil {
				return 0, err
			}
			tokens += n
		}
		if cp.Reasoning.Signature != "" {
			n, err := countText(codec, cp.Reasoning.Signature)
			if err != nil {
				return 0, err
			}
			tokens += n
		}
		if len(cp.Reasoning.Opaque) > 0 {
			n, err := countText(codec, string(cp.Reasoning.Opaque))
			if err != nil {
				return 0, err
			}
			tokens += n
		}
		return tokens, nil
	case aipapi.ContentPartFileRef, aipapi.ContentPartVideoRef, aipapi.ContentPartToolResult:
		return 0, fmt.Errorf("%w: %s contains unsupported %s part for local call counting", app.ErrLocalUnavailable, path, cp.Kind)
	case "":
		return 0, fmt.Errorf("%w: %s has empty part kind", app.ErrLocalUnavailable, path)
	default:
		return 0, fmt.Errorf("%w: %s contains unsupported %s part for local call counting", app.ErrLocalUnavailable, path, cp.Kind)
	}
}

func imageDetail(raw json.RawMessage) string {
	if len(raw) == 0 || !json.Valid(raw) {
		return ""
	}
	var payload struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return payload.Detail
}

func countText(codec tiktokenlib.Codec, text string) (int, error) {
	tokens, err := codec.Count(text)
	if err != nil {
		return 0, err
	}
	return tokens, nil
}

func formatToolDef(tool aipapi.ToolDef) (string, error) {
	var b strings.Builder
	// This pseudo-namespace is deterministic, but keeps description as a trailing comment for compact estimates.
	b.WriteString("namespace functions {\n")
	b.WriteString("type ")
	b.WriteString(tool.Name)
	b.WriteString(" = (_: ")
	if len(tool.Parameters) == 0 {
		b.WriteString("{}")
	} else {
		parameters, err := canonicalJSON(tool.Parameters)
		if err != nil {
			return "", fmt.Errorf("invalid tool parameters json: %w", err)
		}
		b.WriteString(parameters)
	}
	b.WriteString(") => any")
	if tool.Description != "" {
		b.WriteString(" // ")
		b.WriteString(tool.Description)
	}
	b.WriteString("\n}\n")
	return b.String(), nil
}

func formatToolChoice(choice aipapi.ToolChoice) string {
	switch choice.Mode {
	case aipapi.ToolChoiceNone:
		return "tool_choice:none"
	case aipapi.ToolChoiceAny:
		return "tool_choice:any"
	case aipapi.ToolChoiceRequired:
		if choice.Name != "" {
			return "tool_choice:required:" + choice.Name
		}
		return "tool_choice:required"
	case aipapi.ToolChoiceAuto, "":
		if choice.Name != "" {
			return "tool_choice:auto:" + choice.Name
		}
		return ""
	default:
		return "tool_choice:" + string(choice.Mode)
	}
}

func canonicalJSON(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	var b strings.Builder
	writeCanonicalJSON(&b, value)
	return b.String(), nil
}

func writeCanonicalJSON(b *strings.Builder, value any) {
	switch v := value.(type) {
	case map[string]any:
		b.WriteByte('{')
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for i, key := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, key)
			b.WriteByte(':')
			writeCanonicalJSON(b, v[key])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonicalJSON(b, item)
		}
		b.WriteByte(']')
	case string:
		writeJSONString(b, v)
	case float64, bool, nil:
		encoded, _ := json.Marshal(v)
		b.Write(encoded)
	default:
		encoded, _ := json.Marshal(v)
		b.Write(encoded)
	}
}

func writeJSONString(b *strings.Builder, value string) {
	encoded, _ := json.Marshal(value)
	b.Write(encoded)
}

func (c *Counter) resolveEncoding(modelOrEncoding string) (string, *app.Fallback, error) {
	modelOrEncoding = strings.TrimSpace(modelOrEncoding)
	if encoding, ok := normalizeEncoding(modelOrEncoding); ok {
		return encoding, nil, nil
	}
	if encoding, ok := c.modelMappings[strings.ToLower(modelOrEncoding)]; ok {
		return encoding, nil, nil
	}
	if looksLikeExplicitEncoding(modelOrEncoding) {
		return "", nil, fmt.Errorf("%w: unsupported tiktoken encoding %q", app.ErrLocalUnavailable, modelOrEncoding)
	}
	lower := strings.ToLower(modelOrEncoding)
	if strings.Contains(lower, "gpt-4o") {
		return encodingO200KBase, nil, nil
	}
	if isCommonGPTModel(lower) {
		return encodingCL100KBase, nil, nil
	}
	message := "unknown model; using default tiktoken encoding"
	if modelOrEncoding == "" {
		message = "empty model; using default tiktoken encoding"
	}
	return c.defaultEncoding, &app.Fallback{Reason: app.FallbackReasonLocalDefaultEncoding, Message: message}, nil
}

func (c *Counter) codec(encoding string) (tiktokenlib.Codec, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if codec, ok := c.codecs[encoding]; ok {
		return codec, nil
	}
	codec, err := tiktokenlib.Get(tiktokenlib.Encoding(encoding))
	if err != nil {
		if errors.Is(err, tiktokenlib.ErrEncodingNotSupported) {
			return nil, fmt.Errorf("%w: unsupported tiktoken encoding %q", app.ErrLocalUnavailable, encoding)
		}
		return nil, err
	}
	c.codecs[encoding] = codec
	return codec, nil
}

func normalizeEncoding(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case encodingCL100KBase, "openai:" + encodingCL100KBase:
		return encodingCL100KBase, true
	case encodingO200KBase, "openai:" + encodingO200KBase:
		return encodingO200KBase, true
	default:
		return "", false
	}
}

func looksLikeExplicitEncoding(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "openai:") || strings.HasSuffix(value, "_base")
}

func isCommonGPTModel(model string) bool {
	return strings.Contains(model, "gpt-4") || strings.Contains(model, "gpt-3.5") || strings.Contains(model, "chatgpt")
}
