package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type Option func(*Service)

func WithAWSClientFactory(f AWSClientFactory) Option {
	return func(s *Service) {
		s.clientFactory = f
	}
}

func WithStaticClients(r RuntimeClient) Option {
	return func(s *Service) {
		s.runtimeClient = r
	}
}

type Service struct {
	clientFactory AWSClientFactory
	runtimeClient RuntimeClient
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
		clientFactory: DefaultAWSClientFactory,
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
			// Provider streaming is served via unary InvokeEndpoint collect:
			// canonical events are emitted over a managed stream so both
			// streaming and non-streaming delivery are supported.
			StaticCapabilities:    backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("sagemaker: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}

	var rCli RuntimeClient

	if s.runtimeClient != nil {
		rCli = s.runtimeClient
	} else if s.clientFactory != nil {
		r, err := s.clientFactory(ctx, cfg, req.Secrets)
		if err != nil {
			return nil, fmt.Errorf("sagemaker: create AWS clients: %w", err)
		}
		rCli = r
	} else {
		return nil, fmt.Errorf("sagemaker: requires AWS configuration or credentials (use NewProduction)")
	}

	return &instance{
		cfg:  cfg,
		rCli: rCli,
		kind: FactoryKind,
	}, nil
}

type instance struct {
	cfg  Config
	rCli RuntimeClient
	kind string
}

func (i *instance) client() *Client {
	return &Client{
		Config:  i.cfg,
		Runtime: i.rCli,
	}
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		// Provider streaming is served via unary InvokeEndpoint collect:
		// canonical events are emitted over a managed stream so both
		// streaming and non-streaming delivery are supported.
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
			m = i.cfg.EndpointName
		} else {
			if after, ok := strings.CutPrefix(m, i.kind+"/"); ok {
				m = after
			}
			if m != i.cfg.EndpointName {
				return nil, fmt.Errorf("sagemaker: model %q does not match configured endpoint_name %q", inv.CanonicalModelID, i.cfg.EndpointName)
			}
		}
		return cl.Open(ctx, call, m)
	})
}
