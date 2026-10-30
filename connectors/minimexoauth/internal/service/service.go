package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Service implements backendplugin.Service for MiniMax OAuth.
type Service struct {
	httpClient    *http.Client
	tokenProvider TokenProvider
	oauthSession  *oauthcred.Session
}

type Option func(*Service)

func WithHTTPClient(client *http.Client) Option {
	return func(s *Service) {
		s.httpClient = client
	}
}

func WithTokenProvider(tp TokenProvider) Option {
	return func(s *Service) {
		s.tokenProvider = tp
	}
}

func WithOAuthSession(sess *oauthcred.Session) Option {
	return func(s *Service) {
		s.oauthSession = sess
	}
}

// New creates a new Service instance with optional overrides (useful for testing).
func New(opts ...Option) *Service {
	s := &Service{}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// NewProduction constructs a standard production Service.
func NewProduction() *Service {
	return New()
}

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake,
		PluginID:      PluginID,
		Version:       "0.1.0",
		BuildID:       "localdev",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:        FactoryKind,
			DisplayName: DisplayName,
			Description: Description,
			// Effective credential is one human user's MiniMax subscription identity: either a portal access token/API key or the local PKCE/user-code OAuth session produced by StartLogin/LoginSession.Complete and persisted by oauthcred. Under the host principal model it must never be multiplexed to unrelated principals, so it is user-scoped OAuth and stays local-only under access.mode: multi_user.
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
		return nil, fmt.Errorf("minimax-oauth: unexpected factory kind %q", req.FactoryKind)
	}

	cfg, err := ParseAndValidateConfig(req.ConfigYAML, req.Secrets)
	if err != nil {
		return nil, err
	}

	hc := s.httpClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.HTTPTimeout}
	}

	var tp TokenProvider
	var oauthSess *oauthcred.Session

	if s.tokenProvider != nil {
		tp = s.tokenProvider
		oauthSess = s.oauthSession
	} else if cfg.DirectToken != "" {
		tp = &StaticTokenProvider{AccessToken: cfg.DirectToken}
	} else {
		store := oauthcred.NewFileStore(cfg.OAuthTokenFile)
		if _, err := oauthcred.RequireCredential(store, "minimax-oauth", "run the MiniMax login flow first (StartLogin displays the verification URI and user code; LoginSession.Complete polls approval and persists credentials), then retry", TokenRefreshSkew); err != nil {
			return nil, err
		}
		refresher := &MiniMaxOAuthRefresher{
			PortalBaseURL: cfg.PortalBaseURL,
			ClientID:      cfg.OAuthClientID,
			HTTPClient:    hc,
		}
		oauthSess = oauthcred.NewSession(store, refresher, oauthcred.WithSkew(TokenRefreshSkew))
		tp = NewOAuthTokenProvider(oauthSess)
	}

	cl := &Client{
		Config:               cfg,
		TokenProvider:        tp,
		OAuthSession:         oauthSess,
		HTTPClient:           hc,
		accountingEvidenceV1: backendplugin.AccountingEvidenceNegotiated(req.Negotiation),
	}

	return &instance{
		cl:   cl,
		kind: FactoryKind,
	}, nil
}

type instance struct {
	cl   *Client
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
	return i.cl.ListModels(ctx, limit)
}

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		m := strings.TrimSpace(inv.CanonicalModelID)
		if m == "" || m == FactoryKind {
			if inv.NativeModelID != "" && inv.NativeModelID != FactoryKind {
				m = inv.NativeModelID
			} else {
				m = ModelM27
			}
		} else {
			if after, ok := strings.CutPrefix(m, FactoryKind+"/"); ok {
				m = after
			}
		}
		m = strings.TrimSpace(m)
		if m == "" || m == FactoryKind {
			m = ModelM27
		}

		return i.cl.Execute(ctx, inv, call, m)
	})
}
