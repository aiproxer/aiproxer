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

// WithTokenProvider pins a single bearer identity for ML-wire tests. It
// bypasses IAM exchange and quota rotation: production (New/NewProduction
// without this option) always uses host-issued ordered api_key slots with
// per-slot IAM providers.
func WithTokenProvider(tp TokenProvider) Option {
	return func(s *Service) {
		s.tokenProvider = tp
	}
}

func WithHTTPClient(hc *http.Client) Option {
	return func(s *Service) {
		s.httpClient = hc
	}
}

type Service struct {
	tokenProvider TokenProvider
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
			StaticCapabilities:       backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("watsonx: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}

	hc := s.httpClient
	if hc == nil {
		c, err := cfg.HTTPClient()
		if err != nil {
			return nil, err
		}
		hc = c
	}

	if s.tokenProvider != nil {
		// Pinned single bearer for ML-wire tests: no IAM exchange, no
		// quota rotation. Production never sets this option.
		return &instance{
			cfg:      cfg,
			override: s.tokenProvider,
			hc:       hc,
		}, nil
	}

	// Host-issued ordered slots are the sole credential source: no YAML key
	// material, no connector-side environment or file fallback. Each slot
	// gets its own IAM token provider, so exchange and refresh stay isolated
	// under the slot's authenticating key and are never pooled as static
	// bearer material.
	slots, err := decodeAPIKeys(req.Secrets)
	if err != nil {
		return nil, fmt.Errorf("watsonx: %w (want %s[_N] via host Configure secrets)", err, EnvAPIKeyRoot)
	}
	pool, err := credpool.New(credentialSet(slots))
	if err != nil {
		return nil, fmt.Errorf("watsonx: %w", err)
	}
	iam := make(map[string]*IAMTokenProvider, len(slots))
	for _, slot := range slots {
		iam[slot.Name] = NewIAMTokenProvider(cfg.IAMOriginURL(), slot.Secret, hc)
	}

	return &instance{
		cfg:            cfg,
		pool:           pool,
		iam:            iam,
		hc:             hc,
		quotaThreshold: openaicompat.ThresholdFromPolicy(req.RuntimePolicy),
	}, nil
}

type instance struct {
	cfg  Config
	pool *credpool.Pool
	// iam holds one isolated IAM token provider per ordered slot name.
	// Nil when the test-only pinned bearer override is active.
	iam            map[string]*IAMTokenProvider
	override       TokenProvider
	hc             *http.Client
	quotaThreshold time.Duration
}

func (i *instance) clientFor(cred credpool.Credential) *Client {
	if i.override != nil {
		return &Client{
			Config:        i.cfg,
			TokenProvider: i.override,
			HTTPClient:    i.hc,
		}
	}
	tp := i.iam[cred.ID]
	if tp == nil {
		// Unreachable: pool IDs and iam keys are built from the same slot
		// list. Fail fast with an empty bearer rather than a nil dereference.
		return &Client{
			Config:        i.cfg,
			TokenProvider: StaticTokenProvider(""),
			HTTPClient:    i.hc,
		}
	}
	return &Client{
		Config:        i.cfg,
		TokenProvider: tp,
		HTTPClient:    i.hc,
	}
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{FactoryKind},
		EvidenceSource:           FactoryKind,
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	if i.override != nil {
		return i.clientFor(credpool.Credential{}).ListModels(ctx, limit)
	}
	return runWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) (backendplugin.ListModelsResponse, error) {
		return i.clientFor(cred).ListModels(ctx, limit)
	})
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	if i.override != nil {
		// Pinned single bearer for ML-wire tests: direct attempt, no
		// quota rotation. Production always takes the pooled path below.
		cl := i.clientFor(credpool.Credential{})
		return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
			m, err := i.resolveModel(inv)
			if err != nil {
				return nil, err
			}
			return cl.Open(ctx, call, m)
		})
	}
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		m, err := i.resolveModel(inv)
		if err != nil {
			return nil, err
		}
		// Ordered host-issued api_keys with per-slot isolated IAM exchange
		// and quota-aware pre-output selection; pool-exhaustion outcomes map
		// to recoverable pre-output errors while provider-specific failures
		// return as-is.
		return recoverableRunWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			return i.clientFor(cred).Open(ctx, call, m)
		})
	})
}

func (i *instance) resolveModel(inv backendplugin.Invocation) (string, error) {
	m := strings.TrimSpace(inv.CanonicalModelID)
	if m == "" || m == FactoryKind {
		if i.cfg.ModelID != "" {
			m = i.cfg.ModelID
		} else if i.cfg.DeploymentID != "" {
			m = "deployment/" + i.cfg.DeploymentID
		} else {
			return "", fmt.Errorf("watsonx: model is required (set model_id or deployment_id in config or provide model in request)")
		}
	} else {
		if after, ok := strings.CutPrefix(m, FactoryKind+"/"); ok {
			m = after
		}
	}
	if m == "" {
		return "", fmt.Errorf("watsonx: model is required (set model_id or deployment_id in config or provide model in request)")
	}
	return m, nil
}
