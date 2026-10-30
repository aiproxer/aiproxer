package backendplugin

import (
	"strings"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
)

func partFromProto(p *backendpluginv1.Part) (Part, error) {
	if p == nil {
		return Part{}, ErrInvalidInvocation
	}
	kind, err := partKindFromProto(p.GetKind())
	if err != nil {
		return Part{}, err
	}
	raw, err := RawJSONFromProto(p.GetToolArgsJson())
	if err != nil {
		return Part{}, err
	}
	reasoningOpaque, err := RawJSONFromProto(p.GetReasoningOpaque())
	if err != nil {
		return Part{}, err
	}
	reasoningSummary, err := RawJSONFromProto(p.GetReasoningSummary())
	if err != nil {
		return Part{}, err
	}
	reasoningContent, err := RawJSONFromProto(p.GetReasoningContent())
	if err != nil {
		return Part{}, err
	}
	reasoningEncrypted, err := RawJSONFromProto(p.GetReasoningEncryptedContent())
	if err != nil {
		return Part{}, err
	}
	part := Part{
		Kind:                      kind,
		Text:                      optString(p.Text),
		ImageRef:                  optString(p.ImageRef),
		FileRef:                   optString(p.FileRef),
		ReasoningText:             optString(p.ReasoningText),
		ReasoningDialect:          optString(p.ReasoningDialect),
		ReasoningOpaque:           reasoningOpaque,
		ToolArgsJSON:              raw,
		ToolCallID:                optString(p.ToolCallId),
		ToolName:                  optString(p.ToolName),
		ReasoningSummary:          reasoningSummary,
		ReasoningContent:          reasoningContent,
		ReasoningEncryptedContent: reasoningEncrypted,
	}
	if err := validateExactReasoningRawFields(part.ReasoningSummary, part.ReasoningContent, part.ReasoningEncryptedContent, "Part"); err != nil {
		return Part{}, err
	}
	return part, nil
}

func partToProto(p Part) (*backendpluginv1.Part, error) {
	kind, err := partKindToProto(p.Kind)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.Part{
		Kind:                      kind,
		Text:                      optString(p.Text),
		ImageRef:                  optString(p.ImageRef),
		FileRef:                   optString(p.FileRef),
		ReasoningText:             optString(p.ReasoningText),
		ReasoningDialect:          optString(p.ReasoningDialect),
		ReasoningOpaque:           RawJSONToProto(p.ReasoningOpaque),
		ToolArgsJson:              RawJSONToProto(p.ToolArgsJSON),
		ToolCallId:                optString(p.ToolCallID),
		ToolName:                  optString(p.ToolName),
		ReasoningSummary:          RawJSONToProto(p.ReasoningSummary),
		ReasoningContent:          RawJSONToProto(p.ReasoningContent),
		ReasoningEncryptedContent: RawJSONToProto(p.ReasoningEncryptedContent),
	}, nil
}

func messageFromProto(m *backendpluginv1.Message) (Message, error) {
	if m == nil {
		return Message{}, ErrInvalidInvocation
	}
	role, err := roleFromProto(m.GetRole())
	if err != nil {
		return Message{}, err
	}
	parts := make([]Part, 0, len(m.GetParts()))
	for _, p := range m.GetParts() {
		part, err := partFromProto(p)
		if err != nil {
			return Message{}, err
		}
		parts = append(parts, part)
	}
	return Message{Role: role, Parts: parts}, nil
}

func messageToProto(m Message) (*backendpluginv1.Message, error) {
	role, err := roleToProto(m.Role)
	if err != nil {
		return nil, err
	}
	parts := make([]*backendpluginv1.Part, 0, len(m.Parts))
	for _, p := range m.Parts {
		pp, err := partToProto(p)
		if err != nil {
			return nil, err
		}
		parts = append(parts, pp)
	}
	return &backendpluginv1.Message{Role: role, Parts: parts}, nil
}

func toolDefFromProto(t *backendpluginv1.ToolDef) (ToolDef, error) {
	if t == nil {
		return ToolDef{}, ErrInvalidInvocation
	}
	raw, err := RawJSONFromProto(t.GetParametersJson())
	if err != nil {
		return ToolDef{}, err
	}
	return ToolDef{Name: t.GetName(), Description: t.GetDescription(), ParametersJSON: raw}, nil
}

func toolDefToProto(t ToolDef) (*backendpluginv1.ToolDef, error) {
	return &backendpluginv1.ToolDef{
		Name:           t.Name,
		Description:    t.Description,
		ParametersJson: RawJSONToProto(t.ParametersJSON),
	}, nil
}

func generationOptionsFromProto(p *backendpluginv1.GenerationOptions) (GenerationOptions, error) {
	if p == nil {
		return GenerationOptions{ResponseSchemaJSON: RawJSONAbsentValue()}, nil
	}
	raw, err := RawJSONFromProto(p.GetResponseSchemaJson())
	if err != nil {
		return GenerationOptions{}, err
	}
	return GenerationOptions{
		MaxOutputTokens:    optUint32(p.MaxOutputTokens),
		TemperatureMillis:  optInt32(p.TemperatureMillis),
		ReasoningEffort:    optString(p.ReasoningEffort),
		ParallelToolCalls:  optBool(p.ParallelToolCalls),
		ResponseMIMEType:   optString(p.ResponseMimeType),
		ResponseSchemaJSON: raw,
	}, nil
}

func generationOptionsToProto(o GenerationOptions) (*backendpluginv1.GenerationOptions, error) {
	return &backendpluginv1.GenerationOptions{
		MaxOutputTokens:    optUint32(o.MaxOutputTokens),
		TemperatureMillis:  optInt32(o.TemperatureMillis),
		ReasoningEffort:    optString(o.ReasoningEffort),
		ParallelToolCalls:  optBool(o.ParallelToolCalls),
		ResponseMimeType:   optString(o.ResponseMIMEType),
		ResponseSchemaJson: RawJSONToProto(o.ResponseSchemaJSON),
	}, nil
}

// InvocationFromProto converts a wire invocation.
func InvocationFromProto(p *backendpluginv1.Invocation) (Invocation, error) {
	if p == nil {
		return Invocation{}, ErrInvalidInvocation
	}
	opts, err := generationOptionsFromProto(p.GetOptions())
	if err != nil {
		return Invocation{}, err
	}
	inv := Invocation{
		RequestID:           p.GetRequestId(),
		AttemptID:           p.GetAttemptId(),
		ALegID:              p.GetALegId(),
		BLegID:              p.GetBLegId(),
		CanonicalModelID:    p.GetCanonicalModelId(),
		NativeModelID:       p.GetNativeModelId(),
		ToolChoice:          optString(p.ToolChoice),
		Options:             opts,
		SafeMetadata:        p.GetSafeMetadata(),
		ProxyOwnedSessionID: strings.TrimSpace(p.GetProxyOwnedSessionId()),
		PromptCacheKey:      strings.TrimSpace(p.GetPromptCacheKey()),
	}
	for _, ext := range p.GetSemanticExtensions() {
		mapped, err := semanticExtensionFromProto(ext)
		if err != nil {
			return Invocation{}, err
		}
		inv.SemanticExtensions = append(inv.SemanticExtensions, mapped)
	}
	for _, m := range p.GetInstructions() {
		msg, err := messageFromProto(m)
		if err != nil {
			return Invocation{}, err
		}
		inv.Instructions = append(inv.Instructions, msg)
	}
	for _, m := range p.GetMessages() {
		msg, err := messageFromProto(m)
		if err != nil {
			return Invocation{}, err
		}
		inv.Messages = append(inv.Messages, msg)
	}
	for _, t := range p.GetTools() {
		td, err := toolDefFromProto(t)
		if err != nil {
			return Invocation{}, err
		}
		inv.Tools = append(inv.Tools, td)
	}
	if err := invocationWireFromProto(p, &inv); err != nil {
		return Invocation{}, err
	}
	if err := inv.Validate(); err != nil {
		return Invocation{}, err
	}
	return inv, nil
}

// InvocationToProto encodes an invocation.
func InvocationToProto(inv Invocation) (*backendpluginv1.Invocation, error) {
	if err := inv.Validate(); err != nil {
		return nil, err
	}
	opts, err := generationOptionsToProto(inv.Options)
	if err != nil {
		return nil, err
	}
	out := &backendpluginv1.Invocation{
		RequestId:           inv.RequestID,
		AttemptId:           inv.AttemptID,
		ALegId:              inv.ALegID,
		BLegId:              inv.BLegID,
		CanonicalModelId:    inv.CanonicalModelID,
		NativeModelId:       inv.NativeModelID,
		ToolChoice:          optString(inv.ToolChoice),
		Options:             opts,
		SafeMetadata:        inv.SafeMetadata,
		ProxyOwnedSessionId: strings.TrimSpace(inv.ProxyOwnedSessionID),
		PromptCacheKey:      strings.TrimSpace(inv.PromptCacheKey),
	}
	if len(inv.SemanticExtensions) > 0 {
		out.PromptCacheKey = ""
	}
	for _, ext := range inv.SemanticExtensions {
		mapped, err := semanticExtensionToProto(ext)
		if err != nil {
			return nil, err
		}
		out.SemanticExtensions = append(out.SemanticExtensions, mapped)
	}
	for _, m := range inv.Instructions {
		pm, err := messageToProto(m)
		if err != nil {
			return nil, err
		}
		out.Instructions = append(out.Instructions, pm)
	}
	for _, m := range inv.Messages {
		pm, err := messageToProto(m)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, pm)
	}
	for _, t := range inv.Tools {
		pt, err := toolDefToProto(t)
		if err != nil {
			return nil, err
		}
		out.Tools = append(out.Tools, pt)
	}
	if err := invocationWireToProto(inv, out); err != nil {
		return nil, err
	}
	return out, nil
}
