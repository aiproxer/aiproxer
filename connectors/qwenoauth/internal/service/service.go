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

type Service struct {
	tokenProvider        TokenProvider
	tokenProviderFactory TokenProviderFactory
}

func New(tp TokenProvider, tpf TokenProviderFactory) *Service {
	return &Service{
		tokenProvider:        tp,
		tokenProviderFactory: tpf,
	}
}

func NewProduction() *Service {
	return New(nil, DefaultTokenProviderFactory)
}

func DefaultTokenProviderFactory(_ context.Context, cfg Config, secrets backendplugin.SecretBundle, hc *http.Client) (TokenProvider, error) {
	// 1. Check for static token or API key in secrets
	for _, key := range []string{"api_key", "token", "session_key"} {
		if b, ok := secrets.Values[key]; ok && len(b) > 0 {
			return NewStaticTokenProvider(string(b)), nil
		}
	}

	// 2. Check for OAuth credentials.
	//
	// qwen-oauth is pre-provisioned-refresh-only: the connector implements the
	// refresh half of the OAuth lifecycle (refresh_token grant, proactive
	// refresh, terminal quarantine) against credentials provisioned out-of-band
	// through Qwen's official authorization channels. The initial browser/PKCE
	// login belongs to Qwen's first-party surfaces; reimplementing that
	// consumer login here would be private-interface scraping, so no initial
	// login flow is implemented.
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
		return nil, fmt.Errorf("qwen-oauth: oauth_client_id is required in configuration or secrets (Hermes client_id is not used); qwen-oauth is %s", oauthcred.LoginModePreProvisionedRefreshOnly)
	}

	if tokenFilePath == "" {
		return nil, fmt.Errorf("qwen-oauth: credentials required (supply 'api_key' in secrets or 'oauth_token_file' with 'oauth_client_id' in config/secrets); qwen-oauth OAuth is %s", oauthcred.LoginModePreProvisionedRefreshOnly)
	}

	store := oauthcred.NewFileStore(tokenFilePath)
	if _, err := oauthcred.RequireCredential(store, "qwen-oauth", "qwen-oauth is "+oauthcred.LoginModePreProvisionedRefreshOnly+": initial browser/PKCE login is not implemented; provision the token file via Qwen's official authorization channels first, then retry", TokenRefreshSkew); err != nil {
		return nil, err
	}
	refresher := &QwenOAuthRefresher{
		TokenURL:   cfg.GetTokenURL(),
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
			// Effective credential is one human user's Qwen Portal subscription identity: either a portal API key or the pre-provisioned PKCE OAuth session from oauth_token_file (LoginModePreProvisionedRefreshOnly). It is user-scoped, so it must stay local-only under access.mode: multi_user.
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
		return nil, fmt.Errorf("qwen-oauth: unexpected factory kind %q", req.FactoryKind)
	}

	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}

	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}

	var tp TokenProvider
	if s.tokenProvider != nil {
		tp = s.tokenProvider
	} else if s.tokenProviderFactory != nil {
		factoryTP, err := s.tokenProviderFactory(ctx, cfg, req.Secrets, hc)
		if err != nil {
			return nil, fmt.Errorf("qwen-oauth: token provider: %w", err)
		}
		tp = factoryTP
	} else {
		return nil, fmt.Errorf("qwen-oauth: token provider not configured (use NewProduction or provide TokenProvider/TokenProviderFactory)")
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

func (i *instance) newCompatClient(token string, inv backendplugin.Invocation) *openaicompat.Client {
	return &openaicompat.Client{
		BaseURL:    i.cfg.GetInferenceURL(),
		APIKey:     token,
		HTTPClient: i.hc,
		Transport:  openaicompat.TransportChatOnly,
		Hooks: openaicompat.RequestHooks{
			PrepareHeaders: func(h http.Header, call aipapi.Call, model string, flavor openaicompat.Flavor) {
				h.Set("User-Agent", DefaultUserAgent)
			},
			MutateBody: func(body map[string]any, call aipapi.Call, model string, flavor openaicompat.Flavor) error {
				return AdaptRequestBody(body, inv, call)
			},
		},
	}
}

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		if call.Invocation.Operation == aipapi.OperationOpenAIResponses ||
			call.Invocation.Operation == aipapi.OperationOpenResponsesCreate {
			return nil, fmt.Errorf("qwen-oauth: responses operations are not supported; Qwen OAuth supports Chat family only")
		}

		m := resolveModel(i.kind, inv, call, i.cfg.DefaultModel())
		flavor := openaicompat.FlavorChat

		token, err := i.tp.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("qwen-oauth: acquire token: %w", err)
		}

		cl := i.newCompatClient(token, inv)
		es, err := cl.Open(ctx, call, m, flavor)
		if err != nil {
			if httpErr, ok := errors.AsType[*openaicompat.HTTPError](err); ok {
				if httpErr.Status == http.StatusForbidden {
					return nil, fmt.Errorf("qwen-oauth: entitlement access forbidden (403): %s", httpErr.Message)
				}
				if httpErr.Status == http.StatusUnauthorized {
					newToken, refErr := i.tp.ForceRefresh(ctx)
					if refErr != nil {
						return nil, fmt.Errorf("qwen-oauth: 401 token refresh: %w", refErr)
					}
					cl.APIKey = newToken
					retried, retryErr := cl.Open(ctx, call, m, flavor)
					if retryErr != nil {
						var retryHTTPErr *openaicompat.HTTPError
						if errors.As(retryErr, &retryHTTPErr) && retryHTTPErr.Status == http.StatusForbidden {
							return nil, fmt.Errorf("qwen-oauth: entitlement access forbidden (403): %s", retryHTTPErr.Message)
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
