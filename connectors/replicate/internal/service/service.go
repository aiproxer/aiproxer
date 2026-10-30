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

type Option func(*Service)

func WithHTTPClient(hc *http.Client) Option {
	return func(s *Service) {
		s.httpClient = hc
	}
}

type Service struct {
	httpClient *http.Client
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

// NewProduction returns the production service: credentials always come
// from host-issued ordered Configure slots, never connector YAML, env or
// files. It exists so distribution mains keep a stable constructor.
func NewProduction(opts ...Option) *Service {
	return New(opts...)
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
			StaticCapabilities:       backendplugin.CapabilitySummary{Streaming: true, Tools: false, Vision: false},
			TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("replicate: unexpected factory kind %q", req.FactoryKind)
	}

	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}

	// Host-issued ordered slots are the sole credential source: no YAML
	// token material, no connector-side environment or file fallback.
	tokens, err := decodeAPITokens(req.Secrets)
	if err != nil {
		return nil, fmt.Errorf("replicate: %w (want %s[_N] via host Configure secrets)", err, EnvAPITokenRoot)
	}
	pool, err := openaicompat.NewAPIKeyPool(tokens)
	if err != nil {
		return nil, fmt.Errorf("replicate: %w", err)
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
		cfg:            cfg,
		pool:           pool,
		hc:             hc,
		kind:           FactoryKind,
		quotaThreshold: openaicompat.ThresholdFromPolicy(req.RuntimePolicy),
	}, nil
}

type instance struct {
	cfg            Config
	pool           *credpool.Pool
	hc             *http.Client
	kind           string
	quotaThreshold time.Duration
}

func (i *instance) clientFor(token string) *Client {
	return &Client{
		Config:        i.cfg,
		TokenProvider: StaticTokenProvider(token),
		HTTPClient:    i.hc,
	}
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true, Tools: false, Vision: false},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{i.kind},
		EvidenceSource:           i.kind,
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	return runWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) (backendplugin.ListModelsResponse, error) {
		return i.clientFor(cred.Secret).ListModels(ctx, limit)
	})
}

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		if call.Invocation.Operation == aipapi.OperationOpenAIResponses ||
			call.Invocation.Operation == aipapi.OperationOpenResponsesCreate {
			return nil, fmt.Errorf("replicate: responses operations are not supported")
		}
		if len(call.Tools) > 0 || len(inv.Tools) > 0 {
			return nil, fmt.Errorf("replicate: tools are not supported")
		}

		reqModel := strings.TrimSpace(inv.CanonicalModelID)
		if reqModel == "" || reqModel == FactoryKind {
			if inv.NativeModelID != "" && inv.NativeModelID != FactoryKind {
				reqModel = inv.NativeModelID
			} else {
				reqModel = i.cfg.Model
			}
		} else {
			if after, ok := strings.CutPrefix(reqModel, FactoryKind+"/"); ok {
				reqModel = after
			}
		}
		reqModel = strings.TrimSpace(reqModel)
		if reqModel == "" || reqModel == FactoryKind {
			reqModel = i.cfg.Model
		}

		configuredParts := strings.Split(i.cfg.Model, "/")
		configuredName := configuredParts[len(configuredParts)-1]
		if reqModel != i.cfg.Model && reqModel != configuredName {
			return nil, fmt.Errorf("replicate: model %q does not match configured model %q", reqModel, i.cfg.Model)
		}

		// Ordered host-issued tokens with quota-aware pre-output selection;
		// pool-exhaustion outcomes map to recoverable pre-output errors
		// while provider-specific failures return as-is.
		return recoverableRunWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			return i.clientFor(cred.Secret).Open(ctx, call, i.cfg.Model)
		})
	})
}
