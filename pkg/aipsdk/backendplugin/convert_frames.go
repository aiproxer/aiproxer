package backendplugin

import (
	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
)

// TerminalFromProto converts terminals.
func TerminalFromProto(p *backendpluginv1.Terminal) (*Terminal, error) {
	if p == nil {
		return nil, nil
	}
	status, err := terminalStatusFromProto(p.GetStatus())
	if err != nil {
		return nil, err
	}
	pe, err := PluginErrorFromProto(p.GetError())
	if err != nil {
		return nil, err
	}
	return &Terminal{Status: status, Error: pe}, nil
}

// TerminalToProto encodes terminals.
func TerminalToProto(t *Terminal) (*backendpluginv1.Terminal, error) {
	if t == nil {
		return nil, nil
	}
	status, err := terminalStatusToProto(t.Status)
	if err != nil {
		return nil, err
	}
	pe, err := PluginErrorToProto(t.Error)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.Terminal{Status: status, Error: pe}, nil
}

// CanonicalEventFromProto converts stream events.
func CanonicalEventFromProto(p *backendpluginv1.CanonicalEvent) (*CanonicalEvent, error) {
	if p == nil {
		return nil, nil
	}
	kind, err := eventKindFromProto(p.GetKind())
	if err != nil {
		return nil, err
	}
	var usage *UsageEvidence
	if p.Usage != nil {
		u, err := UsageEvidenceFromProto(p.GetUsage())
		if err != nil {
			return nil, err
		}
		usage = &u
	}
	pe, err := PluginErrorFromProto(p.GetError())
	if err != nil {
		return nil, err
	}
	reasoningSummary, err := RawJSONFromProto(p.GetReasoningSummary())
	if err != nil {
		return nil, err
	}
	reasoningContent, err := RawJSONFromProto(p.GetReasoningContent())
	if err != nil {
		return nil, err
	}
	reasoningEncrypted, err := RawJSONFromProto(p.GetReasoningEncryptedContent())
	if err != nil {
		return nil, err
	}
	event := &CanonicalEvent{
		Kind:                      kind,
		MessageIndex:              optInt32(p.MessageIndex),
		Delta:                     optString(p.Delta),
		Signature:                 optString(p.Signature),
		Opaque:                    append([]byte(nil), p.GetOpaque()...),
		ToolCallID:                optString(p.ToolCallId),
		ToolName:                  optString(p.ToolName),
		Usage:                     usage,
		Warning:                   optString(p.Warning),
		Error:                     pe,
		ImageRef:                  optString(p.ImageRef),
		FileRef:                   optString(p.FileRef),
		ReasoningDialect:          optString(p.ReasoningDialect),
		ReasoningOpaque:           append([]byte(nil), p.GetReasoningOpaque()...),
		ReasoningSummary:          reasoningSummary,
		ReasoningContent:          reasoningContent,
		ReasoningEncryptedContent: reasoningEncrypted,
		FinishReason:              optString(p.FinishReason),
		ResponseStatus:            optString(p.ResponseStatus),
	}
	if err := validateExactReasoningRawFields(event.ReasoningSummary, event.ReasoningContent, event.ReasoningEncryptedContent, "CanonicalEvent"); err != nil {
		return nil, err
	}
	return event, nil
}

// CanonicalEventToProto encodes stream events.
func CanonicalEventToProto(e *CanonicalEvent) (*backendpluginv1.CanonicalEvent, error) {
	if e == nil {
		return nil, nil
	}
	if err := validateExactReasoningRawFields(e.ReasoningSummary, e.ReasoningContent, e.ReasoningEncryptedContent, "CanonicalEvent"); err != nil {
		return nil, err
	}
	kind, err := eventKindToProto(e.Kind)
	if err != nil {
		return nil, err
	}
	var usage *backendpluginv1.UsageEvidence
	if e.Usage != nil {
		usage, err = UsageEvidenceToProto(*e.Usage)
		if err != nil {
			return nil, err
		}
	}
	pe, err := PluginErrorToProto(e.Error)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.CanonicalEvent{
		Kind:                      kind,
		MessageIndex:              optInt32(e.MessageIndex),
		Delta:                     optString(e.Delta),
		Signature:                 optString(e.Signature),
		Opaque:                    append([]byte(nil), e.Opaque...),
		ToolCallId:                optString(e.ToolCallID),
		ToolName:                  optString(e.ToolName),
		Usage:                     usage,
		Warning:                   optString(e.Warning),
		Error:                     pe,
		ImageRef:                  optString(e.ImageRef),
		FileRef:                   optString(e.FileRef),
		ReasoningDialect:          optString(e.ReasoningDialect),
		ReasoningOpaque:           append([]byte(nil), e.ReasoningOpaque...),
		ReasoningSummary:          RawJSONToProto(e.ReasoningSummary),
		ReasoningContent:          RawJSONToProto(e.ReasoningContent),
		ReasoningEncryptedContent: RawJSONToProto(e.ReasoningEncryptedContent),
		FinishReason:              optString(e.FinishReason),
		ResponseStatus:            optString(e.ResponseStatus),
	}, nil
}

// ClientFrameFromProto converts host-to-plugin frames.
func ClientFrameFromProto(p *backendpluginv1.ExecuteClientFrame) (ClientFrame, error) {
	if p == nil {
		return ClientFrame{}, ErrInvalidFrame
	}
	kind, err := clientFrameKindFromProto(p.GetKind())
	if err != nil {
		return ClientFrame{}, err
	}
	frame := ClientFrame{
		Kind:                 kind,
		Sequence:             p.GetSequence(),
		InstanceID:           p.GetInstanceId(),
		CancelDeadlineUnixMS: p.GetCancelDeadlineUnixMs(),
	}
	if p.CancelReason != backendpluginv1.CancelReason_CANCEL_REASON_UNSPECIFIED {
		reason, err := cancelReasonFromProto(p.GetCancelReason())
		if err != nil {
			return ClientFrame{}, err
		}
		frame.CancelReason = reason
	}
	if p.Invocation != nil {
		inv, err := InvocationFromProto(p.GetInvocation())
		if err != nil {
			return ClientFrame{}, err
		}
		frame.Invocation = &inv
	}
	if err := frame.ValidateShape(); err != nil {
		return ClientFrame{}, err
	}
	return frame, nil
}

// ClientFrameToProto encodes host-to-plugin frames.
func ClientFrameToProto(f ClientFrame) (*backendpluginv1.ExecuteClientFrame, error) {
	if err := f.ValidateShape(); err != nil {
		return nil, err
	}
	kind, err := clientFrameKindToProto(f.Kind)
	if err != nil {
		return nil, err
	}
	out := &backendpluginv1.ExecuteClientFrame{
		Kind:                 kind,
		Sequence:             f.Sequence,
		InstanceId:           f.InstanceID,
		CancelDeadlineUnixMs: f.CancelDeadlineUnixMS,
	}
	if f.CancelReason != CancelReasonUnspecified {
		reason, err := cancelReasonToProto(f.CancelReason)
		if err != nil {
			return nil, err
		}
		out.CancelReason = reason
	}
	if f.Invocation != nil {
		inv, err := InvocationToProto(*f.Invocation)
		if err != nil {
			return nil, err
		}
		out.Invocation = inv
	}
	return out, nil
}

func accountingEvidenceFromProto(p *backendpluginv1.AccountingEvidence) (*AccountingEvidence, error) {
	if p == nil {
		return nil, nil
	}
	source, err := accountingSourceFromProto(p.GetSource())
	if err != nil {
		return nil, err
	}
	authority, err := accountingAuthorityFromProto(p.GetAuthority())
	if err != nil {
		return nil, err
	}
	plane, err := accountingPlaneFromProto(p.GetPlane())
	if err != nil {
		return nil, err
	}
	evidence := &AccountingEvidence{InputTokens: p.InputTokens, OutputTokens: p.OutputTokens, CacheReadTokens: p.CacheReadTokens, CacheWriteTokens: p.CacheWriteTokens, ReasoningTokens: p.ReasoningTokens, TotalTokens: p.TotalTokens, Presence: UsagePresenceFromProto(p.GetPresence()), Source: source, Authority: authority, Plane: plane, DedupeKey: p.GetDedupeKey(), InputTokensExcludeCacheRead: p.GetInputTokensExcludeCacheRead(), InputTokensExcludeCacheWrite: p.GetInputTokensExcludeCacheWrite()}
	if err := ValidateAccountingEvidence(*evidence); err != nil {
		return nil, err
	}
	return evidence, nil
}

func accountingEvidenceToProto(e *AccountingEvidence) (*backendpluginv1.AccountingEvidence, error) {
	if e == nil {
		return nil, nil
	}
	if err := ValidateAccountingEvidence(*e); err != nil {
		return nil, err
	}
	source, err := accountingSourceToProto(e.Source)
	if err != nil {
		return nil, err
	}
	authority, err := accountingAuthorityToProto(e.Authority)
	if err != nil {
		return nil, err
	}
	plane, err := accountingPlaneToProto(e.Plane)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.AccountingEvidence{InputTokens: e.InputTokens, OutputTokens: e.OutputTokens, CacheReadTokens: e.CacheReadTokens, CacheWriteTokens: e.CacheWriteTokens, ReasoningTokens: e.ReasoningTokens, TotalTokens: e.TotalTokens, Presence: UsagePresenceToProto(e.Presence), Source: source, Authority: authority, Plane: plane, DedupeKey: e.DedupeKey, InputTokensExcludeCacheRead: e.InputTokensExcludeCacheRead, InputTokensExcludeCacheWrite: e.InputTokensExcludeCacheWrite}, nil
}

// RuntimePolicyFromProto converts runtime policy.
func RuntimePolicyFromProto(p *backendpluginv1.RuntimePolicy) RuntimePolicy {
	if p == nil {
		return RuntimePolicy{}
	}
	return RuntimePolicy{
		MaxRequestBytes:         p.GetMaxRequestBytes(),
		MaxStreamFrameBytes:     p.GetMaxStreamFrameBytes(),
		MaxPendingEvents:        p.GetMaxPendingEvents(),
		RequestTimeoutMS:        p.GetRequestTimeoutMs(),
		CancelDeadlineMS:        p.GetCancelDeadlineMs(),
		DiagnosticsVerbosity:    p.GetDiagnosticsVerbosity(),
		MaxConcurrentExecutions: p.GetMaxConcurrentExecutions(),
		LocalOnly:               p.GetLocalOnly(),
		AllowedEnvNames:         append([]string(nil), p.GetAllowedEnvNames()...),
		DisableTransportRetries: p.GetDisableTransportRetries(),
		QuotaSwitchRetryAfterMS: p.GetQuotaSwitchRetryAfterMs(),
	}
}

// RuntimePolicyToProto encodes runtime policy.
func RuntimePolicyToProto(p RuntimePolicy) *backendpluginv1.RuntimePolicy {
	return &backendpluginv1.RuntimePolicy{
		MaxRequestBytes:         p.MaxRequestBytes,
		MaxStreamFrameBytes:     p.MaxStreamFrameBytes,
		MaxPendingEvents:        p.MaxPendingEvents,
		RequestTimeoutMs:        p.RequestTimeoutMS,
		CancelDeadlineMs:        p.CancelDeadlineMS,
		DiagnosticsVerbosity:    p.DiagnosticsVerbosity,
		MaxConcurrentExecutions: p.MaxConcurrentExecutions,
		LocalOnly:               p.LocalOnly,
		AllowedEnvNames:         append([]string(nil), p.AllowedEnvNames...),
		DisableTransportRetries: p.DisableTransportRetries,
		QuotaSwitchRetryAfterMs: p.QuotaSwitchRetryAfterMS,
	}
}

// ConfigureRequestFromProto converts configure wire input.
// Negotiation is domain-only and must be supplied by the host after Negotiate.
func ConfigureRequestFromProto(p *backendpluginv1.ConfigureRequest, neg Negotiation) (ConfigureRequest, error) {
	if p == nil {
		return ConfigureRequest{}, ErrInvalidInvocation
	}
	secrets := SecretBundle{}
	if p.GetSecrets() != nil {
		secrets.Values = make(map[string][]byte, len(p.GetSecrets().GetValues()))
		for k, v := range p.GetSecrets().GetValues() {
			secrets.Values[k] = append([]byte(nil), v...)
		}
	}
	req := ConfigureRequest{
		InstanceID:       p.GetInstanceId(),
		FactoryKind:      p.GetFactoryKind(),
		ConfigYAML:       append([]byte(nil), p.GetConfigYaml()...),
		Secrets:          secrets,
		RuntimePolicy:    RuntimePolicyFromProto(p.GetRuntimePolicy()),
		Negotiation:      neg,
		NegotiationToken: p.GetNegotiationToken(),
	}
	if err := req.Validate(); err != nil {
		return ConfigureRequest{}, err
	}
	return req, nil
}

// ConfigureRequestToProto encodes configure wire fields (Negotiation is not on the wire).
func ConfigureRequestToProto(r ConfigureRequest) *backendpluginv1.ConfigureRequest {
	var secrets *backendpluginv1.SecretBundle
	if len(r.Secrets.Values) > 0 {
		secrets = &backendpluginv1.SecretBundle{Values: make(map[string][]byte, len(r.Secrets.Values))}
		for k, v := range r.Secrets.Values {
			secrets.Values[k] = append([]byte(nil), v...)
		}
	}
	return &backendpluginv1.ConfigureRequest{
		InstanceId:       r.InstanceID,
		FactoryKind:      r.FactoryKind,
		ConfigYaml:       append([]byte(nil), r.ConfigYAML...),
		Secrets:          secrets,
		RuntimePolicy:    RuntimePolicyToProto(r.RuntimePolicy),
		NegotiationToken: r.NegotiationToken,
	}
}

// CountTokensRequestFromProto converts count requests.
func CountTokensRequestFromProto(p *backendpluginv1.CountTokensRequest) (CountTokensRequest, error) {
	if p == nil {
		return CountTokensRequest{}, ErrInvalidInvocation
	}
	inv, err := InvocationFromProto(p.GetInvocation())
	if err != nil {
		return CountTokensRequest{}, err
	}
	return CountTokensRequest{InstanceID: p.GetInstanceId(), ModelID: p.GetModelId(), Invocation: inv}, nil
}

// CountTokensRequestToProto encodes count requests.
func CountTokensRequestToProto(r CountTokensRequest) (*backendpluginv1.CountTokensRequest, error) {
	inv, err := InvocationToProto(r.Invocation)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.CountTokensRequest{InstanceId: r.InstanceID, ModelId: r.ModelID, Invocation: inv}, nil
}

// FinalizeBillingRequestFromProto converts finalize requests.
func FinalizeBillingRequestFromProto(p *backendpluginv1.FinalizeBillingRequest) (FinalizeBillingRequest, error) {
	if p == nil {
		return FinalizeBillingRequest{}, ErrInvalidInvocation
	}
	req := FinalizeBillingRequest{
		InstanceID:     p.GetInstanceId(),
		ALegID:         p.GetALegId(),
		BLegID:         p.GetBLegId(),
		ModelID:        p.GetModelId(),
		Reason:         p.GetReason(),
		IdempotencyKey: p.GetIdempotencyKey(),
	}
	if err := req.Validate(); err != nil {
		return FinalizeBillingRequest{}, err
	}
	return req, nil
}

// FinalizeBillingRequestToProto encodes finalize requests.
func FinalizeBillingRequestToProto(r FinalizeBillingRequest) *backendpluginv1.FinalizeBillingRequest {
	return &backendpluginv1.FinalizeBillingRequest{
		InstanceId:     r.InstanceID,
		ALegId:         r.ALegID,
		BLegId:         r.BLegID,
		ModelId:        r.ModelID,
		Reason:         r.Reason,
		IdempotencyKey: r.IdempotencyKey,
	}
}

// HealthResponseFromProto converts health.
func HealthResponseFromProto(p *backendpluginv1.HealthResponse) HealthResponse {
	if p == nil {
		return HealthResponse{}
	}
	return HealthResponse{Serving: p.GetServing(), Detail: p.GetDetail()}
}

// HealthResponseToProto encodes health.
func HealthResponseToProto(h HealthResponse) *backendpluginv1.HealthResponse {
	return &backendpluginv1.HealthResponse{Serving: h.Serving, Detail: h.Detail}
}

// GracefulShutdownFromProto converts shutdown request/response.
func GracefulShutdownRequestFromProto(p *backendpluginv1.GracefulShutdownRequest) GracefulShutdownRequest {
	if p == nil {
		return GracefulShutdownRequest{}
	}
	return GracefulShutdownRequest{DrainTimeoutMS: p.GetDrainTimeoutMs()}
}

// GracefulShutdownRequestToProto encodes shutdown request.
func GracefulShutdownRequestToProto(r GracefulShutdownRequest) *backendpluginv1.GracefulShutdownRequest {
	return &backendpluginv1.GracefulShutdownRequest{DrainTimeoutMs: r.DrainTimeoutMS}
}

// GracefulShutdownResponseFromProto converts shutdown response.
func GracefulShutdownResponseFromProto(p *backendpluginv1.GracefulShutdownResponse) GracefulShutdownResponse {
	if p == nil {
		return GracefulShutdownResponse{}
	}
	return GracefulShutdownResponse{Accepted: p.GetAccepted()}
}

// GracefulShutdownResponseToProto encodes shutdown response.
func GracefulShutdownResponseToProto(r GracefulShutdownResponse) *backendpluginv1.GracefulShutdownResponse {
	return &backendpluginv1.GracefulShutdownResponse{Accepted: r.Accepted}
}
