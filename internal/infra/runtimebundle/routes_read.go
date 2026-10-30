package runtimebundle

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func routesSnapshotFromCompatible(
	cfg *config.Config,
	reg *pluginreg.Registry,
	compatible []diag.CompatibleBackendRow,
	discovered config.DiscoveredModelForBackend,
) (RoutesSnapshot, error) {
	if cfg == nil {
		return RoutesSnapshot{}, fmt.Errorf("runtimebundle: nil config")
	}
	if reg == nil {
		return RoutesSnapshot{}, fmt.Errorf("runtimebundle: nil registry")
	}
	resolution, err := routing.ResolveDefaultRouteWithInventory(cfg, standardplugins.DefaultWireModel, discovered)
	if err != nil {
		return RoutesSnapshot{}, fmt.Errorf("runtimebundle: %w", err)
	}
	out := RoutesSnapshot{
		EffectiveDefaultRoute: resolution.Selector,
		// The frontends have no route to fall back to. The operator is told how
		// to name one instead of reading an empty route as a working default.
		EffectiveDefaultRouteNote: resolution.Warning,
		// The source and the model tell the operator which route is in effect
		// and where it came from: the configuration, the canonical default
		// model of the backend kind, or the model the instance discovered.
		EffectiveDefaultRouteSource: string(resolution.Source),
		EffectiveDefaultRouteModel:  resolution.Model,
		Backends:                    make([]RouteBackend, 0, len(cfg.Plugins.Backends)),
		ModelAliases:                make([]RouteAlias, 0, len(cfg.ModelAliases)),
		CredentialPosture:           credentialPostureFromBackends(cfg),
	}
	for _, b := range cfg.Plugins.Backends {
		out.Backends = append(out.Backends, RouteBackend{
			ID:      b.InstanceID(),
			Kind:    b.FactoryID(),
			Enabled: b.Enabled,
		})
	}
	for _, a := range cfg.ModelAliases {
		out.ModelAliases = append(out.ModelAliases, RouteAlias{
			Pattern:     a.Pattern,
			Replacement: a.Replacement,
		})
	}
	out.CompatibleBackends = compatible
	projection := diag.ProjectInventoryDiagnostics(cfg, out.CompatibleBackends, standardplugins.StandardDiagnosticProjectors())
	out.InstanceDiagnostics = projection.InstanceDiagnostics
	out.OpenResponsesFrontends = projection.OpenResponsesFrontends
	return out, nil
}

func credentialPostureFromBackends(cfg *config.Config) string {
	hasEnabled := false
	for _, b := range cfg.Plugins.Backends {
		if !b.Enabled {
			continue
		}
		hasEnabled = true
		k := strings.ToLower(strings.TrimSpace(b.FactoryID()))
		if k != "local-stub" {
			return "live_provider"
		}
	}
	if !hasEnabled {
		return "no_enabled_backends"
	}
	return "all_local_stub"
}
