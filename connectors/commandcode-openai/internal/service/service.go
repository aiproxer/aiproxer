package service

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
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
			Kind: FactoryKind, DisplayName: "Command Code (OpenAI)", Description: "Command Code OpenAI-compatible chat completions backend",
			CredentialMode: backendplugin.CredentialModeStatic, AccessScope: backendplugin.AccessScopeAny,
			RoutePrefixes: []string{FactoryKind}, SupportsDynamicInventory: true,
			ProcessSharing:        backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:    backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("commandcode-openai: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	// Host-issued ordered slots are the sole credential source. The legacy
	// ${VAR} interpolation, connector-side environment lookup and the shared
	// COMMANDCODE_API_KEY fallback are removed: this export admits only
	// COMMANDCODE_OPENAI_API_KEY[_N] via host Configure secrets.
	keys, err := openaicompat.DecodeAPIKeySlots(req.Secrets)
	if err != nil {
		return nil, fmt.Errorf("commandcode-openai: %w (want %s[_N] via host Configure secrets)", err, EnvAPIKeyRoot)
	}
	pool, err := openaicompat.NewAPIKeyPool(keys)
	if err != nil {
		return nil, fmt.Errorf("commandcode-openai: %w", err)
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, hc: hc, pool: pool, kind: FactoryKind, quotaThreshold: openaicompat.ThresholdFromPolicy(req.RuntimePolicy)}, nil
}

type instance struct {
	cfg            Config
	hc             *http.Client
	pool           *credpool.Pool
	kind           string
	quotaThreshold time.Duration
}

func (i *instance) clientFor(apiKey string) *openaicompat.Client {
	return NewCompatClient(i.cfg, i.hc, apiKey, ProviderHooks())
}

func NewCompatClient(cfg Config, hc *http.Client, apiKey string, hooks openaicompat.RequestHooks) *openaicompat.Client {
	return &openaicompat.Client{
		BaseURL: cfg.BaseURL, APIKey: apiKey, HTTPClient: hc,
		Transport: openaicompat.TransportChatAndResponses, Hooks: hooks,
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
	models, err := openaicompat.RunWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) ([]openaicompat.Model, error) {
		return i.clientFor(cred.Secret).ListModels(ctx, limit)
	})
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
	return openaicompat.ForwardExecute(stream, openaicompat.ExecuteOpts{
		DefaultModel: "Qwen/Qwen3.7-Flash",
		ResolveModel: func(inv backendplugin.Invocation, call aipapi.Call) string {
			return resolveModel(i.kind, inv, call)
		},
		ResolveFlavor: resolveFlavor,
		Open:          i.open,
	})
}

// open runs one Execute attempt across ordered host-issued credentials with
// quota-aware pre-output selection; pool-exhaustion outcomes map to
// recoverable pre-output errors while provider-specific failures return
// as-is with existing transport behavior.
func (i *instance) open(ctx context.Context, call aipapi.Call, model string, flavor openaicompat.Flavor) (aipapi.ManagedEventStream, error) {
	return openaicompat.RecoverableRunWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
		return i.clientFor(cred.Secret).Open(ctx, call, model, flavor)
	})
}
