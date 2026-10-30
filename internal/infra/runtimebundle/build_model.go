package runtimebundle

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	modelregistryfile "github.com/aiproxer/aiproxer/internal/infra/modelregistry/filestore"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// modelRuntime holds the model-catalog start, built backends, route prefixes, and
// model-registry runtime produced by [buildModelRuntime].
type modelRuntime struct {
	StartedCatalog  *startedModelCatalog
	Backends        map[string]execbackend.Backend
	RoutePrefixes   []string
	RegistryRuntime *modelregistry.Runtime
	Registry        *modelregistry.Registry
	// BackendKinds is the immutable instance-ID -> configured factory-kind map
	// for this generation, copied from the trusted build inventories before any
	// publication. It is a per-generation value, never a live view over a global
	// or current registry, so a published generation cannot observe a later
	// generation's backend set.
	BackendKinds map[string]string
}

// buildModelRuntime runs catalog -> backends -> registry -> strict-accounting.
// Generation-owned cleanup is registered solely on bctx.Ledger (req Resource Ledger).
func buildModelRuntime(bctx buildContext, upstream *http.Client, profiles *standardplugins.PreparedProviderProfiles) (*modelRuntime, error) {
	cfg, parent, reg := bctx.Cfg, bctx.Parent, bctx.Opts.PluginRegistry
	startedCatalog, err := startModelCatalog(parent, cfg, upstream)
	if err != nil {
		return nil, err
	}
	backendDeps := pluginreg.BackendFactoryDeps{
		Identity: cfg.Identity,
	}
	// Authorized candidate credential capture (requirements 1.1, 2.5): resolve
	// the complete numbered-credential snapshot once per candidate from the
	// candidate policy and the host environment (featurehost.HostEnvironment
	// satisfies the snapshot lookup seam structurally). Factories receive it
	// via deps; serving paths never read the environment. A policy violation,
	// malformed root or namespace collision fails this candidate before any
	// publication, leaving the active generation untouched.
	credSnapshot, err := standardplugins.SnapshotCandidateCredentials(bctx.HostEnv, cfg)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: upstream credentials: %w", err)
	}
	backendDeps.Credentials = credSnapshot

	registerStartedCatalogClosers(bctx.Ledger, startedCatalog)
	backends, inventories, routePrefixes, err := buildBackends(cfg, reg, upstream, backendDeps, bctx.Ledger, profiles)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: %w", err)
	}
	// Post-activation ownership: merge resolved external route prefixes and
	// reject collisions before any GenerationRuntime publication.
	if err := validateCandidateResolvedOwnership(cfg, reg, inventories); err != nil {
		return nil, err
	}
	modelRegistryRuntime, modelRegistry, err := startModelRegistryRuntime(parent, cfg, inventories, bctx.Log, bctx.Ledger)
	if err != nil {
		return nil, fmt.Errorf("runtimebundle: model registry: %w", err)
	}
	if cfg.Accounting.StrictAuthoritative {
		for id, be := range backends {
			if be.FinalizeBilling == nil {
				return nil, fmt.Errorf("runtimebundle: accounting strict_authoritative requires billing finalizer for backend %q", id)
			}
		}
	}
	return &modelRuntime{
		StartedCatalog:  startedCatalog,
		Backends:        backends,
		RoutePrefixes:   routePrefixes,
		RegistryRuntime: modelRegistryRuntime,
		Registry:        modelRegistry,
		BackendKinds:    backendKindInventory(inventories),
	}, nil
}

// backendKindInventory copies the trusted per-instance factory kinds out of the
// build inventories into an immutable generation-owned map. Rebuilding the map
// per generation keeps published generations isolated from later reloads.
func backendKindInventory(inventories []modelregistry.BackendInventory) map[string]string {
	if len(inventories) == 0 {
		return nil
	}
	kinds := make(map[string]string, len(inventories))
	for _, inv := range inventories {
		id := strings.TrimSpace(inv.BackendID)
		if id == "" {
			continue
		}
		kinds[id] = inv.Kind
	}
	if len(kinds) == 0 {
		return nil
	}
	return kinds
}

// registerStartedCatalogClosers registers close before refresh quiesce so every
// reverse-order path cancels and waits for refresh work before closing the catalog.
func registerStartedCatalogClosers(ledger *ResourceLedger, started *startedModelCatalog) {
	if started == nil || ledger == nil {
		return
	}
	for i, c := range started.closers {
		ledger.AddClose(fmt.Sprintf("catalog-%d", i), PhaseClose, c)
	}
	for i, c := range started.quiesceClosers {
		ledger.AddClose(fmt.Sprintf("catalog-refresh-%d", i), PhaseQuiesce, c)
	}
}

func buildBackends(
	cfg *config.Config,
	reg *pluginreg.Registry,
	upstream *http.Client,
	backendDeps pluginreg.BackendFactoryDeps,
	ledger *ResourceLedger,
	profiles *standardplugins.PreparedProviderProfiles,
) (map[string]execbackend.Backend, []modelregistry.BackendInventory, []string, error) {
	backends := make(map[string]execbackend.Backend, len(cfg.Plugins.Backends))
	inventories := make([]modelregistry.BackendInventory, 0, len(cfg.Plugins.Backends))
	rawPrefixes := make([]string, 0, len(cfg.Plugins.Backends))
	var nilLedgerRollback []func() error
	if err := resolveEnabledBackendFactories(cfg, reg); err != nil {
		return nil, nil, nil, err
	}
	modelInventoryFetchTimeout := cfg.ModelInventory.FetchTimeoutDuration()
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		fid := p.FactoryID()
		iid := p.InstanceID()
		res, err := profiles.BuildBackendWithLifecycle(reg, fid, iid, p.Config, upstream, backendDeps)
		if err != nil {
			buildErr := fmt.Errorf("backend instance %s (factory %s): %w", iid, fid, err)
			if ledger == nil {
				return nil, nil, nil, withDisposedClosers(buildErr, nilLedgerRollback)
			}
			return nil, nil, nil, buildErr
		}
		be := res.Backend
		if res.Cleanup != nil {
			cleanup := res.Cleanup
			// Once-safe + transport-gone normalization before ledger ownership so
			// generation Close/Rollback share one cleanup owner with assembly.
			safe := func() error {
				return normalizePluginCleanupErr(cleanup())
			}
			if ledger != nil {
				ledger.AddClose("plugin-cleanup:"+iid, PhaseClose, safe)
			} else {
				nilLedgerRollback = RegisterPluginBuildCleanup(nilLedgerRollback, cleanup)
			}
		}
		hooks := optionalBackendHooksFromBackend(be)
		inst := WrapBackendInstance(be, hooks)
		wrapped := inst.AsBackend()
		wrapped.Start = nil
		wrapped.Stop = nil
		wrapped.CleanupIdleTransports = nil
		wrapped.PreflightCapability = nil
		if hooks.PreflightCapability != nil {
			wrapped.PreflightCapability = func(ctx context.Context) (execbackend.CapabilityPreflight, error) {
				res, err := inst.PreflightCapability(ctx)
				return execbackend.CapabilityPreflight{
					Ready: res.Ready, Billable: res.Billable, Description: res.Description,
				}, err
			}
		}

		owns := be.Close != nil || hooks.Start != nil || hooks.Stop != nil || hooks.CleanupIdleTransports != nil
		if owns {
			if ledger != nil {
				ledger.AddClose("backend:"+iid, PhaseClose, inst.Close)
				if hooks.Start != nil {
					ledger.AddAction("backend:"+iid+":start", PhasePrepare, inst.Start, nil)
				}
				wrapped.Close = nil // ledger owns cleanup
			} else {
				wrapped.Close = inst.Close
				nilLedgerRollback = append(nilLedgerRollback, inst.Close)
			}
		}
		backends[iid] = wrapped
		rawPrefixes = append(rawPrefixes, be.BackendPrefixes...)
		inventories = append(inventories, modelregistry.BackendInventory{
			BackendID: iid, Kind: fid, BackendPrefixes: be.BackendPrefixes,
			Provider: be.ModelInventory, FetchTimeout: modelInventoryFetchTimeout,
		})
	}
	return backends, inventories, routing.FilterRoutePrefixes(rawPrefixes), nil
}

func optionalBackendHooksFromBackend(be execbackend.Backend) OptionalBackendHooks {
	hooks := OptionalBackendHooks{
		Start: be.Start, Stop: be.Stop, CleanupIdleTransports: be.CleanupIdleTransports,
	}
	if be.PreflightCapability != nil {
		preflight := be.PreflightCapability
		hooks.PreflightCapability = func(ctx context.Context) (BackendPreflightResult, error) {
			res, err := preflight(ctx)
			if err != nil {
				return BackendPreflightResult{}, err
			}
			return BackendPreflightResult{
				Ready: res.Ready, Billable: res.Billable, Description: res.Description,
			}, nil
		}
	}
	return hooks
}

func startModelRegistryRuntime(
	parent context.Context,
	cfg *config.Config,
	inventories []modelregistry.BackendInventory,
	log *slog.Logger,
	ledger *ResourceLedger,
) (*modelregistry.Runtime, *modelregistry.Registry, error) {
	var cache modelregistry.Cache
	if path := strings.TrimSpace(cfg.ModelInventory.CachePath); path != "" {
		cache = modelregistryfile.New(path)
	}
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: inventories, Cache: cache, Log: log,
	})
	if err := rt.Start(parent); err != nil {
		return nil, nil, err
	}
	reg := rt.ActiveRegistry()
	if reg == nil {
		return nil, nil, modelregistry.ErrSnapshotUnavailable
	}
	if cfg.ModelInventory.EffectiveRefreshEnabled() && hasRefreshableModelInventory(inventories) {
		interval := cfg.ModelInventory.RefreshIntervalDuration()
		if interval > 0 {
			startOwnedLoop(parent, ledger, "model-registry-refresh", PhaseQuiesce, func(ctx context.Context) {
				runModelRegistryRefreshLoop(ctx, rt, interval)
			})
		}
	}
	return rt, reg, nil
}

func hasRefreshableModelInventory(inventories []modelregistry.BackendInventory) bool {
	for _, inv := range inventories {
		if inv.Provider == nil {
			continue
		}
		if static, ok := inv.Provider.(modelinventory.StaticInventory); ok && static.StaticInventory() {
			continue
		}
		return true
	}
	return false
}

// resolveEnabledBackendFactories fails closed when an enabled row's factory is
// absent from the essential∪discovered registry before any Activate/Build runs.
func resolveEnabledBackendFactories(cfg *config.Config, reg *pluginreg.Registry) error {
	if cfg == nil {
		return fmt.Errorf("runtimebundle: nil config")
	}
	enabled := make([]string, 0, len(cfg.Plugins.Backends))
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		enabled = append(enabled, p.FactoryID())
	}
	if err := pluginreg.ResolveEnabledAgainstRegistry(enabled, reg); err != nil {
		return fmt.Errorf("runtimebundle: %w", err)
	}
	return nil
}
