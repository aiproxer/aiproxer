package backendplugin

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// CallFromInvocation maps a public plugin Invocation into a canonical aipapi.Call
// for connector-local protocol engines (ACP support, OpenAI-compat mappers, etc.).
// Validate uses this same mapper so ABI gates cannot accept a shape production rejects.
func CallFromInvocation(inv Invocation) (aipapi.Call, error) {
	if err := inv.validateDTO(); err != nil {
		return aipapi.Call{}, err
	}
	return mapInvocationToCall(inv)
}

func mapInvocationToCall(inv Invocation) (aipapi.Call, error) {
	instructions, err := messagesToAipapi(inv.Instructions)
	if err != nil {
		return aipapi.Call{}, err
	}
	messages, err := messagesToAipapi(inv.Messages)
	if err != nil {
		return aipapi.Call{}, err
	}
	toolChoice, err := ToolChoiceFromWire(inv.ToolChoice)
	if err != nil {
		return aipapi.Call{}, err
	}
	call := aipapi.Call{
		ID:             strings.TrimSpace(inv.RequestID),
		Instructions:   instructions,
		Messages:       messages,
		Tools:          toolsToAipapi(inv.Tools),
		ToolChoice:     toolChoice,
		Options:        optionsToAipapi(inv.Options),
		PromptCacheKey: strings.TrimSpace(inv.PromptCacheKey),
		Route: aipapi.RouteIntent{
			Selector: strings.TrimSpace(inv.CanonicalModelID),
		},
		Session: aipapi.SessionRef{
			ALegID: strings.TrimSpace(inv.ALegID),
			// This field is host-injected at the ABI boundary; SafeMetadata is
			// intentionally not consulted for session authority.
			AuthoritativeSessionID: strings.TrimSpace(inv.ProxyOwnedSessionID),
		},
	}
	for _, ext := range inv.SemanticExtensions {
		mapped := aipapi.SemanticExtension{Namespace: ext.Namespace, Type: ext.Type, Implementor: ext.Implementor, Direction: ext.Direction}
		switch ext.Presence {
		case SemanticExtensionNull:
			mapped.Presence = aipapi.SemanticExtensionNull
		case SemanticExtensionValue:
			mapped.Presence = aipapi.SemanticExtensionValue
			mapped.Data = append([]byte(nil), ext.Data.Bytes()...)
		}
		call.SemanticExtensions = append(call.SemanticExtensions, mapped)
	}
	if err := reconcilePromptCacheKey(&call); err != nil {
		return aipapi.Call{}, err
	}
	RestoreCallWireMetadata(&call, inv.SafeMetadata)
	if err := applyInvocationWireToCall(&call, inv); err != nil {
		return aipapi.Call{}, err
	}
	if err := call.Validate(); err != nil {
		return aipapi.Call{}, err
	}
	return call, nil
}

func reconcilePromptCacheKey(call *aipapi.Call) error {
	var carrier string
	for _, ext := range call.SemanticExtensions {
		if ext.Namespace != "aip" || ext.Type != "prompt_cache_key" || ext.Implementor != "proxy" || ext.Direction != "request" {
			continue
		}
		if ext.Presence != aipapi.SemanticExtensionValue {
			return fmt.Errorf("prompt_cache_key semantic carrier must have a value")
		}
		if err := json.Unmarshal(ext.Data, &carrier); err != nil {
			return fmt.Errorf("prompt_cache_key semantic carrier: %w", err)
		}
		carrier = strings.TrimSpace(carrier)
	}
	legacy := strings.TrimSpace(call.PromptCacheKey)
	if legacy != "" && carrier != "" && legacy != carrier {
		return fmt.Errorf("prompt_cache_key legacy alias conflicts with semantic carrier")
	}
	if carrier != "" {
		call.PromptCacheKey = carrier
	}
	return nil
}

// CanonicalEventFromAipapi maps a canonical stream event into the plugin wire DTO.
func CanonicalEventFromAipapi(ev aipapi.Event) *CanonicalEvent {
	out := &CanonicalEvent{Kind: ev.Kind}
	// The wire carries MessageIndex as int32; skip rather than silently wrap when
	// the canonical int exceeds the representable range (defensive: positions are
	// bounded by input size in practice, but nothing else enforces that).
	if ev.MessageIndex >= math.MinInt32 && ev.MessageIndex <= math.MaxInt32 && ev.MessageIndex != 0 {
		v := int32(ev.MessageIndex)
		out.MessageIndex = &v
	}
	if ev.Delta != "" {
		d := ev.Delta
		out.Delta = &d
	}
	if ev.Signature != "" {
		s := ev.Signature
		out.Signature = &s
	}
	if len(ev.Opaque) > 0 {
		out.Opaque = append([]byte(nil), ev.Opaque...)
	}
	if ev.Reasoning != nil {
		dialect := string(ev.Reasoning.Dialect)
		out.ReasoningDialect = &dialect
		out.ReasoningOpaque = append([]byte(nil), ev.Reasoning.Opaque...)
		mapReasoningExactFields(ev.Reasoning,
			&out.ReasoningSummary, &out.ReasoningContent, &out.ReasoningEncryptedContent)
	}
	if ev.ToolCallID != "" {
		id := ev.ToolCallID
		out.ToolCallID = &id
	}
	if ev.ToolName != "" {
		n := ev.ToolName
		out.ToolName = &n
	}
	if hasUsage(ev) {
		usage := &UsageEvidence{RawUsageJSON: RawJSONAbsentValue()}
		setUsageField(ev.InputTokens, ev.UsagePresence.InputTokens, &usage.InputTokens, &usage.Presence.InputTokens)
		setUsageField(ev.OutputTokens, ev.UsagePresence.OutputTokens, &usage.OutputTokens, &usage.Presence.OutputTokens)
		setUsageField(ev.CacheReadTokens, ev.UsagePresence.CacheReadTokens, &usage.CacheReadTokens, &usage.Presence.CacheReadTokens)
		setUsageField(ev.CacheWriteTokens, ev.UsagePresence.CacheWriteTokens, &usage.CacheWriteTokens, &usage.Presence.CacheWriteTokens)
		setUsageField(ev.ReasoningTokens, ev.UsagePresence.ReasoningTokens, &usage.ReasoningTokens, &usage.Presence.ReasoningTokens)
		// The input relations travel with the counters: a producer that reports
		// its cache reads or its cache writes outside the input counter must
		// keep that declaration across the connector boundary. The two
		// declarations are independent.
		usage.InputTokensExcludeCacheRead = ev.Accounting.InputTokensExcludeCacheRead
		usage.InputTokensExcludeCacheWrite = ev.Accounting.InputTokensExcludeCacheWrite
		// An omitted provider total stays omitted: a derived input+output sum
		// would report a quantity the provider never claimed. A reported nonzero
		// value and an explicit zero keep their presence.
		if ev.TotalTokens != 0 || ev.UsagePresence.TotalTokens {
			setUsageField(ev.TotalTokens, ev.UsagePresence.TotalTokens, &usage.TotalTokens, &usage.Presence.TotalTokens)
		}
		out.Usage = usage
	}
	if ev.WarningMessage != "" {
		w := ev.WarningMessage
		out.Warning = &w
	}
	if ev.ErrorCode != "" || ev.ErrorMessage != "" {
		out.Error = &PluginError{
			Code:    ErrorCode(ev.ErrorCode),
			Message: ev.ErrorMessage,
		}
	}
	if ev.AssistantRef != "" {
		r := ev.AssistantRef
		switch ev.Kind {
		case aipapi.EventAssistantFileRef:
			out.FileRef = &r
		default:
			out.ImageRef = &r
		}
	}
	if ev.FinishReason != "" {
		fr := ev.FinishReason
		out.FinishReason = &fr
	}
	if ev.ResponseStatus != "" {
		rs := ev.ResponseStatus
		out.ResponseStatus = &rs
	}
	return out
}

func hasUsage(ev aipapi.Event) bool {
	return ev.InputTokens != 0 || ev.OutputTokens != 0 || ev.CacheReadTokens != 0 ||
		ev.CacheWriteTokens != 0 || ev.ReasoningTokens != 0 || ev.TotalTokens != 0 ||
		ev.UsagePresence.Any()
}

func setUsageField(value int, present bool, dst **int64, flag *bool) {
	if value == 0 && !present {
		return
	}
	v := int64(value)
	*dst = &v
	*flag = true
}

func messagesToAipapi(in []Message) ([]aipapi.Message, error) {
	out := make([]aipapi.Message, 0, len(in))
	for _, m := range in {
		parts, err := partsToAipapi(m.Parts)
		if err != nil {
			return nil, err
		}
		out = append(out, aipapi.Message{
			Role:  m.Role,
			Parts: parts,
		})
	}
	return out, nil
}

func partsToAipapi(in []Part) ([]aipapi.Part, error) {
	out := make([]aipapi.Part, 0, len(in))
	for _, p := range in {
		switch p.Kind {
		case PartKindText:
			t := ""
			if p.Text != nil {
				t = *p.Text
			}
			out = append(out, aipapi.TextPart(t))
		case PartKindImageRef:
			r := ""
			if p.ImageRef != nil {
				r = *p.ImageRef
			}
			out = append(out, aipapi.Part{Kind: aipapi.PartImageRef, ImageRef: r})
		case PartKindFileRef:
			r := ""
			if p.FileRef != nil {
				r = *p.FileRef
			}
			out = append(out, aipapi.Part{Kind: aipapi.PartFileRef, FileRef: r})
		case PartKindReasoning:
			text := ""
			if p.ReasoningText != nil {
				text = *p.ReasoningText
			}
			reasoning := &aipapi.ReasoningPart{Text: text}
			if p.ReasoningDialect != nil {
				reasoning.Dialect = aipapi.ReasoningDialect(*p.ReasoningDialect)
			}
			if opaque := p.ReasoningOpaque.Bytes(); len(opaque) > 0 {
				reasoning.Opaque = opaque
			}
			applyReasoningExactFields(p.ReasoningSummary, p.ReasoningContent, p.ReasoningEncryptedContent, reasoning)
			out = append(out, aipapi.Part{
				Kind:      aipapi.PartReasoning,
				Reasoning: reasoning,
			})
		case PartKindToolResult:
			part := aipapi.Part{Kind: aipapi.PartToolResult}
			if p.Text != nil {
				part.Text = *p.Text
			}
			if p.ToolCallID != nil {
				part.ToolCallID = *p.ToolCallID
			}
			if p.ToolName != nil {
				part.ToolName = *p.ToolName
			}
			if b := p.ToolArgsJSON.Bytes(); len(b) > 0 {
				part.Content = b
			}
			out = append(out, part)
		case PartKindJSON:
			b := p.ToolArgsJSON.Bytes()
			if len(b) == 0 {
				return nil, fmt.Errorf("%w: json part requires content", ErrInvalidInvocation)
			}
			part := aipapi.Part{Kind: aipapi.PartJSON, Content: b}
			// The reverse mapping must restore the same call identity the
			// host mapper preserved, or a connector still cannot correlate
			// the model's call with the client's result.
			if p.ToolCallID != nil {
				part.ToolCallID = *p.ToolCallID
			}
			if p.ToolName != nil {
				part.ToolName = *p.ToolName
			}
			out = append(out, part)
		default:
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedPartKind, p.Kind)
		}
	}
	return out, nil
}

func toolsToAipapi(in []ToolDef) []aipapi.ToolDef {
	out := make([]aipapi.ToolDef, 0, len(in))
	for _, t := range in {
		td := aipapi.ToolDef{Name: t.Name, Description: t.Description}
		if b := t.ParametersJSON.Bytes(); len(b) > 0 {
			td.Parameters = b
		}
		out = append(out, td)
	}
	return out
}

func optionsToAipapi(o GenerationOptions) aipapi.GenerationOptions {
	out := aipapi.GenerationOptions{}
	if o.MaxOutputTokens != nil {
		v := int(*o.MaxOutputTokens)
		out.MaxOutputTokens = &v
	}
	if o.TemperatureMillis != nil {
		v := float64(*o.TemperatureMillis) / 1000
		out.Temperature = &v
	}
	if o.ReasoningEffort != nil {
		out.ReasoningEffort = strings.TrimSpace(*o.ReasoningEffort)
	}
	if o.ParallelToolCalls != nil {
		v := *o.ParallelToolCalls
		out.ParallelToolCalls = &v
	}
	if o.ResponseMIMEType != nil {
		out.ResponseMIMEType = strings.TrimSpace(*o.ResponseMIMEType)
	}
	return out
}
