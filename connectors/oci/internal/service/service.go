package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type Option func(*Service)

func WithRequestSignerFactory(f RequestSignerFactory) Option {
	return func(s *Service) {
		s.signerFactory = f
	}
}

func WithRequestSigner(r RequestSigner) Option {
	return func(s *Service) {
		s.signer = r
	}
}

func WithHTTPClient(hc *http.Client) Option {
	return func(s *Service) {
		s.httpClient = hc
	}
}

type Service struct {
	signerFactory RequestSignerFactory
	signer        RequestSigner
	httpClient    *http.Client
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
		signerFactory: DefaultRequestSignerFactory,
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
		return nil, fmt.Errorf("oci-generative-ai: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}

	var signer RequestSigner
	if s.signer != nil {
		signer = s.signer
	} else if s.signerFactory != nil {
		sgn, err := s.signerFactory(ctx, cfg, req.Secrets)
		if err != nil {
			return nil, fmt.Errorf("oci-generative-ai: create signer: %w", err)
		}
		signer = sgn
	} else {
		return nil, fmt.Errorf("oci-generative-ai: requires OCI credentials or signer (use NewProduction)")
	}

	hc := s.httpClient
	if hc == nil {
		c, err := cfg.HTTPClient()
		if err != nil {
			return nil, err
		}
		hc = c
	}

	return &instance{
		cfg:    cfg,
		signer: signer,
		hc:     hc,
		kind:   FactoryKind,
	}, nil
}

type instance struct {
	cfg    Config
	signer RequestSigner
	hc     *http.Client
	kind   string
}

func (i *instance) client() *Client {
	return &Client{
		Config:     i.cfg,
		Signer:     i.signer,
		HTTPClient: i.hc,
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

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	return i.client().ListModels(ctx, limit)
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	cl := i.client()
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		m := strings.TrimSpace(inv.CanonicalModelID)
		if m == "" || m == i.kind {
			m = i.cfg.ModelID
		} else {
			if after, ok := strings.CutPrefix(m, i.kind+"/"); ok {
				m = after
			}
		}
		if m == "" {
			return nil, fmt.Errorf("oci-generative-ai: model is required (set model_id in config or provide model in request)")
		}
		return cl.Open(ctx, call, m)
	})
}
