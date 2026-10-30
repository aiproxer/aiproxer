package standardplugins

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

// CollectBuiltInBackendOwners returns generation-scoped owners for non-compatible
// builtin factory kinds. When reg is nil, owners are derived from the essential
// standard bundle IDs (check-config / pre-registry structural path).
func CollectBuiltInBackendOwners(reg *pluginreg.Registry) []pluginreg.BackendOwner {
	var ids []string
	if reg != nil {
		ids = reg.BuiltinBackendFactoryIDs()
	} else {
		for _, entry := range StandardBackendBundle(UpstreamAPIKeys{}).Backends {
			ids = append(ids, entry.ID)
		}
	}
	out := make([]pluginreg.BackendOwner, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || IsCustomCompatibleBackendKind(id) {
			continue
		}
		out = append(out, pluginreg.BackendOwner{
			Origin:      pluginreg.OriginBuiltIn,
			FactoryKind: id,
			Prefix:      id,
			SourceID:    "essential:" + id,
		})
	}
	return out
}

// CollectManifestKindOwners returns owners for discovered external factory kinds
// available from the registry catalog without process activation.
func CollectManifestKindOwners(reg *pluginreg.Registry) []pluginreg.BackendOwner {
	if reg == nil {
		return nil
	}
	ids := reg.DiscoveredBackendIDs()
	out := make([]pluginreg.BackendOwner, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		out = append(out, pluginreg.BackendOwner{
			Origin:      pluginreg.OriginExternalManifest,
			FactoryKind: id,
			SourceID:    "manifest:" + id,
		})
	}
	return out
}

// CollectEnabledCompatibleOwners extracts enabled generic-compatible owners and
// validates backend_prefix syntax. Disabled rows are omitted (enabled-row policy).
func CollectEnabledCompatibleOwners(rows []config.PluginConfig) ([]pluginreg.BackendOwner, error) {
	out := make([]pluginreg.BackendOwner, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		kind := row.FactoryID()
		if !IsCustomCompatibleBackendKind(kind) {
			continue
		}
		prefix, err := DecodeCompatibleBackendPrefix(kind, row)
		if err != nil {
			return nil, err
		}
		prefix = strings.TrimSpace(prefix)
		if err := pluginreg.ValidatePrefixSyntax(prefix); err != nil {
			return nil, err
		}
		inst := row.InstanceID()
		out = append(out, pluginreg.BackendOwner{
			Origin:      pluginreg.OriginBuiltInCompatible,
			FactoryKind: kind,
			InstanceID:  inst,
			Prefix:      prefix,
			SourceID:    "plugins.backends." + inst,
		})
	}
	return out, nil
}

// ValidateCompatibleManifestOwnership validates the manifest-available ownership
// stage: built-ins, enabled generic prefixes, and (when reg is non-nil)
// discovered external factory kinds. It never activates plugin processes.
func ValidateCompatibleManifestOwnership(rows []config.PluginConfig, reg *pluginreg.Registry) error {
	generics, err := CollectEnabledCompatibleOwners(rows)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCustomBackendPrefix, err)
	}
	in := pluginreg.ManifestOwnershipInput{
		BuiltIns:       CollectBuiltInBackendOwners(reg),
		GenericEnabled: generics,
		ManifestKinds:  CollectManifestKindOwners(reg),
	}
	if err := pluginreg.ValidateManifestOwnership(in); err != nil {
		return fmt.Errorf("%w: %w", ErrCustomBackendPrefix, err)
	}
	return nil
}
