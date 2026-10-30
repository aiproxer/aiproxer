package pluginreg

import (
	"fmt"
	"strings"
)

// ResolveEnabledFactoryIDs ensures every enabled factory id is present in the
// union of essential and discovered kinds before runtime construction.
// Missing kinds fail closed.
func ResolveEnabledFactoryIDs(enabledFactoryIDs, availableKinds []string) error {
	avail := make(map[string]struct{}, len(availableKinds))
	for _, k := range availableKinds {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		avail[k] = struct{}{}
	}
	for _, id := range enabledFactoryIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := avail[id]; !ok {
			return fmt.Errorf("pluginreg: enabled backend factory %q is not in essential or discovered kinds", id)
		}
	}
	return nil
}

// EnabledFactoryIDsFromRegistry reports whether each enabled factory is registered.
// Prefer this after InstallDiscoveredExports so the registry is the single source of truth.
func ResolveEnabledAgainstRegistry(enabledFactoryIDs []string, reg *Registry) error {
	if reg == nil {
		return fmt.Errorf("pluginreg: ResolveEnabledAgainstRegistry: nil registry")
	}
	for _, id := range enabledFactoryIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !reg.HasBackend(id) {
			return fmt.Errorf("pluginreg: enabled backend factory %q is not in essential or discovered kinds", id)
		}
	}
	return nil
}
