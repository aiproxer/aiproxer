package service

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/appserver"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/codex"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type Service struct {
	Starter acp.ProcessStarter
}

func containsFeature(features []string, want string) bool {
	return slices.Contains(features, want)
}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	httpCaps := backendplugin.CapabilitySummary{
		Streaming: true, Tools: true, Vision: true, Documents: true,
		ParallelToolCalls: true, Reasoning: true, ReasoningReplay: true,
	}
	appCaps := backendplugin.CapabilitySummary{
		Streaming: true, Tools: true, Vision: true, Reasoning: true,
	}
	transport := backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true}
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake,
		PluginID: PluginID, Version: "0.1.0", BuildID: "localdev",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureExactReasoningParts, Required: true},
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureAccountingEvidence, Required: true},
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind: FactoryKindHTTP, DisplayName: "OpenAI Codex", Description: "OpenAI Codex Responses backend",
			CredentialMode: backendplugin.CredentialModeStatic, AccessScope: backendplugin.AccessScopeLocalOnly,
			RoutePrefixes: []string{FactoryKindHTTP}, SupportsDynamicInventory: true,
			ProcessSharing: backendplugin.ProcessSharingPerInstance, StaticCapabilities: httpCaps,
			TransportCapabilities: transport,
		}, {
			Kind: FactoryKindAppServer, DisplayName: "Codex App Server", Description: "Codex CLI app-server backend",
			CredentialMode: backendplugin.CredentialModeNone, AccessScope: backendplugin.AccessScopeLocalOnly,
			RoutePrefixes: []string{FactoryKindAppServer}, SupportsDynamicInventory: true,
			ProcessSharing: backendplugin.ProcessSharingPerInstance, StaticCapabilities: appCaps,
			TransportCapabilities: transport,
		}},
	}, nil
}

func (s *Service) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	kind := req.FactoryKind
	if kind == "" {
		kind = FactoryKindHTTP
	}
	cfg, err := ParseConfigYAML(kind, req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	cat, src, err := catalog.Load(ctx, cfg.catalogLoadOptions())
	if err != nil {
		return nil, fmt.Errorf("codex connector: catalog: %w", err)
	}
	switch kind {
	case FactoryKindHTTP:
		// static_nonrotatable/no_rotation binds exactly one host-delivered
		// api_token slot: no YAML token material, no connector-side
		// environment or auth.json fallback, no quota pooling.
		slots, err := openaicompat.DecodeAPITokenSlotSecrets(req.Secrets)
		if err != nil {
			return nil, fmt.Errorf("codex connector: %w (want %s via host Configure secrets)", err, EnvAccessTokenRoot)
		}
		if len(slots) != 1 {
			names := make([]string, 0, len(slots))
			for _, slot := range slots {
				names = append(names, slot.Name)
			}
			return nil, fmt.Errorf("codex connector: no_rotation binds exactly one api_token slot for %s (got %d: %s); supply only %s via host Configure secrets",
				EnvAccessTokenRoot, len(slots), strings.Join(names, ", "), EnvAccessTokenRoot)
		}
		pc, err := cfg.toCodexHTTP(cat, slots[0].Secret)
		if err != nil {
			return nil, err
		}
		pc.DisableNativeCompactionWithoutAccounting = !containsFeature(req.Negotiation.EnabledFeatures, backendplugin.FeatureAccountingEvidence)
		eng, err := codex.New(pc)
		if err != nil {
			return nil, err
		}
		return &instance{kind: kind, http: eng, catalogSrc: src}, nil
	case FactoryKindAppServer:
		// Credential-free export (manifest mode none): Configure secrets
		// are never consumed; the CLI subprocess owns its own credentials.
		cache := &acp.ExecutableCache{}
		pc, err := cfg.toAppServer(cat, src, cache)
		if err != nil {
			return nil, err
		}
		var eng *appserver.Engine
		if s != nil && s.Starter != nil {
			eng = appserver.NewWithStarter(pc, s.Starter)
		} else {
			eng, err = appserver.New(pc)
			if err != nil {
				return nil, err
			}
		}
		if tracking, ok := eng.Inventory().(interface {
			AcceptInventory([]modelinventory.Model)
			LoadModels(context.Context) (modelinventory.Snapshot, error)
		}); ok {
			snap, loadErr := tracking.LoadModels(context.Background())
			if loadErr == nil && len(snap.Models) > 0 {
				tracking.AcceptInventory(snap.Models)
			}
		}
		return &instance{kind: kind, app: eng, catalogSrc: src, exeCache: cache}, nil
	default:
		return nil, fmt.Errorf("codex connector: unexpected factory kind %q", kind)
	}
}

type instance struct {
	kind       string
	http       *codex.Engine
	app        *appserver.Engine
	catalogSrc catalog.Source
	exeCache   *acp.ExecutableCache
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	var caps backendplugin.CapabilitySummary
	if i.kind == FactoryKindHTTP {
		caps = backendplugin.CapabilitySummary{
			Streaming: true, Tools: true, Vision: true, Documents: true,
			ParallelToolCalls: true, Reasoning: true, ReasoningReplay: true, Compaction: true,
		}
	} else {
		caps = backendplugin.CapabilitySummary{Streaming: true, Tools: true, Vision: true, Reasoning: true}
	}
	profile := backendplugin.ResolvedProfile{
		Capabilities:             caps,
		ReasoningReplaySupported: i.kind == FactoryKindHTTP,
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{i.kind},
		EvidenceSource:           i.kind,
		ProfileVersion:           "1",
	}
	if i.kind == FactoryKindHTTP {
		profile.DialectSupport.CompactionDialects = []backendplugin.DialectRequirementDTO{{Dialect: "codex.responses.compaction.v2", Implementor: "openai-codex"}}
	}
	return profile, nil
}

func (i *instance) Close(context.Context) error {
	if i.app != nil {
		return i.app.Close()
	}
	if i.http != nil {
		return i.http.Close()
	}
	return nil
}
