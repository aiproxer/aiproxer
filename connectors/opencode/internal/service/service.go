package service

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog"
	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog/vendor"
	"github.com/aiproxer/aiproxer/connectors/opencode/internal/upstream"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1, ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake, PluginID: PluginID, Version: "0.1.0", BuildID: "localdev",
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureCancellationHandshake},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind: FactoryKindGo, DisplayName: "OpenCode Go", Description: "OpenCode Go multi-vendor backend",
			CredentialMode: backendplugin.CredentialModeStatic, AccessScope: backendplugin.AccessScopeAny,
			RoutePrefixes: []string{FactoryKindGo}, SupportsDynamicInventory: true,
			ProcessSharing: backendplugin.ProcessSharingPerInstance,
			StaticCapabilities: backendplugin.CapabilitySummary{
				// OpenCode's provider adapters currently serialize text-only
				// requests; media is not a canonical capability until a lossless
				// mapping is available for every advertised route.
				Streaming: true, Tools: true, ParallelToolCalls: true,
			},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{
				Cancellation: true, BidirectionalStream: true,
			},
		}, {
			Kind: FactoryKindZen, DisplayName: "OpenCode Zen", Description: "OpenCode Zen multi-vendor backend",
			CredentialMode: backendplugin.CredentialModeStatic, AccessScope: backendplugin.AccessScopeAny,
			RoutePrefixes: []string{FactoryKindZen}, SupportsDynamicInventory: true,
			ProcessSharing: backendplugin.ProcessSharingPerInstance,
			StaticCapabilities: backendplugin.CapabilitySummary{
				Streaming: true, Tools: true, ParallelToolCalls: true,
			},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{
				Cancellation: true, BidirectionalStream: true,
			},
		}},
	}, nil
}

func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	kind := req.FactoryKind
	if kind == "" {
		kind = FactoryKindGo
	}
	if kind != FactoryKindGo && kind != FactoryKindZen {
		return nil, fmt.Errorf("opencode: unexpected factory kind %q", kind)
	}
	cfg, err := ParseConfigYAML(kind, req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	// Host-issued ordered slots are the sole credential source for both
	// exports. YAML api_key/auth_json_path, ${VAR} interpolation,
	// connector-side environment lookup and auth.json discovery are removed.
	keys, err := ResolveAPIKeys(kind, req.Secrets)
	if err != nil {
		return nil, err
	}
	pool, err := openaicompat.NewAPIKeyPool(keys)
	if err != nil {
		return nil, fmt.Errorf("opencode: %w", err)
	}
	hc, err := cfg.HTTPClient()
	if err != nil {
		return nil, err
	}
	backendKind := catalogBackendKind(kind)
	resolver := vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{}, true)
	source := catalog.NewModelSource(backendKind, catalog.ModelLoaderConfig{
		BaseURL: cfg.BaseURL, Kind: backendKind, Credentials: keys, HTTPClient: hc,
	}, cfg.Models, resolver)
	return &instance{
		cfg: cfg, hc: hc, kind: kind, backendKind: backendKind, pool: pool,
		source: source, router: upstream.NewRouter(backendKind, cfg.BaseURL, keys[0], hc),
		quotaThreshold: openaicompat.ThresholdFromPolicy(req.RuntimePolicy),
	}, nil
}

func catalogBackendKind(kind string) catalog.BackendKind {
	switch kind {
	case FactoryKindZen:
		return catalog.BackendZen
	default:
		return catalog.BackendGo
	}
}

type instance struct {
	cfg            Config
	hc             *http.Client
	kind           string
	backendKind    catalog.BackendKind
	pool           *credpool.Pool
	source         *catalog.ModelSource
	router         *upstream.Router
	quotaThreshold time.Duration
}

func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{
			Streaming: true, Tools: true, ParallelToolCalls: true,
		},
		TransportCapabilities: backendplugin.TransportCapabilitySummary{
			Cancellation: true, BidirectionalStream: true,
		},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{i.kind},
		EvidenceSource:           i.kind,
		ProfileVersion:           "1",
	}, nil
}

func (i *instance) Close(context.Context) error { return nil }
