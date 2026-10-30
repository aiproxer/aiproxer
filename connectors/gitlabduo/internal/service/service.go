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

type Option func(*Service)

func WithTokenProvider(tp TokenProvider) Option {
	return func(s *Service) {
		s.tokenProvider = tp
	}
}

func WithDirectAccessManager(dam DirectAccessManager) Option {
	return func(s *Service) {
		s.directAccessManager = dam
	}
}

func WithHTTPClient(hc *http.Client) Option {
	return func(s *Service) {
		s.httpClient = hc
	}
}

type Service struct {
	tokenProvider       TokenProvider
	directAccessManager DirectAccessManager
	httpClient          *http.Client
	isProduction        bool
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
	s := &Service{isProduction: true}
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
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:        FactoryKind,
			DisplayName: DisplayName,
			Description: Description,
			// Effective credential is one human operator's personal identity: either a GitLab personal access token or a GitLab OAuth session loaded from oauth_token_file. GitLab Duo direct-access tokens are minted from that user identity, so this backend is never an operator-wide or workload credential and must stay local-only under access.mode: multi_user.
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
		return nil, fmt.Errorf("gitlab-duo: unexpected factory kind %q", req.FactoryKind)
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
	} else {
		// 1. Check for PAT in secrets
		pat := ""
		for _, key := range []string{"pat", "token", "api_key", "personal_access_token"} {
			if b, ok := req.Secrets.Values[key]; ok && len(b) > 0 {
				pat = string(b)
				break
			}
		}

		if pat != "" {
			tp = NewStaticPATProvider(pat)
		} else {
			// 2. Check for OAuth token file
			tokenFilePath := cfg.OAuthTokenFile
			if tokenFilePath == "" {
				if b, ok := req.Secrets.Values["oauth_token_file"]; ok && len(b) > 0 {
					tokenFilePath = string(b)
				}
			}

			if tokenFilePath != "" {
				if cfg.IsSelfManaged() && cfg.OAuthClientID == "" {
					return nil, fmt.Errorf("gitlab-duo: oauth_client_id is required for self-managed GitLab instances (%s)", cfg.GetInstanceURL())
				}

				store := oauthcred.NewFileStore(tokenFilePath)
				refresher := &GitLabOAuthRefresher{
					InstanceURL: cfg.GetInstanceURL(),
					ClientID:    cfg.OAuthClientID,
					HTTPClient:  hc,
				}
				session := oauthcred.NewSession(store, refresher)
				tp = NewOAuthTokenProvider(session)
			} else {
				return nil, fmt.Errorf("gitlab-duo: credentials required (provide 'pat' or 'token' in secrets, or 'oauth_token_file' in config/secrets)")
			}
		}
	}

	var dam DirectAccessManager
	if s.directAccessManager != nil {
		dam = s.directAccessManager
	} else {
		dam = NewHTTPDirectAccessManager(cfg.GetInstanceURL(), tp, hc)
	}

	cl := &Client{
		Config:               cfg,
		TokenProvider:        tp,
		DirectAccessManager:  dam,
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
			} else if i.cl.Config.DefaultModel() != "" {
				m = i.cl.Config.DefaultModel()
			} else {
				m = ModelSonnet45
			}
		} else {
			if after, ok := strings.CutPrefix(m, FactoryKind+"/"); ok {
				m = after
			}
		}
		m = strings.TrimSpace(m)
		if m == "" || m == FactoryKind {
			m = i.cl.Config.DefaultModel()
		}

		return i.cl.Execute(ctx, inv, call, m)
	})
}
