package runtimebundle

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// inventoryLiveSnapshot holds compiled model-runtime state for operator projection.
type inventoryLiveSnapshot struct {
	config   *config.Config
	backends map[string]execbackend.Backend
	runtime  *modelregistry.Runtime
	registry *modelregistry.Registry
	ledger   *ResourceLedger
}

func (s *inventoryLiveSnapshot) Close(ctx context.Context) error {
	if s == nil || s.ledger == nil {
		return nil
	}
	return s.ledger.Close(ctx)
}

func (s *inventoryLiveSnapshot) compatibleInputs() standardplugins.CompatibleLiveInputs {
	if s == nil {
		return standardplugins.CompatibleLiveInputs{}
	}
	return standardplugins.CompatibleLiveInputs{
		Backends: s.backends,
		Registry: s.registry,
		Runtime:  s.runtime,
	}
}

func loadInventoryLiveSnapshot(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry) (*inventoryLiveSnapshot, error) {
	if ctx == nil {
		return nil, fmt.Errorf("runtimebundle: nil context")
	}
	if cfg == nil || reg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config or registry")
	}
	profiles, err := standardplugins.PrepareProviderProfiles(cfg)
	if err != nil {
		return nil, err
	}
	ledger := NewResourceLedger()
	bctx := buildContext{
		Cfg:    profiles.Config,
		Log:    slog.Default(),
		Parent: ctx,
		Opts: &BuildOptions{
			PluginRegistry: reg,
			Startup:        StartupOptions{StartupContext: ctx},
			Infra:          InfraOptions{HTTPClient: httpclient.Standard()},
		},
		Ledger: ledger,
	}
	model, err := buildModelRuntime(bctx, bctx.Opts.Infra.HTTPClient, profiles)
	if err != nil {
		_ = ledger.Rollback(ctx)
		return nil, err
	}
	return &inventoryLiveSnapshot{
		config:   profiles.Config,
		backends: model.Backends,
		runtime:  model.RegistryRuntime,
		registry: model.Registry,
		ledger:   ledger,
	}, nil
}

func compatibleProjector(cfg *config.Config, live *inventoryLiveSnapshot) diag.CompatibleBackendProjector {
	projectionConfig := cfg
	if live != nil && live.config != nil {
		projectionConfig = live.config
	} else if prepared, err := standardplugins.ExpandProviderProfileRows(cfg); err == nil {
		projectionConfig = prepared.Config
	}
	if live == nil || live.runtime == nil {
		return func(*config.Config) []diag.CompatibleBackendRow {
			return standardplugins.ProjectCompatibleBackendRows(projectionConfig)
		}
	}
	inputs := live.compatibleInputs()
	return func(*config.Config) []diag.CompatibleBackendRow {
		return standardplugins.ProjectCompatibleBackendRowsLive(projectionConfig, inputs)
	}
}

func inventoryProjectionForOperator(cfg *config.Config, live *inventoryLiveSnapshot) *diag.InventoryProjection {
	return inventoryProjectionForOperatorWithProjector(cfg, compatibleProjector(cfg, live))
}

func inventoryProjectionForOperatorWithProjector(cfg *config.Config, projector diag.CompatibleBackendProjector) *diag.InventoryProjection {
	compatible := projector(cfg)
	projection := diag.ProjectInventoryDiagnostics(cfg, compatible, standardplugins.StandardDiagnosticProjectors())
	return &projection
}

func inventorySnapshotForOperator(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry, registrations []aipsdk.Registration, live *inventoryLiveSnapshot) (diag.InventorySnapshot, error) {
	return inventorySnapshotForOperatorWithProjector(ctx, cfg, reg, registrations, compatibleProjector(cfg, live))
}

func inventorySnapshotForOperatorWithProjector(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry, registrations []aipsdk.Registration, projector diag.CompatibleBackendProjector) (diag.InventorySnapshot, error) {
	return inventorySnapshotForOperatorWithProjection(ctx, cfg, reg, registrations, inventoryProjectionForOperatorWithProjector(cfg, projector))
}

func inventorySnapshotForOperatorWithProjection(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry, registrations []aipsdk.Registration, projection *diag.InventoryProjection) (diag.InventorySnapshot, error) {
	var featureMetadata []diag.InventoryFeatureMetadata
	if cfg != nil && reg != nil {
		if len(registrations) == 0 {
			registrations = config.RegistrationsFromConfig(cfg)
		}
		merged, err := featurebundle.MergeFeatureSurfacesWithHost(reg, registrations, featurebundle.HostContributions{})
		if err != nil {
			return diag.InventorySnapshot{}, fmt.Errorf("runtimebundle: feature inventory metadata: %w", err)
		}
		featureMetadata = featurehost.InventoryMetadataFromMergeSurface(merged)
	}
	featureMetadata = featurehost.InventoryMetadataForConfig(cfg, featureMetadata, nil)
	return diag.InventorySnapshotForConfig(ctx, cfg, &diag.InventoryExtras{
		Reg:             reg,
		Registrations:   registrations,
		FeatureMetadata: featureMetadata,
		Precomputed:     projection,
	})
}

func routesSnapshotFrom(cfg *config.Config, reg *pluginreg.Registry, live *inventoryLiveSnapshot) (RoutesSnapshot, error) {
	var discovered config.DiscoveredModelForBackend
	if live != nil {
		discovered = discoveredModelResolver(live.runtime)
	}
	return routesSnapshotFromCompatible(cfg, reg, compatibleProjector(cfg, live)(cfg), discovered)
}

func tryLoadInventoryLiveSnapshot(ctx context.Context, cfg *config.Config, reg *pluginreg.Registry) (*inventoryLiveSnapshot, error) {
	snap, err := loadInventoryLiveSnapshot(ctx, cfg, reg)
	if err != nil {
		return nil, err
	}
	return snap, nil
}
