package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/commandcode-anthropic/internal/anthropic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake, PluginID: PluginID, Version: "0.1.0", BuildID: "localdev",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind: FactoryKind, DisplayName: "Command Code (Anthropic)", Description: "Command Code Anthropic Messages backend",
			CredentialMode: backendplugin.CredentialModeStatic, AccessScope: backendplugin.AccessScopeAny,
			RoutePrefixes: []string{FactoryKind}, SupportsDynamicInventory: true,
			ProcessSharing:        backendplugin.ProcessSharingPerInstance,
			StaticCapabilities:    backendplugin.CapabilitySummary{Streaming: true},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("commandcode-anthropic: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	// Host-issued ordered slots are the sole credential source. The legacy
	// ${VAR} interpolation, connector-side environment lookup and the shared
	// COMMANDCODE_API_KEY fallback are removed: this export admits only
	// COMMANDCODE_ANTHROPIC_API_KEY[_N] via host Configure secrets.
	keys, err := decodeAPIKeys(req.Secrets)
	if err != nil {
		return nil, fmt.Errorf("commandcode-anthropic: %w (want %s[_N] via host Configure secrets)", err, EnvAPIKeyRoot)
	}
	pool, err := credpool.New(credentialSet(keys))
	if err != nil {
		return nil, fmt.Errorf("commandcode-anthropic: %w", err)
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, hc: hc, pool: pool, kind: FactoryKind, accountingV1: backendplugin.AccountingEvidenceNegotiated(req.Negotiation), quotaThreshold: openaicompat.ThresholdFromPolicy(req.RuntimePolicy)}, nil
}

type instance struct {
	cfg            Config
	hc             *http.Client
	pool           *credpool.Pool
	kind           string
	accountingV1   bool
	quotaThreshold time.Duration
}

// credentialSet lifts ordered slot secrets into pool credentials with stable
// priority order. Blanks cannot occur (the slot decoder rejects them); the
// filter is defense in depth.
func credentialSet(keys []string) []credpool.Credential {
	creds := make([]credpool.Credential, 0, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		creds = append(creds, credpool.Credential{Secret: key})
	}
	return creds
}

func (i *instance) clientFor(apiKey string) *anthropic.Client {
	client := anthropic.NewClient(i.cfg.BaseURL, apiKey, i.hc)
	client.SetAccountingEvidenceEnabled(i.accountingV1)
	return client
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true, BidirectionalStream: true},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{i.kind}, EvidenceSource: i.kind, ProfileVersion: "1",
	}, nil
}

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	models, err := runWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) ([]anthropic.Model, error) {
		return i.clientFor(cred.Secret).ListModels(ctx, limit)
	})
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(models))
	for _, m := range models {
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: i.kind + "/" + m.ID, NativeModelID: m.ID, FactoryKind: i.kind,
			Capabilities: backendplugin.CapabilitySummary{Streaming: true},
		})
	}
	return backendplugin.ListModelsResponse{
		Models: out, InventorySource: i.kind, FetchedUnixMS: time.Now().UnixMilli(),
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		model := strings.TrimPrefix(strings.TrimSpace(inv.CanonicalModelID), i.kind+"/")
		if model == "" {
			model = "claude-3-5-sonnet-20241022"
		}
		return recoverableRunWithKeysThreshold(ctx, i.pool, i.quotaThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
			return i.clientFor(cred.Secret).Open(ctx, call, model)
		})
	})
}
