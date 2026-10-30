package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
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

func WithHTTPClient(hc *http.Client) Option {
	return func(s *Service) {
		s.httpClient = hc
	}
}

type Service struct {
	tokenProvider        TokenProvider
	tokenProviderFactory TokenProviderFactory
	httpClient           *http.Client
}

func New(opts ...Option) *Service {
	s := &Service{}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func NewProduction(opts ...Option) *Service {
	s := &Service{
		tokenProviderFactory: DefaultOAuthTokenProviderFactory,
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

func (s *Service) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("sapaicore: unexpected factory kind %q", req.FactoryKind)
	}

	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}

	rawKey := req.Secrets.Values["service_key"]
	sk, err := ParseServiceKey(rawKey)
	if err != nil {
		return nil, err
	}

	var tp TokenProvider
	if s.tokenProvider != nil {
		tp = s.tokenProvider
	} else if s.tokenProviderFactory != nil {
		factoryTP, err := s.tokenProviderFactory(ctx, cfg, sk, req.Secrets)
		if err != nil {
			return nil, fmt.Errorf("sapaicore: oauth token provider: %w", err)
		}
		tp = factoryTP
	} else {
		return nil, fmt.Errorf("sapaicore: token provider not configured (use NewProduction or provide TokenProvider/TokenProviderFactory)")
	}

	hc := s.httpClient
	if hc == nil {
		var err error
		hc, err = cfg.HTTPClient()
		if err != nil {
			return nil, err
		}
	}

	return &instance{
		cfg:  cfg,
		sk:   sk,
		tp:   tp,
		hc:   hc,
		kind: FactoryKind,
	}, nil
}

type instance struct {
	cfg  Config
	sk   ServiceKey
	tp   TokenProvider
	hc   *http.Client
	kind string
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

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	return ListModels(ctx, i.cfg, i.sk, i.tp, i.hc, limit)
}

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		flavor := ResolveFlavor(call)
		deploymentID, err := resolveDeployment(i.cfg, inv)
		if err != nil {
			return nil, err
		}

		token, err := i.tp.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("sapaicore: acquire token: %w", err)
		}

		apiOrigin := i.cfg.AIAPIOrigin(i.sk.ServiceURLs.AIAPIURL)
		baseURL := fmt.Sprintf("%s/v2/inference/deployments/%s", apiOrigin, deploymentID)

		cl := &openaicompat.Client{
			BaseURL:    baseURL,
			APIKey:     token,
			HTTPClient: i.hc,
			Transport:  openaicompat.TransportChatOnly,
			Hooks: openaicompat.RequestHooks{
				PrepareHeaders: func(h http.Header, call aipapi.Call, model string, flavor openaicompat.Flavor) {
					h.Set("AI-Resource-Group", i.cfg.ResourceGroup)
				},
			},
		}

		bodyModel := deploymentID
		if inv.NativeModelID != "" && inv.NativeModelID != FactoryKind && !strings.HasPrefix(inv.NativeModelID, FactoryKind+"/") {
			bodyModel = inv.NativeModelID
		}

		return cl.Open(ctx, call, bodyModel, flavor)
	})
}
