package adapter

import (
	"bytes"
	"fmt"
	"math"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// InvocationFromCall maps a core call into the public plugin Invocation DTO.
func InvocationFromCall(call aipapi.Call, cand routing.AttemptCandidate, neg backendplugin.Negotiation) (backendplugin.Invocation, error) {
	if call.Decision != nil {
		return backendplugin.Invocation{}, &aipapi.RejectError{Missing: []aipapi.Capability{aipapi.CapabilityDecisions}, Reason: "executable connectors do not support decision requests"}
	}
	if strings.TrimSpace(call.Session.AuthoritativeSessionID) != "" && !backendplugin.ProxyOwnedSessionIDSupported(neg) {
		return backendplugin.Invocation{}, fmt.Errorf("%w: negotiated proxy-owned session authority is required", backendplugin.ErrProxyOwnedSessionUnsupported)
	}
	if err := aipapi.ValidateToolChoice(call.ToolChoice, call.Tools); err != nil {
		return backendplugin.Invocation{}, err
	}
	if err := backendplugin.ValidateToolChoiceABI(call.ToolChoice); err != nil {
		return backendplugin.Invocation{}, err
	}
	reqID := strings.TrimSpace(call.ID)
	if reqID == "" {
		reqID = "req"
	}
	attemptID := reqID + "-attempt"
	aLeg := strings.TrimSpace(call.Session.ALegID)
	if aLeg == "" {
		aLeg = "aleg"
	}
	bLeg := execbackend.ConnectorLegPseudonym(cand.Key, cand.Primary.String())
	model := strings.TrimSpace(cand.Primary.Model)
	if model == "" {
		model = strings.TrimSpace(call.Route.Selector)
	}
	if model == "" {
		model = "model"
	}
	instructions, err := mapMessages(call.Instructions)
	if err != nil {
		return backendplugin.Invocation{}, err
	}
	messages, err := mapMessages(call.Messages)
	if err != nil {
		return backendplugin.Invocation{}, err
	}
	inv := backendplugin.Invocation{
		RequestID:        reqID,
		AttemptID:        attemptID,
		ALegID:           aLeg,
		BLegID:           bLeg,
		CanonicalModelID: model,
		NativeModelID:    model,
		Instructions:     instructions,
		Messages:         messages,
		Tools:            mapTools(call.Tools),
		ToolChoice:       backendplugin.ToolChoiceToWire(call.ToolChoice),
		Options:          mapOptions(call.Options),
	}
	routeParams := map[string]string{}
	if cand.Primary.Params != nil {
		for k := range cand.Primary.Params {
			if v := cand.Primary.TrimmedParam(k); v != "" {
				routeParams[k] = v
			}
		}
	}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, routeParams, neg); err != nil {
		return backendplugin.Invocation{}, err
	}
	if err := inv.Validate(); err != nil {
		return backendplugin.Invocation{}, err
	}
	return inv, nil
}

func mapMessages(in []aipapi.Message) ([]backendplugin.Message, error) {
	out := make([]backendplugin.Message, 0, len(in))
	for _, m := range in {
		parts, err := mapParts(m.Parts)
		if err != nil {
			return nil, err
		}
		out = append(out, backendplugin.Message{
			Role:  m.Role,
			Parts: parts,
		})
	}
	return out, nil
}

func mapParts(in []aipapi.Part) ([]backendplugin.Part, error) {
	out := make([]backendplugin.Part, 0, len(in))
	for _, p := range in {
		bp := backendplugin.Part{
			Kind:         backendplugin.PartKind(p.Kind),
			ToolArgsJSON: backendplugin.RawJSONAbsentValue(),
		}
		switch p.Kind {
		case aipapi.PartText:
			t := p.Text
			bp.Kind = backendplugin.PartKindText
			bp.Text = &t
		case aipapi.PartImageRef:
			r := p.ImageRef
			bp.Kind = backendplugin.PartKindImageRef
			bp.ImageRef = &r
		case aipapi.PartFileRef:
			r := p.FileRef
			bp.Kind = backendplugin.PartKindFileRef
			bp.FileRef = &r
		case aipapi.PartReasoning:
			bp.Kind = backendplugin.PartKindReasoning
			if p.Reasoning != nil {
				t := p.Reasoning.Text
				bp.ReasoningText = &t
				dialect := string(p.Reasoning.Dialect)
				bp.ReasoningDialect = &dialect
				if len(p.Reasoning.Opaque) > 0 {
					bp.ReasoningOpaque = backendplugin.RawJSONFromBytes(p.Reasoning.Opaque)
				}
				mapReasoningExactPartFields(p.Reasoning, &bp)
			}
		case aipapi.PartToolResult:
			id := p.ToolCallID
			name := p.ToolName
			bp.Kind = backendplugin.PartKindToolResult
			bp.ToolCallID = &id
			bp.ToolName = &name
			// A tool result has two independent canonical carriers: literal
			// text and structured content. The ABI carries both, so neither
			// may be dropped when the other is present.
			if p.Text != "" {
				text := p.Text
				bp.Text = &text
			}
			if len(p.Content) > 0 {
				bp.ToolArgsJSON = backendplugin.RawJSONFromBytes(p.Content)
			}
		case aipapi.PartJSON:
			bp.Kind = backendplugin.PartKindJSON
			if len(p.Content) == 0 {
				return nil, fmt.Errorf("%w: json part requires content", backendplugin.ErrInvalidInvocation)
			}
			bp.ToolArgsJSON = backendplugin.RawJSONFromBytes(p.Content)
			// A JSON part may be the model's tool call; its call identity is
			// part of the canonical content, not decoration.
			if p.ToolCallID != "" {
				id := p.ToolCallID
				bp.ToolCallID = &id
			}
			if p.ToolName != "" {
				name := p.ToolName
				bp.ToolName = &name
			}
		default:
			return nil, fmt.Errorf("%w: %q", backendplugin.ErrUnsupportedPartKind, p.Kind)
		}
		out = append(out, bp)
	}
	return out, nil
}

// mapReasoningExactPartFields projects canonical OpenAI Responses reasoning-item
// exact fields onto the legacy ABI Part carrier with absent/null/value presence.
func mapReasoningExactPartFields(r *aipapi.ReasoningPart, bp *backendplugin.Part) {
	if bp == nil || r == nil {
		return
	}
	if r.SummaryPresent || len(r.Summary) > 0 {
		bp.ReasoningSummary = backendplugin.RawJSONFromBytes(r.Summary)
	}
	if r.ContentPresent || len(r.Content) > 0 {
		bp.ReasoningContent = backendplugin.RawJSONFromBytes(r.Content)
	}
	if r.EncryptedContentPresent {
		if isNullJSON(r.EncryptedContent) {
			bp.ReasoningEncryptedContent = backendplugin.RawJSONNullValue()
		} else {
			bp.ReasoningEncryptedContent = backendplugin.RawJSONFromBytes(r.EncryptedContent)
		}
	}
}

func isNullJSON(b []byte) bool {
	return bytes.Equal(bytes.TrimSpace(b), []byte("null"))
}

func mapTools(in []aipapi.ToolDef) []backendplugin.ToolDef {
	out := make([]backendplugin.ToolDef, 0, len(in))
	for _, t := range in {
		params := backendplugin.RawJSONAbsentValue()
		if len(t.Parameters) > 0 {
			params = backendplugin.RawJSONFromBytes(t.Parameters)
		}
		out = append(out, backendplugin.ToolDef{
			Name: t.Name, Description: t.Description, ParametersJSON: params,
		})
	}
	return out
}

func mapOptions(o aipapi.GenerationOptions) backendplugin.GenerationOptions {
	out := backendplugin.GenerationOptions{
		ResponseSchemaJSON: backendplugin.RawJSONAbsentValue(),
	}
	if o.MaxOutputTokens != nil && *o.MaxOutputTokens > 0 {
		v := *o.MaxOutputTokens
		// Clamp before narrowing: uint32(int) wraps modulo 2^32, so a huge
		// client-controlled value would silently become a small limit (e.g.
		// 1<<40 -> 0) and pass downstream validation. Saturate to MaxUint32
		// so oversized values fail closed in CallFromInvocation (MaxUint32 >
		// aipapi MaxInt32) instead of wrapping.
		if int64(v) > int64(math.MaxUint32) {
			v = int(math.MaxUint32)
		}
		uv := uint32(v)
		out.MaxOutputTokens = &uv
	}
	if o.Temperature != nil {
		t := *o.Temperature
		if math.IsNaN(t) {
			// Preserve existing fail-open skip for NaN; int32(NaN) is
			// implementation-defined and would corrupt wire options.
		} else if math.IsInf(t, 1) {
			ms := int32(math.MaxInt32)
			out.TemperatureMillis = &ms
		} else if math.IsInf(t, -1) {
			ms := int32(math.MinInt32)
			out.TemperatureMillis = &ms
		} else {
			mf := t * 1000
			if mf > float64(math.MaxInt32) {
				mf = float64(math.MaxInt32)
			} else if mf < float64(math.MinInt32) {
				mf = float64(math.MinInt32)
			}
			ms := int32(mf)
			out.TemperatureMillis = &ms
		}
	}
	if s := strings.TrimSpace(o.ReasoningEffort); s != "" {
		out.ReasoningEffort = &s
	}
	if o.ParallelToolCalls != nil {
		v := *o.ParallelToolCalls
		out.ParallelToolCalls = &v
	}
	if s := strings.TrimSpace(o.ResponseMIMEType); s != "" {
		out.ResponseMIMEType = &s
	}
	return out
}
