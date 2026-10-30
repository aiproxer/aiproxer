package runtimebundle

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

// validateCandidateManifestOwnership runs the pre-activation ownership stage
// against the generation-scoped registry (built-ins + discovered kinds) and
// enabled generic rows. Check-config / compile share this seam; it never
// activates plugin processes.
func validateCandidateManifestOwnership(cfg *config.Config, reg *pluginreg.Registry) error {
	if cfg == nil {
		return fmt.Errorf("runtimebundle: nil config")
	}
	if err := standardplugins.ValidateCompatibleManifestOwnership(cfg.Plugins.Backends, reg); err != nil {
		return fmt.Errorf("runtimebundle: %w", err)
	}
	return nil
}

// validateCandidateResolvedOwnership merges advertised external route prefixes
// after activation and rejects collisions before GenerationRuntime publication.
func validateCandidateResolvedOwnership(
	cfg *config.Config,
	reg *pluginreg.Registry,
	inventories []modelregistry.BackendInventory,
) error {
	if cfg == nil {
		return fmt.Errorf("runtimebundle: nil config")
	}
	generics, err := standardplugins.CollectEnabledCompatibleOwners(cfg.Plugins.Backends)
	if err != nil {
		return fmt.Errorf("runtimebundle: %w", err)
	}
	err = pluginreg.ValidateResolvedOwnership(pluginreg.ResolvedOwnershipInput{
		Base: pluginreg.ManifestOwnershipInput{
			BuiltIns:       standardplugins.CollectBuiltInBackendOwners(reg),
			GenericEnabled: generics,
			ManifestKinds:  standardplugins.CollectManifestKindOwners(reg),
		},
		ResolvedPrefixes: collectResolvedExternalOwners(reg, inventories),
	})
	if err != nil {
		return fmt.Errorf("runtimebundle: %w", err)
	}
	return nil
}

func collectResolvedExternalOwners(
	reg *pluginreg.Registry,
	inventories []modelregistry.BackendInventory,
) []pluginreg.BackendOwner {
	if reg == nil || len(inventories) == 0 {
		return nil
	}
	out := make([]pluginreg.BackendOwner, 0, len(inventories))
	for _, inv := range inventories {
		kind := strings.TrimSpace(inv.Kind)
		if kind == "" || !reg.IsDiscoveredBackend(kind) {
			continue
		}
		inst := strings.TrimSpace(inv.BackendID)
		for _, prefix := range inv.BackendPrefixes {
			prefix = strings.TrimSpace(prefix)
			if prefix == "" {
				continue
			}
			// Default advertised prefix equals the factory kind and is already
			// reserved by the manifest-available stage.
			if prefix == kind {
				continue
			}
			out = append(out, pluginreg.BackendOwner{
				Origin:      pluginreg.OriginExternalResolved,
				FactoryKind: kind,
				InstanceID:  inst,
				Prefix:      prefix,
				SourceID:    "resolved:" + kind + "/" + inst,
			})
		}
	}
	return out
}
