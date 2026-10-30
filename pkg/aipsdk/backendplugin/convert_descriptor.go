package backendplugin

import (
	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"google.golang.org/protobuf/proto"
)

func factoryFromProto(p *backendpluginv1.FactoryDescriptor) (FactoryDescriptor, error) {
	if p == nil {
		return FactoryDescriptor{}, ErrInvalidDescriptor
	}
	cm, err := credentialModeFromProto(p.GetCredentialMode())
	if err != nil {
		return FactoryDescriptor{}, err
	}
	as, err := accessScopeFromProto(p.GetAccessScope())
	if err != nil {
		return FactoryDescriptor{}, err
	}
	ps, err := processSharingFromProto(p.GetProcessSharing())
	if err != nil {
		return FactoryDescriptor{}, err
	}
	return FactoryDescriptor{
		Kind:                         p.GetKind(),
		DisplayName:                  p.GetDisplayName(),
		Description:                  p.GetDescription(),
		CredentialMode:               cm,
		AccessScope:                  as,
		RoutePrefixes:                append([]string(nil), p.GetRoutePrefixes()...),
		SupportsCountTokens:          p.GetSupportsCountTokens(),
		SupportsFinalizeBilling:      p.GetSupportsFinalizeBilling(),
		SupportsDynamicInventory:     p.GetSupportsDynamicInventory(),
		SupportsModelAwareProfile:    p.GetSupportsModelAwareProfile(),
		ProcessSharing:               ps,
		Experimental:                 p.GetExperimental(),
		Deprecated:                   p.GetDeprecated(),
		StaticCapabilities:           capabilityFromProto(p.GetStaticCapabilities()),
		TransportCapabilities:        transportCapabilityFromProto(p.GetTransportCapabilities()),
		SupportsAccountingEvidenceV2: p.GetSupportsAccountingEvidenceV2(),
	}, nil
}

func factoryToProto(f FactoryDescriptor) (*backendpluginv1.FactoryDescriptor, error) {
	cm, err := credentialModeToProto(f.CredentialMode)
	if err != nil {
		return nil, err
	}
	as, err := accessScopeToProto(f.AccessScope)
	if err != nil {
		return nil, err
	}
	ps, err := processSharingToProto(f.ProcessSharing)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.FactoryDescriptor{
		Kind:                         f.Kind,
		DisplayName:                  f.DisplayName,
		Description:                  f.Description,
		CredentialMode:               cm,
		AccessScope:                  as,
		RoutePrefixes:                append([]string(nil), f.RoutePrefixes...),
		SupportsCountTokens:          f.SupportsCountTokens,
		SupportsFinalizeBilling:      f.SupportsFinalizeBilling,
		SupportsDynamicInventory:     f.SupportsDynamicInventory,
		SupportsModelAwareProfile:    f.SupportsModelAwareProfile,
		ProcessSharing:               ps,
		Experimental:                 f.Experimental,
		Deprecated:                   f.Deprecated,
		StaticCapabilities:           capabilityToProto(f.StaticCapabilities),
		TransportCapabilities:        transportCapabilityToProto(f.TransportCapabilities),
		SupportsAccountingEvidenceV2: f.SupportsAccountingEvidenceV2,
	}, nil
}

// PluginDescriptorFromProto converts a wire descriptor.
func PluginDescriptorFromProto(p *backendpluginv1.PluginDescriptor) (PluginDescriptor, error) {
	if p == nil {
		return PluginDescriptor{}, ErrInvalidDescriptor
	}
	d := PluginDescriptor{
		ProtocolMajor: p.GetProtocolMajor(),
		ProtocolMinor: p.GetProtocolMinor(),
		PluginID:      p.GetPluginId(),
		Version:       p.GetVersion(),
		BuildID:       p.GetBuildId(),
		Features:      featuresFromProto(p.GetFeatures()),
	}
	for _, f := range p.GetFactories() {
		fd, err := factoryFromProto(f)
		if err != nil {
			return PluginDescriptor{}, err
		}
		d.Factories = append(d.Factories, fd)
	}
	if err := d.Validate(); err != nil {
		return PluginDescriptor{}, err
	}
	return d, nil
}

// PluginDescriptorToProto encodes a descriptor.
func PluginDescriptorToProto(d PluginDescriptor) (*backendpluginv1.PluginDescriptor, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	out := &backendpluginv1.PluginDescriptor{
		ProtocolMajor: d.ProtocolMajor,
		ProtocolMinor: d.ProtocolMinor,
		PluginId:      d.PluginID,
		Version:       d.Version,
		BuildId:       d.BuildID,
		Features:      featuresToProto(d.Features),
	}
	for _, f := range d.Factories {
		fp, err := factoryToProto(f)
		if err != nil {
			return nil, err
		}
		out.Factories = append(out.Factories, fp)
	}
	return out, nil
}

// ResolvedProfileFromProto converts a resolved profile.
func ResolvedProfileFromProto(p *backendpluginv1.ResolvedProfile) (ResolvedProfile, error) {
	if p == nil {
		return ResolvedProfile{}, ErrInvalidInvocation
	}
	promptCacheProfile, err := promptCacheProfileFromProto(p.GetPromptCacheProfile())
	if err != nil {
		return ResolvedProfile{}, err
	}
	effortValues, err := reasoningEffortValuesFromProto(p.GetReasoningEffortValues())
	if err != nil {
		return ResolvedProfile{}, err
	}
	return ResolvedProfile{
		Capabilities:                 capabilityFromProto(p.GetCapabilities()),
		TransportCapabilities:        transportCapabilityFromProto(p.GetTransportCapabilities()),
		DialectSupport:               dialectSupportFromProto(p.GetDialectSupport()),
		ReasoningReplaySupported:     p.GetReasoningReplaySupported(),
		RoutePrefixes:                append([]string(nil), p.GetRoutePrefixes()...),
		EnforceMaxOutput:             p.GetEnforceMaxOutput(),
		MaxOutputTokens:              optUint32(p.MaxOutputTokens),
		SupportsCountTokens:          p.GetSupportsCountTokens(),
		SupportsFinalizeBilling:      p.GetSupportsFinalizeBilling(),
		SupportsDynamicInventory:     p.GetSupportsDynamicInventory(),
		EvidenceSource:               p.GetEvidenceSource(),
		ProfileVersion:               p.GetProfileVersion(),
		PromptCacheProfile:           promptCacheProfile,
		SupportsAccountingEvidenceV2: p.GetSupportsAccountingEvidenceV2(),
		ReasoningEffortValues:        effortValues,
	}, nil
}

// ResolvedProfileToProto encodes a resolved profile, or nil when the profile
// cannot be represented on the wire. Callers that must report that failure use
// resolvedProfileToProto.
func ResolvedProfileToProto(p ResolvedProfile) *backendpluginv1.ResolvedProfile {
	out, _ := resolvedProfileToProto(p)
	return out
}

func resolvedProfileToProto(p ResolvedProfile) (*backendpluginv1.ResolvedProfile, error) {
	promptCacheProfile, err := promptCacheProfileToProto(p.PromptCacheProfile)
	if err != nil {
		return nil, err
	}
	// An invalid effort declaration is never sent: the profile cannot be
	// represented, so the server reports it instead of silently downgrading a
	// corrupt fact to Unknown.
	var effortValues *backendpluginv1.ReasoningEffortValuesV1
	if v, err := reasoningEffortValuesToProto(p.ReasoningEffortValues); err == nil {
		effortValues = v
	} else {
		return nil, err
	}
	return &backendpluginv1.ResolvedProfile{
		Capabilities:                 capabilityToProto(p.Capabilities),
		TransportCapabilities:        transportCapabilityToProto(p.TransportCapabilities),
		DialectSupport:               dialectSupportToProto(p.DialectSupport),
		ReasoningReplaySupported:     p.ReasoningReplaySupported,
		RoutePrefixes:                append([]string(nil), p.RoutePrefixes...),
		EnforceMaxOutput:             p.EnforceMaxOutput,
		MaxOutputTokens:              optUint32(p.MaxOutputTokens),
		SupportsCountTokens:          p.SupportsCountTokens,
		SupportsFinalizeBilling:      p.SupportsFinalizeBilling,
		SupportsDynamicInventory:     p.SupportsDynamicInventory,
		EvidenceSource:               p.EvidenceSource,
		ProfileVersion:               p.ProfileVersion,
		PromptCacheProfile:           promptCacheProfile,
		SupportsAccountingEvidenceV2: p.SupportsAccountingEvidenceV2,
		ReasoningEffortValues:        effortValues,
	}, nil
}

func reasoningEffortValuesFromProto(p *backendpluginv1.ReasoningEffortValuesV1) (*ReasoningEffortValuesV1, error) {
	if p == nil {
		return nil, nil
	}
	out := &ReasoningEffortValuesV1{
		ModelID:    p.GetModelId(),
		Supported:  append([]string(nil), p.GetSupported()...),
		Exhaustive: p.GetExhaustive(),
	}
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

func reasoningEffortValuesToProto(v *ReasoningEffortValuesV1) (*backendpluginv1.ReasoningEffortValuesV1, error) {
	if v == nil {
		return nil, nil
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	return &backendpluginv1.ReasoningEffortValuesV1{
		ModelId:    v.ModelID,
		Supported:  append([]string(nil), v.Supported...),
		Exhaustive: v.Exhaustive,
	}, nil
}

// ListModelsResponseFromProto converts inventory.
func ListModelsResponseFromProto(p *backendpluginv1.ListModelsResponse) (ListModelsResponse, error) {
	if p == nil {
		return ListModelsResponse{}, nil
	}
	out := ListModelsResponse{
		InventorySource:    p.GetInventorySource(),
		FetchedUnixMS:      p.GetFetchedUnixMs(),
		RefreshAfterUnixMS: optInt64(p.RefreshAfterUnixMs),
		ErrorCode:          p.GetErrorCode(),
	}
	for _, m := range p.GetModels() {
		if m == nil {
			continue
		}
		out.Models = append(out.Models, ModelDescriptor{
			CanonicalModelID: m.GetCanonicalModelId(),
			NativeModelID:    m.GetNativeModelId(),
			DisplayName:      m.GetDisplayName(),
			RoutePrefix:      m.GetRoutePrefix(),
			FactoryKind:      m.GetFactoryKind(),
			IsDefault:        m.GetIsDefault(),
			Capabilities:     capabilityFromProto(m.GetCapabilities()),
		})
	}
	if err := out.Validate(DefaultMaxModelsPerResponse); err != nil {
		return ListModelsResponse{}, err
	}
	return out, nil
}

// ListModelsResponseToProto encodes inventory.
func ListModelsResponseToProto(r ListModelsResponse) *backendpluginv1.ListModelsResponse {
	out := &backendpluginv1.ListModelsResponse{
		InventorySource:    r.InventorySource,
		FetchedUnixMs:      r.FetchedUnixMS,
		RefreshAfterUnixMs: optInt64(r.RefreshAfterUnixMS),
		ErrorCode:          r.ErrorCode,
	}
	for _, m := range r.Models {
		out.Models = append(out.Models, &backendpluginv1.ModelDescriptor{
			CanonicalModelId: m.CanonicalModelID,
			NativeModelId:    m.NativeModelID,
			DisplayName:      m.DisplayName,
			RoutePrefix:      m.RoutePrefix,
			FactoryKind:      m.FactoryKind,
			IsDefault:        m.IsDefault,
			Capabilities:     capabilityToProto(m.Capabilities),
		})
	}
	return out
}

// CountTokensResponseFromProto converts count results.
func CountTokensResponseFromProto(p *backendpluginv1.CountTokensResponse) (CountTokensResponse, error) {
	if p == nil {
		return CountTokensResponse{}, nil
	}
	return CountTokensResponse{
		InputTokens:     optInt64(p.InputTokens),
		Presence:        UsagePresenceFromProto(p.GetPresence()),
		EvidenceQuality: p.GetEvidenceQuality(),
	}, nil
}

// CountTokensResponseToProto encodes count results.
func CountTokensResponseToProto(r CountTokensResponse) *backendpluginv1.CountTokensResponse {
	return &backendpluginv1.CountTokensResponse{
		InputTokens:     optInt64(r.InputTokens),
		Presence:        UsagePresenceToProto(r.Presence),
		EvidenceQuality: r.EvidenceQuality,
	}
}

// FinalizeBillingResponseFromProto converts finalize results.
func FinalizeBillingResponseFromProto(p *backendpluginv1.FinalizeBillingResponse) (FinalizeBillingResponse, error) {
	if p == nil {
		return FinalizeBillingResponse{}, nil
	}
	if uint64(proto.Size(p)) > DefaultMaxMessageBytes || len(p.GetAccountingEvidenceV2()) > int(DefaultMaxEconomicEvidencePerAttempt) {
		return FinalizeBillingResponse{}, ErrOversizedMessage
	}
	usage, err := UsageEvidenceFromProto(p.GetUsage())
	if err != nil {
		return FinalizeBillingResponse{}, err
	}
	out := FinalizeBillingResponse{Usage: usage, EvidenceQuality: p.GetEvidenceQuality()}
	for _, wire := range p.GetAccountingEvidenceV2() {
		evidence, convErr := AccountingEvidenceV2FromProto(wire)
		if convErr != nil {
			return FinalizeBillingResponse{}, convErr
		}
		out.AccountingV2 = append(out.AccountingV2, evidence)
	}
	return out, nil
}

// FinalizeBillingResponseToProto encodes finalize results.
func FinalizeBillingResponseToProto(r FinalizeBillingResponse) (*backendpluginv1.FinalizeBillingResponse, error) {
	if len(r.AccountingV2) > int(DefaultMaxEconomicEvidencePerAttempt) {
		return nil, ErrOversizedMessage
	}
	usage, err := UsageEvidenceToProto(r.Usage)
	if err != nil {
		return nil, err
	}
	out := &backendpluginv1.FinalizeBillingResponse{Usage: usage, EvidenceQuality: r.EvidenceQuality}
	for i := range r.AccountingV2 {
		evidence, convErr := AccountingEvidenceV2ToProto(&r.AccountingV2[i])
		if convErr != nil {
			return nil, convErr
		}
		out.AccountingEvidenceV2 = append(out.AccountingEvidenceV2, evidence)
	}
	if uint64(proto.Size(out)) > DefaultMaxMessageBytes {
		return nil, ErrOversizedMessage
	}
	return out, nil
}

// CancelOutcomeFromProto converts cancel outcomes.
func CancelOutcomeFromProto(p *backendpluginv1.CancelOutcome) (*CancelOutcome, error) {
	if p == nil {
		return nil, nil
	}
	reason, err := cancelReasonFromProto(p.GetReason())
	if err != nil {
		return nil, err
	}
	var mode CancelMode
	if p.GetMode() != backendpluginv1.CancelMode_CANCEL_MODE_UNSPECIFIED {
		var modeErr error
		mode, modeErr = cancelModeFromProto(p.GetMode())
		if modeErr != nil {
			return nil, modeErr
		}
	}
	return &CancelOutcome{Acknowledged: p.GetAcknowledged(), Detail: p.GetDetail(), Reason: reason, Mode: mode}, nil
}

// CancelOutcomeToProto encodes cancel outcomes.
func CancelOutcomeToProto(c *CancelOutcome) (*backendpluginv1.CancelOutcome, error) {
	if c == nil {
		return nil, nil
	}
	reason, err := cancelReasonToProto(c.Reason)
	if err != nil {
		return nil, err
	}
	var mode backendpluginv1.CancelMode
	if c.Mode != "" && c.Mode != CancelModeUnspecified {
		var modeErr error
		mode, modeErr = cancelModeToProto(c.Mode)
		if modeErr != nil {
			return nil, modeErr
		}
	}
	return &backendpluginv1.CancelOutcome{Acknowledged: c.Acknowledged, Detail: c.Detail, Reason: reason, Mode: mode}, nil
}
