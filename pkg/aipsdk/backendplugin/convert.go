package backendplugin

import (
	"fmt"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
)

func ProtocolOfferToNegotiateRequest(o ProtocolOffer) (*backendpluginv1.NegotiateRequest, error) {
	if o.Major == 0 {
		return nil, ErrUnknownEnum
	}
	if _, err := indexFeatures(o.Features); err != nil {
		return nil, err
	}
	return &backendpluginv1.NegotiateRequest{
		HostMajor:               o.Major,
		HostMinor:               o.Minor,
		HostFeatures:            featuresToProto(o.Features),
		DisableTransportRetries: o.DisableTransportRetries,
	}, nil
}

// ProtocolOfferFromNegotiateRequest converts a wire negotiate request into a host offer.
func ProtocolOfferFromNegotiateRequest(p *backendpluginv1.NegotiateRequest) (ProtocolOffer, error) {
	if p == nil {
		return ProtocolOffer{}, ErrInvalidInvocation
	}
	if p.GetHostMajor() == 0 {
		return ProtocolOffer{}, ErrUnknownEnum
	}
	feats := featuresFromProto(p.GetHostFeatures())
	if _, err := indexFeatures(feats); err != nil {
		return ProtocolOffer{}, err
	}
	return ProtocolOffer{
		Major:                   p.GetHostMajor(),
		Minor:                   p.GetHostMinor(),
		Features:                feats,
		DisableTransportRetries: p.GetDisableTransportRetries(),
	}, nil
}

// NegotiationToNegotiateResponse encodes a negotiation outcome.
func NegotiationToNegotiateResponse(n Negotiation) (*backendpluginv1.NegotiateResponse, error) {
	if n.Compatible && n.PluginMajor == 0 {
		return nil, ErrUnknownEnum
	}
	if _, err := indexFeatures(n.PluginFeatures); err != nil {
		return nil, err
	}
	return &backendpluginv1.NegotiateResponse{
		PluginMajor:             n.PluginMajor,
		PluginMinor:             n.PluginMinor,
		PluginFeatures:          featuresToProto(n.PluginFeatures),
		NegotiatedMinor:         n.NegotiatedMinor,
		EnabledFeatures:         append([]string(nil), n.EnabledFeatures...),
		Compatible:              n.Compatible,
		RejectReason:            n.RejectReason,
		DisableTransportRetries: n.TransportPolicy.DisableAutomaticRetries,
		NegotiationToken:        n.NegotiationToken,
	}, nil
}

// NegotiationFromNegotiateResponse converts a wire negotiate response.
func NegotiationFromNegotiateResponse(p *backendpluginv1.NegotiateResponse) (Negotiation, error) {
	if p == nil {
		return Negotiation{}, ErrInvalidInvocation
	}
	if p.GetCompatible() && p.GetPluginMajor() == 0 {
		return Negotiation{}, ErrUnknownEnum
	}
	feats := featuresFromProto(p.GetPluginFeatures())
	if _, err := indexFeatures(feats); err != nil {
		return Negotiation{}, err
	}
	policy := DefaultTransportPolicy()
	policy.DisableAutomaticRetries = p.GetDisableTransportRetries()
	if p.GetCompatible() {
		if err := policy.Validate(); err != nil {
			return Negotiation{}, err
		}
	}
	return Negotiation{
		Compatible:       p.GetCompatible(),
		NegotiatedMinor:  p.GetNegotiatedMinor(),
		EnabledFeatures:  append([]string(nil), p.GetEnabledFeatures()...),
		RejectReason:     p.GetRejectReason(),
		TransportPolicy:  policy,
		PluginMajor:      p.GetPluginMajor(),
		PluginMinor:      p.GetPluginMinor(),
		PluginFeatures:   feats,
		NegotiationToken: p.GetNegotiationToken(),
	}, nil
}

// RawJSONFromProto converts a wire RawJSONValue. Nil means absent.
// Unset oneof and is_null=false fail closed.
func RawJSONFromProto(v *backendpluginv1.RawJSONValue) (RawJSON, error) {
	if v == nil {
		return RawJSONAbsentValue(), nil
	}
	switch x := v.State.(type) {
	case *backendpluginv1.RawJSONValue_IsNull:
		if !x.IsNull {
			return RawJSON{}, fmt.Errorf("%w: is_null=false", ErrInvalidRawJSON)
		}
		return RawJSONNullValue(), nil
	case *backendpluginv1.RawJSONValue_Json:
		return RawJSONFromBytes(x.Json), nil
	case nil:
		return RawJSON{}, fmt.Errorf("%w: unset oneof", ErrInvalidRawJSON)
	default:
		return RawJSON{}, fmt.Errorf("%w: unknown oneof", ErrInvalidRawJSON)
	}
}

// RawJSONToProto encodes RawJSON. Absent encodes as nil.
func RawJSONToProto(r RawJSON) *backendpluginv1.RawJSONValue {
	switch r.State() {
	case RawJSONNull:
		return &backendpluginv1.RawJSONValue{State: &backendpluginv1.RawJSONValue_IsNull{IsNull: true}}
	case RawJSONValue:
		return &backendpluginv1.RawJSONValue{State: &backendpluginv1.RawJSONValue_Json{Json: r.Bytes()}}
	default:
		return nil
	}
}

// UsagePresenceFromProto converts usage presence flags.
func UsagePresenceFromProto(p *backendpluginv1.UsagePresence) UsagePresence {
	if p == nil {
		return UsagePresence{}
	}
	return UsagePresence{
		InputTokens:      p.GetInputTokens(),
		OutputTokens:     p.GetOutputTokens(),
		CacheReadTokens:  p.GetCacheReadTokens(),
		CacheWriteTokens: p.GetCacheWriteTokens(),
		ReasoningTokens:  p.GetReasoningTokens(),
		TotalTokens:      p.GetTotalTokens(),
	}
}

// UsagePresenceToProto encodes usage presence flags.
func UsagePresenceToProto(p UsagePresence) *backendpluginv1.UsagePresence {
	return &backendpluginv1.UsagePresence{
		InputTokens:      p.InputTokens,
		OutputTokens:     p.OutputTokens,
		CacheReadTokens:  p.CacheReadTokens,
		CacheWriteTokens: p.CacheWriteTokens,
		ReasoningTokens:  p.ReasoningTokens,
		TotalTokens:      p.TotalTokens,
	}
}

func optInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func optUint32(v *uint32) *uint32 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func optInt32(v *int32) *int32 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func optBool(v *bool) *bool {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func optString(v *string) *string {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func capabilityFromProto(p *backendpluginv1.CapabilitySummary) CapabilitySummary {
	if p == nil {
		return CapabilitySummary{}
	}
	return CapabilitySummary{
		Streaming:          p.GetStreaming(),
		Tools:              p.GetTools(),
		Vision:             p.GetVision(),
		Documents:          p.GetDocuments(),
		StructuredOutputs:  p.GetStructuredOutputs(),
		Reasoning:          p.GetReasoning(),
		ReasoningReplay:    p.GetReasoningReplay(),
		ParallelToolCalls:  p.GetParallelToolCalls(),
		OrderedItems:       p.GetOrderedItems(),
		ItemReferences:     p.GetItemReferences(),
		Compaction:         p.GetCompaction(),
		AssistantPhase:     p.GetAssistantPhase(),
		OpaqueExtensions:   p.GetOpaqueExtensions(),
		VideoInput:         p.GetVideoInput(),
		Annotations:        p.GetAnnotations(),
		AssistantMediaRefs: p.GetAssistantMediaRefs(),
	}
}

func capabilityToProto(c CapabilitySummary) *backendpluginv1.CapabilitySummary {
	return &backendpluginv1.CapabilitySummary{
		Streaming:          c.Streaming,
		Tools:              c.Tools,
		Vision:             c.Vision,
		Documents:          c.Documents,
		StructuredOutputs:  c.StructuredOutputs,
		Reasoning:          c.Reasoning,
		ReasoningReplay:    c.ReasoningReplay,
		ParallelToolCalls:  c.ParallelToolCalls,
		OrderedItems:       c.OrderedItems,
		ItemReferences:     c.ItemReferences,
		Compaction:         c.Compaction,
		AssistantPhase:     c.AssistantPhase,
		OpaqueExtensions:   c.OpaqueExtensions,
		VideoInput:         c.VideoInput,
		Annotations:        c.Annotations,
		AssistantMediaRefs: c.AssistantMediaRefs,
	}
}

func dialectSupportFromProto(p *backendpluginv1.DialectSupportWire) DialectSupport {
	if p == nil {
		return DialectSupport{}
	}
	out := DialectSupport{}
	for _, d := range p.GetItemDialects() {
		out.ItemDialects = append(out.ItemDialects, dialectRequirementFromProto(d))
	}
	for _, d := range p.GetReasoningDialects() {
		out.ReasoningDialects = append(out.ReasoningDialects, dialectRequirementFromProto(d))
	}
	for _, d := range p.GetCompactionDialects() {
		out.CompactionDialects = append(out.CompactionDialects, dialectRequirementFromProto(d))
	}
	for _, e := range p.GetExtensionTypes() {
		out.ExtensionTypes = append(out.ExtensionTypes, extensionRequirementFromProto(e))
	}
	return out
}

func dialectSupportToProto(d DialectSupport) *backendpluginv1.DialectSupportWire {
	out := &backendpluginv1.DialectSupportWire{}
	for _, d := range d.ItemDialects {
		out.ItemDialects = append(out.ItemDialects, dialectRequirementToProto(d))
	}
	for _, d := range d.ReasoningDialects {
		out.ReasoningDialects = append(out.ReasoningDialects, dialectRequirementToProto(d))
	}
	for _, d := range d.CompactionDialects {
		out.CompactionDialects = append(out.CompactionDialects, dialectRequirementToProto(d))
	}
	for _, e := range d.ExtensionTypes {
		out.ExtensionTypes = append(out.ExtensionTypes, extensionRequirementToProto(e))
	}
	return out
}

func transportCapabilityFromProto(p *backendpluginv1.TransportCapabilitySummary) TransportCapabilitySummary {
	if p == nil {
		return TransportCapabilitySummary{}
	}
	return TransportCapabilitySummary{
		Keepalive:           p.GetKeepalive(),
		Cancellation:        p.GetCancellation(),
		BidirectionalStream: p.GetBidirectionalStream(),
	}
}

func transportCapabilityToProto(c TransportCapabilitySummary) *backendpluginv1.TransportCapabilitySummary {
	return &backendpluginv1.TransportCapabilitySummary{
		Keepalive:           c.Keepalive,
		Cancellation:        c.Cancellation,
		BidirectionalStream: c.BidirectionalStream,
	}
}

func featuresFromProto(in []*backendpluginv1.Feature) []Feature {
	if len(in) == 0 {
		return nil
	}
	out := make([]Feature, len(in))
	for i, f := range in {
		if f == nil {
			continue
		}
		out[i] = Feature{Name: f.GetName(), Required: f.GetRequired()}
	}
	return out
}

func featuresToProto(in []Feature) []*backendpluginv1.Feature {
	if len(in) == 0 {
		return nil
	}
	out := make([]*backendpluginv1.Feature, len(in))
	for i, f := range in {
		out[i] = &backendpluginv1.Feature{Name: f.Name, Required: f.Required}
	}
	return out
}

// UsageEvidenceFromProto converts wire usage evidence.
func UsageEvidenceFromProto(p *backendpluginv1.UsageEvidence) (UsageEvidence, error) {
	if p == nil {
		return UsageEvidence{RawUsageJSON: RawJSONAbsentValue()}, nil
	}
	raw, err := RawJSONFromProto(p.GetRawUsageJson())
	if err != nil {
		return UsageEvidence{}, err
	}
	u := UsageEvidence{
		InputTokens:                  optInt64(p.InputTokens),
		OutputTokens:                 optInt64(p.OutputTokens),
		CacheReadTokens:              optInt64(p.CacheReadTokens),
		CacheWriteTokens:             optInt64(p.CacheWriteTokens),
		ReasoningTokens:              optInt64(p.ReasoningTokens),
		TotalTokens:                  optInt64(p.TotalTokens),
		Presence:                     UsagePresenceFromProto(p.GetPresence()),
		RawUsageJSON:                 raw,
		InputTokensExcludeCacheRead:  p.GetInputTokensExcludeCacheRead(),
		InputTokensExcludeCacheWrite: p.GetInputTokensExcludeCacheWrite(),
	}
	if err := u.ValidatePresence(); err != nil {
		return UsageEvidence{}, err
	}
	return u, nil
}

// UsageEvidenceToProto encodes usage evidence.
func UsageEvidenceToProto(u UsageEvidence) (*backendpluginv1.UsageEvidence, error) {
	if err := u.ValidatePresence(); err != nil {
		return nil, err
	}
	return &backendpluginv1.UsageEvidence{
		InputTokens:                  optInt64(u.InputTokens),
		OutputTokens:                 optInt64(u.OutputTokens),
		CacheReadTokens:              optInt64(u.CacheReadTokens),
		CacheWriteTokens:             optInt64(u.CacheWriteTokens),
		ReasoningTokens:              optInt64(u.ReasoningTokens),
		TotalTokens:                  optInt64(u.TotalTokens),
		Presence:                     UsagePresenceToProto(u.Presence),
		RawUsageJson:                 RawJSONToProto(u.RawUsageJSON),
		InputTokensExcludeCacheRead:  u.InputTokensExcludeCacheRead,
		InputTokensExcludeCacheWrite: u.InputTokensExcludeCacheWrite,
	}, nil
}

// PluginErrorFromProto converts a classified error.
func PluginErrorFromProto(p *backendpluginv1.PluginError) (*PluginError, error) {
	if p == nil {
		return nil, nil
	}
	code, err := errorCodeFromProto(p.GetCode())
	if err != nil {
		return nil, err
	}
	return &PluginError{
		Code:            code,
		Message:         p.GetMessage(),
		Retryable:       p.GetRetryable(),
		OutputCommitted: p.GetOutputCommitted(),
	}, nil
}

// PluginErrorToProto encodes a classified error.
func PluginErrorToProto(e *PluginError) (*backendpluginv1.PluginError, error) {
	if e == nil {
		return nil, nil
	}
	code, err := errorCodeToProto(e.Code)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.PluginError{
		Code:            code,
		Message:         e.Message,
		Retryable:       e.Retryable,
		OutputCommitted: e.OutputCommitted,
	}, nil
}
