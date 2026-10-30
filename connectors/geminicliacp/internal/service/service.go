package service

import (
	"context"
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/geminicliacp/internal/product"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type Service struct {
	Starter acp.ProcessStarter
}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake,
		PluginID: "com.aiproxer.backend.geminicliacp", Version: "0.1.0", BuildID: "localdev",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind: FactoryKind, DisplayName: "Gemini CLI ACP",
			Description:              "External Gemini CLI ACP connector",
			CredentialMode:           backendplugin.CredentialModeNone,
			AccessScope:              backendplugin.AccessScopeLocalOnly,
			RoutePrefixes:            []string{FactoryKind},
			SupportsDynamicInventory: true,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:       backendplugin.CapabilitySummary{Streaming: true, Vision: true, Reasoning: true},
			TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("geminicliacp: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	pc := cfg.toProduct()
	var eng *product.Engine
	if s != nil && s.Starter != nil {
		eng = product.NewWithStarter(pc, s.Starter)
	} else {
		eng, err = product.New(pc)
		if err != nil {
			return nil, err
		}
	}
	return &instance{eng: eng, id: req.InstanceID}, nil
}

type instance struct {
	eng *product.Engine
	id  string
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true, Vision: true, Reasoning: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{FactoryKind},
		EvidenceSource:           "geminicliacp",
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) ListModels(ctx context.Context, max uint32) (backendplugin.ListModelsResponse, error) {
	if i.eng == nil || i.eng.Inventory == nil {
		return backendplugin.ListModelsResponse{}, nil
	}
	snap, err := i.eng.Inventory.LoadModels(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(snap.Models))
	for _, m := range snap.Models {
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: m.CanonicalID, NativeModelID: m.NativeID, FactoryKind: FactoryKind,
			Capabilities: backendplugin.CapabilitySummary{Streaming: true},
		})
		if max > 0 && uint32(len(out)) >= max {
			break
		}
	}
	return backendplugin.ListModelsResponse{Models: out, InventorySource: "geminicliacp", FetchedUnixMS: time.Now().UnixMilli()}, nil
}

func (i *instance) Close(context.Context) error {
	if i.eng == nil {
		return nil
	}
	return i.eng.Close()
}

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, _ backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		if i == nil || i.eng == nil {
			return nil, fmt.Errorf("geminicliacp: engine not configured")
		}
		return i.eng.Open(ctx, &call)
	})
}

var (
	_ backendplugin.Service            = (*Service)(nil)
	_ backendplugin.ConfiguredInstance = (*instance)(nil)
)
