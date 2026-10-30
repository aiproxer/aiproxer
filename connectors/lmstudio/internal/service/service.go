package service

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake, PluginID: PluginID, Version: "0.1.0", BuildID: "localdev",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind: FactoryKind, DisplayName: "LM Studio", Description: "Local LM Studio OpenAI-compatible server",
			CredentialMode: backendplugin.CredentialModeNone, AccessScope: backendplugin.AccessScopeLocalOnly,
			RoutePrefixes: []string{FactoryKind}, SupportsDynamicInventory: true,
			ProcessSharing:        backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:    backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("lmstudio: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	// No-auth local export: Configure secrets are never consumed. The
	// constant dummy bearer preserves existing local transport behavior for
	// servers that require a non-empty Authorization header.
	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, hc: hc, kind: FactoryKind}, nil
}

type instance struct {
	cfg  Config
	hc   *http.Client
	kind string
}

func (i *instance) client() *openaicompat.Client {
	return NewCompatClient(i.cfg, i.hc, providerHooks())
}

func NewCompatClient(cfg Config, hc *http.Client, hooks openaicompat.RequestHooks) *openaicompat.Client {
	return &openaicompat.Client{
		BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, HTTPClient: hc,
		Transport: openaicompat.TransportChatOnly, Hooks: hooks,
	}
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{i.kind}, EvidenceSource: i.kind, ProfileVersion: "1",
	}, nil
}

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	models, err := i.client().ListModels(ctx, limit)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(models))
	for _, m := range models {
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: i.kind + "/" + m.ID, NativeModelID: m.ID, FactoryKind: i.kind,
			Capabilities: backendplugin.CapabilitySummary{Streaming: true},
		})
	}
	return backendplugin.ListModelsResponse{
		Models: out, InventorySource: i.kind, FetchedUnixMS: time.Now().UnixMilli(),
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	cl := i.client()
	return openaicompat.ForwardExecute(stream, openaicompat.ExecuteOpts{
		DefaultModel:  "default",
		ResolveModel:  func(inv backendplugin.Invocation, call aipapi.Call) string { return resolveModel(i.kind, inv, call) },
		ResolveFlavor: resolveFlavor,
		Open:          cl.Open,
	})
}
