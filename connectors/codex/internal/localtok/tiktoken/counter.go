package tiktoken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	countapp "github.com/aiproxer/aiproxer/connectors/codex/internal/localtok/countapp"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/localtok/imageestimator"
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

// Counter implements countapp.LocalCounter with local tiktoken encodings.
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
		return nil, fmt.Errorf("%w: unsupported default tiktoken encoding %q", countapp.ErrLocalUnavailable, cfg.DefaultEncoding)
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
			return nil, fmt.Errorf("%w: unsupported tiktoken encoding %q for model %q", countapp.ErrLocalUnavailable, encoding, model)
		}
		mappings[strings.ToLower(strings.TrimSpace(model))] = mapped
	}
	return &Counter{defaultEncoding: resolved, modelMappings: mappings, imageEstimator: imageEstimator, codecs: make(map[string]tiktokenlib.Codec)}, nil
}

func (c *Counter) CountText(ctx context.Context, input countapp.CountTextInput) (countapp.CountResult, error) {
	if err := ctx.Err(); err != nil {
		return countapp.CountResult{}, err
	}
	return c.count(ctx, input.Model, input.Text, true)
}

func (c *Counter) CountOutput(ctx context.Context, input countapp.CountOutputInput) (countapp.CountResult, error) {
	if err := ctx.Err(); err != nil {
		return countapp.CountResult{}, err
	}
	return c.count(ctx, input.Model, input.Text, false)
}

func (c *Counter) CountCall(ctx context.Context, input countapp.CountCallInput) (countapp.CountResult, error) {
	if err := ctx.Err(); err != nil {
		return countapp.CountResult{}, err
	}
	encoding, fallback, err := c.resolveEncoding(input.Model)
	if err != nil {
		return countapp.CountResult{}, err
	}
	codec, err := c.codec(encoding)
	if err != nil {
		return countapp.CountResult{}, err
	}
	tokens, err := countCallTokens(ctx, codec, c.imageEstimator, input.Call)
	if err != nil {
		return countapp.CountResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return countapp.CountResult{}, err
	}

	result := countapp.CountResult{
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

func (c *Counter) count(ctx context.Context, model, text string, input bool) (countapp.CountResult, error) {
	encoding, fallback, err := c.resolveEncoding(model)
	if err != nil {
		return countapp.CountResult{}, err
	}
	codec, err := c.codec(encoding)
	if err != nil {
		return countapp.CountResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return countapp.CountResult{}, err
	}
	tokens, err := codec.Count(text)
	if err != nil {
		return countapp.CountResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return countapp.CountResult{}, err
	}

	result := countapp.CountResult{
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
	// OpenAI-compatible chat framing is an estimator: per-message overhead plus assistant reply priming.
	tokens := chatReplyPriming
	for i, message := range call.Instructions {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		messageTokens, err := countMessageTokens(ctx, codec, imageEstimator, message, fmt.Sprintf("Instructions[%d]", i))
		if err != nil {
			return 0, err
		}
		tokens += messageTokens
	}
	for i, message := range call.Messages {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		messageTokens, err := countMessageTokens(ctx, codec, imageEstimator, message, fmt.Sprintf("Messages[%d]", i))
		if err != nil {
			return 0, err
		}
		tokens += messageTokens
	}
	for i, tool := range call.Tools {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		formatted, err := formatToolDef(tool)
		if err != nil {
			return 0, fmt.Errorf("%w: Tools[%d] %v", countapp.ErrLocalUnavailable, i, err)
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

func countMessageTokens(ctx context.Context, codec tiktokenlib.Codec, imageEstimator imageestimator.Estimator, message aipapi.Message, path string) (int, error) {
	tokens := chatTokensPerMessage
	// Canonical roles are counted explicitly as a stable estimator choice, not as provider-exact framing.
	roleTokens, err := countText(codec, string(message.Role))
	if err != nil {
		return 0, err
	}
	tokens += roleTokens
	for i, part := range message.Parts {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		partTokens, err := countPartTokens(codec, imageEstimator, part, fmt.Sprintf("%s.Parts[%d]", path, i))
		if err != nil {
			return 0, err
		}
		tokens += partTokens
	}
	return tokens, nil
}

func countPartTokens(codec tiktokenlib.Codec, imageEstimator imageestimator.Estimator, part aipapi.Part, path string) (int, error) {
	switch part.Kind {
	case aipapi.PartText:
		return countText(codec, part.Text)
	case aipapi.PartJSON:
		text, err := canonicalJSON(part.Content)
		if err != nil {
			return 0, fmt.Errorf("%w: %s contains invalid json part content: %v", countapp.ErrLocalUnavailable, path, err)
		}
		return countText(codec, text)
	case aipapi.PartImageRef:
		tokens, err := imageEstimator.Count(imageestimator.Input{Ref: part.ImageRef, Detail: imageDetail(part.Content)})
		if err != nil {
			return 0, fmt.Errorf("%w: %s image estimate unavailable: %v", countapp.ErrLocalUnavailable, path, err)
		}
		return tokens, nil
	case aipapi.PartReasoning:
		return countReasoningPartTokens(codec, part, path)
	case aipapi.PartFileRef, aipapi.PartToolResult:
		return 0, fmt.Errorf("%w: %s contains unsupported %s part for local call counting", countapp.ErrLocalUnavailable, path, part.Kind)
	case "":
		return 0, fmt.Errorf("%w: %s has empty part kind", countapp.ErrLocalUnavailable, path)
	default:
		return 0, fmt.Errorf("%w: %s contains unsupported %s part for local call counting", countapp.ErrLocalUnavailable, path, part.Kind)
	}
}

// countReasoningPartTokens includes text and signature via the text encoder and opaque JSON
// as raw UTF-8 bytes through the same encoder (opaque is not treated as natural language).
func countReasoningPartTokens(codec tiktokenlib.Codec, part aipapi.Part, path string) (int, error) {
	if part.Reasoning == nil {
		return 0, fmt.Errorf("%w: %s reasoning part requires Reasoning payload", countapp.ErrLocalUnavailable, path)
	}
	tokens := 0
	n, err := countText(codec, part.Reasoning.Text)
	if err != nil {
		return 0, err
	}
	tokens += n
	n, err = countText(codec, part.Reasoning.Signature)
	if err != nil {
		return 0, err
	}
	tokens += n
	if len(part.Reasoning.Opaque) > 0 {
		// Byte-faithful tokenization of opaque JSON bytes; do not canonical-rewrite provider blobs.
		n, err = countText(codec, string(part.Reasoning.Opaque))
		if err != nil {
			return 0, err
		}
		tokens += n
	}
	return tokens, nil
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

func (c *Counter) resolveEncoding(modelOrEncoding string) (string, *countapp.Fallback, error) {
	modelOrEncoding = strings.TrimSpace(modelOrEncoding)
	if encoding, ok := normalizeEncoding(modelOrEncoding); ok {
		return encoding, nil, nil
	}
	if encoding, ok := c.modelMappings[strings.ToLower(modelOrEncoding)]; ok {
		return encoding, nil, nil
	}
	if looksLikeExplicitEncoding(modelOrEncoding) {
		return "", nil, fmt.Errorf("%w: unsupported tiktoken encoding %q", countapp.ErrLocalUnavailable, modelOrEncoding)
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
	return c.defaultEncoding, &countapp.Fallback{Reason: countapp.FallbackReasonLocalDefaultEncoding, Message: message}, nil
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
			return nil, fmt.Errorf("%w: unsupported tiktoken encoding %q", countapp.ErrLocalUnavailable, encoding)
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
