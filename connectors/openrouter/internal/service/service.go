package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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
			Kind: FactoryKind, DisplayName: "OpenRouter", Description: "OpenRouter OpenAI-compatible backend",
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
		return nil, fmt.Errorf("openrouter: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	// Host-issued ordered slots are the sole credential source: no YAML key
	// material, no connector-side environment or file fallback.
	keys, err := openaicompat.DecodeAPIKeySlots(req.Secrets)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w (want %s[_N] via host Configure secrets)", err, EnvAPIKeyRoot)
	}
	pool, err := openaicompat.NewAPIKeyPool(keys)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, hc: hc, pool: pool, quotaThreshold: openaicompat.ThresholdFromPolicy(req.RuntimePolicy)}, nil
}

type instance struct {
	cfg            Config
	hc             *http.Client
	pool           *credpool.Pool
	quotaThreshold time.Duration
}

func (i *instance) clientFor(apiKey string) *openaicompat.Client {
	return NewCompatClient(i.cfg, i.hc, apiKey)
}

// NewCompatClient builds the OpenAI-compatible client with OpenRouter hooks (tests).
func NewCompatClient(cfg Config, hc *http.Client, apiKey string) *openaicompat.Client {
	inst := &instance{cfg: cfg, hc: hc}
	return &openaicompat.Client{
		BaseURL: cfg.BaseURL, APIKey: apiKey, HTTPClient: hc,
		Transport: openaicompat.TransportChatAndResponses,
		Hooks: openaicompat.RequestHooks{
			PrepareHeaders: inst.prepareHeaders,
			MutateBody:     inst.mutateBody,
		},
	}
}

func (i *instance) prepareHeaders(h http.Header, call aipapi.Call, model string, flavor openaicompat.Flavor) {
	i.applyAttribution(h, call)
	_ = model
	_ = flavor
}

func (i *instance) applyAttribution(h http.Header, call aipapi.Call) {
	referer := resolveAppURL(i.cfg, extString(call, extHTTPReferer))
	if referer != "" {
		h.Set("HTTP-Referer", referer)
	}
	title := resolveAppTitle(i.cfg, extString(call, extTitle))
	if title != "" {
		h.Set("X-OpenRouter-Title", title)
	}
	if v := extString(call, extCategories); v != "" {
		h.Set("X-OpenRouter-Categories", v)
	}
	if v := extString(call, extMetadataHeader); v != "" {
		h.Set("X-OpenRouter-Metadata", v)
	}
}

func (i *instance) mutateBody(body map[string]any, call aipapi.Call, model string, flavor openaicompat.Flavor) error {
	_ = model
	_ = flavor
	return applyOpenRouterBody(body, call)
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{FactoryKind}, EvidenceSource: FactoryKind, ProfileVersion: "1",
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
			CanonicalModelID: FactoryKind + "/" + m.ID, NativeModelID: m.ID, FactoryKind: FactoryKind,
			Capabilities: backendplugin.CapabilitySummary{Streaming: true},
		})
	}
	return backendplugin.ListModelsResponse{
		Models: out, InventorySource: FactoryKind, FetchedUnixMS: time.Now().UnixMilli(),
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return openaicompat.ForwardExecute(stream, openaicompat.ExecuteOpts{
		DefaultModel: "openrouter/auto",
		ResolveModel: func(inv backendplugin.Invocation, _ aipapi.Call) string {
			return strings.TrimPrefix(strings.TrimSpace(inv.CanonicalModelID), FactoryKind+"/")
		},
		ResolveFlavor: resolveFlavor,
		Open:          i.open,
	})
}

// open runs one Execute attempt across ordered host-issued credentials: the
// first usable key stays preferred, auth-invalid and quota-exhausted keys
// advance pre-output, and pool-exhaustion outcomes map to recoverable
// pre-output errors for core routing. Provider-specific failures return
// as-is with existing transport behavior.
func (i *instance) open(ctx context.Context, call aipapi.Call, model string, flavor openaicompat.Flavor) (aipapi.ManagedEventStream, error) {
	return openaicompat.RecoverableRunWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
		return i.clientFor(cred.Secret).Open(ctx, call, model, flavor)
	})
}

func resolveFlavor(call aipapi.Call) openaicompat.Flavor {
	if extString(call, extUpstreamFlavor) == "responses" {
		return openaicompat.FlavorResponses
	}
	if call.Invocation.Operation == aipapi.OperationOpenAIResponses {
		return openaicompat.FlavorResponses
	}
	return openaicompat.FlavorChat
}
