package service

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

type Service struct{}

func New() *Service {
	return &Service{}
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
		return nil, fmt.Errorf("snowflake-cortex: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	if cfg.Account == "" {
		return nil, fmt.Errorf("snowflake-cortex: account is required")
	}

	// Host-issued ordered slots are the sole credential source: no YAML PAT
	// material, no connector-side environment or file fallback. The canonical
	// api_token[_N] slots converge the legacy pat/api_key/token aliases onto
	// the single shared SNOWFLAKE_CORTEX_PAT root declared in the manifest.
	tokens, err := openaicompat.DecodeAPITokenSlots(req.Secrets)
	if err != nil {
		return nil, fmt.Errorf("snowflake-cortex: %w (want %s[_N] via host Configure secrets)", err, EnvAPITokenRoot)
	}
	pool, err := openaicompat.NewAPIKeyPool(tokens)
	if err != nil {
		return nil, fmt.Errorf("snowflake-cortex: %w", err)
	}

	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, hc: hc, pool: pool, kind: FactoryKind, quotaThreshold: openaicompat.ThresholdFromPolicy(req.RuntimePolicy)}, nil
}

type instance struct {
	cfg            Config
	hc             *http.Client
	pool           *credpool.Pool
	kind           string
	quotaThreshold time.Duration
}

func (i *instance) clientFor(token string) *openaicompat.Client {
	return NewCompatClient(i.cfg, token, i.hc, ProviderHooks(i.cfg))
}

func NewCompatClient(cfg Config, token string, hc *http.Client, hooks openaicompat.RequestHooks) *openaicompat.Client {
	return &openaicompat.Client{
		BaseURL:    cfg.BaseURL(),
		APIKey:     token,
		HTTPClient: hc,
		Transport:  openaicompat.TransportChatAndResponses,
		Hooks:      hooks,
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
	models, err := openaicompat.RunWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) ([]openaicompat.Model, error) {
		return i.clientFor(cred.Secret).ListModels(ctx, 0)
	})
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(models))
	for _, m := range models {
		if !isCodingCapableModel(m.ID) {
			continue
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: i.kind + "/" + m.ID,
			NativeModelID:    m.ID,
			FactoryKind:      i.kind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		})
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}
	return backendplugin.ListModelsResponse{
		Models:          out,
		InventorySource: i.kind,
		FetchedUnixMS:   time.Now().UnixMilli(),
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return openaicompat.ForwardExecute(stream, openaicompat.ExecuteOpts{
		DefaultModel: "default",
		ResolveModel: func(inv backendplugin.Invocation, call aipapi.Call) string {
			return resolveModel(i.kind, inv, call)
		},
		ResolveFlavor: ResolveFlavor,
		Open:          i.open,
	})
}

// open runs one Execute attempt across ordered host-issued credentials: the
// first usable token stays preferred, auth-invalid and quota-exhausted tokens
// advance pre-output, and pool-exhaustion outcomes map to recoverable
// pre-output errors for core routing. Provider-specific failures return
// as-is with existing transport behavior.
func (i *instance) open(ctx context.Context, call aipapi.Call, model string, flavor openaicompat.Flavor) (aipapi.ManagedEventStream, error) {
	return openaicompat.RecoverableRunWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
		return i.clientFor(cred.Secret).Open(ctx, call, model, flavor)
	})
}
