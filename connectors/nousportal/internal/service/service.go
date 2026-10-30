package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type TokenProviderFactory func(ctx context.Context, cfg Config, secrets backendplugin.SecretBundle, hc *http.Client) (TokenProvider, error)

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
		tokenProviderFactory: DefaultTokenProviderFactory,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func DefaultTokenProviderFactory(_ context.Context, cfg Config, secrets backendplugin.SecretBundle, hc *http.Client) (TokenProvider, error) {
	// 1. Check for static token or API key in secrets
	for _, key := range []string{"api_key", "token", "session_key"} {
		if b, ok := secrets.Values[key]; ok && len(b) > 0 {
			return NewStaticTokenProvider(string(b)), nil
		}
	}

	// 2. Check for OAuth credentials
	tokenFilePath := cfg.OAuthTokenFile
	if tokenFilePath == "" {
		if b, ok := secrets.Values["oauth_token_file"]; ok && len(b) > 0 {
			tokenFilePath = string(b)
		}
	}

	clientID := cfg.OAuthClientID
	if clientID == "" {
		if b, ok := secrets.Values["oauth_client_id"]; ok && len(b) > 0 {
			clientID = string(b)
		}
	}

	if clientID == "" {
		return nil, fmt.Errorf("nous-portal: oauth_client_id is required in configuration or secrets (Hermes client_id is not used)")
	}

	if tokenFilePath == "" {
		return nil, fmt.Errorf("nous-portal: credentials required (supply 'api_key' in secrets or 'oauth_token_file' with 'oauth_client_id' in config/secrets)")
	}

	store := oauthcred.NewFileStore(tokenFilePath)
	refresher := &NousOAuthRefresher{
		PortalURL:  cfg.GetPortalURL(),
		ClientID:   clientID,
		HTTPClient: hc,
	}
	session := oauthcred.NewSession(store, refresher, oauthcred.WithSkew(TokenRefreshSkew))
	return NewOAuthTokenProvider(session), nil
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
			Kind:        FactoryKind,
			DisplayName: DisplayName,
			Description: Description,
			// Effective credential is one human user's Nous Portal identity: either a portal API key or a scoped OAuth JWT session loaded from oauth_token_file and refreshed against the portal token endpoint. It is user-scoped, so it must stay local-only under access.mode: multi_user.
			CredentialMode:           backendplugin.CredentialModeOAuthUser,
			AccessScope:              backendplugin.AccessScopeLocalOnly,
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
		return nil, fmt.Errorf("nous-portal: unexpected factory kind %q", req.FactoryKind)
	}

	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}

	hc := s.httpClient
	if hc == nil {
		var err error
		hc, err = cfg.HTTPClient()
		if err != nil {
			return nil, err
		}
	}

	var tp TokenProvider
	if s.tokenProvider != nil {
		tp = s.tokenProvider
	} else if s.tokenProviderFactory != nil {
		factoryTP, err := s.tokenProviderFactory(ctx, cfg, req.Secrets, hc)
		if err != nil {
			return nil, fmt.Errorf("nous-portal: token provider: %w", err)
		}
		tp = factoryTP
	} else {
		return nil, fmt.Errorf("nous-portal: token provider not configured (use NewProduction or provide TokenProvider/TokenProviderFactory)")
	}

	return &instance{
		cfg:  cfg,
		tp:   tp,
		hc:   hc,
		kind: FactoryKind,
	}, nil
}

type instance struct {
	cfg  Config
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
	return ListModels(ctx, i.cfg, i.tp, i.hc, limit)
}

func (i *instance) newCompatClient(token string) *openaicompat.Client {
	return &openaicompat.Client{
		BaseURL:    i.cfg.GetInferenceURL(),
		APIKey:     token,
		HTTPClient: i.hc,
		Transport:  openaicompat.TransportChatOnly,
		Hooks: openaicompat.RequestHooks{
			PrepareHeaders: func(h http.Header, call aipapi.Call, model string, flavor openaicompat.Flavor) {
				h.Set("User-Agent", DefaultUserAgent)
			},
		},
	}
}

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		m := resolveModel(i.kind, inv, call, i.cfg.DefaultModel())
		flavor := openaicompat.FlavorChat

		token, err := i.tp.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("nous-portal: acquire token: %w", err)
		}

		cl := i.newCompatClient(token)
		es, err := cl.Open(ctx, call, m, flavor)
		if err != nil {
			if httpErr, ok := errors.AsType[*openaicompat.HTTPError](err); ok {
				if httpErr.Status == http.StatusForbidden {
					return nil, fmt.Errorf("nous-portal: entitlement access forbidden (403): %s", httpErr.Message)
				}
				if httpErr.Status == http.StatusUnauthorized {
					newToken, refErr := i.tp.ForceRefresh(ctx)
					if refErr != nil {
						return nil, fmt.Errorf("nous-portal: 401 token refresh: %w", refErr)
					}
					cl.APIKey = newToken
					retried, retryErr := cl.Open(ctx, call, m, flavor)
					if retryErr != nil {
						var retryHTTPErr *openaicompat.HTTPError
						if errors.As(retryErr, &retryHTTPErr) && retryHTTPErr.Status == http.StatusForbidden {
							return nil, fmt.Errorf("nous-portal: entitlement access forbidden (403): %s", retryHTTPErr.Message)
						}
						return nil, retryErr
					}
					return retried, nil
				}
			}
			return nil, err
		}

		return es, nil
	})
}
