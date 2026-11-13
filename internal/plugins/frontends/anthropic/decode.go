package anthropic

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

// Extension keys for round-trip metadata on the canonical call.
const (
	extModelJSONKey = "anthropic.model"
	// emptyContentBlocksMessage is the wire refusal text for a content array
	// that carries no blocks at all.
	emptyContentBlocksMessage = "anthropic: content blocks are empty"
	// messageOutputConfigKey is the per-message `output_config` key on an inline
	// system message. The request-level field is decoded separately: its `effort`
	// maps onto canonical generation options, and its other fields are reported
	// rather than silently dropped.
	messageOutputConfigKey = "output_config"
)

var (
	// errEmptyContentBlocks is the refusal for a content array with no blocks.
	errEmptyContentBlocks = errors.New(emptyContentBlocksMessage)
	// errEmptySystemCarrier carries the same wire text but a distinct identity,
	// so the decoder can apply the empty_system_messages policy without changing
	// the refusal text operators already know.
	errEmptySystemCarrier = errors.New(emptyContentBlocksMessage)
	// errPerMessageControls refuses an inline system message that declares a
	// per-message control this frontend cannot forward, under the reject policy.
	errPerMessageControls = errors.New("anthropic: inline system message declares controls this frontend cannot forward")
)

// DecodeOptions configures decoding of a Messages API request.
type DecodeOptions struct {
	// RouteSelector is required (e.g. "stub:claude-3-5-haiku"); usually from X-AIP-Route header.
	RouteSelector string
	// AnthropicVersion is the optional anthropic-version header; empty means default for decode.
	AnthropicVersion string
	Headers          http.Header
	// HTTPHeaders carries configured header aliases; zero value uses defaults.
	HTTPHeaders aipsdk.HTTPHeaders
	// EmptySystemMessages is the operator policy for an inline `role: system`
	// message whose content array is empty. Empty means [EmptySystemMessagesServe].
	EmptySystemMessages string
	// PerMessageControls is the operator policy for an inline `role: system`
	// message that declares a per-message control this frontend cannot forward.
	// Empty means [PerMessageControlsServe].
	PerMessageControls string
}

// DecodedMessage is the result of decoding POST /v1/messages JSON.
type DecodedMessage struct {
	Call   *aipapi.Call
	Stream bool
	Model  string
	// EmptySystemCarrier records how contentless inline system carriers were
	// handled, including every semantic the canonical call could not carry.
	EmptySystemCarrier EmptySystemCarrierReduction
	// RequestControls records request-level controls that could not be carried.
	RequestControls RequestControlReduction
	// ContentSystemControls records per-message controls dropped from served
	// content-bearing inline system messages.
	ContentSystemControls ContentSystemControlReduction
}

type wireCreate struct {
	Model        string            `json:"model"`
	MaxTokens    int               `json:"max_tokens"`
	Messages     []json.RawMessage `json:"messages"`
	Stream       bool              `json:"stream"`
	System       json.RawMessage   `json:"system"`
	Metadata     json.RawMessage   `json:"metadata"`
	Temperature  *float64          `json:"temperature"`
	TopP         *float64          `json:"top_p"`
	TopK         *int              `json:"top_k"`
	Tools        json.RawMessage   `json:"tools"`
	ToolChoice   json.RawMessage   `json:"tool_choice"`
	OutputConfig json.RawMessage   `json:"output_config"`
}

// DecodeMessageRequest maps Anthropic Messages JSON into a canonical call.
func DecodeMessageRequest(body []byte, opts DecodeOptions) (*DecodedMessage, error) {
	_ = opts.AnthropicVersion // optional header; wire compatibility (see package doc).
	sel := strings.TrimSpace(opts.RouteSelector)
	if sel == "" {
		return nil, errors.New("anthropic: route selector is required: send a model with a route prefix (for example \"backend:model\")")
	}
	var w wireCreate
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("anthropic: invalid json: %w", err)
	}
	model := strings.TrimSpace(w.Model)
	if model == "" {
		return nil, errors.New("anthropic: model is required")
	}
	if w.MaxTokens <= 0 {
		return nil, errors.New("anthropic: max_tokens is required and must be positive")
	}
	if len(w.Messages) == 0 {
		return nil, errors.New("anthropic: messages is required")
	}
	if err := frontendlimits.Count("messages", len(w.Messages), frontendlimits.MaxMessages); err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}

	instructions, err := parseSystem(w.System)
	if err != nil {
		return nil, fmt.Errorf("anthropic: system: %w", err)
	}
	emptyPolicy, err := normalizeEmptySystemMessagesPolicy(opts.EmptySystemMessages)
	if err != nil {
		return nil, err
	}
	perMessagePolicy, err := normalizePerMessageControlsPolicy(opts.PerMessageControls)
	if err != nil {
		return nil, err
	}
	msgs, reduction, contentControls, err := parseAnthropicMessages(w.Messages, emptyPolicy == EmptySystemMessagesServe, perMessagePolicy == PerMessageControlsReject)
	if err != nil {
		return nil, fmt.Errorf("anthropic: messages: %w", err)
	}
	tools, err := parseTools(w.Tools)
	if err != nil {
		return nil, fmt.Errorf("anthropic: tools: %w", err)
	}
	toolChoice, err := parseToolChoice(w.ToolChoice)
	if err != nil {
		return nil, fmt.Errorf("anthropic: tool_choice: %w", err)
	}

	modelRaw, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal model extension: %w", err)
	}
	ext := map[string]json.RawMessage{extModelJSONKey: modelRaw}

	maxOut := w.MaxTokens
	call := &aipapi.Call{
		Route:        aipapi.RouteIntent{Selector: sel},
		Instructions: instructions,
		Messages:     msgs,
		Tools:        tools,
		ToolChoice:   toolChoice,
		Extensions:   ext,
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationAnthropicMessages,
			DeliveryMode: aipapi.DeliveryModeFromClientStream(w.Stream),
		},
		Options: aipapi.GenerationOptions{
			Temperature:     w.Temperature,
			TopP:            w.TopP,
			MaxOutputTokens: &maxOut,
		},
	}
	requestControls := applyRequestOutputConfig(w.OutputConfig, &call.Options)
	if jsonpresence.IsPresentNonNullJSON(w.Metadata) {
		var meta map[string]string
		if jerr := json.Unmarshal(w.Metadata, &meta); jerr == nil && len(meta) > 0 {
			if err := frontendlimits.Count("metadata", len(meta), frontendlimits.MaxMetadata); err != nil {
				return nil, fmt.Errorf("anthropic: %w", err)
			}
			if err := sessionwire.ValidateMetadata(meta); err != nil {
				return nil, fmt.Errorf("anthropic: %w", err)
			}
			sessionwire.ApplyMetadata(&call.Session, meta)
		}
	}
	if opts.Headers != nil {
		sessionwire.ApplyAuthoritativeHeaders(&call.Session, opts.Headers)
		identitywire.CaptureClientUserAgent(&call.Invocation, opts.Headers)
	}
	if err := sessionwire.ApplyWorkspaceHint(call, opts.Headers, opts.HTTPHeaders); err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	_ = w.TopK
	return &DecodedMessage{
		Call:                  call,
		Stream:                w.Stream,
		Model:                 model,
		EmptySystemCarrier:    reduction,
		RequestControls:       requestControls,
		ContentSystemControls: contentControls,
	}, nil
}

func parseSystem(raw json.RawMessage) ([]aipapi.Message, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, nil
	}
	// Plain string system prompt.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
		return []aipapi.Message{{
			Role:  aipapi.RoleSystem,
			Parts: []aipapi.Part{aipapi.TextPart(s)},
		}}, nil
	}
	// Array of content blocks (text only for v1 subset).
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("anthropic: system: %w", err)
	}
	if err := frontendlimits.Count("system", len(blocks), frontendlimits.MaxParts); err != nil {
		return nil, err
	}
	parts := make([]aipapi.Part, 0, len(blocks))
	for i, blk := range blocks {
		p, err := parseContentBlock(blk)
		if err != nil {
			return nil, fmt.Errorf("anthropic: system block[%d]: %w", i, err)
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return []aipapi.Message{{Role: aipapi.RoleSystem, Parts: parts}}, nil
}

func parseAnthropicMessages(raw []json.RawMessage, serveEmptySystem, rejectControls bool) ([]aipapi.Message, EmptySystemCarrierReduction, ContentSystemControlReduction, error) {
	out := make([]aipapi.Message, 0, len(raw))
	var reduction EmptySystemCarrierReduction
	var contentControls ContentSystemControlReduction
	firstCarrier := 0
	for i, it := range raw {
		m, facts, err := parseAnthropicMessage(it)
		if err != nil {
			if errors.Is(err, errEmptySystemCarrier) && serveEmptySystem {
				// Serving the turn requires removing the carrier. The message
				// holds no instruction text, but any per-turn control it declares
				// is real semantics this frontend does not forward, so each class
				// is counted instead of being called loss-free.
				if rejectControls && facts.declaresAny() {
					return nil, EmptySystemCarrierReduction{}, ContentSystemControlReduction{}, fmt.Errorf("anthropic: messages[%d]: %w", i, errPerMessageControls)
				}
				if reduction.EmptyContent == 0 {
					firstCarrier = i
				}
				reduction.EmptyContent++
				if facts.perTurnEffort {
					reduction.PerTurnEffort++
				}
				if facts.otherFields {
					reduction.MessageFields++
				}
				continue
			}
			return nil, EmptySystemCarrierReduction{}, ContentSystemControlReduction{}, fmt.Errorf("anthropic: messages[%d]: %w", i, err)
		}
		if m.Role == aipapi.RoleSystem && facts.declaresAny() {
			if rejectControls {
				return nil, EmptySystemCarrierReduction{}, ContentSystemControlReduction{}, fmt.Errorf("anthropic: messages[%d]: %w", i, errPerMessageControls)
			}
			if facts.perTurnEffort {
				contentControls.PerTurnEffort++
			}
			if facts.otherFields {
				contentControls.MessageFields++
			}
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		// Removing contentless carriers cannot leave a request with no
		// content-bearing turn; keep the historical refusal for that
		// pathological body instead of inventing a turn.
		return nil, EmptySystemCarrierReduction{}, ContentSystemControlReduction{}, fmt.Errorf("anthropic: messages[%d]: %w", firstCarrier, errEmptyContentBlocks)
	}
	return out, reduction, contentControls, nil
}

func parseAnthropicMessage(raw json.RawMessage) (aipapi.Message, inlineSystemControlFacts, error) {
	var probe struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return aipapi.Message{}, inlineSystemControlFacts{}, err
	}
	role, err := mapAnthropicRole(probe.Role)
	if err != nil {
		return aipapi.Message{}, inlineSystemControlFacts{}, err
	}
	parts, err := parseMessageContent(probe.Content)
	if err != nil {
		if role == aipapi.RoleSystem && errors.Is(err, errEmptyContentBlocks) {
			// The role is the only fact that distinguishes a contentless
			// instruction carrier from an empty conversation turn. Only the
			// former has a defined reduction, and the facts it declares are
			// what make that reduction explicit instead of silent.
			facts, ferr := emptySystemCarrierDeclaredFacts(raw)
			if ferr != nil {
				return aipapi.Message{}, inlineSystemControlFacts{}, ferr
			}
			return aipapi.Message{}, facts, errEmptySystemCarrier
		}
		return aipapi.Message{}, inlineSystemControlFacts{}, err
	}
	var facts inlineSystemControlFacts
	if role == aipapi.RoleSystem {
		facts = declaredInlineSystemControls(raw)
	}
	return aipapi.Message{Role: role, Parts: parts}, facts, nil
}

// emptySystemCarrierDeclaredFacts reports which per-message controls a
// contentless inline system message declares. It keeps the historical refusal
// for a carrier whose `output_config` is not an object; a served content-bearing
// message must never gain that refusal, so it uses declaredInlineSystemControls.
func emptySystemCarrierDeclaredFacts(raw json.RawMessage) (inlineSystemControlFacts, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return inlineSystemControlFacts{}, err
	}
	if rawOutputConfig, ok := probe[messageOutputConfigKey]; ok && !jsonpresence.IsAbsentOrJSONNull(rawOutputConfig) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawOutputConfig, &fields); err != nil {
			return inlineSystemControlFacts{}, fmt.Errorf("anthropic: output_config: %w", err)
		}
	}
	return declaredInlineSystemControls(raw), nil
}

// declaredInlineSystemControls reports which per-message controls an inline
// system message declares. Only presence is retained: no effort value, no other
// field value, and no client text leaves this function. A malformed
// `output_config` is itself an unmodelled field, never a refusal.
func declaredInlineSystemControls(raw json.RawMessage) inlineSystemControlFacts {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return inlineSystemControlFacts{otherFields: true}
	}
	var facts inlineSystemControlFacts
	for key := range probe {
		switch key {
		case fieldRole, fieldContent, messageOutputConfigKey:
		default:
			// `clear_at` lands here too: a per-message rendering lifetime this
			// frontend does not model, so it is reported as an unforwarded field.
			facts.otherFields = true
		}
	}
	rawOutputConfig, ok := probe[messageOutputConfigKey]
	if !ok || jsonpresence.IsAbsentOrJSONNull(rawOutputConfig) {
		return facts
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawOutputConfig, &fields); err != nil {
		facts.otherFields = true
		return facts
	}
	for key := range fields {
		switch key {
		case perTurnEffortFieldKey:
		default:
			// `format` is top-level only per the vendor schema, so any sibling
			// field is outside what this frontend models.
			facts.otherFields = true
		}
	}
	if raw, ok := fields[perTurnEffortFieldKey]; ok && !jsonpresence.IsAbsentOrJSONNull(raw) {
		// Presence is all that is kept, and a malformed value is reported as an
		// unforwarded field rather than parsed. Reading the value would add a
		// refusal the baseline did not have for a field this frontend drops.
		if !jsonValidString(raw) {
			facts.otherFields = true
			return facts
		}
		facts.perTurnEffort = true
	}
	return facts
}

// applyRequestOutputConfig carries `output_config.effort` onto the canonical
// generation options and reports every request-level control it cannot carry.
// It never fails: a malformed value is reported, not rejected, so a body the
// baseline served stays served.
func applyRequestOutputConfig(raw json.RawMessage, opts *aipapi.GenerationOptions) RequestControlReduction {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return RequestControlReduction{}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return RequestControlReduction{FieldsNotForwarded: 1}
	}
	var reduction RequestControlReduction
	for key, value := range fields {
		if key != perTurnEffortFieldKey {
			reduction.FieldsNotForwarded++
			continue
		}
		if jsonpresence.IsAbsentOrJSONNull(value) {
			continue
		}
		var effort string
		if err := json.Unmarshal(value, &effort); err != nil || strings.TrimSpace(effort) == "" {
			reduction.EffortNotForwarded++
			continue
		}
		opts.ReasoningEffort = strings.TrimSpace(effort)
	}
	return reduction
}

// jsonValidString reports whether raw is a JSON string literal.
func jsonValidString(raw json.RawMessage) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func mapAnthropicRole(r string) (aipapi.Role, error) {
	switch strings.TrimSpace(r) {
	case "user":
		return aipapi.RoleUser, nil
	case "assistant":
		return aipapi.RoleAssistant, nil
	case "system":
		// Stock worker histories carry inline instructions. Keep their role and
		// position; hoisting them into top-level instructions would reorder history.
		return aipapi.RoleSystem, nil
	default:
		return "", fmt.Errorf("anthropic: unsupported message role %q", r)
	}
}

func parseMessageContent(raw json.RawMessage) ([]aipapi.Part, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, errors.New("anthropic: message content is required")
	}
	// Shorthand string content.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, errors.New("anthropic: empty text content")
		}
		return []aipapi.Part{aipapi.TextPart(s)}, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("anthropic: content: %w", err)
	}
	if err := frontendlimits.Count("content", len(blocks), frontendlimits.MaxParts); err != nil {
		return nil, err
	}
	out := make([]aipapi.Part, 0, len(blocks))
	for i, blk := range blocks {
		p, err := parseContentBlock(blk)
		if err != nil {
			return nil, fmt.Errorf("anthropic: content[%d]: %w", i, err)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errEmptyContentBlocks
	}
	return out, nil
}

func parseToolResultBlock(blk json.RawMessage) (aipapi.Part, error) {
	var w struct {
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(blk, &w); err != nil {
		return aipapi.Part{}, err
	}
	if strings.TrimSpace(w.ToolUseID) == "" {
		return aipapi.Part{}, errors.New("anthropic: tool_result requires tool_use_id")
	}
	// Content may be a string or array of text blocks; flatten to plain text.
	var resultText string
	if jsonpresence.IsPresentNonNullJSON(w.Content) {
		if err := frontendlimits.Bytes("tool_result.content", len(w.Content), frontendlimits.MaxRawJSONPayload); err != nil {
			return aipapi.Part{}, err
		}
		var s string
		if err := json.Unmarshal(w.Content, &s); err == nil {
			resultText = s
		} else {
			var blocks []json.RawMessage
			if err := json.Unmarshal(w.Content, &blocks); err == nil {
				var sb strings.Builder
				for _, b := range blocks {
					var tb struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}
					if err := json.Unmarshal(b, &tb); err == nil && tb.Type == "text" {
						sb.WriteString(tb.Text)
					}
				}
				resultText = sb.String()
			}
		}
	}
	return aipapi.Part{
		Kind:       aipapi.PartToolResult,
		ToolCallID: w.ToolUseID,
		Text:       resultText,
	}, nil
}

func parseToolUseBlock(blk json.RawMessage) (aipapi.Part, error) {
	var w struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(blk, &w); err != nil {
		return aipapi.Part{}, err
	}
	if strings.TrimSpace(w.ID) == "" {
		return aipapi.Part{}, errors.New("anthropic: tool_use requires id")
	}
	if strings.TrimSpace(w.Name) == "" {
		return aipapi.Part{}, errors.New("anthropic: tool_use requires name")
	}
	input := w.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if err := frontendlimits.Bytes("tool_use.input", len(input), frontendlimits.MaxRawJSONPayload); err != nil {
		return aipapi.Part{}, err
	}
	return aipapi.Part{
		Kind:       aipapi.PartJSON,
		ToolCallID: w.ID,
		ToolName:   w.Name,
		Content:    input,
	}, nil
}

func parseDocumentBlock(blk json.RawMessage) (aipapi.Part, error) {
	var w struct {
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(blk, &w); err != nil {
		return aipapi.Part{}, err
	}
	if w.Source.Type != "base64" {
		return aipapi.Part{}, fmt.Errorf("anthropic: document source type %q not supported", w.Source.Type)
	}
	data := strings.TrimSpace(w.Source.Data)
	if data == "" {
		return aipapi.Part{}, errors.New("anthropic: document requires base64 data")
	}
	if err := frontendlimits.StringBytes("document source.data", data, frontendlimits.MaxBase64Data); err != nil {
		return aipapi.Part{}, err
	}
	mime := strings.TrimSpace(w.Source.MediaType)
	if mime == "" {
		mime = "application/pdf"
	}
	name := "document"
	if mime == "application/pdf" {
		name = "document.pdf"
	}
	ref := "data:" + mime + ";base64," + data
	return aipapi.FilePart(ref, mime, name), nil
}

func parseImageBlock(blk json.RawMessage) (aipapi.Part, error) {
	var w struct {
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(blk, &w); err != nil {
		return aipapi.Part{}, err
	}
	if w.Source.Type != "base64" {
		return aipapi.Part{}, fmt.Errorf("anthropic: image source type %q not supported", w.Source.Type)
	}
	data := strings.TrimSpace(w.Source.Data)
	if data == "" {
		return aipapi.Part{}, errors.New("anthropic: image requires base64 data")
	}
	if err := frontendlimits.StringBytes("image source.data", data, frontendlimits.MaxBase64Data); err != nil {
		return aipapi.Part{}, err
	}
	mime := strings.TrimSpace(w.Source.MediaType)
	if mime == "" {
		mime = "image/png"
	}
	ref := "data:" + mime + ";base64," + data
	return aipapi.Part{Kind: aipapi.PartImageRef, ImageRef: ref, ImageMIME: mime}, nil
}

func parseTextBlock(blk json.RawMessage) (aipapi.Part, error) {
	var s struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(blk, &s); err != nil {
		return aipapi.Part{}, err
	}
	t := strings.TrimSpace(s.Text)
	if t == "" {
		return aipapi.Part{}, errors.New("anthropic: text block requires text")
	}
	return aipapi.TextPart(t), nil
}

func parseContentBlock(blk json.RawMessage) (aipapi.Part, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(blk, &probe); err != nil {
		return aipapi.Part{}, err
	}
	switch probe.Type {
	case "text":
		return parseTextBlock(blk)
	case "image":
		return parseImageBlock(blk)
	case "document":
		return parseDocumentBlock(blk)
	case "tool_use":
		return parseToolUseBlock(blk)
	case "tool_result":
		return parseToolResultBlock(blk)
	case "thinking":
		return parseThinkingBlock(blk)
	case "redacted_thinking":
		return parseRedactedThinkingBlock(blk)
	default:
		return aipapi.Part{}, fmt.Errorf("anthropic: unsupported content block type %q", probe.Type)
	}
}

func parseThinkingBlock(blk json.RawMessage) (aipapi.Part, error) {
	var w struct {
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(blk, &w); err != nil {
		return aipapi.Part{}, err
	}
	thinking := w.Thinking
	if err := frontendlimits.StringBytes("thinking", thinking, aipapi.MaxReasoningTextBytes); err != nil {
		return aipapi.Part{}, err
	}
	if err := frontendlimits.StringBytes("thinking.signature", w.Signature, aipapi.MaxReasoningSignatureBytes); err != nil {
		return aipapi.Part{}, err
	}
	if strings.TrimSpace(thinking) == "" && strings.TrimSpace(w.Signature) == "" {
		return aipapi.Part{}, errors.New("anthropic: thinking block requires thinking or signature")
	}
	return aipapi.Part{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect:   aipapi.ReasoningDialectAnthropicThinkingV1,
			Text:      thinking,
			Signature: w.Signature,
		},
	}, nil
}

func parseRedactedThinkingBlock(blk json.RawMessage) (aipapi.Part, error) {
	var w struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(blk, &w); err != nil {
		return aipapi.Part{}, err
	}
	if err := frontendlimits.StringBytes("redacted_thinking.data", w.Data, aipapi.MaxReasoningOpaqueBytes); err != nil {
		return aipapi.Part{}, err
	}
	opaque, err := json.Marshal(map[string]string{
		"type": "redacted_thinking",
		"data": w.Data,
	})
	if err != nil {
		return aipapi.Part{}, fmt.Errorf("anthropic: redacted_thinking opaque: %w", err)
	}
	return aipapi.Part{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectAnthropicRedactedThinkingV1,
			Opaque:  opaque,
		},
	}, nil
}

func parseTools(raw json.RawMessage) ([]aipapi.ToolDef, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("anthropic: tools: %w", err)
	}
	if err := frontendlimits.Count("tools", len(items), frontendlimits.MaxTools); err != nil {
		return nil, err
	}
	out := make([]aipapi.ToolDef, 0, len(items))
	for i, it := range items {
		var w struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if err := json.Unmarshal(it, &w); err != nil {
			return nil, fmt.Errorf("anthropic: tools[%d]: %w", i, err)
		}
		if strings.TrimSpace(w.Name) == "" {
			return nil, fmt.Errorf("anthropic: tools[%d]: name is required", i)
		}
		params := w.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		if err := frontendlimits.Bytes("input_schema", len(params), frontendlimits.MaxToolSchema); err != nil {
			return nil, fmt.Errorf("anthropic: tools[%d]: %w", i, err)
		}
		out = append(out, aipapi.ToolDef{
			Name:        w.Name,
			Description: w.Description,
			Parameters:  params,
		})
	}
	return out, nil
}

func parseToolChoice(raw json.RawMessage) (aipapi.ToolChoice, error) {
	if jsonpresence.IsAbsentOrJSONNull(raw) {
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.TrimSpace(s) {
		case "", "auto":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
		case "none":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, nil
		case "any":
			return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, nil
		default:
			return aipapi.ToolChoice{}, fmt.Errorf("anthropic: unsupported tool_choice string %q", s)
		}
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return aipapi.ToolChoice{}, fmt.Errorf("anthropic: tool_choice: %w", err)
	}
	switch obj.Type {
	case "auto", "":
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}, nil
	case "none":
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceNone}, nil
	case "any":
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}, nil
	case "tool":
		if strings.TrimSpace(obj.Name) == "" {
			return aipapi.ToolChoice{}, errors.New("anthropic: tool_choice tool requires name")
		}
		return aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: obj.Name}, nil
	default:
		return aipapi.ToolChoice{}, fmt.Errorf("anthropic: unsupported tool_choice type %q", obj.Type)
	}
}

// ModelFromCall returns the wire model string stored during decode.
func ModelFromCall(c *aipapi.Call) string {
	return openaiwire.ModelFromExtensions(c, extModelJSONKey)
}
