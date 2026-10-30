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
		return nil, fmt.Errorf("vertex: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}

	var tp TokenProvider
	if s.tokenProvider != nil {
		tp = s.tokenProvider
	} else if s.tokenProviderFactory != nil {
		factoryTP, err := s.tokenProviderFactory(cfg, req.Secrets)
		if err != nil {
			return nil, fmt.Errorf("vertex: token provider: %w", err)
		}
		tp = factoryTP
	} else {
		return nil, fmt.Errorf("vertex: requires token provider or production credential chain (use NewProduction)")
	}

	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}

	return &instance{
		cfg:                  cfg,
		tp:                   tp,
		hc:                   hc,
		kind:                 FactoryKind,
		accountingEvidenceV1: backendplugin.AccountingEvidenceNegotiated(req.Negotiation),
	}, nil
}

type instance struct {
	cfg                  Config
	tp                   TokenProvider
	hc                   *http.Client
	kind                 string
	accountingEvidenceV1 bool
}

func (i *instance) client() *Client {
	return &Client{
		Config:               i.cfg,
		TokenProvider:        i.tp,
		HTTPClient:           i.hc,
		accountingEvidenceV1: i.accountingEvidenceV1,
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
		model := resolveModel(i.kind, inv, i.cfg.Model)
		if model == "" {
			return nil, fmt.Errorf("vertex: model is required")
		}
		return cl.Open(ctx, call, model)
	})
}

func resolveModel(kind string, inv backendplugin.Invocation, defaultModel string) string {
	m := strings.TrimSpace(inv.CanonicalModelID)
	if m == "" || m == kind {
		return strings.TrimSpace(defaultModel)
	}
	if after, ok := strings.CutPrefix(m, kind+"/"); ok {
		return after
	}
	return m
}

func isCodingCapableModel(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	lower := strings.ToLower(id)
	dropPatterns := []string{
		"embed",
		"image",
		"imagen",
		"video",
		"veo",
		"audio",
		"chirp",
		"whisper",
		"tts",
		"speech",
		"music",
		"transcription",
		"rerank",
	}
	for _, pattern := range dropPatterns {
		if strings.Contains(lower, pattern) {
			return false
		}
	}
	return true
}
