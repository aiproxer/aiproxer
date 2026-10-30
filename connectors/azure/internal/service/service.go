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

type Option func(*Service)

func WithTokenProvider(tp TokenProvider) Option {
	return func(s *Service) {
		s.tokenProvider = tp
	}
}

func WithTokenProviderFactory(f TokenProviderFactory) Option {
	return func(s *Service) {
		s.tokenProviderFactory = f
	}
}

type Service struct {
	tokenProvider        TokenProvider
	tokenProviderFactory TokenProviderFactory
}

// New returns a Service. When opts are omitted, tokenProvider and tokenProviderFactory are unconfigured.
func New(opts ...Option) *Service {
	s := &Service{}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// NewProduction returns a Service wired with the production Microsoft Entra credential chain.
func NewProduction(opts ...Option) *Service {
	s := &Service{
		tokenProviderFactory: DefaultTokenProviderFactory,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake,
		PluginID:      PluginID,
		Version:       "0.1.0",
		BuildID:       "localdev",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:                     FactoryKind,
			DisplayName:              DisplayName,
			Description:              Description,
			CredentialMode:           backendplugin.CredentialModeStatic,
			AccessScope:              backendplugin.AccessScopeAny,
			RoutePrefixes:            []string{FactoryKind},
			SupportsDynamicInventory: true,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:       backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("azure-openai: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	if cfg.Endpoint == "" && cfg.ResourceName == "" {
		return nil, fmt.Errorf("azure-openai: endpoint or resource_name is required")
	}
	if cfg.APIVersion == "" {
		return nil, fmt.Errorf("azure-openai: api_version is required")
	}

	mode := cfg.CredentialMode
	if mode == "" {
		mode = CredentialModeAPIKey
	}

	var apiKeys []openaicompat.SlotSecret
	var tp TokenProvider

	switch mode {
	case CredentialModeAPIKey:
		// Host-issued ordered slots are the sole key-mode credential source:
		// no YAML key material, no connector-side environment or file
		// fallback.
		slots, err := decodeKeySlots(req.Secrets)
		if err != nil {
			return nil, fmt.Errorf("azure-openai: %w (want %s[_N] via host Configure secrets)", err, EnvAPIKeyRoot)
		}
		apiKeys = slots
	case CredentialModeEntra:
		if s.tokenProvider != nil {
			tp = s.tokenProvider
		} else if s.tokenProviderFactory != nil {
			factoryTP, err := s.tokenProviderFactory(cfg, req.Secrets)
			if err != nil {
				return nil, fmt.Errorf("azure-openai: entra token provider: %w", err)
			}
			tp = factoryTP
		} else {
			return nil, fmt.Errorf("azure-openai: entra mode requires token provider or production credential chain (use NewProduction)")
		}
	default:
		return nil, fmt.Errorf("azure-openai: unsupported credential_mode %q", mode)
	}

	if len(cfg.Deployments) == 0 {
		return nil, errMissingDeployments()
	}

	if tp != nil {
		// Entra mode stays on its isolated authenticating identity: token
		// acquisition and refresh run under the provider chain, never the
		// static key pool, and failures never rotate static credentials.
		hc, err := cfg.HTTPClientWithTokenProvider("", tp)
		if err != nil {
			return nil, err
		}
		return &instance{cfg: cfg, hc: hc, tp: tp, kind: FactoryKind}, nil
	}

	pool, err := newKeyPool(apiKeys)
	if err != nil {
		return nil, fmt.Errorf("azure-openai: %w", err)
	}
	keyClients := make(map[string]*openaicompat.Client, len(apiKeys))
	for _, slot := range apiKeys {
		hc, err := cfg.HTTPClientWithTokenProvider(slot.Secret, nil)
		if err != nil {
			return nil, err
		}
		keyClients[slot.Name] = NewCompatClient(cfg, hc, ProviderHooks(cfg))
	}
	return &instance{cfg: cfg, pool: pool, keyClients: keyClients, kind: FactoryKind, quotaThreshold: openaicompat.ThresholdFromPolicy(req.RuntimePolicy)}, nil
}

type instance struct {
	cfg  Config
	hc   *http.Client
	tp   TokenProvider
	pool *credpool.Pool
	// keyClients binds one wire client (with its api-key transport) per
	// ordered slot name for key-mode rotation. Nil in Entra mode.
	keyClients map[string]*openaicompat.Client
	kind       string
	// quotaThreshold is the host-projected nonsecret quota-switch threshold;
	// zero selects the coordinator default. Entra mode ignores it.
	quotaThreshold time.Duration
}

func (i *instance) entraClient() *openaicompat.Client {
	return NewCompatClient(i.cfg, i.hc, ProviderHooks(i.cfg))
}

func NewCompatClient(cfg Config, hc *http.Client, hooks openaicompat.RequestHooks) *openaicompat.Client {
	return &openaicompat.Client{
		BaseURL:    cfg.BaseURL(),
		APIKey:     "",
		HTTPClient: hc,
		Transport:  openaicompat.TransportChatAndResponses,
		Hooks:      hooks,
	}
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{i.kind},
		EvidenceSource:           i.kind,
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) ListModels(_ context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	if len(i.cfg.Deployments) == 0 {
		return backendplugin.ListModelsResponse{}, errMissingDeployments()
	}
	// Deployment-driven inventory: Azure inference routes by deployment name,
	// while /openai/v1/models lists available models, not the deployment-name
	// mapping. Advertising bare model IDs would misroute, so each configured
	// deployment is one routable identity and the models endpoint is not consulted.
	out := make([]backendplugin.ModelDescriptor, 0, len(i.cfg.Deployments))
	for _, name := range sortedDeploymentNames(i.cfg.Deployments) {
		model := i.cfg.Deployments[name]
		if !isResponsesCapableModel(model) {
			continue
		}
		display := name
		if model != name {
			display = name + " (" + model + ")"
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: i.kind + "/" + name,
			NativeModelID:    name,
			DisplayName:      display,
			FactoryKind:      i.kind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		})
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}
	return backendplugin.ListModelsResponse{
		Models:          out,
		InventorySource: i.kind,
		FetchedUnixMS:   time.Now().UnixMilli(),
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	if i.tp != nil {
		// Entra mode: isolated single authenticating identity, no static
		// credential rotation. Failures return as-is.
		cl := i.entraClient()
		return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
			model, err := resolveDeployment(i.cfg, i.kind, inv.CanonicalModelID)
			if err != nil {
				return nil, err
			}
			return cl.Open(ctx, call, model, ResolveFlavor(call))
		})
	}
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		model, err := resolveDeployment(i.cfg, i.kind, inv.CanonicalModelID)
		if err != nil {
			return nil, err
		}
		return i.open(ctx, call, model, ResolveFlavor(call))
	})
}

// open runs one key-mode Execute attempt across ordered host-issued
// credentials: the first usable key stays preferred, auth-invalid and
// quota-exhausted keys advance pre-output, and pool-exhaustion outcomes map
// to recoverable pre-output errors for core routing. Provider-specific
// failures return as-is with existing transport behavior.
func (i *instance) open(ctx context.Context, call aipapi.Call, model string, flavor openaicompat.Flavor) (aipapi.ManagedEventStream, error) {
	return openaicompat.RecoverableRunWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
		cl, ok := i.keyClients[cred.ID]
		if !ok {
			return nil, fmt.Errorf("azure-openai: unknown credential slot %q", cred.ID)
		}
		return cl.Open(ctx, call, model, flavor)
	})
}
