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
			Kind: FactoryKind, DisplayName: "Ollama", Description: "Local Ollama OpenAI-compatible backend",
			CredentialMode: backendplugin.CredentialModeNone, AccessScope: backendplugin.AccessScopeLocalOnly,
			RoutePrefixes: []string{FactoryKind}, SupportsDynamicInventory: true,
			ProcessSharing:        backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:    backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}, {
			Kind: FactoryKindCloud, DisplayName: "Ollama Cloud", Description: "Ollama Cloud OpenAI-compatible backend",
			CredentialMode: backendplugin.CredentialModeStatic, AccessScope: backendplugin.AccessScopeAny,
			RoutePrefixes: []string{FactoryKindCloud}, SupportsDynamicInventory: true,
			ProcessSharing:        backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:    backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	kind := req.FactoryKind
	if kind == "" {
		kind = FactoryKind
	}
	if kind != FactoryKind && kind != FactoryKindCloud {
		return nil, fmt.Errorf("ollama: unexpected factory kind %q", kind)
	}
	cfg, err := ParseConfigYAML(kind, req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	if kind == FactoryKind {
		// Local no-auth export: the constant dummy bearer preserves
		// existing local transport behavior; no credentials are admitted.
		return &instance{cfg: cfg, hc: hc, kind: kind}, nil
	}
	// Cloud export: host-issued ordered slots are the sole credential
	// source. No YAML key material, no connector-side environment or file
	// fallback.
	keys, err := openaicompat.DecodeAPIKeySlots(req.Secrets)
	if err != nil {
		return nil, fmt.Errorf("ollama-cloud: %w (want %s[_N] via host Configure secrets)", err, EnvAPIKeyRoot)
	}
	pool, err := openaicompat.NewAPIKeyPool(keys)
	if err != nil {
		return nil, fmt.Errorf("ollama-cloud: %w", err)
	}
	return &instance{cfg: cfg, hc: hc, pool: pool, kind: kind, quotaThreshold: openaicompat.ThresholdFromPolicy(req.RuntimePolicy)}, nil
}

type instance struct {
	cfg            Config
	hc             *http.Client
	pool           *credpool.Pool
	kind           string
	quotaThreshold time.Duration
}

func (i *instance) clientFor(apiKey string) *openaicompat.Client {
	return NewCompatClient(configForKey(i.cfg, apiKey), i.hc, providerHooks())
}

// configForKey binds one ordered credential to a request-scoped config copy
// without mutating the configured instance.
func configForKey(cfg Config, apiKey string) Config {
	cfg.APIKey = apiKey
	return cfg
}

func (i *instance) client() *openaicompat.Client {
	return NewCompatClient(i.cfg, i.hc, providerHooks())
}

func NewCompatClient(cfg Config, hc *http.Client, hooks openaicompat.RequestHooks) *openaicompat.Client {
	return &openaicompat.Client{
		BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, HTTPClient: hc,
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

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return openaicompat.ForwardExecute(stream, openaicompat.ExecuteOpts{
		DefaultModel:  "default",
		ResolveModel:  func(inv backendplugin.Invocation, call aipapi.Call) string { return resolveModel(i.kind, inv, call) },
		ResolveFlavor: resolveFlavor,
		Open:          i.open,
	})
}

// open runs one Execute attempt. The local export keeps its existing
// single-shot no-auth transport; the cloud export selects across ordered
// host-issued credentials with quota-aware pre-output rotation, mapping
// pool exhaustion to recoverable pre-output errors.
func (i *instance) open(ctx context.Context, call aipapi.Call, model string, flavor openaicompat.Flavor) (aipapi.ManagedEventStream, error) {
	if i.kind != FactoryKindCloud || i.pool == nil {
		return i.client().Open(ctx, call, model, flavor)
	}
	return openaicompat.RecoverableRunWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
		return i.clientFor(cred.Secret).Open(ctx, call, model, flavor)
	})
}
